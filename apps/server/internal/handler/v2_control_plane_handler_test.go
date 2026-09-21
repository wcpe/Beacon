package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
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
		&model.LobbyCluster{},
		&model.Server{}, &model.ServerTag{},
		&model.AgentIdentity{},
		&model.AgentEndpoint{},
		&model.ApprovalRequest{},
		&model.AuditLog{},
	); err != nil {
		t.Fatalf("迁移 v2 表失败: %v", err)
	}
	svc := service.NewV2ControlPlaneService(db)
	approvalRegistry := authz.NewApprovalRegistry()
	approvalService := service.NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), approvalRegistry)
	svc.SetApprovalService(approvalService)
	service.RegisterV2ControlPlaneApprovalAdapters(approvalRegistry, svc)
	return db, svc, NewV2ControlPlaneHandler(svc)
}

func approveV2IdentityForHandlerFixture(t *testing.T, db *gorm.DB, svc *service.V2ControlPlaneService, identityID, serverID string) {
	t.Helper()
	ticket, err := svc.RequestApproveAgentIdentity(identityID, service.ApproveAgentIdentityParams{ServerID: serverID, Operator: "admin", Reason: "测试确认"}, auth.HumanPrincipal("admin"), "handler-"+identityID)
	if err != nil {
		t.Fatalf("创建身份审批申请失败: %v", err)
	}
	runV2ApprovalForHandlerFixture(t, db, svc, ticket)
}

func runV2ApprovalForHandlerFixture(t *testing.T, db *gorm.DB, svc *service.V2ControlPlaneService, ticket service.ApprovalTicketView) {
	t.Helper()
	if err := db.AutoMigrate(&model.ApprovalExecutionReceipt{}); err != nil {
		t.Fatalf("迁移审批执行回执失败: %v", err)
	}
	registry := authz.NewApprovalRegistry()
	approvalService := service.NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	svc.SetApprovalService(approvalService)
	service.RegisterV2ControlPlaneApprovalAdapters(registry, svc)
	if _, err := approvalService.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("admin"), ""); err != nil {
		t.Fatalf("批准测试申请失败: %v", err)
	}
	if _, err := service.NewApprovalWorker(approvalService).RunOnce(); err != nil {
		t.Fatalf("执行测试申请失败: %v", err)
	}
}

