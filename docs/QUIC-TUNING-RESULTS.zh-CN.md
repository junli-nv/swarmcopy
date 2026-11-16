# UDP 实现审查：三项消融与完整结果

> 历史研究/验证记录：保留当时版本、参数与结果，不代表当前默认行为。当前使用方式见[项目首页](../README.md)和[文档索引](README.md)。

[English](QUIC-TUNING-RESULTS.en.md) · [实现与参数](QUIC-TUNING.zh-CN.md) · [原始较慢结果](QUIC-RESULTS.zh-CN.md)

同一45节点、1种子＋44全新接收端，文件40,188,444,672字节（37.43GiB）。以下每张表内部使用同一冻结二进制、同一介质与配置，仅改变标明的变量。不同试验不合并计算倍数。

## 最终同一候选：TCP 与 GSO 批次

全部 UDP 方式均使用4个客户端socket分片、8952字节协商负载上限；每个包仍遵守对端与路径限制，逐包拥塞和 pacing 条件保留。

| 轮次 | TCP / 秒 | UDP 20KiB GSO / 秒 | UDP 64KiB GSO / 秒 |
|---|---:|---:|---:|
| 1 | 65.06 | 77.05 | 71.12 |
| 2 | 66.46 | 74.96 | 69.22 |
| median | 65.76 | 76.01 | 70.17 |

64KiB 相对20KiB耗时变化为 **-7.7%**，正数表示变慢；TCP/64KiB UDP中位数比值为 **0.937×**，大于1才表示UDP更快。仍只代表这套集群的两个观测，不是所有网络、硬件或规模的保证。TCP 保持默认；v0.2.0 为正式稳定版本。

## 单变量一：共享接收 socket

相同代码、1452字节上限、20KiB GSO，1与4个客户端socket。

| 轮次 | 单 socket / 秒 | 4 socket / 秒 |
|---|---:|---:|
| 1 | 157.03 | 112.91 |
| 2 | 153.23 | 117.05 |
| median | 155.13 | 114.98 |

中位数耗时减少 25.9%。CPU采样和该消融支持接收通路并行度是初版的实际限制之一；没有增加下载worker数量。

## 单变量二：包长上限

相同候选、4socket、20KiB GSO；TCP、UDP上限1452与8952。初始包保持保守，通过PMTU探测验证后才增大。

| 轮次 | TCP / 秒 | UDP 上限1452 / 秒 | UDP 上限8952 / 秒 |
|---|---:|---:|---:|
| 1 | 69.17 | 112.85 | 82.72 |
| 2 | 65.17 | 113.08 | 79.07 |
| median | 67.17 | 112.97 | 80.90 |

大包UDP相对小包UDP中位数耗时减少 28.4%。输入/校验完全相同；同一程序中也保留TCP对照。

## 阶段与内核计数

| 试验 / 轮次 / 模式 | 传输阶段最大秒 | 哈希尾部最大秒 | 节点UDP入站报文增量 | 接收缓冲错误增量 |
|---|---:|---:|---:|---:|
| shards / 1 / single | 141.00 | 1.55 | 1347347191 | 1242149 |
| shards / 1 / sharded | 95.00 | 1.95 | 1320052925 | 104120 |
| shards / 2 / sharded | 99.00 | 2.65 | 1319549558 | 94826 |
| shards / 2 / single | 138.00 | 0.15 | 1348377357 | 1281904 |
| pmtu / 1 / tcp | 42.00 | 22.28 | 16682 | 0 |
| pmtu / 1 / small | 96.00 | 2.68 | 1319396997 | 148696 |
| pmtu / 1 / jumbo | 63.00 | 2.48 | 224383022 | 2943 |
| pmtu / 2 / jumbo | 61.00 | 1.83 | 224292072 | 6871 |
| pmtu / 2 / small | 96.00 | 0.79 | 1319185665 | 163466 |
| pmtu / 2 / tcp | 36.00 | 19.77 | 15831 | 0 |
| gso / 1 / tcp | 38.00 | 20.09 | 16468 | 0 |
| gso / 1 / gso20 | 60.00 | 2.57 | 224700896 | 5192 |
| gso / 1 / gso64 | 55.00 | 1.20 | 221047782 | 14275 |
| gso / 2 / gso64 | 53.00 | 3.00 | 221089953 | 9515 |
| gso / 2 / gso20 | 59.00 | 1.38 | 224307537 | 10032 |
| gso / 2 / tcp | 36.00 | 20.88 | 15160 | 0 |

