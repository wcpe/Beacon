# 功能规格：管理台导航分组与设置区聚合 IA（FR-93 / FR-94）

> 状态：开发中　·　关联 PRD：FR-93、FR-94　·　分支：feature/fr-93-94-frontend-ia　·　决策见 [ADR-0043](../adr/0043-admin-nav-grouping-and-settings-aggregation.md)

## 1. 背景与目标

管理台侧栏导航膨胀到 15 项平铺、缺层级；设置类页面散落多个顶层路由。本规格覆盖 IA 重构的前两步（纯前端）：FR-93 给侧栏导航分组、FR-94 给设置区搭聚合页骨架。第三步 FR-95（旧页折叠进设置页）单独做。属 P2。

## 2. 需求（要什么）

### FR-93 侧栏导航层级化
- 15 项平铺导航收为 5 组可折叠手风琴：概览 / 配置管理 / 集群 / 可观测 / 系统。
- 命中当前路由的组按 `location.pathname` 前缀自动展开。
- 用户手动展开 / 折叠态持久化到前端偏好（`navExpandedGroups`），非法值回落默认。
- 折叠交互用原生 `<details>/<summary>` 或 `useState`+偏好，**不引新依赖**。
- Layout 的 `NAV_ITEMS` 与 CommandPalette 导航目标收敛为单一真源（`web/src/lib/navModel.ts`）。

### FR-94 设置区聚合页骨架
- 三块顶层 tab：运维设置 / 系统信息 / 系统设置；每块再含子 tab。
- 嵌套子路由：`/settings` 重定向 `/settings/ops`；三块为 `/settings/ops`、`/settings/system-info`、`/settings/system-config`；块内子 tab 用 search param。
- 运维设置 6 域（health/metric/longpoll/alert/log/reverse-fetch）改 6 个子 tab；**保留跨子 tab 统一草稿 + dirty + 批量保存 saveAll + 逐项恢复默认**。
- 系统信息块：「版本与更新」「控制面健康」空壳子 tab（占位文案）。
- 系统设置块：「网络代理」「更新设置」「API 密钥」「环境管理」空壳子 tab（占位文案）。
- 一屏不滚动：tab 栏常驻、保存条 sticky 底栏、内容局部滚动；判据 1440×900 主操作可达。

- 范围内：纯前端导航分组 + 设置聚合骨架 + 空壳子 tab 容器。
- 不做（范围外）：版本更新 / 代理 / 更新设置真实逻辑（FR-100/99/101）；旧页（/system、/api-keys、/namespaces）内容并入与旧路由重定向（FR-95）；虚拟滚动。

## 3. 设计（怎么做）

仅前端改动，决策见 ADR-0043，此处不重复正文。

- `web/src/lib/navModel.ts`（新）：NAV 单一真源。导出分组树（5 组，各含叶子 `{ to, labelKey }`）与扁平叶子数组（CommandPalette 消费）。
- `web/src/state/preferences.ts`：新增 `navExpandedGroups: string[]` 字段 + `setNavExpandedGroups` setter + DEFAULT + 逐字段校验回落（非数组 / 含未知组 id 时回落默认）。
- `web/src/components/Layout.tsx`：NAV 改读 navModel 分组树，用原生 `<details>/<summary>` 渲染 5 组手风琴；`open` 态 = 命中路由组 ∪ 偏好持久化态；用户 toggle 写偏好。
- `web/src/components/CommandPalette.tsx`：导航目标改读 navModel 扁平叶子，过滤 / 分组逻辑不变。
- `web/src/pages/SettingsPage.tsx` + 新增设置子页：`/settings` 聚合骨架。顶层三块用嵌套 `<Route>`（App.tsx 内），运维设置块复用现有集中草稿逻辑、6 域改子 tab（search param）。系统信息 / 系统设置块为空壳子 tab 容器。
- `web/src/App.tsx`：`/settings` 改嵌套路由（index 重定向 `/settings/ops`，三块各一子路由）。
- `web/src/i18n/locales/zh-CN.ts`：新增 `nav.group*`（5 组名）、`settings.block*` / `settings.tab*`、空壳占位文案；运维设置 6 子 tab 复用既有 `settings.group*` 键。仅 zh-CN（无 en 语言包）。

## 4. 任务拆分
- [x] PRD FR-93/94 状态改「开发中」
- [x] 写 ADR-0043 + 本规格
- [x] 测试先行（vitest 红→绿）
- [x] 实现 navModel + 偏好字段 + Layout 手风琴 + CommandPalette 收敛（FR-93）
- [x] 实现设置聚合骨架 + 嵌套路由 + 运维设置子 tab 保留集中草稿（FR-94）
- [x] 文档同步：PRD 状态、本规格、ADR、CHANGELOG（ARCHITECTURE 无页级导航 / 设置描述，无需同步）

## 5. 验收标准
- FR-93：侧栏渲染 5 组；所属组按当前路由自动展开；偏好持久化与非法值回落；Layout 与 CommandPalette 导航目标同源一致。
- FR-94：三块 + 运维设置 6 子 tab 渲染；跨子 tab 改 health+log 各一项时统一批量保存 dirtyItems=2；search param 还原子 tab；`/settings` 重定向 `/settings/ops`；1440×900 主操作不需滚到底。

## 6. 风险 / 待定
- 真机验证（组展开折叠 + 持久化、各 tab 切换 + 深链刷新、跨子 tab 批量保存、一屏不滚动）由主控 / 真机环节补。
- 无 en 语言包，i18n 只补 zh-CN。
