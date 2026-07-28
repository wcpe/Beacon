# ADR-0075：独立 LobbyCluster 与 BC namespace 首次大厅落脚

**状态**：已接受

## 背景

当前 v2 拓扑以 `BCCluster → Region → Zone → Server` 表达代理、大区、小区和后端。玩家第一次连接 BC 时却需要先进入 namespace 级全局大厅；大厅既不属于任何 Region/Zone，也不等同于业务插件进入小区后的默认服务器。

历史链路由 [ADR-0031](0031-zone-default-entry-and-bc-injection.md) 定义：控制面向 discovery 渲染 `zoneDefaultEntry`，BC 从本地 `proxy.home-group/home-zone` 选择一个小区默认入口，再把该 serverId 写入全部 listener 的 `server-priority`。这使 BC 本地空配置会直接失去入口，也把一次首次落脚与 Bungee 的全局 fallback 语义混在一起：后端断线、业务插件切服和管理员切服都可能被该 priority 影响。

产品现已明确：一台 BC 服务完整 namespace；Region/Zone 只是业务虚拟分隔。首次进入只应落到独立的全局 LobbyCluster，选区、排队、小区默认服务器和任何后续切服均由业务插件负责。控制面短暂不可用时，BC 要能继续用最后有效大厅快照；没有安全候选时必须明确拒绝，不能把玩家静默送到普通区服。

## 决策

### 1. 每个 namespace 有且仅有一个 DB 权威 LobbyCluster

新增 namespace 级 `lobby_cluster`，其唯一键为 `namespace_id`。创建 namespace 时在同一事务创建空 LobbyCluster；既有 namespace 的迁移只幂等补建空行，**不得**根据小区默认入口、在线顺序或任何启发式规则自动加入成员。LobbyCluster 为空是允许的配置中间态，但没有可调度成员即未就绪，不能通过 RC/生产入口验收。

`server.lobby_cluster_id` 表示 backend 的大厅归属。LobbyCluster 及其成员归属是低频、需审计的拓扑事实，真源为控制面 DB；注册、在线、玩家数、健康和容量仍是既有运行态，不能复制到大厅表，也不能把 BC 上报的受管目录反向当作大厅成员真源。

以下不变量由 service 在事务内校验，保持 GORM 与 SQLite/MySQL/Postgres 可移植，不增加跨表外键或方言 CHECK：

1. 每个 namespace 恰有一个 LobbyCluster；不得创建第二个、命名大厅或大厅策略。
2. 仅同 namespace 的 `kind=backend` 可设 `lobby_cluster_id`；proxy/BC 恒为 NULL。
3. backend 正式归属三选一：LobbyCluster、Zone 或未分配。`lobby_cluster_id != NULL` 时 `zone_id`、`pending_zone_id`、`bc_cluster_id`、`pending_bc_cluster_id` 均为 NULL，且 `is_default_entry=false`。
4. `zone_id=NULL AND lobby_cluster_id=NULL` 才是 backend 未分配；大厅成员不能因无 Zone 被判为 `unassigned`。
5. LobbyCluster 成员不能成为小区默认入口。Zone 迁入大厅时在同一事务清除默认入口标记。

### 2. Zone 与 LobbyCluster 的迁移复用排空门并原子审计

允许的归属迁移是未分配 backend→LobbyCluster、Zone→LobbyCluster、LobbyCluster→Zone 与 LobbyCluster→未分配；Zone→Zone 继续走既有换区工单，不借此机制绕过身份重确认。

真实迁移先读取既有运行态：实例在线且 `playerCount > 0` 时以 409 拒绝；实例离线、degraded/lost 或在线但玩家数为 0 时允许。Beacon 不踢人、不迁移玩家、不自动 drain。通过排空门后，在一个事务中锁定 server 行、复核归属、清除互斥字段及默认入口标记、写入新归属和专项审计；事务提交后才通知相关视图。相同目标是幂等 no-op，不写审计。

这个排空门是 LobbyCluster↔Zone 的明确特例，不改变所有 Zone→Zone 的现有语义。namespace 删除继续由既有“存在 server 则拒绝”保护；符合删除条件时，空 LobbyCluster 与 namespace 同事务删除，不提供单独删除大厅的 API。

### 3. BC 维护 namespace 全量受管 Bukkit 目录

BC discovery 按自身 namespace 获取全部当前可用 Bukkit/Paper backend，不再按 group/zone 过滤。成功快照是权威目录：可以接管同名条目、增加新条目，并删除 Beacon 曾标记为 managed 而未出现在该成功快照中的条目；未知手工条目不删。控制面请求、解析或传输失败不是成功空快照：BC 保留上一份目录和最后成功时间，只记录去重中文告警。

