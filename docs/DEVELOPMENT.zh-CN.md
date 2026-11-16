# 开发指南

[English](DEVELOPMENT.en.md) · [文档索引](README.md) · [使用示例](EXAMPLES.zh-CN.md)

本文记录当前主线的架构、兼容约束和验证方法。只维护 `main`，所有传输与调度选项都在同一二进制内；发布沿革见 [CHANGELOG](../CHANGELOG.md)，历史测量见文档索引。

## 构建与本地验证

需要 Go 1.26+；依赖已 vendor。Go/Python 只供开发使用，分发程序运行时不依赖它们。

```bash
go test -mod=vendor -race ./...
go vet -mod=vendor ./...
bash scripts/build.sh
SWARMCOPY_BINARY="$PWD/dist/swarmcopy-linux-x86_64" python3 scripts/cli-smoke.py
SWARMCOPY_BINARY="$PWD/dist/swarmcopy-linux-x86_64" python3 scripts/integration.py
```

build.sh 输出两种静态 Linux 二进制和 SHA256SUMS；可通过 SWARMCOPY_GO 指定编译器。最后两项在独立临时目录检查 CLI 和真实进程链。x86_64 已有集群实机验证；ARM 交叉编译成功不代表实机测试。

## 源码地图

| 模块 | 职责 |
| --- | --- |
| benchmark*.go | 固定缓冲网络流、SSH stdin 租约、测量窗口和可选 NUMA |
| main.go / help.go | CLI、peer 生命周期、HTTP 服务与帮助 |
| cluster.go / parallel.go | 主机展开、作业持久化、SSH 部署与控制器 |
| cli_sources.go / tree_source.go | 多来源参数、glob、只读来源快照与流 |
| tree_plan.go / tree_import.go | 可信清单缓存、种子导入与复核 |
| tree_manifest.go / integrity.go | v1/v2/v3 清单、逻辑流、算法与模式合法性 |
| tree_store.go / tree_detach.go / tree_existing.go | 暂存、盘点、冲突、发布和成品缓存 |
| source_origin.go / resilience.go | 原文件直接供种、节点隔离与诊断 |
| adaptive.go / transfer.go | 有界选块、来源排名、worker、去重和阶段统计 |
| verify.go / verified_linux.go | full 有序流水校验及旧单文件缓存 |
| quic.go / patches / vendor | QUIC/HTTP3、来源分片、PMTU/GSO 与依赖补丁 |
| agent.go / auth_linux.go | PID/启动时间归属、SSH 隐藏密码与内存 askpass |
| progress.go / watch.go / completion.go | 准备进度、全局监控与完成摘要 |
| cleanup.go / remove.go / statefiles.go | 完成清理、显式删除、锁和原子状态文件 |

## 清单与完整性契约

- SCDP/1 与旧 v1/v2 清单 JSON 身份保持兼容，不能用新增必填字段改变旧 ID。v1 的 SHA256 是裸文件摘要；树清单的 SHA256 是 Entries 元数据树摘要。
- SHA256 树可用 v2；BLAKE3/其他自定义算法用 v3，明确记录块/文件算法及逐文件 `algorithm:digest`。默认 chunks/BLAKE3 新作业使用 v3。版本号描述协议格式，不是文件数量。
- 默认预设只在新建 distribute 时解析。`verifyMode("")` 仍代表旧记录/低级 peer 的 full，不能为了新默认改它；resume 不重写算法或缺省语义。
- 块摘要匹配后才能写盘并声明 Have。full 另外检查实际文件内容，chunks 要求全部密码学块通过、fsync 与发布。XXH3 只能搭配 full 密码学文件校验。
- chunks 验块完成不能伪造 full 成品缓存。缓存必须绑定完整清单 ID 以及每个文件的 device/inode/size/mtime/ctime，不能只看长度或目录 stat。
- 多文件为逻辑字节流，块可跨文件；full 按清单的各文件摘要验证，空文件和目录也必须发布。详见[文件布局](TREES.zh-CN.md)和[完整性](INTEGRITY.zh-CN.md)。

