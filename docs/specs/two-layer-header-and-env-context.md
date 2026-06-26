# 功能规格：两层页眉框架 + 环境全局上下文

> 状态：开发中　·　关联 PRD：FR-105　·　分支：feature/two-layer-header

## 1. 背景与目标

当前页眉是两套各管各：全局 `SystemHeader`（控制面状态条，每页一样、不显示当前页）+ 每页内容区各自 `<h1>` + 工具栏。结果是"当前在哪页"只由侧栏高亮表达，页眉不参与；各页标题/主操作位置不统一。

本 FR 把页眉统一为**两层骨架**，并把 `namespace`(环境) 升为**前端全局上下文**（跨页记住）。它是管理台前端布局重设计批1 的**地基**，FR-106/107/108 都依赖它先落 master。属 P2，纯前端。

## 2. 需求（要什么）

- **第一层 = 全局状态条**（全站不变）：沿用现 `SystemHeader` 内容——左 连接药丸 + 版本徽章(可用更新红点、点击跳 `/system/version`) + 运行时长·在线数；右 搜索⌘K + 主题 + 大屏入口。断线时药丸变红 + 贴下沿细红条（沿用 FR-78，行为不回归）。
- **第二层 = 页面头带 PageHeader**（槽位固定、单一真源）：左 标题 + 计数/副标题槽；右 环境槽(仅环境范围页) + 页面主操作槽。
- **页面标题单一真源**：复用 `navModel` 叶子的 `labelKey`（如 `nav.servers`），不新增标题 i18n 键。
- **环境全局 store**：`namespace` 升前端全局状态（localStorage 持久化、跨页记住、刷新仍在），提供 hook 读写；切换不调任何新后端端点。
- **全站接入**：每页用 `usePageHeader({ title?, count?, actions?, envScoped? })` 注入第二层内容，移除各页重复的顶部 `<h1>`；原 h1 行内的主操作按钮上提到 PageHeader 主操作槽（机械迁移）。
- 范围内：框架 + env 全局 store + 全站接入两层页眉（标题 + 既有主操作 + 环境范围页的环境选择器）。
- 不做（范围外）：列表页 A+E（FR-106）、卡片降级/页内组织（FR-107/108）、把各页内部筛选(group/zone/role/status)迁到全局、env 选择驱动各页数据刷新的逐页改造（各页在其 FR 内迁）、窄屏自适应折叠、后端/agent 改动、新 ADR。wallboard(无页眉)/login(Layout 外) 不动。

## 3. 设计（怎么做）

仅前端（`web/`），无后端/agent/契约改动，不写 ADR（两层页眉不违背 ADR-0048 扁平 IA；env 全局上下文为前端状态决策，本 spec 即记录）。

- `web/src/state/environment.ts`：环境全局 store，镜像 `state/preferences.ts` 范式（`useSyncExternalStore` + 订阅者集合 + localStorage 键 `beacon.environment`）。默认 `''`(全部/未选)；`setEnvironment(ns)` / `useEnvironment()` / `currentEnvironment()`。
- `web/src/components/PageHeader.tsx` + `PageHeaderContext`：
  - `PageHeaderProvider`（置于 `Layout`）持有当前页头配置；`usePageHeader(config)` 由各页在渲染期 set（effect 同步）。
  - `PageHeader` 渲染第二层：标题（页未设则回退当前路由 `navModel` 叶子 `labelKey`）+ 计数/副标题 + 环境槽(envScoped 时渲染 `EnvSelector`，默认值取 `navModel` 叶子 `envScoped` 标记，页可覆盖) + 主操作槽。
- `web/src/components/EnvSelector.tsx`：复用既有 `Combobox` + `listNamespaces`/`namespaceOptions`，读写环境全局 store；含"全部"选项。
- `web/src/lib/navModel.ts`：叶子加 `envScoped?: boolean` 标记（dashboard/configs/servers/audits/alert-events/service-analysis/commands/zones/topology/file-preview/imprint/reverse-fetch = true；api-keys/namespaces/settings/system/version = false）。
- `web/src/components/Layout.tsx`：主内容列顶部依次渲染 第一层 `SystemHeader` + 第二层 `PageHeader`；`main` 淡入/滚动行为保持。
- 各页：移除顶部 `<h1>` 行，改 `usePageHeader({ title: <i18nKey>, actions: <既有主操作>, envScoped })`；计数/筛选-env 联动留后续 FR。

## 4. 任务拆分
- [ ] 测试先行：`state/environment.test.ts`(读写/持久化/跨实例广播)、`components/PageHeader.test.tsx`(标题回退路由、计数/主操作槽、envScoped 渲染 EnvSelector)、`components/EnvSelector.test.tsx`(选择写 store + 持久化)
- [ ] `state/environment.ts` 环境全局 store
- [ ] `components/EnvSelector.tsx`
- [ ] `components/PageHeader.tsx` + `PageHeaderProvider` / `usePageHeader`
- [ ] `lib/navModel.ts` 加 `envScoped` 标记
- [ ] `components/Layout.tsx` 渲染两层
- [ ] 全站各页接入 `usePageHeader`、移除重复 h1、主操作上提
- [ ] i18n：如需"全部环境"等新文案补 `zh-CN.ts`（无缺键）
- [ ] 文档同步：PRD 状态(开发中)、ARCHITECTURE(前端页眉骨架/env 上下文)、CHANGELOG 未发布段

## 5. 验收标准
- 全站每页呈现两层页眉：第一层全局状态条全站一致；第二层显当前页标题 + 环境槽(环境范围页) + 主操作。
- 环境选择器切换后跨页保持、刷新仍在(localStorage)；不调任何新后端端点。
- FR-78 断线行为不回归（药丸变红 + 细红条）。
- 各页无重复标题、既有功能不回归。
- i18n 无缺键、暗色正常。
- `cd web && pnpm test` 全绿 + `pnpm build` 通过；真机逐页验两层页眉与环境跨页保持。

## 6. 风险 / 待定
- 各页接入是机械但量大(17 页)：逐页移除 h1 + 接 usePageHeader，须确保既有主操作/功能不丢、布局不破。
- env 全局 store 本 FR 仅落"页眉选择器 + 持久化 + 跨页保持"；各页内部筛选改读全局 env 留各自 FR，避免本 FR 膨胀。
