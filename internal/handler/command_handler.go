package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"beacon/internal/apperr"
	"beacon/internal/auth"
	"beacon/internal/model"
	"beacon/internal/render"
	"beacon/internal/service"
)

// CommandHandler 处理 server→agent 命令（FR-39，见 ADR-0027）：
// admin 触发反向抓取 + agent 拉待办命令 + agent 回传 ingest 结果。
type CommandHandler struct {
	svc     *service.AgentCommandService
	instSvc *service.InstanceService
}

// NewCommandHandler 构造处理器（instSvc 供反向抓取前校验目标在线）。
func NewCommandHandler(svc *service.AgentCommandService, instSvc *service.InstanceService) *CommandHandler {
	return &CommandHandler{svc: svc, instSvc: instSvc}
}

// commandView 是命令对外视图（不含 payload / 结果细节，对齐前端 AgentCommandView）。
type commandView struct {
	ID        uint   `json:"id"`
	Namespace string `json:"namespace"`
	ServerID  string `json:"serverId"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

func toCommandView(c *model.AgentCommand) commandView {
	return commandView{
		ID: c.ID, Namespace: c.NamespaceCode, ServerID: c.ServerID,
		Type: c.Type, Status: c.Status,
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// reverseFetchRequest 是 admin 触发反向抓取的请求体（scope=group 只需 group；scope=server 需 group + target）。
// namespace 走查询参数（与 /instances/{serverId} 其他端点一致），不在请求体重复。
type reverseFetchRequest struct {
	Scope  string `json:"scope"`
	Group  string `json:"group"`
	Target string `json:"target"`
}

// ReverseFetch 处理 POST /admin/v1/instances/{serverId}/reverse-fetch?namespace=（FR-39）：
// 先校验目标在线（实例须在注册表中——admin 从在线列表选取，离线 agent 收不到命令），
// 再建 pending 命令 + 唤醒该 agent + 审计。返回已创建命令（202）。
func (h *CommandHandler) ReverseFetch(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverId")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	var req reverseFetchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	// 校验目标在线（spec §3.1）：不在注册表即 INSTANCE_NOT_FOUND，不建命令。
	if _, err := h.instSvc.Get(ns, serverID); err != nil {
		render.WriteError(w, r, err)
		return
	}
	cmd, err := h.svc.RequestReverseFetch(ns, serverID, req.Scope, req.Group, req.Target,
		auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, toCommandView(cmd))
}

// agentCommandResponse 是 agent 拉待办命令的响应（含执行参考载荷；ingest 落点由控制面 ReceiveIngest 据库内载荷定）。
type agentCommandResponse struct {
	ID      uint              `json:"id"`
	Type    string            `json:"type"`
	Payload map[string]string `json:"payload"`
}

// Pending 处理 GET /beacon/v1/agent/commands（FR-39）：返回该 agent 最早 pending 命令并 CAS 标 fetched；无则 204。
func (h *CommandHandler) Pending(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns := q.Get("namespace")
	serverID := q.Get("serverId")
	if ns == "" || serverID == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	cmd, err := h.svc.FetchPending(ns, serverID)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if cmd == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var payload map[string]string
	_ = json.Unmarshal([]byte(cmd.Payload), &payload)
	render.WriteJSON(w, http.StatusOK, agentCommandResponse{ID: cmd.ID, Type: cmd.Type, Payload: payload})
}

// ingestRequestFile 是回传文件集的单项。
type ingestRequestFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ingestRequest 是 agent 回传 ingest 的请求体。
type ingestRequest struct {
	CommandID uint                `json:"commandId"`
	Files     []ingestRequestFile `json:"files"`
}

// Ingest 处理 POST /beacon/v1/agent/files/ingest（FR-39）：接收 agent 回传文件集，
// 控制面再校验（上限 / 排除 jar / path）+ 复用 Import 落组 / 单服覆盖；命令转 done / failed。
func (h *CommandHandler) Ingest(w http.ResponseWriter, r *http.Request) {
	var req ingestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	files := make([]service.ImportFile, len(req.Files))
	for i, f := range req.Files {
		files[i] = service.ImportFile{Path: f.Path, Content: f.Content}
	}
	res, err := h.svc.ReceiveIngest(req.CommandID, files, clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"created": res.Created, "updated": res.Updated})
}
