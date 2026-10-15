//go:build e2e && loadtest

// v2 agent 数据面上报 + 写路径压测场景 / 连接入队背压探测（补足只读 harness 的盲区）。
//
// 定位：既有 scenario.go 只覆盖 6 个只读端点（内存读 / DB 读 / agent 心跳 / 指标抓取），
// v2 agent 数据面上报（FR-144 指标 / FR-146 调度）与写路径完全没测。本文件补齐三块：
//   - bootstrapV2Identity：建命名空间 → 批量注册 v2 proxy 身份 → 逐个审批成 active。
//   - buildV2Scenarios：指标上报（202）/ 调度候选（200）/ 在线决策（200）。
//   - buildWriteScenarios：agent 挂载（幂等刷新）/ admin 标签写（每请求落审计）。
//   - probeIngestQueueHeadroom：反复打连接批量上报，观测 429（队列满）与 503（等 DB 连接超时）。
//
// 两条铁律：
//  1. 不新增、不改动任何生产代码（apps/server/internal/**）与 loadtest/ 下既有文件；
//     只复用既有路由 / handler / 服务行为，场景正确性以真实状态码在冒烟里逐一验证。
//  2. 写场景逐条落 audit_log：agent 挂载与 admin 标签写都是「直执 + 强审计」端点，每发一请求
//     即落一条审计。故写场景只在显式开启（LOADTEST_ENABLE_WRITE_SCENARIOS=1）时纳入、权重远低于
//     读场景——测的是「审计写入 + 行锁争用」的真实写路径成本，不是接口吞吐；默认关闭，不污染 audit_log。
package loadtest_e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

// authV2AgentToken 是 v2 agent 数据面上报的鉴权方式：X-Beacon-Token（命名空间一次性 token）
// + X-Beacon-Identity（agent 身份 UUID v4）+ X-Beacon-Boot（bootId）三头，由 agentV2ReportMiddleware 校验。
//
// 在本文件而非 scenario.go 定义新 authMode 值的原因：并行开发期禁止改动 scenario.go 等既有文件，
// authMode 枚举与 name() 归主代理统一维护。name() 见到本值走 default 分支显示「未知」，报告可读性
// 由主代理在 scenario.go 的 name() 里补一个 case（authV2AgentToken → "v2 身份令牌"）后补齐。
const authV2AgentToken authMode = authAgentToken + 1

// v2 / 写路径场景在报告里的分组名。
const (
	groupV2Report   = "v2 上报"
	groupV2Schedule = "v2 调度"
	groupWrite      = "写路径"
)

// 权重：v2 上报是 5s / 10s 一批的准周期流量，低于心跳高频；写路径远低于读（审计成本）。
const (
	weightV2Report     = 2
	weightV2Candidates = 2
	weightV2Decide     = 1
	weightWrite        = 1
)

// v2SetupTimeout / v2SnippetLimit 是本文件自带的单请求超时与响应回带片段上限。
// 不在 loadtest_test.go 里取同名 v2SetupTimeout / v2SnippetLimit：那是 _test.go 内的符号，
// 非测试构建（go build ./...）看不见，会让本非测试文件 undefined；单独定义以保持自洽。
const (
	v2SetupTimeout = 30 * time.Second
	v2SnippetLimit = 512
)

// v2Bootstrap 持有 v2 agent 数据面上报场景所需的身份凭据。
type v2Bootstrap struct {
	Namespace   string // namespace code
	AccessToken string // 作 X-Beacon-Token
	IdentityID  string // 作 X-Beacon-Identity（UUID v4）
	BootID      string
	ServerIDs   []string
}

