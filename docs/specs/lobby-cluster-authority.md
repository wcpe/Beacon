# 功能规格：LobbyCluster 权威模型与独立管理台

> 状态：草拟　·　关联 PRD：FR-199　·　分支：待执行时创建

## 1. 背景与目标

Beacon 当前的 v2 区服模型以 `BCCluster → Region → Zone → Server` 表达代理集群、大区、小区和业务子服。玩家首次进入 BC 时需要先落到一组全局大厅服，但大厅服不属于任何虚拟大区或小区，也不应借用“小区默认入口”表达。若继续把大厅塞进 Zone，会混淆“首次落脚”和“进入业务区服”两种职责，并使 BC 必须在本地声明 `home-group/home-zone`。

本规格为每个 namespace 建立唯一、独立的 `LobbyCluster` 权威模型，由控制面管理大厅成员及其与大区/小区归属的互斥关系。大厅只承接玩家首次进入代理后的落脚；选区、排队、小区默认服务器和后续切服由业务插件负责，不进入 Beacon。既有 v2 区服事实继续以 [v2-zone-authority.md](v2-zone-authority.md) 为准，本规格只定义其大厅扩展与明确特例。

本规格涉及新的拓扑归属、迁移语义和现有默认入口职责变化。实施前必须先新增 ADR，明确 LobbyCluster 与 Zone 的关系、排空迁移门，以及它对既有默认入口注入决策的取代范围；ADR 通过后再写实现，不在本规格中预设 ADR 编号或创建悬空链接。

## 2. 需求（要什么）

- 每个 namespace 恰好拥有一个 `LobbyCluster`；该集群是 namespace 级全局大厅，不隶属于 BCCluster、Region 或 Zone。
- 仅同 namespace、`kind=backend` 的 Bukkit/Paper server 可成为大厅成员；proxy/BC 不可加入。
- 一个 backend server 只能处于以下一种正式归属：LobbyCluster、Zone 或未分配。LobbyCluster 与 Zone 归属必须互斥。
- 既有 namespace 升级时自动补建空 LobbyCluster，不从小区默认入口、在线顺序或任何启发式规则推断成员。
- 新建 namespace 时同步创建空 LobbyCluster，禁止产生“namespace 已存在但大厅集群缺失”的正常业务状态。
- LobbyCluster 可以为空，便于初始化；空集群或没有可调度成员时显示“未就绪”，并阻断 RC/生产验收，但不阻止管理员继续配置。
- server 在 LobbyCluster 与 Zone 之间切换时，复用既有排空安全语义：在线且仍有玩家时返回 409；玩家数为 0 或实例离线时允许在单事务内原子切换。Beacon 不踢人、不转移玩家、不自动 drain。
- 所有真实归属变更写专项审计；同值请求幂等返回，不重复写库、不制造审计噪声。
- 管理台新增独立 `/lobby-clusters` 页面，挂在“集群”导航组中；先完成可点击 mockup 并经浏览器评审确认，之后才能接真实 API。

范围内：

- LobbyCluster 表、Server 大厅归属字段、约束、索引与幂等迁移。
- LobbyCluster 只读查询、成员分配与 Zone/LobbyCluster 原子迁移 API。
- 成员变更的校验、事务、审计、错误码和管理台操作闭环。
- 健康/调度事实读取所需的大厅归属字段；具体候选选择与 BC 首次落脚由 FR-200 规格定义。
- `/lobby-clusters` 的空态、常规态、超大量态和错误态，以及 mockup 评审门。

不做（范围外）：

- 不把 LobbyCluster 嵌入大区或小区，不创建“大厅大区”“大厅小区”等兼容层。
- 不支持同一 server 同时属于 LobbyCluster 与 Zone。
- 不支持一个 namespace 配置多个 LobbyCluster，也不增加集群名称、权重、策略等无需求字段。
- 不实现玩家选区、排队、区服内默认服务器、跨服传送或玩家迁移。
- 不自动从旧默认入口、第一台在线服或未分配服中挑选大厅成员。
- 不删除小区默认入口模型、API、UI 或发现字段；它们继续供业务插件消费。
- 不修改 agent 本地身份配置、地址探测或 Redis 清理范围。

