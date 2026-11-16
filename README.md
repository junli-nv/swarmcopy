# swarmcopy

Linux 集群的可续传 P2P 文件与目录分发工具，协议名 **SCDP/1**。适合将 NeMo 等容器介质从共享目录分发到计算节点本地 RAID，减轻头结点和单个种子的压力。

[下载统一正式版](https://github.com/junli-nv/swarmcopy/releases/latest) · [English](README.en.md) · [中文示例手册](docs/EXAMPLES.zh-CN.md) · [开发指南](docs/DEVELOPMENT.zh-CN.md)

**只维护 main 一个长期分支。** 同一个二进制包含 TCP、可选 UDP/QUIC、流水校验、自适应并发、多文件和目录功能，不需要另外安装 UDP 版或 race 版。历史标签和测速报告仅供追溯。

## v0.5.0 校验模式与 SSH 扩展

默认保留 full + SHA256；可选 `--verify chunks` 省略最终整文件回读，或 `--verify full --chunk-hash xxh3-128 --file-hash blake3` 混用快速分块与密码学文件摘要。XXH3 不允许用于 chunks 模式。首次来源清单仍要读完整源文件。详见[模式、完整性边界、硬件实测](docs/INTEGRITY.zh-CN.md)。

SSH 默认包含 `StrictHostKeyChecking=no`、`UserKnownHostsFile=/dev/null`、`LogLevel=quiet`、`CheckHostIP=no`，可重复 `--ssh-opt KEY=VALUE` 扩展或覆盖，设置随作业保存。比如 `--ssh-opt ConnectTimeout=20 --ssh-opt ServerAliveInterval=15`。AES-GCM 可用 `--ssh-cipher aes128-gcm@openssh.com` 选择。

## 大文件准备进度与 Ctrl+C

v0.4.1 起，建立来源清单和初始种子导入每约 2 秒显示字节、百分比、速度与预计剩余时间。部署、检查节点和等待监听显示步骤、完成节点数及耗时；watch 不再把准备阶段误报为全体离线。Ctrl+C 会在源读取块之间响应；估算值只覆盖当前阶段，不含后续工作。[显示示例、取消边界与验证](docs/PROGRESS.zh-CN.md)。

## 安装与依赖

从 Releases 下载 `swarmcopy-linux-x86_64` 或 `swarmcopy-linux-aarch64`，使用 `SHA256SUMS` 校验，重命名为 `swarmcopy` 并 `chmod +x swarmcopy`。将两种原名二进制放到同一个 `dist` 目录，控制器可按远端架构自动部署；只有同架构时也可直接部署自身。

运行时需要 Linux、控制端 OpenSSH 客户端、节点 SSH 服务和基本 shell 工具。无需 pdsh、rsync、外部 tar、Python、Go、数据库或常驻服务。Python/Go 仅用于开发测试。

提前设置 SSH 密钥、agent 或 SSH config；也支持 `--ssh-user alice --ask-pass`，隐藏询问一次所有节点共用的 SSH 密码，不落盘。SSH 默认使用 StrictHostKeyChecking=no、UserKnownHostsFile=/dev/null、LogLevel=quiet、CheckHostIP=no，不要求预登记主机密钥。

## 快速开始

~~~bash
./swarmcopy
./swarmcopy distribute --help
./swarmcopy distribute --hosts 'node[001-005]' \
  --source /shared/nemo.sqsh --dest /raid/images --bin-dir ./dist
./swarmcopy jobs
./swarmcopy watch
~~~

源在**运行命令的控制端**。目标在每个节点自己的本地磁盘上。首次建立可信 SHA256 清单需要读取源文件；源 inode/大小/mtime/ctime 和目录成员不变时，后续新任务可复用本地校验缓存，`--recheck` 可强制重读。

分发前会检查各节点。已有完整副本时，直接从这些节点补齐缺失节点，跳过控制端到种子的介质传输。否则先通过 SSH 向一个种子流式导入一次，再由节点互传。默认任务全体完成后停止 peer。

## 多文件、通配符、目录

~~~bash
# 多个文件直接放到 /raid/images，不加批次目录
./swarmcopy distribute --hosts 'node[001-005]' --dest /raid/images \
  --source /shared/nemo.sqsh --source /shared/config.json

# 程序展开加引号的通配符；不会匹配不到还悄悄继续
./swarmcopy distribute --hosts 'node[001-005]' --dest /raid/images \
  --source '/shared/*.sqsh'

# 也可由 shell 展开，或把输入作为位置参数
./swarmcopy distribute --hosts 'node[001-005]' --dest /raid/images /shared/*.sqsh

# 保留目录名：得到 /raid/images/nemo/...
./swarmcopy distribute --hosts 'node[001-005]' --dest /raid/images \
  --source /shared/nemo
~~~

目录递归分发，保留内部结构、空文件、空目录；目录末尾的 `/` 不会改变布局。多个顶层输入必须具有不同的 basename，不能输入重叠的父子目录。相同输入会去重。`*`、`?`、`[]` 是普通 glob，递归选择请直接输入目录，`**` 没有专门的递归语义。

节点直接写多个文件的临时位置，**不生成每节点完整 tar 文件，也没有传完后再解包的阶段**。首次 SSH 导入仅使用内存中的 tar 流。所有文件共享分块调度和一个 job ID；分块可以跨文件边界。

## 已有文件与冲突

- 已有且 SHA256 一致：复用并可做种，不重新下载。
- 只有一两个节点缺文件：只补缺失节点；完整节点可以同时提供数据。
- 所有节点已有完整文件：新增介质下载量为 0，正常完成任务。
- 同名但内容不同：默认 `--existing ask`，控制端列出冲突，询问续传／修复、重新传输并替换，或者保留并取消。

脚本可明确指定：

~~~bash
# 复用旧文件中校验通过的分块；旧文件保留到新内容校验成功
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --existing resume

# 不从冲突旧文件拷贝数据，重新接收该文件后替换
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --existing replace
~~~

`--existing keep` 遇到内容冲突时取消，保留旧文件；不交互的运行若可能冲突，应显式选策略。选择一次应用到本任务检测出的所有普通文件冲突。同名目录合并，不删除额外文件；文件与目录类型冲突、符号链接和特殊文件需先人工处理。

新文件全部通过分块和逐文件 SHA256 校验后，逐文件原子发布／替换。**整个文件集合不具有一次性原子切换语义**，发布中断可能已有部分成品，恢复会复用它们。修复需在暂存区保留独立数据，旧文件不会作为可写临时文件。

## 调整下载和上传并发

~~~bash
# 固定每节点 8 个并行下载、最多 8 个并行上传
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --workers 8 --uploads 8

# 自动调下载并发：初始 4，下限 2，上限 16；上传上限仍固定为 8
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --adaptive --workers 4 --min-workers 2 --max-workers 16 --uploads 8
~~~

默认 `--workers 4 --uploads 4`，范围均为 1～32，按**每节点**计数。worker 是并行分块请求，不保证等于 TCP/UDP 连接数。自适应只调整下载并发；`--min-workers` 默认等于初始 workers，`--max-workers` 默认 16。自适应不保证更快，**默认关闭**。这些并发参数在任务启动时保存，目前不提供运行中修改命令。

默认不限速。可用 `--download-mib 200 --upload-mib 200` 指定每节点聚合 MiB/s，0 表示不限速。

## 查看、停止、续传

~~~bash
./swarmcopy jobs
./swarmcopy jobs --json
./swarmcopy inspect --job <ID>
./swarmcopy watch --job <ID> --interval 2s --plain
./swarmcopy status --job <ID>
./swarmcopy stop --job <ID>
./swarmcopy resume --job <ID>
# 恢复时也可覆盖冲突策略
./swarmcopy resume --job <ID> --existing resume --ask-pass
~~~

`watch/status` 默认选最近活动任务，否则最近保存任务。完成后可看保存结果，`--live` 才查询已经停止的 peer。全局显示各节点进度、文件总数、聚合速率、哈希阶段及完成数；100% 字节仍需等待校验和发布。

Job 默认在 `~/.local/state/swarmcopy/jobs`，遵循 `XDG_STATE_HOME`。保留 job 目录、token、manifest 和节点的 `--dest/.swarmcopy-trees/<manifest-id>/data`，才能续传。通配符选出的文件列表在创建时冻结，恢复不会重新匹配新增文件。

优先用 `stop --job` 停止，按 PID 和启动时间确认归属。每节点只有一个普通 swarmcopy 二进制；`.swarmcopy/<job-id>/process.json` 和 `peer.log` 可用于定位。不要为停止单个任务而执行全局 `pkill swarmcopy`。退出 watch 不会停止任务。

初始控制端到种子的导入尚不支持按块续传，导入未完成时重做该阶段；P2P 阶段可以续传、修复和重传。旧版 v1 单文件任务及 `prepare --seed-file` 工作流仍兼容，其已有成品继续采用原来的不覆盖规则；新建 `--source` 任务使用 v2 清单和上述冲突策略。

## 网络与校验默认值

默认 TCP，监听所有本机地址的 19700 端口。节点通过 `--hosts` 中名称的解析结果通信；监听所有接口不会自动聚合多张网卡带宽，需保证这些名称解析到期望的数据网地址。

~~~bash
# UDP/QUIC 已包含在同一个程序里，显式开启即可
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --transport quic
~~~

QUIC 模式还需放行同端口 UDP，控制／状态仍走 TCP，不会静默回退到 TCP 数据。包含接收 socket 分片、协商大包和有界 GSO；已有集群测试中 TCP 仍更快，故不改默认值。

默认启用流水 SHA256 和 256 块窗口。`--pipeline-verify=false` 关闭两者；`--verify-window-chunks 0` 仅关闭窗口。全部已知校验路径保持 SHA256，Go 在支持的平台自动使用 CPU 加速，不是换成弱校验。

缓存绑定每个文件的 inode、设备、大小、mtime、ctime 与完整 manifest ID；`--recheck` 强制重读。传输期间请保持源和目标不被其他程序修改。HTTP 控制及 TCP 数据为明文，QUIC 数据有任务身份绑定 TLS；仍应部署在可信集群网络。

## 范围、验证与维护

目前支持普通文件和目录，最多 100,000 个条目；不跟随／保留符号链接，不接受设备、FIFO、socket。源硬链接按独立文件复制，不保留 UID/GID、ACL、xattr、硬链接关系或稀疏布局。新发布文件保存普通权限位及 mtime，去掉特殊权限位；已匹配文件保持原样，数据集目录的权限及 mtime 按源应用。不会删除目标的额外文件。

[多文件设计与验证](docs/TREES.zh-CN.md) · [开发指南](docs/DEVELOPMENT.zh-CN.md) · [UDP 参数维护](docs/QUIC-TUNING.zh-CN.md)

历史单文件 157 节点冷缓存研究：v0.1.0 的 SCDP 校验完成中位数 153.29s，rsync-over-SSH 复制 362.76s、校验完成 408.73s；不计初始导入。这是加密 SSH 与明文 HTTP 的整体方案对比，不代表本次多文件版本的新测速。[全部轮次和边界](docs/COLD-RESULTS.zh-CN.md) · [流水校验对照](docs/PIPELINE-RESULTS.zh-CN.md) · [UDP 全部消融](docs/QUIC-TUNING-RESULTS.zh-CN.md)。

开发构建：

~~~bash
go test -mod=vendor -race ./...
bash scripts/build.sh
~~~

需要 Go 1.26+，依赖已 vendor；重建 QUIC vendor patch 见维护文档。x86_64 做了真实节点验证，aarch64 为静态交叉构建，本次未做 ARM 实机测试。历史发布标签保留，后续改进统一进入 main。
