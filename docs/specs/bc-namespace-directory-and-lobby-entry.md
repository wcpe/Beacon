# 功能规格：BC 全 namespace 目录与首次大厅落脚

> 状态：草拟　·　关联 PRD：FR-200　·　分支：待执行时创建

## 1. 背景与目标

BC agent 已能周期拉取同 namespace 的在线/亚健康 Bukkit，并把它们动态注入 BungeeCord 服务器目录；当前首次进服却仍依赖本地 `proxy.home-group/home-zone` 匹配某个小区默认入口，再把单服写到监听器 `server-priority` 首位。BC 服务整个 namespace 时，本地 home-zone 为空会导致无法设置默认入口，直接阻断玩家进服；即使配置成功，单一静态入口也无法承担大厅集群的健康/容量均衡。

FR-199 将大厅建模为独立 `LobbyCluster`。本规格让 BC 继续维护 namespace 全量受管后端目录，并在玩家首次连接代理、尚未进入任何后端时，只从 LobbyCluster 的可调度成员中复用现有健康/容量调度能力选择落脚大厅。玩家进入大厅后的选区、排队、小区默认服务器与后续切服全部交给业务插件，Beacon 不拦截。

本规格会改变既有 BC 默认入口机制、调度快照形状和控制面断连边界。实施前必须新增 ADR，明确取代 `home-group/home-zone + zoneDefaultEntry + server-priority 单服注入` 的既有决策，并说明“最后有效大厅快照优先、无安全候选时拒绝”的 fail-static/fail-closed 边界；ADR 通过后再写实现，不预设编号或创建悬空链接。

## 2. 需求（要什么）

- 每台 BC 自动注册其 namespace 内全部当前可用的受管 Bukkit 后端；大区/小区只作为虚拟分组，不限制 BC 发现范围。
- 目录同步继续区分“权威成功空快照”和“拉取失败”：成功快照可增删受管条目，失败保留最后有效目录，不因控制面短暂不可用清空 BC。
- 玩家首次连接 BC 且尚未进入任何后端时，只从当前 namespace 的 LobbyCluster 成员中选取大厅。
- 选择复用现有 `highest_score` 调度语义：先排除不可调度成员，健康分最高者优先；同分优先容量占用率低者，再同分随机。允许指标刷新周期内短暂倾斜，不提供严格均匀或跨 BC 全局原子预留。
- 控制面在线时刷新大厅候选快照；断连时继续使用本地最后一次有效大厅候选与健康/容量快照，重启后可从原子落盘快照恢复。
- 没有任何可调度大厅候选时拒绝首次进入，向玩家返回明确中文提示，并记录去重中文 WARN；绝不回退到普通区服、小区默认入口或任意在线 Bukkit。
- 初始落脚只处理首次代理连接；后续业务插件发起的 ServerConnect、区服切换、后端断开 fallback 和玩家调度一律不拦截。
- 多 Bungee listener 共用同一 namespace 大厅候选；本功能不得通过改写全局 fallback 列表影响玩家后续断线处理。
- 保留小区默认入口模型/API/UI/发现字段供业务插件使用，但新 BC agent 不再消费它设置全局默认服。
- `proxy.home-group/home-zone` 进入兼容废弃状态：旧 agent 继续读取，新 agent 不参与首次落脚决策；本 FR 不删除配置字段，物理清理由后续兼容窗口处理。

范围内：

- 控制面健康/调度视图识别 LobbyCluster 成员，并生成 namespace 级大厅候选快照。
- 调度候选/决定 API 的向后兼容扩展，以及 agent 候选缓存与落盘快照扩展。
- BC 全 namespace 目录同步的元数据保留、LobbyCluster 成员标识和最后成功同步时间。
- Bungee 首次连接事件拦截、只读本地快照选择、目标改写、安全 fallback 和无候选拒绝。
- agent core 与 Bungee 壳层的线程边界、日志、兼容与自动化/真机验收。

