# Beacon REST 契约 · 第二版（现行）

> 本文是**第二版（现行）契约**（2026-10-01 自原合并文档拆出）。
> 第一版（Legacy，维护态冻结）契约见 [API-v1-legacy](API-v1-legacy.md)。


> 第二版 REST 与 agent-api 契约的入口文档。**端点明细契约（请求 / 响应体、状态机、错误码）的单一真源在各 `docs/specs/v2-*.md` 规格的「§5 API 契约」章节，本文不复制**——本文只承载三样：跨域通用约定（本文为权威真源）、按域端点索引（方法 / 路径 / 一句话用途 + 指回权威规格）、契约治理规则。

## 通用约定（本章为权威真源）

各 v2 规格的端点必须遵守以下约定；规格与本章冲突时以本章裁决为准并回改规格（见文末「契约治理」）。

### base path 分面

| 面 | base path | 说明 |
|---|---|---|
| 管理面 | `/admin/v2/*` | 管理台 / 脚本 / 外部服务；归档域挂 `/admin/v2/archive/*` |
| agent 面 | `/beacon/v2/agent/*` | agent 控制通道（注册 / 上报 / 调度 / 消息 / 资产 / 交付回执），沿用 Legacy `/beacon/v1/agent` 前缀惯例 |
| 流式数据面 | `/beacon/v2/stream/*` | 大文件流式传输（不过命令通道），当前仅交付域 `/beacon/v2/stream/delivery/*`（[v2-delivery-orchestration](specs/v2-delivery-orchestration.md) §5.3） |

### 认证

- **管理面**：沿用第一版机制——登录令牌（`Authorization: Bearer`）或 API 密钥（`bk_` 前缀，`full` / `readonly` 两级角色，readonly 拒写）。
- **agent 面**：namespace 级接入 token 头 `X-Beacon-Token`（token↔namespace 一致性校验，规则见 [v2-namespace-isolation](specs/v2-namespace-isolation.md)）+ 绑定身份头 `X-Beacon-Identity`（注册期另带 `X-Beacon-Boot`）；身份确认前 agent 仅可调 register / registration 两端点，其余端点一律 403（细则见 [v2-agent-identity](specs/v2-agent-identity.md) §5.1）。
- **流式数据面**：同 agent 面双 header，另校验请求身份属于持有该 blob 引用的活动变更单（模板源仅可上传、目标仅可下载本单清单内 sha256）。

### 错误体 / 时间 / 状态码惯例

- 统一错误体 `{ "code": "...", "message": "...", "traceId": "..." }`；`message` 为脱敏后的真实原因（打码凭据、保留运维上下文），沿用 [ADR-0057](adr/0057-surface-desensitized-errors.md)。
- 时间一律 UTC（ISO-8601）；毫秒时间戳字段以 `...Ms` 后缀显式标注。
- 惯例状态码：批量异步上报受理 `202`（入队即回，写入队列满 `429` + 退避提示）；状态类长轮询无变化 `304`、队列类长轮询无消息 `204`；非法状态迁移 / 占用冲突 `409`；批量操作逐条结果用 207 风格（HTTP 200/207 + 响应体逐条 ok/code）。

### 分页

- 列表端点统一 query `page` / `pageSize`（+ 可选 `keyword`），响应 `{ "items": [...], "total": N }`。
- 例外：连接 / 消息明细查日期分表，用**游标分页 + 强制时间范围或精确 ID**（查询防护，[v2-connection-message-storage](specs/v2-connection-message-storage.md) §5.2）。
- 冷查询跨域参数 `includeArchived` / `from` / `to` 挂在各查询域自己的端点上，契约见 [v2-hot-cold-archive](specs/v2-hot-cold-archive.md) §4.4。

### 长轮询 / SSE / 命令通道

- **状态长轮询**（agent 面）：`GET /beacon/v2/agent/registration?wait=<sec>`，状态无变化超时 `304`（身份域）。
- **队列长轮询**（agent 面）：`POST /beacon/v2/agent/messages/poll`（`waitSec`），无消息超时 `204`（消息域）。挂起时长参数命名以各域规格为准。
- **SSE**（管理面实时进度）：`GET /admin/v2/change-orders/{id}/events`（交付域），断线后轮询可恢复。
- **agent 命令下发**沿用既有长轮询命令通道机制（ADR-0006 一脉），v2 各域只登记新命令类型（如 `asset-rescan` / `asset-read`），不另建通道；命令 payload 与审计 detail 不携带文件内容。

### 命名风格（含裁决记录）

- **路径**：kebab-case 复数资源名（`agent-identities`、`change-orders`、`config-files`）；子动作用 `/{id}/<动词>` 路径段（`/approve`、`/rollback`、`/token/rotate`），**不用 `:verb` 冒号风格**。
- **query 参数与 JSON 字段**：一律 camelCase（`namespaceId`、`pageSize`、`includeArchived`）。
- **枚举值 / 错误码等字段值**：snake_case 小写（`pending_approval`、`zone_not_found`、`cross_namespace`），不受 camelCase 约束；审计事件名用点分小写（`identity.approve`、`cross_namespace.*`）。
- **管理面 namespace 过滤参数**：统一 `namespaceId`（值为 namespace 主键）。观测端点可附加 `envId`：服务端将其解析为冻结的 namespace 集合；`envId` 与 `namespaceId` 同传时后者必须属于该 env，非法值为 `400 invalid_observation_scope`，失效或不匹配为 `409 observation_scope_stale`，不会回退全量。
- **观测 SSE 边界**：当前 `/admin/v2/change-orders/{id}/events` 与文件同步 SSE 是任务事件流，`/beacon/v1/agent/stream` 是数据面流；它们不属于 env/namespace 观测查询，不接受观测范围参数。新增管理面观测 SSE 时必须冻结 scope fingerprint，并在 env 映射漂移后发送 `observation-scope-stale` 后断开。
- **内容指纹**：v2 统一 `sha256`（全新通道，不沿用 Legacy md5）。

> 裁决记录：以上为各规格收口时的**多数派**用法。起草期少数规格用了 snake_case 参数（zone-authority / namespace-isolation / config-center 的 §5 表、hot-cold 的 `include_archived`）、`:verb` 冒号路径（namespace-isolation 的 `token:rotate` / `{id}:revoke`）与 `namespace` 过滤参数（metrics 的 sched-decisions、file-assets 管理面），均已按本裁决回改对应规格；**DB 列名仍为 snake_case，不受本约定约束**。

### agent-api（业务插件本机接口）

业务插件**禁止直连 Beacon HTTP**（直连不作为契约、随时可变），唯一入口是 agent 本机 `BeaconAgentApi` 门面（Kotlin；HTTP / JSON 只存在于适配器，[ADR-0005](adr/0005-agent-transport-codec-abstraction.md) 延续；fail-static 降级语义随门面，绝不阻塞 MC 主线程）：

- 调度 / 健康门面 `BeaconScheduling`（`acquireCandidate` / `candidatesInZone` / `healthOf` / `selfHealth` / `dataSource`）：真源 [v2-metrics-health-scheduling](specs/v2-metrics-health-scheduling.md) §5.3。
- **准入作用域（FR-244）**：`BeaconScheduling` 新增 `acquireCandidate(zone, purpose, scope)` / `candidatesInZone(zone, scope)` 两条 `default` 重载（缺省抛「不支持」，**绝不静默忽略作用域**），作用域类型为 `AdmissionScope`（OR 备选 / 备选内 AND / 逐项精确相等；本仓**不解释**任何 key 的语义，也**不**解释值结构）；结论三态由 `ScheduleResult.state()`（`CHOSEN` / `NO_CANDIDATE` / `UNAVAILABLE`）给出，`admissionExcludedCount()` 回报「因作用域被排除」的台数；候选视图经 `CandidateView.labels()` 带出该节点**自己声明**的标签。既有三个方法与两个构造器签名**一字未动**（接入方是反射调用；本次以 `javap` 人工逐条比对，仓内暂无自动化二进制兼容门禁）。语义细节见 [sched-admission-scope](specs/sched-admission-scope.md)（线上键名与三态），决策见 [ADR-0087](adr/0087-scheduling-admission-scope-over-self-declared-labels.md)（取代 ADR-0086 决策 4 的「不进入调度决策」一条）。
- 消息门面（`send` / `call` / `on` / `isAvailable`）：真源 [v2-connection-message-storage](specs/v2-connection-message-storage.md) §5.1。
- 节点自声明门面 `SelfDeclaration`（`declare`）：门面里**唯一的写面**——只能声明**自己节点**的容量与键值标签，改配置 / 改 zone / 写他人不可达；真源 [agent-self-declaration-runtime-refresh](specs/agent-self-declaration-runtime-refresh.md) §3.3，决策见 [ADR-0086](adr/0086-agent-self-declaration-narrow-write-surface.md)。
- 声明 / 标签的**读回**：发现门面返回的实例值对象 `ServiceInstance` 暴露 `capacity()` 与 `metadata()`（不可变副本，旧控制面缺键 → 空 map）。标签是**节点声明的只读事实**，门面**不解释任何 key 的语义**；键值上限对齐 FR-227（`key ≤ 32` / `value ≤ 128` / 单节点 `≤ 20`）。

