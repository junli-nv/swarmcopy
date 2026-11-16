# 开发指南

[English](DEVELOPMENT.en.md) · [中文使用说明](../README.md) · [示例](EXAMPLES.zh-CN.md)

本文描述统一主线 v0.5.0。只维护 main 一个长期分支；常规 TCP、可选 UDP、自适应、流水校验及目录分发都在同一二进制内。旧标签保留历史，后续直接在 main 改进，不另行发布长期并行功能版。

## 源码地图

| 文件 | 职责 |
| --- | --- |
| main.go | Manifest 入口、v1 prepare、peer、HTTP 服务与 CLI 分派 |
| cluster.go / parallel.go | 主机展开、持久 Job、SSH 部署、控制器与并行启动／停止 |
| cli_sources.go / tree_source.go | 混合参数、来源 glob／目录快照、SSH 流生成 |
| tree_plan.go / tree_import.go | 可信来源清单缓存、种子直接接收并复核流 |
| tree_manifest.go | v2 条目校验、树摘要、流式逐文件校验、contentFile |
| tree_store.go / tree_detach.go | 逻辑偏移映射、暂存、原子发布及硬链接崩溃恢复 |
| tree_existing.go | 成品盘点、逐文件指纹缓存和冲突交互 |
| adaptive.go / transfer.go | 调度、自适应下载并发、尾块去重、分块传输与阶段统计 |
| verify.go / verified_linux.go | 有序流水校验及 v1 最终文件缓存 |
| quic.go / patches / vendor | 可选可靠 UDP、任务身份、分片／PMTU／GSO 集成 |
| agent.go / auth_linux.go | 按 PID 与启动时间认领进程，隐藏密码与内存 askpass |
| statefiles.go / watch.go / help.go | 原子记录、全局进度和英文帮助 |
| *_test.go / scripts | 单元、race、CLI、进程链、构建及基准辅助 |

运行依赖 OpenSSH 和基本 Linux 工具；Go/Python 仅开发时需要。Linux amd64/arm64 静态二进制不依赖目标 glibc。当前依赖 quic-go v0.65.0，构建需 Go 1.26+。

## 关键契约

- SCDP/1 的块 API 与版本 1 清单 ID 不变；新来源任务用 v2。不要给 v1 添加会改变 JSON 编码的必填字段。
- v1 Manifest.SHA256 是整文件内容哈希；v2 是 Entries JSON 的树摘要，逐文件哈希在 Entries 中。详见 [多文件设计](TREES.zh-CN.md)。
- 下载块先验证 SHA256 再写盘／声明 Have；最终内容仍经过完整有序 SHA256，再 fsync／发布。流水完成不等于节点完成。
- v2 多文件为一个逻辑流，允许块跨文件边界；空目录与空文件仍要发布。
- 网络重试、endgame 取消、源切换均不能重复重写已完成块；取消后不得将未验证内容标为 Complete。
- 续传重新验证临时块；缓存必须同时绑定可信 manifest 和数据文件指纹。目录自身 stat 不能代替其内容。
- 同名不同文件默认交互；resume／replace 保留旧成品到验证完成。逐文件原子，没有跨整个集合的原子事务。
- 不跟随树中的符号链接；操作受 os.Root 和祖先类型检查约束。添加新类型须同时补安全、崩溃与恢复测试。
- 所有来源和成品在任务期间应保持不被外部程序修改；仅在检测出竞态时拒绝不能替代系统层面的写入互斥。

## 控制、状态、监控

控制平面用 SSH 上传同一个程序、token、可信清单和配置；agent 启动有归属记录的 peer。数据默认 HTTP/TCP，QUIC 显式启用；控制及监控始终走 TCP。没有 tracker 或 NFS 回源逻辑。

Job 大致经过 created → starting → hashing-source／importing（如需）→ running → complete，失败记录 interrupted。stop 写停止请求并按进程归属结束任务。记录状态不是实时集群共识，watch/status 使用现场数据或明确标记的完成记录。

v2 先全节点盘点，有完整副本就不从控制端上传介质；否则初始 tar 仅作为 SSH 内存流，直接落各文件，且必须匹配控制器清单。首次建立清单的控制端读盘成本与网络分发是两回事。

workers/uploads 默认 4/4；固定与自适应共用 worker 池。自适应只改下载目标数，min 默认初始 workers、max 默认 16；默认关闭。参数创建时保存，当前无运行中调参 API。旧记录中显式 false/0 不能被新版默认值偷偷覆盖。

全局显示 bytes 是全文件集合的已验证块字节，Complete 还需最终校验／发布。零字节数据集的百分比必须以实际 Complete 为依据，不能除零或提前当作成功。结果保存每个节点的阶段统计和来源流量。

## 构建与验证

~~~bash
go test -mod=vendor -race ./...
go vet -mod=vendor ./...
bash scripts/build.sh
SWARMCOPY_BINARY="$PWD/dist/swarmcopy-linux-x86_64" python3 scripts/cli-smoke.py
SWARMCOPY_BINARY="$PWD/dist/swarmcopy-linux-x86_64" python3 scripts/integration.py
~~~

最后两项是本地临时目录回归测试；不要在生产目录运行会改数据的测试。集群功能验证使用独立 dest，只处理测试自己创建的文件，结束后按归属停止 peer。[本版五节点验证及限制](TREES.zh-CN.md)。

