package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

func newV2HandlerTestService(t *testing.T) (*gorm.DB, *service.V2ControlPlaneService, *V2ControlPlaneHandler) {
	t.Helper()
	dsn := "file:" + url.QueryEscape(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Namespace{},
		&model.NamespaceTrust{},
		&model.Env{},
		&model.EnvNamespace{},
		&model.BCCluster{},
		&model.Region{},
		&model.Zone{},
		&model.Server{},
		&model.AgentIdentity{},
		&model.AuditLog{},
	); err != nil {
		t.Fatalf("迁移 v2 表失败: %v", err)
	}
	svc := service.NewV2ControlPlaneService(db)
	return db, svc, NewV2ControlPlaneHandler(svc)
}

func TestV2AgentRegisterHTTPPendingThenActive(t *testing.T) {
	_, svc, h := newV2HandlerTestService(t)
	_, token, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}

	body := map[string]any{
		"identityId":   "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"serverId":     "lobby-1",
		"kind":         model.ServerKindBackend,
		"bootId":       "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		"agentVersion": "0.21.0",
	}
	code, parsed := invokeJSON(h.AgentRegister, http.MethodPost, "/beacon/v2/agent/register", token, body)
	if code != http.StatusAccepted || parsed["status"] != model.AgentIdentityStatusPending {
		t.Fatalf("首次注册应 202 pending，实际 %d：%v", code, parsed)
	}
	if parsed["namespace"] != "prod" || parsed["serverId"] != "lobby-1" {
		t.Fatalf("注册响应应带 token 归属 namespace 与 serverId，实际 %v", parsed)
	}

	approveCode, approveBody := invokeJSONWithParam(
		h.ApproveAgentIdentity,
		http.MethodPost,
		"/admin/v2/agent-identities/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/approve",
		"",
		map[string]any{"forceUnbindOccupier": false},
		"identityId",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	)
	if approveCode != http.StatusOK || approveBody["status"] != model.AgentIdentityStatusActive {
		t.Fatalf("确认身份应 200 active，实际 %d：%v", approveCode, approveBody)
	}

	code, parsed = invokeJSON(h.AgentRegister, http.MethodPost, "/beacon/v2/agent/register", token, body)
	if code != http.StatusOK || parsed["status"] != model.AgentIdentityStatusActive {
		t.Fatalf("已确认身份再次注册应 200 active，实际 %d：%v", code, parsed)
	}
}

func TestV2NamespaceCreateHTTPReturnsOneTimeToken(t *testing.T) {
	db, _, h := newV2HandlerTestService(t)
	code, parsed := invokeJSON(h.CreateNamespace, http.MethodPost, "/admin/v2/namespaces", "", map[string]any{
		"name":        "prod",
		"description": "生产环境",
	})
	if code != http.StatusCreated {
		t.Fatalf("创建 namespace 应 201，实际 %d：%v", code, parsed)
	}
	token, _ := parsed["accessToken"].(string)
	if token == "" {
		t.Fatalf("创建响应应只返回一次明文 token，实际 %v", parsed)
	}
	if parsed["accessTokenHash"] != nil {
		t.Fatalf("响应不得暴露 token hash：%v", parsed)
	}

	var ns model.Namespace
	if err := db.Where("code = ?", "prod").First(&ns).Error; err != nil {
		t.Fatalf("namespace 应已落库: %v", err)
	}
	if ns.AccessTokenHash == "" || ns.AccessTokenHash == token {
		t.Fatalf("库中应只保存 token 哈希，实际 hash=%q token=%q", ns.AccessTokenHash, token)
	}
}

func invokeJSON(handler http.HandlerFunc, method, path, token string, body any) (int, map[string]any) {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Beacon-Token", token)
	}
	rr := httptest.NewRecorder()
	handler(rr, req)
	return decodeRecorder(rr)
}

func invokeJSONWithParam(handler http.HandlerFunc, method, path, token string, body any, key, value string) (int, map[string]any) {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Beacon-Token", token)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	handler(rr, req)
	return decodeRecorder(rr)
}

func decodeRecorder(rr *httptest.ResponseRecorder) (int, map[string]any) {
	var parsed map[string]any
	if rr.Body.Len() > 0 {
		_ = json.Unmarshal(rr.Body.Bytes(), &parsed)
	}
	return rr.Code, parsed
}
