# swarmcopy

用于 Linux 集群的可续传 P2P 文件与目录分发工具，协议为 **SCDP/1**。节点收到并验证的分块可以继续提供给其他节点，减轻 BCM/NFS 头结点和单个种子的带宽压力。

[下载](https://github.com/junli-nv/swarmcopy/releases/latest) · [English](README.en.md) · [文档索引](docs/README.md) · [完整示例](docs/EXAMPLES.zh-CN.md)

## 安装与 SSH

运行需要 Linux、控制端 OpenSSH 客户端，以及目标节点的 SSH 服务和基本 shell 工具。无需 pdsh、rsync、Python、Go 或常驻服务。一个二进制包含全部功能。

从下载页将两种架构的二进制和 `SHA256SUMS` 放入 `dist/`，然后在控制节点执行：

```bash
(cd dist && sha256sum -c SHA256SUMS)
cp dist/swarmcopy-linux-x86_64 ./swarmcopy
chmod +x ./swarmcopy dist/swarmcopy-linux-*
./swarmcopy version
```

ARM 控制节点将复制来源改为 `swarmcopy-linux-aarch64`。保留 `dist/` 中的两种原名文件，程序可按目标架构自动部署；同架构集群也可以只部署控制端自身。x86_64 已做实机验证，aarch64 为静态交叉构建，尚未做 ARM 实机验证。

先确保**运行命令的节点能 SSH 登录全部目标，包括目标列表中的本机**。可使用 SSH 密钥、agent、SSH config，或加 `--ssh-user root --ask-pass`，隐藏输入一次全部节点共用的密码。程序不会自动建立 SSH 互信。SSH 默认关闭主机密钥检查，具体选项见[示例](docs/EXAMPLES.zh-CN.md)。

## 快速开始

推荐在持有介质的计算节点上执行，并把本机列入目标。最常见的用法：在 HGX167 上将 `/raid/nemo*` 分发到各节点相同的 `/raid` 路径。

```bash
./swarmcopy distribute \
  --hosts 'HGX[061,066,082,138,169,167]' \
  --source '/raid/nemo*' \
  --dest /raid
```

本机原文件经过检查后直接复用并供种，不再复制到自己；其他节点之间按已验证分块互传。若目标路径不同，本机目标通过本地读取、验块、写入生成副本。已有完整副本的节点不重复下载。

源文件在**执行命令的机器**上。若在 BCM 头结点执行且它不在目标列表，程序会在需要时先经 SSH 将介质复制到一个计算节点，再进行 P2P 分发：

```bash
./swarmcopy distribute --hosts 'HGX[061,066,082]' \
  --source /shared/nemo.sqsh --dest /raid/images
```

启动时的 `SOURCE MODE: direct/staged` 说明选择的方式。`hashing-source` 是建立可信来源清单，首次要完整读取源；来源未变时可复用缓存。`importing` 是初始种子复制，不是解压或导入容器。准备和传输阶段显示进度；当前阶段的 ETA 不等于全任务剩余时间。

## 多文件、目录与已有文件

```bash
# 多个文件直接放在目标目录下
./swarmcopy distribute --hosts @hosts.txt --dest /raid/images \
  --source /shared/nemo.sqsh --source /shared/config.json

# 目录保留自身名称：得到 /raid/images/models/...
./swarmcopy distribute --hosts @hosts.txt \
  --source /shared/models --dest /raid/images
```

给通配符加引号，由程序展开；`'/raid/nemo*'` 匹配 `/raid` 直属的同名前缀条目，不会搜索 `/raid/images`。匹配到目录时递归分发，保留内部结构和空文件/空目录。多个来源不能有重复顶层名称或重叠的父子根。输入列表在作业创建时固定，续传不会加入后来新增的匹配项。

同名同内容的文件会复用；仅部分节点缺失时只补齐缺失数据。同名不同内容默认询问处理方式，脚本可明确选择：

| 参数 | 行为 |
| --- | --- |
| `--existing resume` | 复用旧文件中校验通过的分块，补齐后替换 |
| `--existing replace` | 冲突文件重新接收，校验成功后替换 |
| `--existing keep` | 遇到内容冲突时取消，保留旧文件 |

当来源节点在目标列表中（SOURCE MODE: direct）时，启动盘点只检查元数据和有效的内容校验缓存；同名同尺寸但没有可信缓存的文件仍待校验。peer 先开放状态接口，再后台读取已有内容，逐块验证、逐块供其他节点下载；本机完成初始化后才开始补齐缺块。因此慢盘节点不会让所有节点等待一轮完整文件校验。staged 模式仍先完整盘点，以选择已有完整种子、避免不必要的初始 SSH 复制。

watch 中的 VERIFYING LOCAL 表示正在验证已有内容，已验证分块可以供种，不表示节点离线或任务完成。完整验证的文件逐步保存缓存检查点，取消后可复用。仅靠文件名、尺寸不会供种。同尺寸冲突可能到后台校验时才发现；默认仍询问，非交互任务应明确指定 --existing resume、replace 或 keep。已选择的策略同样适用于随后发现的冲突。原文件保留到替代文件校验并发布成功。

发布按文件原子进行，不是整个目录一次性切换。不会删除目标上额外的文件，因此不是带删除语义的目录镜像。普通文件和目录受支持，符号链接及特殊文件不支持；详细边界见[文件布局设计](docs/TREES.zh-CN.md)。传输期间不要由其他程序修改源或目标文件。

## 监控、停止与续传

启动时会打印 job ID，之后在同一控制节点查询：

```bash
./swarmcopy jobs
./swarmcopy inspect --job <ID>
./swarmcopy watch --job <ID>
./swarmcopy status --job <ID>
./swarmcopy stop --job <ID>
./swarmcopy resume --job <ID>
```

`watch` 是全局文本进度，`status` 输出 JSON；不指定 ID 时选择最近活动作业，否则最近保存作业。成功后自动打印耗时和平均速度，并保存供以后查看。网络、本地复制、复用字节分别统计；集群合计速度不是单节点网速。

单节点失败不会取消健康节点，但失败节点仍计入未完成总数。恢复节点后，用同一 ID 续传；保留控制器的 `~/.local/state/swarmcopy/jobs`（遵循 `XDG_STATE_HOME`）及节点暂存。初始来源哈希和未完成的 SSH 种子复制需要重做对应阶段；P2P 数据会重新验块并复用有效部分。

退出 `watch` 只退出监控。Ctrl+C 停止分发控制器后，独立 peer 可能继续运行；要明确停止本作业，请用 `stop --job`，不要用全局 `pkill swarmcopy`。更多显示和取消说明见[进度指南](docs/PROGRESS.zh-CN.md)。

## 校验模式

| 模式 | 新作业摘要预设 | 完成条件 |
| --- | --- | --- |
| `chunks`（默认） | BLAKE3 分块和文件摘要 | 每块校验成功、fsync、发布；省略最终整文件复核 |
| `full` | SHA256 分块和文件摘要 | 在验块之外，复核最终文件内容，再完成发布；可与传输流水重叠 |

日常无需填写校验参数；需要最终内容复核时加 `--verify full`。旧作业续传保留保存的模式与算法。fsync 依赖底层存储正确兑现刷盘请求，不代替内容校验；chunks 不检测验块写入后发生的内容损坏。存储错误排除后可以同 ID 续传，重新检查保留数据。高级算法组合与完整性边界见[校验指南](docs/INTEGRITY.zh-CN.md)。

## 完成后清理

所有目标完成校验与发布后，默认停止 peer，清掉本作业部署和空暂存。目标下统一使用一个 `.swarmcopy`，只复用一份 `cache.json` 和最多 8 MiB 的 `last-peer.log`，不按任务永久堆积，也不把介质搬到用户目录。

| 操作 | 用途 |
| --- | --- |
| `stop --job <ID>` | 停止本作业，保留续传数据 |
| `cleanup --job <ID>` | 为旧完成作业或清理失败重试收尾，保留作业记录 |
| `remove --job <ID>` | 删除作业记录和部署；`delete` 为别名 |
| `remove --job <ID> --purge-partials` | 另行删除本作业独占暂存，放弃对应续传数据 |

中断、共享、活动锁或无法确认归属的数据不会自动删除。`--keep-seeding` 会保留供种进程，之后用 `cleanup` 收尾。清理失败会明确报告并保留可重试记录。详见[目录与清理规则](docs/CLEANUP.zh-CN.md)。

## 网络测速

```bash
./swarmcopy benchmark --hosts 'HGX[603-608,610-618,620-653,655-665]'
```

默认 mesh，每节点同时收发；主表显示真实网卡的 RX/TX、标称带宽和占用率，终端原地刷新，结束保留一张平均速率表。默认 64 路并发，只有需要时才调参数；详细帮助用 `benchmark --help-all`，部署日志用 `--verbose`。固定缓冲、接收即丢弃，无介质读写或内容哈希。实测、NIC 统计口径和 NUMA 说明见[网络基准指南](docs/NETWORK.zh-CN.md)。

## 常用参数与网络

| 参数 | 默认值 / 说明 |
| --- | --- |
| `--hosts` | 主机列表、`'node[001-005]'` 或 `@hosts.txt`；建议给方括号加引号 |
| `--workers` / `--uploads` | 每节点下载/上传并发均为 4，范围 1～32 |
| `--download-mib` / `--upload-mib` | 每节点聚合 MiB/s，默认 0，不限速 |
| `--port` | peer 端口默认 19700，可改，例如 `--port 19800` |
| `--adaptive` | 默认关闭，只在显式选择时自适应调整下载并发 |
| `--transport quic` | 显式使用 UDP/QUIC；默认仍为 TCP |
| `--ssh-opt KEY=VALUE` | 可重复，扩展或覆盖 SSH 选项；随作业保存 |

控制节点需能 SSH 和 HTTP 访问全部目标，节点之间需互通。peer 监听所有本机地址；实际网卡由主机名解析和路由决定，不会自动聚合网卡。QUIC 还使用同号 UDP 端口；direct 来源另用动态端口，由 `SOURCE READY` 显示。BCM 只能访问一个节点时，请登录该计算节点执行分发。TCP 数据与 HTTP 控制为明文，应在可信集群网络使用。

## 文档与开发

[文档索引](docs/README.md) 包含全部主题及历史测试报告。按需阅读[完整示例](docs/EXAMPLES.zh-CN.md)、[来源与节点故障](docs/SOURCE-MODES.zh-CN.md)、[开发指南](docs/DEVELOPMENT.zh-CN.md)。性能报告记录其测量版本和限制，不代表任何规模下的速度保证；版本变化见 [CHANGELOG](CHANGELOG.md)。

仓库只维护 `main`，同一个程序包含所有功能。构建需 Go 1.26+，依赖已 vendor；编译、测试、协议兼容和发布流程见开发指南。
