package server

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// MCP 只读拓扑工具的失败原因（不向客户端透传内部细节）。
var (
	errMCPNamespaceRequired = errors.New("需要指定 namespace")
	errMCPNamespaceDenied   = errors.New("namespace 不在授权范围内")
)

// MCPReadServices 只聚合已存在的应用查询服务，避免 MCP 越过服务层直读存储。
type MCPReadServices struct {
	v2          *service.V2ControlPlaneService
	topology    *service.TopologyService
	health      *service.HealthQueryService
	messages    *service.MessageQueryService
	connections *service.ConnQueryService
	commands    *service.CommandObserveService
	scheduling  *service.SchedDecisionQueryService
	audits      *service.AuditService
	// alertEvents 供告警事件只读列表（只走 List 查询，处理动作在 mcp_alert_tools.go 的写工具组）。
	alertEvents *service.AlertEventService
	// deliveryDiff 供交付影响预览只读工具（FR-245）：影响预览归 DeliveryDiffService
	// （与组单生命周期服务职责分离）；交付组单读走注册表既有的 orders。
	deliveryDiff *service.DeliveryDiffService
	scope        *service.ObservationScopeResolver
}

// NewMCPReadServices 构造 MCP 只读服务集合；仅允许调用方传入应用查询服务。
//
// 全部只读依赖**一次传齐**（含交付影响预览的差异面服务）：注册表侧的接入点只有
// SetReadServices 一处，故不另设逐服务 setter——否则「先 setter 后 SetReadServices」
// 会让先设的依赖被整体赋值静默覆盖，表现为对应工具悄无声息地不注册。
func NewMCPReadServices(v2 *service.V2ControlPlaneService, topology *service.TopologyService, health *service.HealthQueryService, messages *service.MessageQueryService, connections *service.ConnQueryService, commands *service.CommandObserveService, scheduling *service.SchedDecisionQueryService, audits *service.AuditService, alertEvents *service.AlertEventService, deliveryDiff *service.DeliveryDiffService, scope *service.ObservationScopeResolver) MCPReadServices {
	return MCPReadServices{v2: v2, topology: topology, health: health, messages: messages, connections: connections, commands: commands, scheduling: scheduling, audits: audits, alertEvents: alertEvents, deliveryDiff: deliveryDiff, scope: scope}
}

type mcpPageInput struct {
	mcpScopeInput
	Page     int `json:"page,omitempty"`
	PageSize int `json:"pageSize,omitempty"`
}

type mcpScopeInput struct {
	EnvID       string `json:"envId,omitempty"`
	NamespaceID string `json:"namespaceId,omitempty"`
}

type mcpTopologyInput struct {
	mcpScopeInput
	Namespace string `json:"namespace"`
}

type mcpZoneTreeInput struct {
	mcpScopeInput
	Namespace string `json:"namespace,omitempty"`
}

type mcpServerListInput struct {
	mcpScopeInput
	Namespace       string `json:"namespace,omitempty"`
	Keyword         string `json:"keyword,omitempty"`
	LifecycleStatus string `json:"lifecycleStatus,omitempty"`
	Page            int    `json:"page,omitempty"`
	PageSize        int    `json:"pageSize,omitempty"`
}

type mcpHealthListInput struct {
	mcpPageInput
	Zone    string `json:"zone,omitempty"`
	Level   string `json:"level,omitempty"`
	Keyword string `json:"keyword,omitempty"`
}

type mcpMetricsSeriesInput struct {
	mcpScopeInput
	ServerIDs []string `json:"serverIds"`
	FromMs    int64    `json:"fromMs,omitempty"`
	ToMs      int64    `json:"toMs,omitempty"`
	StepSec   int      `json:"stepSec,omitempty"`
}

type mcpMessageHistoryInput struct {
	mcpScopeInput
	ServerID string `json:"serverId"`
	FromMs   int64  `json:"fromMs"`
	ToMs     int64  `json:"toMs"`
	Cursor   int    `json:"cursor,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type mcpConnectionStatsInput struct {
	mcpScopeInput
	ServerID string `json:"serverId,omitempty"`
	FromMs   int64  `json:"fromMs"`
	ToMs     int64  `json:"toMs"`
	Bucket   string `json:"bucket,omitempty"`
}

type mcpCommandHistoryInput struct {
	mcpScopeInput
	mcpPageInput
	Namespace string `json:"namespace,omitempty"`
	ServerID  string `json:"serverId,omitempty"`
	Type      string `json:"type,omitempty"`
	Status    string `json:"status,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
}

type mcpSchedulingHistoryInput struct {
	mcpScopeInput
	mcpPageInput
	FromMs   int64  `json:"fromMs"`
	ToMs     int64  `json:"toMs"`
	ServerID string `json:"serverId,omitempty"`
	Result   string `json:"result,omitempty"`
}

