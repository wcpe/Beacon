# 规格：区服权威模型与未分配 Agent 分配（第二版）

> 状态：草拟 · 关联 FR：FR-142, FR-143 · 阶段：P3（0.23.x）

## 1. 背景与目标

第一版的环境 / 集群 / 区 / 服关系靠配置文件硬维护，扩区换区不可审计。第二版要建立清晰的 namespace / 环境 / BC 集群 / 大区 / 小区 / 子服权威模型（PRD §1.2、FR-142），并让未分配 agent 在后台可见、可批量分配到区服结构中（FR-143）。

本规格是**基座 §3 全部核心实体的权威表结构定义**：其余 v2 规格引用本文的表名与字段，不得复制或另定。zone 归属由控制面 DB 权威指派、serverId 由 agent 上报（ADR-0004 延续，见 `.claude/rules/architecture-invariants.md` §6）。

## 2. 范围

**做**：

- 基座 §3 全部实体（namespace / namespace_trust / env / bc_cluster / region / zone / server / agent_identity）的权威表结构：字段、类型、约束、索引要点。
- env 与 namespace 的映射方式。
- 未分配 agent 的呈现与批量分配流程（/zones 页）。
- 换区工单：已分配 server 改归属（backend 换小区 / proxy 换集群）的解绑重确认编排（发起端点与流程在本文，身份状态机归 v2-agent-identity.md）。
- 默认入口（`is_default_entry`）语义。
- server 排空标记（`draining`）列与切换端点（收编自调度域诉求，消费方为 schedulable 判定）。
- 分配变更对调度候选与拓扑的影响契约。
- 区结构（bc_cluster / region / zone）增删改约束。
- 管理面 API 端点表。

**不做**：

- namespace 隔离语义、trust capability 枚举与信任流程 → [v2-namespace-isolation.md](v2-namespace-isolation.md)。
- agent_identity 状态机、注册确认 / 解绑 / 换区流程 → v2-agent-identity.md（本文只锁该表列形态）。
- schedulable 判定与调度候选计算 → v2-metrics-health-scheduling.md（本文只提供归属事实）。
- 配置作用域链的合并解析 → v2-config-center.md（作用域五层引用本文层级）。
- 拓扑图渲染与链路数据 → v2-connection-message-storage.md。

## 3. 数据模型（权威定义）

### 3.1 通用命名与类型约定（各规格建表一律遵守）

- 表名：**单数 snake_case**（GORM 实体显式 `TableName()`），与基座 §3 骨架一致。
- 主键：`id` BIGINT 自增，用 GORM 抽象（`gorm:"primaryKey"`），不写方言 type tag。
- 外键列：`<实体>_id` BIGINT；**不建 DB 级外键约束**，引用完整性由 service 层在事务内校验（可移植、避免跨方言级联行为差异）。
- 枚举：VARCHAR + 应用层校验；json：TEXT；布尔：BOOL；时间：`created_at` / `updated_at` DATETIME，UTC。
- 唯一约束用 GORM `uniqueIndex`；禁 MySQL 专有特性（ENUM/SET/JSON 列、分区表语法），必须能切 Postgres。

### 3.2 `namespace` —— 强隔离边界

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| id | BIGINT | PK 自增 | |
| name | VARCHAR(64) | NOT NULL，唯一索引 | 全局唯一；agent 配置与请求携带此名，**创建后不可改名** |
| description | VARCHAR(255) | 可空 | 展示用途说明 |
| access_token_hash | VARCHAR(64) | NOT NULL | namespace 级接入 token 的 sha256 小写 hex（基座 §2）；明文仅创建 / 轮换时返回一次，不落库 |
| created_at / updated_at | DATETIME | NOT NULL | |

索引要点：`uniq(name)`。

