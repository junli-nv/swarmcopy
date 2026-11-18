# 校验模式与完整性

[English](INTEGRITY.en.md) · [示例](EXAMPLES.zh-CN.md) · [文档索引](README.md)

## 选择模式

| 新作业模式 | 摘要预设 | 接收端完成门槛 |
| --- | --- | --- |
| chunks（默认） | BLAKE3 块与文件摘要 | 每块检查、全部块完整、fsync、逐文件发布 |
| full | SHA256 块与文件摘要 | 在验块之外，按清单复核实际文件内容，再完成发布 |

```bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --dest /raid/images
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --dest /raid/images --verify full
```

chunks 不等于关闭校验，不采样冒充完整性；它省掉最终整文件内容复核。首次可信来源清单仍完整读取文件，已有副本仍须通过文件摘要或可信缓存。direct 启动盘点只查元数据/缓存，未缓存内容在 peer 后台验证后才可信；声明可用的分块必须通过绑定摘要，或来自仍有效的完整文件校验缓存。full 的文件校验默认流水进行，可能与下载重叠，不保证总是额外串行读一遍。

chunks 无法检测验块写入后发生的内容损坏；两种模式不能称为完全等价。fsync 是持久化请求，依赖文件系统、RAID/磁盘正确实现，不是独立内容验证或物理介质读回保证。

## 算法与高级组合

| 参数 | 允许值 | 约束 |
| --- | --- | --- |
| --chunk-hash | sha256、blake3、xxh3-128 | chunks 仅允许密码学块摘要；XXH3 必须 full |
| --file-hash | sha256、blake3 | 来源与 full 的密码学文件摘要 |
| --verify | chunks、full | 新作业采用预设；显式摘要参数可覆盖 |

```bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --verify full --chunk-hash xxh3-128 --file-hash blake3
```

非密码学 XXH3 的匹配不能绕过最终强文件摘要。具体 CPU 上的最快组合不同，不按下面微基准承诺端到端倍数。流水参数及文件摘要计时见[流水校验](PIPELINE.zh-CN.md)，SSH 选项见[操作示例](EXAMPLES.zh-CN.md)。

## 清单、缓存与兼容

摘要算法绑定在可信清单中。SHA256 树可用 v2；自定义算法和 BLAKE3 默认预设用 v3，文件 Digest 明确标注算法。旧 v1/v2 编码与 ID 不变；树级 SHA256 是条目元数据摘要，不是所有文件字节拼接哈希。

resume 沿用保存模式与算法；显式改变模式会强制重查/重启 peer，但不会重写清单。旧缺省模式仍表示 full，旧 seed 清单继续使用原算法。

```bash
./swarmcopy resume --job <ID> --verify full
./swarmcopy resume --job <ID> --verify chunks
```

chunks 完成不生成 full 已验证成品缓存；以后 full 检查不能因此免验。目标 `.swarmcopy/cache.json` 绑定完整清单 ID 与各文件 device/inode/size/mtime/ctime；--recheck 强制内容读取。旧 verified.json 按需兼容，不能只用文件大小或目录 mtime 接受数据。

## I/O 错误与恢复

数据 fsync 错误不能标记 Complete，暂存应保留。先修复空间、文件系统、RAID 或 I/O 问题；原控制器还在等待时先 stop，再恢复同 ID：

```bash
./swarmcopy jobs
./swarmcopy stop --job <ID>
./swarmcopy resume --job <ID>
```

恢复重验保留块，复用有效部分，只补缺块/坏块；断电丢失的数据仍需补传。不要提前 remove/purge。后续目录或完成标记 fsync 可能在文件已改名后失败，此时部分成品存在也不代表整个作业成功。发布逐文件原子；源和目标需要避免外部并行写入。

自动清理只处理完整完成作业的部署和空暂存，不删除失败/共享的数据。durability_test.go 的 EIO 注入验证刷盘错误路径和恢复，不代表真实断电或 RAID 故障实测。更多边界见[清理规则](CLEANUP.zh-CN.md)与[文件布局](TREES.zh-CN.md)。

## 历史 CPU 微基准（不是集群吞吐）

下面是实际节点的**内存双摘要微基准**：同一 1 MiB 输入流同时送入分块及文件摘要，GOMAXPROCS=1，400 ms/次、3 次，中位数，十进制 MB/s。不是磁盘、端到端传输或集群收益测试；不能用它承诺大文件分发提速倍数。

| CPU / 主机 | SHA256 + SHA256 | XXH3-128 + SHA256 | BLAKE3 + BLAKE3 | XXH3-128 + BLAKE3 |
| --- | ---: | ---: | ---: | ---: |
| Xeon 6776P / HGX167 | 560.13 | 1052.81 | 1157.99 | 1993.80 |
| Xeon 6746E / 头结点 | 871.89 | 1512.26 | 446.79 | 830.15 |

头结点的前三类参考测量与后加的 XXH3+SHA256 测量并非同一完整轮次，作为选型参考保留；HGX167 是同一轮参数下的全部组合。不同 CPU 上最快组合不同；当前新作业默认选 BLAKE3/chunks 是大文件分发场景的选择，不表示所有 CPU 上 BLAKE3 都最快；full 仍默认 SHA256。Go SHA256 可用 CPU 加速；所用 BLAKE3 实现提供 x86 AVX2/AVX512 路径，实际取决于 CPU 与运行库。aarch64 发布包完成静态交叉构建，本轮未在 ARM 硬件上测速。

历史 v0.5.0 双节点 HGX061/HGX066 对 139,460,641 字节、多文件及目录数据验证：默认 SHA256、两种混合 full、BLAKE3 full、两种密码学 chunks、QUIC chunks、chunks→full 升级、已有文件损坏修复均通过；最终另用 Python SHA256 比对所有文件。修复测试只重传 16 MiB 对应块。这是功能证据，不是与 rsync 的新大规模公平测速。


[原始内存微基准](data/integrity-bench-hgx167.txt)；使用 `go test -mod=vendor -run '^$' -bench '^BenchmarkIntegritySourcePipeline$'` 可运行开发基准。当前硬件、缓存和后台负载都会影响结果。
