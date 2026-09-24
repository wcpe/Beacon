//go:build integration

package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// FR-233（见 ADR-0084）：v1 数据面注册端点更名的兼容别名契约。
//
// 契约（不可改动）：
//  1. 规范路径 POST /beacon/v1/agent/data-plane/attach 与旧路径 POST /beacon/v1/agent/register **同 handler**
//     （h.Agent.Register），运行时语义逐字一致（鉴权、机器注册直落、审计、registry 写入、状态码全不变）。
//  2. 新路径在 /beacon/v1/agent 组内（挂 agentTokenMiddleware，FR-222 受信调用方判定依赖它）。
//  3. 仅旧路径回带 Deprecation: true 与 Link: <规范路径>; rel="successor-version"，新路径**不得**带。
//
// 本文件的断言均为「改错必失败」的负向保护：把废弃头写进 handler（新路径也会带）、去掉包装（旧路径不带）、
// 或把新路径指向别的实现（副作用缺失）都会让对应用例红。

const (
	// fr233CanonicalPath 是 FR-233 更名后的规范路径。
	fr233CanonicalPath = "/beacon/v1/agent/data-plane/attach"
	// fr233LegacyPath 是保留一个版本周期的兼容别名路径。
	fr233LegacyPath = "/beacon/v1/agent/register"
	// fr233SuccessorLink 是旧路径回带的后继版本链接（全值精确比对，拼错即失败）。
	fr233SuccessorLink = "</beacon/v1/agent/data-plane/attach>; rel=\"successor-version\""
)

// doJSONWithHeader 与 doJSON 相同，但额外返回响应头（FR-233 需要读废弃声明头）。
// agent 端（/beacon/v1/）自动携带共享 token，与管理台令牌互不干扰。
func doJSONWithHeader(t *testing.T, method, url string, body any) (int, http.Header, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, url, reader)
	req.Header.Set("Content-Type", "application/json")
	if adminToken != "" {
		req.Header.Set("Authorization", "Bearer "+adminToken)
	}
	if strings.Contains(url, "/beacon/v1/") {
		req.Header.Set("X-Beacon-Token", testAgentToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求 %s %s 失败: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	if len(data) > 0 {
		_ = json.Unmarshal(data, &parsed)
	}
	return resp.StatusCode, resp.Header.Clone(), parsed
}

// fr233EnsureNamespace 建一个运行环境（发现仅投影 lifecycle=active 的环境，否则查不到注册结果）。
func fr233EnsureNamespace(t *testing.T, ts *integrationTestServer, code string) {
	t.Helper()
	if code2, body := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/namespaces", map[string]any{"code": code, "name": code}); code2 != http.StatusCreated {
		t.Fatalf("创建运行环境 %s 应 201，实际 %d：%v", code, code2, body)
	}
}

// fr233DiscoveryHas 断言 discovery 里能查到该实例（证明注册确实写了内存 registry，即走了真实 handler）。
func fr233DiscoveryHas(t *testing.T, ts *integrationTestServer, namespace, serverID string) {
	t.Helper()
	code, disc := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/discovery?namespace="+namespace, nil)
	if code != http.StatusOK {
		t.Fatalf("发现应 200，实际 %d", code)
	}
	insts, _ := disc["instances"].([]any)
	for _, raw := range insts {
		inst, _ := raw.(map[string]any)
		if inst["serverId"] == serverID {
			return
		}
	}
	t.Fatalf("发现应含 %s（注册未写 registry，说明未走真实注册 handler），实际 %v", serverID, disc["instances"])
}

// fr233SharedTokenAuth 断言该路径挂在 agentTokenMiddleware 之下：缺 token → 401。
// 这同时验证「新路径必须留在 /beacon/v1/agent 组内」——移出该组即失守 401。
func fr233SharedTokenAuth(t *testing.T, ts *integrationTestServer, path, serverID string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"namespace": "prod", "serverId": serverID, "role": "bukkit", "address": "10.0.0.9:25565",
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("无 token 请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("缺 token 时 %s 应 401（共享 token 中间件未生效），实际 %d", path, resp.StatusCode)
	}
}

// TestFR233CanonicalAttachPathRegistersWithoutDeprecation 契约 1/3：新路径能成功挂载数据面（200），
// 且响应**不含** Deprecation / Link 头——废弃声明只能在旧路径的包装层加，写进 handler 本用例即红。
func TestFR233CanonicalAttachPathRegistersWithoutDeprecation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	fr233EnsureNamespace(t, ts, "prod")

	code, header, res := doJSONWithHeader(t, http.MethodPost, ts.URL+fr233CanonicalPath, map[string]any{
		"namespace": "prod", "serverId": "fr233-canonical", "role": "bukkit",
		"address": "10.0.0.11:25565", "capacity": 200, "weight": 100,
	})
	if code != http.StatusOK {
		t.Fatalf("新路径挂载数据面应 200，实际 %d：%v", code, res)
	}
	if got := header.Get("Deprecation"); got != "" {
		t.Fatalf("规范路径不得回带 Deprecation 头（头必须只加在旧路径包装层），实际 %q", got)
	}
	if got := header.Get("Link"); got != "" {
		t.Fatalf("规范路径不得回带 Link 后继版本头，实际 %q", got)
	}
	// 语义等价于注册：响应结构与旧路径一致，且副作用真实发生（registry + instance.register 审计）。
	if _, ok := res["instanceKey"].(string); !ok {
		t.Fatalf("新路径响应应含 instanceKey，实际 %v", res)
	}
	if res["assigned"] != false {
		t.Fatalf("全新实例应 assigned=false，实际 %v", res)
	}
	fr233DiscoveryHas(t, ts, "prod", "fr233-canonical")
	code, audits := doJSON(t, http.MethodGet,
		ts.URL+"/admin/v1/audits?action=instance.register&namespace=prod&keyword=fr233-canonical", nil)
	if code != http.StatusOK {
		t.Fatalf("查 instance.register 审计应 200，实际 %d", code)
	}
	if items, _ := audits["items"].([]any); len(items) == 0 {
		t.Fatalf("新路径应写 instance.register 审计（同 handler 证据），实际无")
	}
	// 共享 token 中间件覆盖：缺 token → 401（证明新端点留在 /beacon/v1/agent 组内）。
	fr233SharedTokenAuth(t, ts, fr233CanonicalPath, "fr233-canonical-unt")
}