### 3.3 `namespace_trust` —— 互通信任关系

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| id | BIGINT | PK 自增 | |
| from_namespace_id | BIGINT | NOT NULL | 发起方（能力的使用者） |
| to_namespace_id | BIGINT | NOT NULL | 授予方（能力作用的目标域）；单向，双向需两条记录 |
| capability | VARCHAR(32) | NOT NULL | 能力范围；**取值枚举与语义由 [v2-namespace-isolation.md](v2-namespace-isolation.md) §3 权威定义** |
| status | VARCHAR(16) | NOT NULL | `active` / `revoked`；应用层校验 |
| note | VARCHAR(255) | NOT NULL | 建立原因（必填，入审计） |
| granted_by | VARCHAR(64) | NOT NULL | 授予操作者 |
| granted_at | DATETIME | NOT NULL | |
| revoked_by | VARCHAR(64) | 可空 | 收回操作者 |
| revoked_at | DATETIME | 可空 | |
| revoke_reason | VARCHAR(255) | 可空 | 收回原因（收回时必填） |
| created_at / updated_at | DATETIME | NOT NULL | |

索引要点：`uniq(from_namespace_id, to_namespace_id, capability)` ——同一三元组只保留一行；收回置 `revoked`，重新授予**复用同一行**置回 `active`（并更新 granted_*），历史流水由审计承载。另建 `idx(to_namespace_id)` 供反向查询。

### 3.4 `env` 与 `env_namespace` —— 展示维度映射

`env`：

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| id | BIGINT | PK 自增 | |
| name | VARCHAR(64) | NOT NULL，唯一索引 | 如「生产」「测试」 |
| description | VARCHAR(255) | 可空 | |
| created_at / updated_at | DATETIME | NOT NULL | |

`env_namespace`（映射连接表，env → 1..N namespace，即「namespace 或 namespace 分组」的统一表达）：

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| id | BIGINT | PK 自增 | |
| env_id | BIGINT | NOT NULL | |
| namespace_id | BIGINT | NOT NULL，**唯一索引** | 一个 namespace 至多属于一个 env（避免展示重复计数） |
| created_at | DATETIME | NOT NULL | |

env 的定位（对齐 PRD §8 术语表）：**纯展示 / 过滤维度**。env 不参与隔离判定、不参与调度、不进配置作用域链（作用域链是 namespace → bc_cluster → region → zone → server 五层，见基座 §3）。前端顶栏按 env 过滤视图时，等价于按其映射的 namespace 集合过滤。

### 3.5 `bc_cluster` / `region` / `zone` —— 区服结构三层

`bc_cluster`：

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| id | BIGINT | PK 自增 | |
| namespace_id | BIGINT | NOT NULL | 所属隔离域 |
| name | VARCHAR(64) | NOT NULL | |
| description | VARCHAR(255) | 可空 | |
| created_at / updated_at | DATETIME | NOT NULL | |

索引要点：`uniq(namespace_id, name)`。

`region`：

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| id | BIGINT | PK 自增 | |
| bc_cluster_id | BIGINT | NOT NULL | |
| name | VARCHAR(64) | NOT NULL | |
| description | VARCHAR(255) | 可空 | |
| created_at / updated_at | DATETIME | NOT NULL | |

索引要点：`uniq(bc_cluster_id, name)`。

`zone`（调度单元）：

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| id | BIGINT | PK 自增 | |
| region_id | BIGINT | NOT NULL | |
| name | VARCHAR(64) | NOT NULL | |
| description | VARCHAR(255) | 可空 | |
| created_at / updated_at | DATETIME | NOT NULL | |

索引要点：`uniq(region_id, name)`。另有应用层约束：**zone 名在其所属 namespace 内唯一**（建 zone / 改名时沿 region → bc_cluster 链在事务内校验，冲突 409）——这是调度按「namespace + zone 名」寻址的前提（v2-metrics-health-scheduling.md §4.6），部分唯一 / 跨表唯一索引不可移植，故走应用层（见 §8-8）。

三层均不存 namespace_id 冗余（除 bc_cluster 外），namespace 归属沿 zone → region → bc_cluster 链推导；跨层查询在 service 层 join，禁止应用侧循环逐条查（N+1）。

