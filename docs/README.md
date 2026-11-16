# 文档索引

[English](README.en.md) · [项目首页](../README.md)

先从项目首页安装并运行；下面按问题选择主题。使用指南描述当前主线，历史报告保留当时的构建、参数和结果，不应据其推断当前默认值。

## 使用与维护

| 主题 | 中文 | English |
| --- | --- | --- |
| 内存网络基准、LACP 与 NUMA | [中文](NETWORK.zh-CN.md) | [English](NETWORK.en.md) |
| 操作示例 | [中文](EXAMPLES.zh-CN.md) | [English](EXAMPLES.en.md) |
| 来源方式与节点故障 | [中文](SOURCE-MODES.zh-CN.md) | [English](SOURCE-MODES.en.md) |
| 准备进度、停止与恢复 | [中文](PROGRESS.zh-CN.md) | [English](PROGRESS.en.md) |
| 校验与算法 | [中文](INTEGRITY.zh-CN.md) | [English](INTEGRITY.en.md) |
| 目录与清理 | [中文](CLEANUP.zh-CN.md) | [English](CLEANUP.en.md) |
| 文件布局与崩溃恢复 | [中文](TREES.zh-CN.md) | [English](TREES.en.md) |
| 开发架构、验证与发布 | [中文](DEVELOPMENT.zh-CN.md) | [English](DEVELOPMENT.en.md) |
| full 流水校验与 CPU 诊断 | [中文](PIPELINE.zh-CN.md) | [English](PIPELINE.en.md) |
| 可选自适应与尾块调度 | [中文](ADAPTIVE.zh-CN.md) | [English](ADAPTIVE.en.md) |
| 可选 QUIC 数据通道 | [中文](QUIC.zh-CN.md) | [English](QUIC.en.md) |
| QUIC 参数和依赖补丁 | [中文](QUIC-TUNING.zh-CN.md) | [English](QUIC-TUNING.en.md) |

## 历史测试与研究

下列是复核材料，不是安装步骤或功能分支列表。数据中的单位、缓存条件、是否计入初始导入及计时范围应以各报告为准；保留较慢或退化的轮次，不将不同时期收益相乘。

| 研究 | 中文 | English |
| --- | --- | --- |
| 早期分发粗测 | [中文](BENCHMARK.zh-CN.md) | [English](BENCHMARK.en.md) |
| 冷页缓存方法 | [中文](COLD-STUDY.zh-CN.md) | [English](COLD-STUDY.en.md) |
| 大规模冷缓存结果 | [中文](COLD-RESULTS.zh-CN.md) | [English](COLD-RESULTS.en.md) |
| 流水校验对照 | [中文](PIPELINE-RESULTS.zh-CN.md) | [English](PIPELINE-RESULTS.en.md) |
| 自适应初始退化 | [中文](ADAPTIVE-INITIAL-RESULTS.zh-CN.md) | [English](ADAPTIVE-INITIAL-RESULTS.en.md) |
| 自适应修正对照 | [中文](ADAPTIVE-RESULTS.zh-CN.md) | [English](ADAPTIVE-RESULTS.en.md) |
| QUIC 初始对照 | [中文](QUIC-RESULTS.zh-CN.md) | [English](QUIC-RESULTS.en.md) |
| QUIC 消融研究 | [中文](QUIC-TUNING-RESULTS.zh-CN.md) | [English](QUIC-TUNING-RESULTS.en.md) |
| 调度与本地复制对照 | [中文](OPTIMIZATION-0.7.0.zh-CN.md) | [English](OPTIMIZATION-0.7.0.en.md) |
| 早期验证记录 | [中文](VALIDATION.zh-CN.md) | [English](VALIDATION.en.md) |

## 数据与变更

- [测试原始数据及用途](data/README.md)
- [版本变更记录](../CHANGELOG.md)
- [第三方声明](../THIRD_PARTY_NOTICES.txt)

仅需使用软件时无需逐项阅读 JSON。新增说明应更新对应主题和中英文入口；不要继续向 README 或开发指南按 release 追加整段内容。
