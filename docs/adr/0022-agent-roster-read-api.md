# ADR-0022：agent-api 暴露玩家位置名册只读查询

**状态**：已接受

## 背景

[ADR-0016](0016-agent-cross-server-messaging-middleware.md) 决策 5 引入了"玩家位置名册"——由 BC 上的 beacon-proxy（agent-bungee）维护"玩家名→所在子服"索引（存 Redis hash `beacon:player-loc`，无 zone、无 TTL），供"按玩家寻址"在发送前解析其所在服。该名册当时**仅作消息中间件的内部寻址用途**：Messaging 门面只暴露 `sendToPlayer`，**不暴露读名册**；agent-core 只有 `PlayerLocator` 端口（`resolveServerId` 单个），没有全表读能力。

现下游业务插件（如 Lodestone 跨服看人）需要"谁在线、各 zone 各服有哪些人"这类**只读名册事实**来做总览、人数统计、Tab 补全。它们当前无从获取：名册数据本就躺在 agent 侧 Redis 里，却没有任何只读出口；控制面从不连 Redis、从不持有名册（ADR-0016 决策 2「控制面永不在消息路径上」），也不应承担此职责。

这触及两条既定约束的边界，需厘清：

- **ADR-0016 决策 5**：原措辞"Messaging 只暴露 `sendToPlayer`、不暴露读名册"。本 ADR 是对它的**扩展而非取代**——把"按玩家寻址"这一隐式读，显式化为"只读名册查询"这一新只读面；ADR-0016 决策正文不动（ADR 不可变只取代），其消息中间件语义完全保留。
- **[architecture-invariants](../../.claude/rules/architecture-invariants.md) #1**：原措辞"跨服玩家行为（看人/传送/经济）是业务插件的事，不进 Beacon MVP；agent 对业务插件只读暴露 API"。需厘清的是：**"看人"这件业务（聚合/分组/展示/补全/排序）仍不进 Beacon；但 agent 可以只读暴露"名册事实"**（谁在哪个服），正如它早已只读暴露 instances 事实（serverId/zone/address）。事实暴露 ≠ 业务实现，边界落在"数字/位置事实" vs "身份业务" 之间。

控制面（Go）侧本 ADR **零改动**：名册是 agent 数据面资产，控制面不参与。

## 决策

1. **在 agent-api 的 Discovery 门面新增两个只读名册方法**（与既有 `query` / `instancesInZone` / `instancesInGroup` 并列）：
   - `roster()`：返回当前 namespace 全量名册 `Map<玩家名, serverId>`，供总览 / 人数 / Tab 补全。
   - `rosterInZone(group, zone)`：返回某 zone 过滤后的名册 `Map<玩家名, serverId>`。
   - 两者均为**只读快照**，不暴露任何写入 / 改派 / 删除名册的旁路（守不变量 #1「只读暴露」）。

2. **agent-core 新增只读端口 `RosterDirectory`**：单一职责"全表读名册"（`Map<玩家名, serverId>`），与既有 `PlayerLocator`（单个解析）分离、不合并。core 只依赖该接口，**不 import Jedis**（守 [ADR-0005](0005-agent-transport-codec-abstraction.md)）。

3. **`DiscoveryView`（agent-core）组合 zone 权威 ∩ 名册**实现两方法：
   - `roster()` = `RosterDirectory` 全表读。
   - `rosterInZone(group, zone)` = `instancesInZone(group, zone)` 解出该 zone 的 serverId 集合（**zone 归属权威来自控制面 DB**，[ADR-0004](0004-zone-authority-control-plane.md)）∩ 全表名册中 value 落在该集合内的条目。**名册本身不臆造 zone**（Redis hash 里没有 zone 维度），zone 只能由控制面发现结果反查 serverId 再做交集。

