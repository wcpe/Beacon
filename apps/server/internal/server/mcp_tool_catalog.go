package server

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
)

// MCP 工具风险等级：复用既有 authz.OperationDescriptor.RiskLevel 的取值体系
// （low / high / critical），不引入新档位（FR-236）。
const (
	// MCPRiskLow 表示只读、无副作用，或仅操作申请者自身审批的工具。
	MCPRiskLow = "low"
	// MCPRiskHigh 表示会改变生产状态、但可通过后续操作回滚的工具；
	// 本档工具多为「只创建审批申请」语义，本身不产生业务副作用。
	MCPRiskHigh = "high"
	// MCPRiskCritical 表示不可逆、影响控制面自身，或可造成权限提升的工具。
	// 该等级是生产模式门禁（FR-237）的隐藏对象。
	MCPRiskCritical = "critical"
)

// mcpToolSpec 是 MCP 工具的唯一登记项（FR-236）。
//
// 本目录是工具发现与门禁的单一真源：MCPToolNames（测试与文档侧）与
// mcpAddTool（运行时注册侧）都从它派生，两侧不会漂移。
type mcpToolSpec struct {
	// Name 是工具名，也是本目录的唯一键。
	Name string
	// RiskLevel 取 MCPRiskLow / MCPRiskHigh / MCPRiskCritical。
	RiskLevel string
	// AutomationOnly 为 true 时仅 automation profile 可发现。
	AutomationOnly bool
	// RequireApprovalDecide 为 true 时，仅当 mcp.allow-approval-decide 开启才可发现。
	RequireApprovalDecide bool
	// OperationKind 是对应既有 authz.Operation.Kind；空串表示该工具无审批语义
	// （只读 / 审批自查 / 建树 / 消费类），也即无 descriptor 可对齐。
	OperationKind string
}

