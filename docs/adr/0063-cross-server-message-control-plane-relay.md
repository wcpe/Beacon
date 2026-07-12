# ADR-0063：跨服消息通道由 Redis 数据面直连改为控制面 HTTP 单跳中转

**状态**：已接受（取代 [ADR-0016](0016-agent-cross-server-messaging-middleware.md) 的传输层与 Redis 决策；随 P5（FR-149/150）落地）。**决策 2 的寻址范围与决策 7 的 topic no-op 条款已被 [ADR-0065](0065-message-broadcast-addressing.md) 取代**（新增广播寻址、复活 publish/subscribe），其余决策仍有效。

## 背景

[ADR-0016](0016-agent-cross-server-messaging-middleware.md)（Legacy，P3 立项）把跨服消息定为**数据面直连**：消息只在 `agent ↔ Redis ↔ agent` 之间流动、Redis Streams 做可靠骨干、pub/sub 做可丢广播、"玩家→所在服"名册存 Redis、**控制面永不在消息路径上**，并在其「备选方案」里把"消息走控制面中转"作为撞架构红线**否决**。

第二版（Beacon v2）的架构不变量已收紧：[architecture-invariants](../../.claude/rules/architecture-invariants.md) §2 明确 **MVP 禁止引入 Redis / 消息队列 / DI 框架**（不再区分控制面 / 数据面），[ADR-0003](0003-no-redis-in-mvp.md) 的"无 Redis"约束在第二版扩展为全局。ADR-0016 的 Redis 传输层与之直接冲突，且其依赖链（Redis 密码经 FR-20 配置加密下发）在第二版范围纪律里同属未纳入项。

P5 需要让跨服通信真正可用（第二版消息面此前随 Redis 一并未激活）。规格 [v2-connection-message-storage.md](../specs/v2-connection-message-storage.md) §8 风险 1 记「已拍板（2026-07-07）：确认控制面中转；新 ADR 在 P5 开工前补写」。本 ADR 即该新 ADR，正式取代 ADR-0016 的传输决策——**不在代码里静默违背既有 ADR**（[decision-alignment](../../.claude/rules/decision-alignment.md) §2）。

## 决策

1. **消息经控制面单跳中转，禁 Redis**：跨服消息走 `agent →(REST)→ Beacon 控制面 →(长轮询)→ agent`。上行 `POST /beacon/v2/agent/messages/send`、下行 `POST /beacon/v2/agent/messages/poll`（长轮询，无消息 204）、回执 `POST /beacon/v2/agent/messages/ack`。控制面把每条消息落 `msg_trace` 元数据日表（payload 分表 `msg_payload`），只做**传输编排 + 事实存储**，不解析业务语义。**不引入任何 Redis / MQ / 中间件**。

2. **只两种寻址，不做 topic 与广播**：保留**定向发送（server）**与**按玩家所在服寻址（player）**两种；**取消** ADR-0016 的主题发布订阅（pub/sub）与广播 fan-out（第二版不做 topic，避免退化为消息总线）。RPC 请求-响应保留，经 `correlationId` 关联。

3. **不做离线补投队列**：无 Redis Streams 的"离线留存 + 补消费"。消息 TTL 到期（默认 30s 无人取走）即 `expired`、目标离线即 `failed`，**不持久化待投**（否则退化为 MQ，撞禁 MQ 不变量）。业务侧按 `messageId` 幂等去重。这是相对 ADR-0016「至少送达一次 + 离线补消费」的**实质可靠性降级**，是有意识取舍。

4. **玩家位置名册权威迁至控制面**：ADR-0016 由 BC 侧 beacon-proxy 把"玩家→所在服"索引存 Redis；本 ADR 改为**控制面依 `conn_detail`（连接明细，FR-145）在内存维护 `player_uuid → resolved server` 快照**，作为按玩家寻址的解析权威。agent 侧不再维护 Redis 名册。沿用 ADR-0016 §5 的一致性取舍（接受换服瞬间短暂错位、解析落空走"找不到目标"兜底、进程重建期短暂误判可接受）。

5. **信封只增不改**：消息信封新增 `messageId`(UUIDv7)、发送时间戳、`hops`（链路事件数组）等字段，沿用 ADR-0016 §13「只增不改」演进规约，保证集群内新老插件混跑向后兼容。

6. **传输抽象仍遵 [ADR-0005](0005-agent-transport-codec-abstraction.md)**：agent core 依赖 `MessageTransport` / `HttpTransport` / `JsonCodec` 接口，HTTP 客户端与 JSON 库只在适配器。`MessageTransport` 的实现由 Redis 换为基于 `BeaconApiClient` 的 HTTP 实现；ADR-0016 的 `RedisMessageTransport` 及 Jedis 依赖退役为孤儿（Legacy 代码保留、v2 不激活，按精准修改不做扩散式删除）。

