package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"beacon/internal/handler"
	"beacon/internal/repository"
	"beacon/internal/runtime"
	"beacon/internal/server"
	"beacon/internal/service"
	"beacon/internal/testsupport"
)

// newTestServer 装配真实路由与 DB-backed 服务（不启用 agent token）；未设 BEACON_TEST_DSN 则跳过。
func newTestServer(t *testing.T) *httptest.Server {
	return newTestServerWithToken(t, "")
}

// newTestServerWithToken 同上，但启用指定的 agent token。
func newTestServerWithToken(t *testing.T, agentToken string) *httptest.Server {
	t.Helper()
	db := testsupport.OpenTestDB(t, "server")
	auditRepo := repository.NewAuditLogRepository(db)
	assignRepo := repository.NewZoneAssignmentRepository(db)
	registry := runtime.NewRegistry()
	nsHandler := handler.NewNamespaceHandler(service.NewNamespaceService(repository.NewNamespaceRepository(db)))
	cfgSvc := service.NewConfigService(db,
		repository.NewConfigItemRepository(db),
		repository.NewConfigRevisionRepository(db), auditRepo)
	instSvc := service.NewInstanceService(registry, assignRepo, auditRepo, 10*time.Second, 30*time.Second)
	zoneSvc := service.NewZoneService(db, assignRepo, auditRepo, registry)
	router := server.NewRouter(server.Handlers{
		Namespace: nsHandler,
		Config:    handler.NewConfigHandler(cfgSvc),
		Agent:     handler.NewAgentHandler(instSvc),
		Instance:  handler.NewInstanceHandler(instSvc),
		Zone:      handler.NewZoneHandler(zoneSvc),
		Web:       http.HandlerFunc(http.NotFound),
	}, agentToken)
	return httptest.NewServer(router)
}

// doJSON 发起一次 JSON 请求并返回状态码与解析后的响应体。
func doJSON(t *testing.T, method, url string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, url, reader)
	req.Header.Set("Content-Type", "application/json")
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
	return resp.StatusCode, parsed
}

// TestConfigRESTFlow REST 集成：建→发布→历史→回滚→diff 全流程经 HTTP。
func TestConfigRESTFlow(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	base := ts.URL + "/admin/v1/configs"

	// 建
	code, created := doJSON(t, http.MethodPost, base, map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "dataId": "app.yml",
		"scopeLevel": "global", "format": "yaml", "content": "k: 1\n", "operator": "alice",
	})
	if code != http.StatusCreated {
		t.Fatalf("建配置应 201，实际 %d：%v", code, created)
	}
	idF, ok := created["id"].(float64)
	if !ok {
		t.Fatalf("建配置响应缺 id：%v", created)
	}
	id := int(idF)
	itemURL := ts.URL + "/admin/v1/configs/" + itoa(id)

	// 发布
	code, pub := doJSON(t, http.MethodPut, itemURL, map[string]any{"content": "k: 2\n", "operator": "bob"})
	if code != http.StatusOK || pub["version"].(float64) != 2 {
		t.Fatalf("发布应 200 且 version=2，实际 %d：%v", code, pub)
	}

	// 历史
	code, revs := doJSON(t, http.MethodGet, itemURL+"/revisions", nil)
	if code != http.StatusOK {
		t.Fatalf("历史应 200，实际 %d", code)
	}
	if items, _ := revs["items"].([]any); len(items) != 2 {
		t.Fatalf("历史应有 2 版，实际 %v", revs["items"])
	}

	// 回滚到 v1
	code, rb := doJSON(t, http.MethodPost, itemURL+"/rollback", map[string]any{"toVersion": 1, "operator": "carol"})
	if code != http.StatusOK || rb["version"].(float64) != 3 {
		t.Fatalf("回滚应 200 且 version=3，实际 %d：%v", code, rb)
	}

	// diff v1 vs v2
	code, diff := doJSON(t, http.MethodGet, itemURL+"/diff?from=1&to=2", nil)
	if code != http.StatusOK || diff["fromContent"] != "k: 1\n" || diff["toContent"] != "k: 2\n" {
		t.Fatalf("diff 错误：%d %v", code, diff)
	}

	// 不存在的配置 → 404 CONFIG_NOT_FOUND
	code, nf := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/configs/999999", nil)
	if code != http.StatusNotFound || nf["code"] != "CONFIG_NOT_FOUND" {
		t.Fatalf("取不存在配置应 404 CONFIG_NOT_FOUND，实际 %d：%v", code, nf)
	}
}

// itoa 是不引入额外依赖的小工具。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
