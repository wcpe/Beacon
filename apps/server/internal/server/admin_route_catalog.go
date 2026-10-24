package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
)

const (
	routeDirect           = "direct"
	routeApprovalRequired = "approval_required"
)

// AdminRouteDescriptor 描述管理面路由的语义授权分类。
type AdminRouteDescriptor struct {
	Operation      string
	Capability     string
	Classification string
}

// ValidateAdminRouteCoverage 验证所有已装配管理路由都在静态目录中登记。
func ValidateAdminRouteCoverage(routes chi.Routes) error {
	return validateAdminRouteCatalog(walkedAdminRoutes(routes), adminRouteCatalog, false)
}

func validateAdminRouteCatalog(routes map[string]struct{}, catalog map[string]AdminRouteDescriptor, exact bool) error {
	for key := range routes {
		if _, ok := catalog[key]; !ok {
			return fmt.Errorf("管理路由未登记授权描述符：%s", key)
		}
	}
	if exact {
		for key := range catalog {
			if _, ok := routes[key]; !ok {
				return fmt.Errorf("管理路由目录存在冗余描述符：%s", key)
			}
		}
	}
	return nil
}

func walkedAdminRoutes(routes chi.Routes) map[string]struct{} {
	found := map[string]struct{}{}
	_ = chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/admin/v1/") || strings.HasPrefix(route, "/admin/v2/") {
			found[adminRouteKey(method, route)] = struct{}{}
		}
		return nil
	})
	return found
}

func adminRouteKey(method, route string) string { return method + " " + route }

// adminRouteDescriptorForRequest 按确定性优先级判定请求归属的授权描述符。
func adminRouteDescriptorForRequest(method, requestPath string) (AdminRouteDescriptor, bool) {
	return lookupAdminRouteDescriptor(adminRouteCatalog, method, requestPath)
}

// adminRouteCandidate 是通配匹配的候选与其确定性排序键。
type adminRouteCandidate struct {
	key        string
	descriptor AdminRouteDescriptor
	literals   int // 字面段数量（越多越具体）
	wildcards  int // 通配段数量（越少越具体）
}

// moreSpecificThan 判断本候选是否应优先于另一候选（用于从多个通配命中里挑唯一结果）。
//
// 排序键依次为：字面段多者优先 → 通配段少者优先 → key 字典序小者优先。
// 最后一级是**必需的**：前两级完全相同时（例如 /a/{x} 与 /a/{y}）若没有稳定兜底，
// map 遍历顺序会让同一请求在不同次调用得到不同描述符。
func (c adminRouteCandidate) moreSpecificThan(other adminRouteCandidate) bool {
	if c.literals != other.literals {
		return c.literals > other.literals
	}
	if c.wildcards != other.wildcards {
		return c.wildcards < other.wildcards
	}
	return c.key < other.key
}

// lookupAdminRouteDescriptor 在给定路由目录中查找 (method, path) 对应的授权描述符。
//
// 为什么必须确定性：旧实现直接 `for key, descriptor := range catalog` 命中即返回，
// 而 Go map 迭代顺序随机——一旦出现「同段数、互相通配」的重叠路由（例如新增通配路由与
// content.read 路径重叠），同一请求的授权结果就会在放行与 403 之间随机。
// 现改为：精确匹配优先 → 再按具体度排序取唯一最优（见 moreSpecificThan）。
func lookupAdminRouteDescriptor(catalog map[string]AdminRouteDescriptor, method, requestPath string) (AdminRouteDescriptor, bool) {
	// 一级：精确匹配（path 与登记 pattern 完全一致）直接命中，避免被更宽的通配路由抢走。
	if descriptor, ok := catalog[adminRouteKey(method, requestPath)]; ok {
		return descriptor, true
	}
	var best adminRouteCandidate
	found := false
	for key, descriptor := range catalog {
		routeMethod, pattern, ok := strings.Cut(key, " ")
		if !ok || routeMethod != method || !adminRoutePatternMatches(pattern, requestPath) {
			continue
		}
		literals, wildcards := adminRoutePatternSpecificity(pattern)
		candidate := adminRouteCandidate{key: key, descriptor: descriptor, literals: literals, wildcards: wildcards}
		if !found || candidate.moreSpecificThan(best) {
			best, found = candidate, true
		}
	}
	if !found {
		return AdminRouteDescriptor{}, false
	}
	return best.descriptor, true
}