4. **agent-adapters 新增 Redis 实现**：`RosterDirectory` 的实现走 `HGETALL beacon:player-loc`，藏在适配器里（与既有 `RedisPlayerRoster` 同处一个 Redis 连接 / 模块，复用 messaging 模块的连接与线程，不另起连接）。core 不感知 Jedis。

5. **优雅降级**：messaging 模块未开 / Redis 未连上 / 名册为空时，`roster()` 与 `rosterInZone()` 返回**空 Map**（非抛异常、非 null），业务插件据此走自身降级。与 ADR-0016 决策 9 的 fail-static 一致。

6. **不在 MC 主线程做阻塞 IO**：`HGETALL` 走 messaging 模块的异步线程，调用方（业务插件）若需在主线程用结果自行切回（守不变量 #5）。

## 理由

- **数据本就在 agent 侧 Redis**：beacon-proxy 已维护该名册供寻址，新增的只是一个"只读出口"，没有新增数据源、没有新增连接、没有新中间件——最小新增面。
- **守控制面/数据面边界**：名册是数据面资产，由数据面（agent）就近只读暴露，控制面零参与，不被拽进玩家热路径，不持有玩家身份数据。
- **事实暴露与业务实现分离**：agent 给"谁在哪个服"的事实，业务插件做"看人"（聚合 / 分组 / 展示 / 补全 / 跨服传送）。这与 agent 早已暴露 instances 事实、由业务插件消费同构，不把游戏逻辑塞进 Beacon。
- **zone 权威单一**：zone 归属只认控制面 DB（ADR-0004），名册不自带 zone 也就不会与控制面争权威；`rosterInZone` 用发现结果做交集，保证 zone 维度的真源唯一。
- **端口分离不滥用**：`RosterDirectory`（全表读）与 `PlayerLocator`（单个解析）职责不同，分立接口而非给 `PlayerLocator` 加方法，避免单一接口承担两类语义（防上帝接口）。

## 后果

- **名册无强一致（最终一致）**：名册由 beacon-proxy 随玩家进服 / 换服 / 退出更新，`roster()` 读到的是某一瞬间快照，换服瞬间可能短暂错位（沿用 ADR-0016 决策 5 的一致性取舍，不上分布式强一致）。业务插件须容忍瞬时偏差。
- **Redis 不可用即降级空**：名册依赖 Redis，Redis 挂 / 模块未开时返回空 Map，业务插件"看人"功能随之降级（看不到人），但不崩、不连累配置同步与玩家进服。
- **扩展了 agent 对业务插件的只读面**：从"只给 `sendToPlayer`"扩为"可加只读名册查询"。这是对 ADR-0016 决策 5 的**扩展**（旧决策正文不动，本 ADR 记录扩展关系），并厘清 architecture-invariants #1 措辞（"看人业务不进 Beacon；agent 可只读暴露名册事实"）。
- **多 BC 入口仍不在范围**：名册命名按单 BC 入口设计（沿用 ADR-0016 决策 11），多入口下名册归属另议。

## 备选方案

- **控制面开 HTTP 名册端点**：让 Beacon 连 Redis 读名册、对外开 `/admin` 或 `/agent` 名册接口。**否决**——直接撞架构红线：控制面从不连 Redis（ADR-0003 / ADR-0016 决策 2），且把玩家位置数据引入控制面、把控制面拽进玩家热路径，违反不变量 #1。
- **Lodestone（业务插件）自建名册**：让看人插件自己监听玩家进退、自建一份"玩家→服"索引。**否决**——与 beacon-proxy 已维护的名册重复造轮子、两份索引易不一致，且每个想看人的业务插件都得重建一遍。复用 agent 已有名册更简单。
- **把 zone 直接写进 Redis 名册**：在 `beacon:player-loc` 里给每条加 zone 字段，免去发现交集。**否决**——zone 归属权威是控制面 DB（ADR-0004），名册自带 zone 会出现第二个 zone 真源、改派时易脱节；用发现结果反查交集保持 zone 真源唯一。
