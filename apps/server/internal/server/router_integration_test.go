//go:build integration

package server_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/handler"
	"github.com/wcpe/Beacon/apps/server/internal/metrics"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/alert"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/longpoll"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/metricwindow"
	"github.com/wcpe/Beacon/apps/server/internal/secret"
	"github.com/wcpe/Beacon/apps/server/internal/server"
	"github.com/wcpe/Beacon/apps/server/internal/service"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// 集成测试用固定鉴权凭据（仅测试，非生产值）。
const (
	testAuthUser   = "admin"
	testAuthPass   = "test-pass"
	testAuthSecret = "test-secret"
	testAgentToken = "integration-agent-token"
)

// adminToken 缓存登录后获得的管理台令牌，供 doJSON 自动携带（admin 端已挂鉴权中间件）。
var adminToken string

// testAlertInbox 暴露当前测试服的站内信通道，供告警端点测试直接投递一条告警再经 HTTP 读回。
var testAlertInbox *alert.InboxAlerter

// testHealthViews 暴露当前测试服的健康视图存储（FR-147）：供指标上报测试预置视图后验证 self 回填。
var testHealthViews *healthview.Store

// integrationTestServer 保存测试路由及其审批执行器，供危险操作走完整审批链路。
type integrationTestServer struct {
	*httptest.Server
	approval *service.ApprovalService
}

// newTestServer 装配真实路由与 DB-backed 服务（不启用 agent token）；未设 BEACON_TEST_DSN 则跳过。
func newTestServer(t *testing.T) *integrationTestServer {
	return newTestServerWithToken(t, testAgentToken)
}

// newTestServerWithToken 同上，但启用指定的 agent token。
func newTestServerWithToken(t *testing.T, agentToken string) *integrationTestServer {
	return newTestServerWithOptions(t, agentToken, false)
}

