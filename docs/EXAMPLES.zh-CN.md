# 操作示例

[English](EXAMPLES.en.md) · [快速开始](../README.md) · [文档索引](README.md)

以下命令在持有源文件的控制节点执行，示例中的主机、路径和 `<ID>` 请替换成实际值。默认使用 chunks/BLAKE3、TCP、每节点 4 个下载与 4 个上传并发，不限速，自适应关闭。

## 在源节点上分发到相同路径

在 HGX167 上执行，并把 HGX167 列入目标：

```bash
./swarmcopy distribute \
  --hosts 'HGX[061,066,082,138,169,167]' \
  --source '/raid/nemo*' --dest /raid
```

本机已存在的原文件检查后直接复用、供种，不复制到自身。其他节点获得相同路径的文件。`'/raid/nemo*'` 只匹配 `/raid` 的直属条目；匹配到目录才递归其内部，不会搜索 `/raid/images`。

本机不在目标列表时不会自动采用 direct；请使用实际主机名，不依赖程序推断任意 SSH 别名。`SOURCE MODE` 与 `SOURCE READY` 会显示方式和来源端口。

## 从 BCM 或源节点分发到另一目录

```bash
# 在 BCM 上：必要时先经 SSH 复制一次到计算节点种子
./swarmcopy distribute --hosts 'HGX[061,066,082]' \
  --source /shared/nemo.sqsh --dest /raid/images

# 在 HGX167 上：原文件直接供种，本机目标副本用本地读取/验块/写入
./swarmcopy distribute --hosts 'HGX[061,066,167]' \
  --source /raid/source/nemo.sqsh --dest /raid/images
```

已有完整副本可用时，程序会跳过不必要的初始介质上传。`importing` 表示种子复制，不是解压。若 BCM 只能访问一个计算节点，请登录该节点执行分发；没有自动转交控制器或 SSH 隧道管理层。

## 多文件、目录与通配符

```bash
./swarmcopy distribute --hosts @hosts.txt --dest /raid/images \
  --source /shared/nemo.sqsh --source /shared/config.json

./swarmcopy distribute --hosts @hosts.txt --dest /raid/images \
  --source '/shared/*.sqsh'

./swarmcopy distribute --hosts @hosts.txt --dest /raid/images \
  --source /shared/models
```

| 来源 | 目标布局 |
| --- | --- |
| `/shared/nemo.sqsh` | `/raid/images/nemo.sqsh` |
| 两个来源文件 | 都直接放在 `/raid/images/`，不增加批次目录 |
| `/shared/models` 或 `/shared/models/` | `/raid/images/models/...` |

也支持 shell 展开和位置参数：`./swarmcopy distribute --hosts @hosts.txt --dest /raid/images /shared/*.sqsh`。建议给通配符及方括号主机表达式加引号。glob 支持 `*`、`?`、`[]`；`**` 没有额外递归语义。未匹配、重叠根及重复顶层名称会报错。目录中的空文件/空目录保留，符号链接和特殊文件不支持。

## 已有文件与冲突处理

```bash
# 复用旧文件的有效块，补齐后替换
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --existing resume

# 不从冲突旧文件复制数据，重新接收该文件后替换
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --existing replace
```

默认 `--existing ask` 在控制端列出冲突并询问一次。`keep` 表示遇到内容冲突时取消，不是“跳过冲突但仍算成功”。相同文件无需重复下载；replace 也不会强制重传已匹配文件。旧成品保留到新内容校验完成后逐文件替换；需留出独立暂存空间。额外目标文件不会被删除。

## 主机列表与 SSH

```bash
./swarmcopy distribute --hosts 'HGX[061,066,167],node[001-005]' \
  --source /shared/nemo.sqsh --dest /raid/images

./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --ssh-user root --ask-pass

./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --ssh-opt ConnectTimeout=20 \
  --ssh-opt ServerAliveInterval=15 --ssh-cipher aes128-gcm@openssh.com
```

`hosts.txt` 可逐行写主机名；SSH 登录可使用密钥、agent 或 SSH config。`--ask-pass` 隐藏询问一次全部目标共用的密码，不落盘，不自动建立互信。默认 SSH 选项为 `StrictHostKeyChecking=no`、`UserKnownHostsFile=/dev/null`、`LogLevel=quiet`、`CheckHostIP=no`。重复的 `--ssh-opt KEY=VALUE` 按键覆盖并随作业保存；显式 `--ssh-cipher` 优先。

