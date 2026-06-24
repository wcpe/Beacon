# 功能规格：全局搜索 + 命令面板（Cmd-K）

> 状态：开发中　·　关联 PRD：FR-83　·　分支：feature/fr-83-command-palette

## 1. 背景与目标

管理台页面已增至十余个（配置中心 / 服务器 / 审计 / 拓扑 / 设置 …），运维要跳到某页、找某条配置、查某台服，得先用鼠标在侧栏点、再在页内逐层筛。FR-83 给管理台加一个**全局命令面板**：`Ctrl/Cmd+K` 一键唤起，输入即时聚合搜索，纯键盘可达，回车直接跳转 / 执行常用导航。属第二期（P2）运维体验增强。

## 2. 需求（要什么）

- **唤起**：全局 `Ctrl/Cmd+K` 打开面板；`Esc` 关闭。页眉提供一个可点的搜索入口（提示 `Ctrl K`）。
- **即时搜索**：输入即过滤，结果**分组**展示：
  1. **导航**——所有页面（跳转到对应路由）。
  2. **配置 / 文件**——按 `dataId` / `path` 关键字命中，跳配置中心（`/configs`）。
  3. **服务器**——按 `serverId` 命中，跳服务器页（`/servers`）。
  4. **审计动作**——常用审计动作快捷项，跳审计页（`/audits`）。
- **键盘可达**：上下方向键在结果间移动、回车执行选中项 / 跳转、`Esc` 关闭。
- 范围内：纯前端；搜索数据源**复用既有列表端点**（配置 `listConfigs`、文件 `listFiles`、实例 `listInstances`），面板打开时拉一次、客户端过滤；导航 / 审计动作为静态项。
- 不做（范围外）：后端搜索端点、全文检索、模糊高亮、最近访问 / 历史记忆、跨环境聚合的服务端实现（沿用既有「不传 namespace = 全部」）、命令执行类动作（仅做导航跳转，不在面板里直接发布 / 删除）。

## 3. 设计（怎么做）

纯前端，不加后端、不加依赖（不引入 `cmdk`；用既有 `radix-ui` Dialog 基元 + 自实现键盘导航）。

- **纯函数层** `web/src/lib/commandPalette.ts`：
  - `buildItems(input)`：把「导航项 + 配置 / 文件 + 实例 + 审计动作」原始数据归一成 `CommandItem[]`（`{ id, group, title, subtitle?, to }`）。
  - `filterItems(items, query)`：按 `query` 子串（大小写无关，匹配 `title` + `subtitle`）过滤；空 `query` 时只返回导航 + 审计动作（不展示全量配置 / 服务器，避免噪声）。
  - `groupItems(items)`：按 `group` 归类并保持组内既有顺序，供分组渲染。
  - 跳转目标用查询参数深链（如配置 `/configs?dataId=xxx`、服务器 `/servers?serverId=xxx`、审计 `/audits?action=xxx`），页面已支持或可忽略未知参数（纯导航、不破坏既有页）。
- **组件** `web/src/components/CommandPalette.tsx`：基于 `Dialog`，内含搜索 `input` + 分组结果列表；持选中下标 `activeIndex`，方向键移动、回车 `navigate(to)` 并关闭；打开时用 react-query 拉 `listConfigs({})` / `listFiles({})` / `listInstances({})`（无 namespace = 全部），失败降级为只剩导航 + 审计动作（不阻断面板）。
- **全局唤起**：在 `Layout` 挂 `Ctrl/Cmd+K` `keydown` 监听（与 FR-75 `ConfigsPage` 的 `Ctrl+S` 不同键、不冲突；编辑器内输入框聚焦时仍允许唤起，面板为模态覆盖），并在 `Layout` 侧栏品牌下加一个「搜索…」入口按钮（点开同一面板、带 `Ctrl K` 快捷键提示）。开合状态由 Layout 持有、下传组件。（入口落侧栏而非 `SystemHeader`：开合态由 Layout 持有，侧栏按钮直接读写该状态、无需把回调穿透进只读状态条组件，耦合更小。）
- **i18n**：新增 `commandPalette.*` 文案（占位符、分组标题、空态、快捷键提示）；导航项标题复用既有 `nav.*`。

## 4. 任务拆分

- [x] 写规格（本文）
- [x] PRD §4 FR-83 行「计划」→「开发中」
- [x] 纯函数 `lib/commandPalette.ts`（构建 / 过滤 / 分组）+ 单测（红→绿）
- [x] `CommandPalette` 组件 + RTL 用例（唤起 / 输入过滤 / 分组 / 键盘上下回车 / Esc）
- [x] Layout 全局 `Ctrl/Cmd+K` 监听 + 侧栏搜索入口
- [x] i18n 文案
- [x] 文档同步：PRD 状态、CHANGELOG 未发布段

## 5. 验收标准

- `Ctrl/Cmd+K` 唤起面板、`Esc` 关闭；页眉搜索入口可点开。
- 输入关键字即时过滤，导航 / 配置·文件 / 服务器 / 审计动作分组展示；命中项回车跳到对应页（带深链参数）。
- 方向键上下选择、回车执行选中、`Esc` 关闭——纯键盘可达。
- 数据源失败时面板仍可用（至少导航 + 审计动作）。
- `cd web && pnpm test` 全绿（含新增纯函数与组件用例）、`pnpm build` 通过。

## 6. 风险 / 待定

- 深链参数（`/configs?dataId=`、`/servers?serverId=`、`/audits?action=`）目标页当前可能不消费这些参数 → 仍能正确导航到页面，参数为「尽力定位」、不消费不报错；后续可由各页按需读取增强。
- 不传 namespace 拉全量列表在大规模环境下数据量偏大 → 面板仅打开时拉一次、客户端过滤，MVP 规模可接受；如成瓶颈再加服务端搜索（届时另议）。
- 真机浏览器交互（实际键盘唤起 / 跳转）需真机验证。
