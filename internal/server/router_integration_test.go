//go:build integration

package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/auth"
	"github.com/wcpe/Beacon/internal/handler"
	"github.com/wcpe/Beacon/internal/metrics"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime"
	"github.com/wcpe/Beacon/internal/runtime/alert"
	"github.com/wcpe/Beacon/internal/runtime/longpoll"
	"github.com/wcpe/Beacon/internal/server"
	"github.com/wcpe/Beacon/internal/service"
	"github.com/wcpe/Beacon/internal/testsupport"
)

// 集成测试用固定鉴权凭据（仅测试，非生产值）。
const (
	testAuthUser   = "admin"
	testAuthPass   = "test-pass"
	testAuthSecret = "test-secret"
)

// adminToken 缓存登录后获得的管理台令牌，供 doJSON 自动携带（admin 端已挂鉴权中间件）。
var adminToken string

// testAlertInbox 暴露当前测试服的站内信通道，供告警端点测试直接投递一条告警再经 HTTP 读回。
var testAlertInbox *alert.InboxAlerter

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
	defaultEntryRepo := repository.NewZoneDefaultEntryRepository(db)
	configRepo := repository.NewConfigItemRepository(db, noEncryptCipher())
	fileRepo := repository.NewFileObjectRepository(db)
	registry := runtime.NewRegistry()
	hub := longpoll.NewHub()
	fileHub := longpoll.NewHub()
	topologyHub := longpoll.NewHub()
	nsHandler := handler.NewNamespaceHandler(service.NewNamespaceService(db, repository.NewNamespaceRepository(db), assignRepo, configRepo, fileRepo, repository.NewFileOverrideSetRepository(db), registry, auditRepo))
	cfgSvc := service.NewConfigService(db, configRepo, repository.NewConfigRevisionRepository(db, noEncryptCipher()), auditRepo)
	fileSvc := service.NewFileService(db, fileRepo, repository.NewFileRevisionRepository(db), auditRepo)
	instSvc := service.NewInstanceService(db, registry, assignRepo, repository.NewServerOfflineRepository(db), auditRepo, 10*time.Second, 30*time.Second)
	zoneSvc := service.NewZoneService(db, assignRepo, defaultEntryRepo, auditRepo, registry)
	instSvc.SetDefaultEntryResolver(zoneSvc.DefaultEntryServerIDs)
	grayRepo := repository.NewConfigGrayRepository(db, noEncryptCipher())
	effSvc := service.NewEffectiveService(configRepo, assignRepo, grayRepo, hub)
	graySvc := service.NewConfigGrayService(db, cfgSvc, configRepo, grayRepo, auditRepo)
	fileEffSvc := service.NewFileEffectiveService(fileRepo, assignRepo, fileHub)
	overrideSetRepo := repository.NewFileOverrideSetRepository(db)
	ovrEffSvc := service.NewOverrideEffectiveService(overrideSetRepo, fileRepo, assignRepo, fileHub)
	ovrSetSvc := service.NewOverrideSetService(db, overrideSetRepo, repository.NewFileOverrideSetRevisionRepository(db), fileRepo, auditRepo)
	schedSvc := service.NewSchedulingService(db, repository.NewServerDrainRepository(db), auditRepo, registry)
	apiKeySvc := service.NewAPIKeyService(db, repository.NewAPIKeyRepository(db), auditRepo)
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
	// 反向抓取命令通道（FR-39）：命令仓库 + 服务（复用 fileSvc.Import 落组/实例覆盖）+ 处理器（校验目标在线）。
	commandRepo := repository.NewAgentCommandRepository(db)
	commandService := service.NewAgentCommandService(db, commandRepo, fileSvc, auditRepo)
	commandService.SetNotifier(notifier)
	// 按需拓印 diff 取期望合并值复用 FR-45 有效文件树解析（FR-46）。
	commandService.SetFileEffectiveService(fileEffSvc)
	// 反向抓取受管任务（FR-58）：任务仓库 + 服务（建任务 + 互斥、scan/submit 编排、ingest 复用 Import）+ 处理器。
	reverseFetchTaskSvc := service.NewReverseFetchTaskService(db, repository.NewReverseFetchTaskRepository(db), commandRepo, fileSvc, auditRepo, settingsSvc)
	reverseFetchTaskSvc.SetNotifier(notifier)
	commandService.SetSubmitIngestReceiver(reverseFetchTaskSvc)
	// 反向抓取持久忽略规则（FR-59）：规则服务供任务详情标 ignoredByRule + CRUD 处理器。
	reverseFetchRuleSvc := service.NewReverseFetchIgnoreRuleService(db, repository.NewReverseFetchIgnoreRuleRepository(db), auditRepo)
	reverseFetchTaskHandler := handler.NewReverseFetchTaskHandler(reverseFetchTaskSvc, instSvc, reverseFetchRuleSvc)
	reverseFetchRuleHandler := handler.NewReverseFetchIgnoreRuleHandler(reverseFetchRuleSvc)
	authn, err := auth.New(testAuthUser, testAuthPass, testAuthSecret, time.Hour)
	if err != nil {
		t.Fatalf("构造测试认证器失败: %v", err)
	}
	router := server.NewRouter(server.Handlers{
		Namespace:        nsHandler,
		Config:           handler.NewConfigHandler(cfgSvc, effSvc, graySvc),
		File:             handler.NewFileHandler(fileSvc, fileEffSvc, ovrEffSvc, instSvc, settingsSvc),
		OverrideSet:      handler.NewOverrideSetHandler(ovrSetSvc),
		Agent:            handler.NewAgentHandler(instSvc, effSvc, settingsSvc),
		Stream:           handler.NewStreamHandler(instSvc, streamSvc),
		Instance:         handler.NewInstanceHandler(instSvc),
		Zone:             handler.NewZoneHandler(zoneSvc),
		Scheduling:       handler.NewSchedulingHandler(schedSvc),
		Audit:            handler.NewAuditHandler(service.NewAuditService(auditRepo)),
		Alert:            handler.NewAlertHandler(testAlertInbox),
		Metric:           handler.NewMetricHandler(service.NewMetricService(registry, repository.NewMetricSampleRepository(db))),
		Auth:             handler.NewAuthHandler(authn, service.NewAuthAuditService(auditRepo)),
		APIKey:           handler.NewAPIKeyHandler(apiKeySvc),
		Command:          handler.NewCommandHandler(commandService, instSvc),
		ReverseFetchTask: reverseFetchTaskHandler,
		ReverseFetchRule: reverseFetchRuleHandler,
		Settings:         handler.NewSettingsHandler(settingsSvc),
		Metrics:          metricsSet.Handler(),
		Web:              http.HandlerFunc(http.NotFound),
	}, agentToken, authn, apiKeySvc, auditRepo)
	ts := httptest.NewServer(router)
	adminToken = loginForToken(t, ts.URL)
	return ts
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

// TestAuditClientIPRecorded 复现并守护缺陷：经 HTTP 的审计操作必须把来源 IP 写入 audit_log.client_ip。
// 此前 config / zone / instance 审计均未从请求提取来源 IP，client_ip 恒空、前端"来源 IP"列恒为 -。
func TestAuditClientIPRecorded(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	const wantIP = "203.0.113.7"

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
	if code := doWithIP(http.MethodPut, ts.URL+"/admin/v1/zones/assignments", map[string]any{
		"namespace": "prod", "serverId": "ip-s1", "group": "area1", "zone": "zoneA", "operator": "bob",
	}); code != http.StatusOK {
		t.Fatalf("zone 指派应 200，实际 %d", code)
	}
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
