# 功能规格：管理台导航分组 + 系统区扁平独立页 IA（FR-93 / FR-94 / FR-95）

> 状态：开发中　·　关联 PRD：FR-93、FR-94、FR-95　·　决策见 [ADR-0043](../adr/0043-admin-nav-grouping-and-settings-aggregation.md)（5 组手风琴有效）与 [ADR-0048](../adr/0048-flatten-system-nav-pages.md)（系统区扁平独立页，取代 ADR-0043 的聚合/折叠/二级子 tab）

## 1. 背景与目标

管理台侧栏导航膨胀到 15 项平铺、缺层级；「系统」类页面此前曾尝试聚合页 + 折叠 + 二级子 tab，真机否决——层级过深、直达性差。现 IA 定为：**FR-93 侧栏 5 组手风琴分组**（保留）+ **「系统」组 5 个扁平独立页**（FR-94 运维设置单页 + 版本与更新独立页；FR-95 控制面健康 / 密钥 / 环境各保留独立路由）。纯前端，属 P2。

## 2. 需求（要什么）

### FR-93 侧栏导航层级化
- 15 项平铺导航收为 5 组可折叠手风琴：概览 / 配置管理 / 集群 / 可观测 / 系统。
- 命中当前路由的组按 `location.pathname` 前缀自动展开。
- 用户手动展开 / 折叠态持久化到前端偏好（`navExpandedGroups`），非法值回落默认。
- 折叠交互用原生 `<details>/<summary>` 或 `useState`+偏好，**不引新依赖**。
- Layout 的 `NAV_ITEMS` 与 CommandPalette 导航目标收敛为单一真源（`web/src/lib/navModel.ts`）。

### FR-94 运维设置单页 + 版本与更新独立页
- **运维设置 `/settings` 为单页**：6 域（health/metric/longpoll/alert/log/reverse-fetch）以**一级 tab** 呈现（禁止两级 tab），保留跨域统一草稿 + dirty + 批量保存 saveAll + 逐项恢复默认。`update.*` 不在本页。
- **版本与更新 `/system/version` 为独立页**：纵向分区——版本信息 / 渠道选择（stable·rc 自由切，写 `update.channel` 热生效后重查）/ 检查更新（`?force=true` + release 日志纯文本安全渲染 + 外链 + check-failed 友好提示）/ 立即更新（FR-76 二次确认 → `POST /system/update` → 进度轮询 → FR-78 重连回显）/ 网络代理（`update.proxy-url` 表单，脱敏回显、未改不覆盖）/ 更新设置（`update.auto-check-enabled` 开关 + `update.check-interval-hours` 1-168）。

### FR-95 系统区扁平独立页
- 控制面健康 `/system`（`SystemObservabilityPage`，FR-82）/ 密钥管理 `/api-keys`（FR-42）/ 环境管理 `/namespaces`（FR-53）**各保留独立路由独立页**，不折叠进设置子 tab、不二级 tab。
- 控制面健康页**补详细明细**：DB 连接池（已建/上限/使用中/空闲/累计等待次数/等待时长）、长轮询四通道逐项、注册表按健康状态逐项、命令队列按状态逐项——表格化分区而非几个大数字。
- 侧栏 `NavLink` 加 `end` 逐项精确高亮（`/system` 前缀不误命中 `/system/version`）；命令面板导航目标随 navModel 更新为新 5 路由。

- 范围内：纯前端 IA 拍平 + 版本与更新页对接既有 FR-99 端点 + 控制面健康补展示。
- 不做（范围外）：后端契约改动（FR-61/82/98/99/100/101 端点不变）；后端「队列明细」新字段（前端只展示后端已返回字段）；虚拟滚动。

## 3. 设计（怎么做）

仅前端改动。FR-93 决策见 ADR-0043（保留）；系统区拍平决策见 ADR-0048，此处不重复正文。

- `web/src/lib/navModel.ts`：NAV 单一真源。system 组 leaves = `/settings`、`/system/version`、`/system`、`/api-keys`、`/namespaces` 五独立路由（删聚合深链 leaf）。
- `web/src/components/Layout.tsx`：5 组手风琴不变；`NavLink` 加 `end` 精确高亮；分组标题排版与子项层级协调（去 uppercase）。
- `web/src/components/CommandPalette.tsx`：导航目标随 navModel 自动更新；修暗色搜索框残留底框。
- `web/src/pages/SettingsPage.tsx`：回归运维设置单页（6 域一级 tab + settingsEditing 共享原语）。
- `web/src/pages/VersionUpdatePage.tsx`（新）：版本与更新独立页，复用 `useUpdateCheck` + FR-99 端点 + listSettings/updateSetting。
- `web/src/pages/SystemObservabilityPage.tsx`：恢复独立页 + 四组逐项详细明细。
- `web/src/components/SystemHeader.tsx`：版本徽章点击 `navigate('/system/version')`，删 UpdateModal。
- `web/src/App.tsx`：删聚合嵌套子路由与三条重定向，`/system`、`/api-keys`、`/namespaces` 恢复直接渲染独立页，新增 `/system/version`。
- 删除：`SystemInfoBlock`/`SystemConfigBlock`/`OpsSettingsBlock`/`PlaceholderTab`/`VersionInfoTab`/`UpdateModal` 及测试。
- `web/src/i18n/locales/zh-CN.ts`：新增 `nav.versionUpdate` + `versionUpdate.*` + 补充 `observability.*` 明细键；删 `settingsAggregate.*`。仅 zh-CN。

## 4. 验收标准
- FR-93：侧栏渲染 5 组；所属组按当前路由自动展开；偏好持久化与非法值回落；Layout 与 CommandPalette 导航目标同源一致。
- FR-94：`/settings` 单页 6 域一级 tab；跨域改两项时批量保存 dirtyItems=2；`/system/version` 渲染版本/渠道下拉/检查/更新/代理/更新设置；切渠道写 `update.channel` 并强制重查；release 日志纯文本（无 HTML 注入）。
- FR-95：`/system`、`/api-keys`、`/namespaces` 各独立页可达；控制面健康四组逐项明细渲染；侧栏在 `/system/version` 时仅「版本与更新」高亮（`/system` 不误高亮）。

## 5. 风险 / 待定
- 真机验证（5 路由切换 + 精确高亮、版本与更新页渠道切换/检查/真触发更新走重启重连、代理脱敏回显交互、控制面健康明细、命令面板可达）由主控 / 真机环节补。
- 后端未提供独立的「命令队列每条明细」（仅按状态计数），控制面健康页已把后端返回的全部计数字段逐项展示；如需逐条队列明细须后端补字段。
- 无 en 语言包，i18n 只补 zh-CN。