### 3.6 `server` —— 子服 / BC 节点

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| id | BIGINT | PK 自增 | |
| namespace_id | BIGINT | NOT NULL | 注册时由 token 对应 namespace 锁定，永不跨域 |
| server_id | VARCHAR(64) | NOT NULL | agent 上报的运维可读标识；`uniq(namespace_id, server_id)` |
| kind | VARCHAR(16) | NOT NULL | `proxy` / `backend`；应用层校验 |
| bc_cluster_id | BIGINT | 可空 | **仅 kind=proxy 使用**：proxy 节点归属的 BC 集群；空 = 未分配 |
| zone_id | BIGINT | 可空 | **仅 kind=backend 使用**：子服归属小区；空 = 未分配（ADR-0004：由控制面 DB 权威指派） |
| pending_zone_id | BIGINT | 可空 | **仅 kind=backend 使用**：换区工单预填目标小区（§4.7）；非空 = 换区中，此时 zone_id 必为 NULL；重确认落区或确认「暂不分配」后清空 |
| pending_bc_cluster_id | BIGINT | 可空 | **仅 kind=proxy 使用**：换集群工单预填目标集群（§4.7）；语义同 pending_zone_id |
| is_default_entry | BOOL | NOT NULL 默认 false | 默认入口标记，语义见 §4.4；仅 kind=backend 且已分配 zone 时可为 true |
| draining | BOOL | NOT NULL 默认 false | 排空标记：排空中不再接受新调度、存量玩家不受影响；消费方为调度 schedulable 判定（v2-metrics-health-scheduling.md §4.5），切换端点见 §5（收编自调度域） |
| created_at / updated_at | DATETIME | NOT NULL | |

索引要点：`uniq(namespace_id, server_id)`、`idx(zone_id)`、`idx(bc_cluster_id)`。

应用层不变量（service 层事务内强制）：

- kind=proxy ⇒ zone_id 必为 NULL 且 is_default_entry=false；kind=backend ⇒ bc_cluster_id 必为 NULL。
- 分配目标（zone / bc_cluster）的 namespace 归属链必须与 server.namespace_id 一致，不一致拒绝（隔离硬规则见 [v2-namespace-isolation.md](v2-namespace-isolation.md) §4）。
- is_default_entry=true ⇒ zone_id 非空；解除分配时自动清为 false。
- pending_zone_id 非空 ⇒ kind=backend 且 zone_id 为 NULL；pending_bc_cluster_id 非空 ⇒ kind=proxy 且 bc_cluster_id 为 NULL；预填目标的 namespace 归属链校验与正式分配相同。

server 行由首次注册人工确认通过时创建（流程归 v2-agent-identity.md）；行的删除属身份域服务器资产操作，删除前必须先解除分配（zone_id / bc_cluster_id 为空）。在线 / 健康状态是 Go 进程内存真源，不落本表（基座 §1）。

### 3.7 `agent_identity` —— 身份绑定（仅锁列形态）

| 字段 | 类型 | 约束 | 说明 |
|---|---|---|---|
| id | BIGINT | PK 自增 | |
| identity_id | VARCHAR(64) | NOT NULL，唯一索引 | agent 首启生成（UUID 文本） |
| namespace_id | BIGINT | NOT NULL | |
| server_id | VARCHAR(64) | NOT NULL | 与 `server.server_id` 同值域；`idx(namespace_id, server_id)` |
| status | VARCHAR(16) | NOT NULL | **状态机与取值由 v2-agent-identity.md 权威定义**，本文只锁列形态 |
| bound_at | DATETIME | 可空 | 绑定（人工确认通过）时间 |
| created_at / updated_at | DATETIME | NOT NULL | |

约束要点：同一 `(namespace_id, server_id)` 同时至多一条处于绑定生效态——用应用层事务校验实现（部分唯一索引不可移植，禁用）。

## 4. 机制与状态机

### 4.1 env 映射机制