// newTestServerWithOptions 装配测试路由，并可显式开启机器注册通道（FR-222）。
func newTestServerWithOptions(t *testing.T, agentToken string, allowMachineRegister bool) *integrationTestServer {
	t.Helper()
	db := testsupport.OpenTestDB(t, "server")
	for _, table := range []string{
		"approval_credential_secret", "approval_execution_receipt", "sensitive_access_grant",
		"config_pending_change", "file_pending_change", "mcp_oauth_client_change", "approval_request",
	} {
		if err := db.Exec("DELETE FROM " + table).Error; err != nil {
			t.Fatalf("清理审批测试表 %s 失败: %v", table, err)
		}
	}
	cipher, err := secret.LoadOrCreateCipher(filepath.Join(t.TempDir(), "secrets", "integration.key"))
	if err != nil {
		t.Fatalf("装配集成测试密钥失败: %v", err)
	}
	auditRepo := repository.NewAuditLogRepository(db)
	assignRepo := repository.NewZoneAssignmentRepository(db)
	configRepo := repository.NewConfigItemRepository(db, cipher)
	fileRepo := repository.NewFileObjectRepository(db)
	registry := runtime.NewRegistry()
	hub := longpoll.NewHub()
	fileHub := longpoll.NewHub()
	topologyHub := longpoll.NewHub()
	nsHandler := handler.NewNamespaceHandler(service.NewNamespaceService(db, repository.NewNamespaceRepository(db), assignRepo, configRepo, fileRepo, repository.NewFileOverrideSetRepository(db), registry, auditRepo))
	revRepo := repository.NewConfigRevisionRepository(db, cipher)
	cfgSvc := service.NewConfigService(db, configRepo, revRepo, auditRepo)
	cfgSvc.SetPendingChangeCipher(cipher)
	fileSvc := service.NewFileService(db, fileRepo, repository.NewFileRevisionRepository(db), auditRepo)
	fileSvc.SetPendingChangeCipher(cipher)
	instSvc := service.NewInstanceService(db, registry, assignRepo, repository.NewServerOfflineRepository(db), auditRepo, 10*time.Second, 30*time.Second)
	// 机器注册通道（FR-222）：默认关闭；显式开启时受信内部调用方（共享 token）的注册直落 active。
	instSvc.SetMachineRegisterAllowed(allowMachineRegister)
	zoneSvc := service.NewZoneService(db, assignRepo, auditRepo, registry)
	grayRepo := repository.NewConfigGrayRepository(db, cipher)
	effSvc := service.NewEffectiveService(configRepo, assignRepo, grayRepo, revRepo, hub)
	graySvc := service.NewConfigGrayService(db, cfgSvc, configRepo, grayRepo, auditRepo)
	cfgSvc.SetGrayService(graySvc)
	fileEffSvc := service.NewFileEffectiveService(fileRepo, assignRepo, fileHub)
	overrideSetRepo := repository.NewFileOverrideSetRepository(db)
	ovrEffSvc := service.NewOverrideEffectiveService(overrideSetRepo, fileRepo, assignRepo, fileHub)
	ovrSetSvc := service.NewOverrideSetService(db, overrideSetRepo, repository.NewFileOverrideSetRevisionRepository(db), fileRepo, auditRepo)
	ovrSetSvc.SetPendingChangeCipher(cipher)
	schedSvc := service.NewSchedulingService(db, repository.NewServerDrainRepository(db), auditRepo, registry)
	apiKeySvc := service.NewAPIKeyService(db, repository.NewAPIKeyRepository(db), auditRepo)
	apiKeySvc.SetCredentialCipher(cipher)
	testAlertInbox = alert.NewInboxAlerter(16)
	commandHub := longpoll.NewHub()
	notifier := service.NewChangeNotifier(hub, fileHub, topologyHub, commandHub, registry, assignRepo)
	metricsSet := metrics.New(registry)
	notifier.SetMetrics(metricsSet)
	cfgSvc.SetNotifier(notifier)
	cfgSvc.SetMetrics(metricsSet)
	fileSvc.SetNotifier(notifier)
	zoneSvc.SetNotifier(notifier)
	instSvc.SetNotifier(notifier)
	ovrSetSvc.SetNotifier(notifier)
	// 运维设置 store（FR-61）：长轮询 max-hold 等热改项的真源；测试用默认值。
	settingsSvc, err := service.NewSettingsService(db, repository.NewSettingRepository(db), auditRepo)
	if err != nil {
		t.Fatalf("构造设置 service 失败: %v", err)
	}
	// SSE 推送流（FR-24 + FR-29 拓扑 watch）：保活间隔给大（测试不依赖保活），复用同源唤醒集合。
	streamSvc := service.NewStreamService(effSvc, fileEffSvc, ovrEffSvc, registry, hub, fileHub, topologyHub, commandHub, settingsSvc)
	v2Svc := service.NewV2ControlPlaneService(db)
	v2Svc.SetLegacyZoneService(zoneSvc)
	v2Svc.SetLegacySchedulingService(schedSvc)
	approvalRegistry := authz.NewApprovalRegistry()
	approvalSvc := service.NewApprovalService(db, repository.NewApprovalRequestRepository(db), auditRepo, approvalRegistry)
	sensitiveAccessGrants := service.NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	approvalSvc.SetSensitiveAccessGrantStore(repository.NewSensitiveAccessGrantRepository(db))
	v2Svc.SetApprovalService(approvalSvc)
	cfgSvc.SetApprovalService(approvalSvc)
	cfgSvc.SetSensitiveAccessGrants(sensitiveAccessGrants)
	fileSvc.SetApprovalService(approvalSvc)
	ovrSetSvc.SetApprovalService(approvalSvc)
	apiKeySvc.SetApprovalService(approvalSvc)
	settingsSvc.SetApprovalService(approvalSvc)
	service.RegisterV2ControlPlaneApprovalAdapters(approvalRegistry, v2Svc)
	service.RegisterConfigApprovalAdapters(approvalRegistry, cfgSvc)
	service.RegisterSensitiveConfigApprovalAdapter(approvalRegistry, cfgSvc, sensitiveAccessGrants)
	service.RegisterFileOverrideApprovalAdapters(approvalRegistry, fileSvc, ovrSetSvc)
	service.RegisterAPIKeyApprovalAdapters(approvalRegistry, apiKeySvc)
	// 反向抓取命令通道（FR-39）：命令仓库 + 服务（复用 fileSvc.Import 落组/实例覆盖）+ 处理器（校验目标在线）。
	commandRepo := repository.NewAgentCommandRepository(db)
	commandService := service.NewAgentCommandService(db, commandRepo, fileSvc, auditRepo)
	commandService.SetNotifier(notifier)
	commandService.SetApprovalService(approvalSvc)
	commandService.SetSensitiveAccessGrants(sensitiveAccessGrants)
	service.RegisterAgentCommandApprovalAdapters(approvalRegistry, commandService, sensitiveAccessGrants)
	v2Svc.SetDirectoryResyncCommandPort(commandRepo, notifier)
	// 按需拓印 diff 取期望合并值复用 FR-45 有效文件树解析（FR-46）。
	commandService.SetFileEffectiveService(fileEffSvc)
	commandHandler := handler.NewCommandHandler(commandService, instSvc)
	commandHandler.SetReportAuthenticator(v2Svc)
	browseHandler := handler.NewBrowseHandler(commandService, instSvc)
	browseHandler.SetReportAuthenticator(v2Svc)
	commandObserveHandler := handler.NewCommandObserveHandler(service.NewCommandObserveService(commandRepo))
	// P8 文件资产索引（FR-163）：清单上报 + 搜索 / 概要 / 比对 / 重扫；复用同一 commandRepo 下发 asset-rescan。
	assetSvc := service.NewAssetService(db, repository.NewFileAssetRepository(db), repository.NewFileAssetScanRepository(db), commandRepo, auditRepo)
	assetSvc.SetNotifier(notifier)
	fileSyncSvc := service.NewFileSyncService(db, repository.NewFileSyncRepository(db), instSvc, auditRepo, service.NewFileSyncEventHub())
	// 反向抓取受管任务（FR-58）：任务仓库 + 服务（建任务 + 互斥、scan/submit 编排、ingest 复用 Import）+ 处理器。
	reverseFetchTaskSvc := service.NewReverseFetchTaskService(db, repository.NewReverseFetchTaskRepository(db), commandRepo, fileSvc, auditRepo, settingsSvc)
	reverseFetchTaskSvc.SetNotifier(notifier)
	reverseFetchTaskSvc.SetApprovalService(approvalSvc)
	reverseFetchTaskSvc.SetSensitiveAccessGrants(sensitiveAccessGrants)
	service.RegisterReverseFetchTaskApprovalAdapters(approvalRegistry, reverseFetchTaskSvc)
	commandService.SetSubmitIngestReceiver(reverseFetchTaskSvc)
	// 反向抓取持久忽略规则（FR-59）：规则服务供任务详情标 ignoredByRule + CRUD 处理器。
	reverseFetchRuleSvc := service.NewReverseFetchIgnoreRuleService(db, repository.NewReverseFetchIgnoreRuleRepository(db), auditRepo)
	reverseFetchTaskHandler := handler.NewReverseFetchTaskHandler(reverseFetchTaskSvc, instSvc, reverseFetchRuleSvc)
	reverseFetchRuleHandler := handler.NewReverseFetchIgnoreRuleHandler(reverseFetchRuleSvc)
	// 取 agent 日志（FR-88）：复用同一命令仓库，编排 tail-logs 命令-回传周期 + 处理器。
	agentLogSvc := service.NewAgentLogService(db, commandRepo, auditRepo)
	agentLogSvc.SetNotifier(notifier)
	agentLogSvc.SetApprovalService(approvalSvc)
	agentLogSvc.SetSensitiveAccessGrants(sensitiveAccessGrants)
	service.RegisterAgentLogApprovalAdapter(approvalRegistry, agentLogSvc, sensitiveAccessGrants)
	agentLogHandler := handler.NewAgentLogHandler(agentLogSvc, instSvc)
	authn, err := auth.New(testAuthUser, testAuthPass, testAuthSecret, time.Hour)
	if err != nil {
		t.Fatalf("构造测试认证器失败: %v", err)
	}
	v2Handler := handler.NewV2ControlPlaneHandler(v2Svc)
	// env 展示维度（FR-178）：env 增删改 + 整体替换 env→namespace 映射，与 main.go 装配一致。
	envHandler := handler.NewEnvHandler(service.NewEnvService(db, repository.NewEnvRepository(db), repository.NewNamespaceRepository(db), auditRepo))
	// 发现/实例视图默认入口标志：真源为 v2 server.is_default_entry（ADR-0067），与 main.go 装配一致。
	instSvc.SetDefaultEntryResolver(v2Svc.DefaultEntryServerIDs)
	// P4 指标上报（FR-144）：60s 窗口 + 异步写入通道 + 接收服务 + 处理器。测试不启写入 worker——
	// 鉴权 / 窗口去重 / 202 语义即可验，落库经 service 集成用例（启 worker）覆盖。
	metricWriter := service.NewAsyncDailyWriter()
	service.RegisterFlusher(metricWriter, service.RouteKindMetricSample, repository.NewMetricSampleV2Repository(db).FlushDaily)
	metricIngestSvc := service.NewMetricIngestService(metricwindow.New(metricwindow.DefaultCapacity),
		service.MetricSampleEnqueuer{Writer: metricWriter})
	// 健康视图存储（FR-147）：测试不启计算轮，用例按需直接预置视图验证 self 回填。
	testHealthViews = healthview.NewStore()
	metricIngestSvc.SetHealthViews(testHealthViews)
	v2MetricsHandler := handler.NewV2MetricsHandler(metricIngestSvc)
	router := server.NewRouter(server.Handlers{
		Namespace:        nsHandler,
		Env:              envHandler,
		V2:               v2Handler,
		V2Metrics:        v2MetricsHandler,
		V2Assets:         handler.NewV2AssetsHandler(assetSvc),
		Config:           handler.NewConfigHandler(cfgSvc, effSvc, graySvc, service.NewImpactService(registry, assignRepo, db)),
		File:             handler.NewFileHandler(fileSvc, fileEffSvc, ovrEffSvc, instSvc, settingsSvc),
		OverrideSet:      handler.NewOverrideSetHandler(ovrSetSvc),
		Agent:            handler.NewAgentHandler(instSvc, effSvc, settingsSvc),
		Stream:           handler.NewStreamHandler(instSvc, streamSvc),
		Instance:         handler.NewInstanceHandler(instSvc, settingsSvc, effSvc),
		Zone:             handler.NewZoneHandler(zoneSvc, v2Svc),
		Scheduling:       handler.NewSchedulingHandler(schedSvc, v2Svc),
		Audit:            handler.NewAuditHandler(service.NewAuditService(auditRepo), settingsSvc),
		Alert:            handler.NewAlertHandler(testAlertInbox),
		AlertEvent:       handler.NewAlertEventHandler(service.NewAlertEventService(db, repository.NewAlertEventRepository(db), auditRepo)),
		Metric:           handler.NewMetricHandler(service.NewMetricService(registry, repository.NewMetricSampleRepository(db))),
		Auth:             handler.NewAuthHandler(authn, service.NewAuthAuditService(auditRepo)),
		APIKey:           handler.NewAPIKeyHandler(apiKeySvc),
		Approval:         handler.NewApprovalHandler(approvalSvc, apiKeySvc),
		Command:          commandHandler,
		CommandObserve:   commandObserveHandler,
		Browse:           browseHandler,
		FileSync:         handler.NewFileSyncHandler(fileSyncSvc),
		AgentLog:         agentLogHandler,
		ReverseFetchTask: reverseFetchTaskHandler,
		ReverseFetchRule: reverseFetchRuleHandler,
		Settings:         handler.NewSettingsHandler(settingsSvc),
		Metrics:          metricsSet.Handler(),
		Web:              http.HandlerFunc(http.NotFound),
	}, agentToken, authn, apiKeySvc, auditRepo)
	ts := httptest.NewServer(router)
	adminToken = loginForToken(t, ts.URL)
	return &integrationTestServer{Server: ts, approval: approvalSvc}
}

