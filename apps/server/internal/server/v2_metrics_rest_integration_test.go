//go:build integration

package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
)

// doAgentJSON 发起一次带 agent 头（X-Beacon-*）的 JSON 请求，不携带 admin 令牌。
func doAgentJSON(t *testing.T, method, url string, headers map[string]string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, url, reader)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("agent 请求 %s %s 失败: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	if len(data) > 0 {
		_ = json.Unmarshal(data, &parsed)
	}
	return resp.StatusCode, parsed
}

// createV2NamespaceToken 经 admin 端建 v2 namespace 并取一次性明文 accessToken。
func createV2NamespaceToken(t *testing.T, baseURL, name string) string {
	t.Helper()
	code, body := doJSON(t, http.MethodPost, baseURL+"/admin/v2/namespaces", map[string]any{"name": name})
	if code != http.StatusCreated {
		t.Fatalf("建 namespace 应 201，实际 %d：%v", code, body)
	}
	token, _ := body["accessToken"].(string)
	if token == "" {
		t.Fatalf("建 namespace 响应缺 accessToken：%v", body)
	}
	return token
}

// metricSamplesBody 构造一次上报请求体。
func metricReportBody(serverID, kind string, buckets []int64) map[string]any {
	samples := make([]map[string]any, 0, len(buckets))
	for _, b := range buckets {
		samples = append(samples, map[string]any{
			"bucketStartMs": b, "sampleCount": 5, "cpuPctAvg": 30.0, "cpuPctMax": 40.0,
			"memUsedMbAvg": 512.0, "memMaxMb": 2048, "tpsAvg": 19.8, "tpsMin": 18.0,
			"onlineAvg": 40, "onlineMax": 42, "maxOnline": 100, "reportRttMs": 8,
		})
	}
	return map[string]any{
		"namespace": "", "serverId": serverID, "kind": kind,
		"agentTimeMs": time.Now().UTC().UnixMilli(), "droppedSinceLast": 0, "samples": samples,
	}
}

// TestV2MetricsReportAuthAndDedup 端到端：未确认 403、错 token 401、确认后 202 接收、重放去重。
func TestV2MetricsReportAuthAndDedup(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	reportURL := ts.URL + "/beacon/v2/agent/metrics/report"

	token := createV2NamespaceToken(t, ts.URL, "p4ns")
	identityID := "11111111-1111-4111-8111-111111111111"
	serverID := "int-lobby-1"

	// 注册（pending）。
	code, _ := doAgentJSON(t, http.MethodPost, ts.URL+"/beacon/v2/agent/register",
		map[string]string{"X-Beacon-Token": token}, map[string]any{
			"identityId": identityID, "serverId": serverID, "kind": "backend", "bootId": "boot-1",
		})
	if code != http.StatusAccepted {
		t.Fatalf("注册应 202 pending，实际 %d", code)
	}

	bucket := (time.Now().UTC().UnixMilli() / 5000) * 5000

	// 未确认上报 → 403 agent_not_confirmed。
	code, body := doAgentJSON(t, http.MethodPost, reportURL,
		map[string]string{"X-Beacon-Token": token, "X-Beacon-Identity": identityID},
		metricReportBody(serverID, "backend", []int64{bucket}))
	if code != http.StatusForbidden {
		t.Fatalf("未确认上报应 403，实际 %d：%v", code, body)
	}
	if body["code"] != "agent_not_confirmed" {
		t.Fatalf("403 code 应为 agent_not_confirmed，实际 %v", body["code"])
	}

	// 错 token → 401。
	code, _ = doAgentJSON(t, http.MethodPost, reportURL,
		map[string]string{"X-Beacon-Token": "bn_wrong", "X-Beacon-Identity": identityID},
		metricReportBody(serverID, "backend", []int64{bucket}))
	if code != http.StatusUnauthorized {
		t.Fatalf("错 token 上报应 401，实际 %d", code)
	}

	// 人工确认（首次确认，无 target）。
	code, _ = doJSON(t, http.MethodPost, ts.URL+"/admin/v2/agent-identities/"+identityID+"/approve", map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("确认身份应 200，实际 %d", code)
	}

	// 确认后上报 → 202 accepted=1。
	code, body = doAgentJSON(t, http.MethodPost, reportURL,
		map[string]string{"X-Beacon-Token": token, "X-Beacon-Identity": identityID},
		metricReportBody(serverID, "backend", []int64{bucket, bucket + 5000}))
	if code != http.StatusAccepted {
		t.Fatalf("确认后上报应 202，实际 %d：%v", code, body)
	}
	if acc, _ := body["accepted"].(float64); acc != 2 {
		t.Fatalf("应接收 2 个批，实际 %v", body["accepted"])
	}
	// 健康计算尚未产出该实例视图 → self 为 null（FR-147，§5.1）。
	if v, ok := body["self"]; !ok || v != nil {
		t.Fatalf("尚无健康视图时 self 应为 null，实际 %v", body["self"])
	}

	// 预置健康视图后重放同批 → 窗口去重 accepted=0 / deduplicated=2，且 self 回填自身健康。
	testHealthViews.ReplaceAll([]healthview.View{{
		NamespaceID: nsIDBySelfReport(t, ts.URL, "p4ns"), Namespace: "p4ns", ServerID: serverID,
		Kind: "backend", Score: 87, Level: "healthy", Schedulable: true, Reasons: []string{},
	}})
	code, body = doAgentJSON(t, http.MethodPost, reportURL,
		map[string]string{"X-Beacon-Token": token, "X-Beacon-Identity": identityID},
		metricReportBody(serverID, "backend", []int64{bucket, bucket + 5000}))
	if code != http.StatusAccepted {
		t.Fatalf("重放上报应 202，实际 %d", code)
	}
	if acc, _ := body["accepted"].(float64); acc != 0 {
		t.Fatalf("重放 accepted 应 0，实际 %v", body["accepted"])
	}
	if dup, _ := body["deduplicated"].(float64); dup != 2 {
		t.Fatalf("重放 deduplicated 应 2，实际 %v", body["deduplicated"])
	}
	self, ok := body["self"].(map[string]any)
	if !ok {
		t.Fatalf("有健康视图时 self 应回填对象，实际 %v", body["self"])
	}
	if score, _ := self["score"].(float64); score != 87 || self["level"] != "healthy" || self["schedulable"] != true {
		t.Fatalf("self 内容不符，实际 %v", self)
	}
	if reasons, ok := self["reasons"].([]any); !ok || len(reasons) != 0 {
		t.Fatalf("self.reasons 应为空数组，实际 %v", self["reasons"])
	}
}