- env 增删改与「设置映射」均为管理面操作，入审计。
- 设置映射为整体替换语义（PUT 幂等）：提交 namespace_id 列表，服务端在事务内先删后插该 env 的映射行；被其他 env 占用的 namespace 报 409 并指明冲突方。
- 删除 env 不受映射保护（映射行级联删除，事务内完成）；env 消失只影响前端过滤视图，不影响任何权威数据。

### 4.2 未分配 agent 的呈现

- 定义：server 行存在（已人工确认）、但 backend 的 zone_id 为空或 proxy 的 bc_cluster_id 为空。**待确认的 agent 不在此列**——它们出现在 `/servers` 待确认列表（身份域）。
- `/zones` 页固定呈现「未分配」区（结构树外挂篮），按 namespace 过滤，展示 kind、serverId、确认时间、在线状态摘要；服务端搜索 / 筛选 / 分页（NFR：1000+ 子服）。
- 未分配 ⇒ 不可调度：schedulable=false、原因 `unassigned`（判定执行归 v2-metrics-health-scheduling.md，本文提供事实）。
- 换区中的服（pending_zone_id / pending_bc_cluster_id 非空）也在未分配区呈现，带「换区中 → 目标」标记；其身份同时出现在 `/servers` 待确认列表（重确认入口，归身份域）。

### 4.3 批量分配流程

1. 勾选 N 台未分配 server（**必须同 namespace、同 kind**，混合选择返回 400）。
2. 选目标：kind=backend → 小区（UI 按 BC 集群 → 大区 → 小区逐级导航定位；DB 落点只有 zone_id，不存在「只分配到大区」的中间态）；kind=proxy → BC 集群。
3. 可选：分配的同时勾选「设为默认入口」（仅 backend）。
4. 影响预览：目标小区 / 集群当前台数 → 变更后台数；提示配置作用域将随归属变化重算。
5. 确认后**单事务**写入全部 server 行的归属字段 + 审计条目；事务提交成功后才触发下行通知（agent 长轮询感知拓扑 / 配置作用域变化），与既有「先提交后唤醒」纪律一致。
6. 本接口只接受**未分配** server 的首次分配；已分配 server 传非 null 目标返回 409 `rezone_required`——已分配服改归属必须走换区工单（§4.7，解绑 + 重新人工确认），不存在后台直接改派通道。
7. 解除分配：同一接口 target 传 null，把归属字段置空并自动清 is_default_entry；同样需原因 + 审计。解除分配不涉及身份解绑（服转入未分配、不可调度）。

失败处理：目标不存在 / 已删除 → 404；namespace 不匹配 → 403；部分行校验失败 ⇒ 整批回滚（事务原子性），响应逐台列出失败原因，不做半成功。

### 4.4 默认入口（is_default_entry）语义

- 定义：小区内被标记为玩家兜底落点的子服（如 lobby）。一个 zone 可有 0..N 台默认入口；N>1 提供容错。
- 消费方约定：
  - 调度（P4）：请求无偏好或候选集按健康过滤后为空时，默认入口作为兜底候选优先级参考（具体算法归 v2-metrics-health-scheduling.md）。
  - BC 注入（P3 起）：控制面把小区默认入口列表随拓扑事实下发给同集群 proxy agent，供其维护代理侧优先连接目标；agent fail-static——控制面不可用时按本地快照继续。
- 某 zone 默认入口数为 0 时，`/zones` 页在该节点明示「无默认入口」提示（不阻断，只提醒）。
- 变更默认入口 = 写操作入审计；解除分配自动清标记（§3.6 不变量）。

### 4.5 分配变更的影响契约

zone_id / bc_cluster_id 变更在事务提交后产生三类下游效应（本文定义契约，执行归各域）：

1. **调度候选**：候选集合按新归属即时重算；未分配恢复 `unassigned` 排除原因（→ v2-metrics-health-scheduling.md）。
2. **配置作用域**：受影响 server 的有效配置按新五层链重算，控制面通知对应 agent 拉取（→ v2-config-center.md；P7 前无配置消费者时该通知为空操作）。
3. **拓扑**：`/topology` 与 `/zones` 树的归属展示立即反映（读 DB 权威数据，无缓存滞后要求超过一次轮询周期）。