scripts/build.sh 输出 dist 下两种静态二进制和 SHA256SUMS。实际 CPU 加速由 Go 的 SHA 实现按平台选择；hashbench 可测内存／文件哈希，不要把已有 SHA-NI 误称为新加速算法。

重建 vendor 需先查看 [QUIC 参数与 patch 维护](QUIC-TUNING.zh-CN.md) 和 THIRD_PARTY_NOTICES。保持依赖版本、vendor diff、patch、默认参数和窄 MTU／小上限对端测试一致。

## 性能优化与发布

[流水校验](PIPELINE.zh-CN.md) · [自适应背景](ADAPTIVE.zh-CN.md) · [全部 UDP 研究](QUIC-TUNING-RESULTS.zh-CN.md) · [冷缓存方法](COLD-STUDY.zh-CN.md)

先区分哈希、磁盘、元数据、syscall、拥塞和部署开销，再改并发或速率控制。UDP 不会天然快于 TCP；不能把 socket 数、PMTU 和 GSO 多次相对收益相乘。不要用续传数据代替从零测速，传输过程重试则应正常计入时间。

发布前同步中英文 README、示例、英文帮助、开发说明、CHANGELOG；检查旧任务兼容、源变化、缓存失效、磁盘数据损坏、空集合、冲突、取消、发布中断、TCP 和可选 UDP；按改动风险选择测试，避免无意义重复跑大规模作业。

只发布同一次提交的 x86_64/aarch64 构建、校验和及许可证声明。未在 ARM 实机跑过须明确写明。仓库不包含 token、job token、介质、私有日志、逆向材料或测试的绝对内部路径。GitHub token 只能从授权的本地文件在内存读取，不进入参数、输出或提交。

后续重点：大量小文件的元数据／FD profiling、组合部分副本避免初始回源、初始导入续传；任何速率控制改造仍要保留默认稳定 TCP 和全量校验。具体数据模型、不变量及验证案例见 TREES。

## 准备进度维护

progress.go 通过 context 传递线程安全 reporter，每两秒输出并原子保存 progress.json；阶段、字节、文件和节点计数独立。Job 的 attempt 与控制器 PID/启动时刻用于拒绝陈旧进度。ETA 是当前阶段平均速率估计，不是全任务保证；metadata/监听准备没有可信速率时只报步骤和耗时。watch 每次重载 Job 并识别完成记录。读取器必须保留按缓冲区检查取消，远端库存校验通过 stdin 心跳感知控制器离开。详见 [进度设计及验证](PROGRESS.zh-CN.md)。

## v0.5.0 开发约束

`integrity.go` 集中管理算法、清单绑定和模式合法性；`integrity_test.go` 覆盖算法向量、合法组合、错误组合、真实 HTTP 接收、弱块摘要与强文件摘要不匹配、缓存隔离、清单流和新稀疏文件扫描。详见[校验语义和实测](INTEGRITY.zh-CN.md)。

必须保留：旧 v1/v2 空算法字段等于 SHA256/full，不能改变旧 JSON ID；v3 必须明确算法并标记逐文件摘要；XXH3 只能 full；chunks 不能生成 full 回读缓存；模式改变必须重启并重查 peer；种子接收必须与可信源清单一致；最终发布不能绕过所选校验门槛。重新启动后现有部分文件仍需检查，不能只按长度认定完成。

`Job.SSHOptions` 保存重复的 `--ssh-opt`。`mergeSSHOptions` 按不区分大小写的键合并默认、已保存和本次覆盖值，不进行 shell 二次拆分；显式 cipher 优先。新增 SSH 参数使用这个集合，避免多个调用路径默认值分裂。SSH 登录预检必须保留在来源大文件读取之前。

新增 BLAKE3/XXH3 固定依赖已 vendor 并附许可证；改依赖后执行 `scripts/vendor-quic.sh`，保留 QUIC bulk 补丁。以 CGO_ENABLED=0 构建 amd64/arm64，明确交叉构建不等于 ARM 真机验证；保留测速参数，不把 CPU 微基准倍数说成网络收益。

v0.6.0：`resolveNewJobIntegrity` 只在新建作业时解析模式预设：full→SHA256/SHA256，chunks→BLAKE3/BLAKE3；显式算法优先。禁止把预设应用到 loadJob 或已存在 manifest，否则会破坏续传兼容性。旧 seed-file 以其清单为准。

## v0.6.0 来源与生命周期约束

`source_origin.go` 将可信来源清单的逻辑偏移映射到原始只读文件；开始供种前验证来源指纹与布局。控制器退出时关闭 HTTP/QUIC 和文件句柄。`controllerSourceHost` 仅在本机真实主机名匹配目标时选择 direct。`SourceAddress` 不计入接收目标总数。agent 复用时比较来源地址及校验模式，来源端口改变时重启 peer，但不因此无条件清空成品验证缓存。

`resilience.go` 为各节点操作提供独立、有界上下文；所有保存的记录都必须保留完整 Hosts，盘点阶段也不例外。监控没有全体就绪门槛，诊断有界并持久化，失败节点绝不能计为完成。

`remove.go` 持有控制器/agent 锁，验证进程身份和配置归属；部分清理失败保留记录。清理暂存命名空间前检查其他作业引用，保护源文件、成品和共享数据，拒绝非作业目录及符号链接根。`resilience_test.go`、`remove_test.go` 验证这些边界。稀疏检测必须跳过空文件；不支持 SEEK_DATA 时保守回退。
