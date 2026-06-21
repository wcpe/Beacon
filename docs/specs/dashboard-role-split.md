# 功能规格：可观测看板 BC/子服双区块拆分

> 状态：开发中　·　关联 PRD：FR-43　·　分支：feature/fr-43-dashboard-split

## 1. 背景与目标

FR-32（[ADR-0023](../adr/0023-control-plane-observability-dashboard.md)）补齐了子服负载画像，FR-34（[ADR-0025](../adr/0025-bc-proxy-metrics-and-netty-traffic.md)）给 bc 加了专属指标并把**平均 TPS·CPU** 限定为只统计 bukkit。但角色分离做了一半：
- **平均内存**（`avgMemUsed` / `avgMemMax`）仍按「÷全部实例」算，bc 的堆字节被混进子服内存均值，口径与平均 TPS·CPU 不一致。
- 趋势 `Downsample` 桶内内存均值同样「÷全部样本」，混入 bc。
- 每服明细只有 `serverId` + `playerCount`，**不带角色**，前端无法按角色分组。
- Dashboard 整体未按角色成两大区块，子服与 bc 指标在同一组卡片 / 同一张明细表里混列。

本功能（FR-43，属 P2 增强）完成 FR-32/FR-34 既定但未尽的角色分离：把平均内存口径统一为「仅 bukkit」，每服明细带 role，前端整体拆成「子服(bukkit)」与「BC 代理」两大区块。**不引入新指标、不改采集、不动 DB schema、不新增端点**，仅修正聚合口径与重组展示。仍严守边界：只展示负载数字，不碰玩家名单 / 身份。

## 2. 需求（要什么）

- **后端聚合口径**：`avgMemUsed` / `avgMemMax` 由「÷全部实例」改「仅 bukkit」（复用 `countsInAvg(role)`，与 `avgTps` / `avgCpuLoad` 同口径）；无 bukkit 实例时为 0。
- **趋势内存口径**：`Downsample` 桶内内存均值同步改为「仅 bukkit」；桶内无 bukkit 时为 0。
- **每服明细带角色**：`Summary.Servers`（`ServerPlayers`）增 `Role` 字段（VARCHAR / 字符串，零方言），summary 视图 JSON 增 `role`，前端 `ServerPlayers` 类型同步。
- **前端双区块**：Dashboard 整体拆「子服(bukkit)」「BC 代理」两大区块——子服区用 bukkit 口径的总览卡片 + 趋势图 + 子服明细；BC 区用 BCPanel + bc 明细。每服明细按角色分组呈现。
- 范围内：上述聚合口径修正、每服明细带角色、前端按角色双区块重组。
- 不做（范围外）：新增任何指标 / 采集 / DB 列 / 端点；bc 专属趋势折线（FR-34 已界定范围外）；玩家名单 / 身份。

## 3. 设计（怎么做）

> 仅控制面聚合层 + web 展示层改动，不涉及新架构决策（完成 FR-32/FR-34 既定的角色分离，属其范围，扩展 ADR-0023，无新 ADR）。

### 3.1 控制面：聚合口径与每服明细角色
- `internal/service/metric_aggregate.go`：
  - `Summarize` 内存均值分母由 `len(insts)` 改为参与平均的 bukkit 计数；统计 bukkit 内存合计而非全部合计；分母为 0 时内存均值为 0。
  - `ServerPlayers` 增 `Role string`；`Summarize` 填充每条 role。
  - `Downsample`：桶内累加内存时仅累加 bukkit 样本，新增桶内 bukkit 内存计数作分母；桶内无 bukkit 时内存均值为 0。
- `internal/handler/metric_handler.go`：`serverPlayersView` 增 `Role string json:"role"`，`Summary` 回填。

### 3.2 web：双区块重组
- `web/src/api/client.ts`：`ServerPlayers` 增 `role: string`。
- `web/src/pages/DashboardPage.tsx`：整体拆「子服(bukkit)」与「BC 代理」两大区块；每服明细按 role 分组（bukkit 一组、bungee 一组）。
- `SummaryCards.tsx` / `BCPanel.tsx` / `TrendChart.tsx`：子服区总览/趋势用 bukkit 口径；BC 区用 BCPanel。复用现有 `StatCard` / `RoleBadge`，无新前端依赖。

## 4. 任务拆分
- [ ] 控制面：内存均值仅 bukkit（`Summarize` + `Downsample`）+ `ServerPlayers` 带 role（含穷举单测）。
- [ ] 控制面：`serverPlayersView` 增 role。
- [ ] web：`ServerPlayers` 类型增 role；Dashboard 拆双区块、每服明细按角色分组（含 vitest）。
- [ ] 文档同步：PRD 状态、ARCHITECTURE（聚合口径）、API（summary servers 增 role、内存口径说明）、CHANGELOG。

## 5. 验收标准
- `avgMemUsed` / `avgMemMax` 仅算 bukkit：混入 bc 实例不拉高 / 拉低子服内存均值；无 bukkit 时为 0（穷举单测覆盖）。
- `Downsample` 桶内内存均值仅算 bukkit（单测覆盖混合 / 全 bungee / 全 bukkit）。
- summary `servers` 每条带 `role`；前端按角色分两组展示，子服区内存 / TPS / CPU 均只算 bukkit，BC 区用 bc 指标。
- 看板与端点**无任何玩家名单 / 身份泄漏**（仅聚合数字）。
- 控制面 `go test ./...` 全绿；web `pnpm test` + `build` 通过。

## 6. 风险 / 待定
- 无 bukkit 实例（全 bc）时内存均值为 0：与 avgTps（无 bukkit 时为 0）口径一致，属预期，非异常。
- bc 专属内存趋势 / 折线本期不做（范围外，沿用 FR-34 界定）。
