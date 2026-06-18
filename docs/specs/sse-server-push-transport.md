# 功能规格：agent↔控制面 server→agent 推送合并为单条 SSE 流

> 状态：开发中　·　关联 PRD：FR-24　·　决策：[ADR-0015](../adr/0015-sse-server-push-transport.md)（取代 [ADR-0006](../adr/0006-rest-long-poll-push.md)、扩展 [ADR-0005](../adr/0005-agent-transport-codec-abstraction.md)）　·　建议分支：feature/sse-server-push

## 1. 背景与目标

把 agent↔控制面的 server→agent 推送从"多条 REST 长轮询（配置/文件树/覆盖集）"合并为**单条 SSE 推送流**，降低每 agent 连接数，并作为后续 server→agent 特性（远程运维命令、FR-29 拓扑 watch）的统一推送地基。架构决策见 [ADR-0015](../adr/0015-sse-server-push-transport.md)。

## 2. 需求（要什么）

**范围内：**
- 控制面新增**单条 SSE 端点**，按 serverId 维护每条连接，定向推送该 agent 受影响的变更事件。
- 事件为**轻量通知**：`config-changed` / `file-changed` / `override-changed`（含新 md5）+ 预留 `command-pending` / `topology-changed`。
- **连接即对账**：agent 建流时上报各通道当前 md5；控制面对账后补发落下的增量（或通知 agent 去拉），再转直播。
- agent 收到通知后**复用现有 HTTP 端点取内容并应用**（pollConfig/pollManifest/override fetch 逻辑不变）。
- agent 侧把 3 条长轮询循环替换为 1 条 SSE 监听 + 重连退避 + 重连对账。
- **心跳保持独立 HTTP**；健康判活（FR-5）独立于 SSE 流活性。
- fail-static：流断用本地快照继续，玩家无感。

**不做（范围外）：**
- 不改 web↔控制面（管理台仍 REST，前端不动）。
- 不把上行（注册/心跳/回执/blob 取数据）并入流，不上 WebSocket 双工。
- 不上 gRPC（netty 冲突 + 换栈 + 过度，见 ADR-0015 备选）。
- 不在流里搬大块内容（blob/文件仍走 HTTP GET）。
- 不实现远程命令、FR-29 watch 的业务（它们是本流的消费者，各自独立 FR）。

## 3. 设计（怎么做）

### 控制面
- 新增 SSE handler：`GET .../agent/stream`（held-open，`text/event-stream`），按 serverId 注册连接到一个 per-conn 事件通道。
- 复用现有"算最小受影响 serverId 集合"逻辑（[ADR-0006](../adr/0006-rest-long-poll-push.md) 的唤醒集合）→ 变更时只向受影响连接推事件。
- 连接建立握手：agent 上报 `{configMd5, fileMd5, overrideMd5}`；控制面比对，落后的立即补发 `*-changed` 事件，再转直播。
- 复用 Registry/Hub/Health 三锁纪律，DB IO 在锁外。

### agent（core + 适配器，遵循 ADR-0005/不变量 #5）
- core 新增**流式传输抽象**（如 `StreamTransport`），SSE 客户端实现放适配器；core 不 import 具体 HTTP/SSE 库。
- `AgentLifecycle` 把 `startConfigPollLoop/startFileTreePollLoop/startOverridePollLoop` 三条长轮询替换为**一条 SSE 监听循环**；收到 `*-changed` 即触发对应 `forcePoll/forceFileTreeSync` 复用既有拉取-应用逻辑。
- 重连：退避 + 重连后先对账（带各通道 md5）。
- 心跳循环不动；reconnect 仍重置各代标识/退避器。

### 故障行为
- 控制面挂/流断：agent 用本地快照继续，退避重连，重连即对账补增量。
- 健康：仍由独立心跳 + TTL 判定，与流活性解耦。

## 4. 任务拆分（待细化）

- [x] PRD FR-24 登记；ADR-0015 落库 + ADR-0006 标取代；本 spec 落 docs/specs。
- [x] 控制面：SSE 端点（`GET /beacon/v1/agent/stream`，`internal/handler/stream_handler.go`）+ per-conn 注册（两 Hub waiter）+ 连接握手对账（`StreamService.Run`/`DiffEvents`，`internal/service/stream_service.go`）+ 复用受影响集合唤醒（`NotifyChan()`）+ 事件编码纯函数（`internal/sse`）。
- [x] agent-core：`StreamTransport`/`StreamEvent` 抽象（`transport/StreamTransport.kt`）+ `SseFrameParser`/`StreamEvents`（`stream/`）+ `AgentLifecycle` SSE 监听循环（注入 streamTransport 时取代三条长轮询）+ 重连对账。
- [x] 适配器：SSE 客户端实现 `OkHttpStreamTransport`（纯 HTTP 读流，`agent-adapters`）；双端壳注入。
- [x] 测试：单元（`DiffEvents` 对账算增量、`SseFrameParser` 分帧、`ChangedPayload` 取 md5、`AgentLifecycleStream` 事件分发/启用流取代长轮询/流断重连）；sqlite-backed `StreamService.Run`（连接即对账 + 直播热推 + 只发受影响，免 MySQL 跑通）；SSE 集成测试 `-tags=integration`（需 MySQL）。
- [x] 实现期同步文档：ARCHITECTURE §6.1 / API §2.5（SSE 端点 + 原长轮询端点退化说明）/ OPERATIONS §3.1（反代关 buffering、调长读超时、Docker）/ CHANGELOG 未发布段。

## 5. 验收标准

- 一条 SSE 流取代 配置/文件树/覆盖集 三条长轮询；agent 每服 server→agent 连接从 ~3 降到 1。
- 配置/文件/覆盖集变更后，受影响 agent 经 SSE **近实时**收到通知并正确应用；未受影响 agent 不收。
- agent 断线期间发生的变更，**重连对账后补齐**，不丢更新（与原长轮询自愈等价）。
- 杀控制面/断流：agent 按本地快照继续、玩家正常游玩；恢复后自动重连并对账。
- 健康判活仍由心跳决定：流断但心跳正常 → 不误判失联；流连着但心跳停 → 仍按 TTL 判失联。
- 双端测试通过，注释/日志中文，无硬编码。

## 6. 仍开放（实现期定）

- SSE 事件是"只发通知触发拉取" vs "通知带最小必要数据" 的细粒度（默认前者，最小改动）。
- 重连退避参数、SSE 读超时/心跳保活间隔默认值。
- 控制面 per-conn 扇出的并发模型（每连接 goroutine + channel）。
