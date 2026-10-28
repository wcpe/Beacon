# 功能规格：agent 调度门面暴露共享游戏池能力

> 状态：开发中　·　关联 PRD：FR-281　·　分支：feature/fr-281-lobby-shared-pool-scheduling　·　依赖：FR-199（LobbyCluster 权威模型）/ FR-225（入口服作为大厅集群成员纳入统一调度）/ FR-244 + ADR-0087（准入作用域与三态结论）/ ADR-0004（zone 权威）/ ADR-0096（拆池决策）　·　纯后端（无 UX 段）

## 1. 背景与目标

### 1.1 一个池装不下两种语义

`LobbyCluster` 从 FR-199 起是 namespace 唯一的池，它的原始用途只有一个：**BC 首次落脚**——玩家连入代理时，从池里挑一台「等待大厅」把他放进去。这条链路是 [InitialLobbyRouter](../../apps/agent/agent-core/src/main/kotlin/top/wcpe/beacon/agent/core/proxy/InitialLobbyRouter.kt)，纯本地路由，只读候选快照的 `lobby` 段与受管目录。

但下游业务（AllinGame）需要的是另一件事：一组**跨区共享的游戏服**，派房时选中一台就开游戏房。它复用了同一个池——于是两种互斥用途撞在一起：

- BC 落脚只按「成员集合」过滤，**没有字段能区分「等待大厅」与「游戏服」**，末段还是按分数 + 负载 + 随机的 `randomIndex` 取一台；
- 若为业务把 game 服放进池，玩家从 BC 进来就会被**直接丢进 game 服**；
- 若只放入口服，业务的共享池派房就会选中**入口服来跑游戏房**。

这不是配置问题，是模型缺一层：同一个池无法同时表达两种语义。**拆成两个池是唯一正解**（决策记录见 [ADR-0096](../adr/0096-split-lobby-cluster-and-shared-game-pool.md)）。

### 1.2 拆完之后的形态

| 池 | 语义 | 用途 | 成员归属列 |
|---|---|---|---|
| `LobbyCluster` | 入口大厅 | BC 首次落脚（`InitialLobbyRouter` 专用） | `Server.LobbyClusterID` |
| `SharedPool` | 共享游戏池 | 业务派房：跨区共享的游戏服池 | `Server.SharedPoolID` |

两者各自 **namespace 唯一**，且 `Server` 归属**四选一互斥**（`ZoneID` / `LobbyClusterID` / `SharedPoolID` / 无归属），所以成员集合天然不重叠——这是「玩家从 BC 进来不会被丢进游戏服」的**机制级**保证，不依赖运维约定。

### 1.3 本规格补的是门面

控制面与 agent 的数据管道早已能表达池（`candidates` 下发池段、`decide` 支持池作用域），但业务插件侧的 `BeaconScheduling` 门面上缺对应入口，调用方只能自己拼假 zone 名（控制面按小区查，得到 `zone_not_found` 这种口径不符的假结论），或绕开 Beacon 自行选服、把健康 / 容量 / 排空判定全部丢掉。

本规格把**两个池的能力**都补到门面上：入口大厅 4 条 + 共享游戏池 4 条，均为 `default`，既有实现不重写也能编译。**控制面与协议只做加性扩展**——新增 `scope=shared_pool` 与 `shared` 候选段，不改既有 `zone` / `lobby` 语义。

## 2. 需求（要什么）

### 2.1 做什么

1. **门面新增 8 条方法**：入口大厅 4 条（`acquireCandidateInLobby` / `candidatesInLobby` 各两条重载）+ 共享游戏池 4 条（`acquireCandidateInSharedPool` / `candidatesInSharedPool` 各两条重载）。全部为 `default`，agent-api 仍是纯 Java 8、零三方依赖。
2. **缺省实现失败关闭**：带参重载抛 `UnsupportedOperationException`（"能力缺失"最响的一档），无参重载委派到带参重载。**不得**改成返回空表或忽略作用域——那会把"本实现不提供该能力"伪装成"池里没有机器"。
3. **下发口径**：共享池档走 `scope=shared_pool`，请求体带 `scope` 且**不带 `zone` 键**（该档 zone 必须为空，控制面 400 兜底）。小区档请求体逐字不变（缺省不下发 `scope` 键）。
4. **读候选的抛出契约**：本帧**没有该池模型**（尚无快照，或本帧来源是未下发该段的旧控制面）⇒ **抛 `IllegalStateException`**（当前状态、可重试），**绝不返回空表**；池在但确实没有可调度成员 ⇒ 返回**空表**（稳定事实）。作用域非空而候选缺自声明标签字段 ⇒ 同样抛 `IllegalStateException`。
5. **取候选的三态结论**：`CHOSEN` / `NO_CANDIDATE`（稳定事实）/ `UNAVAILABLE`（当前状态、可重试），词表与 zone 档**同一套**。`CHOSEN` 时 `CandidateView.zone()` 为**空串**（池成员不属于任何小区）。
6. **控制面 4xx 一律归 `UNAVAILABLE`**：池档的"问到了"只有一条路（200；池空也是 200 + 无候选），故 `400` / `403` / `404` 都是**没问成**，`failReason` 原样带出对端码。
7. **fail-static 降级**：控制面不可达时用本地快照的**对应池段**决策，future 仍正常完成；排序 / 收窄 / 三态判定与小区档**同一个纯函数**（不另造一套排序）。
8. **`healthOf` 覆盖三处**：查询须覆盖「所在小区」「入口大厅池」「共享游戏池」——池成员没有 zone，只按小区找会让它的 `healthOf` 恒为 `null`。
9. **两池严格隔离**：共享池成员不出现在 `lobby` 段，大厅成员不出现在 `shared` 段。控制面按各自归属列过滤，agent 按各自段落盘。
10. **BC 首次落脚链路一行不改**：它继续只读 `lobby` 段与 `lobbyClusterMember()`；拆池对它是纯收益（池里不再混入游戏服）。

