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
- 在代理侧接管 `ServerKickEvent`（仅 `State.CONNECTED`）执行该选路。
- 单元测试覆盖三级降级、随机性、排除当前服、快照缺失不阻断。
- 真机 E2E 覆盖三条路径（详见 §6）。
- 文档：PRD 登记、本 spec、CHANGELOG。

**不做**：

- 不改 BungeeCord 原生 `priorities` 语义，也不要求运维改 `config.yml`——本能力是叠加在其上的更优选择，选不到时原逻辑照常生效。
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

这条是硬约束：本能力**绝不成为玩家断线的新的原因**。

### 3.4 选服打分（同分随机）

用于 §3.1 与 §3.2：

1. 取 `score` 最高的一组；
2. 组内取 `occupancy` 最低的（`maxOnline <= 0` 视为 1.0）；
3. 仍有并列 → 按 serverId 排序后，用**可注入的随机源**在并列内选。

随机源以函数参数注入（`randomIndex: (Int) -> Int`），使单测可确定性验证，运行期用 `Random.nextInt`。

## 4. 触发时机与边界

| 事件 | 是否接管 | 原因 |
|---|---|---|
| `ServerKickEvent` + `State.CONNECTED` | **是** | 玩家已在子服内，该服异常断开——本规格的目标场景 |
| `ServerKickEvent` + 其他 State | 否 | 连接阶段的失败由 BC 自身重试链负责，接管会与原生逻辑重复 |
| 事件已被取消 | 否 | 尊重既有处置 |
| `event.kickedFrom` 为空 | 否 | 无从判断原服，无法做区感知 |
| router / directory 未装配 | 否 | fail-static，不阻断 |

## 5. 设计（实现落点）

现状：`ReconnectRouter` 与 `BungeeReconnectListener` **已实现但未纳入版本控制**（`git status` 显示为未跟踪）。本 FR 负责收口入库并补齐文档与测试。- **agent-core**：`proxy/ReconnectRouter.kt`——纯逻辑，只读 `ManagedDirectorySnapshot` 与 `CandidateSnapshot`，不做网络/磁盘/平台调用。不违反「控制面不写游戏逻辑」边界（ADR-0017 决策与执行分离）。
- **agent-bungee**：`BungeeReconnectListener.kt`——订阅 `ServerKickEvent`，命中则设 `event.cancelServer` 并置 `isCancelled`。
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