不做（范围外）：

- 不实现管理台新页面；LobbyCluster 管理由 FR-199 `/lobby-clusters` 承担。
- 不实现后台“立即重同步”；该操作由 FR-201 单独定义。
- 不实现 `/beacon servers`、`/beacon server` 命令；该查询由 FR-202 单独定义。
- 不实现小区内默认 server、玩家选区、排队、跨服传送、掉线重连或业务插件切服策略。
- 不做严格轮询、权重配置、全局容量预留、事务锁或跨 BC 强一致均衡。
- 不把普通区服作为无大厅候选时的 fallback。
- 不删除 Zone 默认入口数据，也不修改其业务插件契约。
- 不在 BC 事件线程执行阻塞 HTTP、DB 或文件 IO。

## 3. 设计（怎么做）

### 3.1 目录真源与同步语义

BC 继续经现有 discovery 链路拉取 `namespace=<自身 namespace>&role=bukkit`，不带 group/zone 过滤。发现响应的受管实例新增可选字段：

```json
{
  "serverId": "lobby-1",
  "role": "bukkit",
  "group": "",
  "zone": "",
  "address": "10.0.0.2:25565",
  "status": "online",
  "lobbyClusterMember": true
}
```

- `lobbyClusterMember` 从 FR-199 的 DB 权威归属渲染，不落注册内存形成第二真源；旧 agent 忽略未知字段。
- discovery 仍只返回当前可用集合（online + degraded）；lost/offline 在下一次成功快照中从受管目录移除。
- `ProxyServerDirectorySyncer` 在成功响应后原子更新“受管实例元数据快照”和 `lastSuccessfulSyncAt`，再增删 Bungee ServerInfo；拉取失败只记录 `lastError` 并保留目录和最后成功时间。
- 同名手工 ServerInfo 可继续由 Beacon 接管并同步权威地址；仅移除 Beacon 曾标记为 managed 且不再出现在成功快照中的条目，不删除未知手工条目。
- BC 上报的 `backends` 仍表示其运行期实际受管目录事实，供控制面拓扑展示；它不反向成为 LobbyCluster 成员或目录分配真源。

### 3.2 schedulable 与大厅候选

FR-199 后，backend 已分配条件改为：`zone_id != NULL OR lobby_cluster_id != NULL`。LobbyCluster 成员不因 `zone_id=NULL` 命中 `unassigned`。

大厅候选必须同时满足：

1. server 属于请求方 namespace 的唯一 LobbyCluster；
2. `kind=backend` 且身份 active；
3. 当前健康视图存在，未 lost、未 unhealthy；degraded 仍可调度但在分数上自然处于劣势；
4. `draining=false`；
5. 容量事实有效，且既有调度判定未给出其它不可调度原因。

选择算法只保留一个共享纯函数：

1. 过滤 `schedulable=false`；
2. 选最高 `score`；
3. 同分选 `onlineCount/maxOnline` 占用率更低者；`maxOnline<=0` 时沿既有调度口径处理，不另造大厅规则；
4. 仍同分时使用可注入随机源随机选择。

控制面在线决定、BC 首次连接本地决定和断连降级都调用同一排序函数/同一契约测试。现有本地降级若只按 score 取首项，必须先用红测暴露并收敛为同一算法，不能把差异当“近似实现”。

### 3.3 agent 调度 API 与快照兼容

向后兼容扩展现有 agent API，不删除/改名已有字段。

#### 候选快照

`GET /beacon/v2/agent/schedule/candidates`

响应在既有 `zones` 旁新增可选 `lobby`：

```json
{
  "generatedAtMs": 1785210000000,
  "lobby": {
    "clusterId": 12,
    "ready": true,
    "candidates": [
      {
        "serverId": "lobby-1",
        "score": 92,
        "level": "healthy",
        "schedulable": true,
        "onlineCount": 84,
        "maxOnline": 300
      }
    ]
  },
  "zones": []
}
```

