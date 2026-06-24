# 功能规格：暗色模式 / NOC 大屏只读 / 紧凑密度

> 状态：开发中　·　关联 PRD：FR-92　·　分支：feature/fr-92-theme-wallboard

## 1. 背景与目标

管理台长期只有浅色一套主题、表格固定密度、且唯一形态是带侧栏的操作台。运维有三类未被满足的诉求：① 夜间 / 机房值班希望**暗色**护眼；② 信息密集的列表页希望**更紧凑**地一屏看更多行；③ NOC（网络运营中心）值班墙希望一块**纯只读大屏**循环展示集群健康，不要任何可误操作的入口。FR-92 给管理台补这三项纯前端的呈现选项，并把偏好**持久化**。属第三期（P3）体验增强。

## 2. 需求（要什么）

- **暗色主题切换**：页眉一个太阳 / 月亮按钮，浅 ↔ 暗一键反转；暗色样式复用 `index.css` 既有 `.dark` 变量。
- **表格紧凑密度**：页眉一个密度开关（舒适 ↔ 紧凑两档）；紧凑档收紧统一表格 `DataTable` 的行高与单元格内边距，所有用 `DataTable` 的列表页自动响应。
- **NOC 大屏只读看板**：新路由 `/wallboard`，复用可观测看板的只读数据 / 卡片，以大字号 / 大间距全屏呈现，**纯只读**——不含任何下线 / drain / 改派 / 编辑 / 筛选入口。无侧栏，极简页眉仅含「退出大屏」返回链接 + 主题切换。
- **偏好持久化**：主题与密度落 `localStorage`，跨会话恢复；首屏按持久化值生效避免闪烁。
- 不做（范围外）：第三档及以上密度、跟随系统主题（`prefers-color-scheme`）、按页 / 按表独立密度、大屏自定义布局 / 轮播 / 多屏编排、任何新后端端点或 agent 改动、引入 `next-themes` 或新图标 / 主题依赖。

## 3. 设计（怎么做）

纯前端，零后端、零 agent，不新增 ADR、不加新第三方依赖。

- **偏好 store（单一真源）** `web/src/state/preferences.ts`：镜像 `state/auth.ts` 的订阅者模式（`useSyncExternalStore` + 监听者集合广播），`localStorage` 键 `beacon.preferences` 持久化 `{ theme: 'light'|'dark', density: 'comfortable'|'compact' }`。读写包 `try/catch` 兜隐私模式 / 解析失败回退默认（浅色 + 舒适）；非法持久化值逐字段回落默认（应用层枚举校验）。导出 `usePreferences()` / `setTheme` / `setDensity` / `currentPreferences()` / `applyThemeToDocument()`。**不引入 `next-themes`**，避免与本 store 双真源。
- **暗色主题落地**：`main.tsx` 渲染前同步调 `applyThemeToDocument(currentPreferences().theme)`，按持久化值给 `document.documentElement` 打 / 去 `.dark` 类（避免浅→暗首屏闪烁）；`App.tsx` 订阅 store，运行期切换时 `useEffect` 同步 `.dark` 类。
- **页眉控件** `web/src/components/HeaderControls.tsx`：主题切换（太阳 / 月亮）+ 密度切换（rows 图标，复用既有 `lucide-react`）+ 大屏入口（monitor 图标 → `/wallboard`），右对齐挂进 `SystemHeader`（即页眉）。
- **紧凑密度** `web/src/components/DataTable.tsx`：直接 `usePreferences()` 读 density，紧凑档对表头加 `h-8`（替代底层 `table.tsx` 默认 `h-10`）、单元格加 `py-1`（收紧默认 `p-2` 的上下内边距），舒适档不加类沿用默认。改动限定在 `DataTable` 内部类切换，不破坏既有列渲染。
- **大屏布局 + 页** `web/src/components/WallboardLayout.tsx`（无侧栏极简壳：标题 + 主题切换 + 退出大屏链接）承载 `web/src/pages/WallboardPage.tsx`：复用 `dashboard/SummaryCards`、`dashboard/BCPanel`、`dashboard/StatCard` 与 `metricsSummary` / `listInstances` 只读查询（聚合全部环境、短周期轮询），大字号大间距呈现，无任何操作按钮。`App.tsx` 在 `RequireAuth` 下加 `/wallboard` 路由，页眉 `HeaderControls` 提供入口。
- **i18n**：新增 `preferences.*`（主题 / 密度切换无障碍标签、大屏入口）与 `wallboard.*`（标题、退出）文案，只补 `zh-CN`。

## 4. 任务拆分

- [x] 写规格（本文）
- [x] PRD §4 FR-92 行保持「计划」（发版时再翻），验收写入本 §5
- [x] 偏好 store `state/preferences.ts` + 单测（持久化 / 恢复 / 广播 / 隐私模式不崩 / applyTheme）
- [x] 暗色主题：`main.tsx` 首屏同步应用 + `App.tsx` 运行期同步 `.dark` 类
- [x] 页眉 `HeaderControls`（主题 / 密度切换 + 大屏入口）挂进 `SystemHeader` + 单测
- [x] `DataTable` 紧凑密度 + 单测（紧凑 / 舒适样式类断言）
- [x] `WallboardLayout` + `WallboardPage`（复用看板只读组件）+ `/wallboard` 路由 + 单测（只读、无操作入口）
- [x] i18n 文案（`preferences.*` / `wallboard.*`）
- [x] 文档同步：本规格、CHANGELOG 未发布段

## 5. 验收标准

- 页眉点主题按钮，浅 ↔ 暗切换即时生效（`document.documentElement` 有 / 无 `.dark` 类）；刷新后主题按持久化值恢复、首屏不闪。
- 页眉点密度开关，紧凑档下 `DataTable` 表头 / 单元格渲染出收紧样式类（`h-8` / `py-1`）、舒适档无；密度持久化、刷新后恢复。
- `/wallboard` 渲染只读集群健康数据（在线 / 异常计数、KPI 总览、BC 面板），**不含任何操作入口**（无下线 / 改派 / drain / 编辑 / 删除 / 保存按钮）；可经退出链接返回常规管理台。
- 隐私模式（`localStorage` 写失败）下切换不崩，仅本次会话内存生效。
- `cd web && pnpm test` 全绿（含新增 store / 组件 / 页面用例）、`pnpm build` 通过。

## 6. 风险 / 待定

- 暗色下各页 / 第三方组件（recharts 图表、radix 浮层）的实际观感需真机浏览器逐页核对；本期只保证 `.dark` 变量驱动的基础配色生效，个别硬编码色值如有突兀留待真机暴露后增量修。
- 紧凑密度只作用于走 `DataTable` 的统一表格；自管表格 / 非表格密集区不在本期范围。
- 大屏当前为单页静态布局（无轮播 / 多屏编排），面向单块值班墙；更复杂的大屏编排为后续另议。
