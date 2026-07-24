//go:build integration

package server_test

import (
	"net/http"
	"strings"
	"testing"
)

// FR-178 env 展示维度 REST 集成：env 增删改 + 整体替换 env→namespace 映射 + 冲突 409 指明冲突方 + 删 env 级联映射。
// 需真实 MySQL（BEACON_TEST_DSN）；未设则由 testsupport.OpenTestDB 跳过。

// TestEnvRestLifecycle 走完整 HTTP 生命周期：建 / 列 / 改 / 设映射 / 冲突 / 删级联。
func TestEnvRestLifecycle(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 两个 namespace 作映射目标
	ns1, _ := createNamespaceV2(t, ts.URL, "prod")
	ns2, _ := createNamespaceV2(t, ts.URL, "test")

	// 建 env
	code, body := doJSON(t, http.MethodPost, ts.URL+"/admin/v2/envs", map[string]any{"name": "生产", "description": "生产展示维度"})
	if code != http.StatusCreated {
		t.Fatalf("建 env 应 201，实际 %d：%v", code, body)
	}
	envID := uint(body["id"].(float64))
	if body["namespaceCount"].(float64) != 0 {
		t.Fatalf("新建 env 映射数应 0，实际 %v", body["namespaceCount"])
	}

	// 列 env
	code, list := doJSON(t, http.MethodGet, ts.URL+"/admin/v2/envs", nil)
	if code != http.StatusOK || len(asSlice(list["items"])) != 1 {
		t.Fatalf("列 env 应 200 且含 1 项，实际 %d：%v", code, list)
	}

	// PATCH 改描述
	code, patched := doJSON(t, http.MethodPatch, ts.URL+"/admin/v2/envs/"+itoa(int(envID)), map[string]any{"description": "改后描述"})
	if code != http.StatusOK || patched["description"] != "改后描述" {
		t.Fatalf("PATCH env 应 200 且描述更新，实际 %d：%v", code, patched)
	}

	// 整体替换映射 {ns1, ns2}
	code, mapped := doJSON(t, http.MethodPut, ts.URL+"/admin/v2/envs/"+itoa(int(envID))+"/namespaces", map[string]any{"namespaceIds": []uint{ns1, ns2}})
	if code != http.StatusOK || mapped["namespaceCount"].(float64) != 2 {
		t.Fatalf("设置映射应 200 且映射 2 个，实际 %d：%v", code, mapped)
	}

	// 第二个 env 抢占 ns1 → 409 且指明冲突方
	code, env2 := doJSON(t, http.MethodPost, ts.URL+"/admin/v2/envs", map[string]any{"name": "测试"})
	if code != http.StatusCreated {
		t.Fatalf("建 env2 应 201，实际 %d：%v", code, env2)
	}
	env2ID := uint(env2["id"].(float64))
	code, conflict := doJSON(t, http.MethodPut, ts.URL+"/admin/v2/envs/"+itoa(int(env2ID))+"/namespaces", map[string]any{"namespaceIds": []uint{ns1}})
	if code != http.StatusConflict {
		t.Fatalf("抢占已占用 namespace 应 409，实际 %d：%v", code, conflict)
	}
	msg, _ := conflict["message"].(string)
	if !strings.Contains(msg, "prod") || !strings.Contains(msg, "生产") {
		t.Fatalf("409 message 应指明冲突方（prod / env 生产），实际 %q", msg)
	}

	// 删 env（级联删除映射）
	if code, delBody := doJSON(t, http.MethodDelete, ts.URL+"/admin/v2/envs/"+itoa(int(envID)), nil); code != http.StatusNoContent {
		t.Fatalf("删 env 应 204，实际 %d：%v", code, delBody)
	}

	// 释放后 ns1 可归 env2
	code, remapped := doJSON(t, http.MethodPut, ts.URL+"/admin/v2/envs/"+itoa(int(env2ID))+"/namespaces", map[string]any{"namespaceIds": []uint{ns1}})
	if code != http.StatusOK || remapped["namespaceCount"].(float64) != 1 {
		t.Fatalf("释放后 ns1 应可归 env2，实际 %d：%v", code, remapped)
	}
}
