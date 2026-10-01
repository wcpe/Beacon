//go:build integration

package server_test

import (
	"net/http"
	"testing"
)

// TestZoneAssignmentWriteMigratedReadRemains 锁定 V1 指派写入口已迁移（410 Gone）：
// PUT / DELETE 都回 410 zone_assignment_migrated 并引导改用 v2 分配 / 换区流程，且不落库、不产生审计；
// 只读端点（GET /admin/v1/zones/assignments 列表、GET /admin/v1/zones 汇总）保留可用——
// 它们读的是已退役的 zone_assignment 表（生产恒空，仅历史回显），故列表恒空。
func TestZoneAssignmentWriteMigratedReadRemains(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	assignURL := ts.URL + "/admin/v1/zones/assignments"

	// 写入口①：PUT 指派 → 410 + 迁移指引
	code, body := doJSONWithHeaders(t, http.MethodPut, assignURL, map[string]any{
		"namespace": "prod", "serverId": "z-s1", "group": "area1", "zone": "zoneA", "note": "迁移后不再受理",
	}, map[string]string{"Idempotency-Key": t.Name() + "-assign"})
	if code != http.StatusGone || body["code"] != "zone_assignment_migrated" {
		t.Fatalf("V1 指派写端点应 410 zone_assignment_migrated，实际 %d：%v", code, body)
	}

	// 写入口②：DELETE 取消指派 → 410 + 迁移指引
	code, body = doJSONWithHeaders(t, http.MethodDelete, assignURL+"?namespace=prod&serverId=z-s1&reason=迁移后不再受理", nil,
		map[string]string{"Idempotency-Key": t.Name() + "-unassign"})
	if code != http.StatusGone || body["code"] != "zone_assignment_migrated" {
		t.Fatalf("V1 取消指派写端点应 410 zone_assignment_migrated，实际 %d：%v", code, body)
	}

	// 只读端点保留：列表 200 且因退役表恒空而为空（写入口不再落任何一行）
	code, list := doJSON(t, http.MethodGet, assignURL+"?namespace=prod", nil)
	if code != http.StatusOK {
		t.Fatalf("指派列表应 200（只读端点保留），实际 %d", code)
	}
	if items, _ := list["items"].([]any); len(items) != 0 {
		t.Fatalf("迁移后的写入口不得落库，列表应空，实际 %v", list["items"])
	}
	// 只读端点保留：汇总 200
	if code, _ := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/zones?namespace=prod", nil); code != http.StatusOK {
		t.Fatalf("zone 汇总应 200（只读端点保留），实际 %d", code)
	}

	// 迁移后的写入口不产生归属审计
	for _, action := range []string{"zone.assign", "zone.unassign"} {
		code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?action="+action, nil)
		if code != http.StatusOK {
			t.Fatalf("查 %s 审计应 200，实际 %d", action, code)
		}
		if total, _ := audits["total"].(float64); total != 0 {
			t.Fatalf("迁移后的写入口不应产生 %s 审计，实际 %v", action, audits["items"])
		}
	}
}

// asSlice 把 any 安全转为 []any（nil 返回空）。
func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
