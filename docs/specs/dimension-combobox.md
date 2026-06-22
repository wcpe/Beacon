# 功能规格：维度输入统一可编辑下拉（combobox）

> 状态：开发中　·　关联 PRD：FR-51　·　分支：feature/fr-51-combobox

## 1. 背景与目标

管理台里「环境(namespace) / 大区(group) / 小区(zone) / serverId」这四类维度输入散落在多个筛选框与表单中，
当前实现不一致：筛选框是裸 `Input`（纯手输、无候选提示），表单是原生 `<select>`（只能选、不能输新值）。
运维既要能从既有候选里快速选，又要在筛选 / 新建场景填列表外的新值。集群拓扑页还要求先手动输环境才出图，体验割裂。

本功能（FR-51，纯前端、增强 FR-6/FR-37/FR-40）把这四类维度输入统一为一个「下拉 + 可编辑」combobox：
候选项从既有 API 派生，**按场景区分**可编辑性，并让拓扑页默认选第一个环境直接出图。属于 P2 管理台增强。

## 2. 需求（要什么）

- 新增可复用组件 `Combobox`（`web/src/components/ui/combobox.tsx`）：点击展开候选下拉，可键入过滤；支持两种模式：
  - **editable（可编辑）**：允许提交候选列表外的新值（筛选框、可新建维度的表单）。
  - **strict（严格选）**：只能选列表内的值，键入仅用于过滤、不接受列表外值（纯选择处，如改派 / 抓取目标必须是已存在项）。
- 覆盖范围（替换现有维度输入）：
  - 实例与健康页筛选：环境 / 大区 / 小区 → editable。
  - 审计页筛选：环境 → editable。
  - zone 指派表单（ZonesPage）：环境 / serverId / 大区 / 小区 → strict（沿用既有「取值须落候选」校验）。
  - 新建配置对话框（CreateConfigDialog）：环境 → strict（须为已存在 namespace）；大区 / 小区 → editable（可为尚未注册的新维度授权配置）；覆盖目标（server 层）→ strict。
  - 导入到组（ImportFilesDialog）：环境 → strict；目标组 → editable（可导入到新组）。
  - 反向抓取（ReverseFetchDialog）：抓取源 serverId / 目标实例 → strict（须为在线 / 已存在实例）；目标组 → editable。
  - 集群拓扑页（TopologyPage）：环境从裸 Input 改为 strict 下拉（候选来自 `listNamespaces`），并**默认选中第一个环境**直接出图。
- 范围内：纯前端组件与上述页面 / 表单接线；候选来源复用既有 list 端点（namespaces / instances / zoneSummary）。
- 不做（范围外）：后端改动、新 ADR、新增依赖、模糊搜索库、role/status/format/scopeLevel 等**枚举类**下拉（非维度、值域固定，保持现状 `ui/select`）。

## 3. 设计（怎么做）

- `Combobox` 基于既有 `radix-ui` umbrella 包的 `Popover` + 受控 `Input` 自实现（项目无 shadcn `command`/`popover` 基元，避免新增依赖）：
  - props：`value`、`onChange(value)`、`options: string[]`、`allowCustom: boolean`、可选 `placeholder` / `id` / `className` / `disabled`。
  - editable：触发器为 `Input`，键入即 `onChange`（提交列表外值放行）；下拉列出过滤后候选，点击即选。
  - strict：触发器为只读展示 + 下拉候选，键入仅过滤；列表外值不可提交（不触发 `onChange`）。
  - 候选过滤为大小写无关子串匹配；无候选项时下拉给中文空态提示。
  - 可达性：触发器 / 输入用既有 `Input` 样式；选项 `role="option"`，便于沿用既有测试以 `getByRole('option')` 断言。
- 拓扑页：用 React Query 拉 `listNamespaces` 得候选，`useEffect` 在候选就绪且当前未选时把 `namespace` 置为首项，触发既有出图查询。
- 候选来源沿用各页既有派生逻辑（如 ZonesPage 的 group/zone 并集、bukkit-only serverId），不改数据流。

## 4. 任务拆分

- [ ] 新增 `Combobox` 组件 + 单测（editable vs strict、键入过滤、空态、列表外值放行 / 拦截）
- [ ] 接线实例页 / 审计页筛选（editable）
- [ ] 接线各表单（ZonesPage / CreateConfigDialog / ImportFilesDialog / ReverseFetchDialog，按字段 strict / editable）
- [ ] 拓扑页改 strict 下拉 + 默认选第一个环境出图 + 更新单测
- [ ] 文档同步：PRD 状态（交付期由发版技能改）、CHANGELOG 未发布段追加一行

## 5. 验收标准

- `Combobox` 单测覆盖：editable 模式可提交列表外新值；strict 模式拒绝列表外值、仅能选候选；键入过滤候选；空候选给提示。
- 拓扑页单测：候选就绪后自动选首个环境并发起拓扑查询、出图，无需手动选。
- 各既有页面 / 表单单测仍绿（ZonesPage 指派校验、TopologyPage 出图等），按需改测以适配新组件交互。
- `pnpm test` 与 `pnpm build` 全绿。

## 6. 风险 / 待定

- 既有页面单测以原生 `<select>` 的 `selectOptions` 驱动；换 combobox 后需改为 `click` 选项交互，注意 jsdom 下 Popover 的渲染时序。
- editable 与 strict 的逐字段归类已在 §2 固化；如运维实际需要某筛选严格化，再按需调整（不预留开关）。
