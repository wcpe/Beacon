# 功能规格：服务端权威观测范围契约

> 状态：开发中　·　关联 PRD：FR-213　·　依赖：FR-178 env→namespace 映射、[v2-zone-authority.md](v2-zone-authority.md)

## 1. 背景与目标

当前顶栏 env 过滤由前端把 env 展开为 namespace，再在 API 只支持单 namespace 时“先分页取全量、后过滤当前页”。这会让 total、KPI、趋势、导出和详情互相矛盾；选中 env 被删除或尚未加载时还会回退到全量，造成跨环境噪声甚至误判。

本功能建立一个服务端权威契约：管理面观测查询统一接收可选 `envId` 与 `namespaceId`，服务端依据现有 `env_namespace` 映射在分页、聚合、冷热合并和导出**之前**确定范围。它是查询约束，不是授权边界，也不建立新 scope 表。

## 2. 需求（要什么）

### 2.1 查询参数与解析

| envId | namespaceId | 权威范围 |
|---|---|---|
| 省略 | 省略 | 全部 namespace |
| 省略 | N | 仅 N；兼容现有单 namespace 查询 |
| E | 省略 | E 当前映射的全部 namespace；映射为空则合法空集 |
| E | N | 仅 N，且 N 必须属于 E |

- “全部”只用参数省略表达；显式 `0`、负数或非法格式返回 `400 invalid_observation_scope`。
- env/namespace 不存在、namespace 已 tombstoned，或 E/N 不匹配时返回 `409 observation_scope_stale`，响应不带业务数据；禁止回退到全部、首个 env 或首个 namespace。
- archived namespace 仍是合法的管理观测范围：历史查询可读，实时在线/健康/调度视图按其有效停用语义返回空；页面必须标明“已归档，只读”，不能把 archived 误判为保存值失效。
- env 合法但映射为空返回对应端点的标准空结果，不查询全量。
- 旧端点仅支持 namespace code 时继续兼容单值 `namespace`；它与 `envId/namespaceId` 同时出现则 `400`，避免不明确优先级。
- 每个请求只解析一次不可变的 namespace ID 集合，同一请求内的 items、total、buckets、summary 使用同一快照；下一请求再读取最新 env 映射。

### 2.2 过滤时机

- 列表：先按范围过滤，再做其它条件、排序、游标/页码分页和 total。
- 聚合/KPI/趋势：先按范围过滤原始事实，再分桶、计数、算比率；空集返回零值/空 buckets，不借用全局指标。
- 详情：路径 ID 对应记录不在范围内时按未命中返回，不泄露域外详情。
- 导出：复用与列表同一 query object 和范围，先过滤再流式导出；页面筛选、total 与导出行集一致。
- 热/冷查询：范围谓词同时作用于每个热表、归档表和日表，再做跨表游标合并；不能合并后在内存丢行。
- 内存实时态：先用解析出的 namespace set 过滤 registry/health 快照，再分页/聚合。
- 订阅/SSE：若后续新增管理面观测订阅，建立连接时冻结 scope fingerprint，事件进入发送队列前按 namespace set 过滤；env 映射 revision/hash 变化时发送 `observation-scope-stale` 并断开，客户端按新范围重连，不允许旧连接扩大或串入范围外事件。

当前 SSE 盘点：`/admin/v2/change-orders/{id}/events` 与文件同步 SSE 均是按工单/任务 ID 的变更流，`/beacon/v1/agent/stream` 是数据面 Agent 推送；三者均不是管理面观测查询，且不接受 `envId`/`namespaceId`，故不纳入本 FR 的范围矩阵。不得为满足形式覆盖而把观测 scope 注入这些任务或数据面流。

### 2.3 必须覆盖的端点

以下管理面观测端点统一接受本契约；表中静态详情同样做范围归属校验：

| 域 | 端点 |
|---|---|
| 集群与身份只读 | `GET /admin/v2/agent-identities`、`/{identityId}`、`/servers`、`/zone-tree`、`/lobby-clusters`、`/lobby-clusters/{id}` |
| 指标与健康 | `GET /admin/v2/metrics/summary`、`/metrics/series`、`/health`、`/health/snapshots`、`/health/{serverId}` |
| 调度决策 | `GET /admin/v2/sched-decisions`、`/sched-decisions/summary`、`/sched-decisions/{traceId}` |
| 连接 | `GET /admin/v2/connections`、`/connections/stats`、`/connections/{connId}` |
| 消息元数据 | `GET /admin/v2/messages`、`/messages/stats`、`/messages/{messageId}` |
| 既有可观测页 | `GET /admin/v1/topology`、`/alerts`、`/alert-events`、`/audits`、`/audits/analytics`、`/audits/export`、`/commands`、`/commands/analytics` |
| Legacy 指标兼容 | `GET /admin/v1/metrics/summary`、`/metrics/trend`，直至调用者迁完 |

