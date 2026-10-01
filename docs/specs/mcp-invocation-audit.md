# 功能规格：MCP 工具调用流水落库与查询

> 状态：草拟　·　关联 PRD：FR-240（本文件同时承接 **FR-241** 页面所消费的查询面契约）　·　分支：待执行时创建　·　依赖：FR-236（工具风险分级真源）、FR-144（异步日表写入通道）、FR-151/152（日表归档）　·　接口对端：FR-242（生产模式执行面拒执，见 §3.6）

## 1. 背景与目标

MCP 执行面当前**没有任何一行「工具调用」级流水**。取证结论（读码确认，非推测）：

1. 协议入口只做转发：`MCPProtocolHandler.MCP()` 直接把请求交给 SDK transport（`apps/server/internal/server/mcp_protocol_handler.go:90-96`）；`MCPHandler.ServeHTTP` 只校验 bearer 并把主体注入上下文（`apps/server/internal/server/mcp_handler.go:79-103`）。整条链路上不存在任何按「调用」记录的位置。
2. 工具 handler 内部**有**领域审计（如 `config.publish` 建审批申请会落 `audit_log`），但那些记录回答的是「某个业务动作发生了什么」，**不带工具调用上下文**：没有工具名、没有风险等级、没有参数形态、没有耗时、也没有「被门禁拒绝因而什么都没发生」这一类事件——被拒调用在领域审计里是空洞。
3. 后果：无法回答运维最常问的三个问题——「这个 client 最近调了什么」「谁在反复撞 critical 工具」「生产模式拒执到底拒了多少次」。FR-241（管理台流水页）也因此没有数据源。

本 FR 补一层 **append-only 的工具调用流水**（每次 `tools/call` 一行），并提供分页筛选查询端点。定位是**执行面安全观测**，不是业务审计的替代：两者并存，互不重复（流水不含业务 detail，领域审计不含调用上下文）。

**目标**：每次 `tools/call` 可查、字段完整、参数正文不入库、落库不阻塞工具调用主路径。

## 2. 需求（要什么）

### 2.1 做什么

- 每次 `tools/call`（成功 / 失败 / 被拒）落一行流水，字段见 §3.2。
- 参数只落**脱敏摘要**（键名、目标标识、字节数），**绝不落正文**（§3.3）。
- 「被拒调用」纳入流水：生产模式执行面拒执（FR-242）、业务拒绝（既有 `mcpRejectedResult*`）、未发现工具的调用、handler 错误，全部留痕（§3.4、§3.6）。
- 提供分页筛选查询端点：按工具 / clientId / 结果 / 风险等级 / 原因 / 时间筛选（§3.7）。
- 落库走既有异步日表写入通道，**请求 goroutine 不碰 DB**（§3.5）。
- 保留与清理复用 FR-151 归档器，登记新归档域（§3.8）。

### 2.2 明确不做（范围外，逐条给理由）

1. **不存参数正文**：任何非白名单键的**值**、`content` / `files` / `payload` 类键的值、嵌套对象与数组的值，一律不入库（§3.3 三档规则）。
2. **不存结果正文**：`CallToolResult.Content` 的业务输出与 `StructuredContent` 不入库；失败摘要只取控制面自造的错误 / 拒绝文案（§3.4，仅限 `IsError` 时的首条文本块与 `GetError()` 消息）。
3. **不存 `_meta`**：客户端可塞任意内容的保留字段，整体丢弃。
4. **不做按工具的自定义脱敏规则**：统一三档规则（§3.3），不引入 per-tool 字段声明；新增工具不需要改脱敏代码（这也是 fail-closed 的：默认不取值）。
5. **不做实时推送 / SSE / 告警联动**：流水只落库 + 轮询查询。告警阈值与通知属 FR-231 域，本 FR 不建第二套告警通道。
6. **不做 `tools/list` / `initialize` / 其他 JSON-RPC 方法的留痕**：只记 `tools/call`（发现面变更由 FR-236/237 的目录真源回答，不需要流水）。
7. **不做按 namespace 的归属与过滤**：MCP 主体是全局机器身份（`MCPOAuthClient` 无 namespace 归属，`apps/server/internal/model/mcp_oauth.go:32-54`），流水行不可能有权威 namespace；因此查询端点**不接** `ObservationScope`（对比 `/admin/v2/connections` 与 `/admin/v1/audits` 都按调用者观测范围收窄）。
8. **不做「失败 / 被拒行同步直写」的第二条写入路径**：会破坏「请求 goroutine 不碰 DB」这一单不变量，且队列容量远高于 MCP 调用速率，丢弃概率可忽略（取舍见 §3.5、§6）。
9. **不做冷查询（`includeArchived`）**：列表只查热库；超保留期数据在归档库（§3.8），跨热冷并表留给后续 FR（§6 待拍板）。
10. **不做聚合 / 采样端点**：列表 + 详情足够；FR-241 的「危险操作视图」用 `riskLevel` 过滤即可，不需要新聚合。
11. **不做参数校验失败的独立分类**：SDK 对「参数未过 schema 校验」与「handler 返回错误」用**同一种** `SetError` 包装（`mcp/server.go:414` / `:432`，错误对象都是普通 `error`），没有可稳定依赖的类型差异；靠错误文本前缀（`validating "arguments"`）猜测太脆，故不做——`errorSummary` 保留 SDK 原文已足够诊断。
12. **不回填历史**：本 FR 上线前的调用无流水，且不做补偿（无从取证）。
13. **不改 78 处工具注册代码与 handler 签名**：单点 middleware 完成（§3.1）。
14. **查询行为自身不落审计**：与既有全部只读 GET 端点一致（`auditWriteMiddleware` 只兜底写方法，`apps/server/internal/server/router.go:196`）。

