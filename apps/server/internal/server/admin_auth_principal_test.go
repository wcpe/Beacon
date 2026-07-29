package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// principalVerifierStub 是管理面 API key 鉴权的主体返回替身。
type principalVerifierStub struct {
	principal auth.Principal
	err       error
}

func (s principalVerifierStub) Verify(_ string) (auth.Principal, error) {
	return s.principal, s.err
}

// TestAdminAuthMiddlewareInjectsPrincipal 验证登录令牌与 API key 都会注入完整主体，并保持旧 operator/role 兼容。
func TestAdminAuthMiddlewareInjectsPrincipal(t *testing.T) {
	authn, err := auth.New("alice", "pass", "secret", time.Hour)
	if err != nil {
		t.Fatalf("构造认证器失败: %v", err)
	}
	token, err := authn.Login("alice", "pass")
	if err != nil {
		t.Fatalf("签发令牌失败: %v", err)
	}
	assertPrincipalInjected(t, authn, principalVerifierStub{}, "Authorization", "Bearer "+token, auth.SourceLogin, "alice", model.RoleFull)

	apiPrincipal := auth.Principal{ID: "apikey:ci", Operator: "apikey:ci", Source: auth.SourceAPIKey, Role: model.RoleReadonly}
	assertPrincipalInjected(t, authn, principalVerifierStub{principal: apiPrincipal}, apiKeyHeader, "bk_test", auth.SourceAPIKey, "apikey:ci", model.RoleReadonly)
}

// TestAdminAuthMiddlewareRejectsMultipleCredentials 验证同请求出现多份管理面凭据时失败关闭。
func TestAdminAuthMiddlewareRejectsMultipleCredentials(t *testing.T) {
	authn, err := auth.New("alice", "pass", "secret", time.Hour)
	if err != nil {
		t.Fatalf("构造认证器失败: %v", err)
	}
	token, err := authn.Login("alice", "pass")
	if err != nil {
		t.Fatalf("签发令牌失败: %v", err)
	}

	called := false
	h := adminAuthMiddleware(authn, principalVerifierStub{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/api-keys", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(apiKeyHeader, "bk_other")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("多凭据应返回 401，实际 %d", w.Code)
	}
	if called {
		t.Fatal("多凭据请求不应进入后续处理器")
	}
}

func assertPrincipalInjected(t *testing.T, authn *auth.Authenticator, verifier APIKeyVerifier, header, value, source, operator, role string) {
	t.Helper()
	h := adminAuthMiddleware(authn, verifier)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.FromContext(r.Context())
		if !ok {
			t.Fatal("应注入完整认证主体")
		}
		if p.Source != source || p.Operator != operator || p.Role != role {
			t.Fatalf("主体字段不符：%+v", p)
		}
		if auth.Operator(r.Context()) != operator || auth.Role(r.Context()) != role {
			t.Fatalf("旧 operator/role 读取不兼容：operator=%q role=%q", auth.Operator(r.Context()), auth.Role(r.Context()))
		}
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/api-keys", nil)
	req.Header.Set(header, value)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("鉴权应通过，实际 %d", w.Code)
	}
}
