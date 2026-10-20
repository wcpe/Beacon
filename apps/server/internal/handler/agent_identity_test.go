package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// agentReportAuthStub 是 v1 数据面身份校验器的测试替身：返回预置的权威身份或错误，并记录调用次数。
type agentReportAuthStub struct {
	identity       agentauth.Identity
	err            error
	calls          int
	dirResyncCalls int
}

func (s *agentReportAuthStub) AuthenticateAgentReport(_, _, _, _ string) (agentauth.Identity, error) {
	s.calls++
	return s.identity, s.err
}

// ReceiveDirectoryResyncResult 让本桩同时满足 commandReportAuthenticator（命令结果端点用）。
// 身份闸用例不关心目录重同步回执，故仅计数不产生副作用。
func (s *agentReportAuthStub) ReceiveDirectoryResyncResult(_ agentauth.Identity, _ uint, _ bool, _ string) error {
	s.dirResyncCalls++
	return nil
}

// testAgentIdentity 是本文件所有用例共用的权威身份：prod / lobby-1。
func testAgentIdentity() agentauth.Identity {
	return agentauth.Identity{NamespaceID: 1, Namespace: "prod", ServerID: "lobby-1", IdentityID: "identity-1"}
}

// newAgentIdentityTestSvc 构造只挂内存注册表的实例服务（心跳 / 上报 / 声明 / 发现都只读它）。
func newAgentIdentityTestSvc(t *testing.T) (*service.InstanceService, *runtime.Registry) {
	t.Helper()
	reg := runtime.NewRegistry()
	return service.NewInstanceService(nil, reg, nil, nil, nil, 10*time.Second, 30*time.Second), reg
}

// registerTestInstance 在运行态注册表登记一台在线实例（供正向用例走到 200）。
func registerTestInstance(t *testing.T, reg *runtime.Registry, namespace, serverID string) {
	t.Helper()
	if _, err := reg.Register(&runtime.Instance{Namespace: namespace, ServerID: serverID, Role: "bukkit"}, time.Minute, time.Now().UTC()); err != nil {
		t.Fatalf("登记在线实例 %s 失败: %v", serverID, err)
	}
}

// newAgentTestRequest 构造带指定身份头的请求（空串表示该头缺省）。
func newAgentTestRequest(method, target string, body any, identityID, bootID string) *http.Request {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, target, bytes.NewReader(payload))
	if identityID != "" {
		r.Header.Set("X-Beacon-Identity", identityID)
	}
	if bootID != "" {
		r.Header.Set("X-Beacon-Boot", bootID)
	}
	return r
}

// errCode 取出响应体里的错误 code（非 JSON 或空体返回空串）。
func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		return ""
	}
	return body.Code
}

// assertUnauthorized 断言缺身份被 401 拒，且校验器确实被调用一次（没有在别处静默放行）。
func assertUnauthorized(t *testing.T, w *httptest.ResponseRecorder, stub *agentReportAuthStub) {
	t.Helper()
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("缺身份头应 401，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	if stub.calls != 1 {
		t.Fatalf("身份校验器应被调用一次，实际 %d", stub.calls)
	}
}