## 3. 设计（怎么做）

### 3.1 唯一挂载点：MCP receiving middleware

MCP SDK v1.8.0 提供 `Server.AddReceivingMiddleware`（`mcp/server.go:1860`，类型见 `mcp/shared.go:115-130`）。`handleReceive`（`shared.go:206-227`）先把 params 反序列化为该方法声明的参数类型（`tools/call` → `CallToolParamsRaw`，`Arguments` 仍是**线上原文** `json.RawMessage`），再交给 middleware 链——这正是「拿到调用、还没进领域 handler」的唯一收口点。

**取值事实（已核对 SDK 源码，实现不得假设）**：

| 需要的东西 | 在 middleware 里怎么取 |
|---|---|
| 工具名 | `req.(*mcp.CallToolRequest).Params.Name`（`CallToolRequest = ServerRequest[*CallToolParamsRaw]`，`mcp/requests.go:10`） |
| 参数原文 | 同上 `.Params.Arguments`（`json.RawMessage`，可空） |
| 结果 / 错误 | 返回值 `(Result, error)`；`res.(*mcp.CallToolResult).IsError` / `.GetError()` / `.NeedsInput()` —— `GetError()` 是区分「handler 主动拒绝」与「错误」的关键判据（§3.4） |
| 客户端主体 | `auth.FromContext(ctx)` → `principal.ID`（clientId）、`principal.Role`（profile） |
| traceId | `render.TraceID(ctx)`（HTTP 层 `traceMiddleware` 注入，`server/middleware.go:182-196`；SDK 沿 `req.Context()` 透传，`mcp/streamable.go:1245`） |
| 客户端地址 | 见下方「地址注入」 |

**挂载点（冻结）**：`MCPToolRegistry.NewMCPServer`（`mcp_tools.go:97`）在 `server := newEmptyMCPServer()` 之后立刻调用唯一挂载函数：

```go
// MCPInvocationRecorder 是工具调用流水的唯一写入出口（server 包持窄接口，实现由 service 层提供）。
type MCPInvocationRecorder interface { Record(model.MCPInvocation) }

// SetInvocationRecorder 装配流水写入方；装配期调用一次（与 SetConfigService 等其它 Set* 同区）。
func (r *MCPToolRegistry) SetInvocationRecorder(rec MCPInvocationRecorder)

// mcpAttachInvocationAudit 在 server 构造期挂载流水 middleware；rec 为 nil 时 no-op（既有单测零依赖）。
func mcpAttachInvocationAudit(server *mcp.Server, rec MCPInvocationRecorder)
```

- 挂载在**构造期**即足够：MCP server 是**每请求构造**的（`NewPublicMCPHandlerWithTools` 的 factory，`mcp_handler.go:36-49`），所以每个请求的 server 实例都带 middleware，不存在跨请求共享状态，也不需要运行期注册。
- **不去动 78 个工具的 `mcpAddTool` 调用点**：middleware 在方法层，天然覆盖全部工具、以及**未知工具**（`s.callTool` 对未注册工具返回 `unknown tool` 错误，同样经过 middleware）。
- 残余缺口（明示）：`newEmptyMCPServer()` 的另两处调用（`mcp_handler.go:39/43`：非法主体、`tools == nil`）不挂 middleware。这两条分支在现行装配下**不可达**（`MCPHandler.ServeHTTP` 的 verifier 只签发 MCP 主体；`main.go:439` 恒传 `mcpToolRegistry`）。若将来新增 server 构造路径，必须改走 `mcpAttachInvocationAudit`，不得各自实现。
- middleware 只读 `ctx` 与请求参数、只写队列，**不改请求、不改结果、不改 handler 语义**。
- `method != "tools/call"` 直接 `next`，零额外开销。

**地址注入（新增，小改）**：`MCPHandler.ServeHTTP`（`mcp_handler.go:102`）在注入主体时一并注入客户端地址，复用 token 端点的同口径解析 `trustedMCPClientIP(r)`（`mcp_protocol_handler.go:106`，取 `X-Forwarded-For` 首段）：

```go
// auth 包新增（与 WithPrincipal / FromContext 同处）：
func WithClientIP(ctx context.Context, ip string) context.Context
func ClientIPFromContext(ctx context.Context) string
```

地址是**观测字段而非安全边界**（受信反代注入；直连部署下可能为空，空串照记）。

### 3.2 数据模型：`mcp_invocation_YYYYMMDD`

新增 `apps/server/internal/model/mcp_invocation.go`：

