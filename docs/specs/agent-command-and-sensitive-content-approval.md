# 功能规格：Agent 命令与敏感内容访问审批

> 状态：草拟　·　关联 PRD：FR-209　·　分支：待执行时创建　·　依赖：FR-206、FR-207、[ADR-0079](../adr/0079-principal-capability-and-dangerous-operation-approval.md)

## 1. 背景与目标

Beacon 已有反向抓取、日志尾取、文件浏览、强制重同步、受管任务等 Agent command，也能查看消息 payload 和敏感配置/文件内容。这些动作即使使用 GET 或“只读”名称，仍会让在线 Agent 执行命令或向操作者返回敏感明文；只靠 full/readonly、前端确认或原因审计不能阻止 API key、MCP、旧路由和内部调用绕过。

本规格把“任何 Agent 命令”和“任何实时日志、文件内容、敏感明文、消息 payload 访问”统一登记为 `approval_required`。human 批准后，FR-207 worker 自动创建既有 AgentCommand/领域任务或短期受控访问授权；调用方只轮询/消费结果，不再执行第二次 start。普通数据库元数据、指标和脱敏历史继续直接读取。

## 2. 范围与非目标

### 2.1 本期范围

- 所有控制面→Agent 命令类型，包括现有与后续的 scan、submit、imprint、tail-logs、fs-browse、resync、reload、restart 等。
- 在线 Agent 实时日志、文件/目录内容与反向抓取结果。
- 敏感配置明文、敏感文件内容和消息 payload 访问。
- 命令任务/受控访问授权的 resultRef、一次性/短期消费、结果证据和审计。
- REST V1/V2、MCP、管理台与内部服务入口的完整 operation 覆盖和 ExecutionPermit 守卫。

### 2.2 明确不做

- 不把普通元数据、指标、健康摘要、命令状态、消息头/链路或脱敏历史升级为危险读取。
- 不在审批申请、审计、普通日志或指标标签中保存日志正文、文件内容、配置明文、payload、token 或 secret。
- 不提供任意命令字符串、任意 shell、任意路径或通用 Agent RPC 工具；每个命令类型必须有稳定领域 operation 和输入 schema。
- 不重写既有 AgentCommand、反向抓取、文件浏览、交付或消息领域状态机。
- 不把 ExecutionPermit 返回给浏览器/MCP，也不让 system policy 生成命令或敏感访问许可。
- 不用审批绕过 Agent 侧既有 plugins 根、path traversal、二进制、大小、脱敏和 namespace 身份校验。

## 3. 操作目录与分类

### 3.1 Agent 命令

每个具体命令登记独立 operation，例如：

| operation | 既有/目标能力 | 分类 |
|---|---|---|
| `agent.command.reverse_scan` | plugins 清单扫描 | `approval_required` |
| `agent.command.reverse_submit` | 回传选定文件内容 | `approval_required` |
| `agent.command.imprint` | 单文件拓印/对照 | `approval_required` |
| `agent.command.tail_logs` | 读取脱敏日志环形缓冲 | `approval_required` |
| `agent.command.fs_browse` | list/tree/file 浏览 | `approval_required` |
| `agent.command.resync` | BC/Agent 目录或配置强制重同步 | `approval_required` |
| `agent.command.reload` / `restart` | 配置热重载/进程重启 | `approval_required` |

命令状态查询、失败原因摘要和已脱敏的执行元数据为 `direct read`。暂停/取消仍未执行或正在运行的命令属于 `direct + 强审计` 止损；恢复、重新开始或重试必须新建审批申请。

### 3.2 敏感内容访问

| operation | 内容 | 分类 |
|---|---|---|
| `config.sensitive_plaintext_read` | 标记 sensitive 的配置有效值/版本明文 | `approval_required` |
| `file.sensitive_content_read` | 标记敏感或命中敏感路径的文件内容 | `approval_required` |
| `message.payload.read` | 消息 payload | `approval_required` |

