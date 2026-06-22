# 功能规格：归派看板汇总树形化

> 状态：开发中　·　关联 PRD：FR-55　·　分支：feature/fr-55-kanban-tree

## 1. 背景与目标
zone 归派管理台（`web/src/pages/ZonesPage.tsx`，FR-35）当前在**页面底部**用一张扁平表格展示 zone 汇总（列：大区 / 小区 / 服数 / 在线数）。表格把大区与小区拍平成同级行，集群的归属层级（哪个大区下有哪些小区、各自挂多少子服）不直观。

本功能（P2，增强 FR-35）把该汇总区改为**页面上方的树形节点图**，层级为「大区 → 小区 → 子服」，提升集群归属可读性。纯前端、后端零改动、不引入新依赖。

## 2. 需求（要什么）
- 范围内：
  - 汇总区从底部**上移**到归派看板之上（页面上方）。
  - 展现形态从 `DataTable` 扁平表格改为**树形节点图**：大区为一级节点，小区为二级节点，子服为三级（叶）节点。
  - 大区 / 小区节点显示「服数 / 在线数」，且与原表格口径**完全一致**（服数 = DB 指派数、在线数 = 在线注册数，均取自 `zoneSummary` 的 `ZoneStatView`）。大区为其下小区的合计。
  - 子服叶节点列出该 (大区, 小区) 当前在册的子服实例（serverId + 在线状态点），数据复用既有看板模型派生（`buildKanbanModel`），不另发请求。
  - 加载 / 错误 / 空态沿用既有 `AsyncSection` 与 shadcn 风格。
- 不做（范围外）：
  - **不改动**拖拽指派 / 改派的看板交互本体（DndContext / DropBucket / ServerCard / dragAction 一律不碰）。
  - 不改后端、不改 REST 契约、不动 `zoneSummary` 数据源。
  - 不新增第三方依赖（树形用现有 ui 基元 + 递归渲染自实现）。
  - 不做展开 / 折叠状态持久化等镀金能力。

## 3. 设计（怎么做）
- 新增纯函数派生 `web/src/pages/zones/summaryTree.ts`：
  - `buildSummaryTree(summary: ZoneStatView[], model: KanbanModel): SummaryTree`。
  - 以 `summary`（ZoneStatView）为**计数权威**构树，保证服数 / 在线数与原表格一致；子服叶子从 `model.groups[].zones[].instances` 按 (group, zone) 取，仅作展示。
  - 输出按大区、小区、serverId 字典序稳定排序（与既有看板模型一致）。
- 新增展示组件 `web/src/pages/zones/ZoneSummaryTree.tsx`：递归渲染树，复用 `Badge` 显示计数、复用 ServerCard 同款在线状态点配色显示子服；无新依赖。
- 改 `ZonesPage.tsx`：把原「zone 汇总」`Card`（含 `DataTable` 与 `SUMMARY_COLUMNS`）替换为树形组件，并从页面底部上移到归派看板 `Card` 之上。汇总树消费已有的 `summary` 查询与 `model`（`buildKanbanModel` 结果），不新增查询。
- 不涉及架构决策，无需新 ADR。

## 4. 任务拆分
- [ ] `summaryTree.ts` 纯函数 + 单测（红 → 绿）：层级正确、计数取自 summary 与原表一致、子服叶子来自 model、稳定排序、空态。
- [ ] `ZoneSummaryTree.tsx` 展示组件。
- [ ] 改 `ZonesPage.tsx`：替换底部汇总表格为树并上移；移除 `SUMMARY_COLUMNS` / `DataTable` 汇总用法。
- [ ] 文档同步：PRD 状态（交付时改）、ARCHITECTURE（如涉及前端结构描述）、CHANGELOG 未发布段。

## 5. 验收标准
- 树层级正确：大区 → 小区 → 子服三级，结构与归属与数据一致。
- 服数 / 在线数与原扁平表格口径一致（同取自 `ZoneStatView`），大区为其小区合计。
- 子服叶子列出对应 (大区, 小区) 的在册子服，BC 代理不出现（沿用看板模型既有排除）。
- 汇总区位于页面上方（归派看板之上）。
- 不触碰拖拽交互本体；`web/` `pnpm test` 与 `pnpm build` 全绿。
- 浏览器目视维度（树形排布美观、响应式）标「待真机验」。

## 6. 风险 / 待定
- `summary` 的服数（DB 指派）与 `model` 的在册子服（注册表实例）口径不同：服数 / 在线数以 summary 为权威；子服叶子仅展示在册实例，可能与服数不完全相等（属预期，与原表口径分工一致），不做强一致校正。
