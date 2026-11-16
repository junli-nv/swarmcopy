# 自适应并发与尾块调度

[45 节点两轮实测结果](ADAPTIVE-RESULTS.zh-CN.md) · [文档索引](README.md)

[初始原型退化记录](ADAPTIVE-INITIAL-RESULTS.zh-CN.md)

[English](ADAPTIVE.en.md) · [开发指南](DEVELOPMENT.zh-CN.md)

自适应是同一二进制内的可选应用层下载并发与来源反馈控制，默认关闭。它同时适用于支持的 TCP/QUIC 分块通道，不是 FASP 实现，也不替代底层拥塞控制。完整性由所选 chunks/full 模式决定，新作业默认 chunks/BLAKE3。

## 启用与回退

~~~bash
./swarmcopy distribute \
  --hosts 'node[001-005]' --source /shared/nemo.sqsh \
  --dest /raid/images --bin-dir ./dist \
  --adaptive --workers 4 --max-workers 16 --endgame --hedge-delay 2s
~~~

- --adaptive：启用并发探测、来源速度评分、繁忙/失败来源冷却。
- --workers：固定模式并发数，或自适应模式初始并发数，默认 4。
- --min-workers：自适应下限，默认 0 表示等于初始 --workers；可显式设为 1..workers。
- --max-workers：自适应上限，默认 16，最大 32，不能低于初始值。
- --endgame：独立开关，允许尾块从第二来源请求，默认关闭。
- --hedge-delay：尾块第一次请求的最短等待时间，默认 2 秒。实际还会参考已学习来源耗时，避免过早重复下载。

不传 --adaptive 和 --endgame 即采用固定并发与原有来源选择。新建任务保存这些参数，resume 使用保存的设置；旧任务没有这些字段，仍保持关闭。没有单独开放修改活动任务调度参数的热更新接口。默认上传/下载仍无人工带宽上限；并发反馈不会改变显式 --upload-mib / --download-mib 的含义。

控制端自动部署普通二进制，无需额外自适应版本，也不增加运行时 Python、pdsh 或 Go 依赖。race 构建仅供开发检查。

## 并发与存储反馈

每 2 秒采样应用读取的网络块载荷量，并对增加一个并发槽位后的吞吐进行比较。未达到约 5% 改善则回退一个槽位，冷却两个采样周期后再探测。写入调用耗时占本窗口已完成请求的网络、分块哈希和写入工作时间超过 35% 时，减少一个槽位。范围为 --min-workers 的有效值到 --max-workers；默认不会低于初始并发。

初始原型曾仅因写入耗时占比就降到 1 个 worker，使真实传输变慢。修正后默认保留初始并发作为下限；如确需更低并发，必须显式设定 --min-workers。首次退化记录见 [初始对照](ADAPTIVE-INITIAL-RESULTS.zh-CN.md)。

这是启发式的应用层并发控制，不是 Aspera/FASP 的包级拥塞算法，也不替代 TCP/QUIC 的拥塞控制。WriteAt 可能先写页缓存，因此写调用耗时不是物理 RAID 吞吐；内核开始阻塞写入时才可能反映写回压力。最终 fsync 耗时单独统计。没有承诺完全识别网络拥塞、系统后台 I/O 或交换机队列。

降低目标并发不会取消正常在途块；它们完成后再减少新请求。worker 缓冲延迟分配，上限受池大小和分块大小限制，缩减并发后已经分配的缓冲仍保留至进程退出。默认 16×16 MiB 是下载块缓冲上限量级，另有 HTTP、状态和 Go 运行时内存。

## 来源选择

选块使用有界的近似稀缺块优先，并保留稀疏来源补选；原文件本机副本优先走本地读取。自适应启用时，对候选来源综合网络请求耗时 EWMA、本地在途数与远端上传占用评分，优先普通节点接力。每个接收者对一个来源最多两个并行块；没有机架模型或全局最优承诺。

HTTP 429 和其他远端失败会触发有上限的冷却，周期性取得新的 /status 不会清掉冷却。未知来源使用中性初始估值，调度没有机架拓扑信息。局部评分并不等于全局最优；多个接收端仍可能竞争同一个来源。

## 尾块与正确性

只在剩余未完成块数不超过当前并发目标、没有普通新块可调度、原请求已超过阈值时，尝试另一个持有该块的来源。每块最多两个不同来源，且仍受节点总并发上限约束。

