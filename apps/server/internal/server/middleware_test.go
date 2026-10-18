package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestIsWriteMethod 校验写方法判定：只读拒写裁决据此放行 GET/HEAD/OPTIONS、拦 POST/PUT/PATCH/DELETE。
func TestIsWriteMethod(t *testing.T) {
	cases := []struct {
		method string
		want   bool
	}{
		{http.MethodGet, false},
		{http.MethodHead, false},
		{http.MethodOptions, false},
		{http.MethodPost, true},
		{http.MethodPut, true},
		{http.MethodPatch, true},
		{http.MethodDelete, true},
	}
	for _, c := range cases {
		if got := isWriteMethod(c.method); got != c.want {
			t.Fatalf("isWriteMethod(%q) = %v，期望 %v", c.method, got, c.want)
		}
	}
}

// TestContentReadCapabilityAllowsReadOnlyPost 验证内容读取 POST 通过 management.read 能力放行只读主体。
func TestContentReadCapabilityAllowsReadOnlyPost(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		principal  auth.Principal
		wantStatus int
	}{
		{name: "只读主体具有 management.read", path: "/admin/v2/assets/preview", principal: auth.APIKeyPrincipal("ro", "只读", model.RoleReadonly, ""), wantStatus: http.StatusOK},
		{name: "缺少 management.read 时拒绝", path: "/admin/v2/assets/preview", principal: auth.APIKeyPrincipal("ro-no-read", "只读", model.RoleReadonly, "").RemoveCapability(auth.CapabilityManagementRead), wantStatus: http.StatusForbidden},
		{name: "其他 direct 写操作仍受只读限制", path: "/admin/v1/file-sync/tasks/7/start", principal: auth.APIKeyPrincipal("ro", "只读", model.RoleReadonly, ""), wantStatus: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := readonlyWriteGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
			r := httptest.NewRequest(http.MethodPost, tc.path, nil)
			r = r.WithContext(auth.WithPrincipal(r.Context(), tc.principal))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.wantStatus {
				t.Fatalf("状态码 = %d，期望 %d", w.Code, tc.wantStatus)
			}
		})
	}
}

// TestRequireFullRole 校验 full-only 守卫（FR-110）：full 放行、readonly 403、无角色 403。
// 用于 GET 但有写副作用的受限入口，readonlyWriteGuard 放过 GET、须显式挂本守卫。
func TestRequireFullRole(t *testing.T) {
	cases := []struct {
		name       string
		role       string
		setRole    bool
		wantStatus int
		wantNext   bool
	}{
		{name: "full 放行", role: model.RoleFull, setRole: true, wantStatus: http.StatusOK, wantNext: true},
		{name: "readonly 拒", role: model.RoleReadonly, setRole: true, wantStatus: http.StatusForbidden, wantNext: false},
		{name: "无角色拒", setRole: false, wantStatus: http.StatusForbidden, wantNext: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			nextCalled := false
			h := requireFullRole(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusOK)
			}))
			r := httptest.NewRequest(http.MethodGet, "/admin/v1/instances/lobby-1/browse?namespace=prod", nil)
			if c.setRole {
				r = r.WithContext(auth.WithRole(r.Context(), c.role))
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.wantStatus {
				t.Fatalf("状态码 = %d，期望 %d", w.Code, c.wantStatus)
			}
			if nextCalled != c.wantNext {
				t.Fatalf("next 调用 = %v，期望 %v", nextCalled, c.wantNext)
			}
		})
	}
}