// approveAndRun 以另一位 human/full 审批人完成申请，再同步驱动一次 worker。
func (s *integrationTestServer) approveAndRun(t *testing.T, requestID string) {
	t.Helper()
	if _, err := s.approval.Approve(requestID, auth.HumanPrincipal("reviewer"), "127.0.0.2"); err != nil {
		t.Fatalf("批准审批申请失败: %v", err)
	}
	processed, err := service.NewApprovalWorker(s.approval).RunOnce(context.Background())
	if err != nil || processed != 1 {
		t.Fatalf("执行审批申请失败: processed=%d err=%v", processed, err)
	}
}

// applyApprovalTicket 以另一位 human/full 审批人执行 HTTP 提审返回的票据。
func applyApprovalTicket(t *testing.T, ts *integrationTestServer, ticket map[string]any) {
	t.Helper()
	requestID, _ := ticket["approvalRequestId"].(string)
	if requestID == "" {
		t.Fatalf("审批申请响应缺 requestId：%v", ticket)
	}
	ts.approveAndRun(t, requestID)
}

// loginForToken 登录测试服务取得管理台令牌。
func loginForToken(t *testing.T, baseURL string) string {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"username": testAuthUser, "password": testAuthPass})
	resp, err := http.Post(baseURL+"/admin/v1/auth/login", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("登录请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登录应 200，实际 %d", resp.StatusCode)
	}
	var parsed map[string]any
	data, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(data, &parsed)
	token, _ := parsed["token"].(string)
	if token == "" {
		t.Fatal("登录响应缺 token")
	}
	return token
}