```go
// MCPInvocation 是 MCP 工具调用日表 mcp_invocation_YYYYMMDD 的行模型（FR-240）。
// 全部基础类型，禁 JSON / ENUM 列与方言专有 SQL（守 DB 可移植，与 ConnDetail 同口径）；
// 枚举 result / reason / risk_level 落 VARCHAR + 应用层校验。
// 索引用 composite 空名式，让 GORM 按当日表名生成索引名（sqlite 索引名全库唯一，同字面名跨日冲突）。
type MCPInvocation struct {
	InvocationID string    `gorm:"column:invocation_id;size:36;primaryKey"`
	ClientID     string    `gorm:"column:client_id;size:96;not null;index:,composite:client_created,priority:1"`
	Profile      string    `gorm:"column:profile;size:32;not null"`
	ToolName     string    `gorm:"column:tool_name;size:128;not null;index:,composite:tool_created,priority:1"`
	RiskLevel    string    `gorm:"column:risk_level;size:16;not null;index:,composite:risk_created,priority:1"`
	Result       string    `gorm:"column:result;size:16;not null;index:,composite:result_created,priority:1"`
	Reason       string    `gorm:"column:reason;size:32;not null;default:''"`
	TargetDigest string    `gorm:"column:target_digest;size:255;not null;default:''"`
	ArgKeys      string    `gorm:"column:arg_keys;size:512;not null;default:''"`
	ArgBytes     int       `gorm:"column:arg_bytes;not null;default:0"`
	DurationMs   int       `gorm:"column:duration_ms;not null;default:0"`
	TraceID      string    `gorm:"column:trace_id;size:32;not null;default:''"`
	ClientIP     string    `gorm:"column:client_ip;size:45;not null;default:''"`
	ErrorSummary string    `gorm:"column:error_summary;size:255;not null;default:''"`
	CreatedAt    time.Time `gorm:"column:created_at;not null;index:,composite:client_created,priority:2;index:,composite:tool_created,priority:2;index:,composite:risk_created,priority:2;index:,composite:result_created,priority:2"`
}
```

> `CreatedAt` 同时充当四组复合索引的第 2 位（GORM 允许一条 tag 里写多个 `index:` 项）；索引名一律走 `,composite:<name>` 空名式让 GORM 按当日表名生成，禁止写死索引名（sqlite 索引名全库唯一，跨日建表会冲突）。

字段语义（**冻结**，实现逐项对齐）：

| 列 | 取值 | 来源 / 规则 |
|---|---|---|
| `invocation_id` | UUIDv7 文本（36 字符），主键 | 控制面生成；内嵌毫秒 = 完成时刻，**日表按它路由**（与 `conn_detail` 同手法，`store.TimeMsFromUUIDv7` 已有，新增 `store.NewUUIDv7(ms int64) string` 与之同文件，手写、不引第三方 uuid 依赖） |
| `client_id` | `MCPOAuthClient.ClientID` | **权威取自已认证主体** `principal.ID`（`mcp_oauth_service.go:115` 用 ClientID 构造主体），绝不取请求自报值 |
| `profile` | `observer` / `automation` | `principal.Role` |
| `tool_name` | 工具名原文（≤128，超出截断） | `req.Params.Name`；**未知工具也记原样**，故不做外键/白名单校验 |
| `risk_level` | `low` / `high` / `critical` / `unknown` | `mcpToolSpecByName(name).RiskLevel`；未登记目录 → `unknown`（不是空串，便于筛「撞目录的调用」） |
| `result` | `ok` / `fail` / `rejected` | §3.4 |
| `reason` | 受控枚举，成功为空串 | §3.4 |
| `target_digest` | `k1=v1;k2=v2`（≤3 项，≤255 字符） | §3.3 |
| `arg_keys` | 顶层参数键名清单（≤24 项，≤512 字符） | §3.3 |
| `arg_bytes` | `len(Arguments)`；缺省 0 | 参数原文**字节数**（不是字符数），用于发现「传了内容但被我们丢掉了」 |
| `duration_ms` | middleware 入口 → handler 返回的墙钟毫秒 | 不含响应序列化 / 网络 |
| `trace_id` | 16 位十六进制 | `render.TraceID(ctx)`，**与响应头 `X-Trace-Id` 同值**（一次 HTTP POST 对应一次 `tools/call`；契约不依赖该 1:1 假设，故不做唯一索引） |
| `client_ip` | IPv4 / IPv6 文本 | §3.1「地址注入」；可为空 |
| `error_summary` | ≤255 字符，已脱敏 | §3.4 |
| `created_at` | DATETIME(3)，UTC | 调用完成时刻；与 `invocation_id` 内嵌毫秒同源（同一 `time` 取整到毫秒） |

索引（冻结）：主键 + 4 组复合索引 `(client_id, created_at)`、`(tool_name, created_at)`、`(result, created_at)`、`(risk_level, created_at)`，一一对应 §3.7 的四个等值过滤维度（时间由日表切分天然收窄）。

模型不进全局 `AutoMigrate`（日表按 UTC 日期由 `store.EnsureDailyTable` 按需建），与 `ConnDetail` 一致。

### 3.3 参数脱敏摘要（冻结规则）

输入是 `Arguments` 原文（`json.RawMessage`）。**只解析顶层对象**；不是 JSON 对象 / 解析失败 → `target_digest=""`、`arg_keys=""`、`arg_bytes` 照记（原文长度）。**绝不因解析失败而回落存原文。**

**统一兜底**：任何要入库的文本先过 `redact.Desensitize`（FR-122 / ADR-0057）**再**按列宽截断——顺序不可颠倒，否则被截断的凭据片段可能漏码。

**三档规则**（判定顺序 A → B → C，先命中先归属）：

**A 档：目标标识白名单（唯一允许存值的一档）**

按**声明顺序**（不是参数出现顺序——保证输出可复现、可断言）取前 3 个「存在且为标量」的键，拼成 `键=值`，用 `;` 连接：

```
namespace, namespaceCode, namespaceId, fromNamespaceId, toNamespaceId,
serverId, serverRowId, id, targetId, parentId, targetKind, targetType,
identityId, orderId, requestId, grantId, messageId, zone, group,
path, targetRoot, version, scopeLevel, scopeTarget, batchNo, keepBootId
```

