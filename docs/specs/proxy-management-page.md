# 功能规格：代理服管理页（FR-52）

> 状态：开发中　·　关联 PRD：FR-52　·　分支：feature/fr-52-proxy-page

## 1. 背景与目标
解决运维"看不清 BC 代理整体运行态"的问题：现有「可观测看板」只有 BC 维度的**聚合**口径（FR-34 的 `metrics/summary.bc`，把所有 BC 折叠成一行），而拓扑页只画连线、实例与健康页不展示 BC 专属底层参数。运维想知道"每台 BC 各自跑得怎么样"时无处可查。

本页（P2，增强 FR-6）为**每一台 BC 代理**集中、透明地呈现状态 + 底层参数，让运维一眼看全 BC 运行态。只读呈现既有事实，不做任何调度 / 写操作。

## 2. 需求（要什么）
新增独立管理页 `/proxies`，按环境（namespace）列出所有 BC（`role=bungee`）实例，逐台展示：
- 状态：实例 status（online/degraded/lost/offline）、所属大区 / 小区、地址、版本、运行时长。
- 底层参数（FR-34）：在线连接数、JVM 线程数、运行时长、后端可达性（up/total）、到后端的平均 ping 延迟。
- 后端子服清单（FR-36）：该 BC 当前代理的后端 serverId 集合。
- 默认入口（FR-48）：该 BC 所属小区（home-zone）的默认入口 serverId（来自 `zones/default-entry`）。

- 范围内：纯只读呈现；按环境过滤；轮询刷新；逐台 BC 卡片 / 行展示上述维度。
- 不做（范围外）：任何写操作（下线 / 改派 / 设默认入口都在既有页面）、图表、嵌套多层代理（FR-56，P3）、网络吞吐字节（ADR-0025 本期不采）、玩家名单 / 身份（看人归③层，越界）。

## 3. 设计（怎么做）

### 3.1 数据来源（消费既有端点）
- BC 实例与状态 / zone / 后端清单：`GET /admin/v1/instances?namespace=&role=bungee`（`InstanceView`，含 `backends`、`zone`）。
- 每台 BC 的底层参数（连接 / 线程 / 运行时长 / 后端可达·延迟）：来自 `runtime.Instance.Proxy`（FR-34 已采集、控制面只存的事实）。
- 默认入口：`GET /admin/v1/zones/default-entry?namespace=`（`defaultEntryView`，按 zone 索引）。

### 3.2 后端：把已有 BC 事实补到实例视图（最小补丁，非新采集）
FR-34 已把每台 BC 的 `Proxy` 指标采进 `runtime.Instance.Proxy`，但此前**只**在 `metrics/summary.bc` 做了**聚合**暴露，逐实例视图（`instanceView`）未带该事实，导致无法按单台 BC 呈现底层参数。

本页要求逐台展示，故在既有 `instanceView` 上**追加一个可选 `proxy` 对象**（仅 bc 非零、bukkit 恒零）。这是把控制面**已持有的内存事实**补暴露在既有端点上：
- **不新增端点、不新增采集、不新增 DB 列**，控制面仍只"存事实"，未触碰控制/数据面边界。
- 加法且向后兼容：旧消费方忽略未知字段；bukkit 实例该对象各字段恒为 0。
- 不需新 ADR（沿用 FR-34 / ADR-0025 既有决策，仅扩大其暴露面到逐实例视图）。

`proxy` 子对象字段对齐 agent 上报与 `ProxyMetrics`：`onlineConnections` / `threadCount` / `uptimeMs` / `backendUp` / `backendTotal` / `backendAvgLatencyMs`（`-1` 表示无可达后端不可用）。

### 3.3 前端
- 新增 `web/src/pages/ProxiesPage.tsx`：环境过滤 + 轮询；按 `role=bungee` 拉实例 + 拉默认入口；逐台 BC 卡片展示状态 + 底层参数 + 后端清单 + 所属 zone 默认入口。
- `App.tsx` 注册路由 `/proxies`、`Layout.tsx` 导航追加「代理服管理」（均追加到列表末尾，减少与并行分支冲突）。
- `api/types.ts` 给 `InstanceView` 追加 `proxy: ProxyMetricsView`；`api/client.ts` 复用 `listInstances` / `listDefaultEntries`（如缺则补 default-entry 只读客户端）。

## 4. 任务拆分
- [x] 后端：`instanceView` 追加 `proxy` 对象 + `toInstanceView` 映射 + 单测
- [x] 前端类型 / 客户端：`InstanceView.proxy` 类型；default-entry 只读列表客户端
- [x] 前端页面：`ProxiesPage.tsx`（测试先行）+ 路由 + 导航
- [x] 文档同步：PRD 状态（保持开发中）、API.md（instances 视图补 `proxy`）、CHANGELOG 未发布段追加一行

## 5. 验收标准
- 选定环境后，页面按 `role=bungee` 列出全部 BC，逐台展示状态 / 所属 zone / 连接数 / 线程 / 运行时长 / 后端可达性·延迟 / 后端子服清单 / 默认入口。
- 无 BC 时给出空态提示；未选环境不发请求。
- `backendAvgLatencyMs < 0` 显示「不可用」而非负数；无后端显示「无后端」。
- 后端 `instanceView` 单测覆盖 bc 带 `proxy` 真值、bukkit `proxy` 恒零。
- 前端 `pnpm test` + `pnpm build` 全绿；浏览器目视维度标「待真机验」。

## 6. 风险 / 待定
- 任务书原述"纯前端、不需后端改动"，但经核实逐台 BC 底层参数（FR-34）此前未在逐实例视图暴露（只有聚合口径）。本规格据此做了**最小后端补丁**（既有内存事实补暴露，非新采集 / 新端点 / 新 ADR），已在 §3.2 说明理由。若主控要求严格零后端改动，则本页只能呈现状态 / zone / 后端清单 / 默认入口，**无法呈现连接 / 线程 / 运行时长 / 后端延迟**——与 FR-52 明列的"底层参数"不符，需回 brainstorming 重定范围。
