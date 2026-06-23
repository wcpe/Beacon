# 功能规格：监控重构——看板瘦身 + 服务器详情页（FR-64 + FR-65）

> 状态：开发中　·　关联 PRD：FR-64（增强 FR-32/43）、FR-65（增强 FR-52 + FR-5/33/49）　·　纯前端 IA 重构，无新 ADR（InstanceView 经 FR-52 已含全部逐服深指标，不改后端/契约）。

## 1. 背景与目标
可观测看板当前两大区块（子服/BC）各含逐服明细表，纵向深滚、粗细混杂。重构 IA：**FR-64** 把看板瘦身为「集群粗览」（一屏、只粗况）；**FR-65** 把「实例与健康」+「代理服管理」合并为统一「服务器」页（全部 bukkit+bungee、深指标、健康、下线/drain/区操作 + 单服详情）。逐服深数据从看板移到服务器页。

## 2. 需求
### FR-64 看板瘦身
- 看板 = **集群粗览**，一屏不深滚：保留 KPI（总人数/在线服数/集群均值 TPS·内存·CPU 的 5 卡）+ BC 维度 5 卡 + 在线分角色摘要行（子服 N/M、BC N/M）+ 关键趋势（4 折线，可压缩/折叠）+（可选）健康分布。
- **删除两张逐实例明细表**（移交服务器页）。环境筛选 + 清空（FR-63）保留。

### FR-65 服务器页
- 新「服务器」页 `/servers`（nav「服务器」替代「实例与健康」+「代理服管理」）。
- 统一列**全部 serverId（bukkit+bungee）**：role(Badge)/group/zone(未分配黄高亮)/status(健康 Badge)/address/version + 角色相关列（bukkit: 人数·TPS；bungee: 连接·运行时长·后端可达），最近心跳。筛选 namespace/group/zone/role/status（复用 instances 筛选）。
- **单服详情**（Sheet/Dialog）：bukkit 深指标（playerCount/tps/capacity/weight/appliedMd5/registeredAt/metadata）；bungee 深指标（onlineConnections/threadCount/uptimeMs/后端可达 backendUp·Total·延迟 + backends 后端清单 + 小区默认入口）。
- **操作**：① 下线/取消下线（复用 FR-49，按行）；② drain/undrain（前端新接 `PUT/DELETE /admin/v1/scheduling/drains`，列 drain 态）；③ 区改派（**复用 FR-71 的 ReassignDialog**，含排空门 + 手输确认，按行/详情触发）。
- 删 `InstancesPage`/`ProxiesPage`（+ 测试）；`/instances`、`/proxies` 路由重定向 `/servers`。

### 不做
- 后端/契约不改（深指标 FR-52 已齐）。健康分布若需后端聚合端点则本期看板用现有 status 计数前端聚合、不加端点。topology/zones 页不动。

## 3. 设计
### 3.1 FR-64（改 DashboardPage.tsx）
删 bukkit/bc 逐实例明细表；保留 SummaryCards（5 卡）+ BCPanel（5 卡）+ TrendChart（压缩高度或 2x2 折叠）；加「在线分角色摘要」行（由 metricsSummary.servers 按 role 计数 + onlineServers）；加健康分布（由 listInstances 按 status 前端计数：online/lost/offline）。底部「服务器详情 → /servers · 拓扑 → /topology」链接。一屏不深滚。

### 3.2 FR-65（新 ServersPage.tsx + servers/ServerDetailSheet.tsx）
- 主表：`listInstances(filter)`（不限 role）→ DataTable，角色相关列按 role 显「-」或值；status 用 StatusBadge、role 用 RoleBadge、zone=null 黄高亮。已主动下线区（listOfflineInstances + 取消下线）。drain 态列（listDrains）。
- 行操作：下线（AlertDialog 确认，offlineInstance）/ drain（drainInstance）/ undrain / 改派（ReassignDialog，复用 web/src/pages/zones/ReassignDialog）。
- 详情 Sheet：按 role 分区展示 bukkit/bungee 深指标 + 关系（backends、zoneDefaultEntry）。复用 ProxiesPage 的 proxy 指标渲染范式。
- api/client：新增 `drainInstance(serverId,ns,reason?)`(PUT /scheduling/drains)、`undrainInstance(serverId,ns)`(DELETE)、`listDrains(ns)`(GET，若无)；复用 listInstances/offline/online/listDefaultEntries/assignZone/unassignZone。先读后端 `internal/handler` 确认 drains 端点请求/响应形状。
- nav（Layout.tsx）：删 instances/proxies，加 `{to:'/servers',labelKey:'nav.servers'}`；App.tsx：删两路由、加 /servers、/instances+/proxies → `<Navigate to="/servers" replace>`；i18n：删 instances/proxies 命名空间多余项、加 `nav.servers='服务器'` + `servers` 命名空间。

## 4. 任务拆分
- [ ] FR-64：DashboardPage 删明细表 + 加分角色摘要 + 健康分布 + 一屏布局；i18n。
- [ ] FR-65：ServersPage（表+筛选+下线/drain/改派+已下线区）+ ServerDetailSheet（bukkit/bungee 深指标）；api drain 函数（核对后端形状）；删 Instances/Proxies 页 + 测试；nav/路由/重定向/i18n。
- [ ] 测试：ServersPage（列全部实例、角色列、下线调用、drain 调用、改派触发 ReassignDialog、未分配高亮）、DashboardPage 瘦身（无明细表、分角色摘要、健康分布）。
- [ ] doc-sync：PRD FR-64/65、CHANGELOG、本规格。

## 5. 验收标准
- 看板一屏不深滚、只粗览（KPI+趋势+分角色摘要+健康分布），无逐服明细表。
- 服务器页列全部 bukkit+bungee、角色相关深指标、健康、下线/drain/改派可操作、单服详情含双类深指标+关系；旧 /instances /proxies 重定向 /servers。
- 前端测试全绿（pnpm test + build）。
- **真机浏览器**（末批）：看板粗览一屏；服务器页列 lobby-1（bukkit 深指标）+（若 proxy-1 在线）BC 深指标；下线/drain/改派可用。

## 6. 风险/待定
- drain 前端首次接入：核对后端 scheduling/drains 请求/响应；列 drain 态。
- 删 Instances/Proxies 页须把其被引用处（若有跳转）改指 /servers。
- 表格列多：角色相关列按 role 显隐或「-」，避免横滚过宽。