### 4.6 区结构增删改约束

| 操作 | 约束 |
|---|---|
| 创建 bc_cluster / region / zone | 父级必须存在；name 在父级内唯一（§3.5 唯一索引），重名 409；zone 另须 namespace 内唯一（§3.5 应用层约束） |
| 改名 / 改描述 | 允许；作用域与归属引用均按 id，改名不破坏引用；入审计；zone 改名同样过 namespace 内唯一校验 |
| 删除 bc_cluster | 存在下级 region **或**挂有 proxy（bc_cluster_id 引用）→ 409 拒绝，响应列出阻断原因与数量 |
| 删除 region | 存在下级 zone → 409 拒绝 |
| 删除 zone | 存在 server（zone_id 引用）→ 409 拒绝；必须先迁移或解除分配 |
| 跨父级移动（region 换 cluster / zone 换 region） | **不支持**（见 §8）；需要时先建新节点、迁移服务器、删旧节点 |

全部结构变更入审计（操作者、对象、前后值）。

### 4.7 换区工单（已分配 server 改归属）

按 PRD §3 字面语义（2026-07-07 拍板，见 §8-9）：把已绑定的服从一个小区调到另一个小区（proxy 换集群同理）必须**解绑 + 重新人工确认**，不允许后台直接改写归属。为避免运维填两遍表单，采用「换区工单」编排：

1. **发起**：`POST /admin/v2/server-rezones` 勾选 1..N 台已分配 server（必须同 namespace、同 kind），选目标小区 / 集群（namespace 归属链校验同 §4.3），原因必填；影响预览展示离区 / 入区台数变化，并明示「发起后这些服将解绑、不可调度，直至重新人工确认」；二次确认后提交。
2. **解绑**：整批单事务内逐台完成——身份走 T10 解绑（v2-agent-identity.md §4.3，操作者 = 发起人）、清 zone_id / bc_cluster_id 并自动清 is_default_entry、写 pending 预填目标（§3.6）、审计 `zone.rezone.initiated`（含 identityId、原归属、预填目标、原因）；事务提交后内存注册表摘除实例。
3. **重入待确认**：agent 因绑定失效回退注册循环，自动重入 pending（身份域 T11，同三元组）；`/servers` 待确认列表对换区中条目醒目标注「换区中」与预填目标。
4. **重新人工确认**：管理员走身份域 approve（目标默认取预填、确认人可改，含改为「暂不分配」）；同一事务落区（或保持未分配）+ 清 pending 目标 + 审计 `zone.rezone.completed`（关联 identityId）；落区后按 §4.5 契约产生下游效应。
5. **换区期间不可调度**：身份非 active（`pending_confirm`）且归属已清（`unassigned`），复用 P4 既有原因码，不新增。
6. **取消 / 纠错**：不设独立取消端点——确认人改目标或确认「暂不分配」即覆盖；换区中的服重启 / 换机不影响工单（预填目标随 server 行保留）。

失败处理同 §4.3：整批事务原子，部分校验失败整批回滚、逐台列原因；目标不存在 → 404，namespace 不匹配 → 403，勾选含未分配服 → 400（未分配服走首次分配）。

## 5. API 契约（管理面 `/admin/v2/*`）

错误统一 `{code, message, traceId}`（基座 §2）；列表接口统一 `page` / `pageSize` / `keyword` 分页搜索参数。namespace 本体与信任关系端点见 [v2-namespace-isolation.md](v2-namespace-isolation.md) §5。