## 来源、存储与恢复

源节点在目标列表时，原文件只读供种。同路径目标复用已有文件；异路径本机副本通过相同验块/提交路径生成，LocalCopied 与网络计数分离。来源布局及指纹必须保持不变，源读错误不能算远端网络重试。

staged 模式先盘点已有副本；没有完整来源时，SSH tar 仅作为内存流，接收端直接形成文件并与可信清单匹配，没有完整 tar 中间件。首轮源摘要和初始导入成本应分别记录；初始导入尚无块级续传。

新暂存与导入都在 `.swarmcopy` 内、与成品同盘；旧 `.swarmcopy-trees` 按需原位恢复，禁止批量搬介质。稀疏检测跳过能确认的空洞，空文件不能让跨界块误判缺失；不支持 SEEK_DATA 时保守回退。

冲突 resume/replace 使用独立暂存，保留旧成品到所选验证通过。逐文件原子发布，不是集合事务；link 成功/unlink 前崩溃时必须脱离保留硬链接后再修复，避免改写成品。文件和目录元数据/fsync 错误不得误报 Complete。路径受 os.Root、相对路径与逐层类型检查约束，拒绝符号链接和特殊文件；来源与目标须与外部写入者互斥。

## 调度与指标

固定 workers/uploads 默认 4/4，自适应和 endgame 默认关闭。自适应只改变下载目标数，范围是有效 min-workers 到 max-workers；没有运行中热调参 API。降低并发不能无故取消普通请求。

选块以随机起点和互素步长覆盖范围，每次范围选择最多评分 255 个缺块，必要时经稀疏来源位图补选一个；补选仍可遍历位图，不能把评分上限称为全部工作常数时间。保留窗口优先、来源上限、过期/冷却过滤和去重。它是近似稀缺块优先，不承诺全局最优。

Node.manifestID 构造后不可变，状态/发现复用它；未来若允许更改清单，需重设缓存生命周期。网络、哈希、写入工作秒数是并发累计值，不是串行阶段。不能把各节点阶段最大值相加，也不能将本地复制算网络吞吐。

CompletionSummary 的集群平均分母为本次控制器总耗时；同一 peer 实例续传扣除起始计数，重启实例用新计数，避免历史流量抬高速度。准备阶段已有 peer 也可能在传输，所以不能只用监控阶段作分母。保存/读取均保留可选 Summary，旧记录不补造。

## 控制与生命周期

SSH 登录预检在大文件读取之前。`mergeSSHOptions` 按不区分大小写的键合并默认/保存/显式选项，值不做 shell 二次拆分；显式 cipher 优先。密码不进入参数、磁盘日志或 JSON。

节点操作使用独立、有界上下文；所有作业记录保留完整 Hosts。监控不等待全体监听，健康节点继续，失败节点不能算完成。原文件来源属于控制器且有独立动态端口，控制器退出会停止该来源；没有自动转交控制器或隧道层。

agent 按 PID、启动时间、可执行路径和 token 路径判断归属；升级检查启动版本，参数/来源改变按需重启，不因此清空有效缓存。同版本开发构建需主动停止自己的测试 peer，不能依赖版本字符串区分源码。

progress.json 绑定 attempt 与控制器身份，阶段/字节/节点计数分离，ETA 仅覆盖当前阶段。来源按缓冲区响应取消，远端盘点由输入心跳感知控制连接退出。内核阻塞 I/O 不保证立即被信号打断。完成结果可离线查看，不能因已正常停止的 peer 显示失败。

## 完成清理与安全删除

全部目标验证并发布、peer 停止后才自动清理本作业部署和空暂存。固定 cache.json 只保留最近清单，last-peer.log 最多 8 MiB，控制器保留作业统计。中断、共享、实际数据、未知条目、活动锁或不安全符号链接均保留。

