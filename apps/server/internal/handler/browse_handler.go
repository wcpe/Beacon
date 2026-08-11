package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// BrowseHandler 编排需审批的 Agent 文件浏览：创建申请、接收回传、一次消费结果。
type BrowseHandler struct {
	svc     *service.AgentCommandService
	instSvc *service.InstanceService
	reportAuth browseReportAuthenticator
}

type browseReportAuthenticator interface {
	AuthenticateAgentReport(token, identityID, bootID, addr string) (agentauth.Identity, error)
}

// NewBrowseHandler 构造处理器（instSvc 供浏览前校验目标在线）。
func NewBrowseHandler(svc *service.AgentCommandService, instSvc *service.InstanceService) *BrowseHandler {
	return &BrowseHandler{svc: svc, instSvc: instSvc}
}

// SetReportAuthenticator 注入浏览回传的 v2 权威身份校验器。
func (h *BrowseHandler) SetReportAuthenticator(authn browseReportAuthenticator) { h.reportAuth = authn }

// Browse 是旧浏览入口，固定失败关闭，不能绕过审批直接读取结果。
func (h *BrowseHandler) Browse(w http.ResponseWriter, r *http.Request) {
	render.WriteError(w, r, apperr.ErrOperationRequiresApproval)
}

type browseApprovalRequest struct {
	Namespace string `json:"namespace"`
	Op        string `json:"op"`
	Path      string `json:"path"`
	Offset    int    `json:"offset"`
	Limit     int    `json:"limit"`
	MaxDepth  int    `json:"maxDepth"`
	Reason    string `json:"reason"`
}

type consumeBrowseRequest struct {
	CommandID uint `json:"commandId"`
}

// Request 创建文件浏览审批申请；批准 worker 才能下发 fs-browse 命令。
func (h *BrowseHandler) Request(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.svc == nil || h.instSvc == nil {
		render.WriteError(w, r, apperr.ErrOperationRequiresApproval)
		return
	}
	var req browseApprovalRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	serverID := chi.URLParam(r, "serverId")
	if _, err := h.instSvc.Get(req.Namespace, serverID); err != nil {
		render.WriteError(w, r, err)
		return
	}
	ticket, err := h.svc.RequestBrowseApproval(service.BrowseParams{Namespace: req.Namespace, ServerID: serverID, Op: req.Op, Path: req.Path, Offset: req.Offset, Limit: req.Limit, MaxDepth: req.MaxDepth, Operator: auth.Operator(r.Context()), ClientIP: clientIP(r)}, req.Reason, r.Header.Get("Idempotency-Key"), requestPrincipal(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// ConsumeApproved 仅允许原申请主体一次性消费已由 Agent 回传的浏览结果。
func (h *BrowseHandler) ConsumeApproved(w http.ResponseWriter, r *http.Request) {
	var req consumeBrowseRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.CommandID == 0 {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	result, err := h.svc.ConsumeApprovedBrowse(chi.URLParam(r, "grantId"), req.CommandID, requestPrincipal(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, json.RawMessage(result))
}

// browseResultRequest 是 Agent 回传文件浏览结果的请求体。
// namespace/serverId 仅保留协议兼容，归属一律以权威 v2 身份为准；失败 reason 不持久化或回显。
type browseResultRequest struct {
	Namespace string          `json:"namespace"`
	ServerID  string          `json:"serverId"`
	CommandID uint            `json:"commandId"`
	OK        bool            `json:"ok"`
	Result    json.RawMessage `json:"result"`
	Reason    string          `json:"reason"`
}

// BrowseResult 处理 POST /beacon/v1/agent/files/browse-result：接收 Agent 回传，
// 原子推进命令与 grant 状态。与其它 Agent 端点同属 agentToken 防误连信任面。
func (h *BrowseHandler) BrowseResult(w http.ResponseWriter, r *http.Request) {
	var req browseResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if h == nil || h.svc == nil || h.reportAuth == nil {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	identity, err := h.reportAuth.AuthenticateAgentReport(r.Header.Get("X-Beacon-Token"), r.Header.Get("X-Beacon-Identity"), r.Header.Get("X-Beacon-Boot"), r.RemoteAddr)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if err := h.svc.ReceiveBrowseResult(identity, req.CommandID, req.OK,
		string(req.Result), req.Reason); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
