# 功能规格：卡片降级 + 全站页眉接入收尾

> 状态：开发中　·　关联 PRD：FR-107　·　分支：feature/fr-107-card-downgrade

## 1. 背景与目标

FR-105 已落两层页眉框架（`PageHeader` + `usePageHeader`），各页标题 / 主操作已上提第二层页眉。但内容区仍普遍把 shadcn `Card` 当**布局容器**——每个区段（筛选条、汇总、看板、图例、面板、图表）都裹一层 `Card`，造成卡边框 / padding 叠盒、密度低、层级噪声。

本 FR 在内容区落实「卡片降级」原则：`Card` 仅保留给**真正有边界的对象**（单条详情 / 模态 / 可点磁贴），区段间改用「区段标题（含图标）+ 细线 / 留白」轻分隔。属 P2，纯前端，依赖 FR-105。

## 2. 需求（要什么）

落三页：大区（ZonesPage）/ 集群拓扑（TopologyPage）/ 可观测看板（DashboardPage）。

- **卡片降级判据**
  - 保留 `Card`：单条详情记录、模态 / Sheet、可点击的离散对象磁贴（如状态墙 `StatusTile`、看板 server 磁贴）。
  - 降级 `Card`：纯粹「包一段内容加边框 padding」的区段容器 → 改 `区段标题（含图标）+ border-b 细线 + 内容`，或直接留白分隔。
- **大区（ZonesPage）**：筛选条 / 区汇总树 / 归派看板三段外层 `Card` → 区段标题 + 轻分隔；看板「未指派池 + 按大区分组容器」与 server 磁贴密度压实保留；汇总树（FR-55）保留。**FR-71 安全门绝不回归**：解锁改派开关、改派复述对话框、取消指派二次确认、排空门 409「先排空」提示一字不动。
- **集群拓扑（TopologyPage）**：控件 + 图例段、画布段两层 `Card` → 区段标题 + 轻分隔；`TopologyGraph`（ECharts，FR-37）数据 / 交互 / 轮询不动；第二层页眉已接（FR-105），环境下拉页内保留与全局 env 槽并存、本 FR 不强迁。
- **可观测看板（DashboardPage）**：清掉仍把 `Card` 当区段容器的地方（总览条 / 分角色面板 / 时序图段）→ 区段标题 + 轻分隔；状态墙瓷砖（可点磁贴）保留 `Card` 语义不算降级对象，但其外层无 `Card`；图表 / 状态墙数据与布局不回归。
- 范围内：上述三页区段容器 Card 降级 + 轻分隔；抽一个 `SectionHeader` 复用组件（图标 + 标题 + 可选右槽 + border-b）避免三页复制。
- 不做（范围外）：批 2 页面（配置中心 / 拓印 / 反抓 / 文件树预览另立批）；列表页 A+E（FR-106）；页内组织 / 锚点（FR-108）；任何后端 / agent / 契约改动；新 ADR；新依赖。

## 3. 设计（怎么做）

仅前端（`web/`），无后端 / agent / 契约改动，不写 ADR（卡片降级为前端视觉决策，本 spec 即记录）。

- `web/src/components/SectionHeader.tsx`（新）：区段标题轻分隔组件。`icon`（lucide 节点）+ `title` + 可选 `count` / `actions` 右槽 + 底部 `border-b`。复用 DashboardPage 状态墙现有 `<h2 className="flex items-center gap-2"><Icon/>title</h2>` 范式，统一为单一组件。
- `web/src/i18n/locales/zh-CN.ts`：`common` 增 `filter: '筛选'`（筛选区段标题用），无其他新键。
- `ZonesPage.tsx`：三处外层 `<Card><CardContent>` → `<section>` + `SectionHeader`；筛选条、汇总树、看板各自标题 + 内容；`ZoneBucketView` / `DropBucket` / `ServerCard` 磁贴密度保留。移除 `Card` / `CardContent` import（若不再用）。
- `TopologyPage.tsx`：两处 `<Card><CardContent>` → `<section>` + `SectionHeader`（控件 + 图例段标题用 `common.filter`；画布段标题用 `topology.title` 或省略，仅留分隔）。
- `DashboardPage.tsx`：总览条 / 分角色子服面板 / 分角色 BC 面板 / 时序图段的外层 `Card` → `<section>` + `SectionHeader`（沿用各自既有 `<h2>` 图标标题）；状态墙已是 `<section>` 无需改；空态占位 `Card` 视情保留或改轻提示。

## 4. 任务拆分
- [ ] 测试先行：为三页加 / 调断言——区段标题文案出现、行为不回归（安全门 / 图渲染 / 图表数据）；现有测试不挂
- [ ] `components/SectionHeader.tsx`
- [ ] `i18n` 补 `common.filter`
- [ ] ZonesPage 三段降级
- [ ] TopologyPage 两段降级
- [ ] DashboardPage 区段容器降级
- [ ] 文档同步：PRD 状态（开发中）、CHANGELOG 未发布段末尾追加 FR-107

## 5. 验收标准
- 三页区段容器 `Card` 已降级为「区段标题 + 细线 / 留白」轻分隔；可点磁贴 / 模态 / 单条详情仍用 `Card`。
- 行为绝不回归：大区解锁改派安全门 + 排空门 409 提示、拓扑图渲染 / 实时刷新、看板各图表与状态墙数据。
- i18n 无缺键、暗色正常（CSS 变量，不硬编码色）。
- `cd web && pnpm test` 全绿 + `pnpm build` 通过；真机抽验（主控整合后串行验，本 worktree 标「待主控真机验」）。

## 6. 风险 / 待定
- 降级是机械但需逐段核对：移除 `Card` 后须保留原 `space-y` / 内边距节奏，避免视觉塌陷。
- 现有单测多为行为断言（非 Card-DOM 耦合），降级理论上不挂；个别断言若依赖 Card 结构需同步适配。
