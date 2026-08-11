package server

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
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
	scope       *service.ObservationScopeResolver
}

// NewMCPReadServices 构造 MCP 只读服务集合；仅允许调用方传入应用查询服务。
func NewMCPReadServices(v2 *service.V2ControlPlaneService, topology *service.TopologyService, health *service.HealthQueryService, messages *service.MessageQueryService, connections *service.ConnQueryService, commands *service.CommandObserveService, scheduling *service.SchedDecisionQueryService, audits *service.AuditService, scope *service.ObservationScopeResolver) MCPReadServices {
	return MCPReadServices{v2: v2, topology: topology, health: health, messages: messages, connections: connections, commands: commands, scheduling: scheduling, audits: audits, scope: scope}
}

var mcpReadToolNames = []string{
	"beacon.metadata.namespaces.list",
	"beacon.topology.snapshot.get",
	"beacon.metrics.health.list",
	"beacon.metrics.summary.get",
	"beacon.metrics.series.query",
	"beacon.history.messages.list",
	"beacon.history.connections.stats",
	"beacon.history.commands.list",
	"beacon.history.scheduling-decisions.list",
	"beacon.audit.events.list",
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

// registerReadTools 登记 observer 与 automation 共用的显式只读工具。所有返回值在此投影，绝不透传敏感实体。
func (r *MCPToolRegistry) registerReadTools(server *mcp.Server) {
	if r == nil {
		return
	}
	if r.reads.v2 != nil {
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.metadata.namespaces.list", Description: "分页读取 namespace 元数据摘要"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpPageInput) (*mcp.CallToolResult, map[string]any, error) {
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
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.topology.snapshot.get", Description: "读取指定 namespace 的在线拓扑摘要"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpTopologyInput) (*mcp.CallToolResult, map[string]any, error) {
			scope, ok := r.mcpObservationScope(in.mcpScopeInput)
			if !ok || in.Namespace == "" || !r.mcpNamespaceVisible(in.Namespace, scope) {
				return mcpRejectedResult()
			}
			topology := r.reads.topology.Build(in.Namespace)
			return &mcp.CallToolResult{}, mcpTopologyView(topology), nil
		})
	}
	r.registerReadHealthTools(server)
	if r.reads.messages != nil {
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.history.messages.list", Description: "读取消息元数据历史，不含正文与玩家标识"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpMessageHistoryInput) (*mcp.CallToolResult, map[string]any, error) {
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
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.history.connections.stats", Description: "读取连接历史聚合，不含玩家、地址或单连接明细"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpConnectionStatsInput) (*mcp.CallToolResult, map[string]any, error) {
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
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.history.commands.list", Description: "分页读取 Agent 命令历史元数据，不含结果正文"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpCommandHistoryInput) (*mcp.CallToolResult, map[string]any, error) {
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
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.history.scheduling-decisions.list", Description: "分页读取调度决策历史摘要，不含候选排除明细"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpSchedulingHistoryInput) (*mcp.CallToolResult, map[string]any, error) {
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
		mcp.AddTool(server, &mcp.Tool{Name: "beacon.audit.events.list", Description: "分页读取脱敏审计事件摘要"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpAuditListInput) (*mcp.CallToolResult, map[string]any, error) {
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
}

func (r *MCPToolRegistry) registerReadHealthTools(server *mcp.Server) {
	if r.reads.health == nil {
		return
	}
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.metrics.health.list", Description: "分页读取实时健康摘要"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpHealthListInput) (*mcp.CallToolResult, map[string]any, error) {
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
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.metrics.summary.get", Description: "读取集群实时指标摘要"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpScopeInput) (*mcp.CallToolResult, map[string]any, error) {
		scope, ok := r.mcpObservationScope(in)
		if !ok {
			return mcpRejectedResult()
		}
		return &mcp.CallToolResult{}, map[string]any{"summary": r.reads.health.MetricsSummaryInScope(scope)}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "beacon.metrics.series.query", Description: "读取指定服务器的有界指标时序"}, func(_ context.Context, _ *mcp.CallToolRequest, in mcpMetricsSeriesInput) (*mcp.CallToolResult, map[string]any, error) {
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
