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

### 4. fail-static 兜底：home-zone 未配 / 该 zone 暂无默认入口 / 默认入口暂不在线 → 取首个已注入在线 bukkit 作默认服
保证「至少能进」是 P0 的硬要求。BC agent 选默认服的优先级（纯逻辑、可单测）：
1. home-zone 已配 且 发现结果里有 `zoneDefaultEntry=true` 且命中 home-(group,zone) 的在线 bukkit → 选它；
2. 否则取本代理本轮已注入的**第一个在线 bukkit**（确定性顺序）；
3. 一个都没有 → 不设默认服（不抛、等下一轮）。
默认入口指向的服掉线后不在发现集合（只返回 online+degraded），下一轮 sync 自动改兜底，不长期卡死不可达默认服。

### 5. agent 设默认服 = 把 serverId 置于每个监听器 `server-priority` 列表首位（幂等、去重、非反射）
BungeeCord 公开 API `ProxyServer.getInstance().getConfig().getListeners()` → `ListenerInfo.getServerPriority()` 返回可变 `List<String>`（其内容即 BungeeCord 自身 join 落点解析读取的列表）。agent 把默认服 serverId 去重后置列表首位即把它变成默认/fallback——**用公开 API、不碰非公开实现 jar、不反射**（与 [ADR-0025](0025-bc-proxy-metrics-and-netty-traffic.md) 拒绝脆弱反射一脉相承）。设默认服与注入子服 `ServerInfo` 配套（serverId 必须已在 `servers` 中才有效）。每次设默认服打 INFO 中文日志可观测。

`ProxyServerDirectory` 接口加 `setDefaultServer(serverId)`，core 侧只调接口、不引 BungeeCord 类型（守 ADR-0005）；bungee 壳实现具体的 priority 列表写入。选默认服逻辑在 core 纯函数，设默认服经接口落到壳。

## 理由
- **默认入口是事实不是逻辑**：「这个 zone 的默认落点是哪台服」是运维指定的拓扑分配陈述，和 zone 归属同类，落 DB 权威不越「只存事实」边界；控制面不据它做任何玩家连接决策（连接是 BungeeCord 自己按 priority 做的）。
- **复用既有发现通道**：默认入口标志搭既有 discovery / instances 视图下发，不新增 agent 端点、不新增推送通道；BC 既有 directory sync async 循环顺带设默认服，零额外线程、不上主线程。
- **home-zone 配置而非 DB 指派**：BC 无 zone 归属（FR-8/FR-35），强行给 BC 建一张「BC→zone」表既越 zone 禁 BC 边界、又把代理路由意图混进集群拓扑权威；用 BC 自身数据面配置最简、最贴合 BungeeCord 既有配置模型。
- **兜底先行**：即便 home-zone / 默认入口都没配，兜底首个在线 bukkit 直接修住 P0，默认入口是「指定稳定落点」的增强而非「能不能进」的前提。

## 后果
- 新增表 `zone_default_entry`（DB 真源，与 `zone_assignment` 同类、可移植）；`AutoMigrate` 加表。
- discovery / instances 实例视图新增 `zoneDefaultEntry` 字段（bukkit 命中 zone 默认入口为 true，其余 false；bungee 恒 false）；向后兼容（旧 agent 忽略未知字段）。
- `agent-api` `ServiceInstance` 加 `zoneDefaultEntry` 字段（缺键解析 false，向后兼容）。
- BC agent `config.yml` 新增 `proxy.home-group` / `proxy.home-zone`（默认空 = 走兜底）。
- BC agent 会修改运行期监听器 `server-priority` 列表（仅置首位、幂等去重，不删运维原有条目），属预期的运行期行为；agent 卸载不还原列表（下次启动 BungeeCord 重读 config.yml 自然复位）。
- 单层假设：本 ADR 不处理嵌套 BC（BC→BC→bukkit），由 [FR-56](../PRD.md) 后续扩展。

## 备选方案
- **BC 也进一张「BC→zone」DB 表（DB 权威指派 BC 的 home-zone）**：与「zone 禁 BC」（FR-8/FR-35）边界冲突，且把代理路由意图升格为集群拓扑权威，过重。落选（home-zone 是数据面路由配置，留 BC 本地）。
- **默认入口落 `runtime.Instance` 内存事实、随注册下发**：默认入口是 DB 权威的运维分配、非注册态派生事实，落内存要额外同步逻辑且与真源切分相悖（[ADR-0024](0024-bc-backend-membership-as-fact.md) 同款理由）。落选（渲染时读 DB 标记即可）。
- **控制面直接算「每台 BC 该用哪个默认服」并下发给具体 BC**：要求控制面知道每台 BC 服务哪个 zone（即 BC→zone 归属），回到被否的备选 1，且让控制面替代理做路由决策、越「只存事实」边界。落选。
- **agent 用反射改 BungeeCord 内部默认服字段**：`server-priority` 公开可变、无需反射；反射脆弱、跨版本易碎（[ADR-0025](0025-bc-proxy-metrics-and-netty-traffic.md) 已立此原则）。落选。
- **不做默认入口、只兜底首个在线 bukkit**：能修 P0 但落点随发现顺序漂移、运维不可控（重启 / 扩容后默认服可能跳到别的服）。落选（默认入口给运维稳定可指定的落点，兜底仅作 fail-static）。
