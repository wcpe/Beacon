# Git 提交规范

> 适用于本仓库所有 `git commit` 操作。

## 1. 提交信息语言（强制）

- **标题（Description）与正文（Body）必须使用简体中文。** 禁止英文、日文等非中文。
- Conventional Commits 的 type 与 scope 仍用英文小写（`feat`/`fix`/`refactor`/`docs`/`chore`/`test`/`build`/`ci`/`perf`/`style`）。
- **禁止在提交信息中添加任何 AI 签名或尾注**，例如 `Generated with ...`、`Co-Authored-By: ...`。不要附加作者/工具/来源署名。

### 1.1 标题格式

```
<type>(<scope>): <中文描述>
```

- `<scope>`：英文小写模块/能力域，可选。常用：`server`、`agent`、`web`、`config`、`registry`、`longpoll`、`merge`、`store`、`api`、`build`、`ci`、`docs`。
- `<中文描述>`：简洁陈述本次做了什么，必须中文，结尾不加句号。

### 1.2 正文格式

- 用空行与标题分隔，中文撰写，可用 `-` 列要点。
- 说明"为什么改"与"改动要点"，不逐行复述 diff。

### 1.3 示例

✅ 正确
```
feat(longpoll): 实现配置长轮询「唤醒即重算比对」

- 先注册 waiter 再算 md5，消除注册前发布丢唤醒窗口
- 发布事务提交后按 scope 算最小受影响集合再唤醒
- 被唤醒重跑解析比对 md5，真变才下发
```

❌ 错误（标题英文）
```
feat(longpoll): add long-poll push
```

### 1.4 禁止阶段性词语（强制）

提交按**功能点**描述，不按**开发阶段**描述。commit message（标题与正文）**禁止**出现阶段 / 批次性词语：`Phase 0`、`P0` / `P1` / `P2`、`MVP`、`Sprint`、`第一期` / `本次迭代` 等。它们说的是"项目走到哪一步"而非"这次改了什么"，会随时间失效、也无法追溯到具体改动。

✅ 正确（描述功能点）
```
feat(config): 实现 scope 覆盖链键级深合并
```

❌ 错误（用阶段词代替功能描述）
```
feat: 完成 MVP 第一期配置中心
chore: P1 Sprint 3 的若干功能
```

## 2. 文档入库边界（强制）

判据：**活文档（长期维护、是真源）入库；易朽稿（做完即弃）留 `.tmp/`。**

### 2.1 应当入库的耐久文档

- 产品 / 需求：`README.md`、`CHANGELOG.md`、`docs/PRD.md`（活文档，随需求变更同 PR 更新）。
- 架构：`docs/ARCHITECTURE.md`、`docs/adr/*.md`、`docs/API.md`。
- 协作治理：`docs/CONTRIBUTING.md`、`.claude/rules/*.md`。

### 2.2 严禁入库的易朽过程稿（已由 `.gitignore` 排除 `/.tmp/`）

- 实施计划 / 里程碑 / 路线图：`实施计划.md`、`PLAN.md`、`roadmap.md` 等。
- 过程性报告：`IMPLEMENTATION.md`、`执行报告.md`、`分析.md`、`audit-*.md` 等。
- AI 助手过程性笔记、交流稿、思路记录。

> 例：PRD 是活的需求规格 → 入库 `docs/`；实施计划易朽 → 留 `.tmp/`。文档与代码的同步要求见 `docs/CONTRIBUTING.md` 与 `.claude/rules/doc-sync.md`。

## 3. 最小提交粒度（强制）

- **独立可编译**：每个 commit 落地后代码都能编译 / 构建通过，不留"半截"提交。
- **只做一件事**：一个 commit 只对应一个功能点 / 一个修复 / 一次重构，无关改动不混入。
- **不混类型**：不在同一 commit 里混 `feat` / `fix` / `refactor`——各自独立提交（顺手发现的 bug 单独 `fix`，重构单独 `refactor`）。