// bootstrapV2Identity 建 namespace → 注册 servers 个 v2 proxy 身份 → 逐个审批成 active → 返回凭据。
// 任一环节失败即 t.Fatalf（不静默跳过：否则压出来的「成功」是假象）。
func bootstrapV2Identity(t *testing.T, baseURL, adminToken, namespace string, servers int, guard harness.ProcessGuard) *v2Bootstrap {
	t.Helper()
	if servers < 1 {
		servers = 1
	}
	accessToken, nsCode := createV2Namespace(t, baseURL, adminToken, namespace, guard)
	boot := &v2Bootstrap{
		Namespace:   nsCode,
		AccessToken: accessToken,
		ServerIDs:   make([]string, 0, servers),
	}
	for i := range servers {
		serverID := fmt.Sprintf("loadtest-v2-proxy-%d", i)
		identityID := uuid.NewString() // v4，满足服务端 validUUID（UUIDv4 正则）
		bootID := uuid.NewString()
		listenPort := 25577 + i // 每个身份一个探测端口，避免跨身份地址歧义
		registerV2Proxy(t, baseURL, accessToken, identityID, serverID, bootID, listenPort, guard)
		activateV2Identity(t, baseURL, adminToken, identityID, serverID, guard)
		if i == 0 {
			boot.IdentityID = identityID
			boot.BootID = bootID
		}
		boot.ServerIDs = append(boot.ServerIDs, serverID)
	}
	t.Logf("bootstrap：namespace=%s，激活 %d 个 v2 proxy 身份；报告身份 identityId=%s serverId=%s",
		boot.Namespace, len(boot.ServerIDs), boot.IdentityID, boot.ServerIDs[0])
	return boot
}

