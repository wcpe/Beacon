package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/auth"
	"github.com/wcpe/Beacon/internal/render"
	"github.com/wcpe/Beacon/internal/service"
)

// BrowseHandler 代理 agent 只读文件浏览（FR-110，见 ADR-0049 决策 9）：
// admin 触发浏览（经命令生命周期下发 + 等待回传）+ agent 回传浏览结果。
type BrowseHandler struct {
	svc     *service.AgentCommandService
	instSvc *service.InstanceService
}

// NewBrowseHandler 构造处理器（instSvc 供浏览前校验目标在线）。
func NewBrowseHandler(svc *service.AgentCommandService, instSvc *service.InstanceService) *BrowseHandler {
	return &BrowseHandler{svc: svc, instSvc: instSvc}
}

// Browse 处理 GET /admin/v1/instances/{serverId}/browse?namespace=&op=&path=&offset=&limit=&maxDepth=（FR-110）：
// 先校验目标在线（离线 agent 收不到命令），再经命令生命周期下发浏览命令、阻塞等待 agent 回传，
// 把结果 JSON 原文代理给前端（200）。op 非法 → 400；目标不存在 / 不可读 → 404；超时 → 504。
//
// 触发浏览有写副作用（建命令 / 唤醒 agent / 入审计），故路由用 requireFullRole 守卫挡 readonly（403）。
func (h *BrowseHandler) Browse(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverId")
	q := r.URL.Query()
	ns := q.Get("namespace")
	if ns == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	// 校验目标在线：不在注册表即 INSTANCE_NOT_FOUND，不建命令。
	if _, err := h.instSvc.Get(ns, serverID); err != nil {
		render.WriteError(w, r, err)
		return
	}
	result, err := h.svc.RequestBrowse(r.Context(), service.BrowseParams{
		Namespace: ns, ServerID: serverID,
		Op:       q.Get("op"),
		Path:     q.Get("path"),
		Offset:   atoiOr(q.Get("offset"), 0),
		Limit:    atoiOr(q.Get("limit"), 0),
		MaxDepth: atoiOr(q.Get("maxDepth"), 0),
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	// 结果是 agent 回传的浏览结果 JSON 原文（目录清单 / 子树 / 文件内容），逐字透传给前端。
	if result == "" {
		result = "{}"
	}
	render.WriteJSON(w, http.StatusOK, json.RawMessage(result))
}

// browseResultRequest 是 agent 回传文件浏览结果的请求体（FR-110）：
// ok=true 携 result（浏览结果 JSON 原文）；ok=false 携 reason（越权 / 非目录 / 非文本等，无敏感内容）。
type browseResultRequest struct {
	Namespace string          `json:"namespace"`
	ServerID  string          `json:"serverId"`
	CommandID uint            `json:"commandId"`
	OK        bool            `json:"ok"`
	Result    json.RawMessage `json:"result"`
	Reason    string          `json:"reason"`
}

// BrowseResult 处理 POST /beacon/v1/agent/files/browse-result（FR-110）：接收 agent 回传的浏览结果，
// CAS 推进命令 done / failed 并唤醒等待中的 admin。与其它 agent 端点同属 agentToken 防误连信任面。
func (h *BrowseHandler) BrowseResult(w http.ResponseWriter, r *http.Request) {
	var req browseResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if err := h.svc.ReceiveBrowseResult(req.Namespace, req.ServerID, req.CommandID, req.OK,
		string(req.Result), req.Reason); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// atoiOr 解析十进制整数，非法 / 空串回退到 fallback（浏览分页 / 深度参数缺省由 agent 收口到硬上限）。
func atoiOr(s string, fallback int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return fallback
}