// adminRoutePatternSpecificity 统计 pattern 的字面段与通配段数量（"{name}" 段计为通配）。
func adminRoutePatternSpecificity(pattern string) (literals, wildcards int) {
	for _, part := range strings.Split(strings.Trim(pattern, "/"), "/") {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			wildcards++
			continue
		}
		literals++
	}
	return literals, wildcards
}

func adminRoutePatternMatches(pattern, requestPath string) bool {
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	pathParts := strings.Split(strings.Trim(requestPath, "/"), "/")
	if len(patternParts) != len(pathParts) {
		return false
	}
	for i, patternPart := range patternParts {
		if strings.HasPrefix(patternPart, "{") && strings.HasSuffix(patternPart, "}") {
			if pathParts[i] == "" {
				return false
			}
			continue
		}
		if patternPart != pathParts[i] {
			return false
		}
	}
	return true
}

func route(operation, capability, classification string, method string, paths ...string) map[string]AdminRouteDescriptor {
	entries := make(map[string]AdminRouteDescriptor, len(paths))
	for _, path := range paths {
		entries[adminRouteKey(method, path)] = AdminRouteDescriptor{Operation: operation, Capability: capability, Classification: classification}
	}
	return entries
}

func mergeRouteCatalog(groups ...map[string]AdminRouteDescriptor) map[string]AdminRouteDescriptor {
	merged := map[string]AdminRouteDescriptor{}
	for _, group := range groups {
		for key, descriptor := range group {
			if _, exists := merged[key]; exists {
				panic("管理路由目录存在重复描述符：" + key)
			}
			merged[key] = descriptor
		}
	}
	return merged
}

