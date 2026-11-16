# 校验模式、算法与性能

[English](INTEGRITY.en.md) · [README](../README.md) · [示例](EXAMPLES.zh-CN.md)

同一个二进制提供可组合校验。v0.7.1 起，新建分发作业默认 `--verify chunks`（BLAKE3/BLAKE3）；显式 `--verify full` 使用 SHA256/SHA256 并复核最终文件。不提供完全跳过校验或抽样冒充完整校验的模式。

## 两种校验模式（v0.7.1）

日常只需选择 `--verify full` 或 `--verify chunks`：新建 `--source` 作业时，full 默认 SHA256 分块和文件摘要，并检查最终文件；chunks 默认 BLAKE3 分块和文件摘要，全部验块、fsync 后发布，省略最终整文件回读。v0.7.1 起省略 `--verify` 默认 chunks；需要最终整文件复核时显式使用 `--verify full`。无需再记两项 hash 参数。

```bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --dest /raid/images --verify chunks
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --dest /raid/images --verify full
```

`--chunk-hash` / `--file-hash` 保留为高级覆盖参数。续传和已有种子清单沿用原摘要算法，选择模式不会改写清单；旧 SHA256 作业切换 chunks 仍用 SHA256 验块。

## 模式选择

| 模式 | 接收分块 | 最终文件 | 适用场景 |
| --- | --- | --- | --- |
| full | 每块与可信清单比对 | 从暂存文件读取并计算密码学摘要，再发布 | 更强的落盘内容检查 |
| chunks（新作业默认） | 每块做密码学校验 | 全部分块完成、fsync 后发布；省略最终整文件回读 | 信任本地内存和存储、希望减少一次磁盘读取 |

full 的流水校验默认开启，通常与下载重叠；不会保证每次都是额外串行读取。chunks 无法发现成功验块、写入之后发生的内容损坏，不能称为与 full 完全等价。fsync 表示请求操作系统持久化，不是独立硬件读回保证。两种模式均保留续传和坏块重传。

chunks 不生成 full 已验证成品缓存。以后用 full 检查这些成品时仍需密码学验证，不能因为以前传输完成就免检。full 的缓存以清单 ID 和成品文件指纹为条件；`--recheck` 可强制绕过缓存。

## 算法组合

| 参数 | 允许值 | 作用 |
| --- | --- | --- |
| --chunk-hash | sha256、blake3、xxh3-128 | 分块定位、验收与重传 |
| --file-hash | sha256、blake3 | 来源文件与 full 最终文件校验 |
| --verify | full、chunks | 是否执行接收端最终文件回读 |

XXH3-128 是快速非密码学摘要，**只允许 full**，必须配 SHA256 或 BLAKE3 文件摘要；错误最终文件不能仅凭 XXH3 块通过而发布。chunks 必须使用 SHA256 或 BLAKE3 分块摘要。已有成品盘点仍使用文件摘要；chunks 不是所有阶段都不计算文件摘要。

~~~bash
# 默认：BLAKE3 分块及文件，chunks
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --dest /raid/images

# 快速验块 + 密码学文件校验；HGX167 的 CPU 微基准中表现较好
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --verify full --chunk-hash xxh3-128 --file-hash blake3

# 可信本地存储，省略最终回读；每块仍用 BLAKE3 校验
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --verify chunks

# 改变模式：重新检查并重启本作业的 peer，即便显式 --recheck=false
./swarmcopy resume --job <ID> --verify full
~~~

首次来源清单仍要完整读取源文件。chunks 不会省掉大文件初始读取，也不保证整体速度更快。进度、速率与 ETA 覆盖当前阶段；不能把多个阶段的预估简单视为总完成时间。

## 续传、兼容与清单

`jobs` 查 ID，`inspect --job <ID>` 查设置，`watch --job <ID>` 看全局状态，`resume --job <ID>` 恢复。同一作业可以改变 verify 模式，摘要算法由保存的清单固定；改算法请新建作业。保留控制器作业目录和节点暂存文件。

旧 v1/v2 的序列化和清单 ID 保持兼容；SHA256 新作业仍用 v2。自选摘要使用 v3，明确保存块／文件算法；文件 Digest 使用 `算法:摘要`，v3 必须由 v0.5+ 二进制处理，控制器会部署相应架构程序。树级 SHA256 是条目元数据摘要，不是整棵树字节拼接后的文件摘要。清单 ID 绑定算法、文件路径、大小和摘要。

新建空暂存文件不再扫描尚未写入的稀疏区；重启后已有部分文件仍会检查其内容，不依据长度直接认定完成。目录按文件逐个原子发布，不保证整个目录事务原子性。