type mcpAuditListInput struct {
	mcpScopeInput
	mcpPageInput
	Namespace  string `json:"namespace,omitempty"`
	Action     string `json:"action,omitempty"`
	TargetType string `json:"targetType,omitempty"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
}

type mcpAlertEventListInput struct {
	mcpScopeInput
	mcpPageInput
	Namespace string `json:"namespace,omitempty"`
	ServerID  string `json:"serverId,omitempty"`
	Type      string `json:"type,omitempty"`
	Level     string `json:"level,omitempty"`
	// Status 非空时按处理状态过滤（open / acknowledged / resolved）。
	Status string `json:"status,omitempty"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

// registerReadTools 登记 observer 与 automation 共用的显式只读工具。所有返回值在此投影，绝不透传敏感实体。
func (r *MCPToolRegistry) registerReadTools(server *mcp.Server) {
	if r == nil {
		return
	}
	if r.reads.v2 != nil {
		mcpAddTool(server, &mcp.Tool{Name: "beacon.metadata.namespaces.list", Description: "分页读取 namespace 元数据摘要"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpPageInput) (*mcp.CallToolResult, map[string]any, error) {
			scope, ok := r.mcpObservationScope(in.mcpScopeInput)
			if !ok {
				return mcpRejectedResult()
			}
			items, err := r.reads.v2.ListNamespacesWithStats()
			if err != nil {
				return mcpRejectedResult()
			}
			items = mcpNamespacesInScope(items, scope)
			start, end := mcpPageBounds(in.Page, in.PageSize, len(items))
			views := make([]map[string]any, 0, end-start)
			for _, item := range items[start:end] {
				views = append(views, map[string]any{"id": item.Namespace.ID, "code": item.Namespace.Code, "name": item.Namespace.Name, "description": item.Namespace.Description, "serverCount": item.ServerCount, "bcClusterCount": item.BCClusterCount, "activeTrustCount": item.ActiveTrustCount, "createdAt": item.Namespace.CreatedAt.UTC().Format(time.RFC3339), "updatedAt": item.Namespace.UpdatedAt.UTC().Format(time.RFC3339)})
			}
			return &mcp.CallToolResult{}, map[string]any{"items": views, "total": len(items)}, nil
		})
	}
	if r.reads.topology != nil {
		mcpAddTool(server, &mcp.Tool{Name: "beacon.topology.snapshot.get", Description: "读取指定 namespace 的在线拓扑摘要"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpTopologyInput) (*mcp.CallToolResult, map[string]any, error) {
			scope, ok := r.mcpObservationScope(in.mcpScopeInput)
			if !ok || in.Namespace == "" || !r.mcpNamespaceVisible(in.Namespace, scope) {
				return mcpRejectedResult()
			}
			topology := r.reads.topology.Build(in.Namespace)
			return &mcp.CallToolResult{}, mcpTopologyView(topology), nil
		})
	}
	r.registerReadV2TopologyTools(server)
	r.registerReadHealthTools(server)
	if r.reads.messages != nil {
		mcpAddTool(server, &mcp.Tool{Name: "beacon.history.messages.list", Description: "读取消息元数据历史，不含正文与玩家标识"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpMessageHistoryInput) (*mcp.CallToolResult, map[string]any, error) {
			scope, ok := r.mcpObservationScope(in.mcpScopeInput)
			if !ok {
				return mcpRejectedResult()
			}
			page, err := r.reads.messages.List(service.ListMessagesParams{ServerID: in.ServerID, NamespaceIDs: scope.NamespaceIDs, Scoped: !scope.All, FromMs: in.FromMs, ToMs: in.ToMs, Cursor: in.Cursor, Limit: in.Limit})
			if err != nil {
				return mcpRejectedResult()
			}
			return &mcp.CallToolResult{}, mcpMessageHistoryView(page), nil
		})
	}
	if r.reads.connections != nil {
		mcpAddTool(server, &mcp.Tool{Name: "beacon.history.connections.stats", Description: "读取连接历史聚合，不含玩家、地址或单连接明细"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpConnectionStatsInput) (*mcp.CallToolResult, map[string]any, error) {
			scope, ok := r.mcpObservationScope(in.mcpScopeInput)
			if !ok {
				return mcpRejectedResult()
			}
			items, err := r.reads.connections.Stats(service.ConnStatsParams{ServerID: in.ServerID, NamespaceIDs: scope.NamespaceIDs, Scoped: !scope.All, FromMs: in.FromMs, ToMs: in.ToMs, Bucket: in.Bucket})
			if err != nil {
				return mcpRejectedResult()
			}
			return &mcp.CallToolResult{}, map[string]any{"items": items}, nil
		})
	}
	if r.reads.commands != nil {
		mcpAddTool(server, &mcp.Tool{Name: "beacon.history.commands.list", Description: "分页读取 Agent 命令历史元数据，不含结果正文"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpCommandHistoryInput) (*mcp.CallToolResult, map[string]any, error) {
			scope, ok := r.mcpObservationScope(in.mcpScopeInput)
			if !ok {
				return mcpRejectedResult()
			}
			items, total, err := r.reads.commands.List(repository.CommandFilter{Namespace: in.Namespace, NamespaceCodes: scope.NamespaceCodes, Scoped: !scope.All, ServerID: in.ServerID, Type: in.Type, Status: in.Status, From: mcpParseTime(in.From), To: mcpParseTime(in.To), Page: in.Page, Size: in.PageSize})
			if err != nil {
				return mcpRejectedResult()
			}
			return &mcp.CallToolResult{}, mcpCommandHistoryView(items, total), nil
		})
	}
	if r.reads.scheduling != nil {
		mcpAddTool(server, &mcp.Tool{Name: "beacon.history.scheduling-decisions.list", Description: "分页读取调度决策历史摘要，不含候选排除明细"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpSchedulingHistoryInput) (*mcp.CallToolResult, map[string]any, error) {
			scope, ok := r.mcpObservationScope(in.mcpScopeInput)
			if !ok {
				return mcpRejectedResult()
			}
			items, total, err := r.reads.scheduling.List(service.ListSchedDecisionsParams{NamespaceIDs: scope.NamespaceIDs, Scoped: !scope.All, FromMs: in.FromMs, ToMs: in.ToMs, ServerID: in.ServerID, Result: in.Result, Page: in.Page, PageSize: in.PageSize})
			if err != nil {
				return mcpRejectedResult()
			}
			return &mcp.CallToolResult{}, mcpSchedulingHistoryView(items, total), nil
		})
	}
	if r.reads.audits != nil {
		mcpAddTool(server, &mcp.Tool{Name: "beacon.audit.events.list", Description: "分页读取脱敏审计事件摘要"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpAuditListInput) (*mcp.CallToolResult, map[string]any, error) {
			scope, ok := r.mcpObservationScope(in.mcpScopeInput)
			if !ok {
				return mcpRejectedResult()
			}
			items, total, err := r.reads.audits.List(repository.AuditFilter{Namespace: in.Namespace, NamespaceCodes: scope.NamespaceCodes, Scoped: !scope.All, Action: in.Action, TargetType: in.TargetType, From: mcpParseTime(in.From), To: mcpParseTime(in.To), Page: in.Page, Size: in.PageSize})
			if err != nil {
				return mcpRejectedResult()
			}
			return &mcp.CallToolResult{}, mcpAuditHistoryView(items, total), nil
		})
	}
	if r.reads.alertEvents != nil {
		mcpAddTool(server, &mcp.Tool{Name: "beacon.alerts.events.list", Description: "分页读取告警事件摘要，不含结构化详情（detail）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpAlertEventListInput) (*mcp.CallToolResult, map[string]any, error) {
			scope, ok := r.mcpObservationScope(in.mcpScopeInput)
			if !ok {
				return mcpRejectedResult()
			}
			items, total, err := r.reads.alertEvents.List(repository.AlertEventFilter{Namespace: in.Namespace, NamespaceCodes: scope.NamespaceCodes, Scoped: !scope.All, ServerID: in.ServerID, Type: in.Type, Level: in.Level, Status: in.Status, From: mcpParseTime(in.From), To: mcpParseTime(in.To), Page: in.Page, Size: in.PageSize})
			if err != nil {
				return mcpRejectedResult()
			}
			return &mcp.CallToolResult{}, mcpAlertEventHistoryView(items, total), nil
		})
	}
	r.registerReadDeliveryTools(server)
}

// registerReadV2TopologyTools 登记依赖 V2 控制面读取层的拓扑只读工具（FR-221）。
// 从 registerReadTools 抽出，避免其圈复杂度与嵌套深度超限（gocyclo / nestif 门禁）。
func (r *MCPToolRegistry) registerReadV2TopologyTools(server *mcp.Server) {
	if r.reads.v2 == nil {
		return
	}
	mcpAddTool(server, &mcp.Tool{Name: "beacon.topology.zone-tree.get", Description: "读取区服结构树（BC 集群 → 大区 → 小区，含各节点计数与默认入口统计）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpZoneTreeInput) (*mcp.CallToolResult, map[string]any, error) {
		scope, ok := r.mcpObservationScope(in.mcpScopeInput)
		nsID, err := r.mcpResolveNamespaceID(in.Namespace, in.NamespaceID, scope)
		if !ok || err != nil {
			return mcpRejectedResult()
		}
		tree, err := r.reads.v2.ZoneTree(nsID)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpZoneTreeView(tree), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.topology.servers.list", Description: "分页读取 server 富化视图（含归属名 / 默认入口 / 在线摘要 / 排空状态）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpServerListInput) (*mcp.CallToolResult, map[string]any, error) {
		scope, ok := r.mcpObservationScope(in.mcpScopeInput)
		if !ok {
			return mcpRejectedResult()
		}
		nsID, err := r.mcpResolveNamespaceID(in.Namespace, in.NamespaceID, scope)
		if err != nil {
			return mcpRejectedResult()
		}
		lifecycle := in.LifecycleStatus
		if lifecycle == "" {
			lifecycle = "active"
		}
		views, total, err := r.reads.v2.ListServers(service.ListServersParams{
			NamespaceID:     nsID,
			LifecycleStatus: lifecycle,
			Keyword:         in.Keyword,
			Page:            in.Page,
			PageSize:        in.PageSize,
		})
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpServerListView(views, total), nil
	})
}
func (r *MCPToolRegistry) registerReadHealthTools(server *mcp.Server) {
	if r.reads.health == nil {
		return
	}
	mcpAddTool(server, &mcp.Tool{Name: "beacon.metrics.health.list", Description: "分页读取实时健康摘要"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpHealthListInput) (*mcp.CallToolResult, map[string]any, error) {
		scope, ok := r.mcpObservationScope(in.mcpScopeInput)
		if !ok {
			return mcpRejectedResult()
		}
		items, total, err := r.reads.health.ListHealth(service.ListHealthParams{NamespaceIDs: scope.NamespaceIDs, Scoped: !scope.All, Zone: in.Zone, Level: in.Level, Keyword: in.Keyword, Page: in.Page, PageSize: in.PageSize})
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, map[string]any{"items": items, "total": total}, nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.metrics.summary.get", Description: "读取集群实时指标摘要"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpScopeInput) (*mcp.CallToolResult, map[string]any, error) {
		scope, ok := r.mcpObservationScope(in)
		if !ok {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, map[string]any{"summary": r.reads.health.MetricsSummaryInScope(scope)}, nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.metrics.series.query", Description: "读取指定服务器的有界指标时序"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpMetricsSeriesInput) (*mcp.CallToolResult, map[string]any, error) {
		scope, ok := r.mcpObservationScope(in.mcpScopeInput)
		if !ok || len(in.ServerIDs) > 20 {
			return mcpRejectedResult()
		}
		series, err := r.reads.health.MetricsSeriesInScope(service.MetricsSeriesParams{ServerIDs: in.ServerIDs, FromMs: in.FromMs, ToMs: in.ToMs, StepSec: in.StepSec}, scope)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, map[string]any{"stepSec": series.StepSec, "series": series.Series}, nil
	})
}

// ── FR-245 交付域只读工具 ──
//
// 六个工具（order.list / order.get / targets.list / impact.get / observe.get / events.list）对
// observer 与 automation **同时可见**（catalog 不带 AutomationOnly），返回体一律为**有界投影**：
// 列表强制分页、详情只回摘要与计数（不回 items 全量文件清单与批次明细）、影响与目标逐台分页、
// 观察窗与事件沿用 HTTP 端点的有界条数。输出键名一律沿用既有 HTTP 视图字段名
// （apps/server/internal/service/delivery_views.go 为准），不自造键名。

// mcpDeliveryOrderListInput 是变更单列表入参：namespaceId 为数值 ID 且必填——列表按 namespace 定位。
// 范围参数本身是调用者声明的收窄条件，缺省即其可观测的全量范围（与告警处置同口径），不是越界闸。
type mcpDeliveryOrderListInput struct {
	mcpScopeInput
	Status    string `json:"status,omitempty"`
	CreatedBy string `json:"createdBy,omitempty"`
	Keyword   string `json:"keyword,omitempty"`
	Page      int    `json:"page,omitempty"`
	PageSize  int    `json:"pageSize,omitempty"`
}

// mcpDeliveryOrderInput 是按 orderId 定位的交付只读入参（详情 / 观察窗 / 事件共形）。
// 范围参数可选：显式给出即收窄到该 namespace 判归属，缺省即调用者可观测的全量范围。
type mcpDeliveryOrderInput struct {
	mcpScopeInput
	OrderID uint `json:"orderId"`
}

// mcpDeliveryTargetsListInput 是目标分页入参（过滤项与 HTTP 端点同名）。
type mcpDeliveryTargetsListInput struct {
	mcpScopeInput
	OrderID  uint   `json:"orderId"`
	Batch    int    `json:"batch,omitempty"`
	Status   string `json:"status,omitempty"`
	ServerID string `json:"serverId,omitempty"`
	Page     int    `json:"page,omitempty"`
	PageSize int    `json:"pageSize,omitempty"`
}

// mcpDeliveryImpactInput 是影响预览入参。
type mcpDeliveryImpactInput struct {
	mcpScopeInput
	OrderID  uint `json:"orderId"`
	Page     int  `json:"page,omitempty"`
	PageSize int  `json:"pageSize,omitempty"`
}

// registerReadDeliveryTools 登记交付域只读工具（FR-245）。
//
// 未接入交付组单读服务（r.orders）时整组不注册；影响预览另需差异面服务，
// 未装配即只注册其余五项——与本包既有 nil 守卫同口径，不注册注定不可用的工具。
func (r *MCPToolRegistry) registerReadDeliveryTools(server *mcp.Server) {
	if r.orders == nil {
		return
	}
	r.registerReadDeliveryOrderTools(server)
	if r.reads.deliveryDiff != nil {
		r.registerReadDeliveryImpactTool(server)
	}
}

// registerReadDeliveryOrderTools 登记由组单读服务承担的五个交付只读工具。
func (r *MCPToolRegistry) registerReadDeliveryOrderTools(server *mcp.Server) {
	mcpAddTool(server, &mcp.Tool{Name: "beacon.delivery.order.list", Description: "变更单列表（分页筛选；仅回摘要字段；namespaceId 为数值 ID 且必填）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryOrderListInput) (*mcp.CallToolResult, map[string]any, error) {
		scope, ok := r.mcpObservationScope(in.mcpScopeInput)
		if !ok || in.NamespaceID == "" {
			return mcpRejectedResult()
		}
		nsID, err := r.mcpResolveNamespaceID("", in.NamespaceID, scope)
		if err != nil {
			return mcpRejectedResult()
		}
		view, err := r.orders.List(repository.ChangeOrderListQuery{
			NamespaceID: nsID, Status: in.Status, CreatedBy: in.CreatedBy, Keyword: in.Keyword,
			Page: normalizedMCPPage(in.Page), Size: normalizedMCPPageSize(in.PageSize),
		})
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpDeliveryOrderListView(view), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.delivery.order.get", Description: "变更单详情（摘要与目标计数，不含文件清单与批次明细）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryOrderInput) (*mcp.CallToolResult, map[string]any, error) {
		detail, ok := r.mcpDeliveryOrderDetail(in.mcpScopeInput, in.OrderID)
		if !ok {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpDeliveryOrderDetailView(detail), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.delivery.targets.list", Description: "目标分页（逐台状态 / 失败原因 / 备份标记）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryTargetsListInput) (*mcp.CallToolResult, map[string]any, error) {
		if _, ok := r.mcpDeliveryOrderDetail(in.mcpScopeInput, in.OrderID); !ok {
			return mcpRejectedResult()
		}
		view, err := r.orders.Targets(in.OrderID, repository.ChangeTargetQuery{
			BatchNo: in.Batch, Status: in.Status, ServerID: in.ServerID,
			Page: normalizedMCPPage(in.Page), Size: normalizedMCPPageSize(in.PageSize),
		})
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpDeliveryTargetsView(view), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.delivery.observe.get", Description: "当前批观察窗序列（逐目标时间桶 / 健康分与等级 / TPS / 告警数）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryOrderInput) (*mcp.CallToolResult, map[string]any, error) {
		if _, ok := r.mcpDeliveryOrderDetail(in.mcpScopeInput, in.OrderID); !ok {
			return mcpRejectedResult()
		}
		view, err := r.orders.Observe(in.OrderID)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpDeliveryObserveView(view), nil
	})
	mcpAddTool(server, &mcp.Tool{Name: "beacon.delivery.events.list", Description: "进度事件（阶段 / 时间 / 摘要，不含 SSE 流式语义）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryOrderInput) (*mcp.CallToolResult, map[string]any, error) {
		if _, ok := r.mcpDeliveryOrderDetail(in.mcpScopeInput, in.OrderID); !ok {
			return mcpRejectedResult()
		}
		view, err := r.orders.Events(in.OrderID)
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpDeliveryEventsView(view), nil
	})
}

// registerReadDeliveryImpactTool 登记影响预览工具（另依赖差异面服务）。
func (r *MCPToolRegistry) registerReadDeliveryImpactTool(server *mcp.Server) {
	mcpAddTool(server, &mcp.Tool{Name: "beacon.delivery.impact.get", Description: "影响预览（汇总 + 逐目标分页；逐目标含差异计数与命中配置作用域）"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpDeliveryImpactInput) (*mcp.CallToolResult, map[string]any, error) {
		if _, ok := r.mcpDeliveryOrderDetail(in.mcpScopeInput, in.OrderID); !ok {
			return mcpRejectedResult()
		}
		view, err := r.reads.deliveryDiff.Impact(in.OrderID, normalizedMCPPage(in.Page), normalizedMCPPageSize(in.PageSize))
		if err != nil {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, mcpDeliveryImpactView(view), nil
	})
}

// mcpDeliveryOrderDetail 解析观察范围并按 orderId 取单，校验单归属落在范围内。
//
// 交付域除 order.list 外都由 orderId 定位：单的 namespace 是服务端事实（落库后不变），
// 故「先解析范围、再取单、再按其 namespace 判范围」。范围外与不存在**共用同一条拒绝**，
// 不泄露范围外单是否存在（与 mcp_alert_tools.go 单条处置同口径）。
func (r *MCPToolRegistry) mcpDeliveryOrderDetail(in mcpScopeInput, orderID uint) (*service.ChangeOrderDetailView, bool) {
	scope, ok := r.mcpObservationScope(in)
	if !ok {
		return nil, false
	}
	detail, err := r.orders.Get(orderID)
	if err != nil {
		return nil, false
	}
	if !scope.Contains(detail.NamespaceID) {
		return nil, false
	}
	return detail, true
}

// mcpDeliveryOrderListView 投影变更单列表：只回摘要 7 键 + total（对齐既有 ChangeOrderListView，不含额外键）。
func mcpDeliveryOrderListView(view *service.ChangeOrderListView) map[string]any {
	items := make([]map[string]any, 0, len(view.Items))
	for _, item := range view.Items {
		items = append(items, map[string]any{
			"id": item.ID, "title": item.Title, "status": item.Status,
			"pauseKind": mcpDerefString(item.PauseKind), "createdBy": item.CreatedBy,
			"createdAt":    item.CreatedAt.UTC().Format(time.RFC3339),
			"payloadState": item.PayloadState,
		})
	}
	return map[string]any{"items": items, "total": view.Total}
}

// mcpDeliveryOrderDetailView 投影变更单详情：只回摘要字段与计数。
//
// **不回 items（逐文件清单）与 batches（批次明细）**：大单（1000+ 文件 / 目标）会撑爆返回体，
// 逐目标态走 targets.list、影响走 impact.get 分页拉取（规格 §3.2）。
func mcpDeliveryOrderDetailView(view *service.ChangeOrderDetailView) map[string]any {
	return map[string]any{
		"id": view.ID, "title": view.Title, "description": view.Description,
		"namespaceId": view.NamespaceID, "status": view.Status,
		"pauseKind": mcpDerefString(view.PauseKind), "pauseReason": mcpDerefString(view.PauseReason),
		"selector":                      mcpDeliverySelectorView(view.Selector),
		"batchMode":                     view.BatchMode,
		"batchSizes":                    view.BatchSizes,
		"activationMethod":              view.ActivationMethod,
		"observeWindowSec":              view.ObserveWindowSec,
		"activateTimeoutSec":            view.ActivateTimeoutSec,
		"failureRateThresholdPercent":   view.FailureRateThresholdPercent,
		"unhealthyRateThresholdPercent": view.UnhealthyRateThresholdPercent,
		"payloadState":                  view.PayloadState,
		"createdBy":                     view.CreatedBy,
		"submittedAt":                   mcpNullableTime(view.SubmittedAt),
		"approvedAt":                    mcpNullableTime(view.ApprovedAt),
		"startedAt":                     mcpNullableTime(view.StartedAt),
		"finishedAt":                    mcpNullableTime(view.FinishedAt),
		"cancelReason":                  mcpDerefString(view.CancelReason),
		"rollbackBy":                    mcpDerefString(view.RollbackBy),
		"rollbackReason":                mcpDerefString(view.RollbackReason),
		"rollbackAt":                    mcpNullableTime(view.RollbackAt),
		"targetCounts":                  view.TargetCounts,
		"rollbackCounts":                view.RollbackCounts,
	}
}

// mcpDeliverySelectorView 投影 selector 摘要（存库为 TEXT JSON、视图为对象，键名即响应契约）。
func mcpDeliverySelectorView(selector service.ChangeSelector) map[string]any {
	return map[string]any{
		"all": selector.All, "regions": selector.Regions, "zones": selector.Zones,
		"servers": selector.Servers, "excludes": selector.Excludes,
	}
}

// mcpDeliveryTargetsView 投影目标分页（对齐既有 ChangeTargetPageView）。
func mcpDeliveryTargetsView(view *service.ChangeTargetPageView) map[string]any {
	items := make([]map[string]any, 0, len(view.Items))
	for _, target := range view.Items {
		items = append(items, map[string]any{
			"serverId": target.ServerID, "batchNo": target.BatchNo, "status": target.Status,
			"rollbackStatus": mcpDerefString(target.RollbackStatus), "error": mcpDerefString(target.Error),
			"rollbackError": mcpDerefString(target.RollbackError), "backupPresent": target.BackupPresent,
			"changedFileCount": target.ChangedFileCount, "skippedFileCount": target.SkippedFileCount,
			"pushedAt": mcpNullableTime(target.PushedAt), "activatedAt": mcpNullableTime(target.ActivatedAt),
		})
	}
	return map[string]any{"items": items, "total": view.Total}
}

// mcpDeliveryImpactView 投影影响预览（对齐既有 ChangeImpactView：summary + targets 分页）。
func mcpDeliveryImpactView(view *service.ChangeImpactView) map[string]any {
	batches := make([]map[string]any, 0, len(view.Summary.Batches))
	for _, batch := range view.Summary.Batches {
		batches = append(batches, map[string]any{"batchNo": batch.BatchNo, "count": batch.Count})
	}
	rows := make([]map[string]any, 0, len(view.Targets.Items))
	for _, row := range view.Targets.Items {
		rows = append(rows, map[string]any{
			"serverId": row.ServerID, "online": row.Online, "level": row.Level,
			"addCount": row.AddCount, "updateCount": row.UpdateCount,
			"deleteCount": row.DeleteCount, "skipCount": row.SkipCount,
			"configScopes": mcpDeliveryImpactScopesView(row.ConfigScopes),
		})
	}
	return map[string]any{
		"summary": map[string]any{
			"targetTotal": view.Summary.TargetTotal, "batches": batches,
			"fileTotal": view.Summary.FileTotal, "totalBytes": view.Summary.TotalBytes,
			"transferBytes": view.Summary.TransferBytes, "configScopeCount": view.Summary.ConfigScopeCount,
			"snapshotAt": mcpNullableTime(view.Summary.SnapshotAt),
		},
		"targets": map[string]any{"items": rows, "total": view.Targets.Total},
	}
}

// mcpDeliveryImpactScopesView 投影逐目标命中的配置作用域（from→to 版本，键名沿既有视图）。
func mcpDeliveryImpactScopesView(scopes []service.ChangeImpactConfigScopeView) []map[string]any {
	views := make([]map[string]any, 0, len(scopes))
	for _, scope := range scopes {
		views = append(views, map[string]any{
			"scopeKind": scope.ScopeKind, "scopeId": scope.ScopeID,
			"fromVersionId": mcpDerefUint(scope.FromVersionID), "toVersionId": mcpDerefUint(scope.ToVersionID),
		})
	}
	return views
}

// mcpDeliveryObserveView 投影当前批观察窗序列（对齐既有 ChangeObserveView；数组恒非 null）。
func mcpDeliveryObserveView(view *service.ChangeObserveView) map[string]any {
	targets := make([]map[string]any, 0, len(view.Targets))
	for _, series := range view.Targets {
		points := make([]map[string]any, 0, len(series.Series))
		for _, point := range series.Series {
			points = append(points, map[string]any{
				"tsMs": point.TsMs, "score": point.Score, "level": point.Level,
				"tps": point.TPS, "alerts": point.Alerts,
			})
		}
		targets = append(targets, map[string]any{"serverId": series.ServerID, "series": points})
	}
	return map[string]any{
		"batchNo": mcpDerefInt(view.BatchNo), "observeStartedAt": mcpNullableTime(view.ObserveStartedAt),
		"targets": targets,
	}
}

// mcpDeliveryEventsView 投影进度事件（对齐既有 ChangeEventsView；不含 SSE 流式语义）。
func mcpDeliveryEventsView(view *service.ChangeEventsView) map[string]any {
	events := make([]map[string]any, 0, len(view.Events))
	for _, event := range view.Events {
		events = append(events, map[string]any{
			"seq": event.Seq, "at": event.At.UTC().Format(time.RFC3339), "type": event.Type,
			"orderId": event.OrderID, "batchNo": mcpDerefInt(event.BatchNo),
			"serverId": mcpDerefString(event.ServerID), "status": event.Status,
		})
	}
	return map[string]any{"events": events}
}

func mcpPageBounds(page, size, total int) (int, int) {
	page, size = normalizedMCPPage(page), normalizedMCPPageSize(size)
	start := (page - 1) * size
	if start >= total {
		return total, total
	}
	end := start + size
	if end > total {
		end = total
	}
	return start, end
}

func mcpParseTime(raw string) time.Time {
	value, _ := time.Parse(time.RFC3339, raw)
	return value
}

func (r *MCPToolRegistry) mcpObservationScope(in mcpScopeInput) (service.ObservationScope, bool) {
	if r == nil || r.reads.scope == nil {
		return service.ObservationScope{}, false
	}
	scope, err := r.reads.scope.Resolve(in.EnvID, in.NamespaceID)
	return scope, err == nil
}

func mcpNamespacesInScope(items []service.NamespaceStat, scope service.ObservationScope) []service.NamespaceStat {
	if scope.All {
		return items
	}
	filtered := make([]service.NamespaceStat, 0, len(items))
	for _, item := range items {
		if scope.Contains(item.Namespace.ID) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (r *MCPToolRegistry) mcpNamespaceVisible(code string, scope service.ObservationScope) bool {
	items, err := r.reads.v2.ListNamespacesWithStats()
	if err != nil {
		return false
	}
	for _, item := range items {
		if item.Namespace.Code == code {
			return scope.Contains(item.Namespace.ID)
		}
	}
	return false
}

func mcpTopologyView(topology service.Topology) map[string]any {
	nodes := make([]map[string]any, 0, len(topology.Nodes))
	for _, node := range topology.Nodes {
		nodes = append(nodes, map[string]any{"serverId": node.ServerID, "role": node.Role, "group": node.Group, "zone": node.Zone, "status": node.Status})
	}
	edges := make([]map[string]any, 0, len(topology.Edges))
	for _, edge := range topology.Edges {
		edges = append(edges, map[string]any{"source": edge.Source, "target": edge.Target})
	}
	groups := make([]map[string]any, 0, len(topology.Groups))
	for _, group := range topology.Groups {
		groups = append(groups, map[string]any{"group": group.Group, "zone": group.Zone, "members": group.Members})
	}
	return map[string]any{"namespace": topology.Namespace, "nodes": nodes, "edges": edges, "groups": groups}
}

// mcpResolveNamespaceID 把 namespace code 解析为 ID；两者都缺省时回落到 scope 内唯一 namespace。
func (r *MCPToolRegistry) mcpResolveNamespaceID(code, rawID string, scope service.ObservationScope) (uint, error) {
	if code == "" && rawID == "" {
		if scope.All || len(scope.NamespaceIDs) != 1 {
			return 0, errMCPNamespaceRequired
		}
		return scope.NamespaceIDs[0], nil
	}
	if rawID != "" {
		id, err := strconv.ParseUint(rawID, 10, 64)
		if err != nil {
			return 0, err
		}
		if !scope.All && !scope.Contains(uint(id)) {
			return 0, errMCPNamespaceDenied
		}
		return uint(id), nil
	}
	items, err := r.reads.v2.ListNamespacesWithStats()
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		if item.Namespace.Code == code {
			if !scope.Contains(item.Namespace.ID) {
				return 0, errMCPNamespaceDenied
			}
			return item.Namespace.ID, nil
		}
	}
	return 0, errMCPNamespaceDenied
}

// mcpZoneTreeView 投影区服结构树：只暴露结构、计数与默认入口，不含凭据类字段。
func mcpZoneTreeView(tree *service.ZoneTreeResponse) map[string]any {
	if tree == nil {
		return map[string]any{"namespaceId": 0, "unassignedCount": 0, "clusters": []any{}}
	}
	clusters := make([]map[string]any, 0, len(tree.Clusters))
	for _, c := range tree.Clusters {
		regions := make([]map[string]any, 0, len(c.Regions))
		for _, rg := range c.Regions {
			zones := make([]map[string]any, 0, len(rg.Zones))
			for _, z := range rg.Zones {
				zones = append(zones, map[string]any{
					"id": z.ID, "code": z.Code, "name": z.Name, "displayName": z.DisplayName,
					"description": z.Description,
					"serverCount": z.ServerCount, "defaultEntryCount": z.DefaultEntryCount,
				})
			}
			regions = append(regions, map[string]any{
				"id": rg.ID, "code": rg.Code, "name": rg.Name, "displayName": rg.DisplayName,
				"description": rg.Description, "zones": zones,
			})
		}
		clusters = append(clusters, map[string]any{
			"id": c.ID, "code": c.Code, "name": c.Name, "displayName": c.DisplayName,
			"description": c.Description, "proxyCount": c.ProxyCount, "regions": regions,
		})
	}
	return map[string]any{
		"namespaceId": tree.NamespaceID, "unassignedCount": tree.UnassignedCount, "clusters": clusters,
	}
}

// mcpServerListView 投影 server 富化视图（归属 / 默认入口 / 在线 / 排空 / 生命周期）。
func mcpServerListView(items []service.ServerView, total int64) map[string]any {
	views := make([]map[string]any, 0, len(items))
	for _, s := range items {
		views = append(views, map[string]any{
			"id": s.ID, "serverId": s.ServerID, "displayName": s.DisplayName, "kind": s.Kind,
			"namespaceId":     s.NamespaceID,
			"bcClusterId":     mcpDerefUint(s.BCClusterID),
			"bcClusterName":   mcpDerefString(s.BCClusterName),
			"zoneId":          mcpDerefUint(s.ZoneID),
			"zoneName":        mcpDerefString(s.ZoneName),
			"regionName":      mcpDerefString(s.RegionName),
			"isDefaultEntry":  s.IsDefaultEntry,
			"draining":        s.Draining,
			"lifecycle":       s.Lifecycle,
			"lifecycleStatus": s.LifecycleStatus,
			"effectiveActive": s.EffectiveActive,
			"online":          s.Online,
			"assigned":        s.Assigned,
			"createdAt":       s.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return map[string]any{"items": views, "total": total}
}

func mcpDerefUint(v *uint) any {
	if v == nil {
		return nil
	}
	return *v
}

func mcpDerefString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func mcpDerefInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func mcpMessageHistoryView(page service.MsgPage) map[string]any {
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, map[string]any{
			"messageId": item.MessageID, "namespaceId": item.NamespaceID, "sourceServerId": item.SourceServerID,
			"msgType": item.MsgType, "targetKind": item.TargetKind, "targetServerId": item.TargetServerID,
			"resolvedServerId": item.ResolvedServerID, "status": item.Status, "failReason": item.FailReason,
			"createdAt": item.CreatedAt.UTC().Format(time.RFC3339), "durationMs": item.DurationMs,
			"payloadSize": item.PayloadSize, "payloadStored": item.PayloadStored,
		})
	}
	return map[string]any{"items": items, "nextCursor": page.NextCursor}
}

func mcpCommandHistoryView(items []repository.CommandMeta, total int64) map[string]any {
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		views = append(views, map[string]any{"commandId": item.ID, "namespace": item.NamespaceCode, "serverId": item.ServerID, "type": item.Type, "status": item.Status, "createdAt": item.CreatedAt.UTC().Format(time.RFC3339), "updatedAt": item.UpdatedAt.UTC().Format(time.RFC3339)})
	}
	return map[string]any{"items": views, "total": total}
}

func mcpSchedulingHistoryView(items []model.SchedDecisionV2, total int64) map[string]any {
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		views = append(views, map[string]any{"traceId": item.TraceID, "tsMs": item.TsMs, "namespaceId": item.NamespaceID, "requesterServerId": item.RequesterServerID, "zoneName": item.ZoneName, "strategy": item.Strategy, "source": item.Source, "candidateCount": item.CandidateCount, "chosenServerId": item.ChosenServerID, "chosenScore": item.ChosenScore, "failReason": item.FailReason, "durationMs": item.DurationMs})
	}
	return map[string]any{"items": views, "total": total}
}

func mcpAuditHistoryView(items []model.AuditLog, total int64) map[string]any {
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		views = append(views, map[string]any{"id": item.ID, "namespace": item.NamespaceCode, "operator": item.Operator, "action": item.Action, "targetType": item.TargetType, "targetRef": item.TargetRef, "result": item.Result, "createdAt": item.CreatedAt.UTC().Format(time.RFC3339)})
	}
	return map[string]any{"items": views, "total": total}
}

// mcpAlertEventHistoryView 投影告警事件摘要（处理状态 / 收敛计数 / 处理留痕 / 人工改级标记）。
//
// **绝不透传 detail 字段**：该列是结构化 json 文本，含状态前后与实例地址等敏感上下文——
// 与审计 detail、消息 payload 同属「只给元数据、不给正文」的既有只读工具硬约束。
// 人读摘要（message）与收敛计数足以支撑运维定位，无正文需求。
func mcpAlertEventHistoryView(items []model.AlertEvent, total int64) map[string]any {
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		views = append(views, map[string]any{
			"id": item.ID, "type": item.Type, "level": item.Level, "status": item.Status,
			"serverId": item.ServerID, "namespace": item.Namespace, "message": item.Message,
			"createdAt":        item.CreatedAt.UTC().Format(time.RFC3339),
			"lastAt":           mcpNullableTime(item.LastAt),
			"occurrenceCount":  item.OccurrenceCount,
			"handledBy":        mcpNullableString(item.HandledBy),
			"handledAt":        mcpNullableTime(item.HandledAt),
			"handleNote":       mcpNullableString(item.HandleNote),
			"severityOverride": mcpNullableString(item.SeverityOverride),
		})
	}
	return map[string]any{"items": views, "total": total}
}

// mcpNullableString 把空串映射为 null（对齐契约 string | null）。
func mcpNullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// mcpNullableTime 把 nil 映射为 null，其余按 RFC3339（UTC）输出（对齐契约 string | null）。
func mcpNullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339)
}
