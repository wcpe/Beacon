package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/handler"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// namespaceDeleteAPIKeyVerifier 是旧删除路由测试的固定角色鉴权替身。
type namespaceDeleteAPIKeyVerifier struct{}

func (namespaceDeleteAPIKeyVerifier) Verify(rawKey string) (auth.Principal, error) {
	if rawKey == "readonly" {
		return auth.Principal{ID: "readonly-user", Operator: "readonly-user", Source: auth.SourceAPIKey, Role: model.RoleReadonly}, nil
	}
	return auth.Principal{ID: "full-user", Operator: "full-user", Source: auth.SourceAPIKey, Role: model.RoleFull}, nil
}

// TestLegacyNamespaceDeleteRoutesMigrated 验证 V1/V2 旧删除路由返回迁移错误、不会产生兜底审计，且 readonly 仍由原有守卫拒绝。
func TestLegacyNamespaceDeleteRoutesMigrated(t *testing.T) {
	authn, err := auth.New("tester", "test-pass", "test-secret", time.Hour)
	if err != nil {
		t.Fatalf("构造认证器失败: %v", err)
	}
	token, err := authn.Login("tester", "test-pass")
	if err != nil {
		t.Fatalf("签发测试令牌失败: %v", err)
	}
	audit := &recordingAuditCreator{}
	router := NewRouter(Handlers{
		Namespace: &handler.NamespaceHandler{},
		V2:        &handler.V2ControlPlaneHandler{},
		Web:       http.NotFoundHandler(),
	}, "", authn, namespaceDeleteAPIKeyVerifier{}, audit)

	for _, path := range []string{"/admin/v1/namespaces/legacy", "/admin/v2/namespaces/1"} {
		t.Run(path, func(t *testing.T) {
			fullRequest := httptest.NewRequest(http.MethodDelete, path, nil)
			fullRequest.Header.Set("Authorization", "Bearer "+token)
			fullResponse := httptest.NewRecorder()
			router.ServeHTTP(fullResponse, fullRequest)
			assertNamespaceDeleteMigrated(t, fullResponse)

			readonlyRequest := httptest.NewRequest(http.MethodDelete, path, nil)
			readonlyRequest.Header.Set(apiKeyHeader, "readonly")
			readonlyResponse := httptest.NewRecorder()
			router.ServeHTTP(readonlyResponse, readonlyRequest)
			if readonlyResponse.Code != http.StatusForbidden {
				t.Fatalf("只读密钥应由原有写守卫拒绝为 403，实际 %d", readonlyResponse.Code)
			}
		})
	}
	if entries := audit.all(); len(entries) != 0 {
		t.Fatalf("旧删除迁移端点不应产生审计，实际 %d 条", len(entries))
	}
}

func assertNamespaceDeleteMigrated(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("解析迁移错误响应失败: %v", err)
	}
	if response.Code != http.StatusGone || body["code"] != "namespace_delete_migrated" {
		t.Fatalf("旧删除应返回 410 namespace_delete_migrated，实际 %d：%v", response.Code, body)
	}
}