Lobby 成员标识和候选均由 DB 权威归属加既有健康/容量运行态渲染。BC 运行时上报的 `backends` 仅是其当前受管目录事实，供拓扑展示；它不能指派成员、不能覆盖 DB，也不能反向改变 LobbyCluster。

### 4. 首次代理连接只从大厅快照路由

控制面为 namespace 的唯一 LobbyCluster 生成候选快照。候选必须是 active 的 backend 大厅成员，且满足既有健康、容量与 draining 可调度判定；普通 Zone server、proxy、未确认身份、lost、unhealthy、draining 和容量无效成员均排除。选择复用一个共享的纯排序契约：先过滤不可调度项，再取最高健康分；同分取容量占用率较低者；仍同分使用可注入随机源。控制面在线决定、BC 本地首次决定和断连降级必须共享该契约，不新增大厅专属权重、阈值、轮询或全局预留。

BC 将完整解析成功的候选帧以原子替换写入内存和落盘快照。网络/5xx/解析失败保留最后有效快照；权威成功的空大厅快照必须覆盖旧值。快照超过现有新鲜阈值仍可使用并标记 STALE、记录去重中文 WARN，这是 fail-static；从未有有效快照、快照为空、候选均不可调度或候选不在受管目录时，拒绝首次连接并向玩家返回固定中文提示“当前大厅暂不可用，请稍后重试。”，这是 fail-closed。不得回退普通区服、小区默认入口或任意在线 backend。

### 5. 只拦截 Bungee 公开 JOIN_PROXY 首次事件，不接管 fallback

BC 仅在锁定 Bungee 版本公开 `ServerConnectEvent` 的 `JOIN_PROXY` 首次连接语义中处理路由，且必须同时确认 `player.server == null`。此时只读取本地不可变快照、按共享排序选择成员、确认候选仍在 Beacon managed 目录，再在该事件允许的同步窗口改写**本次**目标。事件线程不得进行 HTTP、数据库、磁盘 I/O 或等待 future。

不满足首次条件时立即旁路，不读取快照、不改目标。因此业务插件发起的切服、玩家已在 backend 后的连接、管理员切服、后端断开后的 Bungee fallback 均保持原有语义。多 listener 共享同一 namespace 大厅候选，但不拥有各自的大厅模型或策略。

实现必须先以当前锁定 Bungee API 写契约测试，并在最小真实 BC 环境确认公开 JOIN_PROXY 事件可安全改写首次目标、无候选可明确拒绝。若公开 API 不能同时满足这些条件，必须停止并请求新的产品决策；禁止用反射、内部连接类、阻塞网络调用或扩大为接管后端 fallback 来规避。

### 6. 旧小区默认入口保留，BC 旧消费链在新 agent 中停止

`server.is_default_entry`、默认入口管理 API/UI 和 discovery 的 `zoneDefaultEntry` 兼容字段继续存在，仍表达 Zone 内的业务默认入口，供业务插件或既有消费者使用。它们不是全局大厅，也不参与新 BC 的首次落脚。

新 BC agent 不再以 `proxy.home-group/home-zone` 筛选发现结果，也不把 `zoneDefaultEntry` 交给默认入口选择器。配置字段在滚动升级窗口内仅为兼容解析保留，新路径忽略并输出一次中文兼容提示；物理删除另行决定。所有承接玩家入口的 BC 必须升级后才能计入本 ADR 的 RC/生产验收；新 agent 面对不含 lobby 段的旧控制面安全拒绝首次连接，不能猜测小区或普通服。

## 与既有 ADR 的关系

### 对 ADR-0031 的精确取代范围

1. ADR-0031 决策 1 的 `zone_default_entry` 存储及 v1 写入口，已由 [ADR-0067](0067-default-entry-v2-authority.md) 取代；本 ADR 不再处理该部分。
2. ADR-0031 决策 2 保留 `zoneDefaultEntry` 作为 Zone 默认入口的兼容 discovery 表达；其中“供 BC 默认入口注入链路消费”的用途由本 ADR 取代。该字段不能再被新 BC 用来选择全局首次落点。
3. ADR-0031 决策 3（BC 本地 `proxy.home-group/home-zone` 选择一个 Zone）由本 ADR 全部取代：BC 服务整个 namespace，不再有 BC→Zone 的首次入口路由配置。
4. ADR-0031 决策 4 中“未命中即不设默认服并交由 Bungee 原生无默认服拒绝”的 BC 首次入口处理，由本 ADR 取代为“没有安全大厅候选即在 JOIN_PROXY 首次事件明确拒绝”；其“不得静默落任意 backend”的安全原则保留。
5. ADR-0031 决策 5（运行期将 serverId 写入每个 listener 的 `server-priority`）由本 ADR 全部取代。新 BC 不修改 listener priority、全局 fallback 列表或原始配置文件。

