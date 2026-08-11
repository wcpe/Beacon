package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/merge"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// CommandHandler 处理 server→agent 命令（FR-39，见 ADR-0027）：
// admin 触发反向抓取 + agent 拉待办命令 + agent 回传 ingest 结果。
type CommandHandler struct {
	svc        *service.AgentCommandService
	instSvc    *service.InstanceService
	reportAuth commandReportAuthenticator
}

type commandReportAuthenticator interface {
	AuthenticateAgentReport(token, identityID, bootID, addr string) (agentauth.Identity, error)
	ReceiveDirectoryResyncResult(identity agentauth.Identity, commandID uint, ok bool, reason string) error
}

// NewCommandHandler 构造处理器（instSvc 供反向抓取前校验目标在线）。
func NewCommandHandler(svc *service.AgentCommandService, instSvc *service.InstanceService) *CommandHandler {
	return &CommandHandler{svc: svc, instSvc: instSvc}
}

// SetReportAuthenticator 注入 v2 目录重同步回执的权威身份校验器。
func (h *CommandHandler) SetReportAuthenticator(authn commandReportAuthenticator) {
	h.reportAuth = authn
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

// ReverseFetch 创建反向抓取扫描审批申请；批准后才由 worker 下发命令。
func (h *CommandHandler) ReverseFetch(w http.ResponseWriter, r *http.Request) {
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
	ticket, err := h.svc.RequestReverseFetchApproval(ns, serverID, req.Scope, req.Group, req.Target, decodeReason(r), r.Header.Get("Idempotency-Key"),
		auth.Operator(r.Context()), clientIP(r), requestPrincipal(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// Resync 处理 POST /admin/v1/instances/{serverId}/resync?namespace=（FR-91）：
// 先校验目标在线（不在注册表即 INSTANCE_NOT_FOUND，不建命令），再建 pending resync-config 命令 + 唤醒该 agent + 审计。
// 返回已创建命令（202）；agent 拉取后重拉有效配置/文件树/覆盖集并回传结果。
func (h *CommandHandler) Resync(w http.ResponseWriter, r *http.Request) {
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
	reason := decodeReason(r)
	ticket, err := h.svc.RequestResyncApproval(ns, serverID, reason, r.Header.Get("Idempotency-Key"), auth.Operator(r.Context()), clientIP(r), requestPrincipal(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// commandResultRequest 是 agent 回传命令执行结果的请求体（FR-91 resync-config）：ok 表示成功，失败时 reason 携原因。
type commandResultRequest struct {
	CommandID uint   `json:"commandId"`
	OK        bool   `json:"ok"`
	Reason    string `json:"reason"`
}

// ReportResult 处理 POST /beacon/v1/agent/commands/result（FR-91）：接收 agent 回传的 resync-config 命令执行结果，
// CAS 推进命令 done / failed。与其它 agent 端点同属 agentToken 防误连信任面；无内容回传，仅推进命令生命周期。
func (h *CommandHandler) ReportResult(w http.ResponseWriter, r *http.Request) {
	var req commandResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	commandType, err := h.svc.ResultCommandType(req.CommandID)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if commandType == model.CommandTypeBCDirectoryResync {
		if h.reportAuth == nil {
			render.WriteError(w, r, apperr.ErrCommandNotFound)
			return
		}
		identity, authErr := h.reportAuth.AuthenticateAgentReport(r.Header.Get("X-Beacon-Token"), r.Header.Get("X-Beacon-Identity"), r.Header.Get("X-Beacon-Boot"), r.RemoteAddr)
		if authErr != nil {
			render.WriteError(w, r, authErr)
			return
		}
		err = h.reportAuth.ReceiveDirectoryResyncResult(identity, req.CommandID, req.OK, req.Reason)
	} else {
		err = h.svc.ReceiveResyncResult(req.CommandID, req.OK, req.Reason)
	}
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// imprintRequest 是 admin 触发按需拓印的请求体（FR-46）：仅需目标文件相对 path；
// namespace 走查询参数（与 /instances/{serverId} 其他端点一致），落层在确认时再选。
type imprintRequest struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Imprint 创建拓印审批申请；批准后才由 worker 下发命令。
func (h *CommandHandler) Imprint(w http.ResponseWriter, r *http.Request) {
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
	var req imprintRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	// 校验目标在线：不在注册表即 INSTANCE_NOT_FOUND，不建命令。
	if _, err := h.instSvc.Get(ns, serverID); err != nil {
		render.WriteError(w, r, err)
		return
	}
	ticket, err := h.svc.RequestImprintApproval(ns, serverID, req.Path, req.Reason, r.Header.Get("Idempotency-Key"),
		auth.Operator(r.Context()), clientIP(r), requestPrincipal(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// ImprintStatus 处理 GET /admin/v1/imprints/{commandId}（FR-46）：返回拓印命令状态视图（供前端轮询至 ready）。
// 仅命令状态，不含瞬态磁盘内容；命令非 imprint 模式或不存在 → 404。
func (h *CommandHandler) ImprintStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "commandId")
	if !ok {
		return
	}
	cmd, err := h.svc.GetImprintCommand(id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, toCommandView(cmd))
}

// imprintDiffView 是拓印 diff 对外视图（FR-46）：本地实际值 ⟷ 期望合并值 + 逐键来源 + 是否有差异。
type imprintDiffView struct {
	Path              string                `json:"path"`
	ActualContent     string                `json:"actualContent"`
	ActualMD5         string                `json:"actualMd5"`
	ExpectedContent   string                `json:"expectedContent"`
	ExpectedMD5       string                `json:"expectedMd5"`
	ExpectedWholeFile bool                  `json:"expectedWholeFile"`
	ExpectedSources   []merge.KeyProvenance `json:"expectedSources"`
	ExpectedDeletions []merge.KeyProvenance `json:"expectedDeletions"`
	Differs           bool                  `json:"differs"`
}

// ImprintDiff 是会返回正文的旧拓印入口；未持 grant 时固定失败关闭。
func (h *CommandHandler) ImprintDiff(w http.ResponseWriter, r *http.Request) {
	render.WriteError(w, r, apperr.ErrOperationRequiresApproval)
}

// ConsumeApprovedImprint 仅允许原申请主体一次消费已经回传的拓印差异正文。
func (h *CommandHandler) ConsumeApprovedImprint(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "commandId")
	if !ok {
		return
	}
	grantID := chi.URLParam(r, "grantId")
	q := r.URL.Query()
	res, err := h.svc.ConsumeApprovedImprint(grantID, id, q.Get("scope"), q.Get("group"), q.Get("zone"), requestPrincipal(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, imprintDiffView{
		Path: res.Path, ActualContent: res.ActualContent, ActualMD5: res.ActualMD5,
		ExpectedContent: res.ExpectedContent, ExpectedMD5: res.ExpectedMD5,
		ExpectedWholeFile: res.ExpectedWholeFile, ExpectedSources: res.ExpectedSources,
		ExpectedDeletions: res.ExpectedDeletions, Differs: res.Differs,
	})
}

// confirmImprintRequest 是拓印确认落库请求体（FR-46）：并入层 + 目标键 + 自审 md5。
type confirmImprintRequest struct {
	Scope       string `json:"scope"`
	Group       string `json:"group"`
	Zone        string `json:"zone"`
	Target      string `json:"target"`
	ReviewedMD5 string `json:"reviewedMd5"`
	Reason      string `json:"reason"`
}

// ConfirmImprint 处理 POST /admin/v1/imprints/{commandId}/confirm（FR-46）：
// 命令须 ready 且 imprint 模式；自审 md5、并入层与目标冻结为独立审批申请。批准后 worker 才会落库。
func (h *CommandHandler) ConfirmImprint(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "commandId")
	if !ok {
		return
	}
	var req confirmImprintRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	ticket, err := h.svc.RequestImprintConfirmApproval(id, req.Scope, req.Group, req.Zone, req.Target, req.ReviewedMD5,
		req.Reason, r.Header.Get("Idempotency-Key"), auth.Operator(r.Context()), clientIP(r), requestPrincipal(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// agentCommandResponse 是 agent 拉待办命令的响应（含执行参考载荷；ingest 落点由控制面 ReceiveIngest 据库内载荷定）。
// Payload 用 json.RawMessage 把库内 payload 原文逐字透传——不可再经 map[string]string 反序列化，
// 否则 FR-58 submit 命令的 selectedPaths（JSON 数组）无法塞进 string 值、unmarshal 报错被吞、数组字段被静默丢弃，
// 致 agent 收不到选定集而回退「整树读内容」走老的超限整批失败口径（FR-58 真机暴露的缺陷）。
type agentCommandResponse struct {
	ID      uint            `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
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
	// 库内 payload 原文逐字透传（mode / selectedPaths 等一律不丢）；空载荷兜底为空对象保证响应仍是合法 JSON。
	payload := cmd.Payload
	if payload == "" {
		payload = "{}"
	}
	render.WriteJSON(w, http.StatusOK, agentCommandResponse{ID: cmd.ID, Type: cmd.Type, Payload: json.RawMessage(payload)})
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
	// FR-46 拓印模式：转存待审、不落库，ReceiveIngest 返回 (nil, nil)——无 created/updated，回 ok 即可（不可读 nil.Created）。
	if res == nil {
		render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"created": res.Created, "updated": res.Updated})
}

// parseUintParam 解析指定名的无符号整型路由参数；非法即写 INVALID_PARAM 并返回 ok=false。
// 与 parseID（仅 {id}）平行——拓印端点用 {commandId} 以免与文件 {id} 路由相撞。
func parseUintParam(w http.ResponseWriter, r *http.Request, name string) (uint, bool) {
	n, err := strconv.ParseUint(chi.URLParam(r, name), 10, 64)
	if err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return 0, false
	}
	return uint(n), true
}
