# UDP / QUIC 数据通道

> 当前统一版已合入 main，同一二进制通过参数选择功能，不再维护独立 UDP／自适应分支。本文保留原实现背景；当前用法以 [README](../README.md) 和 [示例](EXAMPLES.zh-CN.md) 为准。

当前候选增加默认4个客户端socket分片、8952协商负载上限与64KiB GSO上限；依赖包含记录在案的本地补丁。见 [最新设计](QUIC-TUNING.zh-CN.md) 和 [完整消融](QUIC-TUNING-RESULTS.zh-CN.md)。以下保留基础通道说明。

[English](QUIC.en.md) · [TCP 正式版](https://github.com/junli-nv/swarmcopy/releases/tag/v0.2.0)

本分支增加可选 `--transport quic`，用 HTTP/3 / QUIC 的可靠字节流传输分块，底层使用 UDP。TCP 仍是默认值，SHA-256 流水线和 256 块前缀窗口默认开启，自适应并发默认关闭。不是 IBM FASP 的实现，也不承诺 UDP 一定更快。

## 使用

~~~bash
./swarmcopy distribute --hosts 'node[001-005]' \
  --source /shared/nemo.sqsh --dest /raid/images --bin-dir ./dist \
  --transport quic
./swarmcopy jobs
./swarmcopy watch --job <ID>
./swarmcopy stop --job <ID>
./swarmcopy resume --job <ID>
~~~

新任务把 transport 保存到 job.json，远程部署与 resume 沿用它，旧任务的空值表示 TCP。不能在同一任务内混合 TCP 和 QUIC 数据节点；使用本分支的同版本二进制部署全部参与者。需要退回 TCP 时创建新任务，不改写运行中的任务记录。

同一个端口号同时用于 **TCP 状态/控制**和 **UDP 数据**，默认 19700。控制端需 SSH 和 TCP 可达；计算节点之间还需双向 UDP 可达。仍由 DNS、操作系统路由决定出站网卡，监听所有地址不代表聚合多网卡或原生 RDMA。程序不修改路由、防火墙、MTU、sysctl，也不关闭拥塞控制。

QUIC 模式的 /chunk 请求必须真实使用 HTTP/3；TCP /chunk 会拒绝，不会静默回退。状态查询与监控仍走 TCP，以便直接比较原有监控逻辑。UDP 不通时请求失败并在任务生命周期内重试；watch 仍可看到状态，stop 可停止，部分文件可以续传。

## 可靠性与任务身份

使用固定版本 quic-go v0.65.0：QUIC 负责可靠传输、丢包恢复、拥塞控制、流控制与 TLS 1.3。应用层保留分块 SHA-256、实际写入、完整文件 SHA-256、fsync、原子发布和中断重验。没有用不可靠 UDP datagram 直接替代文件字节流，没有删除任何校验步骤。

TLS 使用由高熵随机 job token 派生的共享任务 Ed25519 身份，不需要证书文件或外部 CA。双方都必须提供证书，并固定校验其公钥、有效期和签名；不是无条件跳过身份验证。身份代表任务，不代表单独某台主机；持有 token 的参与者被视为互信。手工 token 应使用足够强的随机值，不能使用可猜密码。

**整个系统仍用于受控互信网络。** TCP 状态接口仍是明文并携带原有 bearer token；QUIC 数据加密不能把整体系统变成适用于不可信网络的安全分发服务。不得把数据通道 TLS 描述为全系统端到端保密。

## 实现与资源

- quic.go：固定任务身份、UDP listener、HTTP/3 client、连接关闭。
- main.go / transfer.go：单独的数据 client；发现与状态继续使用原 TCP client。
- cluster.go / parallel.go / agent.go：保存并部署 transport；旧配置默认 TCP。
- Performance.Transport / QUICChunkRequests：报告传输模式及收到真实 HTTP/3 分块响应的请求数，包含 429 等非成功响应，不等于成功块数或线速字节数。
- quic_test.go：真实 UDP 双向丢包代理、摘要/字节一致、跨任务拒绝、bearer 认证、取消、无 TCP 回退。
- scripts/benchmark-quic.py：正式 TCP / 同候选 TCP / 同候选 QUIC 的配对冷缓存对照。

接收窗口上限按连接设置：流初始/最大 2/32 MiB，连接初始/最大 8/128 MiB；不是每节点总内存硬上限，多节点连接会叠加。每个下载 worker 仍有原分块缓冲。最多 64 条入站双向流，握手空闲超时 3 秒，QUIC 空闲超时 45 秒；不启用 0-RTT 或不可靠 datagram。quic-go 可自行使用支持的 GSO/批处理；未做自定义 FASP 速率算法。

构建需要 Go 1.26+（本次工具链 1.27.4）和 go.mod/go.sum 固定的纯 Go 依赖，运行不增加外部依赖；仍输出 CGO_ENABLED=0 的 x86_64/aarch64 单体程序。依赖许可证随 THIRD_PARTY_NOTICES.txt 发布。ARM64 仅交叉构建。

## 测试与测量边界

~~~bash
go test -race ./...
go vet ./...
go build -race -o run/swarmcopy-race .
SWARMCOPY_BINARY="$PWD/run/swarmcopy-race" \
SWARMCOPY_TEST_FLAGS='--transport quic --verify-window-chunks 4 --endgame --adaptive --min-workers 2 --max-workers 8' \
python3 scripts/integration.py
~~~

真实两节点 SSH 已分别通过 TCP/QUIC 部署、完整校验、默认窗口与实际 HTTP/3 请求统计检查。两种传输使用相同的工作线程、上传槽位与完整校验，正式大文件试验使用全新目录，计算节点逐次 sync/drop_caches，正常重传计入连续时间。

这是 HTTP/3+QUIC+TLS 与明文 HTTP/TCP 的整体实现对照，不能分离“UDP 本身”的因果效应。加密、用户态协议、包处理、GSO、UDP socket 缓冲、低 RTT 下 TCP 的成熟卸载都可能影响结果；没有模拟 WAN 或生产网络丢包。单元测试的人为丢包不等于大集群已测试相同丢包率。

参考：[quic-go 客户端](https://quic-go.net/docs/http3/client/)、[服务端](https://quic-go.net/docs/http3/server/)、[GSO 与 UDP 缓冲说明](https://quic-go.net/docs/quic/optimizations/)。


[45 节点完整测试结果](QUIC-RESULTS.zh-CN.md)
