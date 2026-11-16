# 开发指南

[English](DEVELOPMENT.en.md) · [使用说明](../README.md) · [验证记录](VALIDATION.zh-CN.md)

本文描述 v0.1.0 的实际实现，优先保证可理解、可恢复和低部署依赖。优化时保持双语文档、CLI 帮助和测试同步。

## 源码地图

| 文件 | 职责 |
| --- | --- |
| adaptive.go / transfer.go | 并发反馈、来源调度、尾块去重、分阶段统计；细节见 [实验说明](ADAPTIVE.zh-CN.md) |
| verify.go / hashbench.go | 连续前缀整文件哈希、取消门槛与 CPU/文件诊断；见 [设计说明](PIPELINE.zh-CN.md) |
| main.go | Manifest、prepare、peer、分块 HTTP 服务、校验、调度及入口 |
| cluster.go | 主机展开、SSH 部署、Job 持久化、批量分发/恢复/停止 |
| statefiles.go | 记录原子写入、文件及父目录 fsync |
| agent.go | 按任务启动/复用节点进程，以及 PID 身份检查与停止 |
| auth_linux.go | 隐藏密码输入、内存 Unix socket askpass、Linux 终端处理 |
| parallel.go | 有界并行 SSH 部署与停止 |
| verified_linux.go | 绑定 inode/mtime/ctime 的已验证最终文件缓存 |
| watch.go | 有界并发状态采集、速率/ETA、全局终端进度 |
| help.go | 英文帮助、最近任务选择、保存完成结果的显示 |
| *_test.go | 完整性、调度、主机解析和监控测试 |
| scripts/integration.py | 本机三进程链式传输、中断及损坏恢复 |
| scripts/build.sh | 无 CGO 的 x86_64/ARM64 发布构建 |

运行时只依赖 OpenSSH 和目标系统的基本工具。Go/Python 仅在开发和测试时需要。

## 两个平面与状态机

控制平面：控制端通过 SSH 上传二进制、任务 token、清单和配置，调用同一二进制的 agent 子命令。agent 用 setsid 启动 peer，将日志留在任务目录；不安装系统服务。异构节点通过 uname -m 选择对应二进制。

数据平面：peer 之间用 HTTP/TCP 交换分块。全局监控直接从每个 peer 取少量 JSON。无 tracker、数据库、消息队列或公网发现服务。初始导入通过 SSH，之后不回源读取 NFS。

Job 保存状态：created → starting → importing（新源时）→ running → complete；失败记为 interrupted。stop 写 stop.request，并尝试停止全部本任务进程，记录 stopped/stop_failed。保存状态不是集群共识，控制端异常退出可能留下过时状态。

peer 流程：获得目标锁 → 打开/创建文件 → 全量扫描已有块 → 监听 → 发现邻居并下载/上传 → fsync → 整文件 SHA-256 → 原子发布 → 继续做种。默认控制器在全体完成后停止做种；单独 peer 直到信号或 lifetime。扫描、最终校验阶段会有磁盘读取开销。

## 清单和任务格式

Manifest 的 JSON 字段名由 Go 导出字段决定：

~~~json
{"Version":1,"Name":"nemo.sqsh","Size":123,"ChunkSize":16777216,"SHA256":"<64 hex>","Chunks":["<64 hex>"]}
~~~

示例仅说明字段，非有效清单。Version 当前为 1；分块长度须在 64 KiB～64 MiB，末块可短于 ChunkSize，块数必须与 Size 对应。禁止空文件、路径穿越式 Name、错误哈希格式和过大清单。内容哈希是小写十六进制 SHA-256。

Manifest ID = 对 Go json.Marshal(Manifest) 规范输出求 SHA-256。它不是对原始清单文件字节求哈希。跨语言实现要复现字段顺序、字段名、数字表示和 Go JSON 转义规则，不能直接哈希任意重排后的 JSON。

部分文件名为 `<final>.<ManifestID前16位>.part`，不同清单不会共用同一部分文件。最终目标旁的 .swarmcopy.lock 用 flock 防止多个写入进程。文件描述符使用 close-on-exec，避免子进程意外持锁；目标、部分文件及锁使用 O_NOFOLLOW。

控制端任务目录包含 job.json、job.token、manifest.json、controller.lock、result.json（完成时）和可能的 stop.request。Job 中 time.Duration 以纳秒整数编码。默认 XDG 用户状态目录；--job 可用绝对路径。job.json 经临时文件写入、fsync、rename 更新；result.json 保存完成证据。任务目录/token 应按私密数据处理。

