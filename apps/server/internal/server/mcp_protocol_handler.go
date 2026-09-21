package server

import (
	"errors"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/render"
)

// MCPOAuthIssuer 是 OAuth token 端点需要的最小应用服务边界。
type MCPOAuthIssuer interface {
	IssueAccessTokenFrom(clientID, clientSecret, audience, scope, clientIP string) (string, time.Duration, auth.Principal, error)
}

// MCPProtocolHandler 聚合固定 metadata、token 与 MCP resource；不提供任何通用 REST 代理。
type MCPProtocolHandler struct {
	policy *MCPProxyPolicy
	issuer MCPOAuthIssuer
	mcp    http.Handler
}

// NewMCPProtocolHandler 只在有效受信反代策略下构造协议入口。
// 直连模式（policy.Direct()）下关闭 SDK 的 DNS rebinding Host 校验，改由 policy 的 Host 白名单负责。
func NewMCPProtocolHandler(policy *MCPProxyPolicy, issuer MCPOAuthIssuer, verifier MCPAccessTokenVerifier, tools MCPToolRegistrar) *MCPProtocolHandler {
	if policy == nil || !policy.Enabled() || issuer == nil || verifier == nil {
		return nil
	}
	return &MCPProtocolHandler{
		policy: policy, issuer: issuer,
		mcp: NewPublicMCPHandlerWithTools(verifier, policy, tools, policy.Direct()),
	}
}

// Metadata 处理 OAuth Protected Resource Metadata。
func (h *MCPProtocolHandler) Metadata(w http.ResponseWriter, r *http.Request) {
	if !h.accept(w, r) {
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"resource": h.policy.Audience(), "authorization_servers": []string{strings.TrimSuffix(h.policy.Audience(), "/admin/v2/mcp")}})
}

// AuthorizationServerMetadata 处理 OAuth Authorization Server Metadata。
func (h *MCPProtocolHandler) AuthorizationServerMetadata(w http.ResponseWriter, r *http.Request) {
	if !h.accept(w, r) {
		return
	}
	base := strings.TrimSuffix(h.policy.Audience(), "/admin/v2/mcp")
	render.WriteJSON(w, http.StatusOK, map[string]any{"issuer": base, "token_endpoint": base + "/admin/v2/oauth/token", "grant_types_supported": []string{"client_credentials"}, "token_endpoint_auth_methods_supported": []string{"client_secret_post"}})
}

// Token 处理固定 audience 的 Client Credentials 换 token。
func (h *MCPProtocolHandler) Token(w http.ResponseWriter, r *http.Request) {
	if !h.accept(w, r) {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" || r.ParseForm() != nil || r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("audience") != h.policy.Audience() {
		render.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	token, ttl, principal, err := h.issuer.IssueAccessTokenFrom(r.Form.Get("client_id"), r.Form.Get("client_secret"), r.Form.Get("audience"), r.Form.Get("scope"), trustedMCPClientIP(r))
	if err != nil || principal.Kind != auth.PrincipalKindMCP {
		// 按 apperr 的规范错误码回写 OAuth error 字段，与 auditTokenDenied 记录的原因一致。
		// 其余（含凭证错误）统一 401 invalid_client，不区分内部原因以防枚举探测（RFC 6749 §5.2）。
		var ae *apperr.Error
		if errors.As(err, &ae) && ae.Code == apperr.ErrOAuthInvalidScope.Code {
			render.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_scope"})
			return
		}
		if errors.As(err, &ae) && ae.Code == apperr.ErrOAuthInvalidRequest.Code {
			render.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
			return
		}
		render.WriteJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid_client"})
		return
	}
	scope := r.Form.Get("scope")
	if scope == "" {
		scope = principal.Role
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": int(ttl.Seconds()), "scope": scope})
}

// MCP 处理 Streamable HTTP resource。
func (h *MCPProtocolHandler) MCP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.mcp == nil {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return
	}
	h.mcp.ServeHTTP(w, r)
}

func (h *MCPProtocolHandler) accept(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || h.policy == nil || !h.policy.Accepts(r) {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return false
	}
	return true
}

func trustedMCPClientIP(r *http.Request) string {
	return strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
}