7. **agent-api 门面对③层业务插件保持只读、契约稳定**：`Messaging` 门面保留 `send` / `call` / `on`(订阅本机投递) / `isAvailable`；`publish` / `subscribe`(topic) 保留接口签名但底层 no-op（`isAvailable()` 语义下快速失败），守向后兼容、不破坏已依赖该接口的业务插件编译。链路查询是**管理面**能力（`/admin/v2/messages/*`），不经 agent-api 暴露。

8. **fail-static 边界**：消息面**不做本地缓冲重发**——控制面不可用时 `isAvailable()=false`、`send` 快速失败、RPC 超时（实时消息过时即弃）；连接采集面（FR-145）进本地有界内存缓冲、恢复后补报。两者都**绝不阻断玩家进服游玩**（玩家连接是 BC/Bukkit 原生），配置 / 调度的 fail-static 不受影响。

## 理由

- **对齐第二版禁 Redis / MQ 不变量**：去掉 Redis 单点与额外运维件，消息面与控制面共用同一套 HTTP + 长轮询基础设施（配置长轮询、调度候选拉取已在用），最小化新增面。
- **本规模够用**：约 50 服、每秒数百~数千条，控制面单跳中转 + 长轮询足以覆盖定向 / 按玩家寻址 / RPC；不需要 Streams 的持久化 / 消费组 / 重放。
- **正面回应 ADR-0016 的"红线否决"**：ADR-0016 否决控制面中转，理由是"把控制面拽进热路径、把游戏逻辑引入控制面"。本 ADR 的中转**只存事实（msg_trace）+ 转发不透明 payload，不理解任何业务语义**——[architecture-invariants](../../.claude/rules/architecture-invariants.md) §1 禁的是"游戏逻辑进控制面"，数据传输编排不属游戏逻辑；且消息面 fail-static（控制面挂 → `isAvailable=false` 快速失败），**控制面在消息路径上不等于玩家热路径被阻断**，玩家进服游玩全程不经此路径。取舍的代价是消息可靠性降级（见后果），在第二版禁 Redis 前提下这是最简可用方案。
- **名册权威归位**：`conn_detail` 本就是"玩家在哪"的第一手事实（proxy 采集 open/close/换服摘要），由控制面据此解析按玩家寻址，消除 agent 侧 Redis 名册这一冗余真源。

## 后果

- **取代 [ADR-0016](0016-agent-cross-server-messaging-middleware.md)** 的传输层（§2 数据面直连 / §3 Redis Streams·pub/sub / §5 Redis 名册 / §12 Streams 裁剪 / §14-15 Jedis·Redis 配置下发）与"消息不经控制面"的核心决策；ADR-0016 状态改为"已被 ADR-0063 取代"。ADR-0016 的分层思想（§1 ①②③三层、②对③只提供与内容无关的传输）与信封演进规约（§13）予以承继。
- **消息可靠性降级**：由"至少送达一次 + 离线补消费"降为"在线尽力送达 + TTL 过期即弃"。业务插件须按 `messageId` 幂等，并接受离线目标消息丢失。这是与 ADR-0016 的实质差异，须让③层业务插件知晓。
- **孤儿代码**：`RedisMessageTransport` / Jedis 依赖 / Redis 名册实现成为 v2 非激活的 Legacy 代码，保留不删（后续如彻底下线 Legacy 再单独清理）。
- **控制面新增**：`msg_trace` / `msg_payload` 日表与消息中转端点（数据面事实存储 + 传输编排，非调度 / 连接决策，符合 [architecture-invariants](../../.claude/rules/architecture-invariants.md) §1「只存事实」）。
- **不与 [ADR-0003](0003-no-redis-in-mvp.md) 冲突**：本 ADR 令消息面也回到"无 Redis"，与 ADR-0003 同向收紧，不再依赖 ADR-0016 的"数据面 Redis 例外"论证。

## 备选方案

- **保留 ADR-0016 的 Redis Streams 传输**：撞第二版禁 Redis / MQ 不变量（architecture-invariants §2），且引回单点与运维件。**否决**。
- **agent↔agent P2P 直连**：需 agent 间互相可达 + 网状服务发现 + 连接管理，撞单 BC 入口前提与简单优先，且无天然的链路事实落点。**否决**。
- **引入 NATS / 轻量 MQ 替 Redis**：仍是新中间件，撞禁 MQ 不变量。**否决**。
- **消息面完全下线、留给③层业务插件自实现**：撞 ADR-0016 §1 已确立的"②层通用传输由 agent 提供"分层，且 FR-149/150 明确要求控制面可追踪跨服消息链路。**否决**。