func TestFR203AgentRegisterHTTPPendingThenActive(t *testing.T) {
	db, svc, h := newV2HandlerTestService(t)
	_, token, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}

	body := map[string]any{
		"identityId":   "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"kind":         model.ServerKindBackend,
		"bootId":       "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		"agentVersion": "0.21.0",
	}
	code, parsed := invokeJSON(h.AgentRegister, http.MethodPost, "/beacon/v2/agent/register", token, body)
	if code != http.StatusAccepted || parsed["status"] != model.AgentIdentityStatusPending {
		t.Fatalf("首次注册应 202 pending，实际 %d：%v", code, parsed)
	}
	if parsed["namespace"] != "prod" || parsed["serverId"] != nil {
		t.Fatalf("pending 注册响应应带 token 归属 namespace 且 serverId 为 null，实际 %v", parsed)
	}
	if parsed["boundAt"] != nil || parsed["bindingFingerprint"] != nil {
		t.Fatalf("pending 注册不得伪造绑定快照，实际 %v", parsed)
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
	if approveCode != http.StatusBadRequest {
		t.Fatalf("审批请求缺 serverId 应 400，实际 %d：%v", approveCode, approveBody)
	}

	approveCode, approveBody = invokeJSONWithParam(
		h.ApproveAgentIdentity,
		http.MethodPost,
		"/admin/v2/agent-identities/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/approve",
		"",
		map[string]any{"serverId": "lobby-203-http", "forceUnbindOccupier": false, "reason": "确认身份"},
		"identityId",
		"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	)
	if approveCode != http.StatusAccepted || approveBody["status"] != model.ApprovalStatusPending {
		t.Fatalf("确认身份应创建审批请求，实际 %d：%v", approveCode, approveBody)
	}
	approveV2IdentityForHandlerFixture(t, db, svc, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "lobby-203-http")

	code, parsed = invokeJSON(h.AgentRegister, http.MethodPost, "/beacon/v2/agent/register", token, body)
	if code != http.StatusOK || parsed["status"] != model.AgentIdentityStatusActive || parsed["serverId"] != "lobby-203-http" {
		t.Fatalf("已确认身份再次注册应 200 active，实际 %d：%v", code, parsed)
	}
	if _, ok := parsed["boundAt"].(string); !ok {
		t.Fatalf("active 注册必须返回服务端权威 boundAt，实际 %v", parsed)
	}
	fingerprint, ok := parsed["bindingFingerprint"].(string)
	if !ok || len(fingerprint) != 64 {
		t.Fatalf("active 注册必须返回 SHA-256 绑定指纹，实际 %v", parsed)
	}

	pollCode, poll := invokeJSON(h.AgentRegistration, http.MethodGet,
		"/beacon/v2/agent/registration?identityId=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", token, nil)
	if pollCode != http.StatusOK || poll["boundAt"] != parsed["boundAt"] || poll["bindingFingerprint"] != fingerprint {
		t.Fatalf("active 轮询必须复用同一权威绑定快照，实际 %d：%v", pollCode, poll)
	}
	if err := db.Model(&model.AgentIdentity{}).
		Where("identity_id = ?", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa").Update("bound_at", nil).Error; err != nil {
		t.Fatalf("构造缺 boundAt 的 active 脏数据失败: %v", err)
	}
	pollCode, poll = invokeJSON(h.AgentRegistration, http.MethodGet,
		"/beacon/v2/agent/registration?identityId=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", token, nil)
	if pollCode != http.StatusOK || poll["boundAt"] != nil || poll["bindingFingerprint"] != nil {
		t.Fatalf("字段缺失时 HTTP 契约必须显式返回 null 且不得伪造，实际 %d：%v", pollCode, poll)
	}
}

func TestFR204AgentRegisterHTTPUsesTCPRemoteAddress(t *testing.T) {
	db, svc, h := newV2HandlerTestService(t)
	_, token, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	body := map[string]any{
		"identityId": "20400000-0000-4000-8000-000000000011", "kind": model.ServerKindProxy, "bootId": "boot-204-http",
		"listeners": []map[string]any{{"bindHost": "0.0.0.0", "port": 25577, "ordinal": 0}, {"bindHost": "::", "port": 25578, "ordinal": 1}},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/beacon/v2/agent/register", bytes.NewReader(raw))
	req.RemoteAddr = "203.0.113.20:46321"
	req.Header.Set("X-Beacon-Token", token)
	req.Header.Set("X-Forwarded-For", "198.51.100.99")
	req.Header.Set("X-Real-IP", "198.51.100.98")
	rr := httptest.NewRecorder()
	h.AgentRegister(rr, req)
	code, response := decodeRecorder(rr)
	if code != http.StatusAccepted || response["address"] != "203.0.113.20:25577" {
		t.Fatalf("注册响应必须投影 TCP 对端地址，实际 %d：%v", code, response)
	}
	endpoints, ok := response["endpoints"].([]any)
	if !ok || len(endpoints) != 2 {
		t.Fatalf("BC 注册响应必须返回完整 listener 列表，实际 %v", response)
	}
	var ident model.AgentIdentity
	if err := db.Where("identity_id = ?", body["identityId"]).First(&ident).Error; err != nil {
		t.Fatalf("读取身份失败: %v", err)
	}
	var endpoint model.AgentEndpoint
	if err := db.Where("agent_identity_id = ? AND ordinal = ?", ident.ID, 0).First(&endpoint).Error; err != nil {
		t.Fatalf("读取首个 endpoint 失败: %v", err)
	}
	if endpoint.DetectedAddress != "203.0.113.20:25577" || endpoint.DetectedAddress == "198.51.100.99:25577" {
		t.Fatalf("探测地址不得信任代理头，实际 %+v", endpoint)
	}
	invalid := httptest.NewRequest(http.MethodPost, "/beacon/v2/agent/register", bytes.NewBufferString(`{"identityId":"20400000-0000-4000-8000-000000000012","kind":"proxy","bootId":"boot-204-null","listeners":null}`))
	invalid.RemoteAddr = "203.0.113.20:46322"
	invalid.Header.Set("X-Beacon-Token", token)
	invalidRecorder := httptest.NewRecorder()
	h.AgentRegister(invalidRecorder, invalid)
	if invalidRecorder.Code != http.StatusBadRequest {
		t.Fatalf("显式 null listeners 必须拒绝，实际 %d：%s", invalidRecorder.Code, invalidRecorder.Body.String())
	}
}

func TestFR204EndpointOverrideAuditRecordsBindingAndTrace(t *testing.T) {
	db, svc, h := newV2HandlerTestService(t)
	_, token, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	identityID := "20400000-0000-4000-8000-000000000013"
	if _, err := svc.RegisterAgentV2(service.AgentRegisterV2Params{
		Token: token, IdentityID: identityID, Kind: model.ServerKindProxy, BootID: "boot-204-endpoint",
		DetectedHost: "203.0.113.21", ListenersProvided: true,
		Listeners: []service.AgentEndpointReport{{BindHost: "0.0.0.0", Port: 25577, Ordinal: 0}},
	}); err != nil {
		t.Fatalf("注册 endpoint 身份失败: %v", err)
	}
	approveV2IdentityForHandlerFixture(t, db, svc, identityID, "bc-endpoint")
	var endpoint model.AgentEndpoint
	if err := db.Joins("JOIN agent_identity ON agent_identity.id = agent_endpoint.agent_identity_id").
		Where("agent_identity.identity_id = ?", identityID).First(&endpoint).Error; err != nil {
		t.Fatalf("读取 endpoint 失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/admin/v2/agent-identities/"+identityID+"/endpoints/"+endpoint.EndpointKey,
		bytes.NewBufferString(`{"overrideAddress":"proxy.example.com:25577","reason":"公网 NAT 映射"}`))
	ctx := render.WithTraceID(req.Context(), "handler-trace-204")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("identityId", identityID)
	rctx.URLParams.Add("endpointKey", endpoint.EndpointKey)
	req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()
	h.SetAgentEndpointOverride(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("设置 endpoint 覆盖应成功，实际 %d：%s", rr.Code, rr.Body.String())
	}
	var audit model.AuditLog
	if err := db.Where("action = ?", "identity.endpoint_override_changed").First(&audit).Error; err != nil {
		t.Fatalf("读取 endpoint 覆盖审计失败: %v", err)
	}
	var detail struct {
		ServerID *string `json:"serverId"`
		TraceID  string  `json:"traceId"`
	}
	if err := json.Unmarshal([]byte(audit.Detail), &detail); err != nil {
		t.Fatalf("解析 endpoint 覆盖审计详情失败: %v", err)
	}
	if detail.ServerID == nil || *detail.ServerID != "bc-endpoint" || detail.TraceID != "handler-trace-204" {
		t.Fatalf("审计必须保存操作时绑定 serverId 与请求 traceId，实际 %+v", detail)
	}
	if strings.Contains(audit.Detail, "proxy.example.com:25577") {
		t.Fatalf("审计不得回显 endpoint 地址，实际 %s", audit.Detail)
	}
}

// TestFR199ListServersHTTPProjectsNullableLobbyClusterID 锁定 GET servers 的大厅归属 JSON 契约。
func TestFR199ListServersHTTPProjectsNullableLobbyClusterID(t *testing.T) {
	db, svc, h := newV2HandlerTestService(t)
	ns, _, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	var lobby model.LobbyCluster
	if err := db.Where("namespace_id = ?", ns.ID).First(&lobby).Error; err != nil {
		t.Fatalf("读取大厅集群失败: %v", err)
	}
	if err := db.Create(&model.Server{NamespaceID: ns.ID, ServerID: "lobby-http", Kind: model.ServerKindBackend, LobbyClusterID: &lobby.ID}).Error; err != nil {
		t.Fatalf("创建大厅成员失败: %v", err)
	}
	if err := db.Create(&model.Server{NamespaceID: ns.ID, ServerID: "regular-http", Kind: model.ServerKindBackend}).Error; err != nil {
		t.Fatalf("创建普通服务器失败: %v", err)
	}

	code, response := invokeJSON(h.ListServers, http.MethodGet, "/admin/v2/servers?namespaceId="+fmt.Sprint(ns.ID), "", nil)
	if code != http.StatusOK {
		t.Fatalf("读取 server 列表应成功，实际 %d：%v", code, response)
	}
	rawItems, ok := response["items"].([]any)
	if !ok || len(rawItems) != 2 {
		t.Fatalf("响应应含两台服务器，实际 %v", response)
	}
	items := map[string]map[string]any{}
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("列表项应为对象，实际 %T", raw)
		}
		items[item["serverId"].(string)] = item
	}
	if item := items["lobby-http"]; item["lobbyClusterId"] != float64(lobby.ID) || item["assigned"] != true {
		t.Fatalf("大厅成员 JSON 应带 lobbyClusterId 且 assigned=true，实际 %v", item)
	}
	if item := items["regular-http"]; item["lobbyClusterId"] != nil || item["assigned"] != false {
		t.Fatalf("普通服务器 JSON 应带 lobbyClusterId=null 且 assigned=false，实际 %v", item)
	}
}

// TestFR215ServerLifecycleImpactHTTP 锁定 lifecycle-impact 只读接口的 action 边界与响应摘要。
func TestFR215ServerLifecycleImpactHTTP(t *testing.T) {
	db, svc, h := newV2HandlerTestService(t)
	ns, _, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	server := model.Server{NamespaceID: ns.ID, ServerID: "impact-http", Kind: model.ServerKindBackend}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("创建 server 失败: %v", err)
	}
	code, response := invokeJSONWithParam(h.ServerLifecycleImpact, http.MethodGet, "/admin/v2/servers/"+fmt.Sprint(server.ID)+"/lifecycle-impact?action=archive", "", nil, "id", fmt.Sprint(server.ID))
	if code != http.StatusOK || response["action"] != "archive" || response["targetLifecycle"] != model.ServerLifecycleArchived {
		t.Fatalf("archive impact 响应不正确，code=%d response=%v", code, response)
	}
	code, _ = invokeJSONWithParam(h.ServerLifecycleImpact, http.MethodGet, "/admin/v2/servers/"+fmt.Sprint(server.ID)+"/lifecycle-impact?action=invalid", "", nil, "id", fmt.Sprint(server.ID))
	if code == http.StatusOK {
		t.Fatalf("非法 action 不应成功")
	}
}

