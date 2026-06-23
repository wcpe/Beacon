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

// SettingsHandler 处理运维设置 store 的读 / 写（FR-61，见 ADR-0038）。
// 只读拒写由鉴权中间件统一裁决（readonly 角色经 readonlyWriteGuard 对 PUT 一律 403），本处理器不碰角色判断。
type SettingsHandler struct {
	svc *service.SettingsService
}

// NewSettingsHandler 构造处理器。
func NewSettingsHandler(svc *service.SettingsService) *SettingsHandler {
	return &SettingsHandler{svc: svc}
}

// List 处理 GET /admin/v1/settings：列出全部热改项当前值 + 类型 + 默认 + 说明（启动 / 安全项不在此列）。
func (h *SettingsHandler) List(w http.ResponseWriter, _ *http.Request) {
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": h.svc.List()})
}

// updateSettingRequest 是更新单项的请求体（operator / clientIP 由认证态 / 请求派生，忽略手填）。
type updateSettingRequest struct {
	Value string `json:"value"`
}

// Update 处理 PUT /admin/v1/settings/{key}：更新单个热改项（白名单外 key / 非法值拒 400，入审计）。
func (h *SettingsHandler) Update(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	var req updateSettingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if err := h.svc.Update(key, req.Value, auth.Operator(r.Context()), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
