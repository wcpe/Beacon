# 功能规格：MCP 显式领域工具与审批交接

> 状态：草拟　·　关联 PRD：FR-220　·　分支：待执行时创建　·　依赖：FR-207～211、FR-213、FR-215～219

## 1. 背景与目标

FR-219 只建立经过认证的 MCP transport 和机器主体。要让外部 Agent 真正完成 Beacon 运维，还需把现有与新增领域能力逐项暴露为可审计工具，同时确保所有危险操作只能提交审批，永远不能由机器批准或绕过人审。

本规格定义显式 tool registry、读写风险边界、审批返回契约和结果轮询。目标是让 `automation` 客户端覆盖 Beacon 的可自动化领域操作，而不是提供任意 HTTP、SQL、文件路径或内部服务代理。

## 2. 范围与非目标

### 2.1 本期范围

- 为元数据、拓扑、指标、历史、审计、身份、凭据、信任、Agent、系统、配置/文件/交付和资源生命周期登记显式工具。
- `observer` 读取与 `automation` 低风险直执。
- 高风险工具冻结同一份 operation descriptor 并创建 FR-207 审批申请。
- 外部 Agent 查询、分页列出和撤回自己提交的申请，并取得执行结果摘要。
- registry 与全部管理 operation 的覆盖检查、schema 兼容和审计关联。

### 2.2 明确不做

- 不提供 `http.request`、`rest.call`、`sql.query`、`service.invoke`、任意文件路径读写或任意 Agent 命令代理。
- 不提供 approve、reject、approve-and-execute 或可构造 ExecutionPermit 的工具。
- 不让 MCP 工具直接访问 repository、GORM、Agent session/connection map 或绕过 application service。
- 不把实时日志、文件内容、敏感配置明文或消息 payload 归类为普通读取。
- 不复制领域状态机、风险判定或审批状态机；FR-207～211 与各领域 service 是唯一真源。
- 不承诺为没有稳定领域契约的调试内部方法提供 MCP 入口。

## 3. Tool registry

### 3.1 登记项

每个工具必须静态登记以下字段：

- 稳定 `toolName` 与版本；
- 对应的唯一 `operation`；
- 允许 profile/capability；
- `direct` 或 `approval_required` 分类；
- JSON Schema 输入/输出；
- 参数规范化、分页上限、脱敏与审计策略；
- application service adapter；
- 兼容策略和责任人。

`tools/list` 只返回当前 Principal 可发现的工具。隐藏不等于授权：`tools/call` 和 service 层都必须重新校验 capability、operation 分类、namespace 观测权限与机器不可审批不变量。

### 3.2 命名规则

工具名使用 `beacon.<domain>.<noun>.<verb>`，不得把 URL 或 HTTP method 暴露为工具语义。已发布工具的名称、必填输入和已有输出字段保持向后兼容；新增输出只能 additive，破坏性变更发布新版本工具并保留迁移窗。

## 4. 工具目录

以下是必须覆盖的领域族；实施期允许按现有 application service 拆成更细工具，但不得合并成通用代理。

### 4.1 直接读取工具

| 工具族 | 最小能力 | 边界 |
|---|---|---|
| `beacon.metadata.*.list/get` | env、namespace、BC、大区、小区、服务器与 LobbyCluster 元数据 | 服务端分页；遵守 FR-213 观测范围 |
| `beacon.topology.*.list/get` | 权威归属、身份摘要、在线/健康/可调度事实 | 不回传 secret/token、绑定快照明文 |
| `beacon.metrics.*.query` | 当前指标与历史趋势 | 限时间窗、序列数和分页，不做无界导出 |
| `beacon.history.*.query` | 连接、消息元数据、命令/变更历史 | payload、实时内容与敏感明文除外 |
| `beacon.audit.events.list/get` | 脱敏审计 | 只读、分页、按主体/目标/时间过滤 |
| `beacon.approvals.own.list/get` | 本 MCP client 自己提交的申请与结果 | 不能读取无权申请；结果继续脱敏 |

