# ADR-0031：小区默认入口（DB 权威）+ BC 注入 BungeeCord 默认/fallback 服


**状态**：已接受

## 背景

BC（BungeeCord 代理）侧 agent 经 [FR-4](../PRD.md) 链路已能把同 namespace 在线 `role=bukkit` 子服动态注入 `ProxyServer.servers`（[ADR-0004](0004-zone-authority-control-plane.md) zone 权威 + 既有 `ProxyServerDirectorySyncer`），但**从不给 BungeeCord 设默认/fallback 服**。

BungeeCord 的「默认/fallback 服」不是某个 `ServerInfo` 的属性，而是**每个监听器（`ListenerInfo`）的 `server-priority` 列表**决定的：玩家加入时按该列表顺序挑首个可达的 `ServerInfo` 作落点；列表为空、或所列服名不在 `servers` 映射里，BungeeCord 抛 `Could not connect to a default or fallback server` 把玩家踢掉。动态注入的子服 serverId **不在静态 `config.yml` 的 `server-priority` 列表**里 → 「注入了子服却没有默认服」→ 玩家 100% 进不去。这是当前的 P0 现象。

需要一个机制：让运维为每个小区（zone）指定一个稳定的「默认入口」子服，下发给该 zone 下的 BC，让 BC 把它设为 BungeeCord 默认/fallback 服。约束须守住既有边界：控制面只存事实不写游戏逻辑（[architecture-invariants](../../.claude/rules/architecture-invariants.md) #1）、zone 归属由控制面 DB 权威且 agent 不声明 zone（[ADR-0004](0004-zone-authority-control-plane.md)）、zone 仅给 bukkit 子服 BC 不进 zone_assignment（FR-8/FR-35）、agent core TabooLib-free 且 HTTP/JSON 只经接口（[ADR-0005](0005-agent-transport-codec-abstraction.md)）、不在 MC 主线程阻塞、fail-static（#5）。

## 决策

### 1. 默认入口是控制面 DB 权威的「拓扑分配」事实，落新表 `zone_default_entry`
每个 `(namespace, group, zone)` 唯一一个 `default_server_id`。这与 `zone_assignment`（serverId→zone 归属）同类——低频、要强一致、要审计的拓扑分配，DB 权威是正解（沿用 [ADR-0004](0004-zone-authority-control-plane.md) 的理由）。新表全 `VARCHAR` + 时间戳、唯一索引 `(namespace_code, group_code, zone_code)`、零方言可切 Postgres（守 GORM 可移植不变量）。

`default_server_id` 在**应用层校验**为「当前已指派到该 `(group, zone)` 的 serverId」（查 `zone_assignment`）——不下推 DB 外键/约束（保持可移植 + 单一应用层校验口径）。set/clear 在 DB 事务内连同审计原子完成、提交后才唤醒 watch；审计动作 `zone.set-default-entry` / `zone.clear-default-entry`。

### 2. 默认入口经发现接口下发（给 bukkit 实例打 `zoneDefaultEntry` 标志），不进注册态内存事实
默认入口真源在 DB（与 `zone_assignment` 同源），**不**塞进 `runtime.Instance`（那是注册/健康的内存真源，[ADR-0024](0024-bc-backend-membership-as-fact.md)）。改为发现/实例视图**渲染时**由 service 读 DB 默认入口集合、给命中的 bukkit 实例视图标 `zoneDefaultEntry=true`。`InstanceService.Discover` 接收一个「默认入口解析器」回调注入该判定，handler 不碰 DB/repository（守分层单向）。这样默认入口改派即时反映到下一次发现、无需同步内存。

### 3. BC 用自身数据面配置声明 `home-zone` 选择消费哪个 zone 的默认入口；不进 zone_assignment、不违反 ADR-0004
关键岔路：BC（`role=bungee`）被排除在 `zone_assignment` 外，且 [ADR-0004](0004-zone-authority-control-plane.md) 规定 agent 不声明 zone。那 BC 怎么知道该用哪个 zone 的默认入口？

