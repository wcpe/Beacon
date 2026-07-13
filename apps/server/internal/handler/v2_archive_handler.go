package handler

import (
	"encoding/json"
	"net/http"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// V2ArchiveHandler 处理热冷归档管理面端点（FR-153，见 spec §5、ADR-0066）：
// 总览 / 创建任务（dry_run / execute）/ 任务列表（分页 + 过滤）/ 详情 / 重试 / 取消。
// 薄 handler——只解析请求、调 ArchiveService、经 render 统一写出；响应形状由 service 视图逐字对齐
// packages/contracts archive.ts，此处直出不再二次映射。创建 / 重试 / 取消的专项审计由 service 在事务内自记。
type V2ArchiveHandler struct {
	svc *service.ArchiveService
}

// NewV2ArchiveHandler 构造处理器。
func NewV2ArchiveHandler(svc *service.ArchiveService) *V2ArchiveHandler {
	return &V2ArchiveHandler{svc: svc}
}

// createArchiveJobRequest 是创建任务请求体（对齐 contracts）：mode 必填，domains 空 / 省略 = 全部域。
type createArchiveJobRequest struct {
	Mode    string   `json:"mode"`
	Domains []string `json:"domains"`
}

// Overview 处理 GET /admin/v2/archive/overview：目标库形态 / 可达性 + 各域保留期与水位。
func (h *V2ArchiveHandler) Overview(w http.ResponseWriter, r *http.Request) {
	view, err := h.svc.Overview()
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// CreateJob 处理 POST /admin/v2/archive/jobs：创建 dry_run / execute 任务。
// 201 返回 job 详情；已有活跃任务 409、归档库不可达 503、mode / domain 非法 400（错误由 service 判定，脱敏写出）。
func (h *V2ArchiveHandler) CreateJob(w http.ResponseWriter, r *http.Request) {
	var req createArchiveJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := h.svc.CreateJob(req.Mode, req.Domains, auth.Operator(r.Context()))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, view)
}

// ListJobs 处理 GET /admin/v2/archive/jobs：按 status / mode / trigger 过滤 + 分页，返回 {items, total}。
func (h *V2ArchiveHandler) ListJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	view, err := h.svc.ListJobs(repository.ArchiveJobFilter{
		Status:   q.Get("status"),
		Mode:     q.Get("mode"),
		Trigger:  q.Get("trigger"),
		Page:     intQuery(q.Get("page")),
		PageSize: intQuery(q.Get("pageSize")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// GetJob 处理 GET /admin/v2/archive/jobs/{id}：任务全字段 + items[]；不存在 404、id 非法 400。
func (h *V2ArchiveHandler) GetJob(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	view, err := h.svc.GetJob(id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// RetryJob 处理 POST /admin/v2/archive/jobs/{id}/retry：对 failed 任务断点续跑。
// 200 返回任务详情；仅 failed 可重试否则 409、不存在 404、归档库不可达 503。
func (h *V2ArchiveHandler) RetryJob(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	view, err := h.svc.RetryJob(id, auth.Operator(r.Context()))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// CancelJob 处理 POST /admin/v2/archive/jobs/{id}/cancel：取消任务。
// 200 返回任务详情；仅 pending / running 可取消否则 409、不存在 404。
func (h *V2ArchiveHandler) CancelJob(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	view, err := h.svc.CancelJob(id, auth.Operator(r.Context()))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}