当前已登记的第一批只读工具为 `beacon.metadata.namespaces.list`、`beacon.topology.snapshot.get`、`beacon.metrics.health.list`、`beacon.metrics.summary.get`、`beacon.metrics.series.query`、`beacon.history.messages.list`、`beacon.history.connections.stats`、`beacon.history.commands.list`、`beacon.history.scheduling-decisions.list` 与 `beacon.audit.events.list`。它们同时对 `observer` 与 `automation` 可发现；连接不提供单连接明细，消息不提供 payload、玩家标识或 hop 原文，命令不提供结果正文，审计不提供 detail 与客户端地址。

“数据库元数据”指经 query service 暴露的领域元数据，不是 SQL 或表结构浏览器。所有列表必须有服务端上限和游标/分页，禁止一次加载 1000+ 资源或大时间窗历史。

### 4.2 `automation` 直接动作

只有 operation registry 分类为 `direct` 且 capability 允许的低风险/止损动作可直执，例如：

- 更新不参与寻址的 displayName、描述、标签或纯观测偏好；
- 创建/更新无副作用的查询视图或草稿；
- 撤回本 client 仍处于 pending 的审批申请；
- 吊销凭据/信任、禁用身份、暂停或取消运行中任务等 FR-207 定义的止损动作。

是否 direct 由领域 operation descriptor 决定，MCP 不自行根据方法名或参数猜测。止损动作仍需原因、capability 和强审计；恢复、重新启用或扩大影响必须走审批。

### 4.3 危险操作工具族

`automation` 必须能通过显式工具提交下列危险操作；调用只创建审批请求，不产生领域副作用：

| 工具族 | 典型 operation |
|---|---|
| `beacon.identity.*` | 绑定确认、解绑、重新启用、冲突处置 |
| `beacon.credentials.*` | namespace token/API key/MCP client 创建、轮换、启用 |
| `beacon.trust.*` | 建立或扩大 namespace 信任 |
| `beacon.topology.*` | 服务器分配、换区、默认入口、LobbyCluster 成员迁移 |
| `beacon.agent.*` | 下发命令、读取实时日志、读取文件、敏感明文或 payload |
| `beacon.system.*` | 控制面升级、回滚、高影响设置变更 |
| `beacon.config.*` / `beacon.files.*` | 配置/文件写入、覆盖、删除、恢复与 override set 变更 |
| `beacon.delivery.*` | 变更单提交、批准后执行所需意图、批次继续、恢复/启动与回滚 |
| `beacon.server.lifecycle.*` | 服务器归档、恢复、永久删除 |
| `beacon.namespace.lifecycle.*` | namespace 归档、恢复、整棵子树永久删除 |

每个具体 operation 必须有独立工具和输入 schema。例如 namespace 永久删除工具必须显式接受 namespace code、资源版本、影响预览摘要和原因，不能通过 `beacon.resource.delete(kind, id)` 这类通用入口表达。

文件与覆盖集的固定工具如下，均只调用对应 application service 的 `Request*` 方法并只返回 `{approvalRequestId,status}`；它们不发布、不回滚、不删除，也不构造 permit：

