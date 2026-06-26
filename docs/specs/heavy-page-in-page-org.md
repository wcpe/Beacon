# 功能规格：重内容页页内组织

> 状态：开发中　·　关联 PRD：FR-108　·　分支：feature/fr-108-in-page-org

## 1. 背景与目标
FR-105 落地两层页眉框架后，部分页面单页堆叠的内容仍然过多（命令观测把实时队列 / 历史查询 / 趋势分析混在一页；控制面健康 / 运维设置 / 版本与更新区段密集）。本 FR 治「内容太多」——**不加全局三级路由、侧栏保持扁平**，靠**页内 tab / 锚点**让各页自己组织内容。属第二期（P2）管理台体验打磨，纯前端。

## 2. 需求（要什么）

5 页各自范式（mock 均已与用户确认）：

1. **命令观测（CommandObservabilityPage）= 页内 segmented tab**：把混在一页的「实时队列 / 历史查询 / 趋势分析」拆成 `实时 / 历史 / 分析` 三视图，**默认实时**。切换**不跳路由**、侧栏不变；当前视图写 URL query（`?view=live|history|analytics`）以深链 / 刷新保持。各视图＝现有对应区块。
2. **服务分析（ServiceAnalysisPage）= 不做 tab**：仅**卡片降级**（区段 Card→标题 + 细线 / 留白）+ 时间窗 / 环境控件理顺，KPI + 排行 + 趋势保留。
3. **控制面健康（SystemObservabilityPage）**：保留顶部仪表环总览行 + 新增**左侧 sticky 分区锚点 rail + scroll-spy**（分区：进程运行时 / 数据库 / 长轮询 / 注册表 / 命令队列，rail 项带状态色点）+ 明细 Card 降级为「分区标题 + 细线」密网格。指标 / 阈值色 / 数据不回归。
4. **运维设置（SettingsPage）**：顶部 6 域横 tab **改左侧 sticky 分区锚点 rail + scroll-spy** + 设置行去 Card 外壳；**不回归 FR-62/77**（逐项编辑 / 恢复默认 / 跨域批量保存 SettingsSaveBar / dirty 摘要）。
5. **版本与更新（VersionUpdatePage）**：3 个 Card 区段（版本信息 + 渠道 + 检查 / 更新 / 网络代理 / 更新设置）改**左侧锚点 rail + 去卡片分区**；主操作「立即检查 / 立即更新」上提第二层页眉（usePageHeader actions）；**不回归 FR-94/100/101**。

- 范围内：上述 5 页的页内组织重构；一个公共 `AnchorRailLayout` 组件供健康 / 设置 / 版本与更新复用。
- 不做（范围外）：全局三级路由、侧栏层级化、后端 / agent 改动、新依赖、新 ADR。

## 3. 设计（怎么做）

### 3.1 公共组件 `AnchorRailLayout`（web/src/components/）
纯展示组件：左 sticky rail（分区锚点列表，点击平滑滚动定位）+ 右滚动内容；内置 scroll-spy（监听内容滚动，高亮当前分区）。
- 入参：`sections: { id, label, dot? }[]`（dot 为可选状态色点节点）、`children`（按 id 渲染各分区，分区根节点带 `id` 与滚动锚点）。
- 实现：`IntersectionObserver` 观察各分区根，命中视口顶部区域者高亮；点击 rail 项 `scrollIntoView({ behavior: 'smooth' })`。
- 无障碍：rail 用 `<nav>` + `aria-current` 标记当前项。

### 3.2 命令观测页内 tab
- 新增 `view` 状态来源于 `useSearchParams`（`?view=`，非法回落 `live`）。
- 视图 segmented tab 用既有 `Tabs/TabsList/TabsTrigger`（不跳路由，仅写 query）。
- 三视图分别渲染现有「实时队列」「历史查询」「分析（KPI + 趋势 + 分布）」区块；环境 / 时间窗筛选条按视图相关性归位（环境对三视图全局，时间窗仅分析视图用）。
- 所有既有查询（analytics / queue 5s 轮询 / history 分页）行为不变。

### 3.3 卡片降级与锚点接入
- 服务分析：区段外层 `Card/CardContent` 换为 `SectionHeader`（标题 + 细线）+ 留白；KPI / 排行 / 趋势数据与聚合逻辑不动。
- 控制面健康 / 运维设置 / 版本与更新：用 `AnchorRailLayout` 承载分区；每个分区头用「标题 + 细线」，明细行 / 设置行去 Card 外壳。
- 健康页保留仪表环总览行于 rail 之上（rail + 内容在其下）。

### 3.4 i18n
新增公共锚点 / 视图 tab 文案键（中文），无裸键。

## 4. 任务拆分
- [x] 规格 + PRD 状态（计划→开发中，仅 FR-108 一行）
- [x] 公共 `AnchorRailLayout` + vitest
- [x] 命令观测页内 segmented tab（URL ?view=）+ 改测试
- [x] 服务分析卡片降级
- [x] 控制面健康 AnchorRail 接入 + 卡片降级 + 改测试
- [x] 运维设置横 tab→AnchorRail + 行去卡 + 改测试
- [x] 版本与更新 AnchorRail + 主操作上提页眉 + 改测试
- [x] 文档同步：CHANGELOG 未发布段追加 FR-108

## 5. 验收标准
- 页内 tab 切换不跳路由、侧栏扁平不变；命令观测 `?view=` 深链 / 刷新保持，默认实时。
- rail scroll-spy 正确高亮当前分区，点击平滑滚动定位。
- 行为绝不回归：命令观测过滤 / 趋势 / 实时刷新；服务分析聚合；健康指标 / 阈值色；设置 FR-62/77（逐项编辑 / 恢复默认 / 跨域批量保存 / dirty 摘要）；版本更新 FR-94/100/101（渠道切换 / release 安全渲染 / 二次确认 + 进度 + 重连 / 代理脱敏 / 更新设置）。
- i18n 无缺键、暗色正常（CSS 变量）。
- `cd web && pnpm test` 全绿 + `pnpm build` 通过。

## 6. 风险 / 待定
- scroll-spy 在 jsdom 下无真实布局，IntersectionObserver 需在测试中桩或仅断言锚点存在 / 点击不报错；高亮态以真机为准。
- 设置页由 tab 改 rail 后，现有以 `getByRole('tab')` 定位的测试需改为按分区标题定位（行为断言不变）。
