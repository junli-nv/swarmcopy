# 来源方式与节点故障

[English](SOURCE-MODES.en.md) · [中文 README](../README.md) · [示例](EXAMPLES.zh-CN.md)

## 优先推荐：计算节点本地介质直接供种

介质已在计算节点本地盘时，推荐就在该节点执行，并将本机放进目标列表。程序自动从原文件供种，跳过先完整复制一个初始种子的步骤，无需增加模式参数。例如在 HGX167 上：

~~~bash
./swarmcopy distribute --hosts 'HGX[061,066,082,138,169,167]' \
  --source /raid/local-ai/dist/local-ai.tar --dest /raid/images --verify chunks
~~~

程序在读取大文件前打印：

~~~text
SOURCE MODE: direct | HGX167:/raid/local-ai/dist/local-ai.tar serves original files;
no initial seed copy; ALL targets including this node receive into /raid/images
~~~

随后显示 SOURCE READY 和实际供种地址。原文件只读，不搬到暂存种子再开始。HGX167 自己的目标路径如果不同，仍要在 /raid/images 生成一份目标文件；这与其他节点接收同时进行，不是全局启动门槛。同路径且内容一致时复用现有文件。不要在作业期间修改原文件。

来源清单首次仍需读取所有源字节；直接供种省的是初始种子复制，不是省掉首次校验，也不是承诺几 GB/s。源文件变化会被指纹检查发现；接收端仍按可信清单验块。

## BCM 头结点作为控制器

如果执行机器不在目标列表内，自动先把介质经 SSH 复制到一个计算节点作种子，再由计算节点间分发：

~~~bash
./swarmcopy distribute --hosts 'HGX[061,066,082]' \
  --source /home/cmsupport/workspace/nemo-26.08.00.sqsh \
  --dest /raid/images --verify chunks
~~~

启动时明确打印 SOURCE MODE: staged、来源主机/路径及种子节点/目标目录。“importing”表示这次种子复制，**不是解压 tar，也不是导入 Docker/Enroot**。复制阶段显示当前文件、已发送字节、速度和阶段 ETA；达到 100% 后仍可能等待种子 fsync、清单比对和结束确认。

若部署前指定种子已不可达，选择可用计算节点并打印 SEED FAILOVER。复制进行中丢失唯一数据来源仍需恢复或重新准备种子；不会凭空补出没有任何在线副本的块。初始 SSH 种子复制尚不支持按块续传。

## 自动判断的边界

--hosts 表示需要在 --dest 下收到文件的目标，不是单纯的上传者列表。本机出现在列表中才自动采用直接供种；否则采用 staged。匹配使用本机 hostname 与目标短主机名，忽略大小写和 SSH 用户前缀；无法推断任意别名是否本机，使用实际主机名。两种方式都在控制器和 watch 中持续说明。

直接来源进程属于正在执行的控制器，独立监听一个动态 TCP 端口；QUIC 模式还使用同端口 UDP。接收节点必须能解析、访问 SOURCE READY 公布的地址，不能只放行 peer 的 19700。没有额外常驻代理或独立版本。保留控制器运行，直到所需内容已由其他节点持有；退出控制器会停止这个原文件来源。恢复时在同一源节点执行 resume，保留作业记录及原文件路径。

两种模式的控制器都要能 SSH 和 HTTP 查询所有目标，节点之间也须能互通。BCM 只能访问一个计算节点时，请手动把介质放到那台节点，再登录该节点执行 distribute；这样控制器位于计算网络内。没有增加“一键转交”、SSH 隧道或远程控制器管理层。

## 节点异常不阻断健康节点

v0.6.0 删除等待所有监听端口就绪才进入监控的门槛。peer 启动后独立传输；单节点 SSH、部署、盘点或启动失败，不会取消其他节点。

- 输出指出具体节点和失败阶段；节点错误保存到 job.json。
- 正常节点立即显示下载、校验和完成进度。
- 无监听时，控制器检查本作业记录的进程。STARTING 表示进程还活着，显示最近日志和日志路径；FAILED 表示进程已退出；SSH 也失败则显示 UNREACHABLE。日志只作诊断数据。
- 运行中状态请求有界；离线来源会由 peer 发现/重试机制避开。其他来源仍有块时继续传输。
- 失败目标仍在总数中，不能虚报整体 100%。健康目标完成后，仍会保留其供种，等待其余目标或控制器超时/停止。
- 自动状态检查可以发现已启动 peer 恢复监听，但不会自动重新部署启动失败的主机。节点恢复后用同一 job ID 续传补齐。

单节点 SSH/部署/盘点尝试默认最多 1 分钟，可用 --node-timeout 5m 增大。盘点超时会记录结果未知，之后启动的 peer 自己继续检查；它可能暂时显示 STARTING。全局来源不可读、没有可部署目标或唯一种子丢失等无法继续供数的错误仍会中断，不将它们伪装成成功。

~~~bash
./swarmcopy jobs
./swarmcopy watch --job <ID>
./swarmcopy inspect --job <ID>
./swarmcopy resume --job <ID>
~~~

无需把已完成目标从原作业删除。旧 v0.5 作业保留原来的摘要算法；新建作业只需 --verify chunks，默认 BLAKE3/BLAKE3。--verify full 默认 SHA256/SHA256。

## 本轮验证

139,460,641 字节、多文件及目录夹具：BCM 式 staged、HGX061 原文件 direct、QUIC direct 均通过，两个目标逐文件独立 SHA256 一致。合成不可达初始种子不会取消另外两台；另在 HGX061/HGX066/HGX082 三台中途停止 HGX066 的接收进程，其余两台继续完成，明确记录故障，同 ID 续传复用部分数据，最终三台独立 SHA256 一致。另通过 race、vet、CLI 及源文件变化检测测试。

这些是功能验证，不是新一轮 483.84 GiB 或百节点性能报告。aarch64 仅静态交叉构建，本轮未做 ARM 真机验证。


## 删除作业与稀疏文件修复

```bash
./swarmcopy remove --job <ID>
./swarmcopy delete --job <ID> --purge-partials
```

remove 先用 PID/start-time 记录停止本作业控制器，锁定作业，再停止所属 peer。默认清理记录与远端作业目录；--purge-partials 额外删除没有其他作业清单引用的暂存数据。已发布文件与原始源文件保留；节点不通或清理失败时，保留本地作业记录以便重试。删除后不能用这个 ID 续传。

恢复稀疏暂存时，用 SEEK_DATA 确认未分配空洞，跳过其读取和摘要计算，仍标记为缺块。零长度文件不参与物理范围判定；不支持该接口的文件系统回退普通校验，不按文件长度假定完整。1 GiB 稀疏文件重开、有效块复用及独立删除保护测试通过。

真实 RAID 再次打开 519,520,194,560 字节的空洞文件，30,966 块扫描耗时 0.153 秒，约 0.60 秒开放监听；仍为 0 已验证字节。这是一次元数据空洞扫描验证，不是传输测速，也不是对旧版本的配对性能比值。[记录](sparse-recovery-results.json)。