**取舍**：BC 在自身 `config.yml` 声明 `proxy.home-group` + `proxy.home-zone`，表示「**我这台代理服务哪个小区的玩家**」。这是**数据面路由配置**，不是 serverId→zone 的权威归属声明：

- 默认入口的**值**（哪个 serverId 是某 zone 的默认入口）仍 100% DB 权威——BC 只是从下发的发现结果里**挑出 home-zone 命中的那条**，不指派、不声明自己的 zone。
- BC 本就有「我代理哪些后端」的天然数据面配置（BungeeCord `config.yml` 的 servers/priority），`home-zone` 与之同类，是代理路由意图，不是集群拓扑归属。
- 多台 BC 配同一 `home-zone` = 多 BC 共享同一 zone 的默认入口（直接支持），各 BC 独立读发现、独立设自己的默认服。

因此本决策**不**违反 ADR-0004（它约束的是「子服的 zone 归属不由 agent 声明」，而非「代理不能配置自己服务哪个 zone」）。

### 4. 未配 / 无命中 / 默认入口不在线 → 不设任何默认服 + 一条 WARN，绝不静默落任意服
BC **只**把「该 home-zone 在 Beacon 显式配置的默认入口 serverId」设为 BungeeCord 默认服。BC agent 选默认服的纯逻辑（可单测）：
1. home-zone 已配（`proxy.home-group` + `proxy.home-zone` 均非空）且 发现结果里有 `zoneDefaultEntry=true`、命中 home-(group,zone)、且在线的 bukkit → 选它；
2. 否则（home-zone 未配 / 该 zone 在 Beacon 未设默认入口 / 默认入口当前不在线或未注入）→ **不设任何默认服**，并打一条带 group/zone 上下文的中文 WARN（去重、不每轮刷屏，明确告知运维去 Beacon 配默认入口）。

**不回退到任意在线 bukkit**：原方案在选不出时兜底「取首个在线 bukkit」，但这会把玩家**静默**落到一台非大厅服、跳过大厅，造成风险（玩家被丢到错误服而无任何信号）。改为不设默认服后，玩家遇到的是 BungeeCord 原生「无默认服」拒绝——这是「没配」的**明确信号**，运维据 WARN 去 Beacon 配置即可，玩家**绝不被丢到错误服**。WARN 在既有 directory sync async 循环里打（经 `ProxyServerDirectory` 注入的日志门面），不在 MC 主线程阻塞。

默认入口指向的服掉线后不在发现集合（只返回 online+degraded），下一轮 sync 选不出 → 回到「不设 + WARN」，不长期卡死在不可达默认服、也不静默改落别的服。

### 5. agent 设默认服 = 把 serverId 置于每个监听器 `server-priority` 列表首位（幂等、去重、非反射）
BungeeCord 公开 API `ProxyServer.getInstance().getConfig().getListeners()` → `ListenerInfo.getServerPriority()` 返回可变 `List<String>`（其内容即 BungeeCord 自身 join 落点解析读取的列表）。agent 把默认服 serverId 去重后置列表首位即把它变成默认/fallback——**用公开 API、不碰非公开实现 jar、不反射**（与 [ADR-0025](0025-bc-proxy-metrics-and-netty-traffic.md) 拒绝脆弱反射一脉相承）。设默认服与注入子服 `ServerInfo` 配套（serverId 必须已在 `servers` 中才有效）。每次设默认服打 INFO 中文日志可观测。

`ProxyServerDirectory` 接口加 `setDefaultServer(serverId)`，core 侧只调接口、不引 BungeeCord 类型（守 ADR-0005）；bungee 壳实现具体的 priority 列表写入。选默认服逻辑在 core 纯函数，设默认服经接口落到壳。

