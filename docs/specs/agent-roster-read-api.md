# 功能规格：agent-api 玩家位置名册只读查询

> 状态：草拟　·　关联 PRD：FR-31　·　决策：[ADR-0022](../adr/0022-agent-roster-read-api.md)（扩展 [ADR-0016](../adr/0016-agent-cross-server-messaging-middleware.md) 决策 5）　·　分支：feature/agent-roster-read-api

## 1. 背景与目标

[ADR-0016](../adr/0016-agent-cross-server-messaging-middleware.md) 引入的玩家位置名册（beacon-proxy 维护、存 Redis hash `beacon:player-loc`，玩家名→serverId）当前**仅供消息中间件内部按玩家寻址**用：agent-core 只有 `PlayerLocator`（单个解析），agent-api 只暴露 `sendToPlayer`，**没有只读名册的出口**。

下游业务插件（如 Lodestone 跨服看人）需要"谁在线、各 zone 各服有哪些人"这类**只读名册事实**做总览、人数统计、Tab 补全，但当前拿不到。本功能在 agent-api 的 Discovery 门面增加**只读名册查询**，把已躺在 agent 侧 Redis 的名册数据，作为"事实"只读暴露给业务插件——与 agent 早已只读暴露 instances 事实同构。架构决策与边界厘清见 [ADR-0022](../adr/0022-agent-roster-read-api.md)。属 P3（与 FR-26 消息中间件同期，复用其 Redis 模块）。

**边界（锁定）**：agent 给"谁在哪个服"的**事实**；"看人"这件**业务**（聚合 / 分组 / 展示 / 补全 / 排序 / 跨服传送）仍归业务插件。线 = 位置事实（界内）vs 身份业务（越界）。

## 2. 需求（要什么）

**范围内：**
- agent-api `Discovery` 门面新增两只读方法：
  - `roster()`：返回当前 namespace 全量名册 `Map<玩家名, serverId>`。
  - `rosterInZone(group, zone)`：返回某 zone 过滤后的名册 `Map<玩家名, serverId>`。
- agent-core 新增只读端口 `RosterDirectory`（全表读名册），`DiscoveryView` 组合「控制面权威 zone→serverId 集」∩「名册」实现 zone 过滤。
- agent-adapters Redis 实现：`HGETALL beacon:player-loc`，复用 messaging 模块既有 Redis 连接 / 线程，不另起连接。
- 优雅降级：模块未开 / Redis 未连 / 名册空 → 返回空 Map（非 null、非抛异常）。
- 双端（Bukkit / Bungee）业务插件均可经 `BeaconAgent.discovery()` 调用。

**不做（范围外）：**
- 不做"看人"业务本身：聚合 / 分组 / 展示 / Tab 补全 / 排序 / 跨服传送（属业务插件）。
- 不暴露任何写名册 / 改派 / 删名册的旁路（只读）。
- 控制面（Go）零改动——不连 Redis、不开名册端点、不持有名册（守红线，见 ADR-0022 备选方案）。
- 名册不自带 zone 字段；zone 只由控制面发现结果权威反查（不在 Redis 里加 zone）。
- 不上玩家名册强一致（沿用 ADR-0016 决策 5 的最终一致取舍）。
- 不涉及多 BC 入口（沿用单 BC 前提）。

## 3. 设计（怎么做）

不新增数据源 / 连接 / 中间件；复用 FR-26（ADR-0016）的 messaging Redis 模块。涉及的架构决策见 [ADR-0022](../adr/0022-agent-roster-read-api.md)，此处不重复决策正文。

### agent-api（门面，对③层业务插件）

在 `Discovery` 门面新增（与 `query` / `instancesInZone` / `instancesInGroup` 并列）：

```
// 全量名册：当前 namespace 内 玩家名 → 所在 serverId 的只读快照
roster(): Map<String, String>

// zone 过滤名册：仅含落在该 group/zone 下子服的玩家（zone 权威来自控制面）
rosterInZone(group: String, zone: String): Map<String, String>
```

返回不可变 Map 快照；不可用时返回空 Map。

### agent-core（端口 + 组合逻辑）

- 新增只读端口 `RosterDirectory`：
  ```
  // 读取全量名册快照（玩家名 → serverId）；不可用时返回空 Map
  fun snapshot(): Map<String, String>
  ```
  与既有 `PlayerLocator`（`resolveServerId(playerName): String?`，单个）**分立不合并**，职责不同。
