package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"beacon/internal/apperr"
	"beacon/internal/auth"
	"beacon/internal/render"
	"beacon/internal/service"
)

// AuthHandler 处理管理台登录 / 登出（凭据 → 令牌；登出仅记审计）。
type AuthHandler struct {
	authn *auth.Authenticator
	audit *service.AuthAuditService
}

// NewAuthHandler 构造处理器。
func NewAuthHandler(authn *auth.Authenticator, audit *service.AuthAuditService) *AuthHandler {
	return &AuthHandler{authn: authn, audit: audit}
}

// loginRequest 是登录请求体。
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Login 处理 POST /admin/v1/auth/login：校验凭据并签发令牌。
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	token, err := h.authn.Login(req.Username, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrBadCredentials) {
			render.WriteError(w, r, apperr.ErrBadCredentials)
			return
		}
		render.WriteError(w, r, err)
		return
	}
	// 登录成功记审计；审计失败不阻断登录（令牌已签发），仅告警。
	if err := h.audit.RecordLogin(req.Username, clientIP(r)); err != nil {
		slog.Warn("登录审计写入失败", "operator", req.Username, "error", err)
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"token":    token,
		"operator": req.Username,
	})
}

// Logout 处理 POST /admin/v1/auth/logout：仅记登出审计。
// 令牌为无状态 HMAC，服务端不持会话、无可吊销态；登出由前端清本地令牌，此处只留审计痕迹。
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	operator := auth.Operator(r.Context())
	if err := h.audit.RecordLogout(operator, clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
