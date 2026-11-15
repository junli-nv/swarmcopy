# 大规模冷页缓存对照方案

[English](COLD-STUDY.en.md) · [粗测记录](BENCHMARK.zh-CN.md)

本方案在正式数据传输前保存，固定程序与驱动摘要、节点集合、顺序和每轮 UUID，避免根据结果挑选有利设置。

## 固定设计

- 同一份不可变介质，同一计算节点种子，同一组接收节点。
- 三轮配对，方法顺序为 rsync→SCDP、SCDP→rsync、rsync→SCDP。
- 每种方法开始前，对全部参与计算节点执行 sync 后写入 drop_caches=3，记录返回值、缓存和负载信息；不对 BCM 头节点执行。
- 每个方法、每轮都有新的唯一目录；任何节点发现该目录已存在即拒绝开始。跨轮不复用最终文件、部分文件或任务记录。
- 传输开始后的 TCP 重传、分块重试和恢复是正常机制，均允许，耗时持续累计，不重置计时。
- 程序及两架构部署文件 SHA-256 在测试前固定，每个方法前核对未变化。
- 使用已准备且验证过的种子；双方都不计初始介质导入。源文件校验记录不是接收端的已传数据；清 OS 页缓存也不会删除磁盘上的校验记录。

## 完成与真实性检查

rsync 使用真实 SSH 传输，不启用压缩或带宽限制。每个接收节点复制后立即执行 SHA-256，允许不同节点的校验自然重叠。分别记录全部复制完成和全部校验完成的时间。正常单次成功要求 literal bytes 和 transferred file bytes 等于完整文件大小；发生本轮内重试时保留全部尝试记录。

SCDP 不限速。每个接收节点须 Origin=false、Downloaded 等于完整文件大小、Complete=true；总下载量须等于文件大小乘接收节点数。计时包括部署、数据传输、最终 SHA-256、fsync 与本任务进程停止。

最终报告给出所有轮次、中位数和最小/最大值；保留失败及取消记录，不把未完成的轮次计为成功。

## 可比性边界

- 这是方案端到端对比，不是仅改变网络拓扑的消融实验：rsync 的数据走加密 SSH，SCDP 数据为明文 HTTP，安全属性不同。
- drop_caches 清理 OS 的干净页缓存及可回收 dentry/inode，不会清掉正在使用的所有内存，也不能控制磁盘/控制器缓存。参见 [Linux 内核文档](https://docs.kernel.org/admin-guide/sysctl/vm.html#drop-caches)。
- 清缓存会短时增加参与节点的 I/O；本脚本要求显式 --drop-caches，并只对指定计算节点执行。
- 背景训练/系统负载没有完全隔离。三轮小样本仍不能证明所有集群或规模下的固定加速比。
- 中间重试允许；“公平的新起点”只约束每次计时开始前没有该轮介质数据。

## 执行入口

~~~bash
python3 scripts/benchmark-cold.py --help
python3 scripts/benchmark-cold.py \
  --hosts-file hosts.txt --seed node001 \
  --seed-file /raid/images/nemo.sqsh \
  --dest-root /raid/images/cold-study \
  --out ./cold-results-new --swarmcopy ./swarmcopy --bin-dir ./dist \
  --rounds 3 --timeout 1800 --drop-caches
~~~

运行要求控制端 SSH 和 Python 3，参与节点具备 rsync、sha256sum、awk，以及清缓存权限。脚本通过已经认证的控制端 SSH 取得种子公钥，只写测试私有 known_hosts 并预检，不改标准 SSH 配置。所有大文件副本保留在本轮 UUID 目录，结果和日志保留供复核；不会自动删除用户已有文件。


## 查询测速作业

测速的 SCDP 作业使用自定义目录，不进入默认 jobs 列表。以上命令运行后，用 ./swarmcopy jobs --state-dir ./cold-results-new 列出；单项用 ./swarmcopy watch --job ./cold-results-new/<case-id>-job --count 1。case-id 在 study-plan.json 中；它与作业 JOB ID 不同。rsync 是脚本直接调用的对照，用 results.json / summary.json 查看，运行阶段见 progress.json。
