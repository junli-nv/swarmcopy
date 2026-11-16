# 每节点网卡测速

[English](NETWORK.en.md) · [文档索引](README.md) · [使用示例](EXAMPLES.zh-CN.md)

默认就是 mesh：每个节点同时发送和接收。通常只需指定主机：

```bash
./swarmcopy benchmark --hosts 'HGX[603-608,610-618,620-653,655-665]'
```

程序用固定内存缓冲生成流量，接收即丢弃，无介质读写或内容摘要。SSH 只部署和控制；数据在所列节点间传输。控制节点不在列表时不参加数据收发。

## 看网卡是否压满

主表读取每节点路由接口的 Linux 字节计数器，不再用应用字节估计网口速度。自动显示接口、标称带宽及 RX/TX 占用率。例如实际 60 节点测试中的一行：

```text
NODE               INTERFACE/LINK       RX Gbit/s    TX Gbit/s RX/LINK TX/LINK STATE
HGX603             bond0/400G              227.75       226.00   56.9%   56.5% ACTIVE
```

- `bond0/400G` 是四张 100G 卡合计的每方向带宽；单节点 RX 超过 100G 正常，不能把它当作一张物理卡的速率。
- 运行中的 `NIC CURRENT` 使用相邻采样的计数差，约每两秒原地刷新。结束时 `NIC AVERAGE` 是预热之后完整测量窗口的平均值，两者明确区分。
- RX 和 TX 分别判断，不相加。不再突出集群 Aggregate，因为判断单机是否压满时它没有帮助。
- `RX/LINK`、`TX/LINK` 与探测到的标称带宽比较；平均达到 90% 的节点数单独列出。`ACTIVE` 只表示有流量，不表示压满。
- 计数包含同接口其他业务流量。bond 计数只读一次，不再叠加其成员卡，防止重复统计。四个物理成员的原始计数和速率另存 JSON。
- 网口未知、loopback、计数回退或时间区间异常会明确报错，不用应用统计代替，也不钳制或伪造速率。

这是合成网络流量，不执行 SCDP 文件选块、共同文件接力、落盘和发布；它不能代替真实文件分发测速。

## 默认输出保持简洁

正常终端只刷新当前表格，结束保留一张汇总表。屏幕放不下全部节点时优先显示占用率较低或异常的节点，避免刷新时滚屏；最终表包含全部节点。重定向输出或 `--plain` 时，只间隔输出一行进度和最终表格。默认隐藏 SSH 命令、进程 ID 和部署细节，异常仍会报告。

```bash
./swarmcopy benchmark --hosts @hosts.txt --duration 60s
./swarmcopy benchmark --hosts @hosts.txt --workers 128
./swarmcopy benchmark --hosts @hosts.txt --output network.json
./swarmcopy benchmark --help
./swarmcopy benchmark --help-all
```

普通帮助只列常用选项，完整帮助保留全部调优选项。`--verbose` 显示 SSH 和详细配置，`--plain` 禁用终端原地刷新。`--output` 只接受新文件；JSON 保留应用诊断数据、真实 NIC 计数、物理端口、异常分类和测量区间。

## 默认参数与可选调优

| 参数 | 默认 / 说明 |
| --- | --- |
| topology | mesh；全部节点收发，按各自位置均衡选源，并发少于节点数时不偏向列表前部 |
| duration / warmup | 30 秒正式测量，3 秒预热；部署和预热不计入速率 |
| workers / uploads / per-source | 每节点 64 路下载、256 路上传上限、每来源最多 64 路，均可设为 1～1024 |
| cpus | Go 并行度 32，按节点可用 CPU 数封顶；`--cpus 0` 遵循运行时默认，不等于 CPU 亲和性 |
| buffer-kib / block-mib | 每路 1 MiB 接收缓冲，单响应流式发送 64 MiB；不按响应长度分配内存 |
| port / transport | 19701 / TCP；分发默认仍为 19700；QUIC 需要同号 UDP，控制仍为 TCP |
| numa | system；默认不依赖 numactl，只有显式 NUMA 策略需要目标已有 numactl |

