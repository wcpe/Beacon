# 功能规格：MCP 工具风险分级真源

> 状态：开发中（真机验收通过，待发版）　·　关联 PRD：FR-236　·　分支：feature/mcp-tool-risk-grading

## 1. 背景与目标

MCP 面当前有三个结构性问题：

1. **工具目录与风险等级脱节**：`MCPToolNames(profile)` 是纯字符串清单（78 项），而风险等级散落在各 service 的 `authz.OperationDescriptor` 中，其键是 **operation kind**（如 `config.publish`）而非 MCP 工具名。两者靠「工具名去前缀 ≈ kind」这一**未声明的隐式约定**耦合——该约定对 62 个工具不成立（`beacon.config.batch.delete` → `config.batch_delete`、`beacon.files.*` → `file.*`、`beacon.assets.preview.request` → `agent.command.fs_browse` 等），对只读工具、`approvals.*`、建树工具则完全不适用。
2. **注册侧无守护**：78 个工具由 19 个 `register*` 函数注册，其中 62 项经 helper 以字符串参数注册（静态扫描会漏 15 个）。`MCPToolNames` 注释声称「与实际注册行为一致」，但**没有任何测试验证**。
3. **无法按环境收敛**：缺少工具级风险真源，FR-237 的生产模式门禁无从下手。

本 FR 建立**工具级风险分级目录（catalog）**，作为 MCP 工具发现与门禁的唯一真源。

## 2. 需求（要什么）

- 建立 `mcpToolCatalog`：以工具名为唯一键，登记风险等级、可见 profile、与既有 operation kind 的映射。
- `MCPToolNames(profile)` 改为**从 catalog 派生**，删除现有字符串清单与条件分支。
- **风险等级以既有 `OperationDescriptor.RiskLevel` 为基线**，不另立等级体系：
  - 对有 operation kind 的工具：catalog 等级**不得低于**其 descriptor 等级（测试断言此不变量）。
  - 其中「不可逆」或「影响控制面自身」的工具，在 MCP 面**提级为 `critical`**（逐项理由见 §3.3）。
  - 对无 kind 的 28 个工具：显式定级（只读/自查/建树为 `low`，敏感内容消费为 `high`）。
- **不改动既有 `OperationDescriptor`**：本 FR 不修改任何 descriptor 的 RiskLevel，避免影响既有审批中心语义与回归面。
- **覆盖测试 fail-closed**：新增工具未登记 catalog 时测试失败；catalog 与真实注册集合不一致时测试失败。

**范围内**：工具级风险目录、`MCPToolNames` 重构、注册门禁 helper、覆盖测试。

**不做（范围外）**：生产模式门禁（FR-237 独立交付）；修改既有 approval descriptor；MCP 协议 / OAuth 层；管理台 UI。

## 3. 设计（怎么做）

### 3.1 新增 `apps/server/internal/server/mcp_tool_catalog.go`

```go
// MCP 工具风险等级：复用既有 RiskLevel 取值，不引入新档位。
const (
	MCPRiskLow      = "low"
	MCPRiskHigh     = "high"
	MCPRiskCritical = "critical"
)

// mcpToolSpec 是 MCP 工具的唯一登记项。
type mcpToolSpec struct {
	Name      string // 工具名，唯一键
	RiskLevel string // MCPRiskLow / MCPRiskHigh / MCPRiskCritical
	// AutomationOnly 为 false 时 observer 与 automation 均可见；true 则仅 automation 可见。
	AutomationOnly bool
	// RequireApprovalDecide 为 true 时，仅当 mcp.allow-approval-decide 开启才可发现。
	RequireApprovalDecide bool
	// OperationKind 是对应既有 authz.Operation.Kind；空串表示该工具不经审批
	// （只读 / 审批自查 / 建树 / 消费类），也即无 descriptor 可对齐。
	OperationKind string
}

var mcpToolCatalog = []mcpToolSpec{ /* 78 项，见 §3.3 */ }
```

`OperationKind` 字段把「工具 ↔ 既有真源」的对应关系**显式化**，取代隐式字符串约定；它同时是 §3.4 不变量断言的桥梁。

### 3.2 派生与门禁

```go
// mcpToolSpecByName 按工具名取登记项；未登记返回 false（fail-closed 的基础）。
func mcpToolSpecByName(name string) (mcpToolSpec, bool)

// mcpToolDiscoverable 是本包唯一的可发现性判定入口，
// 供 MCPToolNames（测试/文档侧）与 mcpAddTool（运行时注册侧）共用，保证两侧永不漂移。
func mcpToolDiscoverable(name string) bool

// MCPToolNames 从 catalog 派生 profile 可发现的工具名（保序，便于测试稳定比对）。
func MCPToolNames(profile string) []string
```

