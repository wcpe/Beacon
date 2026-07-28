# 规格：通用危险操作审批内核（FR-207）

> 状态：草拟 · 关联 PRD：FR-207 · 决策：[ADR-0079](../adr/0079-principal-capability-and-dangerous-operation-approval.md) · 本文件是全仓唯一审批真源

## 1. 背景与目标

现有身份确认、交付变更单、自审确认与二次确认各自表达“审批”，但没有覆盖所有危险操作的统一冻结、过期、恢复执行与审计证据。仅在前端弹窗或路由上拦截，无法阻止内部 service、API key、未来 MCP 或后台任务绕过。

FR-207 提供唯一的审批申请、决策、24 小时过期、持久自动执行、许可验证和历史查询模型。领域规格只登记 `OperationDescriptor` 与 adapter，不得复制状态机、审批表或审批 API。

## 2. 范围与不做

### 2.1 范围内

- 不可修改的冻结申请、统一状态机、human 决策与申请人撤回。
- 数据库驱动的进程内执行 worker，CAS/lease、有限恢复重试和执行收据。
- 不可由 HTTP/MCP/普通后台代码构造的 `ExecutionPermit`。
- 通用申请/列表/详情/批准/拒绝/撤回 API 与安全摘要视图。
- 审批页所需的排序、过滤、状态与执行结果契约。

### 2.2 不做

- 不保存或返回 token、secret、敏感文件明文、日志正文、消息 payload。
- 不承担 identity、ChangeOrder、配置、拓扑、AgentCommand 等领域状态机。
- 不引 Redis、MQ、外部 worker、分布式锁或第二服务。
- 不做会签、多人票决、代理审批、职责分离或自定义审批流；human 可自批。
- 不允许批准后由申请方再调用公开 execute/start；批准即进入持久自动执行。

## 3. 权威数据模型

遵循 GORM 可移植约束：枚举为 VARCHAR + 应用层校验、JSON 为 TEXT、UTC DATETIME、不建数据库外键、不使用部分索引或方言专有类型。

### 3.1 `approval_request`

| 字段 | 类型/约束 | 说明 |
|---|---|---|
| `id` | BIGINT PK 自增 | 内部主键 |
| `request_id` | VARCHAR(48) 唯一 | `apr_` + crypto/rand 小写 hex；公开不透明 ID |
| `operation_key` | VARCHAR(96) 索引 | 稳定语义 operation |
| `operation_schema_version` | BIGINT | 冻结参数的 adapter 版本 |
| `required_capability` | VARCHAR(96) | 创建时冻结，不由客户端决定 |
| `risk_level` | VARCHAR(16) | `high` / `critical` |
| `namespace_id` | BIGINT 可空、索引 | 有隔离域时必填 |
| `target_type` / `target_ref` | VARCHAR | 安全目标引用，不用 displayName 做身份 |
| `requester_type` / `requester_id` | VARCHAR | 冻结申请 Principal；稳定 ID |
| `requester_display_name` | VARCHAR(128) | 仅历史展示快照 |
| `request_reason` | VARCHAR(512) | 必填，去首尾空白后非空 |
| `safe_summary` | TEXT | adapter 生成的脱敏人类摘要 |
| `frozen_payload` | TEXT | 规范化、脱敏、可执行参数；不得含明文敏感内容 |
| `frozen_payload_sha256` | CHAR(64) | 对规范化 payload 计算 |
| `impact_snapshot` | TEXT | 脱敏影响清单/计数/版本摘要 |
| `precondition_sha256` | CHAR(64) | 目标版本与领域前置事实的规范化 hash |
| `idempotency_key` | VARCHAR(64) | 申请方提供；同主体+operation 内唯一 |
| `status` | VARCHAR(16) 索引 | 状态机见 §4 |
| `decider_type` / `decider_id` | VARCHAR 可空 | 只能是 human |
| `decision_reason` | VARCHAR(512) 可空 | 拒绝必填、批准可选 |
| `decided_at` | DATETIME 可空 | 批准或拒绝时间 |
| `expires_at` | DATETIME 索引 | 创建时间 + 固定 24h，不可修改 |
| `attempt_count` | BIGINT | worker 认领次数 |
| `lease_token` | VARCHAR(64) 可空 | crypto/rand，不对外返回 |
| `lease_until` | DATETIME 可空、索引 | worker 崩溃恢复依据 |
| `last_error_code` / `last_error_summary` | VARCHAR/TEXT 可空 | 脱敏错误；绝不含 payload/secret |
| `result_type` / `result_ref` | VARCHAR 可空 | 指向领域结果/任务/命令/访问许可 |
| `version` | BIGINT | 状态 CAS，初始 1，每次迁移 +1 |
| `created_at` / `updated_at` / `finished_at` | DATETIME | UTC |