- 值规则：字符串去首尾空白后截断 96 字符（超长加 `…`）；数字 / 布尔取 JSON 紧凑形式；**数组 / 对象 / null 一律跳过**（不取值、不占 3 个名额）。
- 整体再截断到 255。
- 这些键都是标识类（id / 名字 / path），非凭据非正文；`path` 依 ADR-0057 属运维定位上下文、不打码（仍走 `redact.Desensitize` 兜底防 `?token=` 之类混入）。

**B 档：内容类键（只记「键名 + 字节数」）** —— 仅对**未命中 A 档**的键判定，键名小写后包含以下任一子串即命中：

```
content, payload, body, text, data, file, secret, token, password, passwd, pwd,
credential, api_key, apikey, comment, note, reason, command, key, name, value
```

在 `arg_keys` 中记作 `键名:字节数`（如 `content:1180`），字节数 = 该值 JSON 紧凑序列化后的长度。**值绝不入库。**

> 覆盖现状参数面：`content`（`beacon.files.create`）、`files[].Content`（`beacon.files.import`）、`reason` / `comment` / `note` / `value` / `reloadCommand` / `displayName` / `keyword` / `idempotencyKey` 等全部落此档——即「调用方确实传了正文，但我们只记长度」。

**C 档：其余键**——只在 `arg_keys` 中记裸键名。

**`arg_keys` 形状（冻结）**：全部顶层键（A/B/C 兼顾）按键名升序（Go 默认字符串序）拼接，`,` 分隔；最多 24 项；合计超 512 字符则从尾部截断并追加 `…`（保留前缀，保证头部项稳定可断言）。

**绝不入库清单（汇总）**：非 A 档键的值、嵌套对象/数组的值、`Content` 与 `StructuredContent`、`_meta` 全部、OAuth 令牌原文与 secret 原文、任何请求头原文。

### 3.4 结果与原因码（冻结枚举）

**判据建立在本包既有的结果约定之上，不新造一套**。工具 handler 现在有两种表达失败的方式：

1. **主动拒绝**（既有 `mcpRejectedResult` / `mcpRejectedResultWithReason`，`mcp_tools.go:930-952`，全仓 80+ 处调用）：返回 `*mcp.CallToolResult{IsError: true, Content: [文本]}`，**`error` 返回 nil**；语义是「已处理的业务拒绝，不把领域错误作为协议故障泄露给客户端」。
2. **错误**：返回非 nil error —— 普通 error 被 SDK 的 `toolForErr` 包成 `CallToolResult{IsError: true}` 并经 `SetError` **保留原始 error 对象**（参数校验失败 `mcp/server.go:414`、handler 返回错误 `mcp/server.go:432` 两处）；`*jsonrpc.Error` 原样上抛。

两者在 middleware 处**可靠可区分**：SDK 只在包装错误时调用 `SetError`，因此 `res.IsError == true && res.GetError() == nil` ⟺ handler 主动拒绝（本 FR 测试须锁定该判据）。

| `result` | 判定（源码级） | 典型 `reason` | `error_summary` |
|---|---|---|---|
| `ok` | `err == nil` 且 `!res.IsError` 且 `!res.NeedsInput()` | `""` | `""` |
| `rejected` | 工具名不在 `mcpToolCatalog`（含 SDK 以 error 上抛的 `unknown tool`） | `unknown_tool` | SDK 原文 |
| `rejected` | `err == nil` 且 `res.IsError` 且 `res.GetError() == nil`（handler 主动拒绝） | 见下表 | 拒绝文案（脱敏截断） |
| `fail` | `err != nil`（含 `*jsonrpc.Error`）或 `res.IsError && res.GetError() != nil` | `handler_error` | 错误消息（脱敏截断） |
| `fail` | `res.NeedsInput()`（多轮交互本轮未完成） | `input_required` | `""`（**防御分支**，现无工具使用 MRTR） |
| `fail` | middleware 自身 panic 恢复路径 | `internal_error` | 固定 `panic`（**不记 panic 值文本**，防携带数据） |

> 判定**自上而下取首个命中**：未知工具即便 SDK 以 error 形式返回，也优先记为 `unknown_tool`，不得落到 `handler_error`。

`reason` 取值与判定（**冻结**，列宽 32、应用层校验，新增 `model.IsValidMCPInvocationReason`）：

| 取值 | 判定 |
|---|---|
| `""` | `result=ok` |
| `unknown_tool` | 工具名不在 `mcpToolCatalog`（与 SDK 的 unknown tool 判定同源） |
| `production_mode` | `result=rejected` 且 `errorSummary` 以 `mcpProductionModeRejectedReason`（`mcp_tool_catalog.go:202`）为前缀 —— 即 **FR-242 的执行面拒执**；一致性兜底判据：`riskLevel=critical` 且 `auth.MCPProductionModeEnabled()` 为真（两者应恒等，测试双向断言） |
| `handler_rejected` | `result=rejected` 且不满足上一条：业务拒绝（参数缺失 / 目标不可用 / 观察范围无效 / 审批票据创建失败等） |
| `handler_error` | `result=fail` 且源于 `err` 或 SDK 包装错误 |
| `input_required` | `result=fail` 且 `NeedsInput()` |
| `internal_error` | `result=fail` 且为 middleware panic 恢复路径 |

`error_summary` 规则（冻结；这是**唯一允许触碰 `Content` 的位置**）：

