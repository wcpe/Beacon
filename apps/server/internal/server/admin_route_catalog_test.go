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

// TestLookupAdminRouteDescriptorPrefersExactMatch 验证精确匹配优先于通配匹配。
// 构造两个互相通配的候选：字面路径与同段数通配路径同时命中同一个请求时，必须选字面那个，
// 否则新增一条通配路由就可能把既有的危险操作降级成更宽的能力要求。
func TestLookupAdminRouteDescriptorPrefersExactMatch(t *testing.T) {
	catalog := map[string]AdminRouteDescriptor{
		adminRouteKey(http.MethodPost, "/admin/v2/messages/{messageId}/payload"): {
			Operation: "content.read", Capability: auth.CapabilityManagementRead, Classification: routeDirect,
		},
		adminRouteKey(http.MethodPost, "/admin/v2/messages/broadcast/payload"): {
			Operation: "approval.request", Capability: auth.CapabilityApprovalRequest, Classification: routeApprovalRequired,
		},
	}
	got, ok := lookupAdminRouteDescriptor(catalog, http.MethodPost, "/admin/v2/messages/broadcast/payload")
	if !ok || got.Operation != "approval.request" || got.Classification != routeApprovalRequired {
		t.Fatalf("字面路径应精确命中更高危描述符，实际 %+v ok=%v", got, ok)
	}
	// 非字面路径仍走通配。
	got, ok = lookupAdminRouteDescriptor(catalog, http.MethodPost, "/admin/v2/messages/abc/payload")
	if !ok || got.Operation != "content.read" {
		t.Fatalf("非字面路径应走通配描述符，实际 %+v ok=%v", got, ok)
	}
}

// TestLookupAdminRouteDescriptorDeterministic 验证同段数通配路由重叠时结果确定且更具体者优先。
//
// 旧实现遍历 map 命中即返回，Go 的 map 迭代顺序随机，同一 (method, path) 会随调用在放行 / 403 之间抖动。
// 这里反复查询同一请求，断言每次结果一致，且字面段更多的候选胜出。
func TestLookupAdminRouteDescriptorDeterministic(t *testing.T) {
	catalog := map[string]AdminRouteDescriptor{
		// 字面段 2、通配段 2
		adminRouteKey(http.MethodGet, "/admin/v2/{scope}/{id}"): {
			Operation: "wide.read", Capability: auth.CapabilityManagementRead, Classification: routeDirect,
		},
		// 字面段 3、通配段 1：更具体
		adminRouteKey(http.MethodGet, "/admin/v2/messages/{id}"): {
			Operation: "content.read", Capability: auth.CapabilityManagementRead, Classification: routeDirect,
		},
		// 字面段 4：最具体
		adminRouteKey(http.MethodGet, "/admin/v2/messages/stats/{id}"): {
			Operation: "approval.request", Capability: auth.CapabilityApprovalRequest, Classification: routeApprovalRequired,
		},
	}
	cases := []struct{ path, operation string }{
		{"/admin/v2/messages/stats/abc", "approval.request"},
		{"/admin/v2/messages/abc", "content.read"},
		{"/admin/v2/other/abc", "wide.read"},
	}
	for _, tc := range cases {
		var first AdminRouteDescriptor
		for i := 0; i < 200; i++ {
			got, ok := lookupAdminRouteDescriptor(catalog, http.MethodGet, tc.path)
			if !ok {
				t.Fatalf("%s 应命中描述符（第 %d 次）", tc.path, i+1)
			}
			if i == 0 {
				first = got
				continue
			}
			if got != first {
				t.Fatalf("%s 同一请求返回了不同描述符：%+v vs %+v（第 %d 次）", tc.path, first, got, i+1)
			}
		}
		if first.Operation != tc.operation {
			t.Fatalf("%s 应选最具体的候选 %q，实际 %q", tc.path, tc.operation, first.Operation)
		}
	}
}