## 端点索引（按域）

> 每域一表，只列方法 / 路径 / 一句话用途；请求响应体、错误码、状态机**以「权威规格」列为准，不在此复制**。阶段与版本线对齐 [ROADMAP](ROADMAP.md) §1 与 [PRD](PRD.md) §4 FR 表。P1 行仅列当前基础闭环已接端点，完整规格剩余端点在 P3 接真深化补齐。

| 域 | 阶段 | 对应 FR | 权威规格 | 端点数 |
|---|---|---|---|---|
| Agent 身份 | P1 · 0.21.x | FR-139/140/141 | [v2-agent-identity.md](specs/v2-agent-identity.md) §5 | 12 |
| namespace 隔离 | P1 · 0.21.x | FR-142 | [v2-namespace-isolation.md](specs/v2-namespace-isolation.md) §5 | 8 |
| 区服权威 | P1 · 0.21.x | FR-142/143 | [v2-zone-authority.md](specs/v2-zone-authority.md) §5 | 25 |
| 指标健康调度 | P4 · 0.24.x | FR-144/146/147/148 | [v2-metrics-health-scheduling.md](specs/v2-metrics-health-scheduling.md) §5 | 14 |
| 连接消息存储 | P5 · 0.25.x | FR-145/149/150 | [v2-connection-message-storage.md](specs/v2-connection-message-storage.md) §5 | 14 |
| 热冷归档 | P6 · 0.26.x | FR-151/152/153 | [v2-hot-cold-archive.md](specs/v2-hot-cold-archive.md) §5 | 6 |
| 配置中心 V2 | P7 · 0.27.x | FR-160/161 | [v2-config-center.md](specs/v2-config-center.md) §5 | 17 |
| 文件资产 V2 | P8 · 0.28.x | FR-163/164 | [v2-file-assets.md](specs/v2-file-assets.md) §5 | 14 |
| 交付编排 V2 | P9 · 0.29.x | FR-162/165/166/167/168/171 | [v2-delivery-orchestration.md](specs/v2-delivery-orchestration.md) §5 | 27 |
| 审批中心 | P10 RC · v1.1.0 | FR-207/212 | [dangerous-operation-approval-core.md](specs/dangerous-operation-approval-core.md) §7 | 13 |
| 生命周期：归档 / 永久删除 | P10 RC · v1.1.0 | FR-216/217/218 | [namespace-archive-and-restore.md](specs/namespace-archive-and-restore.md) §3.4 等 | 3 |
| 大厅集群 | P10 RC · v1.1.0 | FR-199 | [lobby-cluster-authority.md](specs/lobby-cluster-authority.md) §3.4 | 2 |
| BC 受管目录重同步 | P10 RC · v1.1.0 | FR-201 | [bc-managed-directory-resync.md](specs/bc-managed-directory-resync.md) §3.2 | 2 |

当前表内合计 157 个端点（按各域小节登记的端点条数计，含 env / 标签 / 大厅迁移等子表）；其中 P1 基础三段（Agent 身份 / namespace 隔离 / 区服权威）共 45 个，第二版全量规划仍以各规格为准。

### Agent 身份（P1 · 0.21.x，真源 [v2-agent-identity.md](specs/v2-agent-identity.md) §5）

agent 面：

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/beacon/v2/agent/register` | 携 identityId 注册 / 重注册，返回绑定状态 |
| GET | `/beacon/v2/agent/registration` | 长轮询当前身份状态（确认 / 拒绝秒级感知） |

管理面：

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/admin/v2/agent-identities` | 身份分页列表（状态 / namespace / 关键字筛选） |
| GET | `/admin/v2/agent-identities/{identityId}` | 单条身份详情（附 `conflictPeers` 与换区 `rezonePrefill` 预填目标） |
| POST | `/admin/v2/agent-identities/{identityId}/approve` | 确认接入（Q3 占用冲突须显式强制解绑；首次确认只创建未分配 server；换区中按预填 / 指定 `target` 落区或 `target:null` 暂不分配） |
| POST | `/admin/v2/agent-identities/{identityId}/reject` | 拒绝接入（原因必填） |
| POST | `/admin/v2/agent-identities/{identityId}/allow-reapply` | 申请恢复被拒身份的重新申请资格；返回 `202 + approvalRequestId` |
| POST | `/admin/v2/agent-identities/{identityId}/disable` | 禁用（摘除调度与指令下发） |
| POST | `/admin/v2/agent-identities/{identityId}/enable` | 恢复禁用身份 |
| POST | `/admin/v2/agent-identities/{identityId}/unbind` | 解绑（换 serverId / namespace 的前置） |
| POST | `/admin/v2/agent-identities/{identityId}/resolve-conflict` | 并发身份冲突处置（FR-177）：body `keepBootId`（保留哪个实例）+ `reason`（必填）；保留方恢复 active、落败方后续持续 409 并指引；非 conflict → 409、`keepBootId` 不在冲突双方 → 400 |
| PUT | `/admin/v2/agent-identities/{identityId}/endpoints/{endpointKey}` | 逐 listener 覆盖地址（FR-204）：body `{overrideAddress,reason}`；`overrideAddress=null` 清除覆盖并恢复探测值；地址须为单个 host:port（port 1..65535，IPv6 用方括号输出），非 active endpoint 只可清除旧覆盖、不得新增 |

> 逐 listener 覆盖契约（探测值 / 覆盖值 / 生效值及来源）真源为 [agent-address-detection-and-proxy-listeners.md](specs/agent-address-detection-and-proxy-listeners.md) §8.2（FR-204）；身份详情 `endpoints[]` additive 返回，旧 `lastAddr` 继续返回兼容地址。

### namespace 隔离（P1 · 0.21.x，真源 [v2-namespace-isolation.md](specs/v2-namespace-isolation.md) §5）

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/admin/v2/namespaces` | namespace 列表 |
| POST | `/admin/v2/namespaces` | 创建 namespace（返回一次性明文接入 token） |
| DELETE | `/admin/v2/namespaces/{id}` | 旧删除入口已迁移，统一返回 `410 namespace_delete_migrated`，不触发任何硬删、副作用或隐式审批 |
| PATCH | `/admin/v2/namespaces/{id}` | 更新 namespace 展示名与描述（唯一可写字段 `displayName` / `description`；`code` 为稳定标识不可改，改则 `400 IMMUTABLE_IDENTIFIER`）（FR-239） |
| POST | `/admin/v2/namespaces/{id}/token/rotate` | 轮换接入 token（FR-238）：新明文仅本次响应返回一次；旧 token 即刻失效，该域 agent 换用前一律 401 |
| GET | `/admin/v2/namespace-trusts` | 互通信任行列表 |
| POST | `/admin/v2/namespace-trusts` | 授予单向信任（新增或复活，原因必填） |
| POST | `/admin/v2/namespace-trusts/{id}/revoke` | 收回信任（原因必填，即时生效） |

### 区服权威（P1 · 0.21.x，真源 [v2-zone-authority.md](specs/v2-zone-authority.md) §5）

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/admin/v2/bc-clusters` | 新建 BC 集群 |
| PATCH | `/admin/v2/bc-clusters/{id}` | 改 BC 集群展示信息（局部更新 `displayName` / `description`；`code` 不可改） |
| DELETE | `/admin/v2/bc-clusters/{id}` | 删集群（204；含大区或已分配代理 → 409，不产生副作用） |
| POST | `/admin/v2/regions` | 新建大区 |
| PATCH | `/admin/v2/regions/{id}` | 改大区展示信息（局部更新；`code` 不可改） |
| DELETE | `/admin/v2/regions/{id}` | 删大区（204；含小区 → 409） |
| POST | `/admin/v2/zones` | 新建小区 |
| PATCH | `/admin/v2/zones/{id}` | 改小区展示信息（局部更新；`code` 不可改） |
| DELETE | `/admin/v2/zones/{id}` | 删小区（204；挂 server → 409） |