// createV2Namespace 用 admin 令牌建 v2 namespace，返回一次性明文 accessToken 与权威 code。
// 不借用 harness 的私有 CreateV2Namespace（未导出给本包），自行经 DoRequestWithGuard 发请求。
func createV2Namespace(t *testing.T, baseURL, adminToken, name string, guard harness.ProcessGuard) (accessToken, code string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), v2SetupTimeout)
	defer cancel()
	body, err := json.Marshal(map[string]string{"name": name, "description": "loadtest v2 上报场景命名空间"})
	if err != nil {
		t.Fatalf("bootstrap：编码建命名空间请求失败：%v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/admin/v2/namespaces", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("bootstrap：构造建命名空间请求失败：%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := harness.DoRequestWithGuard(req, v2SetupTimeout, guard)
	if err != nil {
		t.Fatalf("bootstrap：建命名空间请求失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("bootstrap：读建命名空间响应失败：%v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap：建命名空间期望 201，实际 %d；片段：%s", resp.StatusCode, snippetOf(raw))
	}
	var out struct {
		Code        string `json:"code"`
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("bootstrap：解析建命名空间响应失败：%v", err)
	}
	if out.AccessToken == "" {
		t.Fatalf("bootstrap：建命名空间响应缺少一次性 accessToken（命名空间 %q）", name)
	}
	if out.Code != "" {
		return out.AccessToken, out.Code
	}
	return out.AccessToken, name // code 缺省回落到请求 name（服务端 code 默认取 name）
}

// registerV2Proxy 注册单个 v2 proxy 身份（新身份进 pending，返回 202 / 幂等复访 200）。
// namespace 由 accessToken 决定（请求体不含 namespace 字段）；proxy 必须带 listeners（显式 null 或不带会 400）。
func registerV2Proxy(t *testing.T, baseURL, accessToken, identityID, serverID, bootID string, listenPort int, guard harness.ProcessGuard) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), v2SetupTimeout)
	defer cancel()
	// proxy 只能带 listeners，**绝不能带 listenPort**（listenPort 仅供 backend；
	// proxy + 非 nil listenPort 会被 endpointReportsForRegistration 判 400 INVALID_PARAM）。
	body, err := json.Marshal(map[string]any{
		"identityId":   identityID,
		"serverId":     serverID,
		"kind":         model.ServerKindProxy,
		"bootId":       bootID,
		"agentVersion": "loadtest",
		"addr":         fmt.Sprintf("127.0.0.1:%d", listenPort),
		"listeners":    []map[string]any{{"bindHost": "0.0.0.0", "port": listenPort, "ordinal": 0}},
	})
	if err != nil {
		t.Fatalf("bootstrap：编码 v2 注册请求失败：%v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/beacon/v2/agent/register", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("bootstrap：构造 v2 注册请求失败：%v", err)
	}
	req.Header.Set("X-Beacon-Token", accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := harness.DoRequestWithGuard(req, v2SetupTimeout, guard)
	if err != nil {
		t.Fatalf("bootstrap：v2 注册 %s 请求失败：%v", serverID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("bootstrap：v2 注册 %s 期望 202/200，实际 %d；片段：%s", serverID, resp.StatusCode, snippetOf(raw))
	}
}

// activateV2Identity 审批 pending 身份为 active（走审批工单 + 异步 worker），并轮询到 active。
func activateV2Identity(t *testing.T, baseURL, adminToken, identityID, serverID string, guard harness.ProcessGuard) {
	t.Helper()
	if err := harness.RequestApproveIdentityWithGuard(baseURL, adminToken, identityID, serverID,
		"loadtest v2 上报身份激活", guard); err != nil {
		t.Fatalf("bootstrap：审批 identity %s（serverId=%s）失败：%v", identityID, serverID, err)
	}
	if err := harness.WaitIdentityStatusByID(baseURL, adminToken, identityID,
		model.AgentIdentityStatusActive, v2SetupTimeout, guard); err != nil {
		t.Fatalf("bootstrap：等 identity %s 进入 active 失败：%v", identityID, err)
	}
}

// buildV2Scenarios 构造 v2 agent 数据面上报场景：指标上报 / 调度候选 / 在线决策。
func buildV2Scenarios(cfg scenarioConfig, v2 *v2Bootstrap) []scenario {
	if v2 == nil || len(v2.ServerIDs) == 0 {
		return nil
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	return []scenario{
		{
			Group: groupV2Report, Name: "v2_metrics_report",
			Path: "/beacon/v2/agent/metrics/report", URL: base + "/beacon/v2/agent/metrics/report",
			Method: http.MethodPost, Weight: weightV2Report, WantStatus: http.StatusAccepted,
			Auth: authV2AgentToken, headers: v2ReportHeaders(v2), bodies: v2MetricBodies(v2),
		},
		{
			Group: groupV2Schedule, Name: "v2_schedule_candidates",
			Path: "/beacon/v2/agent/schedule/candidates", URL: base + "/beacon/v2/agent/schedule/candidates",
			Method: http.MethodGet, Weight: weightV2Candidates, WantStatus: http.StatusOK,
			Auth: authV2AgentToken, headers: v2ReportHeaders(v2),
		},
		{
			Group: groupV2Schedule, Name: "v2_schedule_decide",
			Path: "/beacon/v2/agent/schedule/decide", URL: base + "/beacon/v2/agent/schedule/decide",
			Method: http.MethodPost, Weight: weightV2Decide, WantStatus: http.StatusOK,
			Auth: authV2AgentToken, headers: v2ReportHeaders(v2), bodies: v2DecideBodies(),
		},
	}
}

// v2ReportHeaders 预置 v2 数据面三头（token / identity / boot）+ JSON Content-Type。
func v2ReportHeaders(v2 *v2Bootstrap) []header {
	return []header{
		{key: "X-Beacon-Token", value: v2.AccessToken},
		{key: "X-Beacon-Identity", value: v2.IdentityID},
		{key: "X-Beacon-Boot", value: v2.BootID},
		{key: "Content-Type", value: "application/json"},
	}
}

// v2MetricBodies 预编码指标上报请求体（照 heartbeatBodies 构建期编码，热路径只取址）。
// 设计约束（对齐 metricReportRequest 与 MetricIngestService.Ingest）：
//   - samples ≤ 120；bucketStartMs > 0 且贴近构建时刻（该字段无 skew 校验，取构建时刻附近即可）。
//   - agentTimeMs 省略（=0）：压测用静态请求体，无法像真 agent 那样每请求刷新时钟；写死构建
//     时刻会让长时压测（>5min）后段请求因 clock_skew_too_large 误报 400，故省略跳过该校验，
//     仍完整走到入队 / 202 这条被测路径（校时校验由单测覆盖，不归压测）。
//   - serverId / kind 取权威身份（绑定一致），否则 validateBinding 回 409 identity_binding_mismatch。
func v2MetricBodies(v2 *v2Bootstrap) [][]byte {
	nowMs := time.Now().UnixMilli()
	baseBucket := nowMs - nowMs%5000 // 5s 对齐（agent 侧 5s 一批聚合）
	const count = 8
	bodies := make([][]byte, 0, count)
	for i := range count {
		raw, err := json.Marshal(map[string]any{
			"namespace": v2.Namespace,
			"serverId":  v2.ServerIDs[0],
			"kind":      model.ServerKindProxy,
			"samples": []map[string]any{{
				"bucketStartMs":   baseBucket - int64(i)*5000,
				"sampleCount":     1,
				"cpuPctAvg":       12.5,
				"cpuPctMax":       30.0,
				"memUsedMbAvg":    1024.0,
				"memMaxMb":        4096,
				"connAvg":         8,
				"connMax":         20,
				"backendUp":       2,
				"backendTotal":    4,
				"backendRttMsAvg": 3.5,
				"reportRttMs":     5,
			}},
		})
		if err != nil {
			panic(fmt.Sprintf("编码 v2 指标上报请求体失败：%v", err))
		}
		bodies = append(bodies, raw)
	}
	return bodies
}

// v2DecideBodies 预编码在线决策请求体。scope=lobby：无论 namespace 有无大厅集群都返回 200
// （无候选时 failReason=no_candidate 在 200 体里带回），不踩 zone 作用域的 404 zone_not_found，
// 也不带 admissionScope（无标签真源会 503 admission_unavailable）。
func v2DecideBodies() [][]byte {
	raw, err := json.Marshal(map[string]any{"scope": "lobby"})
	if err != nil {
		panic(fmt.Sprintf("编码 v2 调度决策请求体失败：%v", err))
	}
	return [][]byte{raw}
}

// buildWriteScenarios 构造写路径场景（并发写争用由此观测）。仅在 LOADTEST_ENABLE_WRITE_SCENARIOS
// 显式开启时纳入（默认返回 nil）：写端点逐条落 audit_log，权重远低于读场景。
func buildWriteScenarios(cfg scenarioConfig, v2 *v2Bootstrap) []scenario {
	if !writeScenariosEnabled() || v2 == nil || len(v2.ServerIDs) == 0 {
		return nil
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	adminHeaders := []header{{key: "Authorization", value: "Bearer " + cfg.AdminToken}}
	sharedTokenHeaders := []header{
		{key: "X-Beacon-Token", value: cfg.BootstrapToken},
		{key: "Content-Type", value: "application/json"},
	}
	items := []scenario{{
		Group: groupWrite, Name: "agent_data_plane_attach",
		Path: "/beacon/v1/agent/data-plane/attach", URL: base + "/beacon/v1/agent/data-plane/attach",
		Method: http.MethodPost, Weight: weightWrite, WantStatus: http.StatusOK,
		Auth: authAgentToken, headers: sharedTokenHeaders, bodies: attachBodies(v2),
	}}
	for _, serverID := range v2.ServerIDs {
		items = append(items, scenario{
			Group: groupWrite, Name: "admin_set_server_tags:" + serverID,
			Path: "/admin/v2/servers/" + serverID + "/tags", URL: base + "/admin/v2/servers/" + serverID + "/tags",
			Method: http.MethodPut, Weight: weightWrite, WantStatus: http.StatusOK,
			Auth: authAdminBearer, headers: adminHeaders, bodies: serverTagBodies(),
		})
	}
	return items
}

// writeScenariosEnabled 读环境变量 LOADTEST_ENABLE_WRITE_SCENARIOS：置 1/true/on/yes 才纳入写场景。
// 默认关闭（写场景逐条落 audit_log、污染真源，需显式开启）。
func writeScenariosEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOADTEST_ENABLE_WRITE_SCENARIOS"))) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

// attachBodies 预编码 v1 数据面挂载请求体：固定 serverId + 固定地址。同址重连才会幂等刷新
// （异址会在 TTL 内撞 409 duplicate_server_id），固定地址保证每次 200 且逐条落 instance.register 审计。
func attachBodies(v2 *v2Bootstrap) [][]byte {
	const count = 4 // 有界的固定 serverId 集，重复挂载同 serverId 是刷新、不建新行
	bodies := make([][]byte, 0, count)
	for i := range count {
		raw, err := json.Marshal(map[string]any{
			"namespace": v2.Namespace,
			"serverId":  fmt.Sprintf("loadtest-attach-%d", i),
			"role":      "bukkit",
			"address":   fmt.Sprintf("127.0.0.1:%d", 21000+i),
			"version":   "loadtest",
		})
		if err != nil {
			panic(fmt.Sprintf("编码 v1 挂载请求体失败：%v", err))
		}
		bodies = append(bodies, raw)
	}
	return bodies
}

// serverTagBodies 预编码 admin 标签写请求体：固定 key、value 轮转序号——每次 PUT 都是真实变更，
// 必然 upsert + 落一条 server_tag 审计（server_tag 行有界；audit_log 增长是既定成本）。
func serverTagBodies() [][]byte {
	const count = 4
	bodies := make([][]byte, 0, count)
	for i := range count {
		raw, err := json.Marshal(map[string]any{
			"tags": map[string]string{"loadtest": fmt.Sprintf("seq-%d", i)},
		})
		if err != nil {
			panic(fmt.Sprintf("编码标签写请求体失败：%v", err))
		}
		bodies = append(bodies, raw)
	}
	return bodies
}

// ingestProbeResult 是连接上报入队背压探测的结果。
type ingestProbeResult struct {
	Total           int
	StatusCounts    map[int]int // 见到的状态码 → 次数
	SawBackpressure bool        // 见过 429（conn_ingest_busy 队列满），背压路径可达的正面证据
	SawUnavailable  bool        // 见过 503（等 DB 连接超时 / 其它不可用）
}

// probeIngestQueueHeadroom 并发反复打 POST /beacon/v2/agent/connections/batch，观测状态码分布，
// 用于验证 spec「连接 / 消息上报 p99 仅含队列入队耗时（队列满 429）」——入队快、队列满 429、
// DB 不可用 503。探到 429 是正面证据（证明背压路径真的可达），不以「看起来像错误」为由回避。
func probeIngestQueueHeadroom(t *testing.T, client *http.Client, baseURL string, v2 *v2Bootstrap,
	guard harness.ProcessGuard, rounds, batchSize, concurrency int) ingestProbeResult {
	t.Helper()
	result := ingestProbeResult{StatusCounts: map[int]int{}}
	if v2 == nil || client == nil || rounds < 1 || batchSize < 1 || concurrency < 1 {
		return result
	}
	target := strings.TrimRight(baseURL, "/") + "/beacon/v2/agent/connections/batch"
	headers := v2ReportHeaders(v2)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := w; i < rounds; i += concurrency {
				status := fireConnBatch(t, client, target, headers, v2.BootID, batchSize, guard)
				mu.Lock()
				result.Total++
				result.StatusCounts[status]++
				if status == http.StatusTooManyRequests {
					result.SawBackpressure = true
				}
				if status == http.StatusServiceUnavailable {
					result.SawUnavailable = true
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return result
}

// fireConnBatch 发一个连接批量上报请求，返回状态码；构造 / 网络错误即 t.Fatalf（探测结果才有意义）。
func fireConnBatch(t *testing.T, client *http.Client, target string, headers []header,
	bootID string, batchSize int, guard harness.ProcessGuard) int {
	t.Helper()
	body := connBatchBody(bootID, batchSize)
	reqCtx, cancelReq, err := harness.RequestContext(context.Background(), v2SetupTimeout, guard)
	if err != nil {
		t.Fatalf("连接上报：构造请求上下文失败：%v", err)
	}
	defer cancelReq()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("连接上报：构造请求失败：%v", err)
	}
	for _, h := range headers {
		req.Header.Set(h.key, h.value)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("连接上报：请求失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// connBatchBody 编码一个连接批量上报体：batchSize 条 open 事件。conn_id 必须是 UUIDv7
// （服务端 TimeMsFromUUIDv7 从中取内嵌时间）；playerUuid 非空即可。
func connBatchBody(bootID string, batchSize int) []byte {
	events := make([]map[string]any, 0, batchSize)
	for range batchSize {
		connID, err := uuid.NewV7()
		if err != nil {
			panic(fmt.Sprintf("生成 UUIDv7 conn_id 失败：%v", err))
		}
		events = append(events, map[string]any{
			"kind":       "open",
			"connId":     connID.String(),
			"playerUuid": uuid.NewString(),
			"playerName": "loadtest",
			"clientIp":   "127.0.0.1",
		})
	}
	raw, err := json.Marshal(map[string]any{
		"bootId":       bootID,
		"droppedCount": 0,
		"events":       events,
	})
	if err != nil {
		panic(fmt.Sprintf("编码连接上报请求体失败：%v", err))
	}
	return raw
}

// snippetOf 截断响应体到回带上限（复用 v2SnippetLimit，失败定位不刷屏）。
func snippetOf(raw []byte) string {
	if len(raw) > v2SnippetLimit {
		return string(raw[:v2SnippetLimit]) + "…"
	}
	return string(raw)
}

// 以下编译期引用是给主代理的接入点（并入 TestLoadHTTPControlPlane 前，本文件单独编译暂无调用方），
// 用来消解 unused 类 linter；不是死代码。
var (
	_ = buildV2Scenarios
	_ = buildWriteScenarios
	_ = bootstrapV2Identity
	_ = probeIngestQueueHeadroom
)
