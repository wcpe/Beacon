# 功能规格：子服关闭后的按区回退与登录池随机

> 状态：已交付@v1.6.0　·　关联 PRD：FR-282　·　关联 ADR：待评估（见 §8）

## 1. 背景与目标

玩家已在某个子服内游玩时，该子服进程关闭或崩溃，当前表现是被直接断开回登录界面，而不是被送回大厅继续游戏。

根因在 BungeeCord 侧：`DownstreamBridge.disconnected()` 调 `updateAndGetNextServer()`，该方法取 `listener.getServerPriority()` 建队列、**跳过当前服**、按配置顺序返回下一个可用服。线上 `config.yml` 的两个 listener 的 priorities 都只写了 `[lobby-1]`：

- 玩家在 `lobby-1` 上时，唯一候选等于当前服被跳过 → 队列空 → 回退失败 → 断开；
- 即使配了多个服，BC 也只有**顺序**没有**随机**；
- 且 `priorities` 挂在 listener 上，**拿不到 zone 感知**，无法区分玩家原所在区。

目标：子服关闭时把玩家可靠送回大厅，且回退目标符合玩家原所在区、登录池内随机分摊。

## 2. 范围

**做**：

- 新增 agent 侧回退选路：三级优先级（同区大厅 → 全局登录池 → 放行原生）。
- 在代理侧接管 `ServerKickEvent`（`State.CONNECTED`）与 `ServerConnectEvent`（`SERVER_DOWN_REDIRECT`），覆盖「踢人」与「进程消失」两条路径。
- 单元测试覆盖三级降级、随机性、排除当前服、快照缺失不阻断。
- 真机 E2E 覆盖三条路径（详见 §6）。
- 文档：PRD 登记、本 spec、CHANGELOG。

**不做**：

- 不改 BungeeCord 原生 `priorities` 的语义与排序，也不要求运维重排它。
  但有一条部署前提：`priorities` 须**覆盖全部大厅服**（详见 §4.4）——只配一台时崩溃路径会静默退化为原生断开。
- 不做跨 namespace 回退（回退目标必须同 namespace）。
- 不做玩家主动 `/server` 换服的路由干预（那是显式意图，不属"被动回退"）。
- 不做负载均衡的全局重排（仅回退这一个场景消费选路结果）。
- 不改控制面任何状态：决策发生在 agent 数据面，只读内存快照。

## 3. 回退优先级（本规格的核心契约）

按顺序逐级降级，**任一级命中即停止**：

### 3.1 第一级：同区大厅

- 取玩家原所在服的 `zone`（取自受管目录快照 `directory.byServerId[fromServerId].zone()`）。
- 在该 zone 的候选集中筛选：`schedulable` 为真、且该 serverId 在已发布的受管目录内（避免把玩家送到本代理转发不到的服）。
- 再与 `directory.lobbyMemberIds` 取交集——**必须是大厅簇成员**。
- 排除刚关闭的那个服。
- 命中非空 → 按 §3.4 选一个。

### 3.2 第二级：全局登录池

- 取快照里的 lobby 池（**跨区**），要求 `pool.ready` 为真。
- 筛选 `schedulable` 且在受管目录内，排除刚关闭的服。
- 命中非空 → 按 §3.4 选一个。

### 3.3 第三级：放行原生（兜底）

前两级都选不到 → 返回 `Unavailable`，**不取消事件**，交还 BungeeCord 原生 `priorities` 回退。

这条的语义**在两条路径上不同**，不可一概而论：

| 路径 | 前两级都选不到时的实际结果 |
|---|---|
| 踢人路径（`ServerKickEvent`） | 不取消事件，BungeeCord 按原生行为**断开玩家**（这是它本来就会做的事，本能力未加剧） |
| 崩溃路径（`ServerConnectEvent`） | 不改写 `target`，BungeeCord 继续用它自己算出的原生回退目标 |

即：硬约束是「本能力**不新增**断线原因」，而非「保证玩家在任何情况下都不掉线」。

### 3.4 选服打分（同分随机）

用于 §3.1 与 §3.2：

1. 取 `score` 最高的一组；
2. 组内取 `occupancy` 最低的（`maxOnline <= 0` 视为 1.0）；
3. 仍有并列 → 按 serverId 排序后，用**可注入的随机源**在并列内选。

随机源以函数参数注入（`randomIndex: (Int) -> Int`），使单测可确定性验证，运行期用 `Random.nextInt`。

## 4. 触发时机与边界

