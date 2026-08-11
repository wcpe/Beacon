package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// AgentLogHandler 处理取 agent 自身日志的「命令-回传」端点（FR-88，见 ADR-0040）：
// admin 触发（POST /admin/v1/instances/{serverId}/logs）+ admin 查询（GET 同址）+ agent 回传（POST /beacon/v1/agent/logs）。
// 严守边界：只取 agent 自身脱敏日志、瞬态不入真源 / 不进审计 detail；admin 经 full 角色鉴权 + 限速。
type AgentLogHandler struct {
	svc     *service.AgentLogService
	instSvc *service.InstanceService
}

// NewAgentLogHandler 构造处理器（instSvc 供触发前校验目标在线——离线 agent 收不到命令）。
func NewAgentLogHandler(svc *service.AgentLogService, instSvc *service.InstanceService) *AgentLogHandler {
	return &AgentLogHandler{svc: svc, instSvc: instSvc}
}

// agentLogLineView 是单行日志对外视图（级别 + 已脱敏文本）。
type agentLogLineView struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// agentLogView 是取日志命令对外视图（命令 id + 状态 + 若 done 则附脱敏日志行）。
type agentLogView struct {
	CommandID uint               `json:"commandId"`
	Status    string             `json:"status"`
	Lines     []agentLogLineView `json:"lines"`
}

type consumeAgentLogRequest struct {
	CommandID uint `json:"commandId"`
}

// Request 创建实时日志审批申请；批准后才由 worker 下发命令。
func (h *AgentLogHandler) Request(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		render.WriteError(w, r, apperr.ErrOperationRequiresApproval)
		return
	}
	serverID := chi.URLParam(r, "serverId")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	// 校验目标在线：不在注册表即 INSTANCE_NOT_FOUND，不建命令。
	if _, err := h.instSvc.Get(ns, serverID); err != nil {
		render.WriteError(w, r, err)
		return
	}
	ticket, err := h.svc.RequestTailLogsApproval(ns, serverID, decodeReason(r), r.Header.Get("Idempotency-Key"), auth.Operator(r.Context()), clientIP(r), requestPrincipal(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// Get 是会返回实时日志正文的旧入口；未持 grant 时固定失败关闭。
func (h *AgentLogHandler) Get(w http.ResponseWriter, r *http.Request) {
	render.WriteError(w, r, apperr.ErrOperationRequiresApproval)
}

// ConsumeApproved 仅允许审批原申请主体一次消费 Agent 已回传的日志正文。
func (h *AgentLogHandler) ConsumeApproved(w http.ResponseWriter, r *http.Request) {
	grantID := chi.URLParam(r, "grantId")
	var req consumeAgentLogRequest
	if grantID == "" || json.NewDecoder(r.Body).Decode(&req) != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	result, err := h.svc.ConsumeApprovedLogs(grantID, req.CommandID, requestPrincipal(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	lines := make([]agentLogLineView, len(result.Lines))
	for i, line := range result.Lines {
		lines[i] = agentLogLineView{Level: line.Level, Text: line.Text}
	}
	render.WriteJSON(w, http.StatusOK, agentLogView{CommandID: result.CommandID, Status: result.Status, Lines: lines})
}

// uploadLogsRequest 是 agent 回传自身日志快照的请求体。
type uploadLogsRequest struct {
	CommandID uint `json:"commandId"`
	Lines     []struct {
		Level string `json:"level"`
		Text  string `json:"text"`
	} `json:"lines"`
}

// Receive 处理 POST /beacon/v1/agent/logs（FR-88）：接收 agent 回传的自身脱敏日志快照，转存为命令瞬态并 done。
// 与其它 agent 端点同属 agentToken 防误连信任面。日志已在 agent 侧脱敏，控制面只转存、不处理原文。
func (h *AgentLogHandler) Receive(w http.ResponseWriter, r *http.Request) {
	var req uploadLogsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	lines := make([]service.AgentLogLine, len(req.Lines))
	for i, l := range req.Lines {
		lines[i] = service.AgentLogLine{Level: l.Level, Text: l.Text}
	}
	if err := h.svc.ReceiveLogs(req.CommandID, lines, clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