var adminRouteCatalog = mergeRouteCatalog(
	route("mcp.protocol", "", routeDirect, http.MethodPost, "/admin/v2/mcp", "/admin/v2/oauth/token"),
	route("mcp.protocol", "", routeDirect, http.MethodGet, "/admin/v2/mcp"),
	route("auth.login", "", routeDirect, http.MethodPost, "/admin/v1/auth/login"),
	route("management.read", auth.CapabilityManagementRead, routeDirect, http.MethodGet,
		"/admin/v1/namespaces", "/admin/v1/configs", "/admin/v1/configs/effective", "/admin/v1/configs/gray", "/admin/v1/configs/impact", "/admin/v1/configs/{id}", "/admin/v1/configs/{id}/revisions", "/admin/v1/configs/{id}/revisions/{version}", "/admin/v1/configs/{id}/diff",
		"/admin/v1/files", "/admin/v1/files/effective", "/admin/v1/files/{id}", "/admin/v1/files/{id}/revisions", "/admin/v1/files/{id}/revisions/{version}",
		"/admin/v1/override-sets", "/admin/v1/override-sets/{id}", "/admin/v1/override-sets/{id}/revisions", "/admin/v1/override-sets/{id}/dry-run",
		"/admin/v1/instances", "/admin/v1/instances/offline", "/admin/v1/instances/{serverId}", "/admin/v1/instances/{serverId}/config-timeline",
		"/admin/v1/reverse-fetch/tasks", "/admin/v1/reverse-fetch/tasks/{id}", "/admin/v1/reverse-fetch/tasks/{id}/conflicts", "/admin/v1/reverse-fetch/tasks/{id}/conflicts/diff", "/admin/v1/reverse-fetch/tasks/{id}/conflicts/grants/{grantId}/consume", "/admin/v1/reverse-fetch/ignore-rules",
		"/admin/v1/imprints/{commandId}", "/admin/v1/imprints/{commandId}/diff", "/admin/v1/file-sync/tasks", "/admin/v1/file-sync/tasks/{id}", "/admin/v1/file-sync/tasks/{id}/events",
		"/admin/v1/topology", "/admin/v1/alerts", "/admin/v1/alert-events", "/admin/v1/zones/assignments", "/admin/v1/zones/default-entry", "/admin/v1/zones", "/admin/v1/scheduling/placement", "/admin/v1/scheduling/drains", "/admin/v1/audits", "/admin/v1/audits/analytics", "/admin/v1/audits/export", "/admin/v1/api-keys", "/admin/v1/metrics/summary", "/admin/v1/metrics/trend", "/admin/v1/system/status", "/admin/v1/system/observability", "/admin/v1/commands", "/admin/v1/commands/analytics", "/admin/v1/system/update-check", "/admin/v1/system/update", "/admin/v1/system/proxy-test", "/admin/v1/settings", "/admin/v1/reversible-operations",
		"/admin/v2/namespaces", "/admin/v2/namespaces/{id}/lifecycle-impact", "/admin/v2/namespaces/{id}/permanent-deletion-impact", "/admin/v2/namespace-trusts", "/admin/v2/envs", "/admin/v2/agent-identities", "/admin/v2/agent-identities/{identityId}", "/admin/v2/mcp-clients", "/admin/v2/mcp-clients/{clientId}", "/admin/v2/mcp/config", "/admin/v2/mcp/invocations", "/admin/v2/mcp/invocations/{invocationId}", "/admin/v2/metrics/summary", "/admin/v2/metrics/series", "/admin/v2/health", "/admin/v2/health/snapshots", "/admin/v2/health/{serverId}", "/admin/v2/settings/health-weights", "/admin/v2/agent-settings", "/admin/v2/zone-tree", "/admin/v1/alert-events/{id}/context", "/admin/v2/servers", "/admin/v2/servers/{id}/lifecycle-impact", "/admin/v2/servers/{id}/permanent-deletion-impact", "/admin/v2/lobby-clusters", "/admin/v2/lobby-clusters/{id}", "/admin/v2/shared-pools", "/admin/v2/shared-pools/{id}", "/admin/v2/sched-decisions", "/admin/v2/sched-decisions/summary", "/admin/v2/sched-decisions/{traceId}", "/admin/v2/connections", "/admin/v2/connections/stats", "/admin/v2/connections/{connId}", "/admin/v2/messages", "/admin/v2/messages/stats", "/admin/v2/messages/{messageId}", "/admin/v2/archive/overview", "/admin/v2/archive/jobs", "/admin/v2/archive/jobs/{id}", "/admin/v2/config-files/trash", "/admin/v2/config-files", "/admin/v2/config-files/{id}", "/admin/v2/config-files/{id}/scopes", "/admin/v2/config-files/{id}/versions", "/admin/v2/config-files/{id}/effective", "/admin/v2/config-files/{id}/diff", "/admin/v2/config-versions/{versionId}", "/admin/v2/assets", "/admin/v2/assets/scan-status", "/admin/v2/assets/compare", "/admin/v2/assets/sensitive-rules", "/admin/v2/change-orders", "/admin/v2/change-orders/{id}", "/admin/v2/change-orders/{id}/impact", "/admin/v2/change-orders/{id}/targets", "/admin/v2/change-orders/{id}/observe", "/admin/v2/change-orders/{id}/events", "/admin/v2/change-orders/{id}/items/{itemId}/file-diff", "/admin/v2/change-orders/{id}/rollback-records", "/admin/v2/change-orders/delivered-versions"),
	route("approval.read", auth.CapabilityApprovalRead, routeDirect, http.MethodGet, "/admin/v2/approval-requests", "/admin/v2/approval-requests/{requestId}", "/admin/v2/approvals", "/admin/v2/approvals/{id}"),
	route("content.read", auth.CapabilityManagementRead, routeDirect, http.MethodGet, "/admin/v1/instances/{serverId}/logs", "/admin/v1/instances/{serverId}/browse"),
	route("management.direct", auth.CapabilityManagementDirect, routeDirect, http.MethodPost, "/admin/v1/auth/logout", "/admin/v1/namespaces", "/admin/v1/configs", "/admin/v1/files", "/admin/v1/override-sets", "/admin/v1/reverse-fetch/ignore-rules", "/admin/v1/file-sync/tasks", "/admin/v1/file-sync/tasks/{id}/plan", "/admin/v1/alert-events/{id}/handle", "/admin/v1/alert-events/handle", "/admin/v1/alert-events/{id}/level", "/admin/v1/system/update/cancel", "/admin/v1/instances/{serverId}/offline", "/admin/v1/file-sync/tasks/{id}/pause", "/admin/v1/file-sync/tasks/{id}/terminate", "/admin/v1/reverse-fetch/tasks/{id}/cancel", "/admin/v1/reversible-operations/{id}/undo", "/admin/v2/namespaces", "/admin/v2/envs", "/admin/v2/mcp-clients/{clientId}/revoke", "/admin/v2/bc-clusters", "/admin/v2/regions", "/admin/v2/zones", "/admin/v2/namespace-trusts/{id}/revoke", "/admin/v2/archive/jobs", "/admin/v2/archive/jobs/{id}/cancel", "/admin/v2/config-files", "/admin/v2/config-files/{id}/versions", "/admin/v2/config-files/{id}/validate", "/admin/v2/change-orders", "/admin/v2/change-orders/{id}/diff-scan", "/admin/v2/change-orders/{id}/withdraw", "/admin/v2/change-orders/{id}/pause", "/admin/v2/change-orders/{id}/cancel", "/admin/v2/agent-identities/{identityId}/reject", "/admin/v2/agent-identities/{identityId}/disable"),
	route("management.direct", auth.CapabilityManagementDirect, routeDirect, http.MethodPatch, "/admin/v1/namespaces/{code}", "/admin/v2/namespaces/{id}", "/admin/v2/envs/{id}", "/admin/v2/bc-clusters/{id}", "/admin/v2/regions/{id}", "/admin/v2/zones/{id}", "/admin/v2/servers/{id}", "/admin/v2/config-files/{id}", "/admin/v2/change-orders/{id}"),
	route("management.direct", auth.CapabilityManagementDirect, routeDirect, http.MethodPut, "/admin/v1/namespaces/{code}", "/admin/v1/scheduling/drains", "/admin/v2/envs/{id}/namespaces", "/admin/v2/agent-identities/{identityId}/endpoints/{endpointKey}", "/admin/v2/settings/health-weights", "/admin/v2/agent-settings", "/admin/v2/assets/sensitive-rules", "/admin/v2/servers/{serverId}/tags"),
	route("management.direct", auth.CapabilityManagementDirect, routeDirect, http.MethodDelete, "/admin/v1/reverse-fetch/ignore-rules/{id}", "/admin/v1/configs/{id}/gray", "/admin/v1/instances/{serverId}/offline", "/admin/v2/envs/{id}", "/admin/v2/config-files/{id}/scopes/{scopeLevel}/{scopeRefId}", "/admin/v2/servers/{serverId}/tags/{key}"),
	route("approval.decide", auth.CapabilityApprovalDecide, routeDirect, http.MethodPost, "/admin/v2/approval-requests/{requestId}/approve", "/admin/v2/approval-requests/{requestId}/reject", "/admin/v2/approvals/{id}/approve", "/admin/v2/approvals/{id}/reject"),
	route("approval.withdraw.own", auth.CapabilityApprovalWithdrawOwn, routeDirect, http.MethodPost, "/admin/v2/approval-requests/{requestId}/withdraw", "/admin/v2/approvals/{id}/withdraw"),
	route("credential.secret.redeem", auth.CapabilityApprovalRead, routeDirect, http.MethodPost, "/admin/v2/approval-requests/{requestId}/credential-secret/redeem", "/admin/v2/approvals/{id}/credential-secret/redeem"),
	// 正文读取按 management.read 直执，专项审计在返回内容前完成。
	route("content.read", auth.CapabilityManagementRead, routeDirect, http.MethodPost,
		"/admin/v1/instances/{serverId}/logs", "/admin/v1/instances/{serverId}/browse",
		"/admin/v1/instances/{serverId}/logs/grants/{grantId}/consume", "/admin/v1/instances/{serverId}/browse/grants/{grantId}/consume",
		"/admin/v2/messages/{messageId}/payload", "/admin/v2/messages/{messageId}/payload/approval-requests", "/admin/v2/sensitive-access-grants/{grantId}/consume",
		"/admin/v2/assets/preview", "/admin/v2/assets/preview/approval-requests", "/admin/v2/assets/preview/grants/{grantId}/consume",
		"/admin/v2/assets/pair-read/approval-requests", "/admin/v2/assets/pair-read/grants/{grantId}/consume", "/admin/v2/assets/diff"),
	// 可恢复的文件同步任务启停保持直执，由 FileSyncService 事务内写专项审计。
	route("file-sync.task.start", auth.CapabilityManagementDirect, routeDirect, http.MethodPost, "/admin/v1/file-sync/tasks/{id}/start"),
	route("file-sync.task.resume", auth.CapabilityManagementDirect, routeDirect, http.MethodPost, "/admin/v1/file-sync/tasks/{id}/resume"),
	route("approval.request", auth.CapabilityApprovalRequest, routeApprovalRequired, http.MethodPost,
		"/admin/v2/approval-requests",
		"/admin/v1/api-keys", "/admin/v1/api-keys/{id}/reset", "/admin/v1/system/update", "/admin/v1/system/rollback", "/admin/v1/configs/batch", "/admin/v1/configs/{id}/rollback", "/admin/v1/configs/{id}/gray", "/admin/v1/configs/{id}/gray/promote", "/admin/v1/configs/{id}/plaintext/approval-requests", "/admin/v1/configs/{id}/plaintext/grants/{grantId}/consume", "/admin/v1/files/import", "/admin/v1/files/batch", "/admin/v1/files/{id}/rollback", "/admin/v1/override-sets/{id}/rollback", "/admin/v1/instances/{serverId}/resync", "/admin/v1/instances/{serverId}/reverse-fetch", "/admin/v1/reverse-fetch/tasks/{id}/submit", "/admin/v1/reverse-fetch/tasks/{id}/conflicts/grants/{grantId}/consume", "/admin/v1/reverse-fetch/tasks/{id}/resolve", "/admin/v1/instances/{serverId}/imprint", "/admin/v1/imprints/{commandId}/confirm",
		"/admin/v1/imprints/{commandId}/diff/grants/{grantId}/consume", "/admin/v2/namespaces/{id}/bc-directory-resyncs", "/admin/v2/namespaces/{id}/token/rotate", "/admin/v2/namespace-trusts", "/admin/v2/mcp-clients", "/admin/v2/mcp-clients/{clientId}/rotate", "/admin/v2/mcp-clients/{clientId}/enable", "/admin/v2/agent-identities/{identityId}/approve", "/admin/v2/agent-identities/{identityId}/allow-reapply", "/admin/v2/agent-identities/{identityId}/enable", "/admin/v2/agent-identities/{identityId}/unbind", "/admin/v2/agent-identities/{identityId}/resolve-conflict", "/admin/v2/servers/{id}/bc-directory-resyncs", "/admin/v2/server-placement-transfers", "/admin/v2/server-assignments", "/admin/v2/server-rezones", "/admin/v2/archive/jobs/{id}/retry", "/admin/v2/config-files/{id}/restore", "/admin/v2/config-files/{id}/purge", "/admin/v2/config-versions/{versionId}/rollback", "/admin/v2/assets/rescan", "/admin/v2/change-orders/{id}/submit", "/admin/v2/change-orders/{id}/approve", "/admin/v2/change-orders/{id}/reject", "/admin/v2/change-orders/{id}/resume", "/admin/v2/change-orders/{id}/batches/{batchNo}/confirm", "/admin/v2/change-orders/{id}/rollback", "/admin/v2/change-orders/{id}/rollback/finish", "/admin/v2/change-orders/{id}/rollback/targets"),
	route("approval.request", auth.CapabilityApprovalRequest, routeApprovalRequired, http.MethodPut, "/admin/v1/configs/{id}", "/admin/v1/files/{id}", "/admin/v1/override-sets/{id}", "/admin/v1/settings/{key}", "/admin/v2/servers/{serverRef}/default-entry", "/admin/v2/servers/{serverRef}/draining"),
	route("approval.request", auth.CapabilityApprovalRequest, routeApprovalRequired, http.MethodDelete, "/admin/v1/namespaces/{code}", "/admin/v1/configs/{id}", "/admin/v1/files/{id}", "/admin/v1/override-sets/{id}", "/admin/v1/api-keys/{id}", "/admin/v1/scheduling/drains", "/admin/v2/namespaces/{id}", "/admin/v2/bc-clusters/{id}", "/admin/v2/regions/{id}", "/admin/v2/zones/{id}", "/admin/v2/config-files/{id}", "/admin/v2/change-orders/{id}"),
	// V1 指派写端点已迁移（410 Gone，见 handler.ZoneHandler.Assign / Unassign 与 apperr.ErrZoneAssignmentMigrated）：
	// 两者写的是已退役的 zone_assignment 表，写成功也无人消费，故不再受理审批申请、从 approval.request 分类移出，
	// 单独登记为直执的迁移终态（无任何副作用，错误信息引导改用 v2 分配 / 换区流程）。
	// 保留路由本身（而非删路由）是为了让旧客户端拿到明确的 410 迁移指引，而不是含糊的 404。
	// 只读端点（GET /admin/v1/zones/assignments、/zones、/zones/default-entry）不受影响，仍按 management.read 登记。
	route("zone.assignment.migrated", auth.CapabilityManagementRead, routeDirect, http.MethodPut, "/admin/v1/zones/assignments"),
	route("zone.assignment.migrated", auth.CapabilityManagementRead, routeDirect, http.MethodDelete, "/admin/v1/zones/assignments"),
)
