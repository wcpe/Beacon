package server

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/render"
)

// MCP 工具风险等级：复用既有 authz.OperationDescriptor.RiskLevel 的取值体系
// （low / high / critical），不引入新档位（FR-236）。
const (
	// MCPRiskLow 表示只读、无副作用、仅操作申请者自身审批，或虽有写副作用
	// 但已由领域守卫限界（如拓扑建树的非空拒绝）的工具。
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
	// 告警事件摘要读取：只读、分页、按状态 / 级别 / 环境 / 实例 / 时间过滤，
	// 且**不透传 detail**（该列含状态前后与地址上下文），故定 low。
	{Name: "beacon.alerts.events.list", RiskLevel: MCPRiskLow},

	// 交付编排只读（FR-245）：列表 / 详情 / 目标 / 影响 / 观察窗 / 事件六个工具无副作用、
	// 返回均为有界投影（列表强制分页、详情只回摘要与计数、事件回有界条数）。
	// **不带 AutomationOnly**：observer 与 automation 共用（对齐其余只读段）；
	// AutomationOnly 的真实语义是「仅 automation 可发现」，挂上即令 observer 不可见。
	// OperationKind 留空：只读无审批语义（不触发「catalog 等级 ≥ descriptor」约束）。
	{Name: "beacon.delivery.order.list", RiskLevel: MCPRiskLow},
	{Name: "beacon.delivery.order.get", RiskLevel: MCPRiskLow},
	{Name: "beacon.delivery.targets.list", RiskLevel: MCPRiskLow},
	{Name: "beacon.delivery.impact.get", RiskLevel: MCPRiskLow},
	{Name: "beacon.delivery.observe.get", RiskLevel: MCPRiskLow},
	{Name: "beacon.delivery.events.list", RiskLevel: MCPRiskLow},

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

	// ── 告警处置：直接执行 + 同事务写审计，无审批票据（OperationKind 留空）──
	//
	// 定级依据：告警处理是运维元数据的直接变更，与 HTTP 面
	// `POST /admin/v1/alert-events/{id}/handle` 语义一致（管理台可直执）；批量路径单条 UPDATE、
	// 只影响 status='open' 的行、与审计同事务，故幂等；单条状态可再改，属可逆。
	// 因此不引入审批票据（OperationKind 留空）。但等级取 high 而非 low：关闭告警会隐藏故障信号、
	// 改变生产可见的运维状态，与只读工具不可同档。
	{Name: "beacon.alerts.events.handle", RiskLevel: MCPRiskHigh, AutomationOnly: true},
	{Name: "beacon.alerts.events.batch-handle", RiskLevel: MCPRiskHigh, AutomationOnly: true},

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

	// ── 交付直执写工具（FR-246 / FR-247）：draft 阶段组单与止损，直接执行 + 同事务写审计、无审批票据 ──
	//
	// 组单三项取 low：均为 draft 阶段操作，无生产副作用，且领域守卫已限界（draft 状态机、
	// selector 跨 namespace 拒绝、模板源结构校验）。止损两项取 high：直执改变生产状态
	// （进行中的灰度被暂停 / 终止），但可逆（可 resume / 可回滚）——与告警处置同档（FR-247）。
	//
	// 五项 OperationKind **全部留空**：直执无审批票据（对齐告警处置先例），故不触发
	// 「catalog 等级 ≥ descriptor」约束，也**不改覆盖测试的 wantChecked 计数**；
	// 若将来给任一项挂上 kind，必须同步核对并更新该计数。
	{Name: "beacon.delivery.order.create", RiskLevel: MCPRiskLow, AutomationOnly: true},
	{Name: "beacon.delivery.order.update", RiskLevel: MCPRiskLow, AutomationOnly: true},
	{Name: "beacon.delivery.order.diff-scan", RiskLevel: MCPRiskLow, AutomationOnly: true},
	{Name: "beacon.delivery.order.pause", RiskLevel: MCPRiskHigh, AutomationOnly: true},
	{Name: "beacon.delivery.order.cancel", RiskLevel: MCPRiskHigh, AutomationOnly: true},
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

// ── FR-242 执行面拒执 ──
//
// 发现面（mcpToolDiscoverable）只在**注册与清单期**隐藏 critical 工具：工具若因
// 开关组合意外可见（注册发生在开关翻转之前），或客户端缓存了旧的工具列表，仍可
// 被正常调用。执行面因此在**每次调用时独立再判一次**同一开关与同一份目录——
// 与发现面同源（不引入第二真源），且不依赖「工具没被注册」这一事实本身。

// mcpProductionModeRejectedReason 是执行面拒执的统一理由，与结果文本一起回给客户端。
const mcpProductionModeRejectedReason = "生产模式已禁用 critical 风险等级工具"

// mcpToolExecutionBlocked 判定工具在「被调用的此刻」是否必须被拒执（FR-242）。
//
// 与 mcpToolDiscoverable 共用同一开关（auth.MCPProductionModeEnabled）与同一份
// mcpToolCatalog 风险等级，差别只在判定时机与职责：发现面决定「是否注册/是否进清单」，
// 本判定决定「调用是否执行」。
//
// 刻意**不复用** mcpToolDiscoverable：它还会因 approval-decide 开关与「未登记」返回
// false，套到执行面会让开关关闭时的行为与现状不一致（例如未登记工具即使在非生产模式
// 下也会被拒执行）。执行面只对「生产模式 + critical」这一条负责。
func mcpToolExecutionBlocked(name string) bool {
	if !auth.MCPProductionModeEnabled() {
		// 开关关闭：执行面一律放行，行为与 FR-242 之前逐项一致。
		return false
	}
	spec, ok := mcpToolSpecByName(name)
	if !ok {
		// 未登记工具（风险等级未知）：注册面已 fail-closed 不注册；执行面若仍遇到
		// （例如经其他路径进入 server），同样拒执——「未分级即可调用」会绕过整个分级体系。
		return true
	}
	return spec.RiskLevel == MCPRiskCritical
}

// mcpGuardToolExecution 给工具 handler 包一层执行前判定（FR-242）。
//
// 只由 mcpAddTool 调用，故本包全部工具的调用路径都经过这里；被拒时原 handler
// **完全不执行**（零副作用），只回可读的拒绝结果并留痕。
//
// 位置说明：SDK 在调用 handler 之前会先按输入 schema 校验参数，因此参数非法的调用
// 会先得到协议侧的参数错误。那类调用本就到不了领域逻辑、不产生副作用，无需本条兜底；
// 本层要挡的是「参数合法、工具本不该可调用」的调用。
func mcpGuardToolExecution[In, Out any](name string, handler mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		if !mcpToolExecutionBlocked(name) {
			return handler(ctx, req, in)
		}
		principal, _ := auth.FromContext(ctx)
		mcpRecordProductionModeRejection(ctx, name, principal)
		return mcpToolErrorWithReason(mcpProductionModeRejectedReason + "：" + name), mcpEmptyToolOutput[Out](), nil
	}
}