// mcpToolCatalog 登记全部 MCP 工具，切片顺序即 MCPToolNames 的返回顺序。
//
// 维护要求：新增 MCP 工具时必须在此登记，否则覆盖测试失败（fail-closed）。
// 风险等级的定级依据见 docs/specs/mcp-tool-risk-grading.md §3.3。
var mcpToolCatalog = []mcpToolSpec{
	// ── 只读查询：observer 与 automation 共用 ──
	{Name: "beacon.approvals.own.list", RiskLevel: MCPRiskLow},
	{Name: "beacon.approvals.own.get", RiskLevel: MCPRiskLow},
	{Name: "beacon.metadata.namespaces.list", RiskLevel: MCPRiskLow},
	{Name: "beacon.topology.snapshot.get", RiskLevel: MCPRiskLow},
	{Name: "beacon.topology.zone-tree.get", RiskLevel: MCPRiskLow},
	{Name: "beacon.topology.servers.list", RiskLevel: MCPRiskLow},
	{Name: "beacon.metrics.health.list", RiskLevel: MCPRiskLow},
	{Name: "beacon.metrics.summary.get", RiskLevel: MCPRiskLow},
	{Name: "beacon.metrics.series.query", RiskLevel: MCPRiskLow},
	{Name: "beacon.history.messages.list", RiskLevel: MCPRiskLow},
	{Name: "beacon.history.connections.stats", RiskLevel: MCPRiskLow},
	{Name: "beacon.history.commands.list", RiskLevel: MCPRiskLow},
	{Name: "beacon.history.scheduling-decisions.list", RiskLevel: MCPRiskLow},
	{Name: "beacon.audit.events.list", RiskLevel: MCPRiskLow},

	// ── 审批自助：撤回自己提交的申请，仅影响自身 ──
	{Name: "beacon.approvals.own.withdraw", RiskLevel: MCPRiskLow, AutomationOnly: true},

	// ── 审批决定：机器自批等同放行任意高危操作，MCP 面定为 critical ──
	{Name: "beacon.approvals.approve", RiskLevel: MCPRiskCritical, AutomationOnly: true, RequireApprovalDecide: true},
	{Name: "beacon.approvals.reject", RiskLevel: MCPRiskCritical, AutomationOnly: true, RequireApprovalDecide: true},

	// ── 拓扑建树（FR-221）：低风险结构操作，service 层有非空拒绝保护 ──
	{Name: "beacon.topology.bc-clusters.create", RiskLevel: MCPRiskLow, AutomationOnly: true},
	{Name: "beacon.topology.bc-clusters.update", RiskLevel: MCPRiskLow, AutomationOnly: true},
	{Name: "beacon.topology.bc-clusters.delete", RiskLevel: MCPRiskLow, AutomationOnly: true},
	{Name: "beacon.topology.regions.create", RiskLevel: MCPRiskLow, AutomationOnly: true},
	{Name: "beacon.topology.regions.update", RiskLevel: MCPRiskLow, AutomationOnly: true},
	{Name: "beacon.topology.regions.delete", RiskLevel: MCPRiskLow, AutomationOnly: true},
	{Name: "beacon.topology.zones.create", RiskLevel: MCPRiskLow, AutomationOnly: true},
	{Name: "beacon.topology.zones.update", RiskLevel: MCPRiskLow, AutomationOnly: true},
	{Name: "beacon.topology.zones.delete", RiskLevel: MCPRiskLow, AutomationOnly: true},

	// ── 配置中心：仅创建审批申请 ──
	{Name: "beacon.config.publish", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "config.publish"},
	{Name: "beacon.config.rollback", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "config.rollback"},
	{Name: "beacon.config.gray.publish", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "config.gray_publish"},
	{Name: "beacon.config.gray.promote", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "config.gray_promote"},
	{Name: "beacon.config.delete", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "config.delete"},
	{Name: "beacon.config.batch.delete", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "config.batch_delete"},
	{Name: "beacon.config.batch.enable", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "config.batch_enable"},
	{Name: "beacon.config.batch.disable", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "config.batch_disable"},

	// ── 文件与覆盖集 ──
	{Name: "beacon.files.create", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "file.create"},
	{Name: "beacon.files.import", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "file.import"},
	{Name: "beacon.files.publish", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "file.publish"},
	{Name: "beacon.files.rollback", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "file.rollback"},
	{Name: "beacon.files.delete", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "file.delete"},
	{Name: "beacon.files.batch.delete", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "file.batch_delete"},
	{Name: "beacon.files.batch.enable", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "file.batch_enable"},
	{Name: "beacon.files.batch.disable", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "file.batch_disable"},
	{Name: "beacon.override-sets.publish", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "override_set.publish"},
	{Name: "beacon.override-sets.rollback", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "override_set.rollback"},
	{Name: "beacon.override-sets.delete", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "override_set.delete"},

	// ── 敏感内容读取：request 建票据，consume 消费已批准的 grant ──
	{Name: "beacon.assets.preview.request", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "agent.command.fs_browse"},
	{Name: "beacon.assets.preview.consume", RiskLevel: MCPRiskHigh, AutomationOnly: true},
	{Name: "beacon.messages.payload.request", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "message.payload.read"},
	{Name: "beacon.messages.payload.consume", RiskLevel: MCPRiskHigh, AutomationOnly: true},

	// ── 凭据：签发与轮换可造成权限提升 ──
	{Name: "beacon.credentials.api-key.create", RiskLevel: MCPRiskCritical, AutomationOnly: true, OperationKind: "credential.create"},
	{Name: "beacon.credentials.api-key.rotate", RiskLevel: MCPRiskCritical, AutomationOnly: true, OperationKind: "credential.rotate"},

	// ── 身份与信任 ──
	{Name: "beacon.identity.agent.unbind", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "identity.unbind"},
	{Name: "beacon.identity.agent.enable", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "identity.enable"},
	{Name: "beacon.identity.agent.allow-reapply", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "identity.allow_reapply"},
	{Name: "beacon.identity.agent.approve", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "identity.approve"},
	{Name: "beacon.identity.agent.resolve-conflict", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "identity.resolve_conflict"},
	{Name: "beacon.trust.namespace.grant", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "namespace_trust.grant"},

	// ── 拓扑调度：影响玩家入口与归属 ──
	{Name: "beacon.topology.servers.assign", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "topology.server_assign"},
	{Name: "beacon.topology.servers.rezone", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "topology.server_rezone"},
	{Name: "beacon.topology.server.transfer-placement", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "topology.lobby_member.move"},
	{Name: "beacon.topology.server.disable-draining", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "topology.draining.disable"},
	{Name: "beacon.topology.server.set-default-entry", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "topology.default_entry.change"},

	// ── 生命周期：归档与恢复可回退，永久删除不可逆 ──
	{Name: "beacon.lifecycle.namespace.archive", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "namespace.archive"},
	{Name: "beacon.lifecycle.namespace.restore", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "namespace.restore"},
	{Name: "beacon.lifecycle.namespace.permanent-delete", RiskLevel: MCPRiskCritical, AutomationOnly: true, OperationKind: "namespace.permanent_delete"},
	{Name: "beacon.lifecycle.server.archive", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "server.archive"},
	{Name: "beacon.lifecycle.server.restore", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "server.restore"},
	{Name: "beacon.lifecycle.server.permanent-delete", RiskLevel: MCPRiskCritical, AutomationOnly: true, OperationKind: "server.permanent_delete"},

	// ── Agent 命令 ──
	{Name: "beacon.agent.server.resync", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "agent.command.resync"},

	// ── 系统：控制面自更新与高影响设置 ──
	{Name: "beacon.system.update.apply", RiskLevel: MCPRiskCritical, AutomationOnly: true, OperationKind: "system.update.apply"},
	{Name: "beacon.system.update.rollback", RiskLevel: MCPRiskCritical, AutomationOnly: true, OperationKind: "system.update.rollback"},
	{Name: "beacon.system.settings.update-dangerous", RiskLevel: MCPRiskCritical, AutomationOnly: true, OperationKind: "settings.update.dangerous"},

	// ── 交付：整单回滚不可逆性最高 ──
	{Name: "beacon.delivery.order.submit", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "delivery.approve"},
	{Name: "beacon.delivery.order.delete", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "delivery.draft_delete"},
	{Name: "beacon.delivery.order.resume", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "delivery.resume"},
	{Name: "beacon.delivery.order.rollback", RiskLevel: MCPRiskCritical, AutomationOnly: true, OperationKind: "delivery.rollback"},
	{Name: "beacon.delivery.batch.confirm", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "delivery.confirm_batch"},
	{Name: "beacon.delivery.rollback.finish", RiskLevel: MCPRiskHigh, AutomationOnly: true, OperationKind: "delivery.rollback_finish"},
}