## 3. 设计（怎么做）

### 3.1 权威边界与分层

- LobbyCluster 和成员归属是低频、需审计的控制面事实，真源落 MySQL；不得放进注册/健康内存态充当第二真源。
- 实例在线、玩家数、健康和容量继续来自既有运行态；成员变更只读取这些事实执行安全校验，不把运行指标复制进大厅表。
- handler 只负责解析/渲染，事务与不变量集中在 v2 控制面 service；repository/模型不得反向依赖 handler 或前端。
- DB 使用 GORM 可移植类型：表名 snake_case，枚举用 VARCHAR + 应用层校验，不使用方言专有 ENUM、JSON 列或级联外键。

### 3.2 数据模型

新增 `lobby_cluster`：

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| id | BIGINT | PK 自增 | GORM 抽象自增 |
| namespace_id | BIGINT | NOT NULL，唯一索引 | 一 namespace 恰好一行 |
| created_at / updated_at | DATETIME | NOT NULL | UTC |

不增加 `name`、`description`、`strategy` 等字段。页面展示名由 namespace 名派生，避免给唯一对象制造无意义配置。

扩展 `server`：

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| lobby_cluster_id | BIGINT | 可空，普通索引 | 仅 `kind=backend` 使用；非空表示大厅归属 |

应用层不变量：

1. `lobby_cluster_id != NULL` ⇒ `kind=backend`、目标 LobbyCluster 与 server 的 `namespace_id` 相同。
2. `lobby_cluster_id != NULL` ⇒ `zone_id=NULL`、`pending_zone_id=NULL`、`bc_cluster_id=NULL`、`pending_bc_cluster_id=NULL`、`is_default_entry=false`。
3. `kind=proxy` ⇒ `lobby_cluster_id=NULL`。
4. backend 的“未分配”改为 `zone_id=NULL AND lobby_cluster_id=NULL`；大厅成员不再命中调度原因 `unassigned`。
5. LobbyCluster 成员不得成为小区默认入口；从 Zone 迁入大厅时在同一事务清除 `is_default_entry`。
6. namespace 删除仍沿用既有“存在 server 则拒绝”保护；满足删除条件时，在同一事务内删除其空 LobbyCluster 后再删除 namespace。本规格不新增单独删除 LobbyCluster 的入口。

不建 DB 外键和跨表 CHECK；完整性由 service 在事务内校验，保持 SQLite/MySQL/Postgres 可移植。

### 3.3 迁移与初始化

迁移分两步，均在控制面启动期完成：

1. GORM `AutoMigrate` 创建 `lobby_cluster` 并给 `server` 增加可空 `lobby_cluster_id`。加列默认 NULL，不改变任何旧 server 归属。
2. 幂等回填器按 namespace 扫描：对缺失的 namespace 创建一条空 LobbyCluster；已有行保持不动。回填不得写成员、不得读取 `is_default_entry` 推断成员。

新 namespace 创建流程在同一 DB 事务内创建 namespace 与其空 LobbyCluster；任一写失败整笔回滚。

并发与幂等：

- `namespace_id` 唯一索引是最终并发防线；回填和创建逻辑在重复执行时识别已存在行并返回现有对象。
- 启动回填失败必须阻止控制面进入可服务状态并输出中文错误；不得在缺少大厅权威行时继续提供半套 API。
- 不删除旧 `proxy.home-group/home-zone` 配置字段；其停止消费与兼容窗口由 FR-200 处理。

### 3.4 管理面 API

所有端点位于现有 `/admin/v2` 鉴权组；readonly 角色只能读，写端点统一返回 403。响应字段使用 camelCase。

#### 3.4.1 查询 LobbyCluster

