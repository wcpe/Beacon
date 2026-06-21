package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"beacon/internal/apperr"
	"beacon/internal/auth"
	"beacon/internal/model"
	"beacon/internal/render"
	"beacon/internal/service"
)

// APIKeyHandler 处理管理面 API 密钥的 CRUD（FR-42，见 ADR-0026）。
// 只读拒写由鉴权中间件统一裁决，本处理器不碰角色判断。
type APIKeyHandler struct {
	svc *service.APIKeyService
}

// NewAPIKeyHandler 构造处理器。
func NewAPIKeyHandler(svc *service.APIKeyService) *APIKeyHandler {
	return &APIKeyHandler{svc: svc}
}

// apiKeyView 是密钥对外视图（列表 / 元数据）：**绝不含明文与哈希**。
type apiKeyView struct {
	ID         uint       `json:"id"`
	Name       string     `json:"name"`
	Role       string     `json:"role"`
	KeyPrefix  string     `json:"keyPrefix"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

// apiKeyCreatedView 是创建 / 重置的响应：在元数据之外**一次性**附带明文 `key`（之后不可再得）。
type apiKeyCreatedView struct {
	apiKeyView
	Key string `json:"key"`
}

// toAPIKeyView 把模型转为对外视图（派生 status，剥离明文 / 哈希）。
func toAPIKeyView(k *model.APIKey) apiKeyView {
	return apiKeyView{
		ID: k.ID, Name: k.Name, Role: k.Role, KeyPrefix: k.KeyPrefix,
		Status: apiKeyStatus(k), CreatedAt: k.CreatedAt,
		ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt,
	}
}

// apiKeyStatus 派生密钥状态：已吊销（软删）> 已过期 > 生效。
func apiKeyStatus(k *model.APIKey) string {
	if model.IsDeleted(k.DeletedAt) {
		return "revoked"
	}
	if k.ExpiresAt != nil && time.Now().UTC().After(*k.ExpiresAt) {
		return "expired"
	}
	return "active"
}

// List 处理 GET /admin/v1/api-keys：列出全部密钥（含已吊销，显示状态）。
func (h *APIKeyHandler) List(w http.ResponseWriter, r *http.Request) {
	keys, err := h.svc.List()
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]apiKeyView, 0, len(keys))
	for i := range keys {
		views = append(views, toAPIKeyView(&keys[i]))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": views})
}

// createAPIKeyRequest 是创建密钥的请求体（operator 由认证态派生，忽略手填）。
type createAPIKeyRequest struct {
	Name string `json:"name"`
	Role string `json:"role"`
	// 可选过期时刻（RFC3339）；为空表示永不过期
	ExpiresAt string `json:"expiresAt"`
}

// Create 处理 POST /admin/v1/api-keys：创建密钥，明文仅此响应一次返回。
func (h *APIKeyHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	expiresAt, ok := parseOptionalRFC3339(req.ExpiresAt)
	if !ok {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	plaintext, key, err := h.svc.Create(req.Name, req.Role, expiresAt, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, apiKeyCreatedView{apiKeyView: toAPIKeyView(key), Key: plaintext})
}

// Revoke 处理 DELETE /admin/v1/api-keys/{id}：吊销密钥（软删）。
func (h *APIKeyHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	if err := h.svc.Revoke(id, auth.Operator(r.Context()), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Reset 处理 POST /admin/v1/api-keys/{id}/reset：重置（轮换）密钥明文，旧明文立即失效。
func (h *APIKeyHandler) Reset(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	plaintext, key, err := h.svc.Reset(id, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, apiKeyCreatedView{apiKeyView: toAPIKeyView(key), Key: plaintext})
}

// parseOptionalRFC3339 解析可选的 RFC3339 时间：空串→(nil,true)；合法→(*t,true)；非法→(nil,false)。
func parseOptionalRFC3339(s string) (*time.Time, bool) {
	if s == "" {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, false
	}
	utc := t.UTC()
	return &utc, true
}
