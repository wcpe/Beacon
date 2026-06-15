---
name: update-docs
description: 在 Beacon 中当工作项本身就是文档时使用——写 / 改 PRD、ARCHITECTURE、ADR、API、README、OPERATIONS、CHANGELOG，写或取代 ADR，修文档漂移、整理文档结构等纯文档任务。当用户说"更新文档、改下 README、写个 ADR、文档过时了、补充架构说明、整理一下文档"时触发。
---

# 更新文档（Beacon）

## 核心原则

文档是活的真源，改它要遵循每篇的**演进方式**、保持**单一真源**、不留漂移。**纯文档任务**用本技能；若文档变更是**伴随代码改的**，走对应的 `develop-feature` / `fix-bug` / `refactor-code` 等技能（它们自带 `doc-sync` 步骤）。

## 强制流程

### 1. 定位：改哪篇、按什么方式演进
对照 `docs/CONTRIBUTING.md` 的文档地图（§2）与冷热分层（§9），确认动哪篇、它该怎么演进：
- **PRD**：增量加 FR + 状态流转（`计划`→`开发中`→`已交付@版本`），已交付的保留标版本、不删。
- **ARCHITECTURE / API**：**原地更新**到当前真貌。
- **ADR**：**不可变 + 取代**——不改旧 ADR 决策正文；决策变了写新 ADR（编号 = 现有最大 + 1）取代旧的，旧的只改状态行 + 链接（见 CONTRIBUTING §3.1）。
- **CHANGELOG**：进未发布段（发版分段是 `release-version` 的事）。
- **README / OPERATIONS / SECURITY**：原地更新。
- **`.claude/rules/*`（尤其 `architecture-invariants`）、`.claude/skills/*`**：🧊 近乎不变，改它=动项目根基，要慎重且通常**配 ADR**。

### 2. 单一真源
- 同一事实只在一处权威写，别复制散落；需要时**引用而非复制**。新加的交叉链接必须指向真实存在的文件 / 章节。

### 3. 改写
- 精准修改：只动该动的；保持既有结构与中文风格（`comments` / `config-files` 规范）。
- 写 ADR 用既定模板（状态 / 背景 / 决策 / 理由 / 后果 / 备选）；**取代**旧决策时三件事一起做：新 ADR + 旧 ADR 状态行 + 同步受影响的 `rules` / `ARCHITECTURE` / 技能。

### 4. 防漂移自检
- 过一遍 CONTRIBUTING §5 防漂移检查清单：版本号 / 坐标 / 路径 / 端点是否过时、ADR 链接是否有效、是否与代码现状一致、单一真源有无被破坏。
- 若发现文档与代码不符——这就是漂移——按**代码现状**把文档改正。

### 5. 提交
- 中文提交 `docs(scope): …`（ADR 用 `docs(adr): …`），无 AI 署名，不自动 push。

## 红线

编辑已接受 ADR 的决策正文（应另写 ADR 取代）· 把易朽过程稿写进 `docs/` 入库（应留 `.tmp/`）· 改完留下漂移 / 失效链接 · 同一事实多处复制各说各话 · 随手改架构不变量而不走 ADR · 用纯文档会话取代蕴含代码变更的决策却不落代码（应转 `develop-feature` / `refactor-code` 或建跟踪项）。