| 工具 | 输入 |
|---|---|
| `beacon.config.delete` | `id`、`reason`、`comment`、`idempotencyKey` |
| `beacon.config.batch.delete` / `beacon.config.batch.enable` / `beacon.config.batch.disable` | `ids`、`reason`、`idempotencyKey` |
| `beacon.files.create` | namespace、作用域、path、content、选项、reason、comment、`idempotencyKey` |
| `beacon.files.import` | namespace、作用域、`files`、reason、comment、`idempotencyKey` |
| `beacon.files.publish` | `id`、`content`、`reason`、`comment`、`idempotencyKey` |
| `beacon.files.rollback` | `id`、`version`、`reason`、`comment`、`idempotencyKey` |
| `beacon.files.delete` | `id`、`reason`、`comment`、`idempotencyKey` |
| `beacon.files.batch.delete` / `beacon.files.batch.enable` / `beacon.files.batch.disable` | `ids`、`reason`、`idempotencyKey` |
| `beacon.assets.preview.request` | `serverId`、`path`、`reason`、`idempotencyKey` |
| `beacon.assets.preview.consume` | `grantId`、`commandId`；仅完成一次性消费校验，不返回正文 |
| `beacon.messages.payload.request` | `messageId`、`reason`、`idempotencyKey` |
| `beacon.messages.payload.consume` | `grantId`、`messageId`；仅完成一次性消费校验，不返回正文 |
| `beacon.override-sets.publish` | `id`、`targetRoot`、`reloadCommand`、`reason`、`comment`、`idempotencyKey` |
| `beacon.override-sets.rollback` | `id`、`version`、`reason`、`comment`、`idempotencyKey` |
| `beacon.override-sets.delete` | `id`、`reason`、`comment`、`idempotencyKey` |
| `beacon.delivery.order.submit` | `orderId`、`reason`、`idempotencyKey`；冻结单与有序 items 摘要并创建唯一审批 |
| `beacon.delivery.order.delete` | `orderId`、`reason`、`idempotencyKey`；创建 `delivery.draft_delete` 审批，不直接删草稿 |

上述工具仅 `automation` 可发现；`observer` 不可发现也不可调用。敏感内容消费工具沿用原申请主体、冻结目标与一次性 grant 校验，但 MCP 响应固定不含文件或消息正文。

### 4.4 永久禁止的工具

registry 构建与测试必须拒绝任何 operation 等价于：批准、驳回、构造/注入执行许可、修改审批记录、模拟 human、任意协议转发或任意内部调用。即使未来 profile 配置错误，FR-206/207 服务层也必须拒绝 `mcp` Principal 审批。

## 5. 调用与审批契约

### 5.1 直接调用

直接工具成功返回领域结果与 `{operationId, traceId}`。幂等写入必须接受 `idempotencyKey`；同一 principal、operation 与 key 重试返回同一结果，不重复副作用。领域冲突、权限不足和输入错误映射为稳定 MCP error，不把 Go 错误、SQL 或敏感数据暴露给调用方。

### 5.2 危险调用

危险工具完成输入校验、权限预检、目标解析、规范化和影响预览后，创建不可变申请并返回：

```json
{
  "approvalRequestId": "apr_...",
  "status": "pending",
  "expiresAt": "2026-07-30T12:00:00Z",
  "operation": "namespace.permanent_delete",
  "targetSummary": "prod / 生产环境",
  "impactSummary": {"namespaces": 1, "servers": 42}
}
```

返回 `pending` 不代表执行成功。外部 Agent 不得在批准后再次调用 execute；human 在 `/approvals` 执行“批准并执行”后，由 FR-207 持久 worker 自动调用领域 adapter。相同 `idempotencyKey` 重试只返回原 approvalRequestId，不能创建多份待审申请。

创建申请前的预检不能代替执行前复核。资源版本、目标或影响发生漂移时 worker 必须失败关闭，并把脱敏原因写入终态 `failed`；不得扩大批准范围或自动重提。

### 5.3 查询与撤回

- `beacon.approvals.own.get` 返回申请状态、时间线、批准主体摘要、执行结果/错误摘要和审计引用。
- `beacon.approvals.own.list` 服务端分页，支持状态、operation 和时间筛选，只返回当前 client 的申请。
- `beacon.approvals.own.withdraw` 只允许申请主体在 `pending` 状态撤回；已批准、执行中或终态返回稳定冲突。
- 不提供 approve/reject 工具；外部 Agent 也不能通过管理 REST bearer 完成这些动作。

