package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestValidateAdminRouteCoverageRejectsUnknown 验证新增管理路由未登记时启动校验失败关闭。
func TestValidateAdminRouteCoverageRejectsUnknown(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/admin/v2/unclassified", func(http.ResponseWriter, *http.Request) {})
	if err := ValidateAdminRouteCoverage(r); err == nil || !strings.Contains(err.Error(), "未登记授权描述符") {
		t.Fatalf("未登记管理路由应拒绝启动，实际 %v", err)
	}
}

// TestAdminRouteCatalogSeparatesApproval 验证审批决定与危险兼容入口不会退化为普通直执。
func TestAdminRouteCatalogSeparatesApproval(t *testing.T) {
	approve := adminRouteCatalog[adminRouteKey(http.MethodPost, "/admin/v2/approval-requests/{requestId}/approve")]
	if approve.Operation != "approval.decide" || approve.Classification != routeDirect {
		t.Fatalf("审批决定分类不符：%+v", approve)
	}
	dangerous := adminRouteCatalog[adminRouteKey(http.MethodPost, "/admin/v1/system/update")]
	if dangerous.Classification != routeApprovalRequired {
		t.Fatalf("危险更新不应直执：%+v", dangerous)
	}
	pair := adminRouteCatalog[adminRouteKey(http.MethodPost, "/admin/v2/assets/pair-read/grants/{grantId}/consume")]
	if pair.Operation != "approval.request" || pair.Classification != routeApprovalRequired {
		t.Fatalf("双侧读取消费不得退化为普通直执：%+v", pair)
	}
}

// TestAdminRouteCatalogRejectsRedundancy 验证精确目录检查拒绝未装配的残留描述符。
func TestAdminRouteCatalogRejectsRedundancy(t *testing.T) {
	key := adminRouteKey(http.MethodGet, "/admin/v2/known")
	catalog := map[string]AdminRouteDescriptor{
		key: {Operation: "known", Capability: "management.read", Classification: routeDirect},
		adminRouteKey(http.MethodGet, "/admin/v2/stale"): {Operation: "stale", Capability: "management.read", Classification: routeDirect},
	}
	routes := map[string]struct{}{key: {}}
	if err := validateAdminRouteCatalog(routes, catalog, true); err == nil || !strings.Contains(err.Error(), "冗余描述符") {
		t.Fatalf("冗余目录项应拒绝，实际 %v", err)
	}
}

// TestMergeRouteCatalogRejectsDuplicate 验证重复 method+RoutePattern 不会被静默覆盖。
func TestMergeRouteCatalogRejectsDuplicate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("重复目录项应触发失败")
		}
	}()
	entry := route("test", "management.read", routeDirect, http.MethodGet, "/admin/v2/duplicate")
	_ = mergeRouteCatalog(entry, entry)
}