// nsIDBySelfReport 查建出的 namespace 行数字 id（self 回填按 (namespaceId, serverId) 定位视图）。
func nsIDBySelfReport(t *testing.T, baseURL, name string) uint {
	t.Helper()
	code, body := doJSON(t, http.MethodGet, baseURL+"/admin/v2/namespaces", nil)
	if code != http.StatusOK {
		t.Fatalf("查 namespaces 应 200，实际 %d", code)
	}
	items, _ := body["items"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item["name"] == name {
			id, _ := item["id"].(float64)
			return uint(id)
		}
	}
	t.Fatalf("未找到 namespace %s", name)
	return 0
}

// TestV2MetricsClockSkewRejected 端到端：时钟偏移超阈值 → 400 clock_skew_too_large。
func TestV2MetricsClockSkewRejected(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	token := createV2NamespaceToken(t, ts.URL, "p4skew")
	identityID := "22222222-2222-4222-8222-222222222222"
	serverID := "int-lobby-2"
	doAgentJSON(t, http.MethodPost, ts.URL+"/beacon/v2/agent/register",
		map[string]string{"X-Beacon-Token": token}, map[string]any{
			"identityId": identityID, "serverId": serverID, "kind": "backend", "bootId": "boot-1",
		})
	doJSON(t, http.MethodPost, ts.URL+"/admin/v2/agent-identities/"+identityID+"/approve", map[string]any{})

	bucket := (time.Now().UTC().UnixMilli() / 5000) * 5000
	body := metricReportBody(serverID, "backend", []int64{bucket})
	body["agentTimeMs"] = time.Now().UTC().Add(-10 * time.Minute).UnixMilli() // 超 5min 偏移
	code, resp := doAgentJSON(t, http.MethodPost, ts.URL+"/beacon/v2/agent/metrics/report",
		map[string]string{"X-Beacon-Token": token, "X-Beacon-Identity": identityID}, body)
	if code != http.StatusBadRequest {
		t.Fatalf("时钟偏移超阈值应 400，实际 %d", code)
	}
	if resp["code"] != "clock_skew_too_large" {
		t.Fatalf("code 应为 clock_skew_too_large，实际 %v", resp["code"])
	}
}
