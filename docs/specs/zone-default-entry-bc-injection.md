# 功能规格：小区默认入口 + BC 默认服注入

> 状态：开发中　·　关联 PRD：FR-48　·　分支：feature/fr-48-zone-default-entry

## 1. 背景与目标

P2 阶段。BC（BungeeCord 代理）经 FR-4 链路已能把同 namespace 在线 `role=bukkit` 子服**动态注入**为 `ProxyServer.servers` 条目，但 **从未给 BungeeCord 设默认/fallback 服**——BungeeCord 玩家加入时按监听器（`ListenerInfo`）的 `server-priority` 列表挑首个可达服作为落点；列表为空或所列服不在 `servers` 中，就报 `Could not connect to a default or fallback server` 把玩家踢掉。动态注入的子服名不在静态 `config.yml` 的 priority 列表里，于是「注入了子服却没人能进」。

目标：让 beacon 为每个小区（zone）唯一指定一个「默认入口」（指向一个已指派该 zone 的在线 bukkit serverId），经发现下发给该 zone 下的 BC agent，agent 把默认入口注入为 BungeeCord 监听器 priority 列表首位（即默认/fallback 服），修复玩家进不去的 P0 现象。一个 zone 可多 BC 共享同一入口。

## 2. 需求（要什么）

- **控制面权威存默认入口**：每个 `(namespace, group, zone)` 唯一一个 `default_server_id`，归属由控制面 DB 权威（扩展 FR-8/ADR-0004 zone 权威）。
- **校验**：`default_server_id` 必须是当前已指派到该 `(group, zone)` 的 serverId（查 `zone_assignment`），应用层校验、不下推 DB 约束。
- **Admin API**：set / clear / list 每 zone 默认入口；多表写在事务内，提交后再唤醒相关 watch；写审计。
- **下发给 BC**：发现接口（`/beacon/v1/agent/discovery` 与 admin `/instances`）给 bukkit 实例附 `zoneDefaultEntry` 标志，BC agent 据此选默认服。
- **BC 选 home-zone**：BC（`role=bungee`，被排除在 zone_assignment 外）在**自身数据面配置**声明 `proxy.home-group` + `proxy.home-zone`，表示「我服务哪个小区」；多 BC 同 home-zone = 多 BC 服一 zone。该配置不是 zone_assignment、不进控制面、不违反 ADR-0004。
- **兜底（修住 P0）**：BC 未配 home-zone（或所配 zone 暂无默认入口/默认入口暂不在线）时，**取本代理已注入的第一个在线 bukkit 子服作默认服**——保证玩家至少能进。
- **可观测**：agent 注入子服地址 + 设默认服时打 INFO 中文日志。

- 范围内：zone 级默认入口（每 zone 唯一）的 CRUD + 校验 + 审计 + 下发；BC agent 设 BungeeCord 默认/fallback 服 + home-zone 选择 + 兜底；INFO 日志。
- 不做（范围外）：负载均衡 / 多入口加权 / canary 引流 / drain（属 P2/P3 FR-10，不在本 FR）；嵌套 BC 多层代理（FR-56，本 FR 单层假设）；前端代理服管理页（FR-52，独立 FR）；玩家落位调度（业务插件域）。

## 3. 设计（怎么做）

涉及架构决策 → 见 ADR-0031，本节不重复决策正文，只列改动面。

### 控制面（Go）
- **模型**：新增 `internal/model/zone_default_entry.go`：`ZoneDefaultEntry{ ID, NamespaceCode, GroupCode, ZoneCode, DefaultServerID, CreatedAt, UpdatedAt }`，唯一索引 `(namespace_code, group_code, zone_code)`，全 `VARCHAR`/时间戳、零方言（可切 Postgres）。`AutoMigrate` 注册。
- **repository**：`internal/repository/zone_default_entry_repo.go`：`FindByZone` / `ListByNamespaceGroup` / `Upsert` / `Delete`（按 (ns,group,zone)），`WithTx` 事务副本。
- **service**：`ZoneService` 新增 `SetDefaultEntry` / `ClearDefaultEntry` / `ListDefaultEntries` / `ResolveDefaultEntries`（供发现下发用，返回 `map[serverId]bool` 或 zone→serverId）。`SetDefaultEntry` 校验 serverId 已指派到该 (group,zone)；事务内 upsert + 审计；提交后 `notifyTopology`（拓扑 watch 复用）。
- **enums**：新增审计 action `zone.set-default-entry` / `zone.clear-default-entry`，复用 `TargetTypeZone`。
- **handler**：`ZoneHandler` 新增 `ListDefaultEntries` / `SetDefaultEntry` / `ClearDefaultEntry`。
- **router**：`GET/PUT/DELETE /admin/v1/zones/default-entry`。
- **发现下发**：`runtime.Instance` 不存默认入口（默认入口真源在 DB，非注册态事实）。改为发现/实例视图渲染时，由 service 读 DB 默认入口集合，给命中的 bukkit 实例视图标 `zoneDefaultEntry=true`。`InstanceService.Discover` 接受一个「默认入口解析器」回调（避免 handler 碰 DB / repository）。

