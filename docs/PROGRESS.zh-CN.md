# 准备进度、预计时间与停止

[English](PROGRESS.en.md) · [示例手册](EXAMPLES.zh-CN.md) · [文档索引](README.md)

distribute 自动报告来源哈希、盘点、初始复制和节点传输阶段，无需额外开关。hashing-source 的末尾路径是当前文件，总字节覆盖全部选中来源，不代表只选择该文件。

## 会看到什么

~~~text
PREPARING hashing-source  32.3% | 156.40 / 483.84 GiB | 1126.4 MiB/s | ETA ~4m58s | elapsed 2m22s | /data/local-ai.tar
PREPARING deploying | nodes 3/9 | elapsed 4s | deploying executables and job configuration over SSH
PREPARING checking-existing | nodes 5/9 | elapsed 20s | verifying existing files on each node
PREPARING importing  40.0% | 193.54 / 483.84 GiB | 980.0 MiB/s | ETA ~5m3s | elapsed 3m22s | /data/local-ai.tar
PREPARING starting-peers | nodes 8/9 | elapsed 12s | starting peers
~~~

数值只是显示格式示例，不是性能承诺。源哈希与初始种子流传输每约 2 秒显示已处理字节、总字节、百分比、速度、预计剩余时间和当前文件；短阶段也显示开始与结束。目录任务按所有普通文件的总字节聚合，导入不把 tar 元数据计成介质内容。

部署、已有文件检查及启动 peer 显示当前动作、节点计数与已耗时；这些操作没有足够的字节样本时不编造 ETA。未命中验证缓存的已有大文件仍需读取，其检查可能较久。

前几秒显示 calculating。ETA 根据当前阶段已观测平均速度计算，是估计值；不包含后续部署、校验、fsync 或传输阶段。字节达到 100% 但阶段收尾尚未完成时，显示 finalizing (time unknown)，不会将其当成整个任务完成。

## watch 与 status

~~~bash
./swarmcopy jobs
./swarmcopy watch --job <ID> --plain
./swarmcopy status --job <ID>
~~~

准备阶段读取控制端 job 目录中的 progress.json，不再把尚未监听的 peer 显示成全体 OFFLINE。watch 每次刷新重新读取任务状态，准备完成后自动进入节点全局进度；任务完成后读取保存结果，避免已正常停止的 peer 又被误报离线。

status 在准备阶段返回 Stage、Preparation 和 Complete=false，而不是节点连接失败。准备进度不是任务完成证明。新任务记录 controller 主机、PID、启动时刻标识与每次执行的随机 attempt；本机已退出的控制器会显示 controller-stopped，旧 attempt 的进度不会套到新执行上。

旧 v0.4.0 进程无法通过更新磁盘上的程序获得内部进度或新的取消逻辑；重新运行／resume 使用修复版。旧记录没有进度或进程身份时只能显示保存阶段，不能据此断言仍在运行。

## 取消与恢复

分发控制器正在哈希时可按 Ctrl+C，或在另一终端运行：

~~~bash
./swarmcopy stop --job <ID>
./swarmcopy resume --job <ID>
~~~

源读取按缓冲块检查取消；递归来源检查也响应取消。哈希阶段 stop 由本地控制器处理，不会尝试 SSH 到尚未部署的节点。远端已有文件检查使用输入心跳，控制连接关闭后会取消其读取。

正常本地磁盘读取下取消很快，但不能保证打断内核中阻塞的一次磁盘／文件系统读取。该边界不是“忽略信号”。源哈希和未完成的初始 SSH 导入尚无块级续传，重新执行会重做未完成阶段；已验证的 P2P 临时块仍保留。

Ctrl+C 停止控制器，已经部署的独立 peer 可能继续运行；要明确停止这些 peer，使用 stop --job。退出 watch 只退出监控。不要为了停止某个任务使用全局 pkill -9 swarmcopy，它会连监控和其他任务一同杀死，也来不及保存正常退出状态。

## 实现与历史验证

独立 64GiB 稀疏测试文件的原生进程验证：哈希中有百分比／速度／ETA，watch/status 不查询 peer，SIGINT 约 0.007s 退出，stop 命令约 0.35s 退出，且没有部署节点。另一个 64GiB 检查进程在控制输入关闭后约 0.005s 退出。时间只是该测试环境的观测值，不是时延上限，也不是介质吞吐基准。

两节点、139,460,641 字节、5 个文件的真实 TCP 分发通过全部准备阶段、SSH 输入心跳、监听就绪、完整 SHA256 校验和保存结果显示。没有重启用户原本的 local-ai 任务。x86_64 实机验证，aarch64 交叉构建。

progress.go 保存进度并处理心跳、ETA、取消及阶段视图；progress.json 原子替换但不为每个心跳强制 fsync，避免进度元数据拖慢大文件。任务配置和可信清单仍按原来的持久化规则写入，进度写入失败只提示，不将未验证数据当成有效内容。新增单元／race 测试覆盖 ETA、取消、stop、旧 attempt 隔离、进程身份、无端口查询和载荷计数。

节点启动后独立传输，没有等待全部监听的门槛。旧日志中的 waiting-peers 不能据此判定节点挂掉；应看具体进程、日志和节点诊断。完成后的耗时/速率与清理规则见[操作示例](EXAMPLES.zh-CN.md)和[清理说明](CLEANUP.zh-CN.md)。

## 已有副本的后台校验

当来源节点在目标列表中（SOURCE MODE: direct）时，启动盘点只检查元数据和有效的内容校验缓存；同名同尺寸但没有可信缓存的文件仍待校验。peer 先开放状态接口，再后台读取已有内容，逐块验证、逐块供其他节点下载；本机完成初始化后才开始补齐缺块。因此慢盘节点不会让所有节点等待一轮完整文件校验。staged 模式仍先完整盘点，以选择已有完整种子、避免不必要的初始 SSH 复制。

watch 中的 VERIFYING LOCAL 表示正在验证已有内容，已验证分块可以供种，不表示节点离线或任务完成。完整验证的文件逐步保存缓存检查点，取消后可复用。仅靠文件名、尺寸不会供种。同尺寸冲突可能到后台校验时才发现；默认仍询问，非交互任务应明确指定 --existing resume、replace 或 keep。已选择的策略同样适用于随后发现的冲突。原文件保留到替代文件校验并发布成功。