- 优先级 ①：`res.GetError()` 的消息文本（错误路径）；②：`res.IsError` 时**首条** `TextContent` 的文本（拒绝路径，全仓拒绝文案都是控制面自造的统一文本 `请求被拒绝或目标不可用[：<原因>]`）；两者皆无 → `""`。
- **只读第一条文本块**：绝不读 `StructuredContent`（工具业务输出）、绝不读第二条及以后的内容项、非文本内容块跳过。
- `result=ok` 时**绝不读 `Content`**（那是业务正文，对应 §2.2 第 2 条）。
- 所有文本先过 `redact.Desensitize` 再截断 255。拒绝文案入库时去掉统一前缀「请求被拒绝或目标不可用：」，只留 `<原因>`（无原因则存前缀本身），便于按原因定位。
- panic 路径：middleware 用 `defer` 记录（panic 展开时照常执行），随后让 panic 继续上抛给 HTTP 层 `recoverMiddleware`（既有行为不变）。

### 3.5 写入路径与性能约束（不阻塞主路径）

复用 FR-144 的 `AsyncDailyWriter`（`service/async_daily_writer.go`）：请求侧只做**非阻塞投递**，DB IO 全在后台写入协程。

新增（service / repository 各一处）：

```go
// service：路由键与写入方（实现 server.MCPInvocationRecorder）
const RouteKindMCPInvocation = "mcp_invocation"

type MCPInvocationService struct { Writer *AsyncDailyWriter }

func (s *MCPInvocationService) Record(rec model.MCPInvocation) {
	if !EnqueueRows(s.Writer, RouteKindMCPInvocation, []model.MCPInvocation{rec}) {
		// 队列满：丢弃本条 + WARN（携带累计丢弃数），绝不阻塞、绝不让调用失败。
	}
}

// repository：MCPInvocationRepository.FlushDaily(rows []model.MCPInvocation) (int, error)
// 与 ConnDetailRepository.FlushDaily 同构：按 invocation_id 内嵌 UUIDv7 的 UTC 日分组 →
// 事务外 store.EnsureDailyTable 建各当日表 → 单事务内逐日 CreateInBatches（OnConflict DoNothing 幂等，重放安全）。
```

**装配（`cmd/beacon/main.go`）**，顺序有硬约束：

| 步骤 | 位置约束 |
|---|---|
| `mcpInvocationRepo := repository.NewMCPInvocationRepository(db)` | 任意 |
| `service.RegisterFlusher(asyncDailyWriter, service.RouteKindMCPInvocation, mcpInvocationRepo.FlushDaily)` | **必须早于 `asyncDailyWriter.Start(ctx)`**（`main.go:763`；既有约束见 `main.go:523` 注释与 `RegisterFlusher` 的 panic 守卫） |
| `mcpToolRegistry.SetInvocationRecorder(mcpInvocationService)` | 晚于注册表构造（`main.go:436`）、早于 HTTP 服务对外（与其它 `Set*` 同区，如 `SetReadServices` 附近） |

**性能与取舍（冻结）**：

- 请求路径新增开销只有「生成 UUIDv7 + 解析参数顶层键 + 打包一行 + 非阻塞入队」，纯内存、无锁等待、**无 DB IO**；量级微秒，相对工具本身的领域调用可忽略。
- 队列满（`EnqueueRows` 返回 false）→ **丢弃该行 + WARN 日志（含累计丢弃数）**，绝不阻塞、绝不改调用结果。MCP 调用是人与 Agent 触发的低频操作，而队列容量 4096 批（`defaultDailyWriteQueueCapacity`）+ 2 写入协程 + 200 行 / 500ms 攒批，实际丢弃概率可忽略。
- 刷盘失败容错沿用通道既有语义：WARN + 3 次退避重试，仍失败丢弃该 flush 并累计 `Discarded()`（INSERT 幂等，重放安全）。
- **进程退出取舍**：写入协程随 ctx 取消执行 `drainOnShutdown`，把缓冲与队列剩余批**尽力落盘一次**；因此
  - 优雅重启（SIGINT / SIGTERM）→ 已入队的流水基本不丢；
  - **SIGKILL / 崩溃**→ 未刷批次丢失（该次调用无行）；
  - 队列满丢弃 → 该次调用无行（有 WARN 与计数）。
  这三条是「落库不阻塞主路径」的**显式代价**，在本 FR 中被接受而非隐藏：不引入「失败/被拒同步直写」的第二条写入路径来回补（会破坏单不变量，并把 DB 抖动传导进工具调用）。
- 未知工具名、超长参数等异常输入不改变上述路径：截断在 middleware 内完成，投递仍是定长一行。

### 3.6 与 FR-242（生产模式执行面拒执）的接口

FR-242 的执行面拒执**已在工作区落地**（`mcp_tool_catalog.go:194-306`）：`mcpAddTool` 给每个工具 handler 包一层 `mcpGuardToolExecution`，命中即**原 handler 完全不执行**（零副作用）并返回 `mcpToolErrorWithReason(mcpProductionModeRejectedReason + "：" + name)`，`error` 为 nil。它已明确把**库表意义上的流水交给本 FR**（其注释：*「在库表意义上的『流水』留给 FR-240 的 tools/call 流水（拒执即 result=rejected 的一行），本层先在唯一收口处留下可观测痕迹」*）。

因此本 FR 与它的接口是**单向、零冲突**的，不需要它改任何返回方式：