// mcpToolSpecIndex 是 mcpToolCatalog 的名到登记项索引，构造期一次建成。
var mcpToolSpecIndex = func() map[string]mcpToolSpec {
	index := make(map[string]mcpToolSpec, len(mcpToolCatalog))
	for _, spec := range mcpToolCatalog {
		index[spec.Name] = spec
	}
	return index
}()

// mcpToolSpecByName 按工具名取登记项。
func mcpToolSpecByName(name string) (mcpToolSpec, bool) {
	spec, ok := mcpToolSpecIndex[name]
	return spec, ok
}

// mcpToolDiscoverable 是本包唯一的可发现性判定入口。
//
// MCPToolNames（测试与文档侧）与 mcpAddTool（运行时注册侧）共用它，
// 因此「清单声明的工具」与「真实注册的工具」不可能漂移。
func mcpToolDiscoverable(name string) bool {
	spec, ok := mcpToolSpecByName(name)
	if !ok {
		// 未登记即 fail-closed：宁可少暴露，也不暴露未分级工具。
		return false
	}
	if spec.RequireApprovalDecide && !auth.MCPApprovalDecideEnabled() {
		return false
	}
	// FR-237：生产模式隐藏 critical 档（不可逆 / 影响控制面自身 / 可提权）。
	if auth.MCPProductionModeEnabled() && spec.RiskLevel == MCPRiskCritical {
		return false
	}
	return true
}

// mcpAddTool 是 MCP 工具的唯一注册入口（FR-236 / FR-237）。
//
// 与 SDK 的 mcp.AddTool 签名一致，但注册前先经 mcpToolDiscoverable 判定：
// 未登记、当前不可见或已被生产模式收敛的工具不会进入 server 的工具集。
// 本包内全部工具注册都必须走它，以保证「运行时真实注册的集合」与
// 「MCPToolNames 声明的集合」由同一判定派生、不可能漂移。
func mcpAddTool[In, Out any](server *mcp.Server, tool *mcp.Tool, handler mcp.ToolHandlerFor[In, Out]) {
	if !mcpToolDiscoverable(tool.Name) {
		return
	}
	mcp.AddTool(server, tool, handler)
}