ADR-0031 原文保留为历史记录，未升级的旧 BC 仍可能按其旧机制运行；这不是新机制的验收通过条件。

### 对 ADR-0067 的精确取代范围

[ADR-0067](0067-default-entry-v2-authority.md) 决策 1 的“Zone 默认入口唯一真源为 v2 `server.is_default_entry`”、决策 2 的 v1 写链路移除与只读兼容、决策 3 的管理台 toggle 均继续有效。本 ADR 仅取代 ADR-0067 对 ADR-0031 决策 2/3/4“BC fallback 注入机制保持不变”的延续性结论：新 BC 不再消费 Zone 默认入口建立全局默认/fallback。ADR-0067 的 Zone 默认入口数据模型、API、UI 和兼容 discovery 字段不被删除、不改写。

## 理由

1. 全局大厅是 namespace 级独立业务概念，借 Zone 表达会将首次落脚和业务区服路由混为一体，并迫使 BC 保存不应存在的 home-zone 配置。
2. 大厅成员归属需要一致性、可审计和安全迁移，DB 权威与 ADR-0004 的“拓扑归属由控制面指派”原则一致；运行健康保持在原有运行态，避免第二真源。
3. BC 服务全 namespace 后端，目录与大厅候选分开：前者是数据面受管事实，后者是控制面 DB 归属与运行态共同渲染的调度输入。
4. 仅改 JOIN_PROXY 的本次目标把影响限制到首次落脚，不污染 Bungee 的后端断线 fallback 和业务插件切服职责。
5. fail-static 让短暂控制面故障不直接拒绝已可验证的入口；没有可验证候选时 fail-closed，避免把玩家静默路由到错误服。

## 后果

### 正面

- 每个 namespace 的大厅成员、就绪态和迁移审计可集中管理，业务插件仍完整拥有选区、排队与后续路由。
- BC 无需本地 home-zone，即可为整个 namespace 维护受管目录并在所有 listener 上一致处理首次连接。
- 控制面短暂不可用或 BC 重启后，最后有效快照仍可提供受约束的首次落脚；空或无安全候选不会误入普通服。
- Zone 默认入口保留为其原有业务职责，避免为解决全局大厅而破坏现有 API/UI/插件契约。

### 约束与迁移

- 数据库迁移需新增 `lobby_cluster` 和 `server.lobby_cluster_id`，启动回填失败不得让控制面以缺少大厅行的半状态服务。
- 新 BC 首次入口依赖控制面提供 lobby 候选段；滚动升级期间旧 BC 与新 BC 的首次行为不同，RC/生产入口只能以已升级 BC 验收。
- 真实 RC 门必须以至少 1 BC、2 个大厅 backend、1 个普通 Zone backend 验证首次落脚、健康/容量排除、控制面断连快照、重启恢复、无候选拒绝、多 listener 和后续切服旁路。
- `proxy.home-group/home-zone` 在兼容窗口仍可出现在本地配置，但新路径不消费；其删除不能借本 ADR 偷渡为配置破坏性变更。

## 备选方案

### 方案 A：把大厅建为特殊 Region 或 Zone

会把“首次进入全局大厅”伪装成业务分区，迫使大厅成员拥有并不真实的 Region/Zone，并继续耦合 Zone 默认入口。否决。

### 方案 B：每个 BC 在本地配置 home-zone，并继续修改 listener priority

配置为空会再次阻断玩家进入；更重要的是全局 priority 同时影响首次落点和后端断线 fallback，无法守住业务插件后续切服边界。否决。

### 方案 C：无大厅候选时回退任意在线 backend 或 Zone 默认入口

看似提高可进入性，实际上会把玩家静默送到错误业务服，跳过大厅并隐藏运维故障。否决。

### 方案 D：控制面在线时在 Bungee 事件线程同步调用 decide API

网络和控制面抖动会阻塞连接事件，且把数据面可用性绑定到单次远程调用。否决。

### 方案 E：控制面维护第二套大厅健康/容量缓存或 BC 目录反向指派成员

会与既有运行态和 DB 拓扑权威形成多真源，出现漂移时无法判定谁正确。否决。

### 方案 F：反射 Bungee 内部连接类或接管后端 fallback

反射脆弱且绕过公开 API 边界；接管 fallback 会扩展 Beacon 到业务插件和 Bungee 原生故障恢复路径。否决。