### agent
- **agent-api**：`ServiceInstance` 新增 `zoneDefaultEntry`（boolean）字段 + getter（向后兼容：缺键解析为 false）。
- **agent-core**：
  - `ProxyServerDirectory` 接口新增 `setDefaultServer(serverId)`（把该 serverId 设为 BungeeCord 默认/fallback 服）。
  - `ProxyServerDirectorySyncer` 在注入子服后，按「home-zone 命中的默认入口 → 兜底首个在线 bukkit」算出默认服 serverId，调用 `directory.setDefaultServer(...)`；选择逻辑为可单测的纯逻辑（输入实例列表 + home-zone，输出默认服 serverId 或 null）。
  - 新增 `AgentSettings.proxy`（`ProxySettings{ homeGroup, homeZone }`），`AgentBootstrap` 读 `proxy.home-group` / `proxy.home-zone`（默认空串 = 未配，走兜底）。
- **agent-bungee**：`BungeeServerDirectory.setDefaultServer` 实现——把 serverId 置于每个监听器 `ListenerInfo.getServerPriority()` 列表首位（去重、幂等），打 INFO 日志。`config.yml` 加 `proxy` 配置块（中文注释）。

### 不动的边界
- agent core 保持 TabooLib-free、HTTP/JSON 只经接口（ADR-0005）；选默认服是纯逻辑、设默认服经 `ProxyServerDirectory` 接口由 bungee 壳实现，不在 core 引 BungeeCord API。
- 不在 MC 主线程阻塞 IO（沿用既有 directory sync async 循环）。fail-static：控制面不可用 → 兜底首个本地已知在线 bukkit。

## 4. 任务拆分
- [ ] 控制面：模型 + AutoMigrate + repository（红→绿单测：唯一性、Upsert、Delete）
- [ ] 控制面：service set/clear/list + 校验（serverId 须指派到该 zone）+ 审计（红→绿）
- [ ] 控制面：handler + router 端点
- [ ] 控制面：发现/实例视图标 `zoneDefaultEntry`（service 解析器，handler 不碰 DB）
- [ ] agent：ServiceInstance 加字段；DiscoveryView 解析
- [ ] agent core：默认服选择纯逻辑 + syncer 设默认服 + ProxySettings（红→绿）
- [ ] agent bungee：BungeeServerDirectory.setDefaultServer（priority 列表）+ config.yml proxy 块 + INFO 日志
- [ ] 文档同步：PRD 状态、ARCHITECTURE、API、CHANGELOG、config-files

## 5. 验收标准
- 控制面单测：同 (ns,group,zone) 仅一条默认入口（重复 set 覆盖）；set 一个未指派到该 zone 的 serverId 被拒；clear/list 正确；审计落 `zone.set-default-entry` / `zone.clear-default-entry`。
- 发现单测/集成：被标默认入口的 bukkit 实例视图 `zoneDefaultEntry=true`，其余 false。
- agent 单测：home-zone 命中默认入口 → 选该 serverId 设默认服；home-zone 未配 → 兜底取首个在线 bukkit；默认入口不在已注入集合 → 兜底；空集 → 不设（不抛）。
- agent bungee 单测：`setDefaultServer` 把 serverId 置 priority 首位且幂等、去重。
- 受影响组件测试全绿（`go test ./...`；`gradle :agent-core:test :agent-bungee:test :agent-adapters:test`）。
- **真机维度（玩家真能进服）**：本机无 MC 集群，标「待真机验」。

## 6. 风险 / 待定
- **BC↔zone 取舍（设计待确认）**：BC 用自身 config `home-zone` 选默认入口是否触碰 ADR-0004「agent 不声明 zone」。本规格的立场：`home-zone` 是数据面路由配置（「我服务哪个 zone 的玩家」），非 serverId→zone 权威归属声明，默认入口值本身仍 DB 权威，故不违反。备选见 ADR-0031 备选方案。无论取舍如何，兜底「首个在线 bukkit」都先把 P0 修住。
- 多 BC 同 home-zone 共享同一默认入口 = 直接支持（各 BC 独立读发现、独立设自己的默认服）。
- 默认入口指向的服掉线：发现只返回 online+degraded，掉线后该实例不在发现集合 → agent 下一轮 sync 兜底取其它在线 bukkit（不长期卡死在不可达默认服）。
