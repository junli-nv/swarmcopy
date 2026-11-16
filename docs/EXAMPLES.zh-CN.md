# 常用示例

[English](EXAMPLES.en.md) · [中文 README](../README.md)

以下是统一版 main 的用法。将二进制命名为 `swarmcopy`；异构集群把两种原名二进制放入同级 `dist`（自动识别），或每条命令指定 `--bin-dir /path/to/dist`。示例节点和路径请换成实际值。

## v0.7.0：本地复制与完成摘要

调度避免逐次对全部缺块做昂贵评分；源所在节点的目标副本直接读取本地源、校验后写入，不再经网络绕回。成功结束打印总耗时、准备/分发耗时、网络/本地/复用字节和平均速度；完成后 watch 可重看，status 保持 JSON。自适应默认关闭，固定并发仍是 `--workers 4 --uploads 4`，不限速仍是默认。

九节点、每节点全新 32 GiB 对照：平均分发阶段 32.39 → 25.10 秒（耗时减少 22.50%），控制器总耗时 39.68 → 31.61 秒（减少 20.34%）。每轮清页面缓存、使用新目标并独立 SHA256 校验；不是续传测速。[方法、完整性边界及开发约束](OPTIMIZATION-0.7.0.zh-CN.md)。

## 完成后查看耗时和平均速度

```bash
./swarmcopy distribute --hosts 'HGX[061,066,167]' \
  --source /raid/images/source.bin --dest /raid/images/copies \
  --verify chunks --workers 4 --uploads 4
# 成功时自动打印 JOB SUMMARY；记住启动时输出的 ID
./swarmcopy watch --job <ID>
./swarmcopy status --job <ID>
```

watch 重看保存的文本摘要，status 输出含 Summary 的 JSON。集群平均速度用本次新传字节除以本次总耗时，包含准备；接收节点速度中位数另列。本地复制与已有数据复用都不计作新网络流量。固定并发无需 --adaptive；只有主动选择时才启用自适应。旧版本结果没有的历史耗时不会补造。

## 推荐使用：计算节点直接供种

介质已经在计算节点本地盘时，优先在该节点执行，并把本机列入 `--hosts`。v0.7.0 直接读取原文件供种，跳过初始种子复制；本机目标通过本地复制填充，其余目标通过网络互传。若在 BCM 执行且 BCM 不在目标列表，则先经 SSH 复制到计算节点作种子。开始前与 watch 均明确显示来源方式、主机和路径。[两种场景、网络要求及故障处理](SOURCE-MODES.zh-CN.md)。

```bash
# 在 HGX167 上执行；本机也在目标列表中
./swarmcopy distribute --hosts 'HGX[061,066,082,138,169,167]' \
  --source /raid/local-ai/dist/local-ai.tar --dest /raid/images --verify chunks
```

新作业默认 chunks（BLAKE3/BLAKE3），需要最终整文件复核时使用 `--verify full`（SHA256/SHA256）。异常节点不取消健康节点；仍保留未完成状态，恢复后用同一 ID 续传。恢复时跳过能确认的稀疏空洞，避免对未收到的几百 GB 零重复计算。

删除作业并清理独占暂存：`./swarmcopy remove --job <ID> --purge-partials`，`delete` 为别名。它先停止本作业控制器和 peer，再清理记录、远端作业目录及独占暂存；原始源文件、已发布成品和共享数据保留。清理失败时保留记录供重试。只想暂停请用 `stop`。

## 两种校验模式（v0.7.1）

日常只需选择 `--verify full` 或 `--verify chunks`：新建 `--source` 作业时，full 默认 SHA256 分块和文件摘要，并检查最终文件；chunks 默认 BLAKE3 分块和文件摘要，全部验块、fsync 后发布，省略最终整文件回读。v0.7.1 起省略 `--verify` 默认 chunks；需要最终整文件复核时显式使用 `--verify full`。无需再记两项 hash 参数。

```bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --dest /raid/images --verify chunks
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --dest /raid/images --verify full
```

`--chunk-hash` / `--file-hash` 保留为高级覆盖参数。续传和已有种子清单沿用原摘要算法，选择模式不会改写清单；旧 SHA256 作业切换 chunks 仍用 SHA256 验块。

## 1. 单文件、多个文件、目录

~~~bash
# 当前控制端文件 → 所有目标节点本地目录
./swarmcopy distribute --hosts 'node[001-005]' \
  --source /shared/nemo.sqsh --dest /raid/images

