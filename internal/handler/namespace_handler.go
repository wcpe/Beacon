// Package handler 是 HTTP 处理层：仅做请求编解码与 service 调用，不含业务逻辑。
package handler

import (
	"encoding/json"
	"net/http"

	"beacon/internal/apperr"
	"beacon/internal/render"
	"beacon/internal/service"
)

// NamespaceHandler 处理环境相关的 admin 请求。
type NamespaceHandler struct {
	svc *service.NamespaceService
}

// NewNamespaceHandler 构造处理器。
func NewNamespaceHandler(svc *service.NamespaceService) *NamespaceHandler {
	return &NamespaceHandler{svc: svc}
}

// namespaceView 是对外返回的环境视图。
type namespaceView struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// List 处理 GET /admin/v1/namespaces。
func (h *NamespaceHandler) List(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.List()
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]namespaceView, 0, len(items))
	for _, it := range items {
		views = append(views, namespaceView{Code: it.Code, Name: it.Name})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views})
}

// createRequest 是新建环境的请求体。
type createRequest struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Create 处理 POST /admin/v1/namespaces。
func (h *NamespaceHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	ns, err := h.svc.Create(req.Code, req.Name)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, namespaceView{Code: ns.Code, Name: ns.Name})
}
