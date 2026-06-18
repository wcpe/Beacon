# ADR-0015：agent↔控制面 server→agent 推送合并为单条 SSE 流

**状态**：已接受

> 编号说明：0015 为真空缺填补——原 transfer ADR-0015 与 FR-24 均从未进入 git（已删），两个编号皆未发出过，复用不影响任何已入库引用。

## 背景

当前 agent↔控制面用 **REST 长轮询**（[ADR-0006](0006-rest-long-poll-push.md)）做 server→agent 推送。每个 agent 同时维持 **3 条长轮询（配置 / 文件树 / 覆盖集）+ 1 条心跳** ≈ 4 条往外连接（50 服 ≈ 200 条）。

随着接下来要加的 server→agent 推送特性（远程运维命令、[FR-29](../PRD.md) 拓扑 watch），若继续"每个特性各开一条长轮询"，连接数与循环数线性膨胀。需要一条**统一的 server→agent 推送通道**作地基。

评估过 WebSocket、gRPC 长连接（Nacos 2.x 路线），结论：**SSE 最契合本场景，WS/gRPC 过度且有实际坑**（见「备选方案」）。

## 决策

1. **统一为单条 SSE 推送流**：把 配置/文件树/覆盖集 三条 server→agent 长轮询合并为**一条 SSE 流**（server→agent 单向）。后续命令、拓扑变更等 server→agent 事件复用此流。

2. **流只发"变更通知"，不搬数据**：SSE 事件是轻量通知（如 `{type: config-changed, md5: X}` / `command-pending` / `topology-changed`）；agent 收到后**用现有 HTTP 端点取内容并应用**（pollConfig/pollManifest/fetch… 逻辑不变）。改的只是"如何得知有变更"，不改取数据与应用。

3. **连接即对账，绝不丢更新**：agent 建流时先上报各通道当前 md5 → 控制面**立即对账、补发断线期间落下的增量** → 再转入直播推送。这是正确性底线——替代长轮询天然自愈（每轮带 md5 比对）的能力，防止配置漂移。

4. **上行仍走 HTTP**：agent→控制面（注册、心跳、命令回执、blob/文件/override 内容拉取）继续用普通 HTTP POST/GET，**不进流、不走双工**。大块内容尤其不塞流。

5. **健康判活独立于流活性**：online/lost/offline（[FR-5](../PRD.md)）仍由**独立心跳 + TTL** 判定。**不得**用"SSE 断开"判失联（网络抖动断流但服务器健在 → 误杀），也不得用"流还连着"当健康。两者解耦。

6. **fail-static 不破**：流断 → agent 用本地快照继续、玩家无感；带退避重连 + 重连对账。

7. **扩展而非推翻 [ADR-0005](0005-agent-transport-codec-abstraction.md)**：在既有 `HttpTransport`/`JsonCodec` 之外**新增一个流式传输抽象**，SSE 客户端实现放适配器（守架构不变量 #5：HTTP/流客户端只在适配器、core 不硬绑），core 不绑具体实现；JSON 编解码沿用 `JsonCodec`。

8. **取代 [ADR-0006](0006-rest-long-poll-push.md)**：长轮询作为 server→agent 推送机制被 SSE 取代。**注意**：实际迁移随 FR-24 实施完成；在 FR-24 落地前，长轮询仍是运行实现，`ARCHITECTURE.md`/`API.md`/`OPERATIONS.md` 描述的当前真貌届时随实现同步更新。

9. **web↔控制面不变**：管理台仍走 REST（前端不动）。本 ADR 只改 agent↔控制面 的 server→agent 推送。

10. **反向代理/Docker 兼容**：SSE 经 nginx/反代须**关闭 response buffering**、调长读超时；Docker 网络下沿用现有地址可达约束。运维注意随实现写入 OPERATIONS。

## 理由

- 本场景需要的只有 **server→agent 单向推送**；SSE 纯 HTTP、无握手、Go `http.Flusher` 直接做、最穿透代理，恰好够用且最省。
- "只发通知 + 现有端点取数据"使改动面集中在"唤醒机制"，复用全部取数据/应用逻辑，低风险。
- 一条流取代多条长轮询，连接数与循环数随特性增加不再线性膨胀，为命令/watch 等后续 server→agent 特性打统一地基。
- 扩展 ADR-0005 抽象而非硬编码，保持 core 解耦、可移植（日后换别的流式传输不动业务层）。

## 后果

- agent 每服往外连接从 ~4 降到 ~2（1 条 SSE + 心跳；blob 取数据为瞬时 HTTP）。
- 新增"连接即对账"逻辑（服务端按上报 md5 算增量），是必须实现且必须测的正确性关键路径。
- 取代 ADR-0006；**实现期**（FR-24）同步更新 `ARCHITECTURE.md`、`API.md`（新增 SSE 端点、标注原长轮询端点去向）、`OPERATIONS.md`（反代/Docker SSE 注意）。架构不变量无需改（其未约束传输机制）。
- agent 侧新增 SSE 客户端适配器（仍为纯 HTTP 读流，无重型依赖、无 netty 冲突）。
- 远程命令、FR-29 watch 作为消费者复用本流，各自独立 FR，不在本 ADR 内实现。

## 备选方案

- **维持现状（多条 REST 长轮询）**：最省事，但每加一个 server→agent 特性就多一条长轮询/循环，连接与维护成本线性涨；不利于命令/watch 等后续特性。**否决**（本 ADR 即为解此）。
- **WebSocket（全双工）**：能把上行也并到一条连接，但本场景上行是请求-响应、不需要持久双向；WS 还要 WS 库（双端）、ping/pong、帧协议、更复杂重连，且双工能力闲置。**否决**（过度）。
- **gRPC 长连接（Nacos 2.x 路线）**：为十万级实例 + 高频双向 RPC 设计，远超本规模；**且 gRPC-java 跑在 netty 上，与 Bukkit/BungeeCord 自带 netty 版本冲突（MC 插件类加载死穴）**；还需换栈（撞架构不变量 #7）、迁 protobuf（丢 JSON 简单可调试）、重写 ADR-0005、HTTP/2 代理复杂度、agent 重依赖。收益（双工/二进制/海量多路复用）本场景均用不上。**否决**。
- **内容直接进流（不止发通知）**：省一次取数据往返，但要把大块 blob/文件塞流、流协议变重、与现有 fetch 端点重复。**否决**（通知式改动最小、复用现有逻辑）。