- 旧 agent 忽略 `lobby`；新 agent 遇旧控制面缺失 `lobby` 时视为“尚无大厅候选”，不得从 `zones` 推断。
- 空/未就绪集群仍返回 `lobby`，`ready=false` 且 `candidates=[]`，避免把“模型缺失”和“暂无候选”混为协议错误。
- 候选缓存以整帧原子替换；落盘 `candidates-snapshot.json` 新增可选 `lobby` 段。写盘继续使用原子文件替换，损坏/缺失返回无快照，不抛到玩家线程。
- 只有候选请求成功并完整解析后才替换内存和磁盘；网络/5xx/解析失败保留最后有效快照。

#### 在线调度决定兼容

`POST /beacon/v2/agent/schedule/decide` 请求新增可选 `scope`：

```json
{"scope":"lobby","purpose":"proxy-initial-entry"}
```

- 缺省 `scope` 等价现有 `zone`，并继续要求 `zone` 字段，旧调用完全不变。
- `scope=lobby` 时忽略/拒绝非空 zone，只在请求方 namespace 的 LobbyCluster 中决定。
- 响应继续使用既有 `traceId,chosen,candidateCount,excludedCount,failReason` 形状。
- 无候选是 200 业务结果，`chosen` 缺失、`failReason=no_candidate`；参数非法返回 400 `INVALID_PARAM`。

BC 的玩家首次连接事件不直接调用该 HTTP 端点；它同步读取已刷新到本地的 `lobby` 快照并调用同一纯排序函数，从而不阻塞 Bungee 事件线程。在线 decide 端点保留给契约完整性、诊断与调度可解释性，并与本地算法共享测试向量。

### 3.4 Bungee 首次落脚机制

新增职责单一的 `InitialLobbyRouter`，core 只依赖抽象的大厅候选快照和目标回调，Bungee API 只出现在 agent-bungee 壳层。

触发条件必须同时成立：

- 事件原因为玩家加入代理（使用当前 Bungee API 提供的首次连接 reason）；
- `player.server == null`，尚未连接过任何后端；
- agent 已装配大厅路由器。

任一条件不成立立即返回，不读快照、不改目标。尤其 `player.server != null` 的业务插件切服、后端断线后的 fallback、管理员命令切服均不得拦截。

处理流程：

1. 从线程安全候选缓存读取一帧 LobbyCluster 快照；锁内只取不可变引用，不做网络/磁盘 IO。
2. 调共享 `highest_score` 纯函数选择大厅。
3. 命中时确认该 serverId 仍在 Beacon managed 的 Bungee 目录中；若目录缺失，按不可用处理，禁止构造未知目标。
4. 在事件允许的同步窗口把目标改为所选大厅 ServerInfo，并记录轻量调度追踪；不得等待 HTTP future。
5. 无候选或目录不一致时取消首次连接，返回固定中文玩家提示；日志记录 namespace、BC serverId、候选数量、快照年龄和脱敏原因。

玩家提示建议统一为：“当前大厅暂不可用，请稍后重试。”不得把内部地址、token、堆栈或候选明细发给玩家。

### 3.5 首次事件与 listener 边界

- 多 listener 全部读取同一 namespace 大厅候选，不为 listener 增加独立大厅模型或策略。
- Beacon 只在 `ServerConnectEvent` 的首次加入语义下改写本次目标，不持久或运行期改写 listener 的全局 `server-priority` / fallback 列表。
- 后端断开、业务插件切服、管理员切服和 Bungee 原生 fallback 保持原职责；Beacon 不把这些事件重定向大厅。
- Beacon 不反射、不改写 Bungee 原始配置文件，也不要求普通区服充当首次连接占位目标。

