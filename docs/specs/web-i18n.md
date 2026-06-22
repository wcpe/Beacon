# 功能规格：前端 i18n 框架接入 + 全站文案 key 化

> 状态：开发中　·　关联 PRD：FR-50　·　分支：feature/fr-50-i18n

## 1. 背景与目标

管理台（`web/`）的全部可见文案此前硬编码在组件里，无单一真源、无法支撑多语言；审计 `action` 直接展示后端英文枚举，运维不可读。本规格按 FR-50 引入完整 i18n 框架（react-i18next）、把全站硬编码中文搬到翻译资源经 `t()` 取用、把审计 action 经 i18n key 映射成中文。属 P2 管理台增强（增强 FR-6/FR-7/FR-21）。决策见 [ADR-0033](../adr/0033-web-i18n-framework.md)。

## 2. 需求（要什么）

- 引入 `react-i18next` + `i18next`，本期**只交付 zh-CN 全量**（框架就绪后加 en 只需补资源文件）。
- 全站硬编码中文文案 key 化：导航、页标题、按钮、表头、占位符、空态、提示、label、对话框等，经 `t('key')` 取用。
- 状态 / 角色 label key 化：角色 `bungee→BC 代理`/`bukkit→子服`；健康状态 `online`/`lost`/`offline` 与密钥状态等保留当前可见文本（英文原值仍为英文、中文值仍为中文）。
- 审计 action i18n 映射：后端**不改、仍返英文枚举**；前端 `audit.action.<枚举>` 键族 + `defaultValue` 回退，已知枚举显示中文、未知回退原文。
- 范围内：纯前端（`web/`）；i18n 框架接线（`main.tsx`/`test/setup.ts`）；逐组件文案 key 化。
- 不做（范围外）：控制面 / 后端任何改动；mock 契约改动；`tsconfig`/构建配置改动（除 i18n 必需的最小接线）；把状态文案从英文改成中文（属文案设计变更，非 i18n 接入）；除 react-i18next/i18next 外的新依赖；en 等其他语言资源。

## 3. 设计（怎么做）

仅改前端。新增 `web/src/i18n/index.ts`（i18next 同步初始化，`resources` 内联、`lng`/`fallbackLng` 均 `zh-CN`）与 `web/src/i18n/locales/zh-CN.ts`（全量翻译资源，分层 key）。`main.tsx` 与 `test/setup.ts` 各 import 一次 i18n 模块完成接线（测试环境同步可用、不出裸 key）。各组件改用 `const { t } = useTranslation()` + `t('key')`。审计 action 经 `t('audit.action.' + action, { defaultValue: action })` 映射。架构决策见 [ADR-0033](../adr/0033-web-i18n-framework.md)，此处不重复决策正文。

**key 组织**：`nav.*`（导航）、`common.*`（跨页复用：查询/取消/操作/删除/环境/大区/小区/状态/角色…）、`status.*`/`role.*`（label）、`audit.*`、`instances.*`/`configs.*`/`zones.*`/`namespaces.*`/`apikeys.*`/`dashboard.*`/`imprint.*`/`topology.*`/`proxies.*`/`login.*`/`filepreview.*` 等各页域。同义文案归并到 `common`（DRY）。

## 4. 任务拆分

- [ ] 加依赖 react-i18next/i18next；建 `web/src/i18n/`（index.ts + locales/zh-CN.ts）；接线 main.tsx 与 test/setup.ts
- [ ] 共享组件 key 化（Layout 导航、AsyncSection、DataTable、StatusBadge、RoleBadge、SystemHeader、CodeEditor）
- [ ] 各页 key 化（Login/Dashboard/Configs/FilePreview/Imprint/Instances/Topology/Zones/Audits/ApiKeys/Namespaces/Proxies 及其子组件）
- [ ] 审计 action i18n 映射（AuditsPage 动作列 / 详情）
- [ ] 文档同步：PRD FR-50 状态、CHANGELOG、ARCHITECTURE 前端结构（i18n 层）

## 5. 验收标准

- `web/` `pnpm test` 与 `pnpm build` 全绿（基线 29 文件 / 135 例；因 key 化必须改的测试改到正确断言、不删不跳）。
- 渲染出的可见文本与改造前逐字一致（等值迁移）；全站无裸 key。
- 审计页已知 action 显示中文、未知 action 回退英文原文。
- zh-CN 资源成为前端文案单一真源；新增语言只需补资源文件、组件零改动。

## 6. 风险 / 待定

- 异步加载会导致测试拿不到文案 → 裸 key：以**同步内联初始化**规避。
- 漏 key / 取值方式变化可能导致个别测试红：以"渲染文本不变"为锚，逐一修到正确断言。
- 浏览器目视（各页文案正常、无裸 key）在本环境无法起整 app，标「待真机验」。