✅ 正确（拆成独立、各自可编译、各做一件事）
```
feat(registry): 实例注册表支持按 zone 标签过滤
fix(longpoll): 修复注册前发布导致的丢唤醒
refactor(merge): 提取 deepMerge 为独立纯函数
```

❌ 错误（一个 commit 混了功能 + 修复 + 重构）
```
feat: 加 zone 过滤，顺便修个长轮询 bug 并重构 merge
```

❌ 错误（半截、单独不可编译）
```
feat(api): 加 discovery 端点（handler 还没接，编译不过）
```

## 4. 提交卫生与禁止入库内容（强制）

- 禁止跳过 hooks（`--no-verify`）。禁止对已 push 的提交 `--amend`。
- **禁止入库的内容**（每次暂存前必须自检）：
  - **隐私信息**：真实姓名、个人邮箱、手机号、住址、账号等个人可识别信息。
  - **IP 与真实地址**：真实公网 / 内网 IP、真实主机名与域名——文档与示例一律用保留地址（`192.0.2.0/24`、`example.com`、`beacon.internal` 等占位符）。
  - **密钥与凭据**：API key、token、密码、私钥、证书、含凭据的连接串（如带账号密码的 DSN）。
  - **二进制与产物**：可执行文件、jar、编译产物、数据库文件、大型数据文件。
  - **易朽过程稿**：见 §2.2（`.tmp/` 内的一切）。
- 发现上述内容**已经入库**时：**立即报告**并给出清理方案（说明是否需重写历史、影响哪些提交），不得静默忽略或悄悄改掉。

## 5. 集成方式：squash merge（强制）

- **一切 PR 经 squash merge 并入 `master`**：即 GitHub 的 "Squash and merge"，把 PR 内的多个提交压成**单个**提交入 master。禁止 merge 提交、禁止 rebase-merge 保留多提交、禁止直推 `master`。
- **合并标题必须带 PR 编号**：`<type>(<scope>): <中文描述> (#NN)`，`#NN` 为该 PR 的 GitHub 编号。标题其余部分沿用 §1.1。
- **合并正文必须自行总结**：**禁止直接套用 PR 的 body 原文**。合并者须读 `git diff` 后用自己的话重写「这次 PR 改了什么」，语言与格式沿用 §1.2（中文、说清为什么改与改动要点、不逐行复述 diff）。
- 本约束对**新并入**生效；历史中已存在的 merge 提交与多提交 PR 保留、不追溯重写。

## 6. 分支与 PR 合入（强制）

- **一切变更经 PR 进 `master`**：禁止任何情形直推 `master`（单人期、发版提交同样经 PR）。本地允许 `dev` 等集成草稿分支，但进 `master` 仍走 PR。
- **分支命名**：`feature/*`、`fix/*`、`refactor/*`、`hotfix/*`、`docs/*`、`chore/*`；短生命周期，一个 PR 只做一件事（粒度见 §3）。
- **合入前同步**：PR 分支须先 `rebase` 到最新 `master` 并确认 CI 全绿；实际并入由 GitHub 的 squash merge 完成（见 §5），无需本地 FF 合并。
- **单一提交结果**：PR 内的多个提交在并入时压成**一个**（见 §5）；PR 内部仍保持逻辑提交粒度，便于 review。
- **回滚**：优先 `git revert`；严禁 `force push` 到 `master`（`rebase` 冲突报告用户、不强推）。
- **提交卫生**：严禁 `--no-verify`，严禁 `--amend` 已 push 提交（见 §4）。
- **PR 标题**：沿用 §1 的 Conventional + 中文；自动发布说明只统计两 tag 间合并的 PR 标题，标题质量决定 Release 外观。
- **合并门槛**：CI 质量门全绿才合；PR 的产品打包必须为 `skipped`（由 quality-gate 断言）。
- **分支保护开关**：由人在网页 Settings 开启（要求 PR、要求 status checks、禁 `force push`），AI 只遵守、不操作网页。