// doJSON 发起一次 JSON 请求并返回状态码与解析后的响应体；admin 端自动携带登录令牌。
func doJSON(t *testing.T, method, url string, body any) (int, map[string]any) {
	return doJSONWithHeaders(t, method, url, body, nil)
}

// doJSONWithHeaders 与 doJSON 相同，但允许审批申请携带幂等键。
func doJSONWithHeaders(t *testing.T, method, url string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, url, reader)
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
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
	return resp.StatusCode, parsed
}

// requestAndApplyApproval 经 HTTP 提交带原因和幂等键的申请，再由另一位 human/full 审批并驱动 worker。
func requestAndApplyApproval(t *testing.T, ts *integrationTestServer, method, path, idempotencyKey string, body any) map[string]any {
	return requestAndApplyApprovalWithHeaders(t, ts, method, path, idempotencyKey, nil, body)
}

// requestAndApplyApprovalWithHeaders 保留来源地址等测试头，验证审批链路仍使用同一审计口径。
func requestAndApplyApprovalWithHeaders(t *testing.T, ts *integrationTestServer, method, path, idempotencyKey string, headers map[string]string, body any) map[string]any {
	t.Helper()
	if headers == nil {
		headers = make(map[string]string, 1)
	}
	headers["Idempotency-Key"] = compactIdempotencyKey(idempotencyKey)
	code, ticket := doJSONWithHeaders(t, method, ts.URL+path, body, headers)
	if code != http.StatusAccepted {
		t.Fatalf("提交审批申请应 202，实际 %d：%v", code, ticket)
	}
	applyApprovalTicket(t, ts, ticket)
	return ticket
}