// mcpEmptyToolOutput 返回 Out 的「非 null」空值。
//
// 拒绝路径必须回非 null 的结构化输出：SDK 会把返回值序列化后按输出 schema 校验，
// map 的 nil 零值会序列化成 JSON null，让一次已判定的业务拒绝升级为协议错误
// （与 mcpRejectedResult 回空对象同理）。本包工具的输出类型当前均为 map[string]any。
func mcpEmptyToolOutput[Out any]() Out {
	var zero Out
	if empty, ok := any(&zero).(*map[string]any); ok {
		*empty = map[string]any{}
	}
	return zero
}

// mcpRejectionTrace 是一次被拒执调用的最小痕迹（工具名 + 调用主体 + 追踪号）。
type mcpRejectionTrace struct {
	Tool     string
	ClientID string
	Profile  string
	TraceID  string
}

// mcpRejectionTraceLimit 限制进程内痕迹条数，避免长跑部署下无界增长。
const mcpRejectionTraceLimit = 64

var (
	mcpRejectionMu   sync.Mutex
	mcpRejectionLogs []mcpRejectionTrace
)

// mcpRecordProductionModeRejection 记录一次拒执：结构化日志（运行期可观测）+
// 进程内痕迹（供测试与诊断读取）。两条都**不阻断主路径**，与兜底审计的旁路语义一致。
//
// 为什么不在此落库：本层（server 包）拿不到审计仓库——审计写入发生在各领域 service
// 内，MCP 工具注册表只持有领域服务；MCP 侧的既有审计先例（mcp.token.denied）同样由
// service 层持 audit 依赖写出。因此在库表意义上的「流水」留给 FR-240 的 tools/call
// 流水（拒执即 result=rejected 的一行），本层先在唯一收口处留下可观测痕迹。
func mcpRecordProductionModeRejection(ctx context.Context, name string, principal auth.Principal) {
	level := "未登记"
	if spec, ok := mcpToolSpecByName(name); ok {
		level = spec.RiskLevel
	}
	traceID := render.TraceID(ctx)
	slog.Warn("MCP 生产模式拒执工具调用",
		"工具", name, "风险等级", level, "客户端", principal.ID, "profile", principal.Role,
		"原因", mcpProductionModeRejectedReason, "traceId", traceID)
	mcpRejectionMu.Lock()
	defer mcpRejectionMu.Unlock()
	if len(mcpRejectionLogs) >= mcpRejectionTraceLimit {
		mcpRejectionLogs = mcpRejectionLogs[1:]
	}
	mcpRejectionLogs = append(mcpRejectionLogs, mcpRejectionTrace{
		Tool: name, ClientID: principal.ID, Profile: principal.Role, TraceID: traceID,
	})
}