## 6. 观测范围与目标安全

- 读取工具必须显式接受 FR-213 的 observation scope，或使用 MCP session 内经校验的只读默认 scope；无效、越权、不存在、tombstoned 或映射错配范围失败关闭，不能回落全局。archived namespace 仍是合法只读范围：历史可读，实时在线/健康/调度结果为空，并返回明确的归档标记。
- 观测 scope 只影响读取。任何写/审批工具必须在输入中显式给出目标稳定 code/serverId 与预期资源版本，不能继承页眉、session 或上一次查询范围作为写目标。
- displayName 只用于结果展示，输入中的目标解析永远使用 FR-205 的 code/serverId；歧义名称不能命中资源。
- namespace 隔离与 capability 校验在 query/application service 执行，MCP handler 不做客户端侧过滤或分页后过滤。

## 7. Agent 内容与敏感结果

任何需要在线 Agent 往返、读取实时日志/文件、敏感配置明文或消息 payload 的工具都归 `approval_required`，即使 HTTP 语义看似读取。申请中只保存路径/选择器/时间窗/哈希等脱敏意图，不保存待读取明文。

批准后结果按 FR-209 的受控结果存储与保留期提供；`own.get` 只返回调用方有权查看的脱敏摘要。若外部 Agent 需要取得受控内容，必须使用 FR-209 定义的专用一次性结果读取工具，该工具再次校验原审批、主体、保留期和读取次数，不得退化为任意文件读取。

## 8. 审计、限额与隐私

- 每次 `tools/call` 记录 clientId、profile、toolName、operation、分类、目标摘要、结果、approvalRequestId/operationId 与 traceId。
- 参数和结果按 registry 的字段级规则脱敏；token、secret、Authorization、payload、敏感文件内容不得进入普通日志、审计 detail 或指标标签。
- 为每客户端、工具和风险分类设置并发与速率上限；历史查询设置时间窗/页大小上限，危险申请设置待审数量上限，超限返回可重试错误。
- 日志使用中文并按级别：授权/校验拒绝为聚合 WARN，协议或领域内部失败为 ERROR，正常调用只记结构化审计，不刷 INFO。

## 9. Operation 覆盖门禁

实施期生成一份由代码维护的 operation coverage 清单并执行测试：

1. 每个管理 REST、后台危险入口和领域副作用 operation 都已登记风险分类。
2. 每个面向操作者的管理 operation 恰有一个显式 MCP tool；除 human approve/reject、ExecutionPermit、MCP/OAuth 协议内部动作和无稳定外部契约的纯内部诊断外，不允许以“不要求自动化”为由豁免。
3. 所有 `approval_required` tool adapter 只能调用申请服务，不能直接取得 ExecutionPermit。
4. 固定禁止清单只包含 human approve/reject、permit 构造、协议内部动作与纯内部诊断；不存在 MCP approve/reject/permit 工具，也不存在通用 HTTP/SQL/文件/内部服务代理。
5. 工具 schema 的目标字段使用稳定 code/serverId，不使用 displayName 寻址。
6. 新增面向操作者的 operation 未映射到恰好一个显式工具时测试失败；新增禁止项未进入固定安全审查清单时同样失败，运行时一律 fail-closed。

清单是代码/测试产物，不单独维护易漂移的手写镜像；权威分类仍在 FR-207 operation registry。

## 10. 向后兼容与发布顺序

1. FR-207～211、FR-215～218 先提供稳定 operation descriptor 与领域 adapter。
2. FR-213 提供服务端观测范围契约，FR-219 提供已认证 MCP session。
3. 先实现只读工具及分页/隔离测试，再实现低风险直执与审批申请工具。
4. 接入 own approval 查询/撤回、幂等、审计、限额和覆盖门禁。
5. 使用真实 `observer`、`automation` 客户端执行端到端场景；机器审批绕过测试必须为负向门禁。
6. 实施期同步 API、ARCHITECTURE、OPERATIONS、SECURITY、MCP 工具说明和 CHANGELOG。