// sensitiveGrantIDForTest 从原申请人的审批详情读取一次性敏感内容授权。
func sensitiveGrantIDForTest(t *testing.T, ts *integrationTestServer, requestID string) string {
	t.Helper()
	code, detail := doJSON(t, http.MethodGet, ts.URL+"/admin/v2/approval-requests/"+requestID, nil)
	if code != http.StatusOK {
		t.Fatalf("读取敏感内容授权应 200，实际 %d：%v", code, detail)
	}
	grant, _ := detail["sensitiveAccessGrant"].(map[string]any)
	grantID, _ := grant["grantId"].(string)
	if grantID == "" {
		t.Fatalf("审批详情缺敏感内容授权：%v", detail)
	}
	return grantID
}

// compactIdempotencyKey 把测试名派生键收敛到接口允许的固定长度，仍保持语义唯一。
func compactIdempotencyKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "it-" + hex.EncodeToString(sum[:16])
}

// assignZoneForTest 让旧 V1 指派兼容路由也覆盖申请、审批和执行三段链路。
func assignZoneForTest(t *testing.T, ts *integrationTestServer, namespace, serverID, group, zone, note string) {
	t.Helper()
	ensureActiveNamespaceForTest(t, ts, namespace)
	requestAndApplyApproval(t, ts, http.MethodPut, "/admin/v1/zones/assignments", t.Name()+"-assign-"+serverID, map[string]any{
		"namespace": namespace, "serverId": serverID, "group": group, "zone": zone, "note": note,
	})
}

// ensureActiveNamespaceForTest 为依赖生命周期真源的 V1 兼容动作准备 active namespace。
func ensureActiveNamespaceForTest(t *testing.T, ts *integrationTestServer, namespace string) {
	t.Helper()
	code, listed := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/namespaces", nil)
	if code != http.StatusOK {
		t.Fatalf("查询环境应 200，实际 %d：%v", code, listed)
	}
	for _, raw := range asSlice(listed["items"]) {
		item, ok := raw.(map[string]any)
		if ok && item["code"] == namespace {
			return
		}
	}
	code, created := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/namespaces", map[string]any{"code": namespace, "name": namespace})
	if code != http.StatusCreated {
		t.Fatalf("创建活动环境应 201，实际 %d：%v", code, created)
	}
}

// unassignZoneForTest 让旧 V1 取消指派兼容路由也覆盖审批执行链路。
func unassignZoneForTest(t *testing.T, ts *integrationTestServer, namespace, serverID, reason string) {
	t.Helper()
	path := "/admin/v1/zones/assignments?namespace=" + namespace + "&serverId=" + serverID + "&reason=" + reason
	requestAndApplyApproval(t, ts, http.MethodDelete, path, t.Name()+"-unassign-"+serverID, nil)
}

// approveAgentIdentityForTest 以显式 serverId 提交身份确认，避免测试夹具绕过审批边界。
func approveAgentIdentityForTest(t *testing.T, ts *integrationTestServer, identityID, serverID string) {
	t.Helper()
	approveAgentIdentityWithKeyForTest(t, ts, identityID, serverID, t.Name()+"-identity-"+serverID)
}

// approveAgentIdentityWithKeyForTest 允许同一身份在同一用例内因换区重入 pending 后使用新的幂等键再次确认。
func approveAgentIdentityWithKeyForTest(t *testing.T, ts *integrationTestServer, identityID, serverID, idempotencyKey string) {
	t.Helper()
	path := "/admin/v2/agent-identities/" + identityID + "/approve"
	requestAndApplyApproval(t, ts, http.MethodPost, path, idempotencyKey, map[string]any{
		"serverId": serverID, "reason": "集成测试确认身份",
	})
}

// publishConfigForTest 经审批链发布配置，保留生产端原因与幂等键约束。
func publishConfigForTest(t *testing.T, ts *integrationTestServer, id int, content, comment string) {
	t.Helper()
	requestAndApplyApproval(t, ts, http.MethodPut, "/admin/v1/configs/"+itoa(id), t.Name()+"-config-publish-"+itoa(id), map[string]any{
		"content": content, "comment": comment, "reason": "集成测试发布配置",
	})
}

