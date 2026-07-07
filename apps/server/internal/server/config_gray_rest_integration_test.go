//go:build integration

package server_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestConfigGrayRESTFlow 配置灰度 REST 集成（FR-9）：
// 发布灰度→cohort 成员见灰度/非成员见稳定→晋升为稳定→列表空→二次发布并中止回稳定；含空 cohort、404 负路径。
func TestConfigGrayRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	cfgBase := ts.URL + "/admin/v1/configs"

	// 建稳定配置 v1（global，内容 alpha）
	code, created := doJSON(t, http.MethodPost, cfgBase, map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "dataId": "gray.yml",
		"scopeLevel": "global", "format": "yaml", "content": "v: alpha\n",
	})
	if code != http.StatusCreated {
		t.Fatalf("建配置应 201，实际 %d：%v", code, created)
	}
	id := int(created["id"].(float64))
	itemURL := cfgBase + "/" + itoa(id)

	// effContent 取某 serverId 的有效配置内容（admin 预览，含灰度按 serverId 叠加，FR-22）
	effContent := func(serverID string) string {
		code, eff := doJSON(t, http.MethodGet, cfgBase+"/effective?namespace=prod&serverId="+serverID, nil)
		if code != http.StatusOK {
			t.Fatalf("取有效配置应 200，实际 %d：%v", code, eff)
		}
		items := asSlice(eff["items"])
		if len(items) == 0 {
			return ""
		}
		m, _ := items[0].(map[string]any)
		s, _ := m["content"].(string)
		return s
	}

	// 发布灰度 bravo 给 cohort=[gray-s1]
	code, g := doJSON(t, http.MethodPost, itemURL+"/gray", map[string]any{
		"content": "v: bravo\n", "cohort": []string{"gray-s1"}, "comment": "beta",
	})
	if code != http.StatusCreated {
		t.Fatalf("发布灰度应 201，实际 %d：%v", code, g)
	}
	if cohort := asSlice(g["cohort"]); len(cohort) != 1 || cohort[0] != "gray-s1" {
		t.Fatalf("灰度 cohort 应为 [gray-s1]，实际 %v", g["cohort"])
	}

	// 活跃灰度列表含 1 条
	code, gl := doJSON(t, http.MethodGet, cfgBase+"/gray?namespace=prod", nil)
	if code != http.StatusOK || len(asSlice(gl["items"])) != 1 {
		t.Fatalf("活跃灰度应 1 条，实际 %d %v", code, gl["items"])
	}

	// FR-9 核心：cohort 成员见灰度 bravo，非成员见稳定 alpha（逐字节正交叠加）
	if c := effContent("gray-s1"); !strings.Contains(c, "bravo") {
		t.Fatalf("cohort 成员应见灰度内容 bravo，实际 %q", c)
	}
	if c := effContent("other-s9"); !strings.Contains(c, "alpha") {
		t.Fatalf("非 cohort 成员应见稳定内容 alpha，实际 %q", c)
	}

	// 晋升：灰度 bravo 成为稳定 v2
	code, pr := doJSON(t, http.MethodPost, itemURL+"/gray/promote", map[string]any{"comment": "go"})
	if code != http.StatusOK || pr["version"].(float64) != 2 {
		t.Fatalf("晋升应 200 且 version=2，实际 %d：%v", code, pr)
	}
	// 晋升后无活跃灰度；非成员也见已晋升的稳定内容 bravo
	code, gl2 := doJSON(t, http.MethodGet, cfgBase+"/gray?namespace=prod", nil)
	if code != http.StatusOK || len(asSlice(gl2["items"])) != 0 {
		t.Fatalf("晋升后应无活跃灰度，实际 %v", gl2["items"])
	}
	if c := effContent("other-s9"); !strings.Contains(c, "bravo") {
		t.Fatalf("晋升后非成员应见已晋升稳定内容 bravo，实际 %q", c)
	}

	// 二次发布灰度 charlie 给 [gray-s1]，再中止 → 成员回稳定 bravo
	code, _ = doJSON(t, http.MethodPost, itemURL+"/gray", map[string]any{
		"content": "v: charlie\n", "cohort": []string{"gray-s1"},
	})
	if code != http.StatusCreated {
		t.Fatalf("二次发布灰度应 201，实际 %d", code)
	}
	if c := effContent("gray-s1"); !strings.Contains(c, "charlie") {
		t.Fatalf("二次灰度成员应见 charlie，实际 %q", c)
	}
	code, _ = doJSON(t, http.MethodDelete, itemURL+"/gray?comment=stop", nil)
	if code != http.StatusOK {
		t.Fatalf("中止灰度应 200，实际 %d", code)
	}
	if c := effContent("gray-s1"); strings.Contains(c, "charlie") || !strings.Contains(c, "bravo") {
		t.Fatalf("中止后成员应回稳定 bravo（无 charlie），实际 %q", c)
	}

	// 负路径：空 cohort → 拒绝（4xx）
	code, _ = doJSON(t, http.MethodPost, itemURL+"/gray", map[string]any{"content": "v: x\n", "cohort": []string{}})
	if code < 400 || code >= 500 {
		t.Fatalf("空 cohort 应 4xx 拒绝，实际 %d", code)
	}
	// 负路径：对不存在配置发灰度 → 404
	code, _ = doJSON(t, http.MethodPost, cfgBase+"/999999/gray", map[string]any{"content": "v: x\n", "cohort": []string{"s1"}})
	if code != http.StatusNotFound {
		t.Fatalf("对不存在配置发灰度应 404，实际 %d", code)
	}
	// 负路径：无活跃灰度时晋升 → 404
	code, _ = doJSON(t, http.MethodPost, itemURL+"/gray/promote", map[string]any{})
	if code != http.StatusNotFound {
		t.Fatalf("无活跃灰度晋升应 404，实际 %d", code)
	}
}