工具为 additive 新协议，不改变现有 REST 请求/响应。领域 service 与 operation descriptor 必须由 REST/MCP 共享，禁止为了兼容而复制两套业务规则。

## 11. 实施任务拆分

1. 管理入口与领域 operation 全量盘点、风险分类和不暴露理由审查。
2. tool registry、schema 校验、capability/风险中间件与错误映射。
3. 元数据、拓扑、指标、历史、审计的分页只读工具。
4. 低风险/止损 direct 工具与幂等层。
5. FR-208～211、FR-215～218 的逐 operation 危险工具 adapter。
6. own approval 查询、轮询、撤回与受控结果读取。
7. 审计、脱敏、限额、operation coverage 与禁止工具测试。
8. 外部 Agent 自动化场景、安全复核和实施期文档同步。

## 12. 测试与验收

### 12.1 自动化测试

- `tools/list` 按 observer/automation 能力过滤；伪造工具名、schema 外字段、越权 scope 稳定拒绝。
- 读取分页、时间窗、超大量数据、namespace 隔离与 FR-213 失效范围 fail-closed。
- 每个 direct 动作的 capability、幂等、止损审计和恢复动作升级审批覆盖。
- 每个危险工具只创建申请，业务表/Agent 在批准前零副作用；重复 key 返回同一申请。
- human 批准后 worker 自动执行；MCP 轮询看到 succeeded/failed，无第二次 execute。
- MCP 调 approve/reject REST、伪造 Principal、构造 permit、改审批载荷全部失败。
- Agent 命令/日志/文件/明文/payload 均进入审批；普通历史元数据读取不被误升级。
- registry coverage 覆盖 V1/V2 同义入口、body 分支与后台任务；新增未分类 operation 使测试失败。
- 工具清单中不存在通用 HTTP、SQL、路径读写或内部 service 调用。

### 12.2 端到端验收

- observer 查询线上或灰度 namespace 的服务器、指标和历史，不能执行任何写入或创建危险申请。
- automation 完成一次低风险 displayName 更新、一次止损暂停和一次危险 namespace 归档申请。
- human 在 `/approvals` 自审或审批外部 Agent 申请后，Beacon 自动执行；外部 Agent 只轮询结果。
- 申请被驳回、过期、撤回、漂移失败和执行失败时均为明确终态，重试须新建申请。
- 对 Agent 命令、实时日志、文件、敏感配置与 payload 各执行一次“先申请、后人审、再受控取结果”场景。
- 抽查审计可从 clientId → tool call → approvalRequestId → human → execution/result 完整追溯，且无敏感明文。

## 13. 风险与收口条件

- “覆盖所有操作”不等于提供通用代理；只有具备稳定领域契约、风险分类和显式 schema 的操作才能暴露。
- 隐藏 approve 工具不是安全边界；机器审批服务层负向测试未通过前不得启用 automation profile。
- 工具数量会随领域增长，必须依赖 registry/coverage test 防漂移，不能手工复制 REST 路由。
- 本规格自动化测试绿不能替代真实外部 MCP 客户端、TLS 反代和人审执行验收。

## 14. 已拍板决定

- MCP 只提供显式领域工具，不提供通用 HTTP、SQL、文件或数据库代理。
- observer 可直接读取元数据、指标和历史；automation 可直执低风险动作。
- 任意 Agent 命令、实时日志、文件、敏感明文与 payload 访问都必须审批。
- 危险工具只返回 approvalRequestId；外部 Agent 可查询/撤回自己的申请，但没有审批工具。
- human 批准即由 Beacon 持久执行器自动执行，外部 Agent 只轮询结果。
- 机器不可审批是 Principal/服务层不变量，不依赖 MCP UI 或工具隐藏。