// rollbackConfigForTest 经审批链回滚配置。
func rollbackConfigForTest(t *testing.T, ts *integrationTestServer, id int, version int64, comment string) {
	t.Helper()
	requestAndApplyApproval(t, ts, http.MethodPost, "/admin/v1/configs/"+itoa(id)+"/rollback", t.Name()+"-config-rollback-"+itoa(id), map[string]any{
		"toVersion": version, "comment": comment, "reason": "集成测试回滚配置",
	})
}

// deleteConfigForTest 经审批链软删配置。
func deleteConfigForTest(t *testing.T, ts *integrationTestServer, id int, comment string) {
	t.Helper()
	path := "/admin/v1/configs/" + itoa(id) + "?comment=" + comment + "&reason=集成测试删除配置"
	requestAndApplyApproval(t, ts, http.MethodDelete, path, t.Name()+"-config-delete-"+itoa(id), nil)
}

// publishFileForTest 经审批链发布文件内容。
func publishFileForTest(t *testing.T, ts *integrationTestServer, id int, content, comment string) {
	t.Helper()
	requestAndApplyApproval(t, ts, http.MethodPut, "/admin/v1/files/"+itoa(id), t.Name()+"-file-publish-"+itoa(id), map[string]any{
		"content": content, "comment": comment, "reason": "集成测试发布文件",
	})
}

// rollbackFileForTest 经审批链回滚文件内容。
func rollbackFileForTest(t *testing.T, ts *integrationTestServer, id int, version int64, comment string) {
	t.Helper()
	requestAndApplyApproval(t, ts, http.MethodPost, "/admin/v1/files/"+itoa(id)+"/rollback", t.Name()+"-file-rollback-"+itoa(id), map[string]any{
		"toVersion": version, "comment": comment, "reason": "集成测试回滚文件",
	})
}

// deleteFileForTest 经审批链软删文件。
func deleteFileForTest(t *testing.T, ts *integrationTestServer, id int, comment string) {
	t.Helper()
	path := "/admin/v1/files/" + itoa(id) + "?comment=" + comment + "&reason=集成测试删除文件"
	requestAndApplyApproval(t, ts, http.MethodDelete, path, t.Name()+"-file-delete-"+itoa(id), nil)
}

// createFileForTest 通过审批链创建文件，再从只读列表定位已落库对象。
func createFileForTest(t *testing.T, ts *integrationTestServer, namespace, group, path, scopeLevel, content string) int {
	t.Helper()
	requestAndApplyApproval(t, ts, http.MethodPost, "/admin/v1/files", t.Name()+"-file-create-"+path, map[string]any{
		"namespace": namespace, "group": group, "path": path, "scopeLevel": scopeLevel,
		"content": content, "reason": "集成测试创建文件",
	})
	code, listed := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/files?namespace="+url.QueryEscape(namespace)+"&group="+url.QueryEscape(group)+"&path="+url.QueryEscape(path), nil)
	if code != http.StatusOK {
		t.Fatalf("创建后查询文件应 200，实际 %d：%v", code, listed)
	}
	for _, raw := range asSlice(listed["items"]) {
		item, ok := raw.(map[string]any)
		if !ok || item["path"] != path {
			continue
		}
		id, ok := item["id"].(float64)
		if ok && id > 0 {
			return int(id)
		}
	}
	t.Fatalf("创建后未找到文件 %s：%v", path, listed)
	return 0
}

// applyConfigBatchForTest 经审批链执行配置批量删除或开关。
func applyConfigBatchForTest(t *testing.T, ts *integrationTestServer, action string, ids []int, suffix string) {
	t.Helper()
	requestAndApplyApproval(t, ts, http.MethodPost, "/admin/v1/configs/batch", t.Name()+"-config-batch-"+suffix, map[string]any{
		"action": action, "ids": ids, "reason": "集成测试批量配置变更",
	})
}

// applyFileBatchForTest 经审批链执行文件批量删除或开关。
func applyFileBatchForTest(t *testing.T, ts *integrationTestServer, action string, ids []int, suffix string) {
	t.Helper()
	requestAndApplyApproval(t, ts, http.MethodPost, "/admin/v1/files/batch", t.Name()+"-file-batch-"+suffix, map[string]any{
		"action": action, "ids": ids, "reason": "集成测试批量文件变更",
	})
}

// publishOverrideSetForTest 经审批链发布覆盖集。
func publishOverrideSetForTest(t *testing.T, ts *integrationTestServer, id int, targetRoot, reloadCommand, comment string) {
	t.Helper()
	requestAndApplyApproval(t, ts, http.MethodPut, "/admin/v1/override-sets/"+itoa(id), t.Name()+"-override-publish", map[string]any{
		"targetRoot": targetRoot, "reloadCommand": reloadCommand, "comment": comment, "reason": "集成测试发布覆盖集",
	})
}

