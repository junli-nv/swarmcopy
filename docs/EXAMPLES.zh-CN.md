# 常用示例

[English](EXAMPLES.en.md) · [中文 README](../README.md)

以下是统一版 main 的用法。将二进制命名为 `swarmcopy`；异构集群把两种原名二进制放入同级 `dist`（自动识别），或每条命令指定 `--bin-dir /path/to/dist`。示例节点和路径请换成实际值。

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

`--ask-pass` 询问所有节点共用的一个 SSH 密码，不配置永久互信、不保存密码；需提前登记主机密钥。QUIC 需 TCP 和 UDP 同端口可达，不保证比 TCP 快，默认仍 TCP。监听所有网口不等于叠加多个网卡带宽，主机名应解析到正确的数据网络。

更多参数和默认值见 `./swarmcopy distribute --help`。本手册的新冲突策略适用于 v2 的新建来源任务；历史 v1 prepared-seed 任务保留原先的成品不覆盖规则。

## 大文件开始后先看准备阶段

无需增加参数。hashing-source 是建立可信清单，importing 是初次上传到种子；每约 2 秒显示百分比、速度和当前阶段 ETA。watch 会自动从准备切到传输，字节 100% 仍可能在校验／收尾。哈希时 Ctrl+C 或 stop --job 可取消；重跑会重做未完成的初始哈希。[详细说明](PROGRESS.zh-CN.md)。
