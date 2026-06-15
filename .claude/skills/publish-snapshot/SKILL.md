---
name: publish-snapshot
description: 在 Beacon 中发布快照 / 预发布 / 开发版（非正式 tag 版本）时使用。当用户说"发个快照、出个预发布、推个开发版、更新 latest、让人先试试 main 上的版本"时触发。从 main 构建开发版、按 ADR-0007 命名发到快照渠道，不打正式 tag、不动 CHANGELOG 正式段。
---

# 发布快照 / 预发布（Beacon）

## 核心原则

快照是 main 上的**开发版**，供试用，不是正式发布：不打 `vX.Y.Z` 正式 tag、不把 CHANGELOG 未发布段定稿、不保证稳定。命名与渠道见 `docs/adr/0007-versioning-and-release-channels.md`。

## 强制流程

### 1. 前置确认
- 当前在 `main`（或要发的开发分支）、工作区干净。
- 快照版本号 = `<VERSION>-SNAPSHOT+<short-sha>`（VERSION 为根 `VERSION` 文件内容）。

### 2. 阻断验证
- 构建全部可交付物 + 跑全部测试（控制面前端 build + go build / 镜像；agent gradle build）。失败即停止、报告、不发。

### 3. 以快照号构建并发布
- 用快照版本号注入构建产物，**不改 `VERSION` 文件本身、不提交版本变更**。
- 发到快照渠道：镜像 `:snapshot` 与 `:main-<sha>`、agent jar `*-SNAPSHOT.jar`；滚动更新 `latest` 预发布（说明里标注"开发构建、可能不稳定" + 提交 sha + 贡献者）。

### 4. 不做的事
- **不打** `vX.Y.Z` 正式 tag；**不把** CHANGELOG 未发布段改成正式版本段；**不 bump** `VERSION`。
- 正式发布走 `release-version` 技能。

## 与 CI 的关系

此流程通常由 CI 在 main 推送时自动完成（命名与本技能一致，见 ADR-0007）。手动触发本技能用于临时出一个试用版。

## 红线

构建 / 测试失败仍发 · 误打正式 tag · 把快照当稳定版宣传 · 推送破坏 main。