### 2.2 明确不做（范围外）

- **不改 BC 落脚语义**：`InitialLobbyRouter` 的判定分级、受管目录校验、`ready` 参与判定全部保持原样。
- **不新造协议方言**：不引入 `zone="__shared__"` 这类假 zone 名，也不引入第三个决策端点。
- **不把池决策写进降级补报队列**：`report-local` 的校验要求 `zone` 非空，而两个池的决策都没有 zone；塞进去会让该批 400、刷新循环把整批退回队列，从而**堵住后续所有小区档补报**。代价是池档降级决策不落控制面决策日表（返回值与 traceId 仍可读，日志留痕）。
- **不解释池语义**：Beacon 不定义"游戏服该具备什么标签"，作用域键值的业务含义仍由接入方自持。
- **不碰 zone 权威**：池成员归属仍由控制面权威模型决定，agent 侧不从小区推断。

## 3. 设计（怎么做）

### 3.1 控制面（apps/server）

- **模型**：`SharedPool` 表（`namespace_id` 唯一索引），`Server.SharedPoolID` 列。归属四选一在 `placementKindOf` 统一判定，同时置两个归属返回 `409 SERVER_PLACEMENT_CONFLICT`。
- **调度**：`SchedScopeSharedPool = "shared_pool"`；`sharedPoolViews` 与 `lobbyViews` 各自按归属列过滤（`SharedPoolID` vs `LobbyClusterID`）；`Candidates` 返回 `shared` 段。
- **健康投影**：`HealthFact` / `healthview.View` 增加 `SharedPoolID` 与 `NamespaceSharedPoolID`，与大厅对应字段并列。
- **告警角色**：新增 `RoleSharedPool`。与 `RoleLobby` 分级相同（都是玩家链路关键节点），但角色分开——把共享池故障标成「大厅故障」会把运维引向错误方向。

### 3.2 agent-api（契约为准）

两个池各 4 条 `default` 方法，契约正文（三态、抛出契约、向后兼容方向）即方法 Javadoc，是唯一真源。入口大厅那组与共享池那组**语义逐条对应**，差别只在池的用途与线上作用域值。

### 3.3 agent-core

- **候选池抽象**：`CandidatePool`（`viewZone` / `entries` / `missingNote` / `reportable`）把差异收在一处；`zonePool` / `lobbyPool` / `sharedPool` 三个工厂分别构造。收窄 / 排序 / 三态判定全复用 `admittedBy` 与 `localFallback`。
- **缓存读入口**：`poolEntries(PoolSection)` 按段参数化，`null` / 非 null 区分「没有该池模型」与「池里没有成员」；`findAnywhere` 依次查小区段 → 大厅段 → 共享池段。
- **下发**：`scheduleDecide(…, scope: String?)`，`null` 时请求体逐字不变；池档传对应作用域常量且 `zone` 传空串。
- **落盘**：快照新增 `shared` 段，写入时 null 不落键、读取时缺键置 null（无版本号，靠字段可缺省保持兼容）。

### 3.4 三处易混口径（实现必须分开处置）

| 处境 | 读候选 | 取候选（降级路径） | 取候选（控制面 4xx） |
|---|---|---|---|
| 没有该池模型（旧控制面 / 无快照） | 抛 `IllegalStateException` | `UNAVAILABLE` `unavailable` | — |
| 模型在、池里没有成员 | 空表 | `NO_CANDIDATE` `no_candidate` | — |
| 作用域把成员全滤掉 | 空表 | `NO_CANDIDATE` `no_candidate_in_scope` | — |
| 候选缺自声明标签字段且作用域非空 | 抛 `IllegalStateException` | `UNAVAILABLE` `admission_scope_unavailable` | — |
| 控制面拒绝本次请求（4xx） | — | — | `UNAVAILABLE` + 对端码 |