// rollbackOverrideSetForTest 经审批链回滚覆盖集。
func rollbackOverrideSetForTest(t *testing.T, ts *integrationTestServer, id int, version int64, comment string) {
	t.Helper()
	requestAndApplyApproval(t, ts, http.MethodPost, "/admin/v1/override-sets/"+itoa(id)+"/rollback", t.Name()+"-override-rollback", map[string]any{
		"toVersion": version, "comment": comment, "reason": "集成测试回滚覆盖集",
	})
}

// deleteOverrideSetForTest 经审批链软删覆盖集。
func deleteOverrideSetForTest(t *testing.T, ts *integrationTestServer, id int, comment string) {
	t.Helper()
	path := "/admin/v1/override-sets/" + itoa(id) + "?comment=" + comment + "&reason=集成测试删除覆盖集"
	requestAndApplyApproval(t, ts, http.MethodDelete, path, t.Name()+"-override-delete", nil)
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
	publishConfigForTest(t, ts, id, "k: 2\n", "发布第二版")

	// 历史
	code, revs := doJSON(t, http.MethodGet, itemURL+"/revisions", nil)
	if code != http.StatusOK {
		t.Fatalf("历史应 200，实际 %d", code)
	}
	if items, _ := revs["items"].([]any); len(items) != 2 {
		t.Fatalf("历史应有 2 版，实际 %v", revs["items"])
	}

	// 回滚到 v1
	rollbackConfigForTest(t, ts, id, 1, "回滚第一版")

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

// TestAuditClientIPRecorded 复现并守护缺陷：经 HTTP 的审计操作必须把来源 IP 写入 audit_log.client_ip。
// 此前 config / zone / instance 审计均未从请求提取来源 IP，client_ip 恒空、前端"来源 IP"列恒为 -。
func TestAuditClientIPRecorded(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	const wantIP = "203.0.113.7"
	createV2NamespaceToken(t, ts.URL, "prod")

	// 经 X-Forwarded-For 指定来源 IP 发起一次请求并返回状态码（admin 端携带登录令牌）。
	doWithIP := func(method, url string, body any) int {
		var reader io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
		req, _ := http.NewRequest(method, url, reader)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", wantIP)
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
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	// ① config.create（admin 侧）
	if code := doWithIP(http.MethodPost, ts.URL+"/admin/v1/configs", map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "dataId": "ip-audit.yml",
		"scopeLevel": "global", "format": "yaml", "content": "k: 1\n", "operator": "alice",
	}); code != http.StatusCreated {
		t.Fatalf("建配置应 201，实际 %d", code)
	}
	// ② zone.assign（admin 侧）
	registerOnline(t, ts.URL, "prod", "ip-s1", "area1")
	requestAndApplyApprovalWithHeaders(t, ts, http.MethodPut, "/admin/v1/zones/assignments", t.Name()+"-zone-ip", map[string]string{"X-Forwarded-For": wantIP}, map[string]any{
		"namespace": "prod", "serverId": "ip-s1", "group": "area1", "zone": "zoneA", "note": "来源地址审计",
	})
	// ③ instance.register（agent 侧；来源 IP = agent 连接地址）
	if code := doWithIP(http.MethodPost, ts.URL+"/beacon/v1/agent/register", map[string]any{
		"namespace": "prod", "serverId": "ip-s2", "role": "bukkit", "address": "10.0.0.9:25565",
	}); code != http.StatusOK {
		t.Fatalf("注册应 200，实际 %d", code)
	}

	// 三类审计的 clientIp 都应被写为来源 IP。
	for _, action := range []string{"config.create", "zone.assign", "instance.register"} {
		code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?namespace=prod&action="+action, nil)
		if code != http.StatusOK {
			t.Fatalf("查 %s 审计应 200，实际 %d", action, code)
		}
		items, _ := audits["items"].([]any)
		if len(items) == 0 {
			t.Fatalf("应有 %s 审计，实际无", action)
		}
		first, _ := items[0].(map[string]any)
		if got, _ := first["clientIp"].(string); got != wantIP {
			t.Fatalf("%s 审计 clientIp 应为 %q，实际 %q（来源 IP 未写入）", action, wantIP, got)
		}
	}
}

// TestMetricsEndpoint 验证 /metrics 免鉴权可抓取，且配置发布后发布/推送计数前进（FR-30）。
func TestMetricsEndpoint(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 发布一次配置，触发发布计数与推送计数
	code, _ := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/configs", map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "dataId": "metrics-probe.yml",
		"scopeLevel": "global", "format": "yaml", "content": "k: 1\n", "operator": "alice",
	})
	if code != http.StatusCreated {
		t.Fatalf("建配置应 201，实际 %d", code)
	}

	// /metrics 不带令牌也应 200（内网信任面）
	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("抓取 /metrics 失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics 应 200，实际 %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)
	for _, name := range []string{"beacon_config_publish_total", "beacon_push_notify_total", "beacon_instances_status"} {
		if !bytes.Contains(raw, []byte(name)) {
			t.Fatalf("/metrics 应含指标 %s，实际：\n%s", name, body)
		}
	}
}

