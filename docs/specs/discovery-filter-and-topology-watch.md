# 功能规格：发现接口过滤 + 拓扑 watch 订阅

> 状态：开发中　·　关联 PRD：FR-29（增强 FR-4/FR-16，复用 FR-24 SSE 推送流）　·　分支：feature/fr-29-discovery-watch

## 1. 背景与目标

FR-4 的服务发现只支持按 namespace/group/zone/role 过滤、且是一次性 HTTP 拉取；下游（尤其 BungeeCord 代理）要维护"同 namespace 在线子服目录"时，只能轮询发现端点，既拿不到自定义元数据维度的筛选，也感知不到拓扑变化（上线/下线/改派）的实时性。

本功能在不引入新连接、不另起长轮询的前提下，做两件事（P2）：

1. **发现过滤增强**：discovery 端点与 agent-api SDK 支持按 `role` / `zone` / `tag`（自定义 metadata 键值）过滤。
2. **拓扑 watch**：实例上线/下线/改派 zone 时，经 FR-24 已有的单条 SSE 推送流即时通知订阅方（新增 `topology-changed` 事件），订阅方据此重查发现端点。

## 2. 需求（要什么）

- 范围内：
  - discovery 查询新增 `tag` 维度：按 metadata 键值精确匹配（多 tag 取交集），与既有 role/zone/group 维度叠加。
  - SSE 推送流新增 `topology-changed` 事件：namespace 内任一实例**进入或离开"可用集合"（online+degraded）**或**改派 group/zone**时推送；未变更不推（拓扑摘要幂等去重）。
  - agent-api SDK：`DiscoveryQuery` 增 `tag` 维度；`Discovery` 增 `watch(listener)` 订阅 API，控制面不可用/未注入流时回退（`isAvailable` 语义，返回不可订阅句柄）。
- 不做（范围外）：
  - 不在事件里搬实例数据（事件仅"拓扑变了"信号，订阅方自行重查发现端点）——守控制面/数据面边界。
  - 不新增连接、不另起长轮询循环——拓扑 watch 作为 FR-24 单条 SSE 流的又一类事件消费者接入。
  - 不碰玩家连接、不做跨服玩家行为（属业务插件）。

## 3. 设计（怎么做）

不涉及新架构决策（复用 FR-24 SSE 流 [ADR-0015] + 既有发现），不新增 ADR。

### 控制面

- `runtime.Filter` 增 `Tags map[string]string`；`matches()` 增 tag 全匹配判定（metadata 须含全部 k=v）。
- 新增 `topologyHub *longpoll.Hub`（namespace 级唤醒），与配置/文件 Hub 同构、独立锁。
- 新增 `runtime.TopologyDigest(insts)` 纯函数：对"可用集合"按 serverId 字典序拼 `serverId|role|group|zone|status|address`，复用 `merge.MD5Hex` 取摘要。仅拓扑相关字段入摘要，playerCount/tps/心跳时间等不入（不触发无谓推送）。
- `ChangeNotifier` 增 `NotifyTopologyChange(ns)`：唤醒 `topologyHub` 该 namespace 全部 waiter。四处拓扑变更点调用：
  1. 实例注册成功（`InstanceService.Register`）
  2. 手动下线（`InstanceService.Offline`）
  3. 健康扫描转 lost/offline（`HealthScanner` 离开可用集合）
  4. zone 改派/取消（`ZoneService.Assign`/`Unassign`）
- `StreamService` 增第三个 waiter（topologyHub，namespace 级）+ 拓扑摘要对账：连接即对账（reported 携带 topologyDigest）补一次 `topology-changed`、转直播按摘要"真变才推"。事件 data 载荷为新摘要（通知式，不含实例数据）。
- `sse` 增 `EventTopologyChanged = "topology-changed"`，`encodeData` 复用 changedPayload（md5 字段承载摘要）。
- discovery 端点解析 `tag.<key>=<value>` 重复查询参数为 Tags。

### agent 侧（SDK）

- `DiscoveryQuery` 增 `tags`（Map）维度 + Builder；`BeaconApiClient.discover` 增 tags 入参，拼 `tag.<k>=<v>`。
- `StreamEventTypes` 增 `TOPOLOGY_CHANGED`；`AgentLifecycle` 流分发 + 上报增 topology 维度；收到事件触发已注册的拓扑监听器回调（回调里业务侧自行重查 discovery）。
- `Discovery` 增 `watch(TopologyListener): ListenerHandle`：注入流时注册到生命周期、返回可取消句柄；未注入流（回退态）返回 no-op 句柄并标注不可用。

## 4. 任务拆分

- [ ] 控制面：Filter.Tags + matches tag 匹配（单测先行）
- [ ] 控制面：TopologyDigest 纯函数（穷举单测）
- [ ] 控制面：topologyHub + NotifyTopologyChange + 四处变更点接线
- [ ] 控制面：StreamService 拓扑 waiter + 摘要对账/直播（单测：上线/下线/改派推、未变不推）
- [ ] 控制面：discovery 端点 tag 解析
- [ ] agent-api：DiscoveryQuery.tags、Discovery.watch、TopologyListener、ListenerHandle 复用
- [ ] agent-core：discover tags、StreamEventTypes、Lifecycle 拓扑事件分发与监听器、DiscoveryView.watch
- [ ] 文档同步：PRD 状态（已预置开发中，不改）、ARCHITECTURE、API、CHANGELOG 末尾追加

## 5. 验收标准

- 控制面 `go test ./internal/...` 全绿，含：
  - tag 过滤命中与排除（多 tag 交集、缺键排除）。
  - TopologyDigest：同拓扑同摘要、改派/上下线变摘要、非拓扑字段（playerCount）变不变摘要。
  - StreamService：连接即对账补 topology-changed；直播在上线/下线/改派时推、未变更不推。
- agent `gradle test` 全绿，含 discover tags 拼参、watch 注入态/回退态行为。
- 真机/集成（compose 起 beacon + agent + bungee 观察目录随拓扑实时刷新）：本 worktree 无环境，标"待真机/集成验"。

## 6. 风险 / 待定

- 拓扑 watch 是 namespace 级广播：大规模实例下每次变更唤醒全 namespace agent 重查发现，可接受（发现查询是内存读、O(n)）；如需进一步降噪可后续按 group 细化唤醒集合（当前不做，避免镀金）。