func TestLifecycleApprovalRequestHTTPCreatesUnifiedTicket(t *testing.T) {
	_, svc, h := newV2HandlerTestService(t)
	namespace, _, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "lifecycle-http", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建测试 namespace 失败: %v", err)
	}
	code, response := invokeJSON(h.CreateLifecycleApprovalRequest, http.MethodPost, "/admin/v2/approval-requests", "", map[string]any{
		"operationKey": "namespace.archive", "parameters": map[string]any{"namespaceId": namespace.ID}, "reason": "归档已退役环境",
	})
	if code != http.StatusAccepted || response["approvalRequestId"] == "" || response["operationKey"] != "namespace.archive" {
		t.Fatalf("生命周期统一申请应返回 202 审批票据，code=%d response=%v", code, response)
	}
}

func TestNamespaceLifecycleImpactHTTP(t *testing.T) {
	_, svc, h := newV2HandlerTestService(t)
	namespace, _, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "impact-http", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建测试 namespace 失败: %v", err)
	}
	code, response := invokeJSONWithParam(h.NamespaceLifecycleImpact, http.MethodGet, "/admin/v2/namespaces/1/lifecycle-impact?action=archive", "", nil, "id", fmt.Sprint(namespace.ID))
	if code != http.StatusOK || response["namespaceId"] != float64(namespace.ID) || response["targetLifecycle"] != model.NamespaceLifecycleArchived {
		t.Fatalf("namespace 生命周期影响预览不正确，code=%d response=%v", code, response)
	}
}

