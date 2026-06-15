---
name: release-version
description: 在 Beacon 中发布版本 / 升版本号 / 出 release / 打 tag 时使用。当用户说"发个版本、出 release、升版本号、打 tag、准备发布、出个 X.Y.Z"时触发。触发即允许创建本地 release 提交与本地 tag，但永不 push。先阻断验证、按 commit 定 SemVer、CHANGELOG 先行，再打 tag。
---

# 发布版本（Beacon）

## 核心原则

发版 tag 必须指向"版本号、CHANGELOG、文档、可交付物构建结果全部一致"的本地提交。触发本技能即表示允许创建**本地** release commit 和本地 tag，但**永远不含远程 push 授权**。

Beacon 的可交付物：控制面（Go 二进制 / Docker 镜像，含内嵌前端）+ 两个 agent jar（BeaconAgent、BeaconAgentProxy）。

## 强制流程

### 1. 预检工作区
- `git status --short` 识别已有改动，不覆盖无关文件；读 `.claude/rules/git-commit.md` 确认中文提交、无 AI 署名。

### 2. 阻断验证（改版本号前，确认基线本就可发布）
- 构建**全部可交付物**并跑通**全部测试**：控制面 `npm run build`（前端）+ `go build` / Docker 镜像；agent `gradle build`。
- 任一失败 → 立即停止、报告失败命令与关键错误、询问是否修复；不得改版本、提交或打 tag。

### 3. 按 commit 自动判断 SemVer
- 找上一个版本 tag：`git describe --tags --abbrev=0 --match "v[0-9]*.[0-9]*.[0-9]*"`。
- 审阅该 tag 到 HEAD 的所有提交，取最高级：
  - 对外 REST 契约 / 配置 / 数据模型不兼容或含 BREAKING → **主版本 +1**。
  - `feat` 或新增用户可见能力 → **次版本 +1**。
  - `fix` / `perf` / `refactor` / `docs` / `chore` 且无新增能力 → **修订版本 +1**。
- 用户指定等级仅作参考；与提交内容冲突时以 SemVer 推导为准。

### 4. CHANGELOG 先行（必须先于打 tag）
- 把 `CHANGELOG.md` 的 `## 未发布版本` 段整理为 `## X.Y.Z（YYYY-MM-DD）`，按 新增 / 变更 / 修复 / 移除 / 安全 分类，引用具体模块 / 能力，禁"修了些 bug"之类空话；随后清空未发布段。
- 发版说明以 CHANGELOG 本版本段为准。

### 5. 版本号与文档同步
- 更新根 `VERSION` 文件（**唯一版本来源**，构建注入控制面与两个 agent，见 `docs/adr/0007-versioning-and-release-channels.md`）。
- 同步 README / docs 中过时的版本引用；结构变化更新 `docs/ARCHITECTURE.md`。
- 把本版本交付的 PRD 功能需求（FR）状态改为 `已交付@vX.Y.Z`。
- 注：发快照 / 开发版走 `publish-snapshot` 技能；本技能只出正式 tag 版本。

### 6. 最终验证与自审
- 再次构建全部可交付物、测试全绿，tag 指向这次最终态。
- `git diff` 自审：只含版本、CHANGELOG、必要文档；不含 `.tmp/`、过程稿、凭据。

### 7. 提交与打 tag
- 只暂存相关文件（禁 `git add .` 式全量）；中文提交 `chore(release): 发布 vX.Y.Z`。
- 创建本地附注 tag：`git tag -a vX.Y.Z -m "发布 vX.Y.Z"`，报告 commit 与 tag 名。
- **禁止任何 `git push` / `git push --tags`。** 推送由用户决定。

## 红线

构建或测试失败仍发版 · SemVer 等级无法确定 · 目标 tag 已存在 · CHANGELOG 未先写好 · 需要远程 push · 提交不合中文规范。