### 4.1 BungeeCord 的两条断开路径（源码取证，`DownstreamBridge`）

「玩家离开子服」在 BungeeCord 里有两条互斥路径，**只接其一必然漏场景**：

| 路径 | 触发条件 | 触发事件 | 玩家此刻的连接状态 |
|---|---|---|---|
| `handle(Kick)` | 子服**主动发 Kick 包**（插件踢人、正常 `stop` 时的 "Server closed"） | `ServerKickEvent`（`State.CONNECTED`） | 尚未断开 |
| `disconnected(ChannelWrapper)` | 子服**进程消失 / 连接断**（崩溃、被 kill、网络断） | 无 kick 事件；BungeeCord 自行发起 `connectNow(def, SERVER_DOWN_REDIRECT)` | **尚未断开** |

进程消失这条路径**不产生 `ServerKickEvent`**，但 BungeeCord 会在同一方法内发起一次原生跳转，
该跳转会触发公开事件 `ServerConnectEvent`（`Reason.SERVER_DOWN_REDIRECT`）。本规格即在该事件上接管，
替换 `target` 实现按区回退。这是能覆盖「进程崩溃」的依据。

**纯 `ServerKickEvent` 实现覆盖不到进程崩溃**——这是真机验收时实测确认的缺口，不是推测。

### 4.2 为什么用 `ServerConnectEvent.setTarget` 而不是改内部状态

两条路径最终都收敛到同一个**公开**事件 `ServerConnectEvent`：

| 来源 | `Reason` |
|---|---|
| 子服崩溃后的原生回退 | `SERVER_DOWN_REDIRECT` |
| `ServerKickEvent` 里设 `cancelServer` 之后的跳转 | `KICK_REDIRECT` |

该事件实现 `Cancellable` 且 `target` 可写，直接替换目标即可。

**不得使用内部类/反射**：内部 `ServerConnection.setObsolete(true)` 走反射虽能实现，
但依赖非公开契约（内部类不在 `bungeecord-api` 里，模块只能 `compileOnly` 该 API），
升级即碎。本规格明确禁止该做法。

用 `Reason` 过滤还天然排除了玩家主动 `/server`（`COMMAND`）与插件跳转（`PLUGIN`）。

### 4.3 如何与「玩家主动换服 / 主动退出」区分

| 情形 | 判据 | 是否接管 |
|---|---|---|
| 玩家主动 `/server` 换服 | 走 `Reason.COMMAND`，不匹配 `SERVER_DOWN_REDIRECT` | 否 |
| 插件跳转 | 走 `Reason.PLUGIN` | 否 |
| 子服崩溃 / 进程消失 | 原生回退发出 `Reason.SERVER_DOWN_REDIRECT` | **是** |

### 4.4 已知边界：`priorities` 必须覆盖全部大厅服

原生回退只在「`priorities` 里还能找到第二个候选」时才发起跳转。若 `priorities` 只配了
玩家当前所在的那一台，`updateAndGetNextServer` 返回 null，BungeeCord 直接
`con.disconnect(lost_connection)`、**不产生任何事件**，本能力无从接管。

故部署要求：`listeners[].priorities` 覆盖全部大厅服（与多大厅容灾的运维惯例一致）。
该场景的兜底是原生行为（断开），不比现状更差。

### 4.5 选出的目标本身也不可用时

不是死循环，理由在 BungeeCord 侧：

- `SERVER_DOWN_REDIRECT` 只由**真实服务端连接的** `disconnected()` 触发。连接一个已宕机的服根本建立不起连接，不会再次触发该事件。
- 连接失败走 `ServerConnector.exception`：仅当 `user.getServer() == null` 才 `disconnect`，否则只给玩家一条错误提示、保留当前状态。

因此最坏结果是「玩家停在原处收到一条连接失败提示」，与原生死服时的表现一致，不会因本能力产生重定向风暴。
（早期评审曾担心排除集不含"已尝试过的大厅"会反复选中同一台，经源码核对该路径不会形成循环，故不引入重定向计数上限。）

### 4.6 其余边界

| 情形 | 是否接管 | 原因 |
|---|---|---|
| `ServerKickEvent` + 非 `CONNECTED` State | 否 | 连接阶段失败由 BC 自身重试链负责，接管会与原生逻辑重复 |
| 事件已被取消 | 否 | 尊重既有处置 |
| `kickedFrom` / `target` 为空 | 否 | 无从判断原服，无法做区感知 |
| router / directory 未装配 | 否 | fail-static，不阻断 |

## 5. 设计（实现落点）

