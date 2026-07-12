# ADR-0065：跨服消息新增广播寻址（namespace / zone 级 fan-out），复活 topic 门面

**状态**：已接受（取代 [ADR-0063](0063-cross-server-message-control-plane-relay.md) 决策 2 的寻址范围与决策 7 的 topic no-op 条款；ADR-0063 其余决策——控制面单跳中转、禁 Redis/MQ、不做离线补投、名册权威、信封演进、fail-static——全部保留。随 FR-180（0.25.x）落地）

## 背景

[ADR-0063](0063-cross-server-message-control-plane-relay.md) 把跨服消息从 Redis 数据面直连改为控制面 HTTP 单跳中转时，决策 2 将寻址收窄为「仅 server / player 两种，取消 topic 发布订阅与广播 fan-out」，决策 7 将 `Messaging.publish` / `subscribe` 保留签名但降为 no-op——依据是「v2 PRD 无对应 FR」（YAGNI）。

该判断漏了真实使用信号：真机业务插件 Lodestone 明确依赖广播（部署脚本注记「Lodestone needs roster/**broadcast**/msg/teleport/RPC」），全服公告、跨服事件通知等场景没有广播不可替代（逐台定向 send 由业务插件自拼既笨重又无链路聚合）。需求已回 PRD 立项为 FR-180（正是 ADR-0063 与 spec §8-4 预留的口子）。

关键认知：**广播寻址 ≠ 消息总线**。ADR-0063 拒绝 topic 的实质理由是「控制面维护订阅关系表 + 离线留存会长成 MQ」；而「按注册表在线集合 fan-out + agent 本地按 topic 分发」不需要任何订阅状态与留存——订阅关系只存在于各 agent 进程内，控制面仍是无状态转发 + 事实存储。

## 决策

1. **新增 `targetKind=broadcast` 寻址**：`POST /beacon/v2/agent/messages/send` 接受 `targetKind=broadcast`，可选 `targetZone`（zone 级定向）。接收方集合由控制面按**当前在线服集合**（注册 / 健康内存事实）即时解析：无 `targetZone` = 发送者 namespace 全部在线服（backend + proxy，含发送者自身）；有 `targetZone` = 该 zone 当前在线服。**不建订阅状态表、不做跨 namespace 广播**（跨 ns 一律拒绝，fan-out 不出信任边界）。
2. **可丢语义（与 Legacy pub/sub 层一致）**：只投当前在线服；离线不补投；逐服投递沿用既有内存队列与 TTL / 重投 / 溢出规则（`expired` / `queue_overflow`）。需要可靠送达的单条消息仍走定向 `send` / `call`。
3. **追踪聚合行（防 ×N 写放大）**：一条广播落 **一行** `msg_trace`（`target_kind=broadcast`、可空 `target_zone`），新增可空聚合列（fan-out 目标数、送达 / 失败 / 过期计数），终态 = 全部目标到达终态（或整体 TTL 收口）后一次性落库；payload 仍只存一份 `msg_payload`。广播不参与拓扑 edge 聚合（无单一目标边），管理面列表 / 详情可按 `targetKind` 过滤、payload 照旧不出列表。
4. **复活 topic 门面**：`Messaging.publish(topic, payload)` = 广播发送（topic 落 `msg_type`）；`subscribe(topic, handler)` = agent 本地按 topic 注册分发（广播消息经 poll 下发时携带广播标记，按 topic 路由到订阅者，与定向消息的 `on(type)` 分发表隔离）。新增重载 `publish(topic, payload, zone)` 做 zone 级定向（接口只增不改，守二进制兼容）。既有业务插件（Lodestone）**零改动**从 no-op 变可用。

## 理由

- **补回真实需求**：广播是 MC 集群业务插件的基本盘（公告 / 事件通知 / 缓存失效），Legacy 有、真机在用，v2 不能没有。
- **不撞禁 MQ 红线**：无订阅注册表、无离线留存、无消费组——控制面新增的只是「解析在线集合 + 循环入既有队列」，规模约 50 服 × 低频广播，写放大由聚合行方案压回 1 行 / 广播。
- **语义忠实**：Legacy ADR-0016 里 pub/sub 本就是「可丢层」（可靠层是 Streams 定向），本 ADR 的可丢广播与其语义一一对应，业务插件预期不变。

## 后果

- 取代 ADR-0063 决策 2（寻址范围）与决策 7 中「publish/subscribe 底层 no-op」条款；ADR-0063 标注部分取代。
- `msg_trace` 加可空聚合列（GORM 加列向后兼容）；wire 的 send / poll 消息体各加 additive 键（信封只增不改）。
- 业务插件须知：广播是可丢的——离线服收不到、不补投；要可靠就用定向。
- spec `v2-connection-message-storage.md` §2.2 / §3.3 / §4.2 / §5.1 随本 ADR 同步。

## 备选方案

- **真订阅式 pub/sub（控制面订阅表 + 按订阅过滤投递）**：省去无订阅者服的无效投递，但引入订阅状态管理与一致性问题，向 MQ 滑坡；50 服规模下无效投递成本可忽略。**否决**（规模变大再议）。
- **业务插件自拼逐台定向 send**：无需改 Beacon，但 N 次 wire 往返、N 行 trace、无聚合可观测，且每个业务插件重复造轮子。**否决**。
- **保持 no-op 不做**：Lodestone 等真实场景无解。**否决**。