func arrangeFR203AgentIdentityBindingFacts(t *testing.T) (*V2ControlPlaneHandler, string, string) {
	t.Helper()
	db, svc, h := newV2HandlerTestService(t)
	_, token, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	pendingID := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	if _, err := svc.RegisterAgentV2(service.AgentRegisterV2Params{
		Token: token, IdentityID: pendingID, Kind: model.ServerKindBackend, BootID: "boot-list-detail-pending",
	}); err != nil {
		t.Fatalf("注册待确认身份失败: %v", err)
	}
	identityID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	if _, err := svc.RegisterAgentV2(service.AgentRegisterV2Params{
		Token: token, IdentityID: identityID, ServerID: "legacy-lobby", Kind: model.ServerKindBackend, BootID: "boot-list-detail",
	}); err != nil {
		t.Fatalf("注册 legacy 身份失败: %v", err)
	}
	approveV2IdentityForHandlerFixture(t, db, svc, identityID, "legacy-lobby")
	if _, err := svc.RegisterAgentV2(service.AgentRegisterV2Params{
		Token: token, IdentityID: identityID, Kind: model.ServerKindBackend, BootID: "boot-list-detail-migrated",
	}); err != nil {
		t.Fatalf("触发 legacy 迁移失败: %v", err)
	}
	return h, identityID, pendingID
}

