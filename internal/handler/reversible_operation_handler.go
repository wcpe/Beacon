package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/auth"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/render"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/service"
)

// ReversibleOperationHandler 处理配置操作级撤回子系统 admin 请求（FR-116，见 ADR-0051）：
// 列出可逆操作账目（读，供工作台操作日志）+ 撤回单条（写，full 角色，入审计）。
// 只读拒写由鉴权链统一裁决；撤回端点额外经 requireFullRole 挡 readonly（路由处装配）。handler 不碰 GORM。
type ReversibleOperationHandler struct {
	svc *service.ReversibleOperationService
}

// NewReversibleOperationHandler 构造处理器。
func NewReversibleOperationHandler(svc *service.ReversibleOperationService) *ReversibleOperationHandler {
	return &ReversibleOperationHandler{svc: svc}
}

// reversibleOpView 是可逆操作账目对外视图（小驼峰）：仅元数据 + 摘要 + 状态，**绝不含** inversePayload 反向快照瞬态。
type reversibleOpView struct {
	ID          uint      `json:"id"`
	Namespace   string    `json:"namespace"`
	OpType      string    `json:"opType"`
	Scope       string    `json:"scope"`
	ScopeTarget string    `json:"scopeTarget"`
	Status      string    `json:"status"`
	Summary     string    `json:"summary"`
	Operator    string    `json:"operator"`
	ReversedBy  string    `json:"reversedBy"`
	CreatedAt   time.Time `json:"createdAt"`
	// 是否仍可撤回（status=reversible）：前端据此决定撤回按钮可用 / 置灰
	Reversible bool `json:"reversible"`
}

// List 处理 GET /admin/v1/reversible-operations（按 namespace/opType/status 过滤，最新在前）。
func (h *ReversibleOperationHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	ops, err := h.svc.List(repository.ReversibleOperationFilter{
		Namespace: q.Get("namespace"),
		OpType:    q.Get("opType"),
		Status:    q.Get("status"),
		Limit:     limit,
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]reversibleOpView, 0, len(ops))
	for i := range ops {
		op := &ops[i]
		views = append(views, reversibleOpView{
			ID: op.ID, Namespace: op.NamespaceCode, OpType: op.OpType,
			Scope: op.Scope, ScopeTarget: op.ScopeTarget, Status: op.Status,
			Summary: op.Summary, Operator: op.Operator, ReversedBy: op.ReversedBy,
			CreatedAt:  op.CreatedAt,
			Reversible: op.Status == model.ReversibleStatusReversible,
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views})
}

// Undo 处理 POST /admin/v1/reversible-operations/{id}/undo：撤回一条可逆操作（幂等）。
// full 角色（路由经 requireFullRole 挡 readonly→403）；撤回入审计。重复撤回返回幂等成功。
func (h *ReversibleOperationHandler) Undo(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id == 0 {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	op, err := h.svc.Undo(uint(id), auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, reversibleOpView{
		ID: op.ID, Namespace: op.NamespaceCode, OpType: op.OpType,
		Scope: op.Scope, ScopeTarget: op.ScopeTarget, Status: op.Status,
		Summary: op.Summary, Operator: op.Operator, ReversedBy: op.ReversedBy,
		CreatedAt:  op.CreatedAt,
		Reversible: op.Status == model.ReversibleStatusReversible,
	})
}