// TestAdminRouteDescriptorForRequestRealCatalogDeterministic 验证真实目录下同一请求的判定稳定，
// 且正文读取路径不会被同段数的其它路由抢走。
func TestAdminRouteDescriptorForRequestRealCatalogDeterministic(t *testing.T) {
	for i := 0; i < 200; i++ {
		for _, tc := range []struct{ method, path string }{
			{http.MethodGet, "/admin/v1/instances/lobby-1/logs"},
			{http.MethodGet, "/admin/v1/instances/lobby-1/browse"},
			{http.MethodPost, "/admin/v2/messages/m1/payload"},
			{http.MethodGet, "/admin/v1/instances/offline"},
		} {
			got, ok := adminRouteDescriptorForRequest(tc.method, tc.path)
			if !ok {
				t.Fatalf("%s %s 应命中描述符（第 %d 次）", tc.method, tc.path, i+1)
			}
			if i == 0 {
				continue
			}
			// 前 3 条属 content.read；/instances/offline 是字面路径，必须精确命中管理读。
			if strings.Contains(tc.path, "/logs") || strings.Contains(tc.path, "/browse") || strings.Contains(tc.path, "/payload") {
				if got.Operation != "content.read" {
					t.Fatalf("%s %s 应为 content.read，实际 %q（第 %d 次）", tc.method, tc.path, got.Operation, i+1)
				}
				continue
			}
			if got.Operation != "management.read" {
				t.Fatalf("%s %s 应为 management.read，实际 %q（第 %d 次）", tc.method, tc.path, got.Operation, i+1)
			}
		}
	}
}

// TestAdminRouteCatalogHasNoSameMethodAmbiguity 守护：真实目录内不得出现「同 method、同段数、
// 互相通配且描述符不同」的重叠对。
//
// 这类重叠是 B8 的根因：一旦存在，同一 (method, path) 在确定性匹配下会稳定选中更具体者，
// 但「更具体」未必是设计者想要的那条，属于需要人工裁决的目录错误。
// 当前目录实测无此情况；若将来新增通配路由引入重叠，本用例会直接失败并指出冲突对，
// 逼迫作者显式登记精确路径或调整描述符，而不是让授权结果取决于路由匹配的运气。
func TestAdminRouteCatalogHasNoSameMethodAmbiguity(t *testing.T) {
	type entry struct {
		key      string
		concrete string
		desc     AdminRouteDescriptor
		segments int
	}
	var entries []entry
	for key := range adminRouteCatalog {
		method, pattern, ok := strings.Cut(key, " ")
		if !ok {
			continue
		}
		parts := strings.Split(strings.Trim(pattern, "/"), "/")
		concrete := make([]string, len(parts))
		for i, part := range parts {
			if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
				concrete[i] = "x"
				continue
			}
			concrete[i] = part
		}
		entries = append(entries, entry{
			key: key, concrete: method + " /" + strings.Join(concrete, "/"),
			desc: adminRouteCatalog[key], segments: len(parts),
		})
	}
	for i := range entries {
		for j := i + 1; j < len(entries); j++ {
			a, b := entries[i], entries[j]
			if a.segments != b.segments || a.desc == b.desc {
				continue
			}
			aMethod, _, _ := strings.Cut(a.key, " ")
			bMethod, _, _ := strings.Cut(b.key, " ")
			if aMethod != bMethod {
				continue
			}
			_, aPath, _ := strings.Cut(a.concrete, " ")
			_, bPath, _ := strings.Cut(b.concrete, " ")
			_, aPattern, _ := strings.Cut(a.key, " ")
			_, bPattern, _ := strings.Cut(b.key, " ")
			if adminRoutePatternMatches(bPattern, aPath) || adminRoutePatternMatches(aPattern, bPath) {
				t.Fatalf("存在同 method 的歧义重叠路由，需人工裁决：%s (%+v) 与 %s (%+v)", a.key, a.desc, b.key, b.desc)
			}
		}
	}
}