实现前必须用当前锁定的 Bungee 版本写事件契约测试和最小真机验证，证明在不改写全局 fallback、不中断事件线程的前提下，首次连接事件可取得并安全替换目标，且无候选时可明确拒绝。如果当前公开 API 无法同时满足这些条件，应立即停止并回到用户确认，不得用主线程等待 HTTP、反射内部连接类或扩大到后端断线 fallback。

### 3.6 控制面不可用与错误处理

- 候选刷新失败：保留最后有效内存/磁盘大厅快照，数据源标为 `LOCAL_SNAPSHOT`；只在状态转移或去重窗口内记录中文 WARN。
- 快照超过既有 10 分钟新鲜阈值：仍允许使用，但状态标 STALE、WARN 明示快照年龄；这延续既有调度 fail-static 语义。
- 无内存/落盘快照、快照无候选、全部候选不可调度、候选不在 managed 目录：拒绝首次进入，绝不回退普通区服。
- 权威成功空快照必须覆盖旧大厅候选，避免已被移出 LobbyCluster 的 server 长期接收新玩家；这与“网络失败保留快照”严格区分。
- 日志使用 INFO/WARN/ERROR 正确级别、全部中文并包含 namespace/BC/快照上下文，不输出凭据或玩家隐私，不在每次连接刷相同 WARN。

### 3.7 旧默认入口链路与兼容

- 新 BC agent 停止把 `zoneDefaultEntry` 交给 `DefaultEntrySelector`，停止依据 `proxy.home-group/home-zone` 设置全局默认/fallback 服。
- `ServiceInstance.zoneDefaultEntry`、v2 `server.is_default_entry`、管理台 toggle 和只读列表均保留，业务插件可继续读取。
- agent 配置解析暂时保留 `proxy.home-group/home-zone`，但新路径明确忽略并输出一次兼容提示；不在本 FR 删除字段，以免滚动升级期间旧二进制无法启动。
- 新控制面对旧 agent 只增加可选响应字段，旧 agent 继续旧行为；RC/生产验收要求所有承接玩家入口的 BC 升级，不能把混部旧行为算通过。
- 新 agent 对旧控制面缺失 lobby 段时安全拒绝首次进入，不猜测普通服或小区默认入口。

### 3.8 运维交互边界

本 FR 不新增管理台页面。运维入口与反馈落在现有页面：

- LobbyCluster 成员和就绪态在 FR-199 `/lobby-clusters` 查看/配置。
- BC 单服详情可展示目录最近成功时间、managed 数量、大厅候选数、快照来源/年龄和最近错误；具体本地命令由 FR-202 补齐。
- 无大厅候选时，`/lobby-clusters` 显示未就绪，服务器/BC 详情显示同一原因；两处均不得自动推荐普通区服。
- 本 FR 不提供“按大区/小区推送”按钮；目录始终按 namespace 自动同步。后台立即同步由 FR-201 提供 namespace/单 BC 操作。

### 3.9 API 错误与决定失败码

| 层次 | code / 原因 | 语义 |
|---|---|---|
| HTTP 400 | `INVALID_PARAM` | scope/zone/purpose 形状非法 |
| HTTP 403 | `AGENT_NOT_CONFIRMED` 或既有鉴权码 | 身份未 active/越权 |
| HTTP 404 | `zone_not_found` | 仅旧 `scope=zone` 路径；lobby 不借此码表达空候选 |
| 200 决定失败 | `no_candidate` | LobbyCluster 存在但无可调度候选 |
| 本地拒绝 | `no_snapshot` | 从未取得有效 lobby 快照 |
| 本地拒绝 | `candidate_not_managed` | 选中候选不在当前受管目录，拒绝而非构造目标 |

本地原因用于日志/状态，不直接把内部 code 暴露给玩家。HTTP 错误继续走统一脱敏出口并带 traceId。

## 4. UX / 交互