节点目录为 `<dest>/.swarmcopy/<ID>`，包含程序、token、清单、peer.json、process.json、agent.lock、peer.log。最终介质和 .part 位于实际目标文件旁。第一版不自动回收历史任务目录。

## SCDP/1：HTTP 协议

SCDP = SwarmCopy Distribution Protocol。程序版本为 0.1.0，协议版本为 SCDP/1，Manifest.Version 为 1；三者含义不同。请求和响应携带 X-SCDP-Version: 1，/status 含 Protocol: "SCDP/1"。明确声明其他版本会被拒绝；缺少标识的开发期实现暂时允许，以兼容本次早期测试记录，后续主版本须制定迁移策略。

所有请求均需 `Authorization: Bearer <job-token>`。使用常量时间比较；只接受 GET，不接受重定向，不使用环境 HTTP 代理。

| 路径 | 成功响应 | 常见失败 |
| --- | --- | --- |
| /status | 200，Status JSON | 401 未认证；405 方法不允许 |
| /chunk?i=N | 200，精确 Content-Length，原始块字节 | 400 非法索引；404 尚未校验；429 上传槽位已满 |

Status 主要字段：ID、Version（程序版本）、Size、VerifiedBytes、Have（布尔数组）、Complete、Origin、Downloaded、Uploaded、From、Error。Recorded 为控制端显示保存结果时使用的标志，实时 peer 为 false。Have 只在块校验和 WriteAt 成功后置 true；Complete 还要求整文件哈希和发布完成。

Downloaded/From 统计本进程接受并写入的有效块，不跨重启累积；Uploaded 统计写入 HTTP 响应的字节。它们不是完整网卡线速或协议开销统计。Origin 表示启动时已有最终文件，不表示永久身份。

数据面没有 TLS、端到端节点身份认证或 Byzantine 防护，token 持有者能读取该介质；只适用于互信集群。Hash 检测损坏，不负责证明清单来源。清单和 token 通过受信 SSH 控制平面分发。

## 调度、内存与并发

邻居由固定列表发现，每轮查询 /status，并丢弃清单 ID/块数不匹配或超过新鲜度阈值的记录。当前每轮对邻居并发查询，间隔约 1 秒。

选择缺块时随机化扫描起点，优先稀有块。针对同一块，优先较空闲的非 Origin 节点；单个下载节点对某一来源最多并发两个请求。没有全局块分配，启动时仍可能重复获取同一块。HTTP 429、网络错误、长度错误和哈希错误进入重试。

Node.mu 保护 Have、busy、邻居快照、per-peer 并发、From 和完成状态。上传/下载计数器为 atomic。上传槽位为有界 channel；每个上传连接仅用 256 KiB 缓冲。每个下载 worker 复用最多一个分块，默认约 4×16 MiB 原始缓冲，Go GC 和状态数据有额外开销。速率限制器按节点聚合，256 KiB 片段预留发送/接收时间。

监控并发最多 16 个状态请求，单请求有截止时间。全局分母包含所有列出的目标；离线贡献最近确认值或零，只有当前成功响应且 Complete 才算活动任务完成。保存结果明确标记 Recorded。速率取计数差，进程重启后不产生负速率；ETA 不含最终校验开销。

## 数据安全与恢复

每块：检查长度 → SHA-256 → WriteAt → 发布 Have。传输中可以把尚未 fsync 的有效页提供给其他节点；崩溃恢复时会重新读盘计算哈希，因此不依赖“位图先落盘”的假设。

完成：fsync 数据 → 顺序整文件 SHA-256 → 设置最终权限并 fsync → 同目录硬链接发布（存在则失败）→ 删除 .part 名 → fsync 父目录。它提供同文件系统原子可见和不覆盖语义，不是跨文件/清单的事务。发布后文件须保持不可变；已有错误最终文件不会被自动修复。

恢复：已有 .part 总是重新扫描；未变化且带有效缓存的最终文件可复用上次完整校验，--recheck 强制复验。保留清单/token/部分文件，用原 Job resume；仍活着的 peer 根据 PID、/proc 启动 ticks、argv 和 token 文件路径复用，其他 peer 重启并全量校验。持久化位图不是可信依据。

控制端 SIGINT 不会主动清除数据；peer 在既定生命周期内继续。stop 写入停止标记，并只向身份匹配进程发 SIGTERM；超时不自动 SIGKILL。保留 controller.lock 和 agent.lock 的锁文件名是正常现象，是否持锁由内核决定。

## 测试和修改流程

~~~bash
go test -race ./...
go vet ./...
go build -o swarmcopy .
python3 scripts/integration.py
bash scripts/build.sh
~~~