唯一索引：`uniq(request_id)`、`uniq(requester_type, requester_id, operation_key, idempotency_key)`。列表索引至少覆盖 `(status, created_at, id)`、`(namespace_id, status, created_at)`。

### 3.2 `approval_execution_receipt`

该表解决“领域事务已提交、worker 尚未来得及把申请置 succeeded 就崩溃”的重放窗口：

| 字段 | 类型/约束 | 说明 |
|---|---|---|
| `id` | BIGINT PK | |
| `request_id` | VARCHAR(48) 唯一 | 每个申请至多一份领域提交收据 |
| `operation_key` | VARCHAR(96) | 与申请一致 |
| `frozen_payload_sha256` | CHAR(64) | 与 permit 一致 |
| `result_type` / `result_ref` | VARCHAR | 领域事实引用 |
| `result_summary` | TEXT | 脱敏摘要 |
| `committed_at` | DATETIME | 与领域副作用同事务写入 |

收据不保存业务内容。无收据时不能凭日志推断已执行；有收据的重试只做 request 状态收敛，不重复领域副作用。

## 4. 状态机

```text
pending ──申请人撤回──> withdrawn
   ├────human 拒绝───> rejected
   ├────24h 到期─────> expired
   └────human 批准───> executing ──领域收据──> succeeded
                                  └─确定失败──> failed
```

- `withdrawn`、`rejected`、`expired`、`succeeded`、`failed` 均为终态，不得恢复、编辑、再次批准或原单重试；需要重新提交新申请。
- 申请人只能在 `pending` 撤回自己的申请；human 与 machine 规则相同。
- 只有 human 能批准或拒绝；允许 `requester_type=human && requester_id=decider_id`。
- 批准事务以 CAS `pending + version` 更新为 `executing`，同时记录 human 决策、清空 lease、写强审计；提交后唤醒 worker。不存在公开 `approved` 或“等待手动 start”状态。
- 24h 只约束 pending。批准时必须再次比较数据库时间；在到期边界，approve 与 expire 只有一个 CAS 胜者。进入 executing 后不因原 expiresAt 到期而取消。
- 业务/前置条件错误立即 `failed`。仅无任何可观察领域提交的瞬时基础设施错误可在同一 executing 状态内由 lease 恢复，最多认领 3 次；第 3 次仍失败则终态 failed。

## 5. 冻结、hash 与申请幂等

### 5.1 adapter 冻结

客户端只能提交 `operationKey + typed parameters + reason`。注册 adapter 必须：

1. 校验 Principal capability、namespace 隔离、输入枚举/长度与目标存在性。
2. 读取权威当前事实，生成固定 schemaVersion 的 typed frozen struct；集合按稳定 ID 排序，禁止把无序 map 直接作为 hash 输入。
3. 生成 `safeSummary`、`impactSnapshot` 与 `precondition`；displayName 仅展示，目标引用使用稳定 code/id。
4. 使用 UTF-8、无多余空白的确定性 JSON 计算 SHA-256 小写 hex。
5. 剔除 token、secret、凭据 hash、敏感文件/日志/消息正文及可逆内容；需要读取内容时只冻结资源引用、版本/hash 与范围。

申请创建后所有冻结字段不可更新。执行时 adapter 重新读取权威事实并计算 precondition；不相等即 failed `approval_target_changed`，不得静默扩大/缩小目标。

### 5.2 幂等

- 所有申请入口必须携带 `Idempotency-Key`（1..64 个可打印 ASCII，不允许空白控制符）。
- 同 Principal + operation + key 重放且 frozen payload hash 相同，返回原申请；hash 不同返回 409 `idempotency_key_reused`。
- 批量目标必须在冻结前排序去重；同一集合不同提交顺序得到同一 hash。
- 决策 endpoint 对已由同一 human 完成的相同决定返回当前视图并标 `replayed=true`；相反决定或其他终态返回 409。