其中当前完全缺少范围的 `/admin/v2/metrics/summary`、`/connections/stats`、`/sched-decisions/summary`、`/messages/stats` 必须作为首批红测；已有单 `namespaceId/namespace` 的端点扩展为 env 多 namespace，不允许客户端后过滤。

明确排除：

- `/admin/v2/approval-requests*` 是全局任务源，只用页面自身筛选，不消费本 scope。
- 所有 mutation（含 `POST /messages/{id}/payload`、告警处理、设置、身份、配置、交付动作）不得从本 scope 推导目标；其授权/审批见对应领域规格。
- 控制面自身进程观测 `/admin/v1/system/status`、`/system/observability` 没有 namespace 归属，不套用本契约。

## 3. 设计（怎么做）

### 3.1 `ObservationScopeResolver`

- handler 仅解析原始 query，调用 resolver 得到 `ObservationScope{All bool, NamespaceIDs set, Fingerprint}`；repository/service 不重复查 env。
- resolver 复用现有 `env`、`env_namespace`、`namespace` 表；不新增 `scope`、快照或映射副本。
- DB 查询按 endpoint 选择 `namespace_id IN (...)` 或对 `env_namespace` 的 `EXISTS/JOIN`；内存态用 set。禁止按 namespace 循环发 SQL/远程调用。
- 空集在 service 早返回标准空形状，避免遗漏谓词后误查全量。

### 3.2 领域接线

- 各 query params 增 `Scope ObservationScope`，在 repository 构建基础条件时首先应用；summary/stats 与 list 复用相同基础 predicate。
- 审计/命令等历史 code 字段先由 namespace ID 解析为当前权威 code 集，仍在数据库分页前过滤；不得把 code 集交给前端。
- 详情 service 必须能取到记录的 namespace 并与 scope 比较；全局唯一 ID 不代表可忽略范围。
- 所有 cursor/query key 绑定 scope fingerprint；scope 变化后旧 cursor 无效，客户端重新从第一页查询。

### 3.3 错误与可观测性

- 错误体沿用 `{code,message,traceId}`，不得包含域外实体内容。
- 记录 scope 解析失败与被拒原因的 INFO/WARN 审计/日志摘要，只含 envId/namespaceId 与 traceId，不记录 token 或查询结果。
- 范围是运维聚焦工具，不替代 namespace 强隔离和 Principal capability；后续分域 RBAC 仍需独立授权判定。

## 4. 任务拆分

- [x] 测试先红：resolver 四种合法组合、空映射、非法数值、不存在与 E/N 不匹配。
- [ ] 测试先红：四个当前无范围聚合端点在多 namespace 数据下只统计目标 env，空映射为零。
- [x] 测试先红：列表在过滤后分页/计 total，详情域外未命中，审计导出与列表行集一致，冷热/跨日查询不串域。
- [x] SSE 盘点：现有 SSE 均非管理面观测订阅，明确排除；后续新增观测订阅时必须实现 scope fingerprint 与映射漂移断开。
- [ ] 最小实现 resolver 与 query object，按域接 repository 基础 predicate；不建新表、不按 namespace N+1 查询。
- [ ] 前端 API contracts 增 additive `envId/namespaceId`，移除观测页客户端后过滤。
- [ ] 独立复核：逐项核对端点矩阵、所有 summary/list/detail/export 的过滤时机和 mutation 排除。
- [ ] 文档同步：PRD 状态、ARCHITECTURE 的查询数据流、API 通用 query 与端点矩阵、UX 错误语义、CHANGELOG。

## 5. 验收标准

1. env 映射多个 namespace 时，列表 items/total、KPI、趋势、详情和导出均只包含该集合，且分页前已过滤。
2. env 空映射返回空/零；失效、删除或不匹配 scope 返回 fail-closed 错误，绝不回退全量；FR-178 当前前端实现仅作临时收窄，最终权威由服务端查询契约执行。
3. 仅传旧 `namespaceId` 或 namespace code 的调用保持单 namespace 语义；未传 scope 的旧调用保持全量语义。
4. 热库、归档库、跨日表、内存健康快照与 scoped 订阅使用同一范围，域外详情/事件不可见；archived namespace 历史可读而实时视图为空。
5. 四个首批聚合端点及完整矩阵都有红→绿测试；查询计数证明未按 namespace 产生 N+1。
6. 审批中心和 mutation 不消费本 scope；独立复核确认没有客户端分页后过滤作为正确性依赖。

## 6. 风险 / 待定

- 旧 V1 数据若缺少可追溯 namespace 字段，不能靠 serverId 猜测；应先补权威关联或明确从矩阵移除，禁止“查全量再前端滤”。
- env 映射可能在长导出期间变化；单个请求使用开始时解析的 namespace 集合，下一请求读取新映射，保证一次结果自洽。
- namespace 数量极大时巨大 `IN` 可能退化，可在同一现有映射表上改用 JOIN/EXISTS；这不构成新增 scope 模型。
