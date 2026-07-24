package handler

import (
	"encoding/json"
	"net/http"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// EnvHandler 处理 env 展示维度端点（FR-178，见 v2-zone-authority.md §5）。
// 仅做请求编解码与 service 调用，不含业务逻辑；错误统一经 render.WriteError 脱敏输出。
type EnvHandler struct {
	svc *service.EnvService
}

// NewEnvHandler 构造处理器。
func NewEnvHandler(svc *service.EnvService) *EnvHandler {
	return &EnvHandler{svc: svc}
}

// List 处理 GET /admin/v2/envs：env 列表，含映射的 namespace 摘要。
func (h *EnvHandler) List(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.List()
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

type envCreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Create 处理 POST /admin/v2/envs。
func (h *EnvHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req envCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := h.svc.Create(req.Name, req.Description, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, view)
}

// envUpdateRequest 用指针区分「未传该字段」与「传空值」（PATCH 局部更新语义）。
type envUpdateRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// Update 处理 PATCH /admin/v2/envs/{id}：改 env 名 / 描述。
func (h *EnvHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := uintURLParam(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	var req envUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := h.svc.Update(id, req.Name, req.Description, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// Delete 处理 DELETE /admin/v2/envs/{id}：删 env（映射级联删除）。
func (h *EnvHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := uintURLParam(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if err := h.svc.Delete(id, auth.Operator(r.Context()), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type envSetNamespacesRequest struct {
	NamespaceIDs []uint `json:"namespaceIds"`
}

// SetNamespaces 处理 PUT /admin/v2/envs/{id}/namespaces：整体替换 env→namespace 映射。
func (h *EnvHandler) SetNamespaces(w http.ResponseWriter, r *http.Request) {
	id, err := uintURLParam(r, "id")
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	var req envSetNamespacesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := h.svc.SetNamespaces(id, req.NamespaceIDs, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}