## 并发、限速、端口和 QUIC

```bash
# 固定并发；默认是 4/4，参数按每节点计数
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --workers 8 --uploads 8 --port 19800

# 每节点聚合限速，单位 MiB/s；0 表示不限速
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --download-mib 200 --upload-mib 200

# 显式自适应下载：初始 4、下限 2、上限 16，上传上限仍为 8
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --adaptive --workers 4 --min-workers 2 --max-workers 16 --uploads 8

# 同一二进制中的可选 UDP 数据通道
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --transport quic
```

workers/uploads 范围 1～32，是并行分块请求数，不保证等于连接数。自适应只调下载，默认关闭；目前不提供运行中热调参。QUIC 需放行 peer 同号 UDP，控制/状态仍走 TCP；direct 来源还有独立动态端口。监听全部网卡不等于聚合网卡带宽。参数随作业保存，续传不应靠修改 JSON 切换数据传输协议。

## 校验选择

```bash
# 默认就是 chunks/BLAKE3，无需再写两项 hash 参数
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh --dest /raid/images

# 最终整文件内容复核；默认 SHA256，流水校验开启
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --verify full

# 高级混合：非密码学块摘要必须配 full 密码学文件复核
./swarmcopy distribute --hosts @hosts.txt --source /shared/nemo.sqsh \
  --dest /raid/images --verify full --chunk-hash xxh3-128 --file-hash blake3
```

首次来源清单仍需完整读取；chunks 只省略最终整文件复核，不省验块和 fsync。full 的校验可能与下载重叠。已有作业保持原算法，`resume --verify full` 不会将已有 BLAKE3 清单改成 SHA256。详见[校验模式](INTEGRITY.zh-CN.md)。

## 监控、停止与恢复

```bash
./swarmcopy jobs
./swarmcopy jobs --json
./swarmcopy inspect --job <ID>
./swarmcopy watch --job <ID> --interval 2s --plain
./swarmcopy status --job <ID>
./swarmcopy stop --job <ID>
./swarmcopy resume --job <ID>
./swarmcopy resume --job <ID> --existing resume --ask-pass
```

完成时的 JOB SUMMARY 保存本次总耗时、准备/分发耗时、网络/本地复制/复用字节和平均速度。集群平均用本次新字节除以本次总耗时，包含准备；接收节点速度中位数单列。watch 重看完成记录，status 保持 JSON。没有历史耗时的旧记录不会补造摘要。

单节点失败不取消其他节点，但不会虚报全体完成。退出 watch 不停止任务；Ctrl+C 停止控制器后 peer 可能继续，明确停作业用 stop。若 fsync/I/O 失败，先修复存储，再 stop（原控制器仍在等时）并 resume 同 ID；完整块复用，坏块/缺块补传。后续目录 fsync 出错时可能已有部分成品，仍不能算作业成功。保留作业记录和节点暂存，不要先 purge。

## 清理和删除作业

```bash
# 正常完成后自动清理；用于旧完成作业或清理失败重试
./swarmcopy cleanup --job <ID>

# 删除作业记录和部署，保留源和成品
./swarmcopy remove --job <ID>

# 另行丢弃本作业独占暂存，不再保留这份续传数据
./swarmcopy remove --job <ID> --purge-partials
```

cleanup 保留 jobs/watch/status 历史；remove/delete 删除记录。`.swarmcopy` 中只固定保留最近缓存与有上限的日志，未完成及共享数据受保护。`--keep-seeding` 在完成后继续供种，之后用 cleanup 收尾。规则及旧布局兼容见[清理说明](CLEANUP.zh-CN.md)。

## 查帮助与进一步阅读

```bash
./swarmcopy --help
./swarmcopy distribute --help
./swarmcopy jobs --help
./swarmcopy help cleanup
```

`prepare`、`peer` 和 `hashbench` 是高级或开发入口，日常分发不必手动启动节点 peer。异构集群部署、来源网络要求及字段语义分别见[README](../README.md)、[来源方式](SOURCE-MODES.zh-CN.md)、[进度指南](PROGRESS.zh-CN.md)和[开发指南](DEVELOPMENT.zh-CN.md)。