| 方法 | 路径 | 请求 | 响应 |
|---|---|---|---|
| GET | `/admin/v2/lobby-clusters` | `namespaceId?`、`ready?`、`page?`、`pageSize?` | `{items,total}`；每项含 `id,namespaceId,namespaceName,memberCount,schedulableCount,ready,updatedAt` |
| GET | `/admin/v2/lobby-clusters/{id}` | `memberPage?`、`memberPageSize?`、`keyword?`、`status?` | 集群摘要 + 分页 `members`；成员含 serverId、地址、在线/健康/容量/调度状态与原因 |

`pageSize` 默认 20、最大 200；成员查询必须服务端分页，禁止为 1000+ server 一次加载全量。

`ready=true` 当且仅当当前至少一个成员 `schedulable=true`。该字段是运行态派生展示，不落 `lobby_cluster` 表。

#### 3.4.2 原子变更 server 归属

新增：

`POST /admin/v2/server-placement-transfers`

请求：

```json
{
  "serverId": "lobby-1",
  "target": {"kind": "lobby_cluster", "id": 12},
  "reason": "调整为全局大厅"
}
```

`target.kind` 仅允许 `lobby_cluster` 或 `zone`；`target=null` 表示解除当前大厅归属并转为未分配。该端点只处理以下范围：

- 未分配 backend → LobbyCluster；
- Zone → LobbyCluster；
- LobbyCluster → Zone；
- LobbyCluster → 未分配。

Zone → Zone 继续走既有换区工单，不借本端点绕过身份重确认；未分配 → Zone 继续走既有首次分配。请求不在上述集合内返回 409。

成功响应示例：

```json
{
  "serverId": "lobby-1",
  "namespaceId": 1,
  "placementKind": "lobby_cluster",
  "lobbyClusterId": 12,
  "zoneId": null,
  "isDefaultEntry": false,
  "draining": false
}
```

处理顺序：

1. 校验请求、server、目标和 namespace。
2. 读取当前归属；目标与当前相同则幂等返回现有视图。
3. 对真实变更执行排空门：运行态为 online 且 `playerCount>0` 时返回 409；degraded/lost/offline 或 `playerCount=0` 不因本门被拒。
4. 单事务锁定 server 行，再次校验当前归属未漂移。
5. 清旧归属和默认入口标志，设置新 `zone_id` 或 `lobby_cluster_id`，写专项审计。
6. 提交成功后再刷新受影响的健康/调度视图并唤醒拓扑/目录订阅；提交失败不发送任何通知。

响应：200 返回更新后的 server 富化视图，至少含 `serverId,namespaceId,placementKind,lobbyClusterId,zoneId,isDefaultEntry,draining`。

### 3.5 错误码

| HTTP | code | 条件与安全消息 |
|---|---|---|
| 400 | `INVALID_PARAM` | 参数、目标 kind、reason 或分页非法 |
| 404 | `LOBBY_CLUSTER_NOT_FOUND` | LobbyCluster 不存在 |
| 404 | `INSTANCE_NOT_FOUND` | server 不存在 |
| 409 | `LOBBY_CLUSTER_NAMESPACE_CONFLICT` | server 与目标 LobbyCluster 不在同 namespace |
| 409 | `LOBBY_MEMBER_ROLE_INVALID` | 非 backend 试图加入大厅 |
| 409 | `SERVER_PLACEMENT_CONFLICT` | 当前/目标归属不属于本端点允许的迁移集合，或事务内发现归属已变化 |
| 409 | `ZONE_SERVER_ONLINE_NONEMPTY` | server 在线且仍有玩家，须先排空或等待玩家离开 |
| 409 | `LOBBY_CLUSTER_NOT_READY` | 仅供 RC/生产 readiness 门返回；空集群配置本身不报错 |

错误统一走现有错误出口，保留脱敏后的真实原因和 traceId；前端必须 toast/就地展示，禁止吞错。

### 3.6 审计与事务

建议新增专项动作：

- `lobby_cluster.member.assign`：未分配 → 大厅；
- `lobby_cluster.member.move_in`：Zone → 大厅；
- `lobby_cluster.member.move_out`：大厅 → Zone；
- `lobby_cluster.member.unassign`：大厅 → 未分配。