在线日志与 Agent 在线文件内容分别以 `agent.command.tail_logs`、`agent.command.fs_browse/reverse_submit/imprint` 作为唯一对外 operation；底层“读取正文”不是第二个审批 operation。对于会返回敏感正文的命令，同一份批准必须同时创建命令/任务与绑定的 grant，正文只凭该 grant 消费，不能先批命令再批内容，也不能只批命令后直接返回正文。

同一 endpoint 若既能列元数据又能返回正文，必须在服务端按规范化输入拆分 operation；客户端传 `includeContent=true`、path、mode 或 body 分支不能继续沿用普通 read 分类。

文件资产的搜索、扫描概要和哈希比对，消息的列表、详情和链路聚合，以及命令状态和脱敏结果摘要始终是 `direct read`：它们不得因为同一领域存在正文消费端点而返回 `operation_requires_approval`，且响应不得携带正文。

跨服务器文件内容差异不是普通元数据比对：发起方必须为左、右两侧各建一份 `agent.command.fs_browse` 审批申请。两份申请分别冻结本侧与对侧的 namespace、serverId、path、清单 SHA-256 和同一不透明 pairId；任一申请拒绝、撤回、过期、目标离线或任一侧版本漂移时，整组不得生成结果。批准 worker 对每侧各建一条 `asset-read` 命令和一份 pending grant；单侧 pair grant 永不允许走普通正文消费。原申请主体仅可凭任一侧 grant 发起一次双侧原子消费：服务端同时校验并消费两份 grant 后，在内存中比较回传内容，只返回 `identical`/`changed`/`unsupported` 与已可直接读取的 hash、size、path 元数据；两侧正文、diff 片段和 pairId 均不落库、不进审批详情、审计、日志或 MCP 响应。

### 3.3 覆盖不变量

- 所有创建 AgentCommand/在线任务或返回敏感正文的最终 service 都要求 ExecutionPermit 或消费由批准 worker 生成、绑定申请人的 `SensitiveAccessGrant`。
- `system_exempt` 不能映射本表 operation。普通定时任务只能清理过期结果，不得自行下发命令或读取内容。
- 新命令 type、新内容字段或旧路由别名未登记时测试失败、运行时 fail-closed。
- REST、MCP 与页面只能选择已登记 typed operation，不提供 `commandType + arbitraryPayload` 或任意 path 代理。

## 4. 冻结申请与执行

### 4.1 命令申请

adapter 冻结 requester Principal、namespace、目标 identity/serverId、具体 operation/schema、规范化参数、在线/身份/版本事实、超时/数量上限、目标集合 hash、原因和脱敏影响摘要。命令 payload 中的 secret/正文不得进入通用审批表。

human 批准后 worker 携 ExecutionPermit 调领域 adapter：

1. 重查 identity active、namespace/server 生命周期、Agent 在线能力与目标版本；漂移则申请 `failed`。
2. 在事务中创建既有 `agent_command`/领域任务、强审计与 approval receipt，resultRef 指向命令/任务。若该命令会返回敏感正文，同一事务还创建唯一的 pending grant，receipt 使用可解析 commandId + grantId 的类型化领域 resultRef；不得拆成第二份审批。
3. 提交后才通过既有 SSE/拉取机制唤醒 Agent；不在审批 worker 内等待网络结果。
4. approval request `succeeded` 表示命令已可靠入队，不代表 Agent 执行成功；真实终态仍读取 resultRef 对应领域状态。

重复 worker 先查 receipt，不重复下发。Agent 离线、命令冲突或能力不支持属于确定失败或领域任务失败，必须保留批准事实和安全错误摘要。

### 4.2 `SensitiveAccessGrant`

静态内容或实时日志批准后，worker 创建独立、不可外部伪造的受控访问授权，而不是把内容写入 approval_request：