## 硬件与性能证据

下面是实际节点的**内存双摘要微基准**：同一 1 MiB 输入流同时送入分块及文件摘要，GOMAXPROCS=1，400 ms/次、3 次，中位数，十进制 MB/s。不是磁盘、端到端传输或集群收益测试；不能用它承诺大文件分发提速倍数。

| CPU / 主机 | SHA256 + SHA256 | XXH3-128 + SHA256 | BLAKE3 + BLAKE3 | XXH3-128 + BLAKE3 |
| --- | ---: | ---: | ---: | ---: |
| Xeon 6776P / HGX167 | 560.13 | 1052.81 | 1157.99 | 1993.80 |
| Xeon 6746E / 头结点 | 871.89 | 1512.26 | 446.79 | 830.15 |

头结点的前三类参考测量与后加的 XXH3+SHA256 测量并非同一完整轮次，作为选型参考保留；HGX167 是同一轮参数下的全部组合。不同 CPU 上最快组合不同；v0.7.1 的新作业默认选 BLAKE3/chunks 是大文件分发场景的选择，不表示所有 CPU 上 BLAKE3 都最快；full 仍默认 SHA256。Go SHA256 可用 CPU 加速；所用 BLAKE3 实现提供 x86 AVX2/AVX512 路径，实际取决于 CPU 与运行库。aarch64 发布包完成静态交叉构建，本轮未在 ARM 硬件上测速。

真实双节点 HGX061/HGX066 对 139,460,641 字节、多文件及目录数据验证：默认 SHA256、两种混合 full、BLAKE3 full、两种密码学 chunks、QUIC chunks、chunks→full 升级、已有文件损坏修复均通过；最终另用 Python SHA256 比对所有文件。修复测试只重传 16 MiB 对应块。这是功能证据，不是与 rsync 的新大规模公平测速。

## SSH 默认项与扩展

所有控制器 SSH 调用默认包括：

~~~text
-o StrictHostKeyChecking=no
-o UserKnownHostsFile=/dev/null
-o LogLevel=quiet
-o CheckHostIP=no
~~~

它们是默认选项集合，后续可以继续添加选项，没有另设 strict 开关。用户可重复传入 `--ssh-opt KEY=VALUE`；键名大小写不敏感，同键最后一个显式值覆盖默认或旧作业设置，整个值作为一个 SSH 参数传递，不按 shell 再拆分。示例：

~~~bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --ssh-opt ConnectTimeout=20 \
  --ssh-opt ServerAliveInterval=15 --ssh-cipher aes128-gcm@openssh.com
./swarmcopy resume --job <ID> --ssh-opt ConnectTimeout=30
~~~

选项保存到作业，续传沿用。显式 `--ssh-cipher` 算法优先于 `--ssh-opt Ciphers=...`；auto/system 下显式 Ciphers 可以决定列表。SSH 用于登录部署和初始种子导入，节点间 TCP/QUIC 数据不是都通过 SSH。AES-GCM 能否更快仍取决于两端 CPU 与协商结果。


## 重现 CPU 微基准

```bash
GOMAXPROCS=1 go test -mod=vendor -run '^$' -bench '^BenchmarkIntegritySourcePipeline$' -benchtime=400ms -count=3 -cpu=1
```

[HGX167 raw CPU benchmark](integrity-bench-hgx167.txt).

最终候选还通过 BLAKE3/chunks 真正中断、停止及同一 ID 续传测试，确认复用有效部分，并独立 SHA256 比对。显式 `--recheck=false` 也不能绕过 chunks→full 升级重查。

## fsync 报错后如何恢复

数据 fsync 失败不会完成或发布该数据；保留作业和暂存。先排除磁盘空间、RAID/文件系统或 I/O 故障，原控制器仍在等待时先停止，再恢复同一作业：

```bash
./swarmcopy jobs
./swarmcopy stop --job <ID>
./swarmcopy resume --job <ID>
```

恢复会重新检查保留的数据，复用校验通过的块，补齐缺块/坏块，重新尝试完成；通常无需从头传整个文件。若写入后断电或设备丢数据，丢失块仍需补传；存储持续报错时不能保证完成。不要删除作业或使用 remove --purge-partials，否则会失去该恢复入口。

如果已经完成改名/链接，后续目录或完成标记 fsync 才报错，某些目标文件可能已存在，但作业仍不算成功；恢复会检查并复用有效文件。多文件发布逐文件进行，不是全目录事务。fsync 成功依赖操作系统和底层设备正确兑现刷盘请求，不能代替内容复核；需要额外最终内容检查时用 --verify full。