// TestFR233LegacyRegisterAliasCarriesDeprecation 契约 2/3：旧路径行为完全不变（200 + 同结构 + 真实副作用），
// 且**必须**回带 Deprecation: true 与指向规范路径的 Link 头。去掉包装（或改错 Link）本用例即红。
func TestFR233LegacyRegisterAliasCarriesDeprecation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	fr233EnsureNamespace(t, ts, "prod")

	code, header, res := doJSONWithHeader(t, http.MethodPost, ts.URL+fr233LegacyPath, map[string]any{
		"namespace": "prod", "serverId": "fr233-legacy", "role": "bukkit",
		"address": "10.0.0.12:25565", "capacity": 200, "weight": 100,
	})
	if code != http.StatusOK {
		t.Fatalf("旧路径（兼容别名）注册应 200，实际 %d：%v", code, res)
	}
	// 兼容别名是原地包装、不是重定向：状态码与响应体形状须与既有语义逐字一致。
	if got := header.Get("Deprecation"); got != "true" {
		t.Fatalf("旧路径应回带 Deprecation: true，实际 %q", got)
	}
	if got := header.Get("Link"); got != fr233SuccessorLink {
		t.Fatalf("旧路径 Link 应为 %q，实际 %q", fr233SuccessorLink, got)
	}
	if _, ok := res["instanceKey"].(string); !ok {
		t.Fatalf("旧路径响应应含 instanceKey（行为不变），实际 %v", res)
	}
	fr233DiscoveryHas(t, ts, "prod", "fr233-legacy")
}

// TestFR233BothPathsShareSameSemantics 契约 3/3：两条路径注册同一实例的语义一致——
// ① 响应键集合完全相同；② 两条路径都真实写 registry（同一 handler 的两处挂载）；
// ③ 同一 serverId 在同址重连时两路径互为幂等（跨路径 200，而非冲突 409）。
func TestFR233BothPathsShareSameSemantics(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	fr233EnsureNamespace(t, ts, "prod")

	body := map[string]any{
		"namespace": "prod", "serverId": "fr233-both", "role": "bukkit",
		"address": "10.0.0.13:25565", "capacity": 100, "weight": 100,
	}
	codeCanonical, _, resCanonical := doJSONWithHeader(t, http.MethodPost, ts.URL+fr233CanonicalPath, body)
	if codeCanonical != http.StatusOK {
		t.Fatalf("新路径注册应 200，实际 %d：%v", codeCanonical, resCanonical)
	}
	// 同一 serverId + 同一地址经旧路径重连：共享同一 handler，故应幂等 200（若两路径实现分叉则易出 409）。
	codeLegacy, _, resLegacy := doJSONWithHeader(t, http.MethodPost, ts.URL+fr233LegacyPath, body)
	if codeLegacy != http.StatusOK {
		t.Fatalf("旧路径同址重连应 200（与共享 handler 的幂等语义一致），实际 %d：%v", codeLegacy, resLegacy)
	}
	// 两路径响应键集合完全相同（结构一致，唯一差异只在旧路径的响应头）。
	if canonKeys, legacyKeys := fr233SortedKeys(resCanonical), fr233SortedKeys(resLegacy); strings.Join(canonKeys, ",") != strings.Join(legacyKeys, ",") {
		t.Fatalf("两路径响应结构应一致，新=%v 旧=%v", canonKeys, legacyKeys)
	}
	// 旧路径的重复注册不产生第二条实例：发现里 fr233-both 恰好一条（同 handler 同一 registry 真源）。
	code, disc := doJSON(t, http.MethodGet, ts.URL+"/beacon/v1/agent/discovery?namespace=prod", nil)
	if code != http.StatusOK {
		t.Fatalf("发现应 200，实际 %d", code)
	}
	insts, _ := disc["instances"].([]any)
	count := 0
	for _, raw := range insts {
		inst, _ := raw.(map[string]any)
		if inst["serverId"] == "fr233-both" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("同一 serverId 跨两路径注册应只占一条注册表条目，实际 %d 条", count)
	}
}

// fr233SortedKeys 返回响应体的键集合（字典序），供两条路径的结构一致性比对。
func fr233SortedKeys(body map[string]any) []string {
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
