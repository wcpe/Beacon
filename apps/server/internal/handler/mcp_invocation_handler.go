package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// MCPInvocationHandler 处理 MCP 工具调用流水的管理面查询端点（FR-240，spec §3.7）：
// 列表（六维过滤 + 游标分页）与详情（invocationId 直定日表）。
//
// 与其它只读 GET 端点一致：readonly 角色可读、不新增能力点、查询行为自身不落审计（GET 不写库）。
// 刻意**不接** ObservationScope：MCP 主体是全局机器身份（MCPOAuthClient 无 namespace 归属），
// 流水行不可能有权威 namespace（spec §2.2 第 7 条）。
type MCPInvocationHandler struct {
	svc *service.MCPInvocationService
}

// NewMCPInvocationHandler 构造处理器。
func NewMCPInvocationHandler(svc *service.MCPInvocationService) *MCPInvocationHandler {
	return &MCPInvocationHandler{svc: svc}
}

// mcpInvocationItemJS 是流水列表项 / 详情（键对齐 contracts MCPInvocationItem，camelCase）。
// 空串字段原样返回 ""，不引入 null 分支（spec §3.7）。
type mcpInvocationItemJS struct {
	InvocationID string `json:"invocationId"`
	ClientID     string `json:"clientId"`
	Profile      string `json:"profile"`
	ToolName     string `json:"toolName"`
	RiskLevel    string `json:"riskLevel"`
	Result       string `json:"result"`
	Reason       string `json:"reason"`
	TargetDigest string `json:"targetDigest"`
	ArgKeys      string `json:"argKeys"`
	ArgBytes     int    `json:"argBytes"`
	DurationMs   int    `json:"durationMs"`
	TraceID      string `json:"traceId"`
	ClientIP     string `json:"clientIp"`
	ErrorSummary string `json:"errorSummary"`
	CreatedAt    string `json:"createdAt"`
}

// List 处理 GET /admin/v2/mcp/invocations：按工具 / clientId / 结果 / 风险等级 / 原因 / 时间过滤并游标分页。
func (h *MCPInvocationHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, err := h.svc.List(service.MCPInvocationListParams{
		ToolName:  q.Get("tool"),
		ClientID:  q.Get("clientId"),
		Result:    q.Get("result"),
		RiskLevel: q.Get("riskLevel"),
		Reason:    q.Get("reason"),
		FromMs:    parseISOms(q.Get("from")),
		ToMs:      parseISOms(q.Get("to")),
		Cursor:    intQuery(q.Get("cursor")),
		Limit:     intQuery(q.Get("limit")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items := make([]mcpInvocationItemJS, 0, len(page.Items))
	for i := range page.Items {
		items = append(items, mcpInvocationItem(&page.Items[i]))
	}
	// nextCursor 为空串表示末页（与响应形状一致，不走 nullableStr）。
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": page.NextCursor})
}

// Detail 处理 GET /admin/v2/mcp/invocations/{invocationId}：单条流水，未命中 / 非法 ID 一律 404 mcp_invocation_not_found。
func (h *MCPInvocationHandler) Detail(w http.ResponseWriter, r *http.Request) {
	row, err := h.svc.Detail(chi.URLParam(r, "invocationId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, mcpInvocationItem(&row))
}

// mcpInvocationItem 把流水行映射为对外列表项：字段与 §3.2 一一对应，createdAt 取 RFC3339 毫秒精度 UTC。
func mcpInvocationItem(row *model.MCPInvocation) mcpInvocationItemJS {
	return mcpInvocationItemJS{
		InvocationID: row.InvocationID,
		ClientID:     row.ClientID,
		Profile:      row.Profile,
		ToolName:     row.ToolName,
		RiskLevel:    row.RiskLevel,
		Result:       row.Result,
		Reason:       row.Reason,
		TargetDigest: row.TargetDigest,
		ArgKeys:      row.ArgKeys,
		ArgBytes:     row.ArgBytes,
		DurationMs:   row.DurationMs,
		TraceID:      row.TraceID,
		ClientIP:     row.ClientIP,
		ErrorSummary: row.ErrorSummary,
		CreatedAt:    row.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
}