1. **分类判据（冻结）**：拒绝结果满足 §3.4 的 `err == nil && res.IsError && res.GetError() == nil`，天然落 `result=rejected`；`reason` 由 `errorSummary` 是否以 `mcpProductionModeRejectedReason` 开头判定为 `production_mode`，其余拒绝为 `handler_rejected`。辅以一致性兜底判据（`riskLevel=critical` 且 `auth.MCPProductionModeEnabled()`），两条路径应给出相同结论——测试须**双向断言**：不得出现「文本命中却非 critical」或「critical+开关开却记 handler_rejected」。
2. **门禁先于业务逻辑**（本 FR 侧的可测不变量）：因为 guard 位于 `mcpAddTool` 的包装层，生产模式下 `critical` 工具的调用**不可能**落到业务逻辑，故其流水**不得出现** `reason=handler_rejected`；出现即说明 guard 被绕过（例如有人绕过 `mcpAddTool` 直接 `mcp.AddTool` 注册）。
3. **不重复落库**：FR-242 侧的进程内痕迹（`mcpRejectionTrace`，环形上限 64 条）与 `slog.Warn` 只是运行期可观测性，**不是**持久流水；本 FR 不消费它、也不要求它扩容（库表流水是唯一持久真源）。
4. **未登记工具的边界**：未登记工具在 `mcpAddTool` 处即 fail-closed 不注册，调用落到 SDK 的 `unknown tool`，故生产模式下也记 `unknown_tool`（不是 `production_mode`）——两 FR 在此不冲突（guard 的未登记分支是防御性的）。
5. **降级语义（兜底）**：若将来 FR-242 改用返回 error 的方式，该次调用**仍会被记录**（`result=fail` / `reason=handler_error`），**不漏行、只降分类精度**；届时以本文档为准对齐。

### 3.7 查询端点

沿用 `/admin/v2` 组的既有鉴权与中间件（`adminAuthMiddleware` + `readonlyWriteGuard` + `auditWriteMiddleware`）：登录令牌 / API 密钥皆可，`readonly` 角色可读（GET），不新增能力点。

**列表**

```
GET /admin/v2/mcp/invocations
```

| 参数 | 类型 / 取值 | 默认 | 语义 |
|---|---|---|---|
| `tool` | string（工具名精确匹配） | 空=全部 | 支持未登记工具名（查 `unknown_tool` 撞库） |
| `clientId` | string（精确匹配） | 空=全部 | |
| `result` | `ok` / `fail` / `rejected` | 空=全部 | 非法值 → 400 `INVALID_PARAM` |
| `riskLevel` | `low` / `high` / `critical` / `unknown` | 空=全部 | 非法值 → 400 `INVALID_PARAM` |
| `reason` | string（§3.4 枚举精确匹配） | 空=全部 | 非法值 → 400 `INVALID_PARAM` |
| `from` / `to` | RFC3339Nano（`parseISOms` 同口径） | 空=不限 | 按 `created_at` 闭区间；`from > to` → 400 |
| `limit` | int | 20 | 1..100；`≤0` 取默认、`>100` 取 100（服务端规整，与既有列表端点同口径） |
| `cursor` | string（不透明） | 空=首页 | 跨日表 keyset 游标，与 `/admin/v2/connections` 同口径 |

响应：

```json
{ "items": [ { "invocationId": "...", "clientId": "...", "profile": "automation",
               "toolName": "beacon.approvals.approve", "riskLevel": "critical",
               "result": "rejected", "reason": "production_mode",
               "targetDigest": "requestId=apr-8f31", "argKeys": "idempotencyKey:36,reason:12,requestId:9",
               "argBytes": 130, "durationMs": 1, "traceId": "9f2c…", "clientIp": "10.0.0.7",
               "errorSummary": "生产模式已禁用 critical 风险等级工具：beacon.approvals.approve",
               "createdAt": "2026-07-11T04:05:06.789Z" } ],
  "nextCursor": "" }
```

- item 键为**小驼峰**，字段与 §3.2 一一对应；空串字段原样返回 `""`（不引入 null 分支）；`createdAt` 为 RFC3339（毫秒精度，UTC）。
- **不返回 `total`**：跨日表精确总数需要全扫，与 `/admin/v2/connections` 同口径改为游标分页（`nextCursor` 空串=末页）。
- 只查**保留期内已存在日表**，查询侧只判存不建表（`existingDailyTablesInRange`，`repository/daily_query.go:31`）。

**详情**（FR-241 行点击用）

```
GET /admin/v2/mcp/invocations/{invocationId}
```

- 由 `invocationId` 的 UUIDv7 内嵌毫秒**直定日表**、按主键查单行（与 `ConnDetailRepository.FindByConnID` 同手法，`repository/conn_detail_repo.go:283`）：O(1)、不需要时间参数。
- 命中返回与列表项相同的形状；未命中 / 非法 ID（非 UUIDv7 文本、超 36 字符）**一律 404** `mcp_invocation_not_found`（新增 apperr），不区分「非法」与「不存在」（防探测）。
- 已归档日（超出保留期）→ 404（冷查询不在本 FR，§2.2 第 9 条）。

### 3.8 保留与清理（复用 FR-151 归档范式）

