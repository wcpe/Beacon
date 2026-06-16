package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"beacon/internal/apperr"
	"beacon/internal/auth"
	"beacon/internal/render"
)

// AuthHandler 处理管理台登录（凭据 → 令牌）。
type AuthHandler struct {
	authn *auth.Authenticator
}

// NewAuthHandler 构造处理器。
func NewAuthHandler(authn *auth.Authenticator) *AuthHandler {
	return &AuthHandler{authn: authn}
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
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"token":    token,
		"operator": req.Username,
	})
}