func TestFR203AgentIdentityListExposesBindingFacts(t *testing.T) {
	h, identityID, pendingID := arrangeFR203AgentIdentityBindingFacts(t)
	listCode, items := invokeJSONList(h.ListAgentIdentities, http.MethodGet, "/admin/v2/agent-identities", nil)
	if listCode != http.StatusOK || len(items) != 2 {
		t.Fatalf("身份列表应返回两个项目，实际 %d：%v", listCode, items)
	}
	item, pendingItem := identityListItem(items, identityID), identityListItem(items, pendingID)
	if item == nil || pendingItem == nil {
		t.Fatalf("身份列表缺少预期项目，实际 %v", items)
	}
	if item["serverId"] != "legacy-lobby" || item["bindingSource"] != model.AgentIdentityBindingSourceLegacyLocal ||
		item["migrationState"] != "completed" || item["legacyMigratedAt"] == nil || item["boundAt"] == nil {
		t.Fatalf("列表应返回完整绑定事实，实际 %v", item)
	}
	if _, exists := item["bindingFingerprint"]; exists {
		t.Fatalf("列表不得暴露仅详情需要的绑定指纹，实际 %v", item)
	}
	if pendingItem["serverId"] != nil || pendingItem["boundAt"] != nil || pendingItem["legacyMigratedAt"] != nil ||
		pendingItem["bindingSource"] != model.AgentIdentityBindingSourceAdminAssigned || pendingItem["migrationState"] != "not_required" {
		t.Fatalf("待确认身份的缺失绑定事实必须显式为 null，实际 %v", pendingItem)
	}
}

func TestFR203AgentIdentityDetailReusesListBindingFacts(t *testing.T) {
	h, identityID, pendingID := arrangeFR203AgentIdentityBindingFacts(t)
	listCode, items := invokeJSONList(h.ListAgentIdentities, http.MethodGet, "/admin/v2/agent-identities", nil)
	if listCode != http.StatusOK || len(items) != 2 {
		t.Fatalf("身份列表应返回两个项目，实际 %d：%v", listCode, items)
	}
	item, pendingItem := identityListItem(items, identityID), identityListItem(items, pendingID)
	if item == nil || pendingItem == nil {
		t.Fatalf("身份列表缺少预期项目，实际 %v", items)
	}

	detailCode, detail := invokeJSONWithParam(
		h.GetAgentIdentity, http.MethodGet, "/admin/v2/agent-identities/"+identityID, "", nil, "identityId", identityID,
	)
	if detailCode != http.StatusOK || detail["serverId"] != item["serverId"] ||
		detail["bindingSource"] != item["bindingSource"] || detail["migrationState"] != item["migrationState"] ||
		detail["legacyMigratedAt"] != item["legacyMigratedAt"] || detail["boundAt"] != item["boundAt"] {
		t.Fatalf("详情必须复用列表的绑定事实，实际 %d：%v", detailCode, detail)
	}
	fingerprint, ok := detail["bindingFingerprint"].(string)
	if !ok || len(fingerprint) != 64 {
		t.Fatalf("详情必须返回服务端权威绑定指纹，实际 %v", detail)
	}
	pendingDetailCode, pendingDetail := invokeJSONWithParam(
		h.GetAgentIdentity, http.MethodGet, "/admin/v2/agent-identities/"+pendingID, "", nil, "identityId", pendingID,
	)
	if pendingDetailCode != http.StatusOK || pendingDetail["serverId"] != pendingItem["serverId"] ||
		pendingDetail["boundAt"] != pendingItem["boundAt"] || pendingDetail["legacyMigratedAt"] != pendingItem["legacyMigratedAt"] ||
		pendingDetail["bindingFingerprint"] != nil || pendingDetail["migrationState"] != pendingItem["migrationState"] {
		t.Fatalf("待确认详情的缺失绑定事实必须显式为 null 并复用列表，实际 %d：%v", pendingDetailCode, pendingDetail)
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
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.HumanPrincipal("test-admin")))
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
	req = req.WithContext(auth.WithPrincipal(req.Context(), auth.HumanPrincipal("test-admin")))
	rr := httptest.NewRecorder()
	handler(rr, req)
	return decodeRecorder(rr)
}

