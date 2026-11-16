# 统一元数据目录与自动清理

[English](CLEANUP.en.md) · [示例](EXAMPLES.zh-CN.md) · [开发指南](DEVELOPMENT.zh-CN.md) · [文档索引](README.md)

目标目录下统一使用一个 `.swarmcopy`，不把介质或节点暂存搬到用户目录。所有目标通过所选校验并完成发布后，默认停止本作业 peer，自动删除本作业部署文件和已用完的暂存，再保存完成摘要。源文件和成品保持原位，控制器的 jobs/status/watch 记录继续可查。

## 完成后保留什么

```text
/raid/images/
  nemo.sqsh
  .swarmcopy/
    cache.json       # 最近一份清单的已验证成品指纹；有可用缓存才写
    last-peer.log    # 最近一次 peer 日志，最多 8 MiB，覆盖旧日志
```

每个目标目录只复用这两个固定位置，不按任务永久保留一套缓存/日志，也不为每个数据文件创建旁挂缓存。cache.json 绑定完整清单 ID 和各文件的设备、inode、大小、mtime/ctime；不同清单交替可能重新检查已有文件，不会借错误缓存跳过内容验证。chunks 新下载成品不会凭验块结果生成 full 成品缓存；实际检查过的已有文件和 full 成品可以缓存。

仍运行或中断的作业在 `.swarmcopy/<job-id>` 保留部署/状态，在 `.swarmcopy/trees/<manifest-id>/data` 保留同盘暂存，这是续传必需的数据；成功收尾才清理。传输中按任务隔离是为了并发和归属安全，成功后不会不断累积目录。

控制器已有的 `~/.local/state/swarmcopy/jobs` 和来源摘要缓存继续用于历史查询及复用，不是从目标盘迁移来的介质或暂存。持久锁文件遵守原来的互斥规则，不在运行时盲目 unlink；空锁文件存在不代表持有锁。

## 手动收尾或重试清理

```bash
./swarmcopy jobs
./swarmcopy cleanup --job <ID>
./swarmcopy cleanup --help
```

cleanup 只接受完整完成且有结果记录的作业，停止该作业仍在供种的 peer，清掉部署和空暂存，保留介质、控制器记录及统一缓存/日志。已记录清理成功时重复执行直接返回，不再 SSH 部署。与 remove/delete 不同，cleanup 不删除 jobs 记录。

默认完成会自动清理，无需另外执行。`--keep-seeding` 明确保留供种进程及其运行文件，之后用 cleanup 收尾。无法停止 peer 时不会继续自动清理。清理失败会显示 CLEANUP WARNING，并把具体节点原因存入 job.json 的 CleanupIssues；已验证的传输结果保持完成，可稍后重试 cleanup。

## 哪些内容不会自动删除

- 中断、校验失败、写盘/fsync 失败的作业及实际暂存数据，保留同 ID 续传入口。
- 其他作业引用的相同清单暂存，显示 KEEP SHARED STAGING；只删除本作业自己的部署目录。
- 清理时遇到活动目标锁、符号链接、不明暂存文件或其他无法确认归属的内容，保留并报告。
- 正式源、已发布成品、目标目录中额外文件和其他作业数据。

这是逐作业清理，不会递归扫描整个 RAID 盘找文件删除，也不做所有旧任务的批量搬迁。旧 `.swarmcopy-trees/<id>` 按需原地读取和续传；成功后只处理当前清单对应的空暂存，并在父目录为空时移除旧父目录。旧 full 缓存可合并到固定 cache.json，迁移的是小型元数据，不复制介质。共享或未完成的旧目录仍会保留。

## 开发与验证

`treeNamespace` 优先识别旧暂存，新增暂存及导入临时流使用 .swarmcopy 内部路径。cache.json 使用临时文件、fsync、原子替换和目录 fsync，ID 不匹配时不采信。`cleanupPeerMode` 复核配置归属、agent/peer 锁及进程启动身份。`completedTreeCleanup` 仅清空目录结构和已知完成标记/缓存，发现任意数据文件就拒绝自动删除；`remove --purge-partials` 是另外的显式丢弃操作。

`cleanupCompletedNodes` 在全部目标完成并停止 peer 后调用，独立记录失败节点；不会把清理错误伪装成数据校验失败。cleanup 命令持控制器锁，并支持按 SSH 配置和共享密码处理旧完成作业。完成耗时包含本次清理；旧历史摘要不会重写成一次新的传输。

已通过 race/vet/CLI、成品和缓存保留、活动锁/共享/中断数据保护、符号链接拒绝、30 次缓存替换不累积、日志 8 MiB 上限、旧暂存原位恢复测试。实机检查包括两节点 staged chunks/full、三节点源目标同路径及 QUIC、继续供种后显式清理、0.7.1 中断后升级 0.8.0 续传、独立 SHA256 及保存的 watch/status/stop 行为。x86_64 已实机验证；aarch64 静态交叉构建，未做 ARM 实机验证。