阶段最大值可能来自不同节点，不能相加；哈希尾部变小不代表省略校验。内核计数为全部参与节点的整机增量，包含潜在背景流量。InDatagrams是接收统计；OutDatagrams等发送统计可能受GSO口径影响，不当作线速包数或精确系统调用数。RcvbufErrors也不等同于本任务全部丢包。

## 正确性、公平性与保留结果

包括初版在内共22次大文件传输：原始三模式六次、socket消融四次、PMTU六次、GSO六次。990次计算节点缓存屏障成功，990个任务进程记录停止审计通过；每次核对45个不同boot ID。正式轮次不重叠，也不运行CPU profiler。

每次接收端目录从未存在，先sync/drop_caches=3，不清BCM头节点缓存。共同准备种子导入不计时；部署、传输、正常重试、整文件SHA-256、fsync、原子发布、停止均计时。固定4下载worker、4上传槽、无限速，自适应并发与endgame关闭。每个接收端Origin=false、Downloaded与FileHashBytes均为完整文件大小、Complete=true。UDP接收端有实际HTTP/3记录，不能静默回退TCP。

原始实现约156秒的结果完整保留，源代码检查点为6ec0d33；socket实现为724883e；PMTU实现为d459d31。最终候选摘要、部署文件和驱动摘要均在公开JSON中，不用优化后数据覆盖最初结果，也不将各阶段比值相乘。

CPU诊断为另一次5节点8GiB带采样试验，不能当作正式速度结论。某接收节点的系统调用约23%、AES解密约4%，不能据此推断全体节点的CPU分布。实际socket探针为14MiB，不将普通256KiB的sysctl上限冒充实际socket容量。

一次功能预检中的抓包断言混淆了BPF整包长度与UDP负载长度而报错；原8GiB传输和整文件校验已成功。依据同一份抓包修正断言，记录负载7993/8232字节，未重跑/续传正式测速以替换结果。

## 证据和边界

- socket：[汇总](data/quic-shards-results.json)、[节点](data/quic-shards-nodes.json)、[缓存屏障](data/quic-shards-barriers.json)、[内核增量](data/quic-shards-kernel-deltas.json)
- PMTU：[汇总](data/quic-pmtu-results.json)、[节点](data/quic-pmtu-nodes.json)、[缓存屏障](data/quic-pmtu-barriers.json)、[内核增量](data/quic-pmtu-kernel-deltas.json)
- GSO：[汇总](data/quic-gso-results.json)、[节点](data/quic-gso-nodes.json)、[缓存屏障](data/quic-gso-barriers.json)、[内核增量](data/quic-gso-kernel-deltas.json)
- [匿名CPU诊断](data/quic-cpu-diagnosis.json)、[实际缓冲](data/quic-socket-probe.json)、[真实入站包长](data/quic-jumbo-ingress.json)

UDP方案使用HTTP/3、QUIC和TLS，TCP数据仍是明文；不是仅切换IP协议号，也不是IBM FASP性能测试。未模拟WAN、未隔离设备缓存及后台负载，没有改变全局网络/sysctl/MTU配置。ARM64仅交叉构建；传输中MTU下降恢复尚未验证。新包长和GSO扩展是明确记录的本地依赖补丁，不冒充未经修改的上游发行版。

实际作业查询使用各控制端试验父目录作为--state-dir；单项--job使用<case-id>-job路径。默认jobs不递归搜索自定义目录。完整日志、实际主机列表、任务token、CPU profile与原始抓包均留在控制端私有run/，不公开。