- `DiscoveryView`（agent-core 实现）组合：
  - `roster()` = `rosterDirectory.snapshot()`。
  - `rosterInZone(group, zone)`：
    1. 经发现解出该 zone 的可用 serverId 集合 `S`（即 `instancesInZone(group, zone)` 结果的 serverId 集，zone 归属权威来自控制面 DB，[ADR-0004](../adr/0004-zone-authority-control-plane.md)）；
    2. 取 `snapshot()` 中 value ∈ `S` 的条目，构成过滤后名册。
  - **名册不臆造 zone**：交集的 zone 维度只来自发现结果，名册只提供"玩家→服"。

### agent-adapters（Redis 实现）

- `RosterDirectory` 的 Redis 实现走 `HGETALL beacon:player-loc`，藏在适配器后（与既有 `RedisPlayerRoster` 同处 messaging 模块、共用其 Redis 连接与线程池，不新建连接）。core 不 import Jedis（守 [ADR-0005](../adr/0005-agent-transport-codec-abstraction.md)）。

### 降级与线程

- messaging 模块未开 / Redis 未连 / `HGETALL` 异常 / 名册空 → 实现层返回空 Map，`DiscoveryView` 据此返回空（含 `rosterInZone` 交集为空时也返回空 Map）。
- `HGETALL` 走 messaging 模块异步线程，不在 MC 主线程阻塞 IO；业务插件若需在主线程消费结果自行切回平台线程（守不变量 #5）。

## 4. 任务拆分

- [ ] agent-core：新增 `RosterDirectory` 端口（只读全表读，与 `PlayerLocator` 分立）。
- [ ] agent-core：`DiscoveryView.roster()` / `rosterInZone(group, zone)` 实现（zone 集 ∩ 名册组合逻辑，单测先行）。
- [ ] agent-api：`Discovery` 门面新增 `roster()` / `rosterInZone()` 方法签名 + 经 `BeaconAgent.discovery()` 暴露。
- [ ] agent-adapters：`RosterDirectory` 的 Redis 实现（`HGETALL beacon:player-loc`，复用 messaging 连接 / 线程，异常 → 空 Map）。
- [ ] 单元测试：全表读、zone 过滤交集正确、空名册 / 不可用降级返空、交集为空返空、不臆造 zone、并发读安全。
- [ ] 集成（真 Redis）：双端业务插件调用 `roster()` / `rosterInZone()` 拿到正确名册；杀 Redis 降级返空不崩。**本环境无 Redis，标"待集成/真机验"。**
- [ ] 文档同步：PRD §4 加 FR-31 行 + §6 验收项、ARCHITECTURE 发现/名册章节增补、API.md agent-api Discovery 新方法说明、adr/README 加 0022 行、CHANGELOG 未发布段。

## 5. 验收标准

- agent `gradle test` 全绿，含：
  - **全表读**：`roster()` 返回名册全部条目（玩家名→serverId），与 Redis hash 内容一致。
  - **zone 过滤交集正确**：`rosterInZone(group, zone)` 只含 value 落在该 zone serverId 集合内的玩家；不在该 zone 的玩家被排除；zone serverId 集来自发现结果（控制面权威），名册不提供 zone。
  - **交集为空 / zone 无人**：返回空 Map（非 null）。
  - **Redis 不可用 / 模块未开 / 名册空**：`roster()` 与 `rosterInZone()` 均返回空 Map，不抛异常、不崩。
  - **并发读安全**：多线程并发调用 `roster()` / `rosterInZone()` 不数据竞争、不脏读（快照不可变）。
  - **不阻塞主线程**：`HGETALL` 在异步线程执行（不在 MC 主线程同步阻塞 IO）。
  - **只读无旁路**：门面不暴露任何写 / 删名册方法。
- 控制面（Go）无改动——`go test ./...` 不受影响（零代码改动）。
- 注释 / 日志中文、无硬编码凭据。

## 6. 风险 / 待定

- **名册最终一致**：换服瞬间 `roster()` 可能短暂错位（沿用 ADR-0016 决策 5 取舍），业务插件须容忍瞬时偏差——非缺陷，文档说明即可。
- **`rosterInZone` 两次读的时序错位**：发现结果与名册分两次读，极端情况下二者快照时点略有差（玩家刚换服、发现尚未刷新）。可接受（非强一致场景），不为此上事务 / 锁。
- **大名册 `HGETALL` 开销**：单 BC 单 namespace 规模（约 50 服、数千在线）下 `HGETALL` 一次性读可接受；若未来规模显著增大再评估分批 / 缓存（当前不做，避免镀金）。
