package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/auth"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/render"
	"github.com/wcpe/Beacon/internal/service"
)

// ReverseFetchTaskHandler 处理反向抓取受管任务（FR-58，见 ADR-0037）：
// admin 建扫描任务 / 查 / 列 / 提交选定 / 取消 + agent 回传扫描清单。
type ReverseFetchTaskHandler struct {
	svc     *service.ReverseFetchTaskService
	instSvc *service.InstanceService
}

// NewReverseFetchTaskHandler 构造处理器（instSvc 供建任务前校验目标在线）。
func NewReverseFetchTaskHandler(svc *service.ReverseFetchTaskService, instSvc *service.InstanceService) *ReverseFetchTaskHandler {
	return &ReverseFetchTaskHandler{svc: svc, instSvc: instSvc}
}

// reverseFetchScanFileView 是扫描清单单文件视图（FR-58，无内容）。
type reverseFetchScanFileView struct {
	Path          string `json:"path"`
	Size          int64  `json:"size"`
	IsText        bool   `json:"isText"`
	OverThreshold bool   `json:"overThreshold"`
}

// reverseFetchTaskView 是受管任务对外视图（含状态 / 计数 / 命令引用 / 清单与选定，供任务台与审核台）。
// 清单 / 选定为 JSON 文本字段，前端按需解析；进度由状态 + 计数表达。
type reverseFetchTaskView struct {
	ID                 uint                       `json:"id"`
	Namespace          string                     `json:"namespace"`
	ServerID           string                     `json:"serverId"`
	Scope              string                     `json:"scope"`
	Group              string                     `json:"group"`
	Target             string                     `json:"target"`
	Status             string                     `json:"status"`
	ScanCommandID      uint                       `json:"scanCommandId"`
	SubmitCommandID    uint                       `json:"submitCommandId"`
	TotalFiles         int                        `json:"totalFiles"`
	SelectedCount      int                        `json:"selectedCount"`
	OverThresholdCount int                        `json:"overThresholdCount"`
	SkippedCount       int                        `json:"skippedCount"`
	Files              []reverseFetchScanFileView `json:"files"`
	SelectedPaths      []string                   `json:"selectedPaths"`
	Operator           string                     `json:"operator"`
	Note               string                     `json:"note"`
	CreatedAt          string                     `json:"createdAt"`
	UpdatedAt          string                     `json:"updatedAt"`
}

// manifestFilesEnvelope 仅解出 manifest TEXT 中的 files 数组（视图按需展开清单元信息）。
type manifestFilesEnvelope struct {
	Files []reverseFetchScanFileView `json:"files"`
}

func toReverseFetchTaskView(t *model.ReverseFetchTask) reverseFetchTaskView {
	v := reverseFetchTaskView{
		ID: t.ID, Namespace: t.NamespaceCode, ServerID: t.ServerID,
		Scope: t.Scope, Group: t.GroupCode, Target: t.ScopeTarget, Status: t.Status,
		ScanCommandID: t.ScanCommandID, SubmitCommandID: t.SubmitCommandID,
		TotalFiles: t.TotalFiles, SelectedCount: t.SelectedCount,
		OverThresholdCount: t.OverThresholdCount, SkippedCount: t.SkippedCount,
		Files: []reverseFetchScanFileView{}, SelectedPaths: []string{},
		Operator:  t.Operator,
		Note:      t.Note,
		CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: t.UpdatedAt.UTC().Format(time.RFC3339),
	}
	// 清单 / 选定为 TEXT JSON，best-effort 解析展开；解析失败留空（不致整页失败）。
	if t.Manifest != "" {
		var env manifestFilesEnvelope
		if json.Unmarshal([]byte(t.Manifest), &env) == nil && env.Files != nil {
			v.Files = env.Files
		}
	}
	if t.SelectedPaths != "" {
		var sel []string
		if json.Unmarshal([]byte(t.SelectedPaths), &sel) == nil && sel != nil {
			v.SelectedPaths = sel
		}
	}
	return v
}