`mcpToolDiscoverable` 在 FR-236 阶段只包含 `RequireApprovalDecide` 判定；FR-237 将在此叠加生产模式过滤（单点扩展，测试与运行时同时生效）。

### 3.3 风险等级分配（全部 78 项）

**`critical`（10 项，均为「不可逆」或「影响控制面自身」）**

| 工具 | 对应 kind | descriptor | 提级理由 |
|---|---|---|---|
| `beacon.approvals.approve` | — | 无 | 机器自批能放行任意高危操作，影响面等同被批操作本身 |
| `beacon.approvals.reject` | — | 无 | 同上 |
| `beacon.credentials.api-key.create` | `credential.create` | high | 签发长期管理凭据 |
| `beacon.credentials.api-key.rotate` | `credential.rotate` | high | 轮换管理凭据 |
| `beacon.lifecycle.namespace.permanent-delete` | `namespace.permanent_delete` | high | 墓碑化，不可逆 |
| `beacon.lifecycle.server.permanent-delete` | `server.permanent_delete` | high | 墓碑化，不可逆 |
| `beacon.system.update.apply` | `system.update.apply` | high | 控制面自更新，失败即控制面不可用 |
| `beacon.system.update.rollback` | `system.update.rollback` | high | 控制面回滚 |
| `beacon.system.settings.update-dangerous` | `settings.update.dangerous` | high | 高影响系统设置 |
| `beacon.delivery.order.rollback` | `delivery.rollback` | **critical** | 既有 descriptor 已为 critical，无需提级 |

> 前 9 项在既有 descriptor 上为 `high`，本 FR 在 **MCP 面提级**为 `critical`，不改动 descriptor。依据：MCP 调用方是机器主体，其可发现面应**严于**人类管理台（管理台仍可正常发起这些操作的审批申请）。

**`high`（44 项）**：其余全部申请类与敏感读消费类工具，等级继承既有 descriptor（`config.*` 8、`file.*` 8、`override-set.*` 3、`identity.*` 5、`namespace_trust.grant` 1、`topology` 审批 5、`lifecycle` archive/restore 4、`agent.server.resync` 1、`delivery` 其余 5、`assets.preview.*` 2、`messages.payload.*` 2）。

**`low`（24 项）**：

- 12 个只读工具（`mcpReadToolNames`）：直查服务，无副作用。
- 2 个审批自查：`beacon.approvals.own.list` / `.get`（仅读自己的申请）。
- 1 个自查变更：`beacon.approvals.own.withdraw`（仅撤回自己的 pending 申请）。
- 9 个建树工具（FR-221）：低风险结构操作，FR-221 已明确直执且 service 层有非空拒绝保护。

> **合计校验**：critical 10 + high 44 + low 24 = 78。

**关于 `assets.preview.consume` / `messages.payload.consume`**：不创建审批、只消费既有 grant，故无 descriptor。定为 `high`——它们读取敏感内容，虽受上游 grant 约束，但不应与只读工具同档。

### 3.4 注册门禁与一致性测试

新增泛型 helper 取代直接调用 SDK：

```go
func mcpAddTool[In, Out any](server *mcp.Server, tool *mcp.Tool, handler mcp.ToolHandlerFor[In, Out]) {
	if !mcpToolDiscoverable(tool.Name) {
		return
	}
	mcp.AddTool(server, tool, handler)
}
```

69 处 `mcp.AddTool(` 机械替换为 `mcpAddTool(`（`mcp_tools.go` 48、`mcp_read_tools.go` 12、`mcp_topology_authoring.go` 9）。SDK 的 `AddTool` 是泛型自由函数（`func AddTool[In, Out any](s *Server, t *Tool, h ToolHandlerFor[In, Out])`），签名兼容，替换不改变任何 handler 语义。

> **69 处调用点覆盖全部 78 个工具**：差异来自 helper 函数被多次调用——如 `registerLifecycleTool` 单处 `AddTool` 服务 6 个工具名、`registerConfigBatchTool` / `registerFileBatchTool` / `registerIdentityTransitionTool` 等同理（6 个 helper 共覆盖 15 个工具）。这些 helper 的 `Name` 来自字符串参数而非字面量，因此**基于 `Name:` 字面量的静态扫描会漏掉它们**——这是必须走运行时枚举（§3.4 第 3 点）而非静态分析的原因。

