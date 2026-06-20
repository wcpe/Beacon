# 功能规格：集群拓扑可视化

> 状态：开发中　·　关联 PRD：FR-37　·　分支：feature/fr-37-topology

## 1. 背景与目标

管理台目前只有「实例与健康」的扁平列表，运维看不到 bc（bungee 代理）与其后端 bukkit 子服之间的**真实连接关系**，也看不到按大区 / zone 的整体拓扑。FR-36 已让控制面持有每个 bc 的后端 serverId 集合（`runtime.Instance.Backends`，`instanceView` 已输出 `backends`），本 FR 据此画出真实的 bc→bukkit 拓扑图。属 P2，增强 FR-4（服务发现）/ FR-29（拓扑 watch）。

## 2. 需求（要什么）

- 控制面新增 `GET /admin/v1/topology?namespace=`（admin 鉴权），返回拓扑数据：
  - **节点**：各在线实例（serverId / role / group / zone / status / address）。
  - **边**：bc → 其 `backends` 中**当前在线**的 bukkit 子服。
  - **分组信息**：按大区 / zone 聚合（供前端分簇）。
- 前端新增独立 `/topology` 页（App.tsx 加路由 + Layout 侧边栏导航项），用 **ECharts** graph 画：bc 与 bukkit 用不同图标 / 颜色区分、真实 bc→bukkit 连线、按大区 / zone 聚合、节点带在线状态色；轮询刷新（React Query `refetchInterval`，与实例页一致）。
- 实例与健康页：仅加角色徽标区分 BC / 子服（小改，不在该页放拓扑图）。
- 范围内：拓扑端点 + 拓扑页 + 实例页角色徽标。
- 不做（范围外）：不落 DB、不引重型件；不在控制面做任何调度 / 连接决策（只展示事实）；不复用 SSE watch 推送（前端轮询即可，与实例页一致）；不做拓扑编辑 / 拖拽改派（那是 zone 看板的事）。

## 3. 设计（怎么做）

**控制面（Go，分层 router→handler→service）**

- 新增 `service.TopologyService`：读内存注册表快照（`registry.List`，可用集合 = `online`+`degraded`，与发现 / 拓扑摘要同口径），组装 `Topology` 领域结果——节点列表、边列表（bc→在线 bukkit）、分组列表。纯组装、无 DB IO、无副作用，便于穷举单测。bc→bukkit 边只连**当前在册可用**的 bukkit（`backends` 中已离线的 serverId 不画边，避免悬挂边）。
- 新增 `handler.TopologyHandler`：`GET /admin/v1/topology?namespace=`，调 service 得领域结果，编解码为 JSON 视图。handler 不碰 `runtime` 内存结构细节（经 service 暴露领域结构）。
- `router.go` admin 分组挂 `r.Get("/topology", h.Topology.Topology)`；`main.go` 装配。
- 只读注册表快照，**不落 DB、不引重型件**，写路径零侵入（守「控制面只存事实」边界、不据拓扑做调度）。

**前端（React + ECharts）**

- `pnpm add echarts`（锁定版本）。
- `api/types.ts` + `api/client.ts`：新增 `TopologyView` 类型与 `getTopology(namespace?)`；`InstanceView` 补 `backends`。
- 新增 `pages/TopologyPage.tsx`：React Query `refetchInterval` 轮询；把 ECharts 渲染抽到 `pages/topology/TopologyGraph.tsx` 子组件（页面可在测试中以轻量桩替身断言喂图数据，规避 ECharts 在 jsdom 下的 canvas 依赖，与 DashboardPage 测试同套路）。
- App.tsx 加 `/topology` 路由；Layout.tsx 加导航项。
- InstancesPage：列表与详情加角色徽标（bc / 子服区分）。

不涉及新架构决策（消费 FR-36 的 ADR-0024 事实、复用既有发现 / 拓扑口径），不新增 ADR。

## 4. 任务拆分

- [x] 后端：`TopologyService`（组装节点/边/分组，纯函数式）+ 单测
- [x] 后端：`TopologyHandler` + `GET /admin/v1/topology` 路由 + main.go 装配 + handler 单测
- [x] 前端：`pnpm add echarts`（锁版本）
- [x] 前端：types/client（`TopologyView` + `getTopology` + `InstanceView.backends`）
- [x] 前端：`TopologyPage` + `TopologyGraph` + 路由 + 导航项 + 组件测试
- [x] 前端：InstancesPage 角色徽标
- [x] 文档同步：PRD 状态、ARCHITECTURE、API、CHANGELOG

## 5. 验收标准

- `GET /admin/v1/topology?namespace=` 返回节点 + bc→在线 bukkit 边 + 大区/zone 分组；离线 bukkit 不连边；handler 单测绿。
- `/topology` 页渲染真实 bc→bukkit 连线、角色区分、按大区/zone 组织、随轮询刷新；组件测试绿。
- 实例与健康页有角色徽标。
- `go test ./...` 绿；`cd web && pnpm install && pnpm test && pnpm build` 绿。

## 6. 风险 / 待定

- ECharts 在 jsdom 下依赖 canvas，组件测试以桩替身隔离图渲染（只断言喂图数据），与既有 DashboardPage 测试一致，不引 canvas polyfill。