拓扑建树三个创建端点（`/bc-clusters`、`/regions`、`/zones`）的请求体约定，真源 [stable-business-identifiers-and-display-names.md](specs/stable-business-identifiers-and-display-names.md)：

- **父级字段**：统一别名 `parentId` 与各端点原有字段**都接受**（`/bc-clusters` 旧字段 `namespaceId`、`/regions` 旧字段 `bcClusterId`、`/zones` 旧字段 `regionId`）。`parentId` 与 MCP 建树工具（`beacon.topology.bc-clusters.create` / `regions.create` / `zones.create`，入参统一为 `parentId`）同名同义，照 MCP 文档写 HTTP 调用可直接对上；只给其一时取该值，两者都给且不一致 → `400 INVALID_PARAM`（点名冲突的两个字段），两者都缺省 → `400 INVALID_PARAM`（点名应传 `parentId` 及对应旧字段名）。
- **标识与展示名**：`code` 是稳定业务标识（创建后不可改），`displayName` 是可改展示名；`name` 为兼容旧调用方的历史字段，**取值必须与 `code` 完全相同**，展示名请用 `displayName`。只传 `code`（或只传 `name`）时以该值同时初始化 `code` 与 `displayName`；同时给 `name` 与 `code` 且不一致 → `400 AMBIGUOUS_IDENTIFIER`（错误信息直接说明 `name` 须等于 `code`、展示名改用 `displayName`）。

| GET | `/admin/v2/zone-tree?namespaceId=` | 区服结构树只读聚合（BC 集群 → 大区 → 小区，各节点带计数，附未分配计数） |
| GET | `/admin/v2/servers` | server 分页列表（富化视图：含归属名 / 默认入口 / 在线摘要；`assigned=false` 即未分配篮）；`lifecycleStatus=active\|archived\|all`，默认 `active` |
| GET | `/admin/v2/servers/{id}/lifecycle-impact?action=archive\|restore` | 读取有界脱敏影响预览；当前状态不满足动作前置条件返回 `409 server_not_active` 或 `server_not_archived`，不产生副作用 |
| PATCH | `/admin/v2/servers/{id}` | 更新 server 展示名（唯一可写字段 `displayName`；serverId 仍由身份确认流程创建并确定） **【FR-205】** |
| POST | `/admin/v2/server-assignments` | 批量分配申请（**经统一审批**：`202` + 票据 `{approvalRequestId,status,operationKey,secretReturned}`）。`target` 为对象即首次分配（仅未分配 server；`kind` 取 `zone` / `bc_cluster`，未分配→Zone 之外的组合仍走对应流程），`target` 显式 `null` 解除分配（原因必填）。**`target.kind=lobby_cluster` 直接受理大厅成员分配**：逐台转发到单服迁移入口（无需改调 `/server-placement-transfers`），响应为 `202` + `{approvalRequestId,status,operationKey,secretReturned,tickets:[{serverId,approvalRequestId,status,operationKey,secretReturned}]}`（`operationKey=topology.lobby_member.move`，`tickets` 按请求顺序逐台给票）；该路径 `isDefaultEntry` 必须为空/`false`（大厅成员不能是默认入口），`serverIds` 为空或含无效 id → `400 INVALID_PARAM` 并带修正指引。已分配服改归属须走换区工单 |
| POST | `/admin/v2/server-rezones` | 批量发起换区工单（已分配、同 namespace 同 kind）：单事务解绑清归属 + 写预填目标 + 身份重入 pending；未分配台 400 `not_assigned`，整批原子回滚 |
| PUT | `/admin/v2/servers/{serverId}/draining` | 设置 `draining=true` 为直接止损：立即写强审计并返回富化视图；设置 `draining=false` 为恢复调度，必须携带原因与 `Idempotency-Key`，返回 `202` 审批票据，批准 worker 执行后才恢复 |
| PUT | `/admin/v2/servers/{id}/default-entry` | 更新默认入口标记（路径为 server 行 id）；未分配小区 → 409 `not_assigned` |

服务器键值标签（FR-227，真源 [server-key-value-tags.md](specs/server-key-value-tags.md) §3）：`key=value` 单 key 单值，与 FR-29 发现过滤**同源**（`tag.<key>=<value>` 直读同一标签表）；低风险直执 + 强审计，不驱动调度 / 分配，不做继承，无自由文本标签。

| 方法 | 路径 | 用途 |
|---|---|---|
| PUT | `/admin/v2/servers/{serverId}/tags` | 按 key 增改标签（重复 key 即覆盖；**未出现的既有 key 保留**，故不能借本端点删标签，删除请用下一条；空 `tags` → 400；key 有格式与数量上限） |
| DELETE | `/admin/v2/servers/{serverId}/tags/{key}` | 删除单个标签（增 / 删 / 覆盖均写 `server.tag_updated` 类审计，含 key 与新旧值） |

大厅归属迁移（FR-199，真源 [lobby-cluster-authority.md](specs/lobby-cluster-authority.md) §3.4.2）：

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/admin/v2/server-placement-transfers` | 申请变更 server 归属（**经统一审批**：`202` + 票据，批准后由 worker 原子落库）：body `{serverId,target:{kind,id}\|null,reason}`，`target.kind` 仅 `lobby_cluster` / `zone`，`target:null` 解除大厅归属转未分配；只受理「未分配→大厅」「Zone→大厅」「大厅→Zone」「大厅→未分配」四种，Zone→Zone 仍走既有换区工单、未分配→Zone 仍走首次分配，集合外 → 409。**这是大厅归属的单服入口**（一次一台、`serverId` 为业务字符串）；批量场景可继续用 `POST /admin/v2/server-assignments` 的 `target.kind=lobby_cluster`，该端点会把数字 `serverIds` 逐台转发到这里，两者语义与审计口径完全一致 |

env 展示维度（FR-178 · P8 · 0.28.x）：纯展示 / 过滤维度，不参与隔离 / 调度 / 配置作用域链；映射整体替换、一个 namespace 至多属一个 env。

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/admin/v2/envs` | env 列表（含映射的 namespace 摘要） |
| POST | `/admin/v2/envs` | 创建 env（name 全局唯一，撞名 409 `ENV_CONFLICT`） |
| PATCH | `/admin/v2/envs/{id}` | 改 env 名 / 描述（局部更新；撞名 409，不存在 404 `ENV_NOT_FOUND`） |
| DELETE | `/admin/v2/envs/{id}` | 删 env（映射级联删除，204；只影响过滤视图、不动权威数据） |
| PUT | `/admin/v2/envs/{id}/namespaces` | 整体替换 env→namespace 映射（幂等；被其他 env 占用的 namespace → 409 `ENV_NAMESPACE_CONFLICT` 指明冲突方；不存在 namespace → 400 `ENV_NAMESPACE_NOT_FOUND`） |

### 指标健康调度（P4 · 0.24.x，真源 [v2-metrics-health-scheduling.md](specs/v2-metrics-health-scheduling.md) §5）

