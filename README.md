# swarmcopy

[UDP实现审查与全部消融结果](docs/QUIC-TUNING-RESULTS.zh-CN.md) · [参数与维护](docs/QUIC-TUNING.zh-CN.md)

UDP候选使用静态接收socket分片、路径协商大包和有界GSO批次；仍默认TCP，固定4个下载worker，自适应并发关闭。使用UDP请下载上方v0.3.0-dev.4，而常规稳定版为v0.2.0。

[UDP 下载与全部实测](https://github.com/junli-nv/swarmcopy/releases/tag/v0.3.0-dev.4) · [45 节点结果](docs/QUIC-RESULTS.zh-CN.md)

[UDP/QUIC 尝试与开发说明](docs/QUIC.zh-CN.md)：本分支用 `--transport quic` 选择 UDP 数据通道；默认仍为 TCP。

[正式版 v0.2.0 下载](https://github.com/junli-nv/swarmcopy/releases/tag/v0.2.0) · [完整校验优化](docs/PIPELINE.zh-CN.md) · [45 节点对照](docs/PIPELINE-RESULTS.zh-CN.md)

新建任务默认启用流水线 SHA-256 和 256 块前缀窗口，自适应并发仍默认关闭。关闭流水线用 `--pipeline-verify=false`，仅关闭窗口用 `--verify-window-chunks 0`。旧任务续传沿用原配置。

Linux 计算集群的大文件 P2P 分发工具。适用于将 NeMo 等容器的 `.sqsh` 介质从共享存储分发到节点本地 RAID。

[English](README.en.md) · [开发指南（中文）](docs/DEVELOPMENT.zh-CN.md) · [Developer guide](docs/DEVELOPMENT.en.md) · [验证记录](docs/VALIDATION.zh-CN.md)

[冷页缓存对照方案](docs/COLD-STUDY.zh-CN.md) · [速度对比](docs/BENCHMARK.zh-CN.md)

## 大规模对照结果

157 节点（1 种子＋156 接收）、37.43 GiB 单文件，三轮全新目标且每次清 OS 页缓存：SCDP 校验完成中位数 **153.29 秒**，rsync-over-SSH 复制 **362.76 秒**、校验完成 **408.73 秒**，分别为 **2.37× / 2.67×**。使用已准备种子，不计初始导入；这是加密 SSH 与明文 HTTP 的整体方案对比。所有轮次、范围和边界见 [正式测速报告](docs/COLD-RESULTS.zh-CN.md)。

## 先看帮助

~~~bash
./swarmcopy
./swarmcopy --help
./swarmcopy distribute --help
./swarmcopy jobs --help
./swarmcopy help watch
~~~

帮助信息使用英文，提供参数、默认值和示例。单独运行不会启动任何传输。

## 协议名称

**SCDP — SwarmCopy Distribution Protocol**（SwarmCopy 分发协议），第一版记为 **SCDP/1**。协议版本独立于程序版本。

## 工作方式与依赖

控制端通过 SSH 部署程序，并将介质只发送一份到初始种子。节点间通过 HTTP/TCP 交换已校验的分块，文件尚未全部下载的节点也能上传。控制端只汇总状态，不转发节点间的数据。

每种架构一个静态二进制，不依赖 pdsh、Python、Go、tracker、Kubernetes 或常驻 agent。控制端需要 OpenSSH `ssh`；计算节点需 SSH 服务、POSIX shell 和基本的 cat/chmod/mv/mkdir/uname 工具。程序不是兼容公网客户端的 BitTorrent 实现。

控制端须能 SSH 到所有目标，并访问所有节点的 TCP 19700；计算节点也须彼此访问该端口。可用 `--port` 更改。主机名必须在控制端和所有节点上解析到可互通的地址；仅配置在 SSH 中的别名不会自动映射为数据网络地址。目标目录须为有足够空间的本地文件系统。

## 下载与安装

从 [v0.2.0 Releases](https://github.com/junli-nv/swarmcopy/releases/tag/v0.2.0) 下载两个架构的二进制及 SHA256SUMS，放在同一目录：

~~~bash
sha256sum -c SHA256SUMS
chmod +x swarmcopy-linux-*
cp swarmcopy-linux-x86_64 swarmcopy   # ARM 控制端改用 aarch64
./swarmcopy version
~~~

异构集群需保留两个架构文件，并用 `--bin-dir` 指向其目录。同架构部署可直接复用控制端自身的程序。发布的 ARM64 文件经过交叉编译检查；实际硬件测试范围见验证记录。


开发目录中的 swarmcopy-race 仅用于竞态检测，不是运行依赖，不需复制到节点，也不随发布分发。distribute 会自动部署普通版程序。

## 首次分发

以下使用示例节点和路径，为新介质选择尚未存在的目标文件名：

~~~bash
./swarmcopy distribute \
  --hosts 'node[001-005]' \
  --source /shared/media/nemo.sqsh \
  --dest /raid/images \
  --bin-dir .
~~~

`--source` 是控制端可读的文件，不要求节点挂载同一个 NFS。默认第一个节点为种子，先通过 SSH 接收一份文件并生成 SHA-256 清单，再进行 P2P 分发。源文件在导入过程中必须保持不变。

程序打印任务 ID 和目录，然后显示全局与各节点进度、上传/下载速率、ETA、最终校验及做种状态。每节点默认上传、下载均不限速（0），4 个下载 worker、4 个上传槽位。需要限速时可设置 --upload-mib 256 --download-mib 256，单位 MiB/s。全部节点完成整文件 SHA-256 校验后，默认停止本次做种；`--keep-seeding` 可保留，仍受 `--lifetime` 限制（默认 2 小时）。

主机列表支持以下形式，最多 1024 个目标（不是已验证的性能规模）：

~~~bash
--hosts 'node001,node006,node009'
--hosts 'node[001,006,009-012]'
--hosts 'alice@node[001-005]'
--hosts @hosts.txt
~~~

主机文件每行一项，支持 # 注释。

## SSH 用户名与密码

复用已有 SSH key、ssh-agent 和 SSH 配置；首次连接须自行核验并记录主机密钥。程序启用严格主机密钥检查，不修改 authorized_keys。

~~~bash
./swarmcopy distribute \
  --hosts 'node[001-005]' --ssh-user alice --ask-pass \
  --source /shared/media/nemo.sqsh --dest /raid/images --bin-dir .
~~~

`--ask-pass` 交互输入一次，所有目标尝试同一密码。密码通过内存中的本地 askpass 服务交给 OpenSSH，不写入参数、任务文件或日志；需要交互控制终端，不能从管道输入。续传或重启控制程序需重新输入。各节点密码不同请分批或使用公钥/config；MFA、首次主机密钥确认或服务器禁用密码登录时需要单独处理。

## 找任务、看进度、停止和续传

~~~bash
./swarmcopy jobs
./swarmcopy jobs --json
./swarmcopy inspect --job <ID>
./swarmcopy watch                   # 最近活动任务，否则最近保存的任务
./swarmcopy status                  # JSON
./swarmcopy watch --job <ID> --interval 2s
./swarmcopy resume --job <ID>
./swarmcopy resume --job <ID> --ask-pass
./swarmcopy stop --job <ID>
~~~

`distribute` 和 `resume` 自带全局监控；退出单独的 `watch` 不停止传输。控制端中断后，节点可继续在生命周期内交换数据；`resume` 重新部署并复用仍活着的本任务进程。`stop` 仅停止通过 PID、进程启动时间和命令行身份检查的本任务进程，不删除介质或部分文件。

默认记录在 `$XDG_STATE_HOME/swarmcopy/jobs`，未设置时为 `~/.local/state/swarmcopy/jobs`。ID 是控制端当前用户的本地索引。`jobs` 显示保存状态，控制端崩溃后可能过时；活动任务用 watch/status 查询实时状态。完成任务默认显示保存结果（明确标注 RECORDED）；`--live` 强制查询节点，已停止做种的节点可能不可达。

可用 `--job /absolute/new-job-directory` 创建自定义目录，后续传同一路径；`jobs --state-dir /parent` 查询其父目录。迁移控制端须安全复制整个任务目录，其中包含私密 token。不要将任务目录提交 Git。

**为什么 `jobs` 看不到刚完成的任务？** 它只列出指定状态目录的直接子目录，不会递归搜索磁盘。切换用户、设置不同的 XDG_STATE_HOME，或创建任务时使用自定义 --job 目录，都会改变查询范围。自定义任务不会自动注册到默认目录；仅复制 JOB ID 不能在默认目录中找到它，需要传原任务目录。

例如冷缓存测速脚本把 SCDP 作业存到 --out 指定的结果目录：

~~~bash
./swarmcopy jobs --state-dir ./cold-results-new
./swarmcopy watch --job ./cold-results-new/<case-id>-job --count 1
./swarmcopy inspect --job ./cold-results-new/<case-id>-job
~~~

这里的 <case-id> 取自测速 study-plan.json；jobs 列出的 JOB ID 与测速 case-id 不同。rsync 对照不创建 swarmcopy 作业，查看结果目录中的 progress.json、results.json 和最终 summary.json。测速脚本是实验工具，不是运行 swarmcopy 的依赖。

保留清单和 `.part` 即可恢复 P2P 传输。程序逐块重算 SHA-256，只补缺失或损坏的块；完成后校验完整 SHA-256，再原子发布文件。有效的已有最终文件可做种；错误的最终文件会被拒绝，不自动覆盖。

初始“控制端发送到种子并生成清单”的导入阶段暂不支持块级续传，中断后可能需要重新导入。若文件已发布但清单未发布，先检查现场。这里的限制不影响已有清单后的 P2P 续传。

## 使用已经准备好的种子

~~~bash
# 在种子节点执行，out 不得已存在
./swarmcopy prepare --source /shared/media/nemo.sqsh --out /raid/images/nemo.sqsh

# 在控制端执行，复用该文件及其 .manifest.json
./swarmcopy distribute \
  --hosts 'node[001-005]' --seed node001 \
  --seed-file /raid/images/nemo.sqsh --dest /raid/images --bin-dir .
~~~

## 网络、限速与完成条件

默认监听所有地址，出站连接由 DNS 和系统路由决定；监听所有网口不等于自动多网卡聚合，也不代表原生 RDMA。第一版数据面是明文 HTTP，仅用于受控、互信的隔离集群。任务 token 只做访问控制，不能替代 TLS；持有 token 的进程能读取本任务介质。

`--upload-mib 0 --download-mib 0` 取消限速。提高 `--workers`、`--uploads` 会增加资源负载。默认分块 16 MiB，可通过 prepare 的 `--chunk-mib` 调整。

100% 字节后仍要等待完整文件校验。失联节点保留上次进度或按零计，不算完成。ETA 不含完整磁盘校验时间；首份状态取得前总量可能未知。活动任务 status 在未完成或不可达时返回非零退出码。

低层 peer/resume 接受显式清单、token、host:port 列表。没有自动网络配置、防火墙修改、磁盘清理、节点重启或训练作业调度。

## 校验缓存与强制复验

已经通过完整校验的最终文件会生成 .swarmcopy.verified.json，绑定清单 ID、设备、inode、大小、mtime 和 ctime。默认仅在这些信息完全匹配时复用上次校验结果；新建部分文件无需扫描零空间，已有 .part 仍逐块重算 SHA-256，新收到的文件仍必须通过完整 SHA-256。

缓存假设文件不可变、操作系统正确记录写入元数据，不能代替检测静默磁盘损坏的定期复验。使用 --recheck 忽略最终文件缓存：

~~~bash
./swarmcopy distribute --hosts 'node[001-005]' --seed node001 --seed-file /raid/images/nemo.sqsh --dest /raid/images --recheck
./swarmcopy resume --job <ID> --recheck
~~~

新 prepare 会在导入校验后生成缓存；旧介质第一次使用会完整校验一次。不要复制其他文件的校验缓存。并行启动/停止默认最多 8 个 SSH 操作，可在创建任务时用 --fanout 调整。

## 手动查找与停止

~~~bash
pgrep -af swarmcopy
kill -TERM <确认过的PID>
~~~

优先用 stop --job 停止整批任务。杀掉控制端不会自动停止已脱离终端的 peer；本任务节点 PID 和日志在 <dest>/.swarmcopy/<ID>/。不要用全局 pkill 误停其他任务。强制 SIGKILL 仅作最后手段，部分文件可在恢复时重校验。

当前版本只接受单个文件，不支持目录、多输入或文件通配符；这些功能按计划留到下一版。并行任务应使用不同端口和独立目标；同一目标文件受排他锁保护。

## 开发

Go 1.26+；Python 仅用于测试，运行程序不需要。

~~~bash
bash scripts/build.sh
go test -race ./...
go vet ./...
python3 scripts/integration.py
~~~

协议、并发与锁、恢复流程、已知限制和优化路线见双语开发指南。