- 用户任务：运维确认 BC 已同步全 namespace 后端、LobbyCluster 已就绪，并能从现有服务器详情定位“为什么玩家无法首次进入”。
- 进入路径：大厅成员配置走 FR-199 `/lobby-clusters`；BC 运行状态走现有 `/servers` 单服详情。本 FR 不新增导航和页面。
- 操作闭环：在大厅页确认成员/就绪 → 在 BC 详情确认目录和候选快照 → 玩家首次连接被分配到健康有容量的大厅 → 调度追踪/日志能解释选择；失败时两处展示同一未就绪或快照原因。
- 状态设计：控制面在线、使用本地快照、快照过期、无快照、无候选、目录不一致必须可区分；前端展示脱敏原因，不只显示“内部错误”。
- 非目标交互：不增加按大区/小区推送、不增加大厅策略配置、不让运维从 BC 详情直接改变玩家后续路由。
- IA 挂载：无新页面；只扩展现有“服务器”详情与 FR-199 大厅页摘要。若实现中发现必须新增页面或重构导航，须停止并补 mockup 评审，不得借本 FR 静默扩 IA。

## 5. 任务拆分

- [ ] 基线：运行并记录控制面调度/发现、agent-core scheduling/proxy、agent-bungee 与相关前端详情测试，确认改动前全绿。
- [ ] ADR：实施前新增并确认 BC namespace 目录、LobbyCluster 首次落脚、旧 home-zone/priority 机制取代关系、fail-static/fail-closed 边界；同步被取代 ADR 状态与架构不变量。
- [ ] 测试先红：控制面健康事实测试证明 LobbyCluster backend 不再命中 `unassigned`，proxy/Zone 外普通服不能进入大厅候选。
- [ ] 最小实现：让健康/调度视图读取 FR-199 大厅归属，不复制健康事实、不引第二套候选存储。
- [ ] 测试先红：共享 ranker 覆盖不可调度过滤、最高分、同分低占用率、最终随机；在线决定与本地决定使用同一测试向量。
- [ ] 最小实现：抽取单一纯排序函数，归真现有本地降级与控制面算法差异。
- [ ] 测试先红：候选 API 覆盖 lobby 正常/空、旧 zone 契约不变、旧客户端忽略新字段、旧控制面缺字段、新旧快照往返、损坏快照与成功空快照覆盖。
- [ ] 最小实现：扩展 candidates/decide 契约、agent 缓存和原子落盘快照。
- [ ] 测试先红：目录同步覆盖全 namespace、group/zone 不限、Lobby 标识、同名接管、成功删 stale、失败保留、最后成功时间与权威空快照。
- [ ] 最小实现：扩展受管目录元数据快照，不改变 discovery 的可用集合语义。
- [ ] 测试先红：`InitialLobbyRouter` 覆盖仅首次触发、后续业务切服旁路、候选不在目录拒绝、无候选拒绝、STALE 可用、WARN 去重和玩家提示脱敏。
- [ ] 最小实现：在 core 写纯路由逻辑，在 agent-bungee 接首次事件和目标改写；事件线程零阻塞 IO。
- [ ] 测试先红：多 listener 使用同一大厅候选；只有首次加入事件改写目标，后端断线/业务插件/管理员切服/fallback 全部旁路；无候选明确拒绝。
- [ ] 最小实现：只用公开 Bungee 事件 API 改写单次首次连接目标，不改运行期 priority，不反射、不写原配置。
- [ ] 独立复核：审查首次事件边界、线程安全、快照原子性、普通服 fallback、旧 Zone 默认入口兼容、日志脱敏和是否出现第二套调度算法。
- [ ] 门禁：运行 agent Gradle 格式化、静态检查、相关模块与全量 build；运行 Go 格式化、lint、全量单测/集成测试；运行受影响前端门禁。
- [ ] 真机：至少 1 个真实 BC、2 个大厅服、1 个普通区服，逐项验证首次落脚、健康/容量排除、短时分布、控制面断连缓存、重启落盘恢复、无候选拒绝、后续业务切服不拦截和多 listener。
- [ ] 文档同步：PRD 状态、ARCHITECTURE、API、ADR/索引、OPERATIONS、配置字段废弃说明、CHANGELOG；验收完成后再改规格状态。