## 6. 持久 worker、lease 与 ExecutionPermit

### 6.1 认领与恢复

- 单进程 worker 在 approve 提交后被内存信号唤醒，并以不超过 5 秒的 DB 轮询作重启/丢信号兜底。
- 认领使用 `status=executing AND (lease_until IS NULL OR lease_until < now) AND version=?` 的 CAS，写随机 leaseToken、`lease_until=now+30s`、attempt+1、version+1。
- 长于 10 秒的 adapter 仅可续租自己的 leaseToken；CAS 失败立即停止，不能继续产生副作用。
- worker 重启只扫描 DB；不依赖内存队列。不得执行阻塞 HTTP 回调原管理路由。

### 6.2 `ExecutionPermit`

Permit 是进程内不可序列化值，至少绑定 requestId、operationKey、schemaVersion、payloadHash、leaseToken。构造函数仅审批 worker 可见；handler、MCP、测试外部输入和普通 system task 无法构造。

危险领域 service 必须把 permit 作为必填参数，并在产生副作用的数据库事务内调用 verifier：

1. 查询申请与 lease，确认 status=executing、human 决策存在、operation/hash/token 完全一致、lease 未过期。
2. 重算领域 precondition，匹配冻结 hash；领域排空、状态机、冲突等守卫全部再次执行。
3. 应用领域变更或创建已有领域 command/task 事实。
4. 写领域强审计与 `approval_execution_receipt`；任一失败整体回滚。

审批证据数据库不可达、记录缺失、hash/lease 不符或审计失败时一律不产生副作用。permit 不能跨 request、operation 或目标复用。

### 6.3 执行完成边界

- 本地 DB 变更：领域事务提交即有 receipt；worker 随后 CAS request→succeeded。
- Agent 命令/长任务：adapter 在事务内创建既有 `agent_command`/ChangeOrder/任务行与 receipt，提交后才唤醒；审批 request 的 succeeded 表示“已持久接受执行”，长流程终态仍以领域状态机为真源，并通过 resultRef 下钻。
- worker 在领域提交后崩溃：下一次认领先查 receipt，发现匹配即只收敛 request→succeeded。
- adapter 返回确定性校验失败且无 receipt：记录脱敏错误并 request→failed；不自动改回 pending。

## 7. API 契约

错误统一 `{code,message,traceId}`；时间 UTC ISO-8601；列表服务端分页。所有响应只返回安全摘要，不返回 frozenPayload 原文、lease、凭据或敏感内容。

| 方法 | 路径 | 契约 |
|---|---|---|
| POST | `/admin/v2/approval-requests` | `{operationKey,parameters,reason}` + `Idempotency-Key`；仅调用注册 adapter 冻结，成功 `202` + Location；未知 operation fail-closed |
| GET | `/admin/v2/approval-requests` | `status? operationKey? riskLevel? namespaceId? requesterType? requesterId? from? to? page? pageSize?`；普通 human 看全部，machine 只看自己，1000+ 分页 |
| GET | `/admin/v2/approval-requests/{requestId}` | 安全详情、影响摘要、决定、执行状态、resultRef；machine 仅自己的申请 |
| POST | `/admin/v2/approval-requests/{requestId}/approve` | human；`{reason?}`；CAS pending→executing，返回 `202` |
| POST | `/admin/v2/approval-requests/{requestId}/reject` | human；`{reason}` 必填；pending→rejected |
| POST | `/admin/v2/approval-requests/{requestId}/withdraw` | 仅申请人；`{reason?}`；pending→withdrawn |

现有危险 POST/PUT/DELETE 路由可作为兼容申请入口：内部调用同一 `Request`，返回 `202 {approvalRequest}`，不得执行原副作用。approval_required 的旧 GET 不创建申请，返回 409：

```json
{"code":"operation_requires_approval","requestEndpoint":"/admin/v2/approval-requests","operationKey":"...","traceId":"..."}
```

关键错误码：`machine_principal_cannot_decide`、`approval_not_found`、`approval_illegal_state`、`approval_expired`、`approval_not_owner`、`approval_target_changed`、`approval_execution_failed`、`idempotency_key_reused`。

## 8. 审计与保留

