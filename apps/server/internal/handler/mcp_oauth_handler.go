package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// MCPOAuthHandler 处理受管理面鉴权保护的 MCP OAuth 客户端生命周期端点。
type MCPOAuthHandler struct{ svc *service.MCPOAuthService }

// NewMCPOAuthHandler 构造 MCP OAuth 管理处理器。
func NewMCPOAuthHandler(svc *service.MCPOAuthService) *MCPOAuthHandler {
	return &MCPOAuthHandler{svc: svc}
}

type mcpClientView struct {
	ClientID      string `json:"clientId"`
	DisplayName   string `json:"displayName"`
	SecretPrefix  string `json:"secretPrefix"`
	Profile       string `json:"profile"`
	Status        string `json:"status"`
	SecretVersion uint   `json:"secretVersion"`
	// 生命周期时间戳：供管理台回答"何时建的、谁建的、何时被吊销"。
	// RevokedAt 仅吊销后存在，用指针区分"未吊销"与零值时间。
	CreatedBy string     `json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
}

func toMCPClientView(client *model.MCPOAuthClient) mcpClientView {
	return mcpClientView{
		ClientID:      client.ClientID,
		DisplayName:   client.DisplayName,
		SecretPrefix:  client.SecretPrefix,
		Profile:       client.Profile,
		Status:        client.Status,
		SecretVersion: client.SecretVersion,
		CreatedBy:     client.CreatedBy,
		CreatedAt:     client.CreatedAt,
		UpdatedAt:     client.UpdatedAt,
		RevokedAt:     client.RevokedAt,
	}
}

// List 处理 GET /admin/v2/mcp-clients。
func (h *MCPOAuthHandler) List(w http.ResponseWriter, r *http.Request) {
	clients, err := h.svc.ListClients(requestPrincipal(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items := make([]mcpClientView, 0, len(clients))
	for i := range clients {
		items = append(items, toMCPClientView(&clients[i]))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// Get 处理 GET /admin/v2/mcp-clients/{clientId}。
func (h *MCPOAuthHandler) Get(w http.ResponseWriter, r *http.Request) {
	client, err := h.svc.GetClient(chi.URLParam(r, "clientId"), requestPrincipal(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, toMCPClientView(client))
}

type createMCPClientRequest struct {
	DisplayName string `json:"displayName"`
	Profile     string `json:"profile"`
	Reason      string `json:"reason"`
}
type mcpReasonRequest struct {
	Reason string `json:"reason"`
}

// Create 处理 POST /admin/v2/mcp-clients，只有首次 202 响应含明文 secret。
func (h *MCPOAuthHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body createMCPClientRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	ticket, err := h.svc.RequestCreate(body.DisplayName, body.Profile, body.Reason, r.Header.Get("Idempotency-Key"), requestPrincipal(r), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// Rotate 处理 POST /admin/v2/mcp-clients/{clientId}/rotate。
func (h *MCPOAuthHandler) Rotate(w http.ResponseWriter, r *http.Request) {
	h.requestExistingChange(w, r, true)
}

// Enable 处理 POST /admin/v2/mcp-clients/{clientId}/enable。
func (h *MCPOAuthHandler) Enable(w http.ResponseWriter, r *http.Request) {
	h.requestExistingChange(w, r, false)
}

func (h *MCPOAuthHandler) requestExistingChange(w http.ResponseWriter, r *http.Request, rotate bool) {
	var body mcpReasonRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	var ticket service.MCPClientApprovalTicket
	var err error
	if rotate {
		ticket, err = h.svc.RequestRotate(chi.URLParam(r, "clientId"), body.Reason, r.Header.Get("Idempotency-Key"), requestPrincipal(r), clientIP(r))
	} else {
		ticket, err = h.svc.RequestEnable(chi.URLParam(r, "clientId"), body.Reason, r.Header.Get("Idempotency-Key"), requestPrincipal(r), clientIP(r))
	}
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, ticket)
}

// Revoke 处理 POST /admin/v2/mcp-clients/{clientId}/revoke，该止损动作不等待审批。
func (h *MCPOAuthHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RevokeClient(chi.URLParam(r, "clientId"), requestPrincipal(r), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
