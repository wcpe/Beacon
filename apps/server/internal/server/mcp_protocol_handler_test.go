package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
)

type mcpIssuerStub struct{ called bool }

func (s *mcpIssuerStub) IssueAccessTokenFrom(clientID, clientSecret, audience, scope, _ string) (string, time.Duration, auth.Principal, error) {
	s.called = clientID == "mcp_client" && clientSecret == "mcs_secret" && audience == "https://beacon.example/admin/v2/mcp" && scope == "observer"
	return "mct_token", 15 * time.Minute, auth.MCPPrincipal(clientID, "自动化", "automation"), nil
}

func newMCPProtocolTestHandler(t *testing.T) (*MCPProtocolHandler, *mcpIssuerStub) {
	t.Helper()
	policy, err := NewMCPProxyPolicy(true, "https://beacon.example", []string{"192.0.2.0/24"})
	if err != nil {
		t.Fatalf("构造受信反代策略失败: %v", err)
	}
	issuer := &mcpIssuerStub{}
	return NewMCPProtocolHandler(policy, issuer, mcpVerifierStub{principal: auth.MCPPrincipal("mcp_client", "自动化", "automation")}, nil), issuer
}

func trustedMCPRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = "192.0.2.7:443"
	req.Host = "beacon.example"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "beacon.example")
	return req
}

func TestMCPProtocolTokenRequiresTrustedProxyAndFixedAudience(t *testing.T) {
	h, issuer := newMCPProtocolTestHandler(t)
	form := "grant_type=client_credentials&client_id=mcp_client&client_secret=mcs_secret&audience=https%3A%2F%2Fbeacon.example%2Fadmin%2Fv2%2Fmcp&scope=observer"
	req := trustedMCPRequest(http.MethodPost, "/admin/v2/oauth/token", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	h.Token(recorder, req)
	if recorder.Code != http.StatusOK || !issuer.called || !strings.Contains(recorder.Body.String(), "mct_token") {
		t.Fatalf("合法 token 请求不符: status=%d body=%s called=%v", recorder.Code, recorder.Body.String(), issuer.called)
	}

	issuer.called = false
	spoofed := trustedMCPRequest(http.MethodPost, "/admin/v2/oauth/token", strings.NewReader(form))
	spoofed.RemoteAddr = "203.0.113.8:443"
	spoofed.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder = httptest.NewRecorder()
	h.Token(recorder, spoofed)
	if recorder.Code != http.StatusUnauthorized || issuer.called {
		t.Fatalf("非受信来源伪造转发头必须失败关闭: status=%d called=%v", recorder.Code, issuer.called)
	}
}

func TestMCPProtocolMetadataPublishesFixedResourceOnlyThroughTrustedProxy(t *testing.T) {
	h, _ := newMCPProtocolTestHandler(t)
	req := trustedMCPRequest(http.MethodGet, "/.well-known/oauth-protected-resource/admin/v2/mcp", nil)
	recorder := httptest.NewRecorder()
	h.Metadata(recorder, req)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "https://beacon.example/admin/v2/mcp") {
		t.Fatalf("resource metadata 不符: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	req.Header.Set("X-Forwarded-Host", "attacker.example")
	recorder = httptest.NewRecorder()
	h.Metadata(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("错误转发 host 必须失败关闭: %d", recorder.Code)
	}
}

// mcpIssuerErrStub 让 token 端点按预设错误返回，用于验证 handler 的 OAuth 错误映射。
type mcpIssuerErrStub struct{ err error }

func (s *mcpIssuerErrStub) IssueAccessTokenFrom(string, string, string, string, string) (string, time.Duration, auth.Principal, error) {
	return "", 0, auth.Principal{}, s.err
}

// TestMCPProtocolTokenMapsOAuthErrorsByCause 验证 token 端点按失败原因回写 OAuth 规范错误码，
// 而不是把参数缺失与 scope 越权一律压成 401 invalid_client。
//
// 动机：service 层已按原因记录 invalid_request / invalid_scope 审计，若响应统一回 invalid_client，
// 外部集成方会去反复核对 client_secret，而真实原因是漏参数或 scope 写错。
func TestMCPProtocolTokenMapsOAuthErrorsByCause(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantError  string
	}{
		{"缺参数", apperr.ErrOAuthInvalidRequest, http.StatusBadRequest, "invalid_request"},
		{"scope 越权", apperr.ErrOAuthInvalidScope, http.StatusBadRequest, "invalid_scope"},
		{"凭证错误", apperr.ErrAdminUnauthorized, http.StatusUnauthorized, "invalid_client"},
		{"未预期错误", errors.New("boom"), http.StatusUnauthorized, "invalid_client"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := NewMCPProxyPolicy(true, "https://beacon.example", []string{"192.0.2.0/24"})
			if err != nil {
				t.Fatalf("构造策略失败: %v", err)
			}
			h := NewMCPProtocolHandler(policy, &mcpIssuerErrStub{err: tc.err}, mcpVerifierStub{}, nil)
			form := "grant_type=client_credentials&client_id=mcp_client&client_secret=mcs_secret&audience=https%3A%2F%2Fbeacon.example%2Fadmin%2Fv2%2Fmcp"
			req := trustedMCPRequest(http.MethodPost, "/admin/v2/oauth/token", strings.NewReader(form))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			h.Token(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("状态码应为 %d，实际 %d（body=%s）", tc.wantStatus, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"error":"`+tc.wantError+`"`) {
				t.Fatalf("应回 error=%q，实际 %s", tc.wantError, rec.Body.String())
			}
		})
	}
}
