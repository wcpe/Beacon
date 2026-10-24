# ADR-0096：入口大厅与共享游戏池必须分成两个池

**状态**：已接受（2026-10-24）

## 背景

`LobbyCluster`（FR-199 起）是 namespace 唯一的池，成员由 `Server.LobbyClusterID` 表达。它的原始且唯一用途是 **BC 首次落脚**：玩家连入代理时，从池里挑一台「等待大厅」把他放进去（[InitialLobbyRouter](../../apps/agent/agent-core/src/main/kotlin/top/wcpe/beacon/agent/core/proxy/InitialLobbyRouter.kt)，纯本地路由）。

下游业务（AllinGame）需要的是另一件事：一组**跨区共享的游戏服**，派房时选中一台就开游戏房。它复用了同一个池，通过 `BeaconScheduling` 的池作用域读取。

## 问题

两种用途的成员集合撞在一起，且**没有任何字段能区分**：

1. BC 落脚只按「成员集合」（`Server.LobbyClusterID`）过滤，末段是「按分数 → 负载 → serverId 排序后 `randomIndex` 取一台」。若池里混入 game 服，玩家从 BC 进来会被**随机丢进游戏服**。
2. 反过来，若池里只放入口服，业务的共享池派房会选中**入口服来跑游戏房**。
3. 下游自己的文档已承认这个风险：「池成员范围由部署时的归属决定……若把入口节点也一并归进去，它们同样是候选。本仓不另加一层成员过滤：那会再占一条标签配额，而"谁进池"本来就是运维的归属决定。」

也就是说：**两侧都没有强制，全靠运维约定**。而约定错了，表现是「玩家落到错误的服务器类型」这类不可静默的线上事故。

## 决策

**拆成两个池，各自 namespace 唯一，`Server` 归属四选一互斥。**

| 池 | 语义 | 用途 | 归属列 |
|---|---|---|---|
| `LobbyCluster` | 入口大厅 | BC 首次落脚专用 | `Server.LobbyClusterID` |
| `SharedPool`（新增） | 共享游戏池 | 业务派房：跨区共享的游戏服池 | `Server.SharedPoolID` |

配套决定：

- **归属四选一**：`ZoneID` / `LobbyClusterID` / `SharedPoolID` / 无归属，同时置两个返回 `409 SERVER_PLACEMENT_CONFLICT`。成员集合因此**天然不重叠**，正确性不依赖运维约定。
- **调度作用域三档**：`zone` / `lobby` / `shared_pool`。池档都要求 `zone` 为空。
- **候选快照三段**：`zones` / `lobby` / `shared`，各自独立解析与落盘。
- **门面能力对称**：`...InLobby` 与 `...InSharedPool` 各 4 条，语义逐条对应。
- **BC 落脚链路一行不改**：它继续只读 `lobby` 段与 `lobbyClusterMember()`。拆池对它是纯收益。

## 备选方案与否决理由

**A. 给 `LobbyCluster` 加 `kind` 字段（`lobby` / `shared`），唯一索引改为 `(namespace_id, kind)`**

改动面更小，但 `Server.LobbyClusterID` 这个名字会长期误导（指向的可能是游戏服池），且每次读归属都得回表看 `kind` 才能判断语义。归属列本身无法表达「这台服在哪个池」，是模型层面的信息缺失。否决。

**B. 靠部署约定 + 标签过滤**

下游明确拒绝（会多占一条标签配额），且 Beacon 侧没有任何校验。两个消费方都靠约定等于没有约束，误配直接表现为送错人。否决。

**C. 复用同一池，靠 `Server.Kind` 或角色区分**

`Server.Kind` 只有 `backend` / `proxy` 两值，两者都是 `backend`；要用它区分就得再造一个维度，成本高于拆表。否决。

## 后果

**正面的**：

- 「玩家从 BC 进来不会被丢进游戏服」从**约定**变成**机制**：成员集合不重叠由归属互斥直接保证，有 `TestFR281SharedPoolCandidatesDoNotLeakIntoLobby` 与 `TestFR281ServerPlacementIsExclusiveAcrossThreePools` 锁住。
- 两侧语义各自清晰：入口大厅是基础设施（BC 自用），共享游戏池是业务资源（插件可读）。
- 告警角色可以分开（`RoleLobby` / `RoleSharedPool`），运维不会被「大厅故障」这种错误标签引偏。

**代价**：

- 控制面七层联动（模型 / 迁移 / 归属 / 调度 / 健康投影 / 审计告警 / HTTP）+ 30 余处测试需要同步，改动面大。
- `SchedulingCache` 已逼近 `TooManyFunctions` 阈值，故池段读写按 `PoolSection` 参数化而非各写一套同构方法；将来加第三个池需沿用该约束。
- **过渡期**：AllinGame-74 目前直引 `candidatesInLobby`，拆池后它拿到的是入口大厅服而非游戏服。必须切到 `candidatesInSharedPool` 才符合其语义；切换前该缺陷仍存在。
- 运维需要在部署时明确「这台服属于哪个池」——但这次选错会被归属互斥与候选段隔离挡住，而不是静默送错人。