func invokeJSONList(handler http.HandlerFunc, method, path string, body any) (int, []map[string]any) {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler(rr, req)
	var parsed struct {
		Items []map[string]any `json:"items"`
	}
	if rr.Body.Len() > 0 {
		_ = json.Unmarshal(rr.Body.Bytes(), &parsed)
	}
	return rr.Code, parsed.Items
}

func identityListItem(items []map[string]any, identityID string) map[string]any {
	for _, item := range items {
		if item["identityId"] == identityID {
			return item
		}
	}
	return nil
}

func decodeRecorder(rr *httptest.ResponseRecorder) (int, map[string]any) {
	var parsed map[string]any
	if rr.Body.Len() > 0 {
		_ = json.Unmarshal(rr.Body.Bytes(), &parsed)
	}
	return rr.Code, parsed
}

// TestFR226ServerWorkDirRoundTrip 校验 FR-226：agent 上报的服务器工作目录落库并在身份视图回显；
// 旧 agent 未上报时落空串、视图输出 null（前端降级展示「未上报」），不报错。
func TestFR226ServerWorkDirRoundTrip(t *testing.T) {
	db, svc, _ := newV2HandlerTestService(t)
	_, token, err := svc.CreateV2Namespace(service.CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	identityID := "22600000-0000-4000-8000-000000000001"
	if _, err := svc.RegisterAgentV2(service.AgentRegisterV2Params{
		Token: token, IdentityID: identityID, Kind: model.ServerKindBackend, BootID: "boot-226",
		Addr: "10.0.0.5:25565", DetectedHost: "10.0.0.5", ServerWorkDir: "/srv/mc/game-1",
	}); err != nil {
		t.Fatalf("注册上报目录的身份失败: %v", err)
	}
	var ident model.AgentIdentity
	if err := db.Where("identity_id = ?", identityID).First(&ident).Error; err != nil {
		t.Fatalf("读取身份失败: %v", err)
	}
	if ident.ServerWorkDir != "/srv/mc/game-1" {
		t.Fatalf("工作目录未落库，实际 %q", ident.ServerWorkDir)
	}
	if got := agentIdentityView(&ident)["serverWorkDir"]; got != "/srv/mc/game-1" {
		t.Fatalf("身份视图未回显工作目录，实际 %v", got)
	}

	// 旧 agent（未上报）→ 落空串、视图输出 null，供前端降级。
	legacyID := "22600000-0000-4000-8000-000000000002"
	if _, err := svc.RegisterAgentV2(service.AgentRegisterV2Params{
		Token: token, IdentityID: legacyID, Kind: model.ServerKindBackend, BootID: "boot-226-legacy",
		Addr: "10.0.0.6:25565", DetectedHost: "10.0.0.6",
	}); err != nil {
		t.Fatalf("注册未上报目录的身份失败: %v", err)
	}
	var legacy model.AgentIdentity
	if err := db.Where("identity_id = ?", legacyID).First(&legacy).Error; err != nil {
		t.Fatalf("读取旧 agent 身份失败: %v", err)
	}
	if legacy.ServerWorkDir != "" {
		t.Fatalf("未上报目录应落空串，实际 %q", legacy.ServerWorkDir)
	}
	if got := agentIdentityView(&legacy)["serverWorkDir"]; got != nil {
		t.Fatalf("未上报目录视图应输出 null，实际 %v", got)
	}
}