基础测试覆盖错误分块拒绝、部分文件损坏重校验、已有目标不覆盖、分块鉴权和可用性、清单合法性、来源调度、主机范围、混合清单监控及重启速率。集成测试以 A→B→C 链传输，在 B 未完成时中断，修改其 .part，再启动，检查最终全部 SHA-256 和中间节点上传。

更改协议必须同步清单版本/兼容策略、两种语言文档和损坏恢复测试。更改 CLI 必须验证无参数、--help、help COMMAND、错误参数、空任务目录和保存完成结果。更改锁与进程管理必须验证不影响其他任务，尤其是 PID 重用和重复 resume。

## 已知限制及优化顺序

1. 初始导入尚无块级恢复；介质与清单发布有崩溃窗口。优先引入可恢复导入会话，明确发布事务与清单持久化顺序。
2. 无 TLS/mTLS。跨不可信网络前应补齐机密性、身份认证、凭据轮换及测试。
3. 恢复需完整扫描，磁盘空间未预分配；可增加持久化校验索引、fallocate/空间预检和校验流水线，不能盲信旧位图。
4. 固定全连接发现为 O(N²) 控制流量，SSH 部署和停止已有有界并发（默认 8）；大规模应优化部署调度、邻居限制和分层/拓扑感知调度。1024 是解析上限，尚未压测。
5. 当前聚合速率限制是软件节流，不是严格 QoS。应基于 CPU、磁盘、GC、交换机上联测量后优化缓冲池和读写合并。
6. 本地写入错误目前会进入重试，最终按无进展超时退出；应区分磁盘永久错误并尽快失败。节点启动失败、端口占用目前主要由失联状态及 peer.log 诊断。
7. job.json 状态并非实时心跳；节点故障、原始种子丢失和控制端迁移需要更完整的运维流程。恢复控制器当前仍要求所有列出主机可 SSH；已运行 peer 的数据交换不依赖控制端在线。
8. 原生 RDMA、IPv6 控制端主机展开、SSH alias 到独立数据 IP 的映射、多介质缓存/去重、机架感知、自动扩容节点、Prometheus/浏览器面板尚未实现。
9. ARM64 二进制可构建，但无本次目标硬件实测；--ask-pass 的实际目标密码认证也需独立验证。已完成的五节点测试使用现有公钥认证。


10. 目录、多文件和通配匹配留待下一版，需设计相对路径、逐文件完整性、冲突及中断后的多文件发布。

## 校验缓存与强制复验

已经通过完整校验的最终文件会生成 .swarmcopy.verified.json，绑定清单 ID、设备、inode、大小、mtime 和 ctime。默认仅在这些信息完全匹配时复用上次校验结果；新建部分文件无需扫描零空间，已有 .part 仍逐块重算 SHA-256，新收到的文件仍必须通过完整 SHA-256。

缓存假设文件不可变、操作系统正确记录写入元数据，不能代替检测静默磁盘损坏的定期复验。使用 --recheck 忽略最终文件缓存：

~~~bash
./swarmcopy distribute --hosts 'node[001-005]' --seed node001 --seed-file /raid/images/nemo.sqsh --dest /raid/images --recheck
./swarmcopy resume --job <ID> --recheck
~~~

新 prepare 会在导入校验后生成缓存；旧介质第一次使用会完整校验一次。不要复制其他文件的校验缓存。并行启动/停止默认最多 8 个 SSH 操作，可在创建任务时用 --fanout 调整。

## 手动查找与停止

~~~bash
pgrep -af swarmcopy
kill -TERM <确认过的PID>
~~~

优先用 stop --job 停止整批任务。杀掉控制端不会自动停止已脱离终端的 peer；本任务节点 PID 和日志在 <dest>/.swarmcopy/<ID>/。不要用全局 pkill 误停其他任务。强制 SIGKILL 仅作最后手段，部分文件可在恢复时重校验。

当前版本只接受单个文件，不支持目录、多输入或文件通配符；这些功能按计划留到下一版。并行任务应使用不同端口和独立目标；同一目标文件受排他锁保护。

## QUIC 数据通道分支

quic.go / quic_test.go 实现任务身份绑定的 HTTP/3 数据通道及真实丢包测试。开发此分支需要 Go 1.26+ 与固定模块依赖，仍不增加运行时依赖。完整接口、资源和安全边界见 [QUIC 开发说明](QUIC.zh-CN.md)。

UDP新增参数、四文件依赖补丁和后续接收反馈速率控制路线见 [调优维护说明](QUIC-TUNING.zh-CN.md)。quic_mtu_test.go覆盖真实代理的窄路径与协商，scripts/vendor-quic.sh重建固定vendor后应用patches/quic-go-bulk.patch。
