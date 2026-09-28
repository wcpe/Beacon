package server

import (
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/render"
)

const maxMCPRequestBytes = 1 << 20

// MCPAccessTokenVerifier 校验只面向固定 MCP resource 的 bearer。
type MCPAccessTokenVerifier interface {
	VerifyAccessToken(raw, audience string) (auth.Principal, error)
}

// MCPToolRegistrar 按认证后的机器主体构造仅含其可发现工具的 MCP server。
type MCPToolRegistrar interface {
	NewMCPServer(principal auth.Principal) *mcp.Server
}

// MCPHandler 是 Streamable HTTP MCP 的协议与认证边界；领域工具另行静态登记。
type MCPHandler struct {
	audience  string
	verifier  MCPAccessTokenVerifier
	transport http.Handler
	policy    *MCPProxyPolicy
}

// NewPublicMCPHandlerWithTools 为公网 MCP resource 注入按主体隔离的显式工具目录。
// disableLocalhostProtection 为 true 时关闭 SDK 的 DNS rebinding Host 校验（内网直连部署）。
func NewPublicMCPHandlerWithTools(verifier MCPAccessTokenVerifier, policy *MCPProxyPolicy, tools MCPToolRegistrar, disableLocalhostProtection bool) *MCPHandler {
	h := newMCPHandler(verifier, policy.Audience(), func(r *http.Request) *mcp.Server {
		if tools == nil {
			return newEmptyMCPServer()
		}
		principal, ok := auth.FromContext(r.Context())
		if !ok || principal.Kind != auth.PrincipalKindMCP {
			return newEmptyMCPServer()
		}
		return tools.NewMCPServer(principal)
	}, disableLocalhostProtection)
	h.policy = policy
	return h
}

// NewMCPHandler 构造空工具 MCP transport。未登记工具时不暴露任何领域写入能力。
func NewMCPHandler(verifier MCPAccessTokenVerifier, audience string) *MCPHandler {
	return newMCPHandler(verifier, audience, func(*http.Request) *mcp.Server { return newEmptyMCPServer() }, false)
}

// newMCPHandler 构造**无状态** transport；disableLocalhostProtection 为 true 时关闭 SDK 的
// DNS rebinding Host 校验（内网直连部署由 MCPProxyPolicy 的 Host 白名单接管该职责）。
//
// 无状态（Stateless）语义：不读也不写 Mcp-Session-Id，每个请求使用带默认初始化参数的临时会话。
// 为什么必须如此：会话一旦空闲失效（此前为 5 分钟），客户端再次调用会收到 session not found，
// 而主流客户端（含 NarraFork 内置 HTTP 客户端）在会话失效后不会重发 initialize，通道因此
// **永久卡死**、必须人工重连。SDK 的 Stateless 即 MCP 规范的无状态方向（SEP-2567）。
func newMCPHandler(verifier MCPAccessTokenVerifier, audience string, factory func(*http.Request) *mcp.Server, disableLocalhostProtection bool) *MCPHandler {
	return &MCPHandler{
		audience: audience, verifier: verifier,
		transport: mcp.NewStreamableHTTPHandler(factory, &mcp.StreamableHTTPOptions{
			Stateless:                  true,
			JSONResponse:               true,
			DisableLocalhostProtection: disableLocalhostProtection,
		}),
	}
}

func newEmptyMCPServer() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{Name: "beacon-admin", Version: "v2"}, &mcp.ServerOptions{PageSize: 100})
}

// ServeHTTP 拒绝非 MCP bearer，再把可信机器主体注入 SDK 请求上下文。
func (h *MCPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.verifier == nil || strings.TrimSpace(h.audience) == "" {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return
	}
	if h.policy != nil && !h.policy.Accepts(r) {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return
	}
	if r.ContentLength > maxMCPRequestBytes {
		render.WriteError(w, r, apperr.ErrPayloadTooLarge)
		return
	}
	credential := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), bearerPrefix))
	if !strings.HasPrefix(r.Header.Get("Authorization"), bearerPrefix) || credential == "" {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return
	}
	principal, err := h.verifier.VerifyAccessToken(credential, h.audience)
	if err != nil {
		render.WriteError(w, r, apperr.ErrAdminUnauthorized)
		return
	}
	h.transport.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
}