- **登记为归档域**：`archive_domains.go` 的 `archiveDomains` 增一项——`name: "mcp_invocation"`、`baseTable: "mcp_invocation"`、`form: archiveFormDaily`、`pkColumn: "invocation_id"`、`pkKind: archivePKString`、`retentionKey: SettingArchiveRetentionMCPInvocation`、`newModel: func() any { return &model.MCPInvocation{} }`。
- **保留期设置键**：`archive.retention-days.mcp-invocation`，默认 **180 天**（与 `audit` 同档：MCP 调用是低频机器操作流水，取证窗口应与审计一致），沿用既有下限 7 天（`archiveMinRetentionDays = 7`，`settings_metadata.go:76`）与上限 3650 守卫。`settings_metadata.go` 需四处同步（缺一即 fail-open）：
  1. 常量 `SettingArchiveRetentionMCPInvocation = "archive.retention-days.mcp-invocation"`（与其它保留期键同处，`settings_metadata.go:44-50`）；
  2. 默认值常量 `archiveDefaultRetentionMCPInvocation = 180`（`settings_metadata.go:59-77` 区块，纯设置 store 项、无 config.yml 对应项）；
  3. 元数据条目：`valueType: model.SettingValueTypeInt`、`min: archiveMinRetentionDays`、`max: 3650`、`defaultFromConfig` 返回上述默认值；
  4. **加入 `dangerousSettingKeys`**（`settings_metadata.go:334-342`）——该集合是高影响设置审批链路的唯一判据（`settings_service.go:71/174/219`、`settings_handler.go:49`）。漏登记会让保留期改动**绕过**危险设置审批流程直接生效，属安全回归，必须由测试锁定。
- **清理方式**：完全复用 FR-151 归档器——按表名前缀 + 8 位日期后缀枚举到期日表（`expiredDailyTables`）→ 整表搬运到归档库（幂等 `OnConflict DoNothing`）→ 行数 + 抽样哈希校验 → 校验通过才 `DROP TABLE`；校验不通过**绝不删热库**。`mcp_invocation` 是 `daily` 形态，天然无半表状态，无需新机制。
- **同步更新属主规格**：`docs/specs/v2-hot-cold-archive.md` §3.1 域注册表增一行（属主规格 = 本文件），§3.3 设置键表增 `archive.retention-days.mcp-invocation`。
- 归档任务自身仍落 `archive_job` / `archive_job_item`（控制面事实），不在本 FR 改动。

### 3.9 契约与 mock（前端前置）

| 层 | 文件 | 改动 |
|---|---|---|
| 契约真源 | `packages/contracts/src/mcp.ts` | 增 `MCPInvocationItem`、`MCPInvocationListResponse`（含 `nextCursor`）、`MCPInvocationResult` / `MCPInvocationRiskLevel` 联合类型 |
| mock | `packages/devmock/src/domains/mcp.ts` | 增 `GET /admin/v2/mcp/invocations` 与详情 handler（覆盖空 / 常规 / 超大量 / 异常四态数据） |
| 前端页面 | `apps/web` | **本 FR 不改**（FR-241 承接） |

## 4. 任务拆分

- [ ] `model/mcp_invocation.go`：行模型 + `result`/`reason`/`risk_level` 枚举常量与校验函数
- [ ] `store/uuidv7.go`：新增 `NewUUIDv7(ms int64) string`（与既有解析函数配套，补单测）
- [ ] `server/mcp_invocation_audit.go`：`MCPInvocationRecorder` 接口、middleware（计时 / 分档脱敏 / 结果分类含 §3.4 判据 / defer 记录）、`mcpAttachInvocationAudit`
- [ ] `server/mcp_tools.go`：`SetInvocationRecorder` + `NewMCPServer` 挂载
- [ ] `server/mcp_handler.go` + `auth` 包：客户端地址注入（§3.1）
- [ ] `service/mcp_invocation_service.go`：`RouteKindMCPInvocation` + `Record`（非阻塞投递 + 丢弃计数与 WARN）
- [ ] `repository/mcp_invocation_repo.go`：`FlushDaily`（跨日拆分 + 幂等批插）
- [ ] `handler/mcp_invocation_handler.go` + `router.go`：列表与详情两个 GET 端点
- [ ] 归档域登记：`service/archive_domains.go`、`service/settings_metadata.go`（四项：键常量、默认值、元数据条目、`dangerousSettingKeys`）
- [ ] `cmd/beacon/main.go` 装配（三处，顺序见 §3.5）
- [ ] `packages/contracts` + `packages/devmock` 同步
- [ ] 测试（§5 各项）
- [ ] 文档同步：PRD 状态、ARCHITECTURE（MCP 段补流水一句话）、CHANGELOG、`docs/specs/v2-hot-cold-archive.md` 域注册表

## 5. 验收标准

全部可证伪，逐条对应测试：