// createScanTaskRequest 是 admin 建扫描任务请求体（scope=group 只需 group；scope=server 需 group + target）。
type createScanTaskRequest struct {
	Scope  string `json:"scope"`
	Group  string `json:"group"`
	Target string `json:"target"`
}

// CreateScanTask 处理 POST /admin/v1/instances/{serverId}/reverse-fetch?namespace=（FR-58 重定义）：
// 先校验目标在线，再互斥建任务(scanning) + 下发 scan 命令 + 唤醒 agent + 审计。返回任务视图（202）。
func (h *ReverseFetchTaskHandler) CreateScanTask(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "serverId")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	var req createScanTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	// 校验目标在线：不在注册表即 INSTANCE_NOT_FOUND，不建任务。
	if _, err := h.instSvc.Get(ns, serverID); err != nil {
		render.WriteError(w, r, err)
		return
	}
	task, err := h.svc.CreateScanTask(ns, serverID, req.Scope, req.Group, req.Target,
		auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, toReverseFetchTaskView(task))
}

// GetTask 处理 GET /admin/v1/reverse-fetch/tasks/{id}（FR-58）：返回任务详情（状态 / 清单 / 计数 / 命令引用）。
func (h *ReverseFetchTaskHandler) GetTask(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	task, err := h.svc.Get(id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, toReverseFetchTaskView(task))
}

// ListTasks 处理 GET /admin/v1/reverse-fetch/tasks?namespace=&serverId=&status=（FR-58）：任务历史列表（任务台）。
func (h *ReverseFetchTaskHandler) ListTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tasks, err := h.svc.List(q.Get("namespace"), q.Get("serverId"), q.Get("status"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items := make([]reverseFetchTaskView, len(tasks))
	for i := range tasks {
		items[i] = toReverseFetchTaskView(&tasks[i])
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// submitTaskRequest 是提交选定集请求体（FR-58）：选定 path 数组 + 是否确认纳入超阈值文件。
type submitTaskRequest struct {
	SelectedPaths        []string `json:"selectedPaths"`
	ConfirmOverThreshold bool     `json:"confirmOverThreshold"`
}

// SubmitTask 处理 POST /admin/v1/reverse-fetch/tasks/{id}/submit（FR-58）：任务须 pending-review；
// 校验选定（超阈值须确认）→ 下发 submit 命令 + 任务→fetching + 审计 + 唤醒。返回任务视图（202）。
func (h *ReverseFetchTaskHandler) SubmitTask(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	var req submitTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	task, err := h.svc.Submit(id, req.SelectedPaths, req.ConfirmOverThreshold,
		auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, toReverseFetchTaskView(task))
}

// CancelTask 处理 POST /admin/v1/reverse-fetch/tasks/{id}/cancel（FR-58）：非终态 → cancelled + 审计。
func (h *ReverseFetchTaskHandler) CancelTask(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	task, err := h.svc.Cancel(id, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, toReverseFetchTaskView(task))
}

// scanRequestFile 是 agent 回传扫描清单的单文件元信息（无内容，FR-58 线路契约）。
type scanRequestFile struct {
	Path          string `json:"path"`
	Size          int64  `json:"size"`
	IsText        bool   `json:"isText"`
	OverThreshold bool   `json:"overThreshold"`
}

// scanRequest 是 agent 回传扫描清单的请求体（FR-58 线路契约）。
type scanRequest struct {
	CommandID uint              `json:"commandId"`
	Files     []scanRequestFile `json:"files"`
}

// Scan 处理 POST /beacon/v1/agent/files/scan（FR-58，agentToken 中间件下）：接收 agent 回传扫描清单
// （只含元信息、无内容、永不失败）→ 控制面存任务 manifest + 计数、任务→pending-review、命令→done。
func (h *ReverseFetchTaskHandler) Scan(w http.ResponseWriter, r *http.Request) {
	var req scanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	files := make([]service.ScanFile, len(req.Files))
	for i, f := range req.Files {
		files[i] = service.ScanFile{Path: f.Path, Size: f.Size, IsText: f.IsText, OverThreshold: f.OverThreshold}
	}
	if err := h.svc.ReceiveScan(req.CommandID, files, clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
