# 内存网络基准与吞吐排查

[English](NETWORK.en.md) · [文档索引](README.md) · [使用示例](EXAMPLES.zh-CN.md)

`benchmark` 测量计算节点之间的应用层有效载荷吞吐：启动时生成一小段随机数据并循环发送，接收端计数后丢弃。不读取介质、不保存接收内容、不计算内容摘要。它使用独立的流式测试端点，不执行真实分发的选块、块转发、落盘或发布，因此不是文件分发速度承诺。

## 开始测试

```bash
# 多节点同时收发；默认 64 路下载、256 路上传上限，预热 3 秒、计时 30 秒
./swarmcopy benchmark --hosts 'HGX[061,066,082,138,169]'

# 两节点单向，接近 iperf 默认的方向口径：第一个节点发，第二个节点收
./swarmcopy benchmark --hosts 'HGX[061,066]' --topology seed \
  --workers 64 --per-source 64 --uploads 128 \
  --cpus 32 --buffer-kib 1024 --warmup 3s --duration 30s \
  --output network-forward.json

# 交换主机顺序即可反向；报告路径须为新文件
./swarmcopy benchmark --hosts 'HGX[066,061]' --topology seed \
  --workers 64 --per-source 64 --cpus 32 --buffer-kib 1024

./swarmcopy benchmark --help
```

控制节点只通过 SSH 部署和维持进程租约，通过 HTTP 控制和查询。有效载荷始终在所列计算节点之间传输。控制节点自己列入 hosts 时也会参与数据收发。默认专用端口 19701，可用 `--port` 修改；不会占用分发的默认 19700。

SSH 使用相同的用户名、密码、cipher 和重复 `--ssh-opt` 机制。可加 `--bin-dir ./dist` 部署对应架构；其中的二进制需与控制端匹配。默认不需要额外测速软件。显式指定非 system 的 NUMA 策略时，目标需已有 `numactl`。

## 拓扑、并发与内存

| 参数 | 含义 |
| --- | --- |
| `--topology mesh`（默认） | 每节点主动从其他节点下载，同时为其他节点发送；并发不足以覆盖全部主机对时只建立相应数量的流 |
| `--topology ring` | 每节点从前一个节点下载；各节点发送自己的随机缓冲，不转发一个共同文件 |
| `--topology seed` | 第一个节点给其余节点发送；其余节点不接力，用于单向及单源带宽测试 |
| `--workers 64` | 每节点下载流总数，范围 1～1024 |
| `--per-source 64` | 每个来源最多的下载流，范围 1～1024；实际总数取 workers 与来源数×上限的较小值 |
| `--uploads 256` | 每节点同时发送的响应上限，范围 1～1024；过小会出现 busy/失败计数 |
| `--buffer-kib 256` | 每路接收缓冲，加一份共享发送缓冲；范围 16～1024 KiB |
| `--block-mib 64` | 单响应流式发送总量，不分配这么大的内存；范围 1～1024 MiB |
| `--cpus 0` | Go CPU 并行度，0 遵循运行时默认；不是 CPU 亲和性或物理核数 |
| `--transport quic` | 可选 QUIC，仍需要 TCP 控制端口及同号 UDP；不回退到 TCP 数据 |

默认显式数据缓冲约为 `64×256 KiB + 256 KiB = 16.25 MiB`。总进程内存还包括 Go、连接、协议和统计开销；内核 socket 内存另计。增加时长、累计传输量或 block-mib 不会保留更多接收内容。接收缓冲总配置上限为每节点 1 GiB。程序不锁定物理内存，系统自身的 swap 策略仍适用。

测试不限速。提高并发可能改善 LACP 分流，也可能增加调度和内存开销，不能认为 128/1024 一定比 64 更快。

## 如何读结果

每两秒显示剩余时间、各节点从预热后开始累计的平均 RX/TX Gbit/s、接收 GiB 和失败请求数。Gbit/s 为十进制，GiB 为二进制。`Aggregate RX` 是各节点平均接收速率之和，不再加 TX，避免同一流量计算两次。

预热与部署不计入吞吐。各节点使用本机单调时钟，JSON 保存实际 Seconds，不依赖机器时钟同步；控制请求的起止有少量偏差。统计是应用实际读取/写入的有效载荷，包括失败响应中已读到的部分，不是唯一文件字节、网卡线速或 TCP 重传计数。QUIC 即使没有内容哈希仍有 TLS/AEAD 验证成本。

