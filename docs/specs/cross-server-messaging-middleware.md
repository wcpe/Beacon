# 功能规格：agent 内置跨服消息中间件

> 状态：开发中　·　关联 PRD：FR-26　·　决策：[ADR-0016](../adr/0016-agent-cross-server-messaging-middleware.md)　·　建议分支：feature/cross-server-messaging

## 1. 背景与目标

为 BC/Bukkit 子服间提供**通用、可靠、可定向、能等回信**的服务器间通信，替代 PluginMessage 广播式、需在线玩家载体、无回调的现状。中间件只做**与内容无关的传输**，业务功能（匹配/对战/存储/排行）由③层业务插件基于本中间件自行实现。架构决策见 [ADR-0016](../adr/0016-agent-cross-server-messaging-middleware.md)。

## 2. 需求（要什么）

**范围内（②层通用传输）：**
- 四种模式：
  - 定向发送 `send(目标serverId, type, payload)`：不等回复。
  - 请求-响应 `call(目标serverId, type, payload) → Future<resp>`：带关联ID + 超时。
  - 主题发布订阅 `publish(topic, payload)` / `subscribe(topic, handler)`。
  - 按玩家寻址 `sendToPlayer(playerName, type, payload)`：解析玩家所在服后定向发送。
- 收消息：`on(type, handler)` 注册按消息类型分发的处理器。
- 可靠送达：定向/按玩家/需补偿事件走 Redis Streams + 每服消费组，离线补消费、至少送达一次；可丢事件可走 pub/sub。
- 消息有 **type + version 字段**，序列化经既有 JsonCodec 抽象。
- 玩家位置名册：beacon-proxy 维护"玩家→所在子服"（Redis），随进服/换服/退出更新。
- 软依赖 + `isAvailable()`：模块未开/Redis 未连上时业务插件优雅降级。

**不做（范围外）：**
- 不做任何业务功能：匹配、实时对战、存储、排行榜（属③层独立业务插件）。
- 不引入 Kafka/RabbitMQ/Akka/Nakama/Agones 等重型件。
- 消息不经过 Beacon 控制面中转。
- 不做控制面 HA、不做 Redis 集群化（后续按需）。
- **不做多 BC 入口**：明确假设单 BC（类 nginx 单流量入口），多入口未来另立 ADR。
- 不做跨服实时状态高频同步（MC 每台服自身权威，非本中间件职责）。

## 3. 设计（怎么做）

### 分层
- ①Beacon 控制面：只提供发现（服解析地址簿），不参与消息。
- ②本中间件：agent 内独立可开关模块。
- ③业务插件：基于②的 API 实现玩法，独立项目。

### 模块与隔离
- 包结构（建议）：`agent-core` 内新增 `messaging/`（API + `MessageTransport` 抽象 + 路由/关联ID/超时/分发）；Redis 实现放适配器模块（如 `agent-adapters/messaging-redis`）。
- Redis 客户端（Jedis）自带、**不经 CoreLib**，经 TabooLib `@RuntimeDependencies` 运行期下载、不 shade，藏在 `MessageTransport` 适配器后（见 [ADR-0016](../adr/0016-agent-cross-server-messaging-middleware.md) 决策 14）。
- 独立连接、独立线程池；与配置同步/心跳互不共享，互不阻塞。
- 配置开关 `messaging.enabled`（默认关）。Redis 连接配置由 **Beacon 配置中心下发**（host/port/db），全集群一份、可热更可回滚；agent 无 env，**密码也经 Beacon 下发，故依赖 FR-20 配置加密先行**（详见 [ADR-0016](../adr/0016-agent-cross-server-messaging-middleware.md) 决策 14/15）。不硬编码、密码不明文入库。