agent 面：

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/beacon/v2/agent/metrics/report` | 5s 批量上报 5s 桶聚合指标（兼活性信号，顺带回传自身健康）**【已实现·FR-144，`self` 已接真实健康视图·FR-147】** |
| GET | `/beacon/v2/agent/schedule/candidates` | 拉取本 namespace 各 zone 调度候选快照 **【已实现·FR-146 服务端】** |
| POST | `/beacon/v2/agent/schedule/decide` | 请求控制面做一次调度决策（产生 traceId）**【已实现·FR-146 服务端】** |
| POST | `/beacon/v2/agent/schedule/report-local` | 降级期本地决策恢复后补报（幂等）**【已实现·FR-146 服务端】** |

> **FR-244 准入作用域（已实现）**：`POST /beacon/v2/agent/schedule/decide` 新增可选请求键 `admissionScope`（`[{k:v,...}, ...]`，备选之间 OR / 备选之内 AND / 逐项精确相等，不 trim 不折叠大小写），在生成候选的那一刻按「候选节点自己声明过的标签」收窄候选；响应新增可选键 `admissionExcludedCount`（`omitempty`，只计因作用域被排除的台数），`failReason` 新增 `no_candidate_in_scope`（仅当全部候选都因作用域被排除；「部分作用域 + 部分健康」仍为 `no_candidate`），新增 `503 admission_unavailable`（作用域非空但本进程没有标签读取真源——判不了、可重试，**不忽略作用域、不报成没有候选**），决策明细 `excluded` 新增原因码 `admission_scope_mismatch`（先于健康原因）。`GET /beacon/v2/agent/schedule/candidates` 每台候选新增可选键 `labels`（对象），**缺键 = 真源未装配 / 空对象 = 该节点没声明过标签**：这是版本信号，调用方不得把缺键读成"没有声明"。真源只有一处（FR-243 声明端点写进注册表的那一份），**不新增第二真源**；语义与上限见 [sched-admission-scope](specs/sched-admission-scope.md) §3，决策见 [ADR-0087](adr/0087-scheduling-admission-scope-over-self-declared-labels.md)。

> **FR-144 采样入库已实现**：`POST /beacon/v2/agent/metrics/report` 挂 token↔namespace + identity 鉴权中间件（未确认身份 403），接收端只做校验 + 更 60s 内存窗口 + 非阻塞入队即回 `202 {accepted, deduplicated, self}`（请求 goroutine 不碰 DB），后台写入池攒批事务批插当日 `metric_sample_YYYYMMDD` 日表（唯一键 `(server_id,bucket_start_ms)` 幂等去重、跨日自动拆表、队列满回 `429 metrics_ingest_busy`、时钟偏移 >5min 回 `400 clock_skew_too_large`）。`samples[]` 为 agent 端已按 5s 桶聚合的批（含 `bucketStartMs`/`sampleCount`/各 `*Avg`/`*Max`/`*Min` 字段）。
>
> **FR-147 健康值模型已实现**：`self` 回传 `{score, level, schedulable, reasons[]}`（无视图时仍 `null`）；健康计算轮每 5s 锁外读 DB 事实 + 聚合 60s 内存窗口整批替换健康视图，>30s 无批判 `lost`；每 30s 全量视图经异步通道落 `health_snapshot_YYYYMMDD`。**FR-146 调度决策服务端已实现**：`decide` 在健康视图上纯内存 highest_score 决策（同分优先容量占用率低者再随机）、决策行异步落 `sched_decision_YYYYMMDD`（trace_id 唯一键幂等）；`report-local` 按 localTraceId 幂等补报（≤100 条/批）；候选与决策全程请求 goroutine 零 DB。**agent 侧客户端与 `BeaconScheduling` 门面（FR-148）已实现**：agent-api 纯 Java 8 门面 + core `SchedulingView`（连接级失败走本地快照 highest_score 降级、future 不异常完成不阻塞玩家链路）+ `SchedulingRefresher`（10s 拉候选刷缓存 + 原子落盘 `candidates-snapshot.json`、恢复后 report-local 补报）+ `selfHealth` 消费 metrics 响应 self 段；真机 fail-static e2e 见 [OPERATIONS](OPERATIONS.md) §7.6。

管理面：

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/admin/v2/metrics/summary` | 集群聚合概览（分角色 / level 分布 / schedulable 计数）**【已实现·FR-147】** |
| GET | `/admin/v2/metrics/series` | 单服 / 多服指标时序（服务端聚合，serverId 必填、跨日并表）**【已实现·FR-147】** |
| GET | `/admin/v2/health` | 全部服务器当前健康列表（内存实时）**【已实现·FR-147】** |
| GET | `/admin/v2/health/{serverId}` | 单服健康详情（因子分解 + 权重版本）**【已实现·FR-147】** |
| GET | `/admin/v2/health/snapshots` | 健康快照回放（查询侧不隐式建日表）**【已实现·FR-147】** |
| GET | `/admin/v2/sched-decisions` | 调度决策记录分页查询（from/to 必填、跨日并表）**【已实现·FR-146】** |
| GET | `/admin/v2/sched-decisions/{traceId}` | 单条决策详情（候选 / 排除原因 / 选择）**【已实现·FR-146】** |
| GET | `/admin/v2/sched-decisions/summary` | 决策概览（成功率 / 失败原因 Top / 降级占比）**【已实现·FR-146】** |
| GET | `/admin/v2/settings/health-weights` | 当前健康权重配置 + 历史 rev **【已实现·FR-147】** |
| PUT | `/admin/v2/settings/health-weights` | 全量替换健康权重（校验 → 镜像 + 新 rev + 审计 → 热更下轮生效）**【已实现·FR-147】** |

> agent-api 本机接口（`BeaconScheduling`）见其 §5.3；排空切换端点已收编至区服权威域（上表 `/servers/{serverId}/draining`），不重复。

### 连接消息存储（P5 · 0.25.x，真源 [v2-connection-message-storage.md](specs/v2-connection-message-storage.md) §5）

agent 面：

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/beacon/v2/agent/connections/batch` | proxy 批量上报连接 open / close 事件 **【已实现·FR-145】** |
| GET | `/beacon/v2/agent/player-roster` | 玩家位置名册只读查询（FR-31）：返回 `{namespace, count, players}`，**按鉴权身份只返回本域玩家**（强隔离；空名册为 `{}` 而非 404）；名册权威在控制面、由连接明细驱动 **【已实现·ADR-0063 决策 4】** |
| POST | `/beacon/v2/agent/messages/send` | 发送跨服消息（server / player / **broadcast** 寻址，广播可选 `targetZone` 做 zone 级定向）；payload 接受 object / array / string / number / boolean / null；Agent 先按 JSON 编码后的 UTF-8 字节数执行 64KB 前置校验，控制面再按中转 / 保存文本执行 64KB 硬校验；`msgType` 非空且 UTF-8 编码 ≤64 字节（冒号合法）。响应 `200 {messageId, status}` 的 `accepted` 仅表示**已受理入队**（送达与否以 `msg_trace` 终态行为准：目标未启用 messaging / 离线 → 限时内落 `expired`）。`messageId` 应为规范 UUIDv7；非 UUIDv7 仍受理（服务端只校验可解析性、不校验版本位，UUIDv4 合法），但终态行改按控制面接收时刻定日表（记 WARN），按 ID 直查走有界回退 **【已实现·FR-149/180】** |
| POST | `/beacon/v2/agent/messages/poll` | 长轮询拉取本服待投消息（无消息 204）；payload 往返保持 JSON 类型，string 保持业务原文且不做二次 JSON 编码，object / array / number / boolean 以 JSON 文本中转，null 表示无 payload **【已实现·FR-149】** |
| POST | `/beacon/v2/agent/messages/ack` | 批量回执投递结果 **【已实现·FR-149/150】** |

管理面：

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/admin/v2/connections` | 连接明细查询（强制精确 ID 或过滤 + 时间范围） **【已实现·FR-145】** |
| GET | `/admin/v2/connections/{connId}` | 单连接详情 **【已实现·FR-145】** |
| GET | `/admin/v2/connections/stats` | 连接 / 玩家流时间桶聚合 **【已实现·FR-145】** |
| GET | `/admin/v2/messages` | 消息元数据检索（**永不含 payload**；支持 `targetKind` 过滤，广播行输出 fan-out 聚合字段 `fanoutTotal`/`deliveredCount`/`failedCount`/`expiredCount`/`targetZone`）；`messageId` / `correlationId` 直查在首选日表未命中时按最近**真实**日表有界回退（约 8 个真实日表，晚于当前的未来日表不入窗口；覆盖 `message_id` 非规范 UUIDv7 的消息），未投递消息的终态（`expired` / `failed` + `failReason`）据此可查 **【已实现·FR-149/180】** |
| GET | `/admin/v2/messages/{messageId}` | 消息详情 + hops 链路（payload 仅元信息） **【已实现·FR-149】** |
| POST | `/admin/v2/messages/{messageId}/payload` | 旧 payload 正文入口，固定 `409 operation_requires_approval`；先通过专用审批申请，再由原申请主体消费一次性 grant **【FR-209】** |
| GET | `/admin/v2/messages/stats` | 异常链路聚合（拓扑页数据源；`groupBy=edge\|type`，独立 bucket 维度无契约与消费方、暂未提供） **【已实现·FR-149/156】** |
| POST | `/admin/v2/messages/{messageId}/payload/approval-requests` | payload 读取专用审批申请入口（body `{reason}` + `Idempotency-Key`，返回 `202 {requestId,status}`）；旧正文端点不隐式建申请 **【FR-209】** |
| POST | `/admin/v2/sensitive-access-grants/{grantId}/consume` | 原申请主体一次性消费已批准的敏感内容（消息 payload）：重校验原 Principal、批准事实、grant 状态与目标版本，任一不匹配失败关闭 **【FR-209】** |