- 状态迁移事件：`approval.requested/withdrawn/rejected/expired/execution_started/execution_succeeded/execution_failed`。
- 创建、决定、认领、receipt 与结果迁移均与审批真源同事务；FR-72 兜底审计的 fail-open 行为不能用于本域。
- 审计至少含 requestId、operationKey、principalType/id、targetRef、payloadHash、前后状态、reason/安全错误码、traceId；不含 frozenPayload、secret 或内容。
- 审批申请与 receipt 永久保留；列表按时间分页，不做热冷归档或物理删除入口。若未来需要保留期，另立 FR。

## 9. 领域状态机兼容

- identity 的 `pending/active/...` 与 ChangeOrder 的 `draft/pending_approval/...` 仍由原规格拥有；approval_request 不复制这些字段。
- 领域“提交审批”可在同一事务建立领域 pending 投影与 approval request；拒绝/撤回/过期回调只走 adapter 明确的合法迁移。
- identity 的 `/approve` 变为创建通用申请；human 在通用审批页批准后，adapter 直接执行原 T3，不再出现第二次身份审批。
- ChangeOrder 提交创建通用申请；human 批准后 adapter 记录既有批准事实并自动进入持久 start 路径，不再要求公开 start 再确认。执行前仍跑 ADR-0071 冲突守卫。
- 本规格不定义各领域具体映射；FR-208/209 与后续领域规格只提供 descriptor/adapter。

## 10. UX 数据契约

审批页至少支持“待我处理、我的申请、全部记录”三视图；列表显示 operation 安全名称、风险、申请人、目标、影响计数、剩余时间与状态。详情显示冻结摘要、版本/hash、原因、决定和 resultRef，不显示原始 payload。human 在详情执行“批准并执行”或拒绝；本人申请不隐藏批准按钮。machine 登录态不显示决定入口，后端仍硬拒。

批准后页面立即显示 executing，并轮询详情直至 succeeded/failed；页面刷新不得丢状态。失败、拒绝、过期只提供“按当前事实重新申请”，不能原单重试。大列表必须分页，空态/加载/错误/过期并发冲突均有明确中文文案。

## 11. 实施任务与测试纪律

1. 先写失败测试：完整状态迁移表、24h 边界、approve/withdraw/expire 并发 CAS、machine 决策拒绝、自批、幂等 key/hash。
2. 写 adapter 冻结与规范化 hash 测试：集合乱序一致、敏感字段绝不进入快照/审计。
3. 写 worker 集成红测：批准后无人二次调用仍执行、重启恢复、lease 竞争仅一赢家、3 次基础设施失败、领域提交后崩溃凭 receipt 收敛且不重复。
4. 最小实现表/repository/service/worker/permit/API；不引 MQ，不为单次执行建立通用工作流引擎。
5. 先为一个假领域 adapter 跑通，再由 FR-208/209 接真实领域；领域危险方法没有 permit 时编译或运行测试必须失败。
6. 完成后由未参与实现的代理独立做并发与旁路复核，重点直接调用 service、伪造 requestId/lease、审计失败、DB 故障和 worker 崩溃窗口；先补红测再修。

## 12. 验收标准

1. 申请创建后冻结字段不可改，固定 24h；过期与批准竞态只有一条合法终态路径。
2. human 可自批；api_key/mcp/system 通过任何入口都不能批准或拒绝；申请人仅可撤回自己的 pending。
3. 批准提交后不依赖浏览器或申请方二次调用，控制面重启后仍自动认领执行。
4. 两个 worker/并发请求只能一个 lease 获胜；领域事务提交后模拟崩溃，恢复不重复副作用。
5. permit 缺失、伪造、过期、hash 不符、审批/审计 DB 失败均 fail-closed，危险 service 无副作用。
6. rejected/withdrawn/expired/failed 无恢复入口；重新提交产生新 requestId 并关联新快照。
7. API 列表/详情分页与安全摘要符合契约，任何数据库行、日志、审计、错误均不含凭据和敏感正文。
8. `go test ./...`、真实数据库 integration 门、前端审批消费测试与 build 全绿；独立复核无未处理高风险问题。

## 13. 固定边界

- pending TTL 固定 24 小时，不做运维配置。
- worker 最大认领 3 次、lease 30 秒，属于实现常量；需要调整时先以故障证据修改规格，不预留设置项。
- human 自批与批准即自动执行均不可由部署配置关闭。
