# 功能规格：Agent 命令与敏感内容访问审批

> 状态：草拟　·　关联 PRD：FR-209　·　分支：待执行时创建　·　依赖：FR-206、FR-207、[ADR-0079](../adr/0079-principal-capability-and-dangerous-operation-approval.md)

## 1. 背景与目标

Beacon 已有反向抓取、日志尾取、文件浏览、强制重同步、受管任务等 Agent command，也能查看消息 payload 和敏感配置/文件内容。这些动作即使使用 GET 或“只读”名称，仍会让在线 Agent 执行命令或向操作者返回敏感明文；只靠 full/readonly、前端确认或原因审计不能阻止 API key、MCP、旧路由和内部调用绕过。

本规格区分审批类操作与 `direct + 强审计` 内容读取。会改变状态的 Agent 命令、敏感配置明文、反向抓取/拓印及其他未列入直读清单的操作继续登记为 `approval_required`；实时脱敏日志、受限目录浏览、文件资产 preview/diff 与消息 payload 的管理面 REST 读取按 `content.read + management.read + direct` 执行，service 先写仅含目标/哈希/计数等元数据的审计再返回正文。`management.read` 包含机器主体，故 API key 自动化也可直接读取这些响应；此安全代价由 ADR-0093 明确接受。

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
| `agent.command.reverse_resolve` | 冲突审核后将抓取结果创建/覆盖到目标层 | `approval_required` |
| `agent.command.imprint` | 单文件拓印/对照 | `approval_required` |
| `agent.command.imprint.confirm` | 将已审拓印写入目标覆盖层 | `approval_required` |
| `content.read` | 读取脱敏日志、受限目录或文件资产/消息正文（管理面 REST） | `direct + 强审计`（`management.read`） |
| `agent.command.tail_logs` | 独立 MCP/内部工具的日志命令（若暴露） | `approval_required`（沿独立 tool adapter 策略） |
| `agent.command.fs_browse` | 独立 MCP/内部工具的文件浏览命令 | `approval_required`（沿独立 tool adapter 策略） |
| `agent.command.resync` | BC/Agent 目录或配置强制重同步 | `approval_required` |
| `agent.command.reload` / `restart` | 配置热重载/进程重启 | `approval_required` |

命令状态查询、失败原因摘要和已脱敏的执行元数据为 `direct read`。暂停/取消仍未执行或正在运行的 Agent 命令属于 `direct + 强审计` 止损；恢复、重新开始或重试 Agent 命令仍需审批。可撤销的文件同步任务 start/resume 则按 ADR-0093 使用 `management.direct + direct + 强审计`。

### 3.2 敏感内容访问

| operation | 内容 | 分类 |
|---|---|---|
| `config.sensitive_plaintext_read` | 标记 sensitive 的配置有效值/版本明文 | `approval_required` |
| `content.read` | 文件资产 preview/diff、日志、受限目录等指定 REST 内容读取 | `direct + 强审计`（`management.read`） |
| `message.payload.read` | 消息 payload 管理面 REST 直读 | `direct + 强审计`（`management.read`） |

REST 管理面指定在线日志、受限目录、文件资产与消息 payload 统一登记为 `content.read`，不创建审批 request 或 grant；读取成功前由对应 service 写一次专项审计。反向抓取 submit、拓印等其他会返回敏感正文的审批类 Agent 命令仍由同一份批准创建命令/任务与绑定 grant，正文只凭该 grant 消费，不能先批命令再批内容，也不能只批命令后直接返回正文。MCP/内部 tool adapter 按各自 catalog 保留独立授权策略。`reverse_submit` 的一份 grant 一次返回该任务的全部冲突 diff 包（仅冲突路径，绝不含非冲突正文），使多冲突审核不拆成多份审批或多份 grant。

同一 endpoint 若既能列元数据又能返回正文，必须在服务端按规范化输入拆分 operation；客户端传 `includeContent=true`、path、mode 或 body 分支不能继续沿用普通 read 分类。