**一致性测试**（新增 `mcp_tool_catalog_test.go`）：

1. **登记完整性**：catalog 无重复名、无空等级、工具名后缀合法。
2. **descriptor 不变量**：对每个 `OperationKind` 非空的工具，断言 `catalog.RiskLevel >= descriptor.RiskLevel`（序：low < high < critical）。

   **实现方式（已取证）**：`MCPToolRegistry` 不持有 `authz.ApprovalRegistry`（其字段全部是 service 指针），server 包测试无法直接读 descriptor。故测试需**自行构造** `authz.NewApprovalRegistry()`，调用 13 个导出的 adapter 注册函数填充它（`RegisterConfigApprovalAdapters`、`RegisterFileOverrideApprovalAdapters`、`RegisterV2ControlPlaneApprovalAdapters`、`RegisterSystemOperationApprovalAdapters`、`RegisterAPIKeyApprovalAdapters`、`RegisterDeliveryApprovalAdapter`、`RegisterAgentCommandApprovalAdapters`、`RegisterAgentLogApprovalAdapter`、`RegisterAssetPreviewApprovalAdapter`、`RegisterMessagePayloadApprovalAdapter`、`RegisterSensitiveConfigApprovalAdapter`、`RegisterReverseFetchTaskApprovalAdapters`、`RegisterMCPOAuthApprovalAdapters`），再用导出的 `registry.Descriptor(kind)` 读取比对。

   **降级路径**：若某注册函数无法以零值依赖调用（如 `RegisterSystemOperationApprovalAdapters` 需 `*gorm.DB`、`RegisterMessagePayloadApprovalAdapter` 需 `*repository.MessageRepository`，若其内部访问依赖字段会 panic），则该函数覆盖的 kind 退出自动断言，改为 catalog 显式记录 descriptor 等级 + 测试内豁免清单（附原因与人工核对结论），**不阻塞其余 kind**。
3. **注册一致性**：用 `mcp.NewInMemoryTransports()` 构造真实 `NewMCPServer(principal)` 会话，经 `cs.Tools(ctx, nil)` **迭代器**枚举已注册工具名（服务端 `PageSize: 100`，automation 目录超 100，必须用迭代器翻页），断言「枚举集合 == catalog 派生集合」双向成立。

   测试构造的 registry 必须把全部可选服务字段填为非 nil（零值 `&service.X{}` 即可——注册只生成闭包、不触达内部），否则 20 余处 nil 守卫会使断言误报。

## 4. 任务拆分

- [ ] 新建 `mcp_tool_catalog.go`（常量 + `mcpToolSpec` + 78 项目录 + 查表/判定/派生函数）
- [ ] 重构 `MCPToolNames` 从 catalog 派生
- [ ] 新增 `mcpAddTool` 并替换 69 处 `mcp.AddTool` 调用
- [ ] 新增 `mcp_tool_catalog_test.go`（登记完整性 + descriptor 不变量 + in-memory 一致性）
- [ ] 适配既有 `mcp_tools_test.go` / `mcp_read_tools_test.go`（如有断言需随派生逻辑调整）
- [ ] 文档同步：PRD 状态、ARCHITECTURE、CHANGELOG

## 5. 验收标准

- catalog 覆盖全部 78 个工具，无遗漏、无多余；新增工具未登记 catalog 时，覆盖测试失败（fail-closed）。
- in-memory 枚举集合与 catalog 派生集合**双向一致**。
- 每个有 operation kind 的工具，catalog 等级 **不低于** descriptor 等级。
- `MCPToolNames` 对外行为不变：observer 14 项、automation 76 / 78 项（随 `allow-approval-decide` 两态：关闭 76、开启 78）。
- 既有测试全绿。

## 6. 风险 / 待定

- **待拍板**：§3.3 中 9 项提级为 `critical` 的清单（含 `approve` / `reject` 两项无 kind 工具）。这决定 FR-237 生产模式实际隐藏的范围。
- **测试装配依赖**：注册路径有 20 余处 nil 守卫，双向断言要求测试把可选服务填满。若某服务无法零值构造，该组工具降级为单向包含断言并在测试中注明原因。
- **catalog 与注册的长期一致性**：靠 §3.4 的 in-memory 测试守护；若未来引入动态注册（如按租户裁剪），需重新评估该断言形式。