## 6. 验收标准

- BC 成功快照后，其 Beacon managed 目录包含同 namespace 全部当前可用 Bukkit，不受大区/小区过滤；控制面拉取失败保留最后目录，权威成功空快照可清空旧目录。
- LobbyCluster 成员候选来自 DB 权威成员集合；普通 Zone server、proxy、draining、lost、unhealthy、身份未 active 的 server 均不能成为首次大厅候选。
- 控制面在线决定、BC 首次本地决定和断连降级共享同一 `highest_score` 规则：最高健康分、同分低容量占用率、再同分随机。
- 玩家首次进入只落到两个大厅服之一；指标刷新周期内允许短暂倾斜，不宣称严格均匀。
- `player.server != null` 的业务插件切服、区服调度和后端切换事件不被 Beacon 改写。
- 控制面断连时继续使用最后有效快照；BC 重启后能从完整落盘快照恢复。快照过期仍可用但明确标 STALE 并记录中文 WARN。
- 没有有效快照或没有可调度大厅候选时，玩家被明确拒绝并收到中文提示；BC 不落普通区服，不消费小区默认入口兜底，日志不泄露凭据。
- 多 listener 的首次连接均使用同一大厅候选；Beacon 未改写全局 priority/fallback，后端断线与业务切服语义不受影响。
- Zone 默认入口模型/API/UI/发现字段仍可用；新 BC agent 不再读取 `proxy.home-group/home-zone` 决定首次落脚。
- candidates/decide 旧 zone 请求与响应保持兼容；旧 agent 可忽略新字段，新 agent 面对旧控制面不会猜测落点。
- 真机至少 1 BC + 2 大厅 + 1 普通服完成：健康排除、容量排除、控制面断连、重启快照、无候选拒绝、多 listener、后续切服不拦截。自动化全绿不能替代该门。

## 7. 风险 / 已拍板决定

已拍板：

- 一台 BC 服务整个 namespace，并注册该 namespace 全部后端；大区/小区仅虚拟分隔。
- 首次落脚只进入全局 LobbyCluster；小区内默认服、选区与排队由业务插件负责。
- 大厅选择复用现有健康/容量调度器，不新增大厅专属权重、阈值或轮询算法。
- 接受现有调度语义及指标刷新周期内短暂倾斜，不做全局严格均衡。
- 控制面不可用时用最后有效大厅快照；无候选时拒绝进入，不回退普通区服。
- 只拦截玩家首次连接代理，后续切服全部旁路。
- 小区默认入口保留，但 BC 不再消费为全局默认入口。
- 首个 RC 前必须通过真实 BC、双大厅和普通区服闭环。

风险与实施约束：

- Bungee 在首次连接事件前如何解析 priority 与事件 reason 受当前锁定版本 API 约束，必须用契约测试和真机验证；不得用阻塞等待 HTTP或反射内部实现规避。
- 当前控制面与 agent 本地降级的同分处理可能不完全一致；在复用前必须通过共享红测归真，否则无法声称“同一调度器”。
- 断连快照保可用性，但成员已被紧急移除时存在直到下一次成功刷新前的陈旧窗口；权威成功空快照必须覆盖旧值，运维紧急处置可结合 drain 使旧快照中的成员不可继续成为新候选。
- 当前 Bungee 公开事件 API 是否能在不依赖全局 priority 的情况下覆盖首次目标是实现硬门；若真机验证失败，必须回到用户确认，不能扩大成“同时接管后端断线 fallback”。
- 新 agent + 旧控制面会安全拒绝首次进入，这是有意的 fail-closed 兼容边界；RC 升级顺序与最低 agent 版本必须写进 OPERATIONS。