文件资产的搜索、扫描概要和哈希比对，消息的列表、详情和链路聚合，以及命令状态和脱敏结果摘要始终是 `direct read`：它们不得因为同一领域存在正文消费端点而返回 `operation_requires_approval`，且响应不得携带正文。

跨服务器文件内容差异走 `content.read` 直执：`POST /admin/v2/assets/pair-read/approval-requests` 接收左右 `serverId/path` 与可选原因，按既有清单校验后直接读取并返回两侧差异；敏感路径仍要求原因。兼容路径 `/pair-read/grants/{grantId}/consume` 只用旧 pair 记录定位目标，不校验申请主体或 grant 状态，按当前资产重新比较并返回脱敏差异摘要。两路均写 `asset.diff` 元数据审计；审计、日志与持久化记录不含两侧正文或 diff 片段。

### 3.3 覆盖不变量

- 审批类写命令/任务、敏感配置明文及其他审批读取仍要求 ExecutionPermit 或消费批准 worker 生成的 grant；`content.read` 例外只允许明确登记的只读读取，并要求 `management.read`、正文返回前 service 专项审计与无正文审计 detail。
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

拓印回传进入 `ready` 后，确认写入是另一项危险操作：申请仅冻结命令标识、目标层、已审内容 md5 与脱敏影响，不保存正文；批准 worker 必须在同一事务内重新校验命令仍为 `ready`、正文 md5 与冻结值一致，再写文件版本、命令终态、审计与 receipt。任何公开确认入口不得直接写入。

重复 worker 先查 receipt，不重复下发。Agent 离线、命令冲突或能力不支持属于确定失败或领域任务失败，必须保留批准事实和安全错误摘要。

### 4.2 `SensitiveAccessGrant`

对于仍属 `approval_required` 的静态内容或实时日志操作，批准后 worker 创建独立、不可外部伪造的受控访问授权，而不是把内容写入 approval_request；ADR-0093 列出的 `content.read` REST 操作不使用该 grant：

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

现有 `log_content`、`browse_result`、`imprint_content`、`submit_content` 等瞬态字段继续由原领域拥有。审批类 `imprint_content` / `submit_content` 读取仍绑定 grant；ADR-0093 指定的日志与目录浏览 direct 读取由 service 审计，浏览结果在一次读取后清理，日志按既有命令生命周期清理。

敏感配置解密仍使用 ADR-0018 既有 cipher；本 FR 不新增第二加密设施。payload/文件内容未获批准时不得为了生成预览而先解密/抓取正文，申请只冻结引用、版本/hash 和范围。

## 5. API 与 MCP 契约

| 方法 | 路径 | 语义 |
|---|---|---|
| POST | `/admin/v2/approval-requests` | typed command/content parameters + reason + Idempotency-Key，返回 pending 申请 |
| GET | `/admin/v2/agent-operations/{resultRef}` | 查询审批类命令/任务的安全状态摘要，不返回敏感正文 |
| POST | `/admin/v2/assets/preview`、`/admin/v2/assets/preview/approval-requests` | 直接预览 `{serverId,path,reason?}`，原因仅在敏感路径时必填 |
| POST | `/admin/v2/assets/preview/grants/{grantId}/consume` | 兼容旧路径，body `{commandId,reason?}` 定位文件后 direct 重读，grantId 不作授权 |
| POST | `/admin/v2/assets/diff` | 直接比较 `{left:{serverId,path},right:{serverId,path},reason?}` 并返回差异两侧内容 |
| GET/POST | `/admin/v1/instances/{serverId}/logs`、`/admin/v1/instances/{serverId}/browse` | management.read 直执日志/受限目录读取：`POST` 创建读取命令（返回 `202 {commandId,status}`），`GET` 只按 `commandId` 读结果、**不创建也不启动**（缺 `commandId` → `400`；`GET /logs` 返回该实例最近一次日志读取结果，`namespace` 必填）；结果读取前先写专项审计 |
| POST | `/admin/v1/instances/{serverId}/logs/grants/{grantId}/consume`、`/admin/v1/instances/{serverId}/browse/grants/{grantId}/consume` | 兼容旧 grant 路径，以 commandId 读取结果，不校验 grant 申请主体 |
| POST | `/admin/v2/messages/{messageId}/payload`、`/admin/v2/messages/{messageId}/payload/approval-requests` | 直接读取 payload，body `{reason}`，返回正文/哈希/大小并先写元数据审计 |
| POST | `/admin/v2/sensitive-access-grants/{grantId}/consume` | 兼容旧路径名，body `{messageId,reason}` 按 `content.read` 直接读取 payload，`grantId` 不作授权依据 |
| POST | `/admin/v2/assets/pair-read/approval-requests` | 兼容旧路径名，body `{left:{serverId,path},right:{serverId,path},reason}` 直接读取并返回文件差异 |
| POST | `/admin/v2/assets/pair-read/grants/{grantId}/consume` | 兼容旧 pair 路径，body `{commandId,reason?}` 定位目标并直读，只返回脱敏差异摘要 |
| POST | `/admin/v2/agent-operations/{resultRef}/cancel` | 未完成命令/任务止损取消，direct + 强审计 |