审计 detail 只记录 serverId、源/目标类型与 ID、操作原因，不写 token、地址覆盖或玩家隐私。归属写与审计必须同事务原子完成；审计失败即归属变更回滚。同值 no-op 不写审计。

### 3.7 兼容性

- 旧 server 行的 `lobby_cluster_id` 为 NULL，原 Zone/BCCluster 归属与默认入口不变。
- 既有 `/admin/v2/servers`、zone-tree 和 server 详情仅新增可选大厅归属字段；旧前端忽略未知字段。
- 既有 Zone 默认入口 API、`is_default_entry` 和 `zoneDefaultEntry` 发现字段继续保留。
- 配置中心 scope 链不新增 lobby 层；大厅 server 若需要配置，继续使用 global/server 等现有层级，不暗中改变配置解析。
- 旧 agent 不认识大厅字段也可继续注册，但不能提供 FR-200 的首次大厅落脚能力；RC 门要求相关 BC 升级到支持版本。

## 4. UX / 交互

- 用户任务：集群管理员查看每个 namespace 的大厅就绪状态，明确选择大厅成员，并在安全门允许时把 server 在大厅与业务区之间迁移。
- 进入路径：“集群”导航组 → “大厅集群” → `/lobby-clusters`。页面按环境/namespace 上下文筛选，不合并进“区服”页。
- 操作闭环：查看就绪摘要 → 打开 namespace 大厅详情 → 从同 namespace 的未分配或 Zone backend 中选择 server → 查看源归属、在线玩家数和影响摘要 → 填写原因并确认 → 后端原子迁移 → 刷新成员、就绪态和审计结果。
- 破坏性操作：Zone↔LobbyCluster 迁移和移除成员必须展示影响摘要；在线非空被 409 拒绝时明确提示“请先排空或等待玩家离开”。不得自动踢人、自动 drain 或把失败伪装为成功。
- 空态：LobbyCluster 存在但无成员时展示“大厅集群未就绪”，给出“添加大厅服务器”主操作；不得自动选服。
- 加载态：摘要与成员分页分别显示骨架；切换 namespace 时保留页面结构，避免整页闪空。
- 错误态：列表/详情加载失败显示脱敏真因与重试；写失败保留对话框输入并展示原因。
- 常规态：展示成员总数、可调度数、最近更新、容量/健康摘要及成员分页。
- 超大量态：覆盖 1200+ server 候选与成员；搜索、状态筛选、分页均由服务端执行，不在浏览器全量过滤。
- IA 挂载：`docs/UX.md` 的“集群”大域新增独立页面；路由、导航、页眉、面包屑、命令面板与 i18n 同步。
- mockup 门：先在 devmock 构造空、常规、超大量、异常四态，产出可点击 `/lobby-clusters` mockup；必须经用户浏览器评审确认后，才允许接真实 API。评审前不得把静态稿当已验收页面。

## 5. 任务拆分

- [ ] 基线：运行并记录控制面 v2 authority、管理台集群页与 devmock 相关测试，确认改动前全绿。
- [ ] ADR：实施前新增并确认 LobbyCluster 权威、Zone/LobbyCluster 互斥迁移、排空门及旧默认入口职责变化的 ADR；同步被取代决策的状态。
- [ ] mockup：先写 devmock 四态和页面交互测试，再完成可点击 `/lobby-clusters` mockup；经用户浏览器评审确认后才能继续接真。
- [ ] 测试先红：模型/迁移测试覆盖 namespace 唯一、升级空集群回填幂等、新建 namespace 同事务建集群、旧 server 不被自动迁入。
- [ ] 最小实现：新增 `lobby_cluster`、`server.lobby_cluster_id`、AutoMigrate 与幂等回填，不增加名称/策略等额外字段。
- [ ] 测试先红：service 测试覆盖角色/namespace/互斥、同值 no-op、四种允许迁移、Zone→Zone 拒绝、在线非空 409、玩家 0/离线放行、事务回滚与审计。
- [ ] 最小实现：实现只读查询、`server-placement-transfers`、事务锁与提交后通知。
- [ ] 测试先红：handler/集成测试覆盖 API 形状、分页上限、readonly 拒写、错误码、真 MySQL 迁移与并发唯一性。
- [ ] 最小实现：接入 v2 路由、错误出口、审计覆盖表与 server 富化视图。
- [ ] 前端接真：在已确认 mockup 上接 API，覆盖写失败保留输入、409 专属提示、分页/筛选和审计可见性。
- [ ] 独立复核：审查是否存在第二真源、Zone/Lobby 双挂、无审计写、N+1 查询、非中文日志/注释或未授权的自动选服。
- [ ] 门禁：运行 Go 格式化、静态检查、全量相关单测/集成测试；运行前端 lint、类型检查、build、vitest 与 devmock 契约测试。
- [ ] 文档同步：PRD 状态、ARCHITECTURE、UX、API、ADR 索引、CHANGELOG；规格验收完成后再把状态改为已交付。