本 FR 将 `ReconnectRouter` 与 `BungeeReconnectListener` 收口入库，并补齐文档与测试（初版实现游离于版本控制之外，且只接管了 `ServerKickEvent`，见 §4.1）。

- **agent-core**：`proxy/ReconnectRouter.kt`——纯逻辑，只读 `ManagedDirectorySnapshot` 与 `CandidateSnapshot`，不做网络/磁盘/平台调用。不违反「控制面不写游戏逻辑」边界（ADR-0017 决策与执行分离）。
- **agent-bungee**：`BungeeReconnectListener.kt`——订阅 `ServerKickEvent`（设 `cancelServer` + `isCancelled`）
  与 `ServerConnectEvent`（`Reason.SERVER_DOWN_REDIRECT` 时替换 `target`）。全部走公开 API，无反射。
  - 决策部分抽为 `ReconnectDispatch.kt`：`forKick` / `forConnect` 只吃事件枚举与字符串（`route` 由调用方注入），
    不碰需要代理实例才能构造的平台对象。这样「接哪些事件」「与原生选择相同时不写回」才能在无平台环境下被单测锁住，
    handler 也随之退化为「读事件 → 问决策 → 写事件」。
  - `BungeeServerDirectory.kt` 提供 `ManagedServerLookup` 函数式接口（`managedServerInfo`），
    让监听器不直接依赖那个会摸 `ProxyServer` 单例的实现，从而可注入替身。装配调用点不变。
- **测试落点**：`agent-bungee` 此前无测试源集，本 FR 首次引入——
  `ReconnectDispatchTest`（接管范围 11 例）与 `ReconnectDispatchListenerTest`（事件写入层 8 例，
  含 `cancelServer` / `isCancelled` 必须成对设置的回归防护）。平台对象用 `support/` 下的手写替身
  （`FakePlayer` / `FakeServer` / `FakeServerInfo`）构造，事件用 BungeeCord 真实类，不引入 mock 框架与反射。
  选路算法本身由 `agent-core` 的 `ReconnectRouterTest` 覆盖，两侧不重复。
- **装配**：`BeaconAgentBungee` 在注册后启动监听器，注入 `ReconnectRouter(directorySyncer::snapshot, assembled.lobbySnapshotProvider)`。
- **不动的边界**：agent-core 保持 TabooLib-free（ADR-0005）；选路是纯函数，平台 API 只在 bungee 壳内出现。

## 6. 验收标准

### 6.1 单元测试（agent-core）

- 三级优先级各自命中时的返回值与 `Target` 标记正确；
- 同区命中时不落到登录池；
- 同区无大厅成员时降级到登录池；
- 两级都空 → `Unavailable`；
- 排除集合含当前服时不会被选中；
- 快照为 null / `pool.ready=false` 时不阻断；
- 并列候选的随机源可注入（同一输入 + 固定随机源 → 结果确定）。

### 6.2 真机 E2E（依赖 FR-283 的 bot 设施）

三条路径都要在真机跑，**单测绿不算通过**：

| 路径 | 断言 |
|---|---|
| 同区回退 | bot 进入子服 → 该子服关闭 → bot 落回**同区大厅**，未断开 |
| 登录池回退 | 同区大厅不可用时 → bot 落到**登录池**内的某个大厅服 |
| 兜底不阻断 | 两级都无可用 → 事件放行，玩家不被本功能阻断 |

**随机性验证口径**（按用户明确要求，不做统计分布）：断言选出的目标**属于候选集**，且**不恒等于候选集首个**（用可注入随机源或多次调用观察非恒定性）。不引入统计学阈值，避免偶发假阳性。

### 6.3 文档

- `docs/PRD.md` §4 登记 FR-282；
- 本 spec 定稿；
- `CHANGELOG.md` 增加条目。

## 7. 依赖与执行

- **无前置依赖**，可与 FR-283 并行开发。
- 但 §6.2 的 E2E 验收**必须等 FR-283 落地**后才能执行——FR-283 提供 bot 编排与多后端拓扑能力。

## 8. 待定 / 需评估

- 是否需要 ADR：本能力引入「agent 数据面在掉线时做路由决策」，与 ADR-0017（决策与执行分离）方向一致，但属于既有决策的**新执行点**。倾向不新增 ADR，在 spec 内说清边界即可；若评审认为构成新架构决策，再补。
- 回退目标是否应受 `draining` 影响：当前复用 `schedulable`，若 `schedulable` 已含 draining 判定则无需额外处理，实现时确认。