# 两个文件直接放到 /raid/images 下
./swarmcopy distribute --hosts @hosts.txt --dest /raid/images \
  --source /shared/nemo.sqsh --source /shared/config.json

# 选择多个 sqsh；引号让程序展开通配符
./swarmcopy distribute --hosts @hosts.txt --dest /raid/images \
  --source '/shared/*.sqsh'

# shell 展开也可以
./swarmcopy distribute --hosts @hosts.txt --source /shared/*.sqsh --dest /raid/images

# 目录保留名字：/raid/images/nemo/models/...
./swarmcopy distribute --hosts @hosts.txt --dest /raid/images --source /shared/nemo
~~~

一个 `hosts.txt` 每行一个节点；也支持逗号与 `'node[001,006,010-020]'`。目录递归，包括空文件和空目录；不支持符号链接及特殊文件。同名顶层输入或重叠的父子目录要先整理，避免输出路径歧义。

## 2. 固定“下载线程”数量

~~~bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --workers 8 --uploads 8
~~~

| 参数 | 含义 | 默认 |
| --- | --- | --- |
| `--workers` | 每节点并行下载分块请求 | 4 |
| `--uploads` | 每节点并行上传请求上限 | 4 |
| `--adaptive` | 自动调整下载并发 | 关闭 |
| `--min-workers` | 自适应下载下限 | 初始 workers |
| `--max-workers` | 自适应下载上限 | 16 |

workers/uploads 范围 1～32，按节点分别计算；不等于精确的连接数。固定并发时无需开启 adaptive。

## 3. 自动调节下载并发

~~~bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --adaptive \
  --workers 4 --min-workers 2 --max-workers 16 --uploads 8
~~~

从 4 个下载请求开始，在 2～16 之间根据表现调整；上传仍最多 8。不会保证更快，因此默认关闭。若省略 `--min-workers`，默认下限是初始 workers，通常不会降到比初始值更低。

并发参数创建任务时保存；恢复沿用保存值。目前没有运行中修改或用 resume 改 workers 的选项。

## 4. 默认不限速；必要时限速

~~~bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --download-mib 200 --upload-mib 200
~~~

每节点聚合下载和上传各 200 MiB/s，`0` 表示不限速。不是整个集群共用 200 MiB/s。

## 5. 已有文件，只有少数节点缺失

重新执行同一来源和目标即可创建补齐任务：

~~~bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images
~~~

内容一致的文件复用，完整节点参与做种；若有完整副本，就跳过头结点到种子的介质上传。所有节点已有时，新增介质下载为 0。首次建立来源清单和首次验证远端旧文件仍需要读盘，不代表没有校验成本；未变更的缓存可减少后续重读。

## 6. 同名文件不同：续传／替换

交互运行不写 `--existing`，默认询问。按 `r` 续传修复、`p` 重新接收并替换、`c` 保留并取消；选择应用于本任务检测出的全部普通文件冲突。

~~~bash
# 脚本明确选择：从旧文件复制并重新校验可复用的分块
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --existing resume

# 忽略冲突旧文件的数据，重新接收这些文件
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --existing replace

# 检测到内容不同就取消，保留旧文件
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --existing keep
~~~

旧文件保持到新数据全部校验成功，逐文件原子替换；其他无关文件不删除。已匹配文件不重下，replace 也不强制重下匹配文件或本任务已保留的有效临时块。跨文件分块可能顺带传输邻接完整文件的一小段，但不会重写该成品。

## 7. 找 job ID、监控、停止、继续

~~~bash
./swarmcopy jobs
./swarmcopy jobs --help
./swarmcopy inspect --job <ID>
./swarmcopy watch --job <ID> --plain --interval 2s
./swarmcopy status --job <ID>
./swarmcopy stop --job <ID>
./swarmcopy resume --job <ID>
./swarmcopy resume --job <ID> --existing resume
~~~

`<ID>` 换成创建时输出或 jobs 列出的 ID；保留任务目录和节点暂存数据。watch 默认最近活动／最近保存任务；退出 watch 不会停止分发。完成任务默认查看历史结果，只有 `--live` 才查实时节点。

“resume --job”恢复同一清单、同一任务的有效块；“distribute”创建新任务，检查已有成品后补齐。恢复不会重新展开来源通配符。初始 SSH 导入未完成时重做导入，P2P 部分支持断点续传。

## 8. 密码 SSH、UDP、强制校验

~~~bash
./swarmcopy distribute --hosts @hosts.txt --ssh-user alice --ask-pass \
  --source /shared/nemo.sqsh --dest /raid/images

# 同一二进制的 UDP 数据通道，控制和状态仍使用 TCP
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --transport quic

# 强制重读来源和已有文件，绕过验证缓存
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --recheck
~~~

`--ask-pass` 询问所有节点共用的一个 SSH 密码，不配置永久登录互信、不保存密码；SSH 不要求预登记主机密钥。QUIC 需 TCP 和 UDP 同端口可达，不保证比 TCP 快，默认仍 TCP。监听所有网口不等于叠加多个网卡带宽，主机名应解析到正确的数据网络。

更多参数和默认值见 `./swarmcopy distribute --help`。本手册的新冲突策略适用于 v2 的新建来源任务；历史 v1 prepared-seed 任务保留原先的成品不覆盖规则。

## 大文件开始后先看准备阶段

无需增加参数。hashing-source 是建立可信清单，importing 是初次上传到种子；每约 2 秒显示百分比、速度和当前阶段 ETA。watch 会自动从准备切到传输，字节 100% 仍可能在校验／收尾。哈希时 Ctrl+C 或 stop --job 可取消；重跑会重做未完成的初始哈希。[详细说明](PROGRESS.zh-CN.md)。

## SSH 默认参数与加密加速

v0.4.2 起统一使用以下参数，不再提供 strict 开关：

~~~text
-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=quiet -o CheckHostIP=no
~~~

SSH 登录会在耗时来源哈希前先检查，旧任务 resume 也采用这些参数。默认 --ssh-cipher auto：本机有 AES 与 GCM 多项式硬件支持时优先协商 AES-GCM，并保留 ChaCha20／AES-CTR 回退；没有检测到时沿用 OpenSSH 默认。实际指令加速由 OpenSSH／加密库提供。

~~~bash
./swarmcopy resume --job <ID>
./swarmcopy resume --job <ID> --ssh-cipher aes128-gcm@openssh.com
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --ssh-cipher system
~~~

system 完全沿用 OpenSSH 的 cipher 协商。密码、用户名、密钥／agent 的用法不变；SSH 加密只影响部署／初始导入，不等于 SCDP 数据校验。

## 校验算法与 SSH 扩展

默认 chunks + BLAKE3，省略最终整文件回读；可显式选择 `--verify full`，或 `--verify full --chunk-hash xxh3-128 --file-hash blake3` 混用快速分块与密码学文件摘要。XXH3 不允许用于 chunks 模式。首次来源清单仍要读完整源文件。详见[模式、完整性边界、硬件实测](INTEGRITY.zh-CN.md)。

SSH 默认包含 `StrictHostKeyChecking=no`、`UserKnownHostsFile=/dev/null`、`LogLevel=quiet`、`CheckHostIP=no`，可重复 `--ssh-opt KEY=VALUE` 扩展或覆盖，设置随作业保存。比如 `--ssh-opt ConnectTimeout=20 --ssh-opt ServerAliveInterval=15`。AES-GCM 可用 `--ssh-cipher aes128-gcm@openssh.com` 选择。

```bash
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --dest /raid/images \
  --verify full --chunk-hash xxh3-128 --file-hash blake3 \
  --ssh-opt ConnectTimeout=20 --ssh-opt ServerAliveInterval=15
./swarmcopy resume --job <ID> --ssh-opt ConnectTimeout=30 --verify full
```

## fsync 报错后如何恢复

数据 fsync 失败不会完成或发布该数据；保留作业和暂存。先排除磁盘空间、RAID/文件系统或 I/O 故障，原控制器仍在等待时先停止，再恢复同一作业：

```bash
./swarmcopy jobs
./swarmcopy stop --job <ID>
./swarmcopy resume --job <ID>
```

恢复会重新检查保留的数据，复用校验通过的块，补齐缺块/坏块，重新尝试完成；通常无需从头传整个文件。若写入后断电或设备丢数据，丢失块仍需补传；存储持续报错时不能保证完成。不要删除作业或使用 remove --purge-partials，否则会失去该恢复入口。

如果已经完成改名/链接，后续目录或完成标记 fsync 才报错，某些目标文件可能已存在，但作业仍不算成功；恢复会检查并复用有效文件。多文件发布逐文件进行，不是全目录事务。fsync 成功依赖操作系统和底层设备正确兑现刷盘请求，不能代替内容复核；需要额外最终内容检查时用 --verify full。
