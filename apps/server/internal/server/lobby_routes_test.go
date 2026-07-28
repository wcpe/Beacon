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