| 字段 | 语义 |
|---|---|
| grantId / approvalRequestId | 不透明引用与批准证据 |
| requesterType/requesterId | 只允许原申请主体消费 |
| operation / targetRef / contentVersionHash | 固定内容类型、目标和版本 |
| limits | 既有领域大小/行数上限；客户端只能缩小 |
| expiresAt | 固定短期，最长 5 分钟 |
| maxUses / usedAt | 静态内容一次成功读取；实时日志一次有界会话 |
| status | pending/active/consumed/expired/revoked |

- 不经 Agent 的既有敏感内容读取在批准事务中直接创建 active grant 与 receipt；receipt 固定写 `resultType=sensitive_access_grant`、`resultRef=grantId`。
- 会返回敏感正文的 Agent command 在同一批准事务中创建 command、pending grant 与一份 receipt，`resultType=agent_sensitive_operation` 的 resultRef 可解析 commandId + grantId；Agent 成功回传并通过版本/大小/脱敏校验后，将回传正文哈希原子绑定到 commandId 对应 pending grant 并 CAS 为 active，5 分钟从激活时计算。命令失败则 grant 进入 revoked/终态，不产生可消费正文。
- 两类 grant 均与 `approval_execution_receipt` 同事务，`approval_request_id` 建唯一约束，供审批详情和 MCP `own.get` 返回权威引用。
- worker 在事务提交后、申请状态收敛前崩溃时，下一次认领先查 receipt 并只把原 request 收敛为 succeeded；不得重复创建命令/任务、签发第二份 grant、延长 expiresAt 或重置 maxUses。
- grant 创建事务必须验证申请仍为 executing、human 决策与 ExecutionPermit 完全匹配；没有批准事实或 receipt 写失败时不得留下 active grant。
- 静态文件、配置明文与 payload 只允许一次成功读取；失败且未返回任何内容可在过期前安全重试。
- 实时日志授权只允许一个最长 5 分钟的有界会话，受既有日志环形缓冲和传输上限约束；不读取磁盘日志，不扩展到其它 server。
- 消费时重新校验原 Principal、批准事实、grant 状态、目标版本和 namespace 权限；任何不匹配失败关闭。
- 内容沿既有领域通道返回并遵守 at-rest/内存清理规则；grant、审计和日志只记录 hash、字节/行数、时间与结果，不保存正文。
- 过期、消费或申请主体凭据吊销后 grant 不可使用；不得恢复或转让，重新访问必须新建申请。

### 4.3 现有瞬态结果兼容

现有 `log_content`、`browse_result`、`imprint_content`、`submit_content` 等瞬态字段继续由原领域拥有，但读取必须绑定 grant。既有“等待中的 admin 取一次”改为“原批准 Principal 凭 grant 取一次”；清理器按更早的领域过期或 grant 过期清空，不延长正文保留。

敏感配置解密仍使用 ADR-0018 既有 cipher；本 FR 不新增第二加密设施。payload/文件内容未获批准时不得为了生成预览而先解密/抓取正文，申请只冻结引用、版本/hash 和范围。

## 5. API 与 MCP 契约

| 方法 | 路径 | 语义 |
|---|---|---|
| POST | `/admin/v2/approval-requests` | typed command/content parameters + reason + Idempotency-Key，返回 pending 申请 |
| GET | `/admin/v2/agent-operations/{resultRef}` | 查询命令/任务与绑定 grant 的安全状态摘要，不返回敏感正文 |
| POST | `/admin/v2/sensitive-access-grants/{grantId}/consume` | 原申请主体一次性消费批准内容/日志会话 |
| POST | `/admin/v2/assets/pair-read/approval-requests` | 左右两侧分别创建文件差异读取审批；请求体 `{left:{serverId,path},right:{serverId,path},reason}`，返回两个 requestId |
| POST | `/admin/v2/assets/pair-read/grants/{grantId}/consume` | 原申请主体以任一侧 grant + `{commandId}` 原子消费两侧授权，只返回脱敏差异摘要 |
| POST | `/admin/v2/agent-operations/{resultRef}/cancel` | 未完成命令/任务止损取消，direct + 强审计 |