列入 `content.read` 清单的 `/admin/v1` 日志/目录浏览与 `/admin/v2` 文件资产/message payload 路由直接返回读取结果，并要求 `management.read` 与 service 审计；旧 `/approval-requests`、`/grants/{grantId}/consume` 路径仅作 REST 兼容别名。反向抓取、拓印、重同步、敏感配置明文等未列入清单的入口仍按本规格审批。MCP 显式工具按其独立 catalog 策略运行，本次 ADR-0093 只调整指定管理面 REST 路由。

错误体统一 `{code,message,traceId}`。关键错误：`operation_requires_approval`、`agent_target_changed`、`agent_offline`、`sensitive_access_expired`、`sensitive_access_consumed`、`sensitive_access_wrong_principal`、`content_version_changed`、`content_limit_exceeded`。

## 6. 审计、隐私与性能

- 审计覆盖 requested/approved/task_enqueued/task_finished/grant_issued/grant_consumed/grant_expired/cancelled，关联 requestId、resultRef/grantId、主体、目标、类型、hash、大小/行数摘要与 traceId。
- 每次 `content.read` 成功读取均由 service 先写一条专项审计；审计失败时不返回正文，兜底中间件不重复记录。
- 审批类审计、普通日志、错误和指标标签不含正文、payload、secret、Authorization 或任意 credential hash；`content.read` 专项审计可记 serverId/path、内容哈希、字节/行数与结果计数，但严禁正文、日志行、payload 或 diff 片段。
- Agent 失败日志使用中文 WARN/ERROR，按 commandId/target 聚合，不为每次轮询刷屏；正常内容读取不打正文 INFO。
- 批量命令目标分页并冻结完整排序 hash；不得循环内查库/远程下发。worker 只批量建任务，实际 Agent IO 由既有异步通道执行。
- 内容、日志和 payload 沿既有大小/截断上限；超限不自动扩大，必须改为更小范围重新申请。

## 7. UX / 交互

- 用户任务：日志、受限目录、文件资产与消息 payload 可按 `management.read` 直接读取并追溯审计；写入型 Agent 命令、敏感配置明文、反向抓取/拓印等仍提交审批并在批准后查看结果。
- 进入路径：指定内容读取按钮走 direct 读取（敏感资产路径按既有规则要求原因），不创建审批；未降级操作仍走影响预览、审批中心与原领域 resultRef。
- 操作闭环：direct 读取为选择目标/范围 → 必要时填原因 → service 写元数据审计 → 返回本次内容；审批类操作仍为 human 比较快照并批准 → 后端自动入队命令或签发 grant → 页面轮询/一次性消费 → 查看安全审计；取消命令可立即止损。
- IA：命令状态仍在原领域页，统一决策在 `/approvals`；不新增通用命令终端或文件浏览器。

页面四态：