每个副本先完整读取并通过清单绑定的块摘要。按块加锁后再检查 have；第一个有效副本才写入和增加 Downloaded/From，随后取消本块其他请求。已到达的第二个有效副本只记录 DuplicateBytes，不再写盘和计有效下载。坏副本不会取消仍正常进行的原请求。不同块可以并行写入。

仍保留所选模式的验块、fsync 和原子发布；full 还要验证实际文件内容，chunks 省略最终整文件复核。本地读写失败单独报错并保留恢复数据，不归咎远端。重启重新验块，不信任断电前的内存位图。

## 监控与字段

watch/distribute 显示动态 workers、SYNC / FINAL-HASH / PUBLISH 阶段及全局重试、尾块请求、取消和应用接收量。status JSON 与任务 result.json 新增 Performance，旧节点没有该字段时仍能查询。

| 字段 | 含义 |
|---|---|
| Stage | transfer、sync、final-hash、publish、complete 或 error |
| ActiveDownloads / TargetDownloads / MinDownloads / MaxDownloads | 在途请求、当前目标、有效下限、配置上限 |
| ActiveUploads / UploadLimit | 本节点上传占用与上限 |
| ScanSeconds | 启动时扫描或接受已有文件校验缓存的耗时 |
| TransferSeconds | peer 扫描后到全部块齐备的墙钟时间，包含发现、等待和调度 |
| FinalSyncSeconds / FinalHashSeconds / PublishSeconds | 最终显式 fsync、整文件哈希、发布及校验记录耗时 |
| NetworkWorkSeconds / ChunkHashWorkSeconds / WriteWorkSeconds | 所有并发请求的累计工作时间，不能当作串行阶段相加 |
| Requests / Retries / BusyResponses | 数据请求数、需要恢复的失败请求数、HTTP 429 数 |
| HedgeRequests / CanceledRequests / WorkerChanges | 重复尾块请求、已取消请求、并发目标变化次数 |
| ReceivedBytes / DuplicateBytes | 应用读取的块响应字节、完整有效但已提交过的副本字节 |
| SourceCount | 已学习来源数；高频状态不复制全部来源历史，避免放大集群状态流量 |

ReceivedBytes 包括应用已经读取的部分、损坏和重复块数据，不包含 HTTP/TCP 头、内核已接收但应用未读的数据或 TCP 重传，不能当作网卡字节。Downloaded 仍只统计唯一成功提交块。计数按 peer 进程生命周期累计，重启后不承诺累计连续；任务历史保存完成时的快照。

全局各节点阶段最大值可能来自不同节点且互相重叠，不能相加推导整批耗时。控制端部署和停止需看基准驱动的整体墙钟时间。

## 验证与复测

新增并发上限/写压力、来源速度与稀缺性、冷却保持、真实 HTTP 尾块取消、重复副本竞态、坏副本不取消原请求、本地写错和最终整文件校验测试。单元测试启用 race，另用实际 race 可执行文件执行三进程中断/损坏/接力恢复。

~~~bash
go test -race ./...
go vet ./...
SWARMCOPY_TEST_FLAGS='--adaptive --max-workers 8 --endgame' python3 scripts/integration.py
python3 scripts/benchmark-adaptive.py --help
~~~

正式对照使用 baseline / fixed / adaptive 三组，所有方法每次均为新目录并先清 OS 页缓存，计时包含停止做种。两轮顺序相反，只有两个样本，不能作普遍性能保证。这些是当时原型的历史对照，不代表当前默认 chunks、多文件和本地复制实现已重跑同一组测速。

## 参考

- [IBM：FASP 的链路适用范围](https://www.ibm.com/docs/en/connect-direct/6.2.0?topic=v62-using-fasp)
- [IBM：传输速率控制配置](https://www.ibm.com/docs/en/ahts/4.4.x?topic=csfcl-asperaconf-transfer-configuration-2)
- [Aspera：存储速率反馈](https://download.asperasoft.com/download/docs/csrv/3.2.1/linux/html/fasp/conf-file_system-cmd.html)

当前计数另含 LocalCopied/LocalReadWorkSeconds 和 SchedulerCalls/Candidates/WorkSeconds；本地复制不算网络接收。源码契约与完整工作量边界见[开发指南](DEVELOPMENT.zh-CN.md)。
