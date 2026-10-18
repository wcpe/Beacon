package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
)

// TestValidateAdminRouteCoverageRejectsUnknown 验证新增管理路由未登记时启动校验失败关闭。
func TestValidateAdminRouteCoverageRejectsUnknown(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/admin/v2/unclassified", func(http.ResponseWriter, *http.Request) {})
	if err := ValidateAdminRouteCoverage(r); err == nil || !strings.Contains(err.Error(), "未登记授权描述符") {
		t.Fatalf("未登记管理路由应拒绝启动，实际 %v", err)
	}
}

// TestAdminRouteCatalogSeparatesApproval 验证审批决定、直读正文与其余危险操作分类彼此分离。
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
	if pair.Operation != "content.read" || pair.Capability != auth.CapabilityManagementRead || pair.Classification != routeDirect {
		t.Fatalf("双侧文件读取应为 management.read 直执：%+v", pair)
	}
	reverse := adminRouteCatalog[adminRouteKey(http.MethodPost, "/admin/v1/reverse-fetch/tasks/{id}/conflicts/grants/{grantId}/consume")]
	if reverse.Operation != "approval.request" || reverse.Classification != routeApprovalRequired {
		t.Fatalf("反向抓取正文消费不得退化为普通直执：%+v", reverse)
	}
}

// TestAdminRouteCatalogContentReadsAreDirect 验证本次范围内的内容读取直接要求 management.read。
func TestAdminRouteCatalogContentReadsAreDirect(t *testing.T) {
	contentReads := []string{
		"POST /admin/v2/assets/preview", "POST /admin/v2/assets/diff",
		"POST /admin/v2/assets/pair-read/approval-requests", "POST /admin/v2/assets/pair-read/grants/{grantId}/consume",
		"POST /admin/v2/assets/preview/approval-requests", "POST /admin/v2/assets/preview/grants/{grantId}/consume",
		"POST /admin/v2/messages/{messageId}/payload", "POST /admin/v2/messages/{messageId}/payload/approval-requests",
		"POST /admin/v2/sensitive-access-grants/{grantId}/consume",
		"GET /admin/v1/instances/{serverId}/logs", "GET /admin/v1/instances/{serverId}/browse",
		"POST /admin/v1/instances/{serverId}/logs", "POST /admin/v1/instances/{serverId}/browse",
		"POST /admin/v1/instances/{serverId}/logs/grants/{grantId}/consume",
		"POST /admin/v1/instances/{serverId}/browse/grants/{grantId}/consume",
	}
	for _, key := range contentReads {
		descriptor, ok := adminRouteCatalog[key]
		if !ok || descriptor.Operation != "content.read" || descriptor.Capability != auth.CapabilityManagementRead || descriptor.Classification != routeDirect {
			t.Errorf("内容读取路由应为 content.read + management.read + direct：%s = %+v", key, descriptor)
		}
	}
	for _, tc := range []struct{ key, operation string }{
		{"POST /admin/v1/file-sync/tasks/{id}/start", "file-sync.task.start"},
		{"POST /admin/v1/file-sync/tasks/{id}/resume", "file-sync.task.resume"},
	} {
		descriptor, ok := adminRouteCatalog[tc.key]
		if !ok || descriptor.Operation != tc.operation || descriptor.Capability != auth.CapabilityManagementDirect || descriptor.Classification != routeDirect {
			t.Errorf("可恢复任务控制应保持 management.direct 直执：%s = %+v", tc.key, descriptor)
		}
	}
}

// TestAdminRouteCatalogPreservesDangerousApprovals 验证非读取危险操作仍维持审批分类。
func TestAdminRouteCatalogPreservesDangerousApprovals(t *testing.T) {
	approvalRequired := []string{
		"DELETE /admin/v2/namespaces/{id}", "DELETE /admin/v2/bc-clusters/{id}", "DELETE /admin/v2/regions/{id}",
		"DELETE /admin/v2/zones/{id}", "DELETE /admin/v2/config-files/{id}", "DELETE /admin/v2/change-orders/{id}",
		"POST /admin/v1/api-keys", "POST /admin/v1/api-keys/{id}/reset", "POST /admin/v2/mcp-clients",
		"POST /admin/v2/mcp-clients/{clientId}/rotate", "POST /admin/v2/namespaces/{id}/token/rotate", "POST /admin/v2/namespace-trusts",
		"POST /admin/v1/system/update", "POST /admin/v1/system/rollback", "PUT /admin/v1/settings/{key}",
		"POST /admin/v2/server-placement-transfers", "POST /admin/v2/server-assignments", "POST /admin/v2/server-rezones",
		"POST /admin/v2/agent-identities/{identityId}/unbind", "POST /admin/v2/agent-identities/{identityId}/enable",
		"POST /admin/v2/agent-identities/{identityId}/approve", "POST /admin/v2/agent-identities/{identityId}/allow-reapply",
		"POST /admin/v2/agent-identities/{identityId}/resolve-conflict", "POST /admin/v2/change-orders/{id}/approve",
		"POST /admin/v2/change-orders/{id}/rollback", "POST /admin/v2/change-orders/{id}/rollback/finish",
		"POST /admin/v2/config-files/{id}/purge", "POST /admin/v2/archive/jobs/{id}/retry",
		"POST /admin/v1/files/import", "POST /admin/v1/files/batch", "POST /admin/v1/configs/batch",
	}
	for _, key := range approvalRequired {
		descriptor, ok := adminRouteCatalog[key]
		if !ok || descriptor.Operation != "approval.request" || descriptor.Capability != auth.CapabilityApprovalRequest || descriptor.Classification != routeApprovalRequired {
			t.Errorf("危险操作审批分类被意外修改：%s = %+v", key, descriptor)
		}
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