| 方法 | 路径 | 请求要点 | 响应要点 |
|---|---|---|---|
| GET | /admin/v2/envs | — | env 列表，含映射的 namespace 摘要 |
| POST | /admin/v2/envs | name, description | 新 env |
| PATCH | /admin/v2/envs/{id} | name?, description? | 更新后 env |
| DELETE | /admin/v2/envs/{id} | — | 204；映射级联删除 |
| PUT | /admin/v2/envs/{id}/namespaces | namespaceIds[] | 整体替换映射；冲突 409 |
| GET | /admin/v2/zone-tree | namespaceId | BC 集群 → 大区 → 小区树，各节点含服务器计数与默认入口计数，附「未分配」计数 |
| POST | /admin/v2/bc-clusters | namespaceId, name, description | 新集群 |
| PATCH | /admin/v2/bc-clusters/{id} | name?, description? | 更新后集群 |
| DELETE | /admin/v2/bc-clusters/{id} | — | 204；有子级 / 挂 proxy → 409 |
| POST | /admin/v2/regions | bcClusterId, name, description | 新大区 |
| PATCH | /admin/v2/regions/{id} | name?, description? | 更新后大区 |
| DELETE | /admin/v2/regions/{id} | — | 204；有子级 → 409 |
| POST | /admin/v2/zones | regionId, name, description | 新小区 |
| PATCH | /admin/v2/zones/{id} | name?, description? | 更新后小区 |
| DELETE | /admin/v2/zones/{id} | — | 204；挂 server → 409 |
| GET | /admin/v2/servers | namespaceId?, kind?, assigned?(bool), zoneId?, bcClusterId?, keyword?, page | server 分页列表（含归属、默认入口、在线摘要）；`assigned=false` 即未分配篮数据源 |
| POST | /admin/v2/server-assignments | serverIds[], target:{kind:`zone`/`bc_cluster`, id} 或 target:null（解除）, isDefaultEntry?, reason?（解除必填） | 批量首次分配（仅未分配 server）/ 解除；已分配 server 传非 null 目标 → 409 `rezone_required`（走换区工单）；整批事务，失败逐台列原因 |
| POST | /admin/v2/server-rezones | serverIds[]（已分配、同 namespace 同 kind）, target:{kind:`zone`/`bc_cluster`, id}, reason（必填） | 批量发起换区工单（§4.7）：整批事务内解绑 + 清归属 + 记预填目标；失败逐台列原因 |
| PUT | /admin/v2/servers/{id}/default-entry | value(bool) | 更新默认入口标记；未分配 → 409 |
| PUT | /admin/v2/servers/{serverId}/draining | draining(bool), reason | 切换排空标记（路径与语义收编自调度域，保持不变），写审计；消费方为调度 schedulable 判定 |

agent 面（`/beacon/v2/agent/*`）不暴露任何改归属接口——zone 归属只能由控制面指派（ADR-0004；agent 不声明 zone）。

## 6. 与其他规格的边界

| 依赖方向 | 内容 |
|---|---|
| 本文 → v2-namespace-isolation.md | namespace / namespace_trust 表结构在本文；capability 枚举、隔离硬规则、信任流程、/namespaces 页 API 在彼处 |
| 本文 → v2-agent-identity.md | agent_identity 列形态在本文；状态机、注册确认、解绑 / 冲突流程、换区重确认（approve 按预填目标落区）、server 行创建与删除在彼处；换区工单的发起编排与端点在本文 §4.7 |
| 本文 → v2-metrics-health-scheduling.md | 本文提供 zone_id / bc_cluster_id / is_default_entry / draining / 未分配事实，以及「zone 名 namespace 内唯一」约束（按名寻址前提）；schedulable 判定、候选算法、`unassigned` 排除原因输出在彼处 |
| 本文 → v2-config-center.md | 五层作用域链引用本文层级实体；分配变更后的配置重算与下发在彼处 |
| 本文 → v2-connection-message-storage.md | 拓扑展示消费本文归属数据 |

## 7. 验收标准（对齐 PRD FR-142 / FR-143 验收摘要）

