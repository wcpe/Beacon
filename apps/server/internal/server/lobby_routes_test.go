package server

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/handler"
)

// TestLobbyClusterRoutesRegistered 确保大厅查询与迁移路由注册到管理面。
func TestLobbyClusterRoutesRegistered(t *testing.T) {
	routes, ok := NewRouter(Handlers{
		V2:  &handler.V2ControlPlaneHandler{},
		Web: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	}, "", nil, nil, nil).(chi.Routes)
	if !ok {
		t.Fatal("路由器应实现 chi.Routes")
	}

	want := map[string]struct{}{
		"GET /admin/v2/lobby-clusters":              {},
		"GET /admin/v2/lobby-clusters/{id}":         {},
		"POST /admin/v2/server-placement-transfers": {},
	}
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		delete(want, method+" "+route)
		return nil
	}); err != nil {
		t.Fatalf("遍历路由失败: %v", err)
	}
	for route := range want {
		t.Errorf("缺少大厅管理路由 %s", route)
	}
}

// TestServerDeleteRoutesNotRegistered 锁定 V1/V2 管理面不存在可绕过归档流程的 server 硬删除入口。
func TestServerDeleteRoutesNotRegistered(t *testing.T) {
	routes, ok := NewRouter(Handlers{
		V2:  &handler.V2ControlPlaneHandler{},
		Web: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	}, "", nil, nil, nil).(chi.Routes)
	if !ok {
		t.Fatal("路由器应实现 chi.Routes")
	}
	forbidden := map[string]struct{}{
		"DELETE /admin/v1/servers/{id}":          {},
		"DELETE /admin/v1/instances/{serverId}":  {},
		"DELETE /admin/v2/servers/{id}":          {},
		"DELETE /admin/v2/servers/{serverId}":    {},
		"DELETE /admin/v2/servers/{serverRowId}": {},
	}
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := method + " " + route
		if _, exists := forbidden[key]; exists {
			t.Errorf("发现禁止的 server 硬删除路由 %s", key)
		}
		return nil
	}); err != nil {
		t.Fatalf("遍历路由失败: %v", err)
	}
}