### 传输与模式实现（Redis）
- 定向：每服订阅 Stream `beacon:msg:{serverId}`（消费组 = serverId）；A 发给 B 即 `XADD beacon:msg:B`。
- 主题：`beacon:topic:{topic}`，订阅者按需消费。
- RPC：请求带 `correlationId` + `replyTo`（发起方专属回信通道）；目标处理后回发，发起方按 correlationId 唤醒等待的 Future，超时清理。
- 按玩家：查 `beacon:player-loc`（hash: playerName→serverId）得目标服 → 走定向。
- 可靠性：Streams 消费 ack；未 ack 在重连后重投（业务侧幂等）。

### 线程模型
- 订阅消费在独立线程；收到消息默认在异步线程交给 handler；提供 `runOnPlatformThread` 帮助方法供需碰 Bukkit/Bungee API 的 handler 切回平台线程。

### 故障行为
- Beacon 挂：地址簿用缓存，消息照传。
- Redis 挂：`send` 失败/`call` 超时/订阅暂停并自动重连；配置同步与玩家游玩不受影响。
- 目标服离线：可靠消息留存待其上线补消费；RPC 直接超时。

## 4. 任务拆分（待审定后细化，暂不勾选）

- [ ] PRD FR-26 登记 + Non-Goals 澄清；ADR-0016 落库；本 spec 落 docs/specs。
- [ ] agent-core：`MessageTransport` 抽象 + 消息信封（type/version/correlationId/replyTo）+ 路由分发 + RPC Future/超时。
- [ ] 适配器：Redis（Streams 消费组 + pub/sub + 回信通道）实现，连接管理与重连。
- [ ] beacon-proxy：玩家位置名册维护（进服/换服/退出事件 → Redis），重启重新扫描重建。
- [ ] agent-api：对③层暴露 send/call/publish/subscribe/sendToPlayer/on + isAvailable。
- [ ] 测试：单元（关联ID配对、超时、幂等、信封序列化）；集成（真 Redis：离线补消费、定向、RPC、主题、按玩家寻址）；故障（杀 Redis 不连累配置/玩家）。
- [ ] Docker：compose 加 Redis；验证容器网络下 address 可达。

## 5. 验收标准

- A 服 `call` B 服某 type，能在超时内拿到 B 的返回值；B 不在线则超时报错。
- 定向/按玩家消息发给离线目标服，目标**上线后补收到**（Streams 消费组）。
- 主题订阅者只收所订主题，未订阅者收不到（非广播）。
- `sendToPlayer` 能据玩家当前所在服正确投递；玩家换服后投递到新服。
- 杀 Redis：消息功能失败但配置同步正常、玩家正常进服游玩；Redis 恢复后自动重连续传。
- 关闭 `messaging.enabled`：业务插件 `isAvailable()=false` 并走降级，不报错崩溃。
- Beacon、agent 双端测试通过，注释/日志中文、无硬编码凭据。

## 6. 已定细则（见 [ADR-0016](../adr/0016-agent-cross-server-messaging-middleware.md) 决策 5/11/12/13）

- **玩家名册一致性**：接受换服瞬间短暂错位；解析落空走"找不到目标"兜底（重试一次或丢弃），不上强一致。**proxy 重启**后不信旧名册，重新扫描在线玩家重建。
- **Streams 裁剪**：每条流设 `MAXLEN`/时间窗口上限，旧消息自动淘汰；离线超窗的子服上线即"错过不补"。阈值实现时按 Redis 内存定。
- **单 BC 入口为前提**：集群仅一个流量入口（类 nginx 单入口），名册与消费组命名按单 proxy 设计。**多 BC 不在本轮范围**，未来另立 ADR。
- **消息版本**：信封带 `type` + `version`，演进"只增不改"（可加可选字段，不删/不改已有字段），保证新老插件混跑兼容。

## 7. 仍开放（实现期定，非阻塞）

- Streams 具体阈值数值（MAXLEN/窗口）——按部署机 Redis 内存定。
- RPC 默认超时时长、重试次数的默认值——按业务可接受延迟定。