// assertNotLeakedNotFound 断言身份与请求自报归属不符时回 404 COMMAND_NOT_FOUND：
// 不能是 403 / NOT_REGISTERED / INSTANCE_NOT_FOUND —— 那些等于告诉调用方「目标确实存在」。
func assertNotLeakedNotFound(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusNotFound {
		t.Fatalf("身份与自报归属不符应 404，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	if code := errCode(t, w); code != "COMMAND_NOT_FOUND" {
		t.Fatalf("不一致必须回 COMMAND_NOT_FOUND（不泄露存在性），实际 %q", code)
	}
}

// assertPassedIdentityGate 断言请求已越过身份闸：既不是 401，也不是 COMMAND_NOT_FOUND。
// 用例刻意不装配下游服务，故只验证「没被身份闸拦下」，不验证业务结果。
func assertPassedIdentityGate(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("身份一致的请求不得被判未授权，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	if code := errCode(t, w); code == "COMMAND_NOT_FOUND" {
		t.Fatalf("身份一致的请求不得被判命令不存在，实际 %d（body=%s）", w.Code, w.Body.String())
	}
}

// newAgentIdentityCommandSvc 构造内存 sqlite 命令服务（命令拉取 / 回传端点用）。
func newAgentIdentityCommandSvc(t *testing.T) (*gorm.DB, *service.AgentCommandService) {
	t.Helper()
	dsn := "file:" + url.QueryEscape(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AgentCommand{}); err != nil {
		t.Fatalf("迁移 agent_command 失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	})
	return db, service.NewAgentCommandService(db, repository.NewAgentCommandRepository(db), nil, nil)
}

// seedAgentPendingCommand 建一条指定归属的 pending 命令。
func seedAgentPendingCommand(t *testing.T, db *gorm.DB, namespace, serverID string) *model.AgentCommand {
	t.Helper()
	cmd := &model.AgentCommand{
		NamespaceCode: namespace, ServerID: serverID,
		Type: model.CommandTypeResyncConfig, Status: model.CommandStatusPending,
		Payload: `{"mode":"scan"}`, Operator: "alice",
	}
	if err := db.Create(cmd).Error; err != nil {
		t.Fatalf("建 pending 命令失败: %v", err)
	}
	return cmd
}

// —— 共享助手：bindAgentTarget / bindAgentNamespace / authenticateAgentReport ——

func TestBindAgentTargetFillsMissingAndRejectsMismatch(t *testing.T) {
	identity := testAgentIdentity()
	cases := []struct {
		name         string
		namespace    string
		serverID     string
		wantOK       bool
		wantNS       string
		wantServerID string
	}{
		// 缺参不在本函数补：原样返回空值，入参契约由各端点原有校验负责。
		{name: "全缺省原样返回", wantOK: true, wantNS: "", wantServerID: ""},
		{name: "与身份一致放行", namespace: "prod", serverID: "lobby-1", wantOK: true, wantNS: "prod", wantServerID: "lobby-1"},
		{name: "跨 namespace 拒", namespace: "test", serverID: "lobby-1"},
		{name: "他服 serverId 拒", namespace: "prod", serverID: "lobby-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			ns, serverID, ok := bindAgentTarget(w, httptest.NewRequest(http.MethodGet, "/x", nil), identity, tc.namespace, tc.serverID)
			if ok != tc.wantOK {
				t.Fatalf("ok want %v 实际 %v（body=%s）", tc.wantOK, ok, w.Body.String())
			}
			if !tc.wantOK {
				assertNotLeakedNotFound(t, w)
				return
			}
			if ns != tc.wantNS || serverID != tc.wantServerID {
				t.Fatalf("归属回填错误：want (%s,%s) 实际 (%s,%s)", tc.wantNS, tc.wantServerID, ns, serverID)
			}
		})
	}
}

func TestBindAgentNamespaceNarrowsToIdentity(t *testing.T) {
	identity := testAgentIdentity()
	w := httptest.NewRecorder()
	ns, ok := bindAgentNamespace(w, httptest.NewRequest(http.MethodGet, "/x", nil), identity, "")
	if !ok || ns != "prod" {
		t.Fatalf("缺省 namespace 应收敛到身份 namespace，实际 ok=%v ns=%q", ok, ns)
	}
	w = httptest.NewRecorder()
	if _, ok := bindAgentNamespace(w, httptest.NewRequest(http.MethodGet, "/x", nil), identity, "test"); ok {
		t.Fatal("显式跨环境发现请求必须被拒")
	}
	assertNotLeakedNotFound(t, w)
}

func TestAuthenticateAgentReportFailClosedWithoutAuthenticator(t *testing.T) {
	w := httptest.NewRecorder()
	if _, ok := authenticateAgentReport(w, httptest.NewRequest(http.MethodGet, "/x", nil), nil); ok {
		t.Fatal("校验器未注入时必须失败关闭，不能放行")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("校验器缺失应 401，实际 %d", w.Code)
	}
}

// —— POST /beacon/v1/agent/heartbeat ——

func TestHeartbeatBindsReporterIdentity(t *testing.T) {
	svc, reg := newAgentIdentityTestSvc(t)
	registerTestInstance(t, reg, "prod", "lobby-1")
	h := NewAgentHandler(svc, nil, newStubHealthSettings())
	h.SetReportAuthenticator(&agentReportAuthStub{identity: testAgentIdentity()})

	w := httptest.NewRecorder()
	h.Heartbeat(w, newAgentTestRequest(http.MethodPost, "/beacon/v1/agent/heartbeat",
		map[string]any{"namespace": "prod", "serverId": "lobby-1"}, "identity-1", "boot-1"))
	if w.Code != http.StatusOK {
		t.Fatalf("身份一致的心跳应 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}

	// 冒充他服：不得替别人的实例续命。
	w = httptest.NewRecorder()
	h.Heartbeat(w, newAgentTestRequest(http.MethodPost, "/beacon/v1/agent/heartbeat",
		map[string]any{"namespace": "prod", "serverId": "ghost"}, "identity-1", "boot-1"))
	assertNotLeakedNotFound(t, w)

	// 跨环境替别人续命同样拒。
	w = httptest.NewRecorder()
	h.Heartbeat(w, newAgentTestRequest(http.MethodPost, "/beacon/v1/agent/heartbeat",
		map[string]any{"namespace": "test", "serverId": "lobby-1"}, "identity-1", "boot-1"))
	assertNotLeakedNotFound(t, w)
}

func TestHeartbeatRejectsMissingIdentity(t *testing.T) {
	svc, reg := newAgentIdentityTestSvc(t)
	registerTestInstance(t, reg, "prod", "lobby-1")
	h := NewAgentHandler(svc, nil, newStubHealthSettings())
	stub := &agentReportAuthStub{err: apperr.ErrUnauthorized}
	h.SetReportAuthenticator(stub)

	w := httptest.NewRecorder()
	h.Heartbeat(w, newAgentTestRequest(http.MethodPost, "/beacon/v1/agent/heartbeat",
		map[string]any{"namespace": "prod", "serverId": "lobby-1"}, "", ""))
	assertUnauthorized(t, w, stub)
}

// —— POST /beacon/v1/agent/report ——

func TestReportBindsReporterIdentity(t *testing.T) {
	svc, reg := newAgentIdentityTestSvc(t)
	registerTestInstance(t, reg, "prod", "lobby-1")
	h := NewAgentHandler(svc, nil, newStubHealthSettings())
	h.SetReportAuthenticator(&agentReportAuthStub{identity: testAgentIdentity()})

	w := httptest.NewRecorder()
	h.Report(w, newAgentTestRequest(http.MethodPost, "/beacon/v1/agent/report",
		map[string]any{"namespace": "prod", "serverId": "lobby-1", "playerCount": 3, "tps": 20.0}, "identity-1", "boot-1"))
	if w.Code != http.StatusOK {
		t.Fatalf("身份一致的上报应 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	h.Report(w, newAgentTestRequest(http.MethodPost, "/beacon/v1/agent/report",
		map[string]any{"namespace": "prod", "serverId": "lobby-9", "playerCount": 3, "tps": 20.0}, "identity-1", "boot-1"))
	assertNotLeakedNotFound(t, w)
}

func TestReportRejectsMissingIdentity(t *testing.T) {
	svc, reg := newAgentIdentityTestSvc(t)
	registerTestInstance(t, reg, "prod", "lobby-1")
	h := NewAgentHandler(svc, nil, newStubHealthSettings())
	stub := &agentReportAuthStub{err: apperr.ErrUnauthorized}
	h.SetReportAuthenticator(stub)

	w := httptest.NewRecorder()
	h.Report(w, newAgentTestRequest(http.MethodPost, "/beacon/v1/agent/report",
		map[string]any{"namespace": "prod", "serverId": "lobby-1"}, "", ""))
	assertUnauthorized(t, w, stub)
}

// —— POST /beacon/v1/agent/declaration ——

func TestDeclarationBindsReporterIdentity(t *testing.T) {
	svc, reg := newAgentIdentityTestSvc(t)
	registerTestInstance(t, reg, "prod", "lobby-1")
	h := NewAgentHandler(svc, nil, newStubHealthSettings())
	h.SetReportAuthenticator(&agentReportAuthStub{identity: testAgentIdentity()})

	capacity := 100
	w := httptest.NewRecorder()
	h.Declaration(w, newAgentTestRequest(http.MethodPost, "/beacon/v1/agent/declaration",
		map[string]any{"namespace": "prod", "serverId": "lobby-1", "capacity": capacity}, "identity-1", "boot-1"))
	if w.Code != http.StatusOK {
		t.Fatalf("身份一致的声明应 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}

	// 替别人改写容量 / 标签必须拒。
	w = httptest.NewRecorder()
	h.Declaration(w, newAgentTestRequest(http.MethodPost, "/beacon/v1/agent/declaration",
		map[string]any{"namespace": "prod", "serverId": "lobby-2", "capacity": capacity}, "identity-1", "boot-1"))
	assertNotLeakedNotFound(t, w)
}

func TestDeclarationRejectsMissingIdentity(t *testing.T) {
	svc, reg := newAgentIdentityTestSvc(t)
	registerTestInstance(t, reg, "prod", "lobby-1")
	h := NewAgentHandler(svc, nil, newStubHealthSettings())
	stub := &agentReportAuthStub{err: apperr.ErrUnauthorized}
	h.SetReportAuthenticator(stub)

	capacity := 100
	w := httptest.NewRecorder()
	h.Declaration(w, newAgentTestRequest(http.MethodPost, "/beacon/v1/agent/declaration",
		map[string]any{"namespace": "prod", "serverId": "lobby-1", "capacity": capacity}, "", ""))
	assertUnauthorized(t, w, stub)
}

// —— GET /beacon/v1/agent/discovery ——

func TestDiscoveryNarrowsToCallerNamespace(t *testing.T) {
	svc, reg := newAgentIdentityTestSvc(t)
	registerTestInstance(t, reg, "prod", "lobby-1")
	registerTestInstance(t, reg, "test", "other-1")
	h := NewAgentHandler(svc, nil, newStubHealthSettings())
	h.SetReportAuthenticator(&agentReportAuthStub{identity: testAgentIdentity()})

	// 缺省 namespace：此前会把所有环境的在线实例一起吐出来，现在只应看到本环境。
	w := httptest.NewRecorder()
	h.Discover(w, newAgentTestRequest(http.MethodGet, "/beacon/v1/agent/discovery", nil, "identity-1", "boot-1"))
	if w.Code != http.StatusOK {
		t.Fatalf("发现应 200，实际 %d（body=%s）", w.Code, w.Body.String())
	}
	var body struct {
		Instances []struct {
			ServerID string `json:"serverId"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析发现响应失败: %v", err)
	}
	if len(body.Instances) != 1 || body.Instances[0].ServerID != "lobby-1" {
		t.Fatalf("缺省 namespace 应只返回本环境实例 lobby-1，实际 %v（%s）", body.Instances, w.Body.String())
	}

	// 显式跨环境：拒。
	w = httptest.NewRecorder()
	h.Discover(w, newAgentTestRequest(http.MethodGet, "/beacon/v1/agent/discovery?namespace=test", nil, "identity-1", "boot-1"))
	assertNotLeakedNotFound(t, w)
}

func TestDiscoveryRejectsMissingIdentity(t *testing.T) {
	svc, _ := newAgentIdentityTestSvc(t)
	h := NewAgentHandler(svc, nil, newStubHealthSettings())
	stub := &agentReportAuthStub{err: apperr.ErrUnauthorized}
	h.SetReportAuthenticator(stub)

	w := httptest.NewRecorder()
	h.Discover(w, newAgentTestRequest(http.MethodGet, "/beacon/v1/agent/discovery", nil, "", ""))
	assertUnauthorized(t, w, stub)
}

// —— GET /beacon/v1/agent/config/effective ——

func TestEffectiveBindsReporterIdentity(t *testing.T) {
	svc, _ := newAgentIdentityTestSvc(t)
	h := NewAgentHandler(svc, nil, newStubHealthSettings())
	// 正向用例用一台**未注册**但属于该身份的实例：请求越过身份闸后会落到「未注册」，
	// 从而在不装配配置服务的前提下证明「身份闸放行了」。
	unregistered := agentauth.Identity{NamespaceID: 1, Namespace: "prod", ServerID: "ghost-2", IdentityID: "identity-2"}
	h.SetReportAuthenticator(&agentReportAuthStub{identity: unregistered})

	// 冒充他服：必须 404，而不是回别人的有效配置正文。
	w := httptest.NewRecorder()
	h.Effective(w, newAgentTestRequest(http.MethodGet, "/beacon/v1/agent/config/effective?namespace=prod&serverId=lobby-2", nil, "identity-2", "boot-1"))
	assertNotLeakedNotFound(t, w)

	// 身份一致：越过身份闸。
	w = httptest.NewRecorder()
	h.Effective(w, newAgentTestRequest(http.MethodGet, "/beacon/v1/agent/config/effective?namespace=prod&serverId=ghost-2", nil, "identity-2", "boot-1"))
	assertPassedIdentityGate(t, w)
}

func TestEffectiveRejectsMissingIdentity(t *testing.T) {
	svc, reg := newAgentIdentityTestSvc(t)
	registerTestInstance(t, reg, "prod", "lobby-1")
	h := NewAgentHandler(svc, nil, newStubHealthSettings())
	stub := &agentReportAuthStub{err: apperr.ErrUnauthorized}
	h.SetReportAuthenticator(stub)

	w := httptest.NewRecorder()
	h.Effective(w, newAgentTestRequest(http.MethodGet, "/beacon/v1/agent/config/effective?namespace=prod&serverId=lobby-1", nil, "", ""))
	assertUnauthorized(t, w, stub)
}

// —— 文件树 / 覆盖集拉取（四个会回正文的端点）——

func TestFilePullEndpointsBindReporterIdentity(t *testing.T) {
	svc, _ := newAgentIdentityTestSvc(t)
	// 四个端点正向用例统一用一台**未注册**但从属于该身份的实例：越过身份闸后落到「未注册」，
	// 从而在不装配有效配置 / 覆盖集服务（nil 会 panic）的前提下证明身份闸已放行。
	unregistered := agentauth.Identity{NamespaceID: 1, Namespace: "prod", ServerID: "ghost-2", IdentityID: "identity-2"}

	endpoints := []struct {
		name   string
		target string // 含 namespace=%s&serverId=%s 两处占位
		call   func(h *FileHandler, w http.ResponseWriter, r *http.Request)
	}{
		{"files/manifest", "/beacon/v1/agent/files/manifest?namespace=%s&serverId=%s",
			func(h *FileHandler, w http.ResponseWriter, r *http.Request) { h.Manifest(w, r) }},
		{"files/content", "/beacon/v1/agent/files/content?namespace=%s&serverId=%s&path=a.yml",
			func(h *FileHandler, w http.ResponseWriter, r *http.Request) { h.Content(w, r) }},
		{"override-sets", "/beacon/v1/agent/override-sets?namespace=%s&serverId=%s",
			func(h *FileHandler, w http.ResponseWriter, r *http.Request) { h.OverrideManifest(w, r) }},
		{"override-sets/content", "/beacon/v1/agent/override-sets/content?namespace=%s&serverId=%s&set=s1&path=a.yml",
			func(h *FileHandler, w http.ResponseWriter, r *http.Request) { h.OverrideContent(w, r) }},
	}
	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			// 缺身份 → 401。
			h := &FileHandler{insSvc: svc}
			stub := &agentReportAuthStub{err: apperr.ErrUnauthorized}
			h.SetReportAuthenticator(stub)
			w := httptest.NewRecorder()
			ep.call(h, w, newAgentTestRequest(http.MethodGet, fmt.Sprintf(ep.target, "prod", "ghost-2"), nil, "", ""))
			assertUnauthorized(t, w, stub)

			// 冒充他服 → 404 COMMAND_NOT_FOUND。
			h = &FileHandler{insSvc: svc}
			h.SetReportAuthenticator(&agentReportAuthStub{identity: unregistered})
			w = httptest.NewRecorder()
			ep.call(h, w, newAgentTestRequest(http.MethodGet, fmt.Sprintf(ep.target, "prod", "lobby-9"), nil, "identity-2", "boot-1"))
			assertNotLeakedNotFound(t, w)

			// 身份一致 → 越过身份闸。
			h = &FileHandler{insSvc: svc}
			h.SetReportAuthenticator(&agentReportAuthStub{identity: unregistered})
			w = httptest.NewRecorder()
			ep.call(h, w, newAgentTestRequest(http.MethodGet, fmt.Sprintf(ep.target, "prod", "ghost-2"), nil, "identity-2", "boot-1"))
			assertPassedIdentityGate(t, w)
		})
	}
}

// —— GET /beacon/v1/agent/commands（拉待办命令）——

func TestPendingCommandsBindsReporterIdentity(t *testing.T) {
	db, svc := newAgentIdentityCommandSvc(t)
	seedAgentPendingCommand(t, db, "prod", "lobby-1")
	other := seedAgentPendingCommand(t, db, "prod", "lobby-2")
	h := NewCommandHandler(svc, nil)
	h.SetReportAuthenticator(&agentReportAuthStub{identity: testAgentIdentity()})

	// 正向：身份一致 → 拉到自己的命令。
	w := httptest.NewRecorder()
	h.Pending(w, newAgentTestRequest(http.MethodGet, "/beacon/v1/agent/commands?namespace=prod&serverId=lobby-1", nil, "identity-1", "boot-1"))
	if w.Code != http.StatusOK {
		t.Fatalf("身份一致应拉到命令（200），实际 %d（body=%s）", w.Code, w.Body.String())
	}

	// 负向：冒充他服 → 404，且不得把对方的命令消费掉（fetched）。
	w = httptest.NewRecorder()
	h.Pending(w, newAgentTestRequest(http.MethodGet, "/beacon/v1/agent/commands?namespace=prod&serverId=lobby-2", nil, "identity-1", "boot-1"))
	assertNotLeakedNotFound(t, w)

	var got model.AgentCommand
	if err := db.First(&got, other.ID).Error; err != nil {
		t.Fatalf("读取对方命令失败: %v", err)
	}
	if got.Status != model.CommandStatusPending {
		t.Fatalf("越权拉取不得消费对方命令，实际状态 %s", got.Status)
	}
}

func TestPendingCommandsRejectsMissingIdentity(t *testing.T) {
	db, svc := newAgentIdentityCommandSvc(t)
	seedAgentPendingCommand(t, db, "prod", "lobby-1")
	h := NewCommandHandler(svc, nil)
	stub := &agentReportAuthStub{err: apperr.ErrUnauthorized}
	h.SetReportAuthenticator(stub)

	w := httptest.NewRecorder()
	h.Pending(w, newAgentTestRequest(http.MethodGet, "/beacon/v1/agent/commands?namespace=prod&serverId=lobby-1", nil, "", ""))
	assertUnauthorized(t, w, stub)
}

// —— 回传类端点（files/ingest、files/scan、files/error）——

// TestAgentWriteBackEndpointsRejectMissingIdentity 锁定回传类端点的身份闸：
// 它们写的是真源（落库文件 / 扫描清单 / 任务失败成因），此前仅凭共享 token 即可写入。
func TestAgentWriteBackEndpointsRejectMissingIdentity(t *testing.T) {
	_, cmdSvc := newAgentIdentityCommandSvc(t)
	commandHandler := NewCommandHandler(cmdSvc, nil)
	commandHandler.SetReportAuthenticator(&agentReportAuthStub{err: apperr.ErrUnauthorized})
	scanHandler := NewReverseFetchTaskHandler(nil, nil, nil)
	scanHandler.SetReportAuthenticator(&agentReportAuthStub{err: apperr.ErrUnauthorized})

	cases := []struct {
		name   string
		target string
		body   map[string]any
		call   func(w http.ResponseWriter, r *http.Request)
	}{
		{"files/ingest", "/beacon/v1/agent/files/ingest",
			map[string]any{"commandId": 1, "files": []map[string]string{{"path": "a.yml", "content": "a: 1"}}},
			func(w http.ResponseWriter, r *http.Request) { commandHandler.Ingest(w, r) }},
		{"files/scan", "/beacon/v1/agent/files/scan",
			map[string]any{"commandId": 1, "files": []map[string]any{}},
			func(w http.ResponseWriter, r *http.Request) { scanHandler.Scan(w, r) }},
		{"files/error", "/beacon/v1/agent/files/error",
			map[string]any{"commandId": 1, "reason": "读盘失败"},
			func(w http.ResponseWriter, r *http.Request) { scanHandler.ReportError(w, r) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.call(w, newAgentTestRequest(http.MethodPost, tc.target, tc.body, "", ""))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("缺身份的回传必须 401，实际 %d（body=%s）", w.Code, w.Body.String())
			}
		})
	}
}

// TestIngestPassesIdentityGateWhenAuthenticated 是回传类的正向锁：身份校验通过后不再被 401 拦。
func TestIngestPassesIdentityGateWhenAuthenticated(t *testing.T) {
	db, cmdSvc := newAgentIdentityCommandSvc(t)
	cmd := seedAgentPendingCommand(t, db, "prod", "lobby-1")
	h := NewCommandHandler(cmdSvc, nil)
	stub := &agentReportAuthStub{identity: testAgentIdentity()}
	h.SetReportAuthenticator(stub)

	w := httptest.NewRecorder()
	h.Ingest(w, newAgentTestRequest(http.MethodPost, "/beacon/v1/agent/files/ingest",
		map[string]any{"commandId": cmd.ID, "files": []map[string]string{{"path": "a.yml", "content": "a: 1"}}},
		"identity-1", "boot-1"))
	// 已越过身份闸：校验器被调用且响应不是未授权。
	// 业务结果（命令态不符 → COMMAND_NOT_FOUND）由命令服务判定，不属本用例断言范围：
	// commandId 归属比对需 service 层接受身份参数后才能真正闭环（见 issue #146 输出说明）。
	if stub.calls != 1 {
		t.Fatalf("身份校验器应被调用一次，实际 %d", stub.calls)
	}
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("身份一致的请求不得被判未授权，实际 %d（body=%s）", w.Code, w.Body.String())
	}
}

// —— GET /beacon/v1/agent/stream（推送流订阅）——

func TestStreamBindsSubscriberIdentity(t *testing.T) {
	svc, reg := newAgentIdentityTestSvc(t)
	registerTestInstance(t, reg, "prod", "lobby-1")

	h := NewStreamHandler(svc, nil)
	h.SetReportAuthenticator(&agentReportAuthStub{identity: testAgentIdentity()})

	// 冒充他服订阅 → 404（不得旁路别人的变更流）。
	w := httptest.NewRecorder()
	h.Stream(w, newAgentTestRequest(http.MethodGet, "/beacon/v1/agent/stream?namespace=prod&serverId=lobby-9", nil, "identity-1", "boot-1"))
	assertNotLeakedNotFound(t, w)

	// 缺身份 → 401。
	stub := &agentReportAuthStub{err: apperr.ErrUnauthorized}
	h.SetReportAuthenticator(stub)
	w = httptest.NewRecorder()
	h.Stream(w, newAgentTestRequest(http.MethodGet, "/beacon/v1/agent/stream?namespace=prod&serverId=lobby-1", nil, "", ""))
	assertUnauthorized(t, w, stub)
}