默认显式数据缓冲约 `64×1 MiB + 1 MiB = 65 MiB`，另有 Go、协议和内核 socket 内存。累计传输量和时长不会累计占用接收内存。程序不限速、不锁定物理内存；接收缓冲配置上限为每节点 1 GiB。并发越多不一定越快。

`--topology seed`、`ring` 是完整帮助中的特殊对照：seed 只由列表第一个节点发给其余节点，不接力；ring 每节点从前一个节点接收。这些不能当作默认 mesh。SSH 用户名、共享密码、cipher、重复 `--ssh-opt` 和双架构 `--bin-dir` 仍可使用。

## 异常如何解释

`BUSY` 明确表示来源上传槽位已满的 HTTP 429，并与连接、读取、其他 HTTP/协议错误分别统计。它不是网卡丢包数。忙时使用有界退避，避免大量固定间隔重试。结束会给出异常摘要和最近的具体错误；详细数据保存在 JSON。

没有请求错误只说明流量测试正常结束，不说明网口压满。若所有节点都有收发，但占用率只有 60%，就应该如实报告约 60%。

## LACP、MTU 与 NUMA

只读检查可用：

```bash
ip link show bond0
cat /sys/class/net/bond0/bonding/slaves
cat /sys/class/net/bond0/bonding/xmit_hash_policy
cat /proc/net/bonding/bond0
numactl --hardware
```

测试集群已配置 MTU 9000、802.3ad LACP、layer3+4 和四个活动 100G 端口。程序不会重新配置它们。MTU 需要整条路径支持；LACP 按流哈希，一条 TCP 流不会自动获得四卡总带宽。

先前核验的 HGX061/HGX066 是双 NUMA，每侧两张卡；将整个进程绑到一个 NUMA 仍会访问另外两张卡，不保证更快。需要对照时，保持主机、方向、并发、缓冲、计时一致，分别加 `--numa system`、`--numa 0`、`--numa 1` 或 `--numa interleave`。数字策略绑定 CPU 与内存，interleave 设置内存交错；显式策略使用现有 numactl。默认不强制绑定。

## 本次实际结果与证据

[60 节点真实 NIC 记录](data/network-nic-mesh-20261116.json) 保存参数、每节点 bond/物理端口计数及独立抽查。所用节点就是首页命令中的 60 台，默认参数、30 秒测量：

| 指标 | 最低 | 中位数 | 最高 |
| --- | --- | --- | --- |
| 每节点 NIC RX Gbit/s | 222.52 | 229.00 | 256.90 |
| 每节点 NIC TX Gbit/s | 212.02 | 229.95 | 264.44 |

60/60 节点都有实际数据收发，HTTP 429 和其他请求错误为 0，但平均 RX/TX 达到标称 90% 的节点都是 0/60：本轮没有压满。独立 SSH 抽查 HGX603、608、618、653、665 的 bond 与四个物理端口计数，与程序量级一致。另一次 HGX603–605 三节点短测约 396～397 Gbit/s/方向；这说明不能把小规模近线速结果推广到 60 节点 mesh，差距需进一步区分拓扑与程序开销。

[较早的应用字节测量](data/network-benchmark-20261116.json) 保留了单向、双向、并发和 NUMA 对照，包括较慢轮次。它的应用吞吐不是本页的 NIC 吞吐；不同硬件、规模、方向或统计口径不能直接拼成版本加速比。

## 停止与清理

Ctrl+C 停止本次基准，关闭 SSH stdin 租约并等待 peer 退出，正常收尾删除独占 `/tmp/.swarmcopy/benchmark-...`。不能确认退出或清理失败会报告目录，保留待检查数据。不使用全局 pkill，不停止其他作业。

基准不是可续传的文件作业，不出现在 jobs 中；再次执行是新测试。中断、节点失败或无法读取真实网卡计数会标记失败，不伪装为完整测速。
