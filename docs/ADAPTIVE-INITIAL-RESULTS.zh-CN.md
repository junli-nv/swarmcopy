# 自适应调度：45 节点对照结果

[English](ADAPTIVE-INITIAL-RESULTS.en.md) · [实现与指标说明](ADAPTIVE.zh-CN.md) · [机器可读结果](adaptive-initial-results.json)

本组对照没有测得自适应模式比已发布版本更快，不能把本原型宣传为性能提升；保持优化开关默认关闭。

同一 45 节点、1 个已准备种子和 44 个全新接收端，文件 40,188,444,672 字节。对照均使用 HTTP/TCP、同一源节点、相同默认上传槽位与初始下载并发，无人工速率上限。计时从控制端启动 distribute 到整批完成 SHA-256、fsync 并停止任务进程。

| 轮次 | v0.1.0 / 秒 | 新代码关闭优化 / 秒 | 新代码开启优化 / 秒 |
|---|---:|---:|---:|
| 1 | 85.03 | 85.17 | 101.14 |
| 2 | 82.97 | 82.97 | 104.75 |
| median | 84.00 | 84.07 | 102.94 |

开启优化相对 v0.1.0 的中位数比值为 **0.816×**，相对新代码关闭优化为 **0.817×**；大于 1 表示更快。两轮各方法范围：baseline 82.97–85.03 秒，fixed 82.97–85.17 秒，adaptive 101.14–104.75 秒。

## 分阶段诊断

| 轮次 / 模式 | transfer 最大秒 | fsync 最大秒 | 整文件哈希最大秒 | 失败请求 | 尾块重复请求 | 并发调整 |
|---|---:|---:|---:|---:|---:|---:|
| 1 / fixed | 36.00 | 6.16 | 41.40 | 3011 | 0 | 0 |
| 1 / adaptive | 55.00 | 2.34 | 41.94 | 3683 | 0 | 261 |
| 2 / adaptive | 61.00 | 4.68 | 42.20 | 4167 | 0 | 277 |
| 2 / fixed | 35.00 | 5.77 | 40.68 | 3448 | 0 | 0 |

各阶段最大值可能来自不同节点，且阶段在节点之间重叠，不能相加当作整批时间。transfer 包括 peer 发现、调度与等待；控制端部署和停止另计入整批时间。工作累计时间、HTTP 应用读取字节及逐节点数据见 [匿名记录](adaptive-initial-node-results.json)。ReceivedBytes 不等于网卡计数；尾块请求可增加流量，不能靠重复记账制造吞吐提升。

## 复核与边界

每个方法每轮均为 UUID 新目录；45 个节点全部通过目录不存在检查，再 sync/drop_caches=3，合计 270 次缓存屏障。两个轮次顺序相反：baseline→fixed→adaptive，以及 adaptive→fixed→baseline。正常传输重试允许，计时不中断、不重置。所有接收端 Origin=false、Downloaded 等于文件大小且 Complete=true，未复用其他轮次的数据。

最后核对 45 个不同内核 boot ID，六个作业共 270 个拥有身份的 peer 进程记录均已停止。原始任务、完整日志和节点身份保存在私有 run/compare45-initial，公开数据经过匿名化。源码和二进制摘要固定，每个方法前检查未改变；正式测试期间只改文档。

只有两个样本，不能据此宣称稳定的通用性能增益。OS 页缓存已清，设备缓存和后台负载未隔离。本表没有测商业 Aspera，也没有 UDP，更不能与之前 rsync 对照的倍数直接相乘。ARM64 只有交叉构建。

- [匿名节点数据](adaptive-initial-node-results.json)
- [全部缓存屏障](adaptive-initial-cache-barriers.json)
- [可复测驱动](../scripts/benchmark-adaptive.py)

在本次控制端查询任务：./swarmcopy jobs --state-dir ./run/compare45-initial；单项 --job ./run/compare45-initial/<case-id>-job。默认 jobs 不会自动搜索自定义目录。

[初始原型源码](https://github.com/junli-nv/swarmcopy/commit/82e5707c3ed60f90b5fbc02f8c4938ca4ddd7e6a)