1. **每次调用留痕**：in-memory MCP 会话（`mcp.NewInMemoryTransports()`，与 FR-236 覆盖测试同手法）调用一个只读工具 → 流水恰 1 行；`clientId` / `profile` / `toolName` / `riskLevel` / `result=ok` / `durationMs` / `traceId` / `createdAt` 全非空，且 clientId/profile 与主体一致、riskLevel 与 `mcpToolCatalog` 一致。
2. **参数正文不入库**：构造带 canary 的调用（`beacon.files.create` 的 `content` 与 `reason` 塞 `SECRET-CANARY-<随机>`）→ 扫描当日日表**所有文本列**不得出现该 canary；同时 `argKeys` 含 `content:<字节数>` 形式、`argBytes` 等于原文长度、`targetDigest` 含 `path=`。
3. **未知工具**：调用未登记工具名 → 留痕且 `result=rejected` / `reason=unknown_tool` / `riskLevel=unknown`。
4. **业务拒绝**：工具 handler 走既有 `mcpRejectedResultWithReason("<原因>")`（`err` 为 nil）→ `result=rejected` / `reason=handler_rejected`，且 `errorSummary` 含该 `<原因>`（已脱敏）。
5. **失败与拒执分类（与 FR-242 的已落地实现对齐）**：
   - handler 返回普通 error → `result=fail` / `reason=handler_error` / `errorSummary` 非空且已脱敏（SDK 包装路径经 `GetError()` 也可判出，不漏行）；
   - 业务拒绝（既有 `mcpRejectedResult*`）→ `result=rejected` / `reason=handler_rejected`，`errorSummary` 含其 `<原因>`；
   - 生产模式开启时调用 `critical` 工具（经 `mcpGuardToolExecution` 拒执）→ `result=rejected` / `reason=production_mode`，`errorSummary` 含 `mcpProductionModeRejectedReason`；同一工具在生产模式关闭时 → `handler_rejected` 或成功（证明判据确实由开关驱动）；
   - 生产模式开启时，`critical` 工具的任何调用都不得出现 `reason=handler_rejected`（= guard 先于业务逻辑，§3.6 可测不变量）。
6. **查询闭环**：按 `tool` / `clientId` / `result` / `riskLevel` / `reason` / `from`+`to` 逐个过滤，结果集与预置行精确一致；`limit` 越小越可续（`nextCursor` 非空 → 第二页无重复无遗漏）；非法枚举值返回 400；`from > to` 返回 400。
7. **跨日可见**：构造昨日与今日各一行 → 不带时间窗的列表两行都在；带昨日时间窗只返回昨日行。
8. **详情直查**：按 `invocationId` 命中返回同行；乱造 ID / 已删除日表 → 404，且响应不含任何参数正文。
9. **不阻塞**：写入通道未启动（路由未注册）与队列塞满两种构造下，工具调用仍正常返回（结果不变）、耗时增量 < 10ms，且不返回错误（只有 WARN 日志 / 丢弃计数）。
10. **保留与清理**：`archive.retention-days.mcp-invocation` 默认 180、低于 7 被拒（既有守卫）；`service.SettingDangerous("archive.retention-days.mcp-invocation")` 为真（改动走危险设置审批链路，防漏登记）；构造到期 `mcp_invocation_YYYYMMDD` → 归档 dry-run 能枚举到该表，execute 后校验通过才删热库；校验失败保留热库。
11. **既有测试全绿**：尤其 FR-236/237 的 catalog / 生产模式覆盖测试、`mcp_stateless_test.go`、`mcp_handler_test.go` 不受 middleware 影响（recorder 为 nil 时 no-op）。

## 6. 风险 / 待定

- **待拍板 1｜冷查询是否纳入本 FR**：本 spec 冻结为「只查热库 + 归档库不参与查询」。若 FR-241 页面需要「查一年前的调用」，必须在本 FR 内增 `includeArchived`（复用 `daily_query.go` 的 keyset 归并原语），或另立 FR。**默认建议：本 FR 不做**（与 FR-240 验收范围「可按…筛选并分页」不冲突）。
- **与 FR-242 的接口已对齐（非待拍板，供复核）**：FR-242 的 guard 已落地于 `mcpAddTool` 包装层，拒绝走既有结果约定（`err` 为 nil），并把库表流水明确让给本 FR（§3.6）。因此两 FR 之间**不需要任何额外改动**；唯一需要守的是「guard 不得被绕过」（即不得有人绕过 `mcpAddTool` 直接 `mcp.AddTool`）——已写成 §5 第 5 条的可测断言。
- **待拍板 2｜`reason=production_mode` 的判据口径**：本 spec 冻结为「以 `mcpProductionModeRejectedReason` 文本前缀为主判据 + `critical`/开关一致性兜底」。若实现时希望省掉文本匹配（改为纯结构判据），需确认「生产模式下 critical 工具的业务逻辑不可达」这一前提长期成立（当前由 `mcpGuardToolExecution` 位于 `mcpAddTool` 包装层保证）。
- **明确接受的取舍｜崩溃/丢弃窗口**：SIGKILL、崩溃、队列满三种情况下对应调用无流水（§3.5）。这是「不阻塞主路径」的直接代价；若未来要求「critical 调用必留痕」，需引入同步兜底通道并重新评估对工具调用延迟的影响（本 FR 明确不做，见 §2.2 第 8 条）。
- **量级估算（供评审）**：单行含索引约数百字节；即使按每天 1 万次调用估算，也只有数 MB/日、180 天保留期下为 GB 以内，与既有采集域相比可忽略。此为估算，非实测；实现后可用归档 overview 的 `rows_expected` 反查真实增速。
- **middleware 是新增执行面代码**：它位于所有工具调用的公共路径上，任何 panic 或阻塞都会影响全部 78 个工具。测试必须覆盖「recorder 抛错/阻塞不传导给调用方」（§5 第 9 条），且 middleware 内部不得启动 goroutine 或加锁。
- **`riskLevel=unknown` 的语义边界**：未知工具记 `unknown`，与目录内的三档并列。若将来对未登记工具调用做告警，需注意「客户端缓存旧清单」与「恶意探测」在流水里**无法区分**（都表现为 `rejected` / `unknown_tool`），只能靠 `clientId` 频次人工判断。
- **索引成本**：日表 4 组复合索引使写入放大。MCP 调用量低，可接受；若线上观测到写放大问题，可先砍 `(result, created_at)`（结果维度可用更粗的时间窗过滤兜住）。