旧 `/admin/v1` 日志、文件浏览、payload、命令、重同步等入口不得直接下发或返回正文：危险写入口作为兼容申请入口返回 `202`；旧 GET 不隐式创建申请，返回 `409 operation_requires_approval` 与明确 operation/request endpoint。MCP 由 FR-220 暴露每个具体显式工具，高危调用只返回 approvalRequestId；批准后用 own approval/resultRef 工具查询，敏感内容工具再消费同一 grant，不存在 approve 工具。

错误体统一 `{code,message,traceId}`。关键错误：`operation_requires_approval`、`agent_target_changed`、`agent_offline`、`sensitive_access_expired`、`sensitive_access_consumed`、`sensitive_access_wrong_principal`、`content_version_changed`、`content_limit_exceeded`。

## 6. 审计、隐私与性能

- 审计覆盖 requested/approved/task_enqueued/task_finished/grant_issued/grant_consumed/grant_expired/cancelled，关联 requestId、resultRef/grantId、主体、目标、类型、hash、大小/行数摘要与 traceId。
- 审计、approval、普通日志、错误和指标标签不含正文、payload、secret、Authorization、任意 credential hash 或完整路径中的敏感段。
- Agent 失败日志使用中文 WARN/ERROR，按 commandId/target 聚合，不为每次轮询刷屏；正常内容读取不打正文 INFO。
- 批量命令目标分页并冻结完整排序 hash；不得循环内查库/远程下发。worker 只批量建任务，实际 Agent IO 由既有异步通道执行。
- 内容、日志和 payload 沿既有大小/截断上限；超限不自动扩大，必须改为更小范围重新申请。

## 7. UX / 交互

- 用户任务：从服务器、命令、文件、配置或消息详情提交明确的命令/敏感访问申请；human 审批后，申请人查看任务状态或一次性受控结果。
- 进入路径：保留现有日志/文件/重同步/payload 等按钮，但改为影响预览和“提交审批”；审批中心深链回原领域 resultRef。
- 操作闭环：选择具体目标/范围 → 填原因 → 提交 → human 比较快照并批准 → 后端自动入队命令或签发 grant → 页面轮询任务/一次性消费 → 查看安全审计；取消命令可立即止损。
- IA：命令状态仍在原领域页，统一决策在 `/approvals`；不新增通用命令终端或文件浏览器。

页面四态：

- **空态**：目标离线/无日志/无文件/无 payload 或无可执行命令时说明原因，不创建无效申请。
- **加载态**：目标事实、影响摘要、审批、任务和一次性结果各自显示骨架；未取得 grant 前绝不预取正文。
- **错误态**：目标漂移、Agent 离线、执行失败、grant 过期/已消费、内容版本变化分别给出脱敏原因与重新申请入口，不复用旧批准。
- **超大量态**：命令目标、目录项和历史服务端分页；日志/文件/payload 严格截断，批量目标由服务端完整 hash，浏览器不持有无界正文。
- **常规态**：明确区分“审批已成功入队”和“Agent 已执行成功”；一次性内容查看前提示 5 分钟/一次限制，消费后立即进入只读已消费态。

把现有直接命令/内容查看改成审批与一次性结果是主流程结构性变化。接真前必须提供命令、日志、文件、敏感配置和 payload 五类可点击 mock，覆盖上述四态、批准后自动入队、结果轮询、一次性消费与止损取消，经用户浏览器评审拍板。

## 8. 实施任务拆分