1. 全部 §3 表结构经 GORM 迁移在 MySQL 与 sqlite（e2e 基线）建表成功，无方言专有语法；枚举列均 VARCHAR、无 JSON/ENUM 列。
2. 同 namespace 内 server_id 唯一约束生效；不同 namespace 允许同名 serverId。
3. 已确认未分配的 agent 出现在 `/zones` 未分配区，schedulable=false 且原因为 `unassigned`。
4. 可一次勾选多台同 kind 未分配 server 批量分配到小区 / BC 集群；混合 kind 或跨 namespace 目标被拒绝；整批事务原子（构造一台失败可见整批回滚）。
5. 分配结果实时反映到 `/zones` 树、`/servers` 列表与拓扑数据（一次轮询周期内）；调度候选按新归属计算（P4 联验）。
6. 默认入口可在分配时或分配后设置；解除分配自动清除标记；同集群 proxy agent 能收到默认入口列表下发。
7. 删除挂有 server 的 zone、有下级的 region / bc_cluster 均返回 409 并说明阻断原因；结构增删改与分配 / 解除 / 换区工单全部产生审计条目（含原因字段）。
8. env 映射为整体替换语义；一个 namespace 归属第二个 env 时返回 409；env 过滤不影响任何权威数据。
9. 已分配 server 经 `server-assignments` 传新目标被 409 `rezone_required` 拒绝；经换区工单发起后：身份转 unbound、归属清空、不可调度（`pending_confirm` + `unassigned`）、`/zones` 未分配区显示「换区中 → 目标」；agent 自动重入 pending；重新人工确认（目标预填可改）后落区恢复 active；`zone.rezone.initiated` / `zone.rezone.completed` 与 `identity.unbound` / `identity.approved` 审计齐全且可关联 identityId。

## 8. 风险 / 待定（默认决定待拍板）

1. **proxy 归属字段扩展骨架**：基座 §3 的 server 只列了 zone_id，本文增加 `bc_cluster_id`（kind=proxy 的归属落点）——否则 proxy 节点无处挂靠。已收口：按 kind 双挂（proxy→bc_cluster_id、backend→zone_id）以本文为全仓权威，各引用规格（identity / metrics / delivery / file-assets）已对齐。
2. **「分配到大区」解释**：FR-143 的「分配到大区 / 小区 / 默认入口」按「大区 = UI 导航粒度，DB 落点只有 zone / bc_cluster，无中间态」处理；若要求 backend 可停留在「仅分配到大区」态，需扩 server 表并重定义调度语义。
3. **跨父级移动不支持**：region / zone 不支持整体挪到另一父级（先建后迁再删）；若运维强诉求整体迁移，需另行设计带影响预览的迁移事务。
4. **默认入口 0..N 且 0 不阻断**：仅页面提示，不做告警；是否在 P4 健康域升级为告警待定。
5. **一个 namespace 至多属一个 env**：为避免展示重复计数取此默认；PRD 术语「namespace 分组」按「一个 env 映射多个 namespace」满足。
6. **不建 DB 外键**：引用完整性全部走 service 层事务校验，换取可移植与删除保护逻辑统一；接受孤行风险由事务纪律兜底。
7. **server 行删除入口**：归身份域（v2-agent-identity.md）定义；本文只约束「删除前必须已解除分配」。
8. **zone 名 namespace 内唯一（应用层校验）**：为支撑调度按 zone 名寻址（v2-metrics-health-scheduling.md §8-8），在 `uniq(region_id, name)` 之上增加应用层约束「namespace 内 zone 名唯一」，建 zone / 改名时校验——默认决定待拍板；若拍板否决，调度须改为携带 region 限定或 zoneId 寻址。
9. **换区字面语义——已拍板**：2026-07-07 拍板按 PRD §3 字面执行——已分配服改归属必须解绑 + 重新人工确认（宽松解读「受控改派不解绑」被否），落地为 §4.7 换区工单。**遗留边界（默认决定）**：「解除分配 → 再按未分配首次分配」两步组合可达成换区效果而不经重确认（两步各自有原因 + 审计 + 二次确认）；默认允许，若需封死须把曾分配服的再分配也限制为换区工单。