4×100G 的 bond 表示每节点每方向合计标称 400 Gbit/s，约 50 GB/s。双向测试可同时接收和发送；两节点的 Aggregate RX 上限口径是 800 Gbit/s，不能拿它当某一台的 400 Gbit/s。网格测试与单向 iperf 也不能直接比较。

## LACP 与 NUMA 检查

```bash
ip link show bond0
cat /sys/class/net/bond0/bonding/slaves
cat /sys/class/net/bond0/bonding/xmit_hash_policy
cat /proc/net/bonding/bond0
numactl --hardware
```

测试集群已确认 bond0 和成员 MTU 9000、802.3ad LACP、layer3+4、四个活动 100G 端口。程序不修改这些系统配置。MTU 需要整条路径支持；layer3+4 是按流哈希，一条 TCP 流不会自动合并四张卡，接收侧分流还涉及交换机。

HGX061/HGX066 都是双 NUMA：ens1 的两个端口归 NUMA 0，ens3 的两个端口归 NUMA 1。将整个进程绑定一个 NUMA 仍会访问另一侧的两张卡，不保证改善。可保持其他参数完全相同做以下对比：

```bash
./swarmcopy benchmark --hosts 'HGX[061,066]' --workers 64 --per-source 64 --cpus 32 --buffer-kib 1024 --numa system
./swarmcopy benchmark --hosts 'HGX[061,066]' --workers 64 --per-source 64 --cpus 32 --buffer-kib 1024 --numa 0
./swarmcopy benchmark --hosts 'HGX[061,066]' --workers 64 --per-source 64 --cpus 32 --buffer-kib 1024 --numa 1
./swarmcopy benchmark --hosts 'HGX[061,066]' --workers 64 --per-source 64 --cpus 32 --buffer-kib 1024 --numa interleave
```

数字策略通过 numactl 同时绑定 CPU 和内存；interleave 只设置跨可用 NUMA 的内存交错。默认 system 不加绑定。比较时统一方向、节点、并发、缓冲、CPU 并行度、预热和计时，交替顺序复测；同时检查各物理端口计数、CPU/softirq、重传和拓扑。不要直接照搬某台机器的 NUMA 编号。

## 实测证据与结论边界

[原始记录](data/network-benchmark-20261116.json) 保留各轮参数及结果，包含较慢轮次。这些是短时排查，不是多轮统计性能保证；调参改变不代表程序版本对照。

- 五节点 mesh、64 workers 的 20 秒测试合计约 1.18 Tbit/s；128 workers 的另一次 10 秒测试约 1.26 Tbit/s。独立 bond 计数与应用量级一致；五节点进程 RSS 约 93～96 MiB，接收内容不累计占内存。
- 两节点双向、64 workers、Go 并行度 32、1 MiB 缓冲的 NUMA 对照：system 约 697.4 Gbit/s，绑定 0/1 和 interleave 约 672.4/678.0/683.3 Gbit/s。这一组没有支持“强制 NUMA 更快”，所以不改默认。
- 同一硬件改为两节点单向、64 workers、Go 并行度 32、1 MiB 缓冲后，正向约 388.5 Gbit/s，反向约 387.7 Gbit/s，接近 400G 标称速率。独立计数显示四卡发送各约 97.6～98.4 Gbit/s，接收各约 96.1～99.7 Gbit/s。

因此不能把双向/网格成绩直接解释为链路只有 200～350G，也不能据此把真实文件分发差距全部归于哈希。文件分发还需分别分析存储、验块、来源请求上限、选块、并发和最终收尾。这里没有修改正常分发的校验或调度。

## 停止、报告与清理

Ctrl+C 结束本次基准，并关闭 SSH stdin 租约使目标退出；断开后还有有界进程 lifetime。正常退出会清理本次 `/tmp/.swarmcopy/benchmark-...` 部署。无法确认退出或清理失败会报错并打印目录，不宣称干净完成。不调用全局 pkill，不清理其他作业。

基准不创建可续传的文件作业，不出现在 `jobs`，无需 resume；下一次重新测量。只有 `--output` 才在控制端保存新 JSON，拒绝覆盖已有文件。中断或节点失败的报告标记 failed，不能当作成功成绩。