1. 清点所有 AgentCommand type、在线内容、敏感配置/文件/payload 的 V1/V2/页面/MCP/内部入口，建立可执行 operation 覆盖表。
2. 运行命令、SSE、反向抓取、文件浏览、日志、配置加密、消息 payload、审计与 web 测试基线。
3. 测试先红：每个命令/内容入口未批准零副作用，system/machine 禁批/禁 permit，旧 GET/POST 无旁路。
4. 测试先红：敏感命令一份审批原子创建 command+pending grant+receipt、非命令内容创建 active grant、重启幂等、目标漂移、一申请一授权、激活/崩溃恢复、主体/版本/TTL/一次消费、正文不入审批/审计/日志、大小上限。
5. 先完成五类四态可点击 mockup 与用户浏览器评审。
6. 最小实现 Agent command adapters、SensitiveAccessGrant 与最终 service 守卫，复用既有任务/瞬态结果/cipher/清理器，不新增通用 RPC。
7. 接入统一申请、resultRef、MCP 显式工具和管理台结果消费；封死旧路由与内部 service 直调。
8. 独立安全复核任意 command/path/body 分支、grant 转让/重放、日志/payload 泄漏、批量 N+1、进程重启和 diff 范围。
9. 重跑相关完整门禁；实施期同步 API、ARCHITECTURE、UX、OPERATIONS、SECURITY、Agent 协议、旧 specs 与 CHANGELOG。

## 9. 测试与验收

### 9.1 自动化验收

1. 每个 Agent command type、实时日志、文件内容、敏感明文和 payload operation 均为 approval_required；新增未登记入口使覆盖测试失败。
2. 批准前不创建命令、不唤醒 Agent、不解密/读取正文；API key/MCP/system 无法批准或构造 ExecutionPermit。
3. human 批准后无需申请方第二次 start，命令/任务与 receipt 原子持久化；worker 崩溃/重试不重复下发。
4. approval succeeded 与领域命令终态严格区分，resultRef 可追踪 pending/running/succeeded/failed/cancelled。
5. SensitiveAccessGrant 只允许原申请主体、固定目标/版本、5 分钟内一次消费；过期、转让、重放、凭据吊销和版本漂移均失败关闭。
6. 每个敏感读取用户动作恰有一份审批和至多一份 grant；Agent 命令、pending grant 与 receipt 原子创建，回传成功后才激活且无需二次审批；事务后崩溃/worker 重试不重复命令、不产生第二份授权或延长期限。
7. approval、数据库普通字段、审计、日志、错误和指标中无日志正文、文件内容、配置明文、payload、secret；只留 hash/计数/结果证据。
8. Agent 侧 plugins 根/path traversal/二进制/大小/脱敏/身份守卫全部保持；批准不能绕过任何既有保护。
9. 批量目标和查询分页有界，无循环 DB/远程调用、无无界内存加载；取消为 direct 止损，恢复/重试新建审批。

### 9.2 真实环境验收

1. 对真实 Agent 分别提交 resync/日志/文件命令；human 自批或审批外部 Agent 申请后自动入队，领域结果与审批结果均可追溯。
2. 对真实敏感配置、敏感文件和消息 payload 各完成一次审批与一次性读取；第二次读取、转给另一主体和过期读取均失败。
3. 批准前让 Agent 离线或修改内容版本，旧申请 failed 且不返回旧/新正文；重新申请后成功。
4. 取消运行中命令立即止损并强审计；重新开始必须新申请。
5. 五类四态 mockup 先经用户浏览器评审，接真后再检查数据库、日志和审计无敏感明文。

## 10. 固定边界

- 任何 Agent 命令都必须审批；命令名为“读取/同步/诊断”不能降级风险。
- 实时日志、文件内容、敏感配置明文、敏感文件和消息 payload 都必须审批；普通元数据、指标和脱敏历史可直接读。
- 批准即由 Beacon 自动入队命令或签发短期 grant；申请方不再调用 execute/start。
- approval 只保存脱敏意图和证据，敏感正文永不进入 approval/audit/log；一次性 grant 不能转让、恢复或扩权。
