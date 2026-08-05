package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
)

type mcpVerifierStub struct {
	principal auth.Principal
	err       error
}

func (s mcpVerifierStub) VerifyAccessToken(_, _ string) (auth.Principal, error) {
	return s.principal, s.err
}

func TestMCPHandlerRejectsMissingOrInvalidBearer(t *testing.T) {
	h := NewMCPHandler(mcpVerifierStub{principal: auth.MCPPrincipal("client", "自动化", "automation")}, "https://beacon.example/admin/v2/mcp")
	for _, header := range []string{"", "Bearer bk_not_mcp"} {
		req := httptest.NewRequest(http.MethodPost, "/admin/v2/mcp", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		if header != "" {
			h = NewMCPHandler(mcpVerifierStub{err: apperr.ErrAdminUnauthorized}, "https://beacon.example/admin/v2/mcp")
		}
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("Authorization=%q 状态=%d，期望 401", header, recorder.Code)
		}
	}
}

func TestMCPHandlerAcceptsMCPBearerBeforeProtocolHandling(t *testing.T) {
	h := NewMCPHandler(mcpVerifierStub{principal: auth.MCPPrincipal("client", "自动化", "automation")}, "https://beacon.example/admin/v2/mcp")
	req := httptest.NewRequest(http.MethodPost, "/admin/v2/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
	req.Header.Set("Authorization", "Bearer mct_valid")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Accept", "text/event-stream")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("合法 MCP 初始化状态=%d，响应=%s", recorder.Code, recorder.Body.String())
	}
}