// mcpRejectionsSnapshot 返回已记录的拒执痕迹副本（只读，供测试断言）。
func mcpRejectionsSnapshot() []mcpRejectionTrace {
	mcpRejectionMu.Lock()
	defer mcpRejectionMu.Unlock()
	return append([]mcpRejectionTrace(nil), mcpRejectionLogs...)
}

// mcpResetRejections 清空痕迹，供测试在断言前取得干净起点。
func mcpResetRejections() {
	mcpRejectionMu.Lock()
	defer mcpRejectionMu.Unlock()
	mcpRejectionLogs = nil
}

// mcpAddTool 是 MCP 工具的唯一注册入口（FR-236 / FR-237 / FR-242）。
//
// 与 SDK 的 mcp.AddTool 签名一致，但注册前先经 mcpToolDiscoverable 判定：
// 已登记但当前不可见（生产模式收敛、审批决定开关关闭）的工具不会进入 server；
// 未登记的工具同样不注册（fail-closed），并记入注册意图痕迹供覆盖测试捕获。
// 注册成功后 handler 一律套上 mcpGuardToolExecution：发现面给不出的保证
// （工具意外可见、客户端缓存旧清单）由执行面在调用时再判一次补齐。
// 本包内全部工具注册都必须走它，以保证「运行时真实注册的集合」与
// 「MCPToolNames 声明的集合」由同一判定派生、不可能漂移。
func mcpAddTool[In, Out any](server *mcp.Server, tool *mcp.Tool, handler mcp.ToolHandlerFor[In, Out]) {
	if _, registered := mcpToolSpecByName(tool.Name); !registered {
		// fail-closed 不注册；同时留痕——否则「代码新增工具、目录漏登记」会
		// 静默消失（清单与注册两侧同时缺失，双向一致性断言看不见），
		// 与 ADR-0079「新增入口未分类时覆盖测试失败」的要求相悖。
		mcpRecordUnregisteredAttempt(tool.Name)
		return
	}
	if !mcpToolDiscoverable(tool.Name) {
		return
	}
	// 执行面兜底（FR-242）：注册面能挡住的这里不再挡，注册面挡不住的在调用时挡。
	mcp.AddTool(server, tool, mcpGuardToolExecution(tool.Name, handler))
}

// mcpUnregisteredAttempts 记录「运行时尝试注册、但未登记入目录」的工具名。
//
// 只由 mcpAddTool 写入、只由覆盖测试读取；生产路径不消费它，故仅是诊断痕迹。
var (
	mcpUnregisteredMu       sync.Mutex
	mcpUnregisteredAttempts = map[string]struct{}{}
)

func mcpRecordUnregisteredAttempt(name string) {
	mcpUnregisteredMu.Lock()
	defer mcpUnregisteredMu.Unlock()
	mcpUnregisteredAttempts[name] = struct{}{}
}

// mcpUnregisteredSnapshot 返回已记录的未登记工具名（排序后），供覆盖测试断言为空。
func mcpUnregisteredSnapshot() []string {
	mcpUnregisteredMu.Lock()
	defer mcpUnregisteredMu.Unlock()
	names := make([]string, 0, len(mcpUnregisteredAttempts))
	for name := range mcpUnregisteredAttempts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// mcpResetUnregisteredAttempts 清空痕迹，供测试在枚举前取得干净起点。
func mcpResetUnregisteredAttempts() {
	mcpUnregisteredMu.Lock()
	defer mcpUnregisteredMu.Unlock()
	mcpUnregisteredAttempts = map[string]struct{}{}
}
