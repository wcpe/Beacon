package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// BrowseHandler 编排受限 Agent 文件浏览：创建命令、接收回传、一次读取结果。
type BrowseHandler struct {
	svc        *service.AgentCommandService
	instSvc    *service.InstanceService
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

// Browse 直接创建浏览命令；带 commandId 时读取该命令的一次性结果。
func (h *BrowseHandler) Browse(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if rawID := q.Get("commandId"); rawID != "" {
		commandID, err := strconv.ParseUint(rawID, 10, 0)
		if err != nil || commandID == 0 {
			render.WriteError(w, r, apperr.ErrInvalidParam)
			return
		}
		if h == nil || h.svc == nil {
			render.WriteError(w, r, apperr.ErrInternal)
			return
		}
		result, err := h.svc.ReadBrowseResult(uint(commandID), chi.URLParam(r, "serverId"), auth.Operator(r.Context()), clientIP(r))
		if err != nil {
			render.WriteError(w, r, err)
			return
		}
		render.WriteJSON(w, http.StatusOK, json.RawMessage(result))
		return
	}
	p := service.BrowseParams{
		Namespace: q.Get("namespace"), ServerID: chi.URLParam(r, "serverId"), Op: q.Get("op"),
		Path: q.Get("path"), Offset: intQuery(q.Get("offset")), Limit: intQuery(q.Get("limit")), MaxDepth: intQuery(q.Get("maxDepth")),
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	}
	h.startBrowse(w, r, p)
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

// Request 直接创建受限浏览命令并返回 commandId。
func (h *BrowseHandler) Request(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.svc == nil || h.instSvc == nil {
		render.WriteError(w, r, apperr.ErrInternal)
		return
	}
	var req browseApprovalRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	serverID := chi.URLParam(r, "serverId")
	h.startBrowse(w, r, service.BrowseParams{
		Namespace: req.Namespace, ServerID: serverID, Op: req.Op, Path: req.Path,
		Offset: req.Offset, Limit: req.Limit, MaxDepth: req.MaxDepth,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
}

func (h *BrowseHandler) startBrowse(w http.ResponseWriter, r *http.Request, params service.BrowseParams) {
	if strings.TrimSpace(params.Namespace) == "" || strings.TrimSpace(params.ServerID) == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if h == nil || h.svc == nil || h.instSvc == nil {
		render.WriteError(w, r, apperr.ErrInternal)
		return
	}
	if _, err := h.instSvc.Get(params.Namespace, params.ServerID); err != nil {
		render.WriteError(w, r, err)
		return
	}
	commandID, err := h.svc.RequestBrowse(r.Context(), params)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, map[string]any{"commandId": commandID, "status": "pending"})
}

// ConsumeApproved 保留旧 grant 路径作为兼容定位器，读取权限由 management.read 能力控制。
func (h *BrowseHandler) ConsumeApproved(w http.ResponseWriter, r *http.Request) {
	var req consumeBrowseRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.CommandID == 0 {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if h == nil || h.svc == nil {
		render.WriteError(w, r, apperr.ErrInternal)
		return
	}
	result, err := h.svc.ReadBrowseResult(req.CommandID, chi.URLParam(r, "serverId"), auth.Operator(r.Context()), clientIP(r))
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
