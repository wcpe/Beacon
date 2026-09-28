package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
)

const (
	mcpTestAudience   = "https://beacon.example/admin/v2/mcp"
	mcpTestInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	mcpTestToolsList  = `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
)

// mcpStatelessHandler 返回启用无状态模式后的 MCP handler（工具目录为空，仅验证传输层）。
func mcpStatelessHandler() *MCPHandler {
	return NewMCPHandler(
		mcpVerifierStub{principal: auth.MCPPrincipal("client", "自动化", "automation")},
		mcpTestAudience,
	)
}

// mcpPost 构造一次带合法 bearer 的 MCP POST 请求（Content-Type 与 Accept 均满足协议要求）。
func mcpPost(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/admin/v2/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer mct_valid")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Accept", "text/event-stream")
	return req
}

// 无状态端点：不带 Mcp-Session-Id 也必须能反复调用。
// 这是本次改造的核心回归——此前会话空闲失效后，客户端再次调用会被拒，且因不重发
// initialize 而永久卡死。
func TestMCPHandlerStatelessAllowsRepeatedCallsWithoutSession(t *testing.T) {
	h := mcpStatelessHandler()
	for i, body := range []string{mcpTestInitialize, mcpTestInitialize, mcpTestToolsList} {
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, mcpPost(body))
		if recorder.Code != http.StatusOK {
			t.Fatalf("第 %d 次无会话调用状态=%d，期望 200；响应=%s", i+1, recorder.Code, recorder.Body.String())
		}
	}
}

// 无状态端点不下发会话 id：客户端因而不需要维护会话状态。
func TestMCPHandlerStatelessDoesNotIssueSessionID(t *testing.T) {
	recorder := httptest.NewRecorder()
	mcpStatelessHandler().ServeHTTP(recorder, mcpPost(mcpTestInitialize))
	if recorder.Code != http.StatusOK {
		t.Fatalf("initialize 状态=%d，期望 200；响应=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Mcp-Session-Id"); got != "" {
		t.Fatalf("无状态端点不应下发 Mcp-Session-Id，实际=%q", got)
	}
}

// 无状态端点忽略客户端携带的会话 id：陈旧会话不再导致调用失败。
func TestMCPHandlerStatelessIgnoresStaleSessionID(t *testing.T) {
	req := mcpPost(mcpTestToolsList)
	req.Header.Set("Mcp-Session-Id", "stale-session-from-previous-run")
	recorder := httptest.NewRecorder()
	mcpStatelessHandler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("携带陈旧会话 id 应被忽略而非失败，状态=%d；响应=%s", recorder.Code, recorder.Body.String())
	}
}

// 无状态端点只支持 POST：GET / DELETE 按规范返回 405 且带 Allow 头。
func TestMCPHandlerStatelessRejectsNonPostMethods(t *testing.T) {
	h := mcpStatelessHandler()
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		req := httptest.NewRequest(method, "/admin/v2/mcp", nil)
		req.Header.Set("Authorization", "Bearer mct_valid")
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s 状态=%d，期望 405", method, recorder.Code)
		}
		if allow := recorder.Header().Get("Allow"); allow != http.MethodPost {
			t.Fatalf("%s 的 Allow 头=%q，期望 %q", method, allow, http.MethodPost)
		}
	}
}

// 回归：无状态化不得放宽鉴权——缺 bearer 仍 401。
func TestMCPHandlerStatelessStillRejectsMissingBearer(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/v2/mcp", strings.NewReader(mcpTestInitialize))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mcpStatelessHandler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("缺少 bearer 状态=%d，期望 401", recorder.Code)
	}
}
