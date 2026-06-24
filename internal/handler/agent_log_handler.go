package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/auth"
	"github.com/wcpe/Beacon/internal/render"
	"github.com/wcpe/Beacon/internal/service"
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

// Request 处理 POST /admin/v1/instances/{serverId}/logs?namespace=（FR-88）：
// 先校验目标在线（不在注册表即 INSTANCE_NOT_FOUND，不建命令），再单活跃限速 + 建 pending tail-logs 命令 + 唤醒 + 审计。
// 返回已创建命令（202）；前端据返回 commandId 轮询 Get 取结果。
func (h *AgentLogHandler) Request(w http.ResponseWriter, r *http.Request) {
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
	cmd, err := h.svc.RequestTailLogs(ns, serverID, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, agentLogView{CommandID: cmd.ID, Status: cmd.Status, Lines: []agentLogLineView{}})
}

// Get 处理 GET /admin/v1/instances/{serverId}/logs?namespace=（FR-88）：
// 取该实例最近一条取日志命令的状态 + 日志行（done 则附脱敏日志；进行中 / 失败 lines 为空）。
// 从无取日志命令 → 204（前端据此显示「点按钮拉取」）。
func (h *AgentLogHandler) Get(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverId")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	res, err := h.svc.GetLatest(ns, serverID)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if res == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	lines := make([]agentLogLineView, len(res.Lines))
	for i, l := range res.Lines {
		lines[i] = agentLogLineView{Level: l.Level, Text: l.Text}
	}
	render.WriteJSON(w, http.StatusOK, agentLogView{CommandID: res.CommandID, Status: res.Status, Lines: lines})
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
