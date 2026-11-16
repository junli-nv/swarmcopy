# QUIC 接入审查与调优

> 当前统一版已合入 main，同一二进制通过参数选择功能，不再维护独立 UDP／自适应分支。本文保留原实现背景；当前用法以 [README](../README.md) 和 [示例](EXAMPLES.zh-CN.md) 为准。

[English](QUIC-TUNING.en.md) · [原始试验](QUIC-RESULTS.zh-CN.md) · [UDP 基础说明](QUIC.zh-CN.md)

正式稳定版仍为 v0.2.0：默认 TCP，流水线完整校验与 256 块窗口开启，自适应并发关闭。本候选继续保持这些选择。只有明确选择 `--transport quic` 才启用 UDP。

## 找到的问题与证据

第一版把所有来源交给同一个 http3.Transport。固定版本 quic-go 的默认 Transport 为不同目标共用一个 UDP socket 与接收循环。在高包率、多来源分发下，这会形成接收侧的并行度限制。新实现按来源地址稳定映射到有限个 Transport，不为每个块重复建立连接。`--quic-sockets` 范围 1..32，当前候选默认 4，是静态接收通路分片，不是自适应增加下载 worker。没有流量的分片可保持未创建 socket，指标报告配置数量而非实际打开数量。

5 节点 8 GiB 的 CPU 采样用于定位，不能当作正式测速。某接收端样本的系统调用约 23%，AES 解密约 4%，另有明显调度与收包工作；这些百分比不代表全部节点，也不能把累计调用栈比例相加。原始单 socket 与多 socket 的 45 节点冷缓存消融单独保留。

第二个限制是上游包缓冲/协商上限固定在 1452 字节，不能完整利用本集群配置为 MTU 9000 的网络。候选通过窄范围依赖补丁增加可配置上限，初始包仍为 1280（原有错误处理可回退到 1200），只在对端与路径探测允许时增大发送包。没有修改系统 MTU 或跳过路径验证。

普通 sysctl 的 rmem_max 为 256 KiB，不能等同于实际 socket 大小：相同程序的短时探针实际报告读写缓冲均为 14 MiB。quic-go 可在具备权限时尝试 SO_RCVBUFFORCE/SO_SNDBUFFORCE。原始测试区间整机 UDP 接收缓冲错误增加约 271 万，44 节点出现增长；包含其他进程流量，不能据此认定本任务全部丢包或唯一瓶颈。

## 使用与参数

~~~bash
./swarmcopy distribute --hosts 'node[001-005]' \
  --source /shared/nemo.sqsh --dest /raid/images --bin-dir ./dist \
  --transport quic
~~~

QUIC 候选默认相当于 `--quic-sockets 4 --quic-max-payload 8952 --quic-gso-kib 64`。默认传输仍为 TCP。`--quic-max-payload` 表示 UDP 负载上限，范围 1452..8952；8952 为 9000 减去 IPv6/UDP 的 48 字节，并不是把网卡设置成这个 MTU。实际发送长度还受对端通告与路径探测限制。

~~~bash
# 关闭接收分片与大包，仅做消融；新建独立任务/目标
./swarmcopy distribute --hosts @hosts.txt --seed node001 \
  --seed-file /raid/images/nemo.sqsh --dest /raid/images/new-case \
  --transport quic --quic-sockets 1 --quic-max-payload 1452
~~~

新任务保存参数，resume 沿用保存值。旧任务缺失字段时显式使用 1 socket / 1452，不能偷偷继承新默认。Performance.QUICSockets / QUICMaxPayload 报告配置；QUICMaxPayload 不是测得的实际路径 MTU。所有参与者使用同版本程序；旧小包上限的对端协商已通过局部测试。

## PMTU 正确性

新增使用真实 UDP 代理的测试：允许大包时观察到 8937 字节负载；代理丢弃超过 1472 字节的包时仍完成完整数据传输，观察到 1470 字节以内的包；小包对端只协商到约 1441。测试比较全部接收字节，均启用 race 检查。

真实 5 节点 8 GiB 默认参数部署也通过完整文件校验；接收端入站抓到约 8 KiB UDP 负载。只记录本任务端口的少量包长，公开数据不包含抓包原文或媒体内容。BPF 的 greater 条件按整包长度匹配，公开 JSON 的数值是解码后的 UDP 负载长度，两者不混为一谈。

这些测试验证建立连接时的窄路径与协商。传输中路由变化导致 MTU 降低的恢复尚未验证，不能宣传为覆盖所有动态网络的自适应 MTU 方案。内网之外仍须评估网络条件；TCP 保持默认。

## 最小依赖补丁与维护

Go 模块固定 quic-go v0.65.0；vendor 目录包含固定的构建依赖及许可证，可离线构建。四个 quic-go 文件的本地修改完整保存在 `patches/quic-go-bulk.patch`：

- interface.go：每连接 MaxUDPPayloadSize 上限。
- config.go：范围校验、默认 1452、配置复制与初始包裁剪。
- connection.go：客户端/服务端通告及本端、对端上限取最小。
- internal/protocol/protocol.go：缓冲容量可容纳到 8952。

上游默认包长仍为 1452；本程序 CLI 显式为新 QUIC 任务配置较大上限。原有可靠传输、拥塞控制、TLS、初始包保守行为和 PMTU 算法保留。缓冲容量增加不是发送包长的强制设置。

~~~bash
SWARMCOPY_GO=/path/to/go bash scripts/vendor-quic.sh
go test -race ./...
go vet ./...
bash scripts/build.sh
~~~

重建脚本先从固定模块生成 vendor，再应用补丁。已检查生成的 928 个文件逐字节可复现。升级上游时必须重新审查四个改动点，并重新执行窄路径/混合上限/丢包/中断恢复及新目录速度对照；不能只更改版本号。运行仍是一个静态二进制，无外部 QUIC 动态库。

## 后续速率控制方向

网络排队、接收端 CPU/磁盘能力和用户限速应各自观测，只有定义明确的约束才合并。RTT 必须使用网络层采样，不能将完整 HTTP 请求或落盘耗时当作网络 RTT；丢包也不能自动等同于网络拥塞。接收端反馈和按速率批量 pacing 值得单独研究。

当前不叠加第二套时延控制器，不关闭 QUIC 拥塞控制，不更改下载并发默认值。静态样本中的某一速率公式、阈值或 TLV 布局不能直接作为经过动态验证的实现。新控制算法须独立测试，避免混入本轮 socket/包长的消融结果。


## GSO 批次与包长配套

--quic-gso-kib 范围16..64，新任务默认64，旧记录缺失时使用20。64KiB 的实际上限保守裁到65,527字节，表示一次 GSO 提交的总容量；每个线上 UDP 包仍受8952与路径上限约束，不是发送64KiB巨型报文，也不依赖 IP 分片。

原20KiB缓存最多容纳两个满8952包；较大批次可以减少容量限制导致的提交。是否能填满还取决于数据、ECN、发送队列、拥塞窗口和 pacing，逐包预算检查全部保留。独立同二进制对照比较20与64，不预设更大一定更快。依赖补丁仍限于上述四文件，额外包含 MaxGSOBatchSize 的配置、归一化、容量上限和发送循环条件。原始容量/模式保留在前序提交。