// TestAuditOperatorFilter 验证审计查询新增的 operator 过滤维度（FR-30）经 HTTP 生效。
func TestAuditOperatorFilter(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// alice 建一条、bob 发布一条 → 各产生一条审计（操作者以登录身份为准，见 ADR-0009）。
	// 鉴权将写操作 operator 统一为登录用户 admin，故此处按 admin 验证 operator 过滤生效与隔离。
	code, _ := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/configs", map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "dataId": "op-filter.yml",
		"scopeLevel": "global", "format": "yaml", "content": "k: 1\n", "operator": "alice",
	})
	if code != http.StatusCreated {
		t.Fatalf("建配置应 201，实际 %d", code)
	}

	// operator=admin（登录身份）应能查到刚才的审计
	code, hit := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?namespace=prod&operator="+testAuthUser, nil)
	if code != http.StatusOK {
		t.Fatalf("查 operator 审计应 200，实际 %d", code)
	}
	if total, _ := hit["total"].(float64); total < 1 {
		t.Fatalf("operator=%s 审计应 >=1，实际 %v", testAuthUser, hit["total"])
	}

	// operator=不存在者 应查不到
	code, miss := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?namespace=prod&operator=nobody-x", nil)
	if code != http.StatusOK {
		t.Fatalf("查不存在 operator 应 200，实际 %d", code)
	}
	if total, _ := miss["total"].(float64); total != 0 {
		t.Fatalf("operator=nobody-x 审计应 0，实际 %v", miss["total"])
	}
}

// TestAuditMiddlewareCoveredNoDoubleLog 经真实路由 + DB 审计仓库 end-to-end 守护 FR-72：
// 已被专项审计覆盖的写端点（config.create）只落「一条」专项审计、兜底中间件不重复补记。
// 若有人误把该路由移出 coveredWriteRoutes，兜底中间件会再补记一条同 action 的空 detail 审计 → 本测试失败。
func TestAuditMiddlewareCoveredNoDoubleLog(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// config.create 在覆盖集合内：建一条配置，触发 service 层专项审计。
	code, _ := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/configs", map[string]any{
		"namespace": "prod", "group": "__GLOBAL__", "dataId": "audit-nodup.yml",
		"scopeLevel": "global", "format": "yaml", "content": "k: 1\n", "operator": "alice",
	})
	if code != http.StatusCreated {
		t.Fatalf("建配置应 201，实际 %d", code)
	}

	// 查 config.create 审计：应恰好 1 条（专项审计），兜底中间件未对已覆盖端点重复补记。
	code, audits := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?namespace=prod&action=config.create", nil)
	if code != http.StatusOK {
		t.Fatalf("查审计应 200，实际 %d", code)
	}
	items, _ := audits["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("config.create 应恰好 1 条审计（专项、无兜底双记），实际 %d 条", len(items))
	}
}

// TestAuditMiddlewareV2CoveredNoDoubleLog 经真实路由 + DB 审计仓库 end-to-end 守护 /admin/v2 兜底双记缺陷修复：
// v2 自审写端点（namespace 创建）只落「一条」专项审计（namespace.create）；此前兜底中间件硬编码只剥
// /admin/v1/ 前缀，会对 v2 路由再补记一条 action=".create"、targetType="" 的垃圾行 → 双记。
func TestAuditMiddlewareV2CoveredNoDoubleLog(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// 建一个 v2 namespace：service 在事务内自记 namespace.create 专项审计。
	createNamespaceV2(t, ts.URL, "prod")

	// 专项审计恰好 1 条。
	code, special := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?action=namespace.create", nil)
	if code != http.StatusOK {
		t.Fatalf("查 namespace.create 审计应 200，实际 %d", code)
	}
	if total, _ := special["total"].(float64); total != 1 {
		t.Fatalf("namespace.create 应恰好 1 条专项审计，实际 %v", special["total"])
	}

	// 无前缀未剥净的垃圾兜底行（action=".create"、targetType 空）。
	code, garbage := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/audits?action=.create", nil)
	if code != http.StatusOK {
		t.Fatalf("查 .create 垃圾审计应 200，实际 %d", code)
	}
	if total, _ := garbage["total"].(float64); total != 0 {
		t.Fatalf("v2 写端点不应产生 action=\".create\" 兜底垃圾行，实际 %v 条（双记）", garbage["total"])
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