- **空态**：目标离线/无日志/无文件/无 payload 或无可执行命令时说明原因，不创建无效申请。
- **加载态**：目标事实、审批类任务和结果分别显示骨架；direct 内容读取只在用户触发后下发命令，不预取未请求正文。
- **错误态**：目标离线、执行失败、内容版本变化与审批类 grant 过期/已消费分别给出脱敏原因；direct 读取可重新发起，审批类操作须重新申请。
- **超大量态**：命令目标、目录项和历史服务端分页；日志/文件/payload 严格截断，批量目标由服务端完整 hash，浏览器不持有无界正文。
- **常规态**：direct 读取区分“命令已入队”和“Agent 已执行完成”；审批类 grant 仍提示 5 分钟/一次限制，消费后进入只读已消费态。

直读例外不改变其他命令和敏感配置的审批主流程。管理台应区分 `content.read` 直读与审批类操作；页面调整按当前 PRD/UX 评审门执行，直读成功仍须显示审计可追溯信息，审批类工具保留申请、批准、轮询与一次性消费状态。

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

1. 审批类 Agent command、敏感配置明文、反向抓取/拓印等 operation 均为 approval_required；指定 `content.read` 路由为 direct + `management.read`，新增未登记入口使覆盖测试失败。
2. 审批类操作批准前不创建命令、不唤醒 Agent、不解密/读取正文；API key/MCP/system 无法批准或构造 ExecutionPermit。`content.read` 直读允许具备 `management.read` 的机器主体，并且必须先写 service 审计。
3. human 批准后无需申请方第二次 start，命令/任务与 receipt 原子持久化；worker 崩溃/重试不重复下发。
4. approval succeeded 与领域命令终态严格区分，resultRef 可追踪 pending/running/succeeded/failed/cancelled。
5. SensitiveAccessGrant 只允许原申请主体、固定目标/版本、5 分钟内一次消费；过期、转让、重放、凭据吊销和版本漂移均失败关闭。
6. 审批类敏感读取每个用户动作恰有一份审批和至多一份 grant；命令、pending grant 与 receipt 原子创建，回传成功后才激活，worker 重试不重复下发。`content.read` 不创建审批/grant，但每次成功读取恰有一条先行的 service 审计。
7. approval、数据库普通字段、审计、日志、错误和指标中无日志正文、文件内容、配置明文、payload、secret；直读审计只留 serverId/path/messageId、哈希、大小/行数等元数据。
8. Agent 侧 plugins 根/path traversal/二进制/大小/脱敏/身份守卫全部保持；批准不能绕过任何既有保护。
9. 批量目标和查询分页有界，无循环 DB/远程调用、无无界内存加载；取消为 direct 止损，恢复/重试新建审批。

### 9.2 真实环境验收

1. 对真实 Agent 验证 resync、反向抓取/拓印等仍需审批；日志、受限目录读取经 API key 与人工主体 direct 成功，service 审计可追溯且不含正文。
2. 对真实文件资产 preview/diff 与消息 payload 验证 direct `management.read` 可用、正文仅在响应中出现、审计只留元数据；敏感配置明文仍走审批与原申请人 grant。
3. direct 读取时让 Agent 离线或改变清单版本，应返回脱敏失败且不返回正文；重新 direct 读取后可成功。
4. 取消运行中命令立即止损并强审计；重新开始必须新申请。
5. 五类四态 mockup 先经用户浏览器评审，接真后再检查数据库、日志和审计无敏感明文。

## 10. 固定边界

- 任何会改变状态的 Agent 命令以及未列入 ADR-0093 的危险操作都必须审批；命令名为“读取/同步/诊断”本身不能自动降级风险。
- ADR-0093 明确列出的实时脱敏日志、受限目录、文件资产 preview/diff 与消息 payload REST 读取为 `content.read + management.read + direct + 强审计`；敏感配置明文、反向抓取/拓印和其他敏感内容仍须审批。
- 批准即由 Beacon 自动入队命令或签发短期 grant；申请方不再调用 execute/start。
- approval 只保存脱敏意图和证据，敏感正文永不进入 approval/audit/log；一次性 grant 不能转让、恢复或扩权。