## 4. 任务拆分

- [x] 控制面模型：`SharedPool` 表 + `Server.SharedPoolID` + AutoMigrate + 存量回填 + namespace 创建时成对建池
- [x] 控制面归属：四选一互斥 + 迁移状态机 + 审计 action + 摘要/成员查询参数化
- [x] 控制面调度：`SchedScopeSharedPool` + `sharedPoolViews` + 候选快照 `shared` 段
- [x] 控制面健康投影：`HealthFact` / `healthView` 增加两个字段
- [x] 控制面 HTTP 与告警：`/admin/v2/shared-pools` 端点 + `shared` 响应段 + `RoleSharedPool`
- [x] agent-api：8 条 `default` 方法 + API 基线
- [x] agent-core：`PoolSection` / `poolEntries` / `findInPool` / `sharedPool` 工厂 / 4 条方法实现
- [x] agent-core：快照 `shared` 段解析与落盘
- [x] agent-core：`UnavailableScheduling` 覆写 8 条
- [x] 单测：控制面 4 个 FR-281 用例 + agent `SchedulingSharedPoolTest` 12 例
- [x] 文档：本规格、PRD、CHANGELOG、API.md、ADR-0096

## 5. 验收标准

- `apps/agent` 的 `./gradlew ktlintCheck detekt test` 与 `:agent-api:apiCheck` 全绿；`apps/server` 的 `go build ./... && go test ./...` 全绿。
- **拆池的核心保证**（`TestFR281SharedPoolCandidatesDoNotLeakIntoLobby`）：同一帧里 lobby 段只含入口大厅成员、shared 段只含共享池成员，两集合交集为空。
- **归属互斥**（`TestFR281ServerPlacementIsExclusiveAcrossThreePools`）：小区 → 共享池 → 入口大厅的迁移链上，每次落地后只有目标归属非空，其余全部清空。
- **作用域形状**（`TestFR281SharedPoolScopeRejectsZone`）：`scope=shared_pool` 带非空 zone 返回 400。
- 自测覆盖并可判别的判据：
  - 本帧无 shared 段 ⇒ `candidatesInSharedPool` **抛** `IllegalStateException`（不是空表）；池在而池空 ⇒ 返回空表。
  - 作用域收窄：空作用域不收窄、非空只留满足者；作用域非空而候选缺标签字段 ⇒ 抛 `IllegalStateException`。
  - 共享池决策请求体带 `scope=shared_pool` 且**不带** `zone`；小区档请求体不含 `scope` 键。
  - `CHOSEN` 时 `CandidateView.zone()` 为空串，`level` / 在线人数从**共享池段**补全。
  - 控制面 4xx ⇒ `UNAVAILABLE`，不得读成 `NO_CANDIDATE`；池空（200 + 无候选）⇒ `NO_CANDIDATE`。
  - 控制面不可达 ⇒ 本地降级仍能出 `CHOSEN`，且不把池决策塞进补报队列。
  - `BeaconScheduling` 缺省实现（只实现 6 个抽象方法）调用 8 条池方法均抛 `UnsupportedOperationException`。
  - `healthOf` 能查到只存在于共享池段的成员。
- **插件壳自证**：重新构建 `agent-bukkit` 插件 jar 后，`javap` 该 jar 内的 `top.wcpe.beacon.agent.api.BeaconScheduling` 必须命中 8 条 `...InLobby` / `...InSharedPool` 方法。

## 6. 风险 / 待定

- **池降级决策不入补报队列**（见 §2.2）：降级期"池里选了谁"只留 agent 日志与返回值。若将来要可查，正解是让控制面 `report-local` 接受显式的 scope 标记，而不是往 `zone` 里塞假值。
- **detekt 阈值口径变更的副作用**：`TooManyFunctions.ignoreOverridden: true` 让所有类的 override 不再计入，本仓的上帝类把关实际上由人工评审与分层规则承担。
- **`CandidatePool` 的函数数量约束**：`SchedulingCache` 已逼近 `TooManyFunctions` 阈值，故池段读写按 `PoolSection` 参数化而不是各写一套同构方法。将来再加第三个池时仍需沿用这条约束。
- **AllinGame-74 的过渡期**：它目前直引 `candidatesInLobby`（入口大厅）。拆池后它拿到的是入口大厅服，**不是游戏服**——必须切到 `candidatesInSharedPool` 才符合它的语义。切换前该缺陷仍存在，发布节奏需明确。
- **`ready` 字段未参与 agent 判定**：控制面 `ready` 等价于"池里有可调度成员"，与"候选表是否为空"同源；agent 侧一律以候选表为准，避免同一事实两处取值不一致。