## 理由
- **默认入口是事实不是逻辑**：「这个 zone 的默认落点是哪台服」是运维指定的拓扑分配陈述，和 zone 归属同类，落 DB 权威不越「只存事实」边界；控制面不据它做任何玩家连接决策（连接是 BungeeCord 自己按 priority 做的）。
- **复用既有发现通道**：默认入口标志搭既有 discovery / instances 视图下发，不新增 agent 端点、不新增推送通道；BC 既有 directory sync async 循环顺带设默认服，零额外线程、不上主线程。
- **home-zone 配置而非 DB 指派**：BC 无 zone 归属（FR-8/FR-35），强行给 BC 建一张「BC→zone」表既越 zone 禁 BC 边界、又把代理路由意图混进集群拓扑权威；用 BC 自身数据面配置最简、最贴合 BungeeCord 既有配置模型。
- **宁可不设也不静默落错**：默认入口是「玩家落到哪台大厅」的运维显式决策。选不出时回退到任意在线 bukkit 看似「修住 P0」，实则把玩家**静默**送进非大厅服、跳过大厅，是更隐蔽的事故（玩家与运维都收不到信号）。改为不设默认服 + WARN：玩家遇 BungeeCord 原生「无默认服」拒绝（明确信号），运维据 WARN 去配，落点始终由运维显式决定、绝不漂移到错误服。

## 后果
- 新增表 `zone_default_entry`（DB 真源，与 `zone_assignment` 同类、可移植）；`AutoMigrate` 加表。
- discovery / instances 实例视图新增 `zoneDefaultEntry` 字段（bukkit 命中 zone 默认入口为 true，其余 false；bungee 恒 false）；向后兼容（旧 agent 忽略未知字段）。
- `agent-api` `ServiceInstance` 加 `zoneDefaultEntry` 字段（缺键解析 false，向后兼容）。
- BC agent `config.yml` 新增 `proxy.home-group` / `proxy.home-zone`（默认空 = 未配 → 不设默认服 + WARN）。
- 未配 / 无命中 / 默认入口不在线时 BC **不设默认服**，玩家加入会遇 BungeeCord 原生「无默认服」拒绝——这是「没配」的预期信号（不是回退故障），需运维据 WARN 在 Beacon 为该小区配默认入口后自愈。
- BC agent 会修改运行期监听器 `server-priority` 列表（仅置首位、幂等去重，不删运维原有条目），属预期的运行期行为；agent 卸载不还原列表（下次启动 BungeeCord 重读 config.yml 自然复位）。
- 单层假设：本 ADR 不处理嵌套 BC（BC→BC→bukkit），由 [FR-56](../PRD.md) 后续扩展。

## 备选方案
- **BC 也进一张「BC→zone」DB 表（DB 权威指派 BC 的 home-zone）**：与「zone 禁 BC」（FR-8/FR-35）边界冲突，且把代理路由意图升格为集群拓扑权威，过重。落选（home-zone 是数据面路由配置，留 BC 本地）。
- **默认入口落 `runtime.Instance` 内存事实、随注册下发**：默认入口是 DB 权威的运维分配、非注册态派生事实，落内存要额外同步逻辑且与真源切分相悖（[ADR-0024](0024-bc-backend-membership-as-fact.md) 同款理由）。落选（渲染时读 DB 标记即可）。
- **控制面直接算「每台 BC 该用哪个默认服」并下发给具体 BC**：要求控制面知道每台 BC 服务哪个 zone（即 BC→zone 归属），回到被否的备选 1，且让控制面替代理做路由决策、越「只存事实」边界。落选。
- **agent 用反射改 BungeeCord 内部默认服字段**：`server-priority` 公开可变、无需反射；反射脆弱、跨版本易碎（[ADR-0025](0025-bc-proxy-metrics-and-netty-traffic.md) 已立此原则）。落选。
- **不做默认入口、只取首个在线 bukkit 作默认服**：落点随发现顺序漂移、运维不可控（重启 / 扩容后默认服可能跳到别的服），更严重的是会把玩家**静默**落到非大厅服、跳过大厅。落选（默认入口给运维稳定可指定的落点；选不出时宁可不设 + WARN，绝不静默落错）。
- **选不出时兜底取首个在线 bukkit（fail-static）**：曾作为本 FR 初版方案，被否。「至少能进」不等于「进对地方」——把玩家丢进随机非大厅服比直接拒绝更隐蔽、更难排查，且违背「默认入口=运维显式决策」的初衷。落选（改为不设 + 一条去重 WARN，让运维收到明确信号去配）。
