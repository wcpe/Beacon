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
// 严守边界：只取 agent 自身脱敏日志、瞬态不入真源 / 不进审计 detail；读取由 management.read 能力控制并写元数据审计。
type AgentLogHandler struct {
	svc     *service.AgentLogService
	instSvc *service.InstanceService
	// reportAuth 解析回传 agent 的权威身份，用于把命令绑定到发起它的那台 agent。
	reportAuth agentReportAuthenticator
}

// NewAgentLogHandler 构造处理器（instSvc 供触发前校验目标在线——离线 agent 收不到命令）。
func NewAgentLogHandler(svc *service.AgentLogService, instSvc *service.InstanceService) *AgentLogHandler {
	return &AgentLogHandler{svc: svc, instSvc: instSvc}
}

// SetReportAuthenticator 注入日志回传的 v2 权威身份校验器。
func (h *AgentLogHandler) SetReportAuthenticator(authn agentReportAuthenticator) {
	h.reportAuth = authn
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

// Request 直接创建日志读取命令；命令创建与日志读取均由 service 写专项审计。
func (h *AgentLogHandler) Request(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil || h.instSvc == nil {
		render.WriteError(w, r, apperr.ErrInternal)
		return
	}
	serverID := chi.URLParam(r, "serverId")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if _, err := h.instSvc.Get(ns, serverID); err != nil {
		render.WriteError(w, r, err)
		return
	}
	command, err := h.svc.RequestTailLogs(ns, serverID, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, map[string]any{"commandId": command.ID, "status": command.Status})
}

// Get 读取最近一次日志结果，service 审计只记录命令状态与行数。
func (h *AgentLogHandler) Get(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		render.WriteError(w, r, apperr.ErrInternal)
		return
	}
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	result, err := h.svc.ReadLatest(ns, chi.URLParam(r, "serverId"), auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	writeAgentLogView(w, result)
}

// ConsumeApproved 保留旧 grant 路径作为兼容定位器，但读取授权改由 management.read 判定。
// namespace 为可选 query 参数：显式给出时按 (serverId, namespace) 联合定位，缺省由 service 反查，
// 反查出现跨环境同名歧义时按不存在拒绝。
func (h *AgentLogHandler) ConsumeApproved(w http.ResponseWriter, r *http.Request) {
	var req consumeAgentLogRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.CommandID == 0 {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	result, err := h.svc.ReadCommand(r.URL.Query().Get("namespace"), chi.URLParam(r, "serverId"), req.CommandID, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	writeAgentLogView(w, result)
}

func writeAgentLogView(w http.ResponseWriter, result *service.AgentLogResult) {
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
	if h == nil || h.svc == nil {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	// 必须确认回传者就是该命令的目标 agent，否则任一 agent 凭猜测的 commandID 即可覆盖他人结果。
	identity, ok := authenticateAgentReport(w, r, h.reportAuth)
	if !ok {
		return
	}
	lines := make([]service.AgentLogLine, len(req.Lines))
	for i, l := range req.Lines {
		lines[i] = service.AgentLogLine{Level: l.Level, Text: l.Text}
	}
	if err := h.svc.ReceiveLogs(identity, req.CommandID, lines, clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