> 上表末两行的契约真源为 [agent-command-and-sensitive-content-approval.md](specs/agent-command-and-sensitive-content-approval.md) §5（FR-209）；本域只登记入口，不复制 grant / 一次性消费与错误码细则，敏感执行状态摘要另见该规格的 `GET /admin/v2/agent-operations/{resultRef}`。

### 热冷归档（P6 · 0.26.x，真源 [v2-hot-cold-archive.md](specs/v2-hot-cold-archive.md) §5）

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/admin/v2/archive/overview` | 归档总览（目标库 / 各域水位与保留期） **【已实现·FR-151/153】** |
| POST | `/admin/v2/archive/jobs` | 创建归档任务（dry-run / 执行；有 running 409） **【已实现·FR-153】** |
| GET | `/admin/v2/archive/jobs` | 任务列表（status/mode/trigger 过滤 + 分页） **【已实现·FR-153】** |
| GET | `/admin/v2/archive/jobs/{id}` | 任务详情（逐域 item 进度与校验结果） **【已实现·FR-153】** |
| POST | `/admin/v2/archive/jobs/{id}/retry` | 失败任务断点续跑（仅 failed 否则 409） **【已实现·FR-153】** |
| POST | `/admin/v2/archive/jobs/{id}/cancel` | 取消任务（仅 pending/running 否则 409） **【已实现·FR-153】** |

> 保留期等设置走运维设置域端点（`archive.*` 键已入 `/admin/v1/settings` 白名单，≥7 天守卫，**【已实现·FR-151】**）；冷查询参数 `includeArchived=true`（强制时间范围 ≤ `archive.cold-query-max-days` 默认 31、归档不可达 503、应用层归并去重保热侧）已挂各查询域端点 `/audits`、`/admin/v2/{metrics/series,sched-decisions,health/snapshots,connections,messages}`（§4.4 跨域契约，**【已实现·FR-152】**），均不在 `/archive/*` 下。归档到同实例独立 database `beacon_archive`（或独立 DSN）走双连接应用层搬运 + sha256 校验门（[ADR-0066](adr/0066-hot-cold-archive-dual-connection.md)）。前端页面接真（归档清理块 / 冷查询「包含归档」勾选 / 设置页归档策略）随后续版本。

### 配置中心 V2（P7 · 0.27.x，真源 [v2-config-center.md](specs/v2-config-center.md) §5）

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/admin/v2/config-files` | 配置文件分页列表 **【已实现·FR-160/161】** |
| POST | `/admin/v2/config-files` | 创建配置文件（格式 / schema / 敏感路径） **【已实现·FR-160/161】** |
| GET | `/admin/v2/config-files/{id}` | 文件元数据 + 各层覆盖概览 **【已实现·FR-160/161】** |
| PATCH | `/admin/v2/config-files/{id}` | 更新描述 / schema / 敏感路径 **【已实现·FR-160/161】** |
| DELETE | `/admin/v2/config-files/{id}` | 移入回收站（软删除，版本链保留） **【已实现·FR-160/161】** |
| GET | `/admin/v2/config-files/trash` | 回收站分页列表 **【已实现·FR-160/161】** |
| POST | `/admin/v2/config-files/{id}/restore` | 从回收站恢复（名称被占用 409） **【已实现·FR-160/161】** |
| POST | `/admin/v2/config-files/{id}/purge` | 彻底删除（物理删除连带版本链，原因必填） **【已实现·FR-160/161】** |
| GET | `/admin/v2/config-files/{id}/scopes` | 各层贡献链概览 **【已实现·FR-160/161】** |
| GET | `/admin/v2/config-files/{id}/versions` | 某链版本列表 **【已实现·FR-160/161】** |
| GET | `/admin/v2/config-versions/{versionId}` | 版本详情（内容脱敏） **【已实现·FR-160/161】** |
| POST | `/admin/v2/config-files/{id}/versions` | 保存新版本（语法 / schema 校验 + 并发守卫） **【已实现·FR-160/161】** |
| POST | `/admin/v2/config-versions/{versionId}/rollback` | 回退（生成内容等于历史版本的新版本） **【已实现·FR-160/161】** |
| DELETE | `/admin/v2/config-files/{id}/scopes/{scopeLevel}/{scopeRefId}` | 撤销某层贡献（生成 removal 版本） **【已实现·FR-160/161】** |
| POST | `/admin/v2/config-files/{id}/validate` | 只读校验（不落库不审计） **【已实现·FR-160/161】** |
| GET | `/admin/v2/config-files/{id}/effective` | 有效配置预览（五层合并 + 逐键来源） **【已实现·FR-160/161】** |
| GET | `/admin/v2/config-files/{id}/diff` | 版本间 / 层间 / 目标间键级 diff **【已实现·FR-160/161】** |

> 本域**无 agent 面端点**：配置下发 / 生效 / 灰度全部归交付编排域（变更单）。合并语义为键级深合并（标量覆盖 / map 深合并 / list 整替 / null 删键，spec §4.1）；schema 用 JSON Schema Draft 2020-12 子集（`santhosh-tekuri/jsonschema/v6`）做部分校验（required 仅 namespace 基线层强制）；敏感值 write-only（读出口统一 `__BEACON_MASKED__`、保存占位符回填 head 明文）；`/configs` 页面接真随 0.27.1。

### 文件资产 V2（P8 · 0.28.x，真源 [v2-file-assets.md](specs/v2-file-assets.md) §5）

agent 面：

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/beacon/v2/agent/assets/manifest` | 上报文件清单（增量 / 全量分片，摘要校准） |
| POST | `/beacon/v2/agent/assets/content` | 回传单文件内容（响应 `asset-read` 命令） |

管理面：

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/admin/v2/assets` | 资产搜索分页（路径 / 扩展名 / 哈希组合条件） |
| GET | `/admin/v2/assets/scan-status` | 每服扫描概要（摘要 / 文件数 / 耗时） |
| GET | `/admin/v2/assets/compare` | 跨服同路径哈希分组比对 + 缺失服列表 |
| POST | `/admin/v2/assets/rescan` | 批量下发重扫命令 |
| POST | `/admin/v2/assets/preview` | 旧正文直出入口，固定 `409 operation_requires_approval` |
| POST | `/admin/v2/assets/preview/approval-requests` | 提交单文件内容读取审批：`{serverId,path,reason}` + `Idempotency-Key`，冻结清单 SHA-256，返回 `202 {requestId,status}` |
| POST | `/admin/v2/assets/preview/grants/{grantId}/consume` | 原申请主体在 Agent 成功回传后一次消费：`{commandId}`；校验 command、内容版本与 grant，返回正文或元数据 |
| POST | `/admin/v2/assets/diff` | 旧双文件正文直出入口，固定 `409 operation_requires_approval` |
| POST | `/admin/v2/assets/pair-read/approval-requests` | 跨服务器内容差异的双侧审批入口：`{left:{serverId,path},right:{serverId,path},reason}` + `Idempotency-Key`；分别冻结左右 server/path/SHA-256，返回 `202 {leftRequestId,rightRequestId,status:"pending"}`，不含正文、diff 或 pairId |
| POST | `/admin/v2/assets/pair-read/grants/{grantId}/consume` | 原申请主体携任一侧 grant 的 `{commandId}` 触发双侧原子消费；仅当同一 pair 的两份 grant 都已由 Agent 回传激活且当前哈希仍匹配时返回 `{identical,changed,unsupported,left:{serverId,path,sha256,size},right:{...}}`，绝不返回任一正文或 diff 片段 |
| GET | `/admin/v2/assets/sensitive-rules` | 敏感路径规则清单 |
| PUT | `/admin/v2/assets/sensitive-rules` | 整体替换敏感路径规则（审计） |

> 下行命令 `asset-rescan` / `asset-read` 经既有长轮询命令通道，非独立端点。

### 交付编排 V2（P9 · 0.29.x，真源 [v2-delivery-orchestration.md](specs/v2-delivery-orchestration.md) §5）

管理面 `/admin/v2/change-orders`：

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/admin/v2/change-orders` | 创建 draft 变更单（含扫描目录范围 scanDir） |
| GET | `/admin/v2/change-orders` | 变更单列表 |
| GET | `/admin/v2/change-orders/{id}` | 详情（单 + items + 批次概要） |
| PATCH | `/admin/v2/change-orders/{id}` | 编辑（approved 编辑触发回 draft） |
| DELETE | `/admin/v2/change-orders/{id}` | 创建 draft 删除统一审批申请，返回 `202 + approvalRequestId` |
| POST | `/admin/v2/change-orders/{id}/diff-scan` | 同步读最新快照重算差异返回 items；重扫另设（复用文件资产域 asset-rescan） |
| GET | `/admin/v2/change-orders/{id}/impact` | 影响预览（汇总 + 逐目标分页） |
| POST | `/admin/v2/change-orders/{id}/submit` | 冻结变更单与 items 摘要并创建唯一统一审批申请（原因必填，缺则 `400 approval_reason_required`；须带 `Idempotency-Key`，缺或非法则 `400 INVALID_PARAM`），返回 `202 + approvalRequestId` |
| POST | `/admin/v2/change-orders/{id}/withdraw` | 旧入口，统一返回 `403`；创建人应调用对应统一审批申请的 withdraw |
| POST | `/admin/v2/change-orders/{id}/approve` | 旧第二步审批入口，统一返回 `403`；审批决定只能在审批中心完成 |
| POST | `/admin/v2/change-orders/{id}/reject` | 旧入口，统一返回 `403`；审批人应调用对应统一审批申请的 reject |
| POST | `/admin/v2/change-orders/{id}/pause` | 人工暂停 |
| POST | `/admin/v2/change-orders/{id}/resume` | 创建继续灰度审批申请，返回 `202 + approvalRequestId`；批准 worker 执行冻结的 mode / reason |
| POST | `/admin/v2/change-orders/{id}/cancel` | 紧急终止（原因必填） |
| POST | `/admin/v2/change-orders/{id}/batches/{batchNo}/confirm` | 创建推进门审批申请，返回 `202 + approvalRequestId`；批准 worker 核对冻结批和目标状态后执行 |
| POST | `/admin/v2/change-orders/{id}/rollback` | 创建整单回滚审批申请，返回 `202 + approvalRequestId`；批准 worker 事务内执行 |
| POST | `/admin/v2/change-orders/{id}/rollback/finish` | 残留失败时人工结束回滚 |
| GET | `/admin/v2/change-orders/{id}/targets` | 目标分页（批次 / 状态过滤） |
| GET | `/admin/v2/change-orders/{id}/observe` | 当前批观察窗数据（健康 / TPS / 告警） |
| GET | `/admin/v2/change-orders/{id}/events` | SSE 实时进度 |
| GET | `/admin/v2/change-orders/{id}/items/{itemId}/file-diff` | 旧变更项正文预览入口，固定 `409 operation_requires_approval` |

> 文件内容结果只能经 FR-209 批准后的命令与一次性 grant 返回；旧 file-diff 不再下发 `asset-read`、不再写内容查看审计。双文件 diff 的受控结果契约尚未接入前保持失败关闭。

agent 面 `/beacon/v2/agent/delivery`（命令经既有长轮询通道下发）：

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/beacon/v2/agent/delivery/orders/{id}/upload-manifest` | 模板源拉取待上传 blob 清单 |
| GET | `/beacon/v2/agent/delivery/orders/{id}/manifest` | 目标拉取本服文件清单（含普通文件差异与配置冻结工件） |
| POST | `/beacon/v2/agent/delivery/orders/{id}/result` | 阶段回执（upload / push / activate / rollback） |

> 目标 manifest 的 `files[]` additive 字段 `sourceKind` 取 `file_diff` / `config_artifact`，分别标记普通文件差异与配置冻结渲染工件；旧 Agent 对缺失字段按 `file_diff` 兼容，当前 Agent 对未知值 fail-closed。`configs` 保留为历史兼容字段且恒为空数组。含配置项时，控制面会校验预期配置路径集合与该目标冻结工件完全一致，缺任一工件返回 `409 config_artifact_missing`，不下发残缺清单；混合单同一路径同时出现普通文件差异与配置工件时，以 `config_artifact` 为准，保证每路径只下发一次。`hot_reload` 仅把 `config_artifact` 路径集合交给 Agent 既有配置变更回调，回调摘要按通知时磁盘实际状态确定；无配置工件成功 no-op，普通文件与 JAR 仅落盘。其 activate 回执状态机固定为：success→命令 `done`→目标 `activated`，failed→命令 `failed`→目标 `failed`，命令 `expired`→目标 `failed`，`pending` / `fetched` 超过 `activateTimeoutSec`→目标 `failed` 且控制面尽力将命令置 `expired`。

流式数据面 `/beacon/v2/stream/delivery`：

| 方法 | 路径 | 用途 |
|---|---|---|
| HEAD | `/beacon/v2/stream/delivery/blobs/{sha256}` | blob 存在性 / 就绪查询（去重与断点判断） |
| PUT | `/beacon/v2/stream/delivery/blobs/{sha256}` | 模板源流式上传（服务端校验 sha256） |
| GET | `/beacon/v2/stream/delivery/blobs/{sha256}` | 目标流式下载（Range 断点续传） |

### 审批中心（P10 RC · v1.1.0，真源 [dangerous-operation-approval-core.md](specs/dangerous-operation-approval-core.md) §7 + [approval-center.md](specs/approval-center.md) §3.1）

危险操作的申请、决定与追溯统一走本域，`/approvals` 页面只消费这些端点、不保存第二份状态真源。两组路径由同一组处理器实现同一契约（`/approvals*` 用 `{id}`、`/approval-requests*` 用 `{requestId}`，指同一个路径参数）：

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/admin/v2/approval-requests` | 申请服务端筛选 / 排序 / 分页（状态 / operation / 风险 / namespace / 主体 / 时间；`namespaceId=global` 只查无 namespace 操作，machine 只看自己的申请）；`status=pending&pageSize=1` 的 total 供导航徽标 |
| GET | `/admin/v2/approval-requests/{requestId}` | 申请详情：脱敏快照、adapter 只读实时证据与 snapshot/current diff、决定与执行状态、`resultRef`、timeline 与领域链接；`canApprove` / `canReject` / `canWithdraw` 只由服务端按当前主体计算 |
| POST | `/admin/v2/approval-requests` | 提交申请：body `{operationKey,parameters,reason}` + `Idempotency-Key`；仅已登记 adapter 可规范化并冻结，未知 operation fail-closed，成功 `202` + Location |
| POST | `/admin/v2/approval-requests/{requestId}/approve` | human 批准并自动执行（`{reason?}`；CAS pending→executing 返回 `202`），领域动作由持久 worker 携执行许可完成，页面不再调领域 execute/start |
| POST | `/admin/v2/approval-requests/{requestId}/reject` | human 拒绝（`reason` 必填；pending→rejected） |
| POST | `/admin/v2/approval-requests/{requestId}/withdraw` | 仅申请人撤回自己的 pending 申请（`{reason?}`；pending→withdrawn） |
| POST | `/admin/v2/approval-requests/{requestId}/credential-secret/redeem` | 仅原申请 human 一次性领取凭据明文（仅 succeeded 的凭据变更；其他情形 `410 credential_secret_lost`） |
| GET | `/admin/v2/approvals` | 申请筛选 / 分页（等价路径族，契约同上表第一行） |
| GET | `/admin/v2/approvals/{id}` | 申请详情（同上表第二行） |
| POST | `/admin/v2/approvals/{id}/approve` | 批准并执行（同上表第四行） |
| POST | `/admin/v2/approvals/{id}/reject` | 拒绝（同上表第五行） |
| POST | `/admin/v2/approvals/{id}/withdraw` | 撤回（同上表第六行） |
| POST | `/admin/v2/approvals/{id}/credential-secret/redeem` | 一次性领取凭据明文（同上表第七行） |

> 各领域入口不再自行执行高危动作：旧危险写路由作为兼容申请入口返回 `202`，旧 GET 不隐式建申请、返回 `409 operation_requires_approval` 并指向 `requestEndpoint`；machine principal 无 approve / reject 能力（服务层拒绝，页面隐藏按钮仅为体验）。

### 生命周期：归档 / 永久删除（P10 RC · v1.1.0，真源 [namespace-archive-and-restore.md](specs/namespace-archive-and-restore.md) §3.4、[namespace-permanent-deletion-and-tombstone.md](specs/namespace-permanent-deletion-and-tombstone.md) §3.4、[server-permanent-deletion-and-tombstone.md](specs/server-permanent-deletion-and-tombstone.md) §3.4）

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/admin/v2/namespaces/{id}/lifecycle-impact?action=archive\|restore` | namespace 归档 / 恢复的有界脱敏影响预览（子树有效停用范围），不产生副作用 |
| GET | `/admin/v2/namespaces/{id}/permanent-deletion-impact` | namespace 永久删除影响预览：分类计数 + 分页样本 + 冻结所需版本，不产生副作用 |
| GET | `/admin/v2/servers/{id}/permanent-deletion-impact` | server 永久删除影响预览：读取当前脱敏影响快照，不产生副作用 |

> 生命周期动作**没有直接执行端点**：归档 / 恢复与永久删除均经统一审批申请（`namespace.archive` / `namespace.restore` / `namespace.permanent_delete` / `server.permanent_delete`，内核见 [dangerous-operation-approval-core.md](specs/dangerous-operation-approval-core.md) §7；批准后由领域 adapter 携执行许可在单事务内落墓碑或恢复），旧 `DELETE /admin/v2/namespaces/{id}` 统一返回 `410 namespace_delete_migrated`。server 归档 / 恢复的 impact 端点（[server-archive-and-restore.md](specs/server-archive-and-restore.md) §3.4，FR-215）现挂「区服权威」域 `/admin/v2/servers/{id}/lifecycle-impact`，本表不重复。

### 大厅集群（P10 RC · v1.1.0，真源 [lobby-cluster-authority.md](specs/lobby-cluster-authority.md) §3.4）

每个 namespace 唯一一个独立 LobbyCluster，成员仅限同 namespace Bukkit 且与大区 / 小区归属互斥；`ready=true` 当且仅当当前至少一个成员 `schedulable=true`（运行态派生展示，不落表）。

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/admin/v2/lobby-clusters` | 集群列表（`namespaceId` / `ready` 筛选 + 分页；每项含 namespace 名、成员数、可调度计数、`ready` 与 `updatedAt`） |
| GET | `/admin/v2/lobby-clusters/{id}` | 集群摘要 + 成员分页（成员含 serverId、地址、在线 / 健康 / 容量 / 调度状态与原因；服务端分页，禁止为 1000+ server 一次加载全量） |

> 成员归属变更不在此域：未分配→大厅、Zone→大厅、大厅→Zone、大厅→未分配走「区服权威」域的 `POST /admin/v2/server-placement-transfers`（本规格 §3.4.2），Zone→Zone 仍走既有换区工单。

### BC 受管目录重同步（P10 RC · v1.1.0，真源 [bc-managed-directory-resync.md](specs/bc-managed-directory-resync.md) §3.2）

两个端点都是写操作（`readonly` 403）；控制面只负责下发与记录，不在命令载荷内复制目录（载荷固定空对象，BC 执行时从受管目录权威端点取最新快照，避免排队期间拓扑变化被旧载荷覆盖）。

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/admin/v2/namespaces/{id}/bc-directory-resyncs` | namespace 级重同步：对触发时刻注册表快照内的全部 BC 逐个求值（离线 / 角色异常 / 已有活跃同类命令只回拒绝项，不做整批回滚）；至少一台受理 `202 {namespaceId,requested,accepted,rejected,results}`，全部被拒 `409 NO_ELIGIBLE_BC`，无 BC 目标 `409 NO_BC_TARGETS`；不分页 |
| POST | `/admin/v2/servers/{id}/bc-directory-resyncs` | 单 BC 定点重试（路径 `id` 为 server 数字主键）；`202 {serverId,accepted,commandId,status}`（被拒时含 `code` / `message`） |

> 命令历史与后续状态查询复用 Legacy `GET /admin/v1/commands?namespace=&serverId=&type=bc-directory-resync&status=`（[API-v1-legacy](API-v1-legacy.md)），响应投影不含 `payload`。

## 契约治理

- **P0 冻结**：本章通用约定 + 各 v2 规格 §5 端点表构成第二版契约草案基线，随 P0 出口冻结（[ROADMAP](ROADMAP.md) §3 / §5）。
- **P2 mock 只依赖本草案**：全量 mock 管理台（FR-172）的页面数据形状只依赖本草案（含各规格 §5 的请求 / 响应形状），不接真后端。
- **此后契约变更按 ADR 管理**：改语义、删字段、改路径、改错误码等破坏性变更，**必须**先写新 ADR 决策，并**同步已拍板的 mock 页面**；纯新增端点 / 新增可选字段类小改可不立 ADR，但仍须同一变更内更新对应规格 §5 与本章索引（doc-sync）。**禁止后端实现随手改契约**（ROADMAP §5 尾部约定）。
- **漂移处置**：端点索引与规格 §5 不一致时，以规格为准并回补本章索引；通用约定与规格冲突时，以本章裁决为准并回改规格。

## MCP OAuth 公网入口

MCP resource 固定为 `/admin/v2/mcp`，token 固定为 `POST /admin/v2/oauth/token`；仅接受 Client Credentials 表单请求和 metadata 发布的精确 audience。客户端创建、轮换、启用分别通过 `/admin/v2/mcp-clients` 的审批申请端点完成，吊销是直接止损动作。MCP bearer 不可调用普通管理 REST。

token 端点按 RFC 6749 §5.2 回写错误码，且与 `mcp.token.denied` 审计记录的原因一致——三类失败各自成码，不合并成 `invalid_client`：缺 `client_id`/`client_secret`/`audience` 回 `400 invalid_request`（缺必填参数）；`scope` 超出该客户端 profile 允许范围回 `400 invalid_scope`；凭证错误、客户端不存在或已吊销统一回 `401 invalid_client`，且不区分内部原因以防枚举探测。

### MCP bearer 的两类形态：access token 与客户端凭据直连

> 本节的 `GET` / `POST /admin/v2/mcp` 与 `POST /admin/v2/oauth/token` 属 **MCP 协议端点**，**不纳入上面的「端点索引（按域）」**（该索引只登记管理面业务端点）；本节即它们的登记处。

`/admin/v2/mcp` 的 `Authorization: Bearer` 接受两类凭据，按前缀区分：

| 凭据 | 前缀 | 来源 |
|---|---|---|
| access token | `mct_` | `POST /admin/v2/oauth/token` 以 Client Credentials 换取，短期且 audience-bound |
| 客户端 secret（**客户端凭据直连**） | `mcs_` | 客户端创建 / 轮换响应中**仅出现一次**的 `clientSecret`，直接当 bearer 使用 |

**客户端凭据直连**面向长驻客户端（常驻 Agent 运行时、MCP 客户端进程）：它不必再自行实现 15 分钟 access token 的续期循环，因而也不再需要外部桥接进程代持与刷新凭据——把 `clientSecret` 原样放进 `Authorization: Bearer` 即可，凭据可反复使用。

与 token 路径的差异：

- **无 15 分钟窗口**：直连凭据不消费 token 表、也不签发 token，每次请求按 `secret_hash` 现查现比；access token 的 15 分钟有效期语义不变。
- **仍不签发 refresh token**：规范约束不变，两类形态都不引入 refresh token。
- **吊销 / 轮换即时生效**：吊销（client 转 `revoked`）在下一次请求即被拒；轮换改写 `secret_hash` 并递增 `secret_version` 后，旧 secret 立即不可用、只有新 secret 生效，无需任何额外的失效或缓存清理逻辑。
- **不参与 audience 校验**：直连凭据只证明客户端身份，端点固定为 `/admin/v2/mcp`；audience 精确比对只存在于 token 路径。
- **校验失败口径一致**：前缀不合规、secret 不匹配、客户端已吊销一律回 `401` 且不区分内部原因（防枚举探测）。
- **生产必须 HTTPS**：直连模式下 secret 本身即长期凭据（不再有 15 分钟自然过期），泄露即可长期使用，故**生产环境必须经 HTTPS 反向代理**；`mcp.allow-insecure-internal: true` 的明文直连仅限内网 / 回环部署（见 [OPERATIONS](OPERATIONS.md) §9）。

当前 `automation` profile 还可发现显式审批工具：配置的删除与批量删除/启停、文件创建/导入/发布/回滚/删除/批量删除/启停、覆盖集发布/回滚/删除，以及资产预览和消息正文的审批申请。危险写入工具均只创建审批申请并返回 `{approvalRequestId,status}`；不会直接执行领域操作、构造 permit 或代理文件路径。资产预览与消息正文的消费工具仍核验原申请主体、冻结目标和一次性 grant，但响应固定只返回消费状态，绝不回吐敏感正文。`observer` 不可发现这些工具。

**拓扑建树工具（FR-221）**：`automation` 另可发现九个低风险结构写工具——`beacon.topology.bc-clusters.create/update/delete`、`beacon.topology.regions.create/update/delete`、`beacon.topology.zones.create/update/delete`，语义与既有 `/admin/v2` HTTP 端点逐一对齐。与分配/换区等高风险动作**刻意不同**：建树按 FR-220 的「低风险按能力直执」原则**直接执行并写审计**，不产生审批票据。删除非空节点（大区下含小区、小区下含服务器、集群下含大区或已分配代理）按既有约束拒绝。`regions.create` 须 `parentId` = 所属 BC 集群 id，`zones.create` 须 `parentId` = 所属大区 id。`observer` 不可发现写工具。

**审批决定工具（FR-223，归真项）**：`beacon.approvals.approve` / `beacon.approvals.reject`（拒绝须给理由）**仅当 `mcp.allow-approval-decide=true` 时**对 `automation` profile 暴露，默认关闭——关闭时审批决定权归人类，保持原分权设计；内网单操作者部署可显式开启以打通自动化闭环。批准与拒绝均写强审计，批准后由 approval worker 执行领域动作。`observer` 任何情况下不可发现审批决定工具。该开关只影响这两个工具，不影响 FR-221 建树工具等其他能力。

**生产模式（FR-237）**：`mcp.production-mode=true` 时，`automation` 面隐藏 `critical` 风险等级的十个工具——`beacon.approvals.approve` / `.reject`、`beacon.credentials.api-key.create` / `.rotate`、`beacon.lifecycle.namespace.permanent-delete`、`beacon.lifecycle.server.permanent-delete`、`beacon.delivery.order.rollback`、`beacon.system.update.apply` / `.rollback`、`beacon.system.settings.update-dangerous`。被收敛工具对客户端表现为**「工具不存在」**（`tools/call` 返回 `unknown tool`，错误码 `-32602`）而非执行被拒；`low` / `high` 档不受影响，`observer` 集合不变。该开关与 `mcp.allow-approval-decide` 正交但**优先**——生产模式下即使开启审批决定开关，`approve` / `reject` 仍不可发现。工具风险分级与隐藏范围由 `mcpToolCatalog` 单一真源驱动（新增工具无需改代码即自动纳入），详见 [mcp-tool-risk-grading](specs/mcp-tool-risk-grading.md) 与 [mcp-production-mode-gate](specs/mcp-production-mode-gate.md)。

`observer` 与 `automation` 均可发现 `beacon.metadata.namespaces.list`、`beacon.topology.snapshot.get`、`beacon.metrics.health.list`、`beacon.metrics.summary.get`、`beacon.metrics.series.query`、`beacon.history.messages.list`、`beacon.history.connections.stats`、`beacon.history.commands.list`、`beacon.history.scheduling-decisions.list`、`beacon.alerts.events.list` 与 `beacon.audit.events.list`。列表均分页或受时间窗约束；消息不返回 payload、玩家标识或 hop 原文，连接仅返回聚合，命令不返回结果正文，告警不返回 detail，审计不返回 detail 与客户端地址。

**告警处置工具（直接执行 + 同事务写审计）**：`automation` 另可发现 `beacon.alerts.events.handle`（单条处理）与 `beacon.alerts.events.batch-handle`（按筛选批量处理），二者风险等级均为 `high`，语义与 HTTP 面 `POST /admin/v1/alert-events/{id}/handle` / `POST /admin/v1/alert-events/handle` 一致，**直接执行并写审计、不产生审批票据**（`mcpToolCatalog` 的 `OperationKind` 留空）。约束：目标状态仅 `acknowledged` / `resolved`（其余含空值拒绝）；处理说明 `note` 必填、去空白后为空即拒绝；**单条与批量都受调用者观测范围约束**——批量把范围解析成筛选条件后注入（不越界改行），单条没有筛选条件可注入，故先按 `id` 读出目标再按其 `namespace` 判定，范围外即拒且零写；单条的范围外目标与不存在的目标返回**同一条**拒绝文案（`告警不存在或不在观察范围内`，不泄露目标是否存在）；范围参数缺省即全局范围、范围解析失败即拒（`观察范围无效`）；批量只影响 `status='open'` 的行（与审计同事务，故重复调用幂等，第二次 `affected=0`）；单条返回 `{id,status,handledBy,handledAt}`，批量返回 `{affected}`。`observer` 不可发现这两个工具（只能读 `beacon.alerts.events.list`）。

公网入口只有在 `mcp.enabled=true`、`mcp.public-base-url` 为无路径 HTTPS 基址且 `mcp.trusted-proxy-cidrs` 已配置时才挂载；请求必须来自受信代理，并携带与基址一致的 `X-Forwarded-Proto: https`、`X-Forwarded-Host` 和 Host。详见 [built-in-admin-v2-mcp-and-oauth.md](specs/built-in-admin-v2-mcp-and-oauth.md)。

### MCP 客户端管理端点

管理面（`/admin/v2` 组内，走登录令牌 / API 密钥鉴权）提供 MCP OAuth 客户端生命周期：

| 方法 | 路径 | 语义 |
|---|---|---|
| GET | `/admin/v2/mcp-clients` | 客户端全量列表（按创建时间倒序）；返回 `clientId`、`displayName`、`secretPrefix`、`profile`、`status`、`secretVersion` 与 `createdBy` / `createdAt` / `updatedAt` / `revokedAt`（未吊销时省略）。不含 secret 哈希或明文 |
| GET | `/admin/v2/mcp-clients/{clientId}` | 单个客户端脱敏视图（字段同上） |
| POST | `/admin/v2/mcp-clients` | 申请创建客户端（`displayName` / `profile` / `reason`），202 + `{approvalRequestId, clientId, clientSecret, status}`；`clientSecret` 仅本次响应出现一次 |
| POST | `/admin/v2/mcp-clients/{clientId}/rotate` | 申请轮换 secret（`reason`），202 + 同形票据 |
| POST | `/admin/v2/mcp-clients/{clientId}/enable` | 申请重新启用已吊销客户端（`reason`），202 + 同形票据 |
| POST | `/admin/v2/mcp-clients/{clientId}/revoke` | 立即吊销（止损，不等待审批），200 + `{ok:true}` |
| GET | `/admin/v2/mcp/config` | MCP 入口部署配置只读视图（见下） |
| GET | `/admin/v2/mcp/invocations` | MCP 工具调用流水（FR-240）：`tool` / `clientId` / `result` / `riskLevel` / `reason` / `from` / `to` 六维过滤 + `limit` / `cursor` 游标分页，返回 `{items, nextCursor}`（空串 = 末页）。参数只存脱敏摘要，正文不入库 |
| GET | `/admin/v2/mcp/invocations/{invocationId}` | 单条流水详情（UUIDv7 直定日表，未命中或非法 ID 一律 404） |

三个申请端点的硬约束：

- 必须携带 `Idempotency-Key` 头，缺失返回 `400 idempotency_key_required`；`reason` 必填，缺失返回 `400 reason_required`；仅人类主体可提审，机器主体返回 `403 human_only_operation`。三类失败给出**可区分的错误码**而非泛化 403——它们的处置方向完全不同（补请求头 / 补原因 / 换主体）。
- 同键重放返回既有票据，此时 `clientSecret` **不返回**（明文只在首次生成时出现一次，遗失只能重新申请轮换）。
- `readonly` 角色被写守卫拒绝。
- 轮换批准后旧 secret 与已签发 token 即时失效（直连模式下旧 secret 同样立即不可用）；吊销后该客户端无法再换取 token，其直连 secret 与已签发 token 立即失效。

`GET /admin/v2/mcp/config` 返回 MCP 入口的部署事实，**任何启用状态下都返回 200**（未启用时 `enabled=false`，供管理台展示配置指引而非报错）：

```json
{
  "enabled": true,
  "publicBaseUrl": "https://beacon.example.com",
  "trustedProxyCidrs": ["10.0.0.0/8"],
  "allowInsecureInternal": false,
  "allowedHosts": [],
  "allowApprovalDecide": false,
  "allowMachineRegister": false,
  "productionMode": false,
  "directMode": false
}
```

字段集合固定为上述九项。这些配置全部是**启动项**（改后须重启控制面），因此只提供读取、不提供写入端点。响应**绝不回显任何凭据**（如 agent 共享 token）；`directMode` 表示内网明文直连（无 TLS 反代），与 `MCPProxyPolicy` 的判定一致，未启用时恒为 `false`；`productionMode` 开启时 MCP 面隐藏 `critical` 风险等级的工具（不可逆 / 影响控制面自身 / 可提权），工具对客户端表现为「不存在」而非「执行被拒」，详见 [mcp-production-mode-gate](specs/mcp-production-mode-gate.md)。