## 6. 验收标准

- 每个已有和新建 namespace 在 DB 中恰有一个 LobbyCluster；重复启动/迁移不产生重复行。
- 无 server 的 namespace 可按既有入口删除，空 LobbyCluster 与 namespace 同事务删除；存在大厅成员时仍由 server 占用保护拒绝。
- 升级后所有 LobbyCluster 初始为空，旧 Zone server、小区默认入口和在线 server 均未被自动迁入。
- 仅同 namespace backend 能加入；proxy、跨 namespace、Lobby/Zone 双挂均被服务端拒绝且不落库、不审计。
- Zone→LobbyCluster、LobbyCluster→Zone、LobbyCluster→未分配在玩家数为 0 或离线时单事务成功；在线且玩家数大于 0 返回 409 `ZONE_SERVER_ONLINE_NONEMPTY`，数据与审计均无变化。
- 真实变更与专项审计原子完成；同值请求幂等成功且不新增审计。
- 空集群可正常展示和配置，但页面明确标“未就绪”，RC/生产 readiness 门失败；至少一个可调度成员出现后转“已就绪”。
- `/lobby-clusters` 完成空、常规、1200+、错误四态，并在接真 API 前取得可点击 mockup 浏览器评审确认。
- 管理台在操作失败时展示脱敏真实原因；分页、筛选和搜索不会一次加载 namespace 全部 server。
- 既有 Zone 默认入口模型、API、UI、发现字段和业务插件消费不回归。
- 全部相关自动化门禁通过；没有以局部测试代替真 MySQL 迁移和浏览器交互验证。

## 7. 风险 / 已拍板决定

已拍板：

- LobbyCluster 是独立一等模型，不属于任何大区或小区。
- 每个 namespace 恰好一个 LobbyCluster；只允许同 namespace Bukkit/Paper backend 成为成员。
- LobbyCluster 与 Zone 归属互斥，BC 实例不属于 LobbyCluster。
- 允许空集群，但空/无可调度成员阻断 RC/生产验收。
- 升级只建空集群，管理员必须显式选择成员。
- 归属迁移复用排空安全门；Beacon 不负责踢人、转移玩家或自动 drain。
- 小区默认入口继续保留给业务插件，不随本 FR 删除。

风险与实施约束：

- v2 当前把 backend `zone_id=NULL` 统一视为 `unassigned`；若只加模型而未同步健康/调度判定，大厅成员会全部不可调度。FR-199 的事实字段与 FR-200 的候选消费必须在同一发布线闭环验证。
- 当前 v2 换区工单对已分配 server 采用解绑重确认；大厅迁移要求排空后原子切换，是新的明确特例。必须先由 ADR 锁定边界，禁止在 service 中静默改变全部 Zone→Zone 语义。
- readiness 是派生运行态，不能落库形成与健康视图竞争的第二真源；具体健康与 schedulable 计算继续引用 [v2-metrics-health-scheduling.md](v2-metrics-health-scheduling.md)。
- 页面是新 IA，mockup 未确认前不得接真或声称 UX 已验收。