cleanup 持控制器/agent/目标锁并验证归属，清理失败保存 CleanupIssues，不能冒充传输校验失败。重复成功清理不再部署，stop 已完成且清理的作业也应成功。remove/delete 另行删除记录，purge-partials 是显式放弃独占续传数据，不能合并成默认清理。详见[清理契约](CLEANUP.zh-CN.md)。

## 回归、性能与发布

按改动风险选择测试：身份与旧清单、源变化、坏块/短尾块、乱序/跨文件、空集合、冲突、取消、fsync EIO、发布中断、硬链接恢复、缓存隔离、活动锁/共享数据、TCP/QUIC。集群测试使用独立 dest，及时清理自己创建的介质和进程；不要在生产目录演练破坏性故障。

测速冻结构建、来源和参数，使用全新目标与一致的缓存处理，保留所有轮次和运行中重试。分别报告准备、盘点、传输、校验、fsync、发布、停止/清理；独立成品核验与计时口径写清楚。不能用续传假装从零，或将 CPU 微基准比值当网络收益。历史 JSON 在 [data](data/README.md)，报告入口见[索引](README.md)。

发布前同步中文主文档、英文对应页、CLI 帮助和变更记录，检查链接/示例/默认值；文档按主题更新，禁止再将 release 段落逐次堆到 README 顶部。发布同一提交的双架构构建、校验和与许可证。改 vendor 前读 [QUIC 补丁维护](QUIC-TUNING.zh-CN.md)，保持上游版本、补丁和测试一致。仓库不提交凭据、介质、私有日志或逆向材料；第三方文档保留原文。

后续优化以 profiling 为依据：大量小文件的元数据/FD 开销、分散的部分副本组合、初始导入续传。保持默认 TCP 和所选完整性门槛，不以增加复杂度或默认并发掩盖瓶颈。

## 网络基准维护

benchmark 是独立的合成网络测试，不是禁用校验的 distribute。保持 payload 固定缓冲、接收即丢弃，禁止按测试总量分配内存。默认 mesh 同时收发；seed 在双节点时为单向，不能混比两种成绩。流量只在 hosts 内，SSH 仅部署、控制和租约；默认端口 19701。默认无 numactl 依赖，显式 NUMA 策略通过受限参数调用现有 numactl。--cpus 仅调 Go 并行度。

测量 reset/freeze 使用每节点单调时钟和计数快照，预热不计入；冻结后保留流量到全部节点取得快照，避免停止早的节点截断其他窗口。JSON 不保存 token，失败标记 failed。Ctrl+C 先关闭 stdin 租约，再等待 peer 退出、清理独占临时目录；不杀其他作业。新增场景要验证真实 TCP/QUIC、无 TCP 回退、计时冻结、有限内存和中断收尾。硬件检查、64 路/LACP/NUMA 经验和证据见[网络指南](NETWORK.zh-CN.md)。

网络指标必须来自路由接口 /sys/class/net/<iface>/statistics 的原始计数，以每节点单调时间计算差值；CURRENT 使用相邻样本，AVERAGE 使用预热后的完整窗口。bond 与物理成员分别保存，不相加。未知接口、loopback、计数回退和无效时间不能回退到应用字节。默认界面只突出每节点网口，不展示集群累计吞吐。

默认 mesh 64 workers、Go 并行度 32、1 MiB 缓冲。来源从各自索引之后循环排列；即便主机数大于 worker 数，每个节点仍分担上传，不能都固定选择列表前部。回归覆盖 60/160 节点的输入负载均衡。HTTP429、连接、读取、其他 HTTP/协议错误分别统计，忙时有界退避。quiet 只在 benchmark 的 sshRunner 实例启用，普通文件作业的日志契约不变。

普通帮助只列常用选项；--help-all 保留高级调优。交互终端原地刷新，非交互只显示稀疏阶段进度及最终表。--verbose 才显示部署命令。UI 测试必须用不同的应用与 NIC 数值确认取的是 NIC，且不把无请求错误或 ACTIVE 解释为压满。完整 60 节点 NIC 对照见网络指南。
