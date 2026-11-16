# 流水线整文件校验与 CPU 加速

[English](PIPELINE.en.md) · [开发指南](DEVELOPMENT.zh-CN.md)

本分支在 SCDP/1 上增加可选流水线整文件 SHA-256，以及单体 hashbench 诊断命令。它与之前的自适应并发是独立开关。分块校验、完整文件摘要、fsync、原子发布仍然保留。

## 使用

~~~bash
./swarmcopy distribute \
  --hosts 'node[001-005]' --source /shared/nemo.sqsh \
  --dest /raid/images --bin-dir ./dist \
  --pipeline-verify --verify-buffer-mib 4
~~~

--pipeline-verify 默认关闭，开启后接收数据和整文件哈希可以重叠。--verify-buffer-mib 默认 4，范围 1..64，控制串行或流水线校验的读取缓冲。未开启时仍在接收全部完成后校验。新任务保存参数，resume 沿用保存值；旧任务默认关闭流水线。目录和多文件仍是后续工作。

## 正确性与生命周期

下载 worker 完成块 SHA-256 和 WriteAt 后才设置 have，之后本进程不再重写该块。后台校验器仅读取 have=true 的连续前缀，从实际文件读取，而非仅相信网络缓冲或分块摘要。乱序块等待其前序块，不按到达顺序拼接。

每次都对完整文件的每一个字节计算普通 SHA-256，并与既有清单 SHA256 比较。不能将多个块的摘要拼成原文件 SHA-256。校验器可能提前完成计算，但不会自行发布文件：所有块完整、整文件摘要正确、最终 fsync 和权限设置成功后，才原子发布。

取消、读取失败或完整摘要不符时不发布，保留部分文件。退出会等待后台校验线程结束，避免文件描述符关闭后仍访问。恢复先按原规则重新校验已有块，再从文件开头重建整文件哈希，不从磁盘直接恢复旧的哈希内部状态。

这个设计依赖任务独占目标文件、已提交块不再被改写的约定；外部程序不得直接修改 .part。和任何文件哈希一样，它不承诺检测校验后其他进程对文件的改写，也不等同于验证磁盘介质断电后的物理读回。

## 乱序与收益边界

校验器只需要一个读取缓冲，不把整份镜像缓存到 Go 堆中。读取已就绪数据后更新 SHA 状态，等候缺失前缀时通过合并通知唤醒。已发布文件作为种子时，仍沿用原来的完整验证缓存或 --recheck，不额外启动重复校验线程。

默认继续使用稀缺块优先调度。如果很靠前的块晚到，顺序 SHA 会等待；因此“使用流水线”不自动等于隐藏全部校验时间。它也可能与传输争抢 CPU、内存和存储，必须看整批完成时间。当前实现没有改变下载顺序来强迫某个结果。

## 指标

watch 在接收节点显示 hash 百分比；status 和 result.json 的 Performance 增加：

| 字段 | 定义 |
|---|---|
| PipelineVerify | 此任务是否配置流水线 |
| FileHashBytes | 本进程已纳入完整文件 SHA 的字节 |
| FileHashBytesAtTransferEnd | 主循环确认全块齐备、进入最终化时已经哈希的字节 |
| FileHashReadSeconds | 文件读取调用的累计墙钟时间 |
| FileHashComputeSeconds | SHA256.Write 调用的累计墙钟时间，可能包含调度等待 |
| FileHashWaitSeconds | 流水线等待前序数据就绪的时间 |
| FileHashWallSeconds | 整个文件哈希线程耗时；流水线包含等待，串行是完整扫描耗时 |
| FinalHashSeconds | 全块到齐并完成第一次 fsync 后等待完整摘要的尾部时间 |

FileHashBytesAtTransferEnd 是主循环观察时的进度，不是网卡最后一个字节抵达时的精确采样。FinalHashSeconds 变小不代表计算被省略，须同时看总哈希字节、计算耗时和整批时间。不同节点阶段最大值不能相加。

## CPU 硬件加速

程序继续使用 Go crypto/sha256。当前工具链在 x86_64 支持条件满足时自动优先选择 SHA-NI，再选择 AVX2/BMI2，否则使用通用实现；在 aarch64 上可使用 Arm SHA2 扩展。这是运行时 CPU 能力选择，同架构不需要为不同 CPU 型号分发不同程序。

本次实际检查了计算节点 CPU 特征，并采集 CPU profile，确认执行了 crypto/internal/fips140/sha256.blockSHANI。再以仅对子进程生效的 GODEBUG=cpu.sha=off、cpu.all=off 做 AVX2/通用对照，结果摘要一致。硬件加速此前已经生效，不能把该对照的倍数当作新版相对旧版的端到端提升。

ARM64 仍只有交叉构建，没有本次硬件执行验证。是否实际走硬件路径应看对应机器的 profile，而不是仅看 CPU 型号或编译成功。

## hashbench

~~~bash
./swarmcopy hashbench --help
./swarmcopy help hashbench
./swarmcopy hashbench --bytes-mib 2048
./swarmcopy hashbench --file /raid/images/nemo.sqsh --buffer-mib 4 \
  --cpu-profile /tmp/new-sha-profile.pprof
go tool pprof -top ./swarmcopy /tmp/new-sha-profile.pprof
~~~

hashbench 只读输入文件。profile 路径必须是新文件，不覆盖已有路径。默认内存缓冲 64 MiB、总输入 2048 MiB；缓冲范围 1..512，总输入范围 1..65536，均可用帮助查看。profile 同时包含初始化、内存和可选文件测试；FileReadSeconds / FileComputeSeconds 单独报告文件部分。

AdvertisedCPUFeatures 来自 /proc/cpuinfo，不能代替实际指令路径证据；GODEBUG 可能关闭特性。纯内存速率不等于磁盘校验速率，profile 增加的开销和后台负载也要考虑。运行分发不需要 Go 或分析工具，只有离线分析 profile 时需要 go tool pprof。

## 验证与复测

新增乱序前缀等待、短尾块、完整文件/清单矛盾、磁盘字节损坏、取消与读错误、原子发布门槛测试。实际 race 二进制运行中断、坏块修复和接力测试；也用旧版种子与新接收节点混合验证。

~~~bash
go test -race ./...
go vet ./...
SWARMCOPY_TEST_FLAGS='--pipeline-verify --endgame' python3 scripts/integration.py
python3 scripts/benchmark-verify.py --help
~~~

对照使用上一发布版 / 当前串行 / 当前流水线，固定下载并发，不混入自适应策略。每次新目录、清 OS 页缓存、全部接收端完整传输并校验，计时包括部署和停止。CPU 后端微测与分发实测分开报告，不以降低完整性换取加速。

