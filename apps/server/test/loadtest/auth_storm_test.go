//go:build e2e && loadtest

// 鉴权风暴探测：在**真实生产链路**上分别压三条鉴权路径，给出各路径的每请求成本差异。
//
// 三条路径的成本模型完全不同（这是本探测的核心结论，不是猜测）：
//
//  1. POST /admin/v1/auth/login —— 唯一付 bcrypt 成本的点。
//     internal/auth/auth.go:70 的 Login 调 bcrypt.CompareHashAndPassword（bcrypt.DefaultCost=10，
//     即 2^10 轮 EksBlowfish）。纯 CPU、无 DB，但单次成本是毫秒级的——它是本探测里最贵的一条路径。
//     注意：用户名比较用 subtle.ConstantTimeCompare，且**口令校验无条件执行**（不做短路），
//     所以「用户名错」与「口令错」成本相同（这是有意的防时序侧信道设计）。
//
//  2. /admin/v2/* 带已签发 HMAC 令牌 —— 纯 CPU，最便宜。
//     internal/server/middleware.go:99 的 adminAuthMiddleware → auth.Verify（auth.go:80）：
//     只做 splitToken（两次 base64 解码）+ hmac.Equal（一次 HMAC-SHA256）+ 过期比较，**不查 DB**。
//
//  3. /beacon/v2/agent/* 上报 —— 每请求 2 次 DB 查询 + 1 次 sha256。
//     internal/server/middleware.go:47 的 agentV2ReportMiddleware → AuthenticateAgentReport
//     （internal/service/v2_control_plane_service.go:321）：namespaceByToken（按 token 的 sha256 查
//     namespace 表）+ findIdentityByID（查 agent_identity 表）。真实 sqlite 下这两次查询走的是磁盘 / page cache。
//
// 另外测「错误凭据风暴」：大量 401 是否影响正常请求时延（鉴权失败会不会成为放大器）。
// 结论的关键是**错误路径与成功路径的成本差**：
//   - login 失败：bcrypt 照样跑满（不短路）→ 错误路径与成功路径几乎同价，但**成功路径多一次审计写**；
//   - admin v2 失败：splitToken / hmac.Equal 提前失败 → 比成功路径**更便宜**（少一次 HMAC 计算）；
//   - v2 上报失败：无效 UUID 直接返回（连 DB 都不查）→ 比成功路径便宜得多。
//
// 无账号锁定 / 无失败计数：全仓无 failCount / lockout / attempts 逻辑，故狂打错误凭据不会触发
// 封禁或 429 升级——这一点本身就是结论（缺防护），不是本探测的缺陷。
//
// 运行（正式 / 冒烟同一入口，凭据缺失即 skip）：
//
//	export E2E_ADMIN_PASS='...' E2E_AUTH_SECRET='...'
//	go test -tags "e2e loadtest" -timeout=40m -v -run '^TestAuthStormRealChain' ./apps/server/test/loadtest/
//
// 环境变量开关：
//
//	AUTH_CONCURRENCY      并发档位（逗号分隔），默认 1,4,16,64
//	AUTH_DURATION         每档时长，默认 10s
//	AUTH_STORM_DURATION   错误凭据风暴时长，默认 20s
//	AUTH_STORM_CONCURRENCY 错误凭据风暴并发，默认 32
//
// 隔离：独立 sqlite 库 beacon-auth.db、独立日志前缀、独立二进制输出路径、独立 v2 namespace code。
package loadtest_e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	// authDefaultConcurrency 是并发档位：1 用于测单请求纯成本（无排队），高并发用于测饱和点。
	authDefaultConcurrency = "1,4,16,64"
	// authDefaultDuration 是每档时长。10s 足够在 1 并发档收满 bcrypt 的样本（≈几十次），
	// 又不至于让 4 档 × 3 路径 × 高并发把整轮拖到不可接受。
	authDefaultDuration = "10s"
	// authDefaultStormDuration / authDefaultStormConcurrency 是错误凭据风暴参数。
	authDefaultStormDuration    = "20s"
	authDefaultStormConcurrency = 32

	// authDBName / authLogPrefix / authBinName / authNamespace 是本用例的隔离标识。
	authDBName    = "beacon-auth.db"
	authLogPrefix = "beacon-auth"
	authBinName   = "beacon-auth-bin"
	// authNamespace 是独立 v2 namespace code。
	authNamespace = "loadtest-auth"
	// authServerID 是 v2 上报身份绑定的 serverId。
	authServerID = "auth-storm-server"

	// authSetupTimeout 是链路前置的单次请求上限。
	authSetupTimeout = 60 * time.Second
	// authRequestTimeout 是压测单请求上限（登录含 bcrypt，给足余量避免把成本误记成超时）。
	authRequestTimeout = 30 * time.Second
	// authMaxSamples 是单档保留的时延样本数（超配额后只计数不再采样）。
	authMaxSamples = 200_000
	// authSnippetLimit 是失败回带的响应片段上限。
	authSnippetLimit = 256
	// authPrimeCount 是「成功路径探测」的前置请求数（预热连接 / 页面缓存，不参与统计）。
	authPrimeCount = 20
)

// authPath 是三条被测鉴权路径之一。
type authPath struct {
	// Name 是报告里的路径标识。
	Name string
	// Method / URL 是请求目标。
	Method string
	URL    string
	// Body 是请求体（login 需要 JSON；其余为 nil）。
	Body []byte
	// Headers 是预置鉴权头。
	Headers map[string]string
	// WantStatus 是「成功」的期望状态码。
	WantStatus int
	// CostModel 是该路径的成本模型描述（报告里展示，帮助读者理解数字差异的来源）。
	CostModel string
	// SourceRef 是成本模型对应的代码位置（路径:行号）。
	SourceRef string
}

// authLatency 是一档压测的时延分布（毫秒）。
type authLatency struct {
	Count int     `json:"count"`
	AvgMs float64 `json:"avgMs"`
	P50Ms float64 `json:"p50Ms"`
	P95Ms float64 `json:"p95Ms"`
	P99Ms float64 `json:"p99Ms"`
	MaxMs float64 `json:"maxMs"`
}

// authResult 是一条路径 × 一个并发档的实测结果。
type authResult struct {
	Path        string      `json:"path"`
	CostModel   string      `json:"costModel"`
	SourceRef   string      `json:"sourceRef"`
	Concurrency int         `json:"concurrency"`
	DurationMs  float64     `json:"durationMs"`
	Requests    int         `json:"requests"`
	Success     int         `json:"success"`
	Failed      int         `json:"failed"`
	NetErrors   int         `json:"netErrors"`
	ErrorRate   float64     `json:"errorRate"`
	RPS         float64     `json:"rps"`
	StatusCodes string      `json:"statusCodes"`
	Latency     authLatency `json:"latency"`
	// Verdict 是这一档的一句话结论（如「单核每秒只能验 N 次登录」）。
	Verdict string `json:"verdict"`
	// ServerCPUSeconds 是本档期间控制面进程消耗的 CPU 秒数（用于推算单请求 CPU 成本）。
	ServerCPUSeconds float64 `json:"serverCpuSeconds"`
	// CPUPerRequestMs 是单请求摊到的控制面 CPU 毫秒（ServerCPUSeconds / Requests）。
	CPUPerRequestMs float64 `json:"cpuPerRequestMs"`
}

// authEnv 是本用例的共享装配结果。
type authEnv struct {
	cp          *harness.ControlPlane
	baseURL     string
	adminToken  string
	adminPass   string
	repoRoot    string
	accessToken string
	identityID  string
	bootID      string
	pid         int
	// metricsBase 是抓取端点基址（独立监听，见 ADR-0090）。
	// 「无鉴权基线」路径打的是它而不是主端口：基线要把「网络 + 路由 + 序列化」的固定成本
	// 从鉴权成本里减掉，而主端口上已不再有 /metrics。
	metricsBase string
}

// TestAuthStormRealChain 是鉴权风暴主入口。
func TestAuthStormRealChain(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")
	levels := blobParseIntList(t, envOr("AUTH_CONCURRENCY", authDefaultConcurrency), "AUTH_CONCURRENCY")
	duration := durationEnv(t, "AUTH_DURATION", authDefaultDuration)
	stormDuration := durationEnv(t, "AUTH_STORM_DURATION", authDefaultStormDuration)
	stormConcurrency := intEnv(t, "AUTH_STORM_CONCURRENCY", authDefaultStormConcurrency)

	env := authSetup(t, adminPass, authSecret)
	paths := authBuildPaths(t, env)

	t.Logf("鉴权风暴开始：档位=%v，每档 %s，宿主 load=%s", levels, duration, authLoadText())
	results := make([]authResult, 0, len(paths)*len(levels))
	for i := range paths {
		for _, level := range levels {
			results = append(results, authMeasure(t, env, &paths[i], level, duration))
		}
	}

	// 错误凭据风暴：三路径同时被大量错误凭据轰击，同时以低并发打「正常请求」量其是否被拖慢。
	// 成功路径的对照 RPS 直接复用上面已测出的同并发档结果，不重新测一遍——
	// 重测会在风暴刚结束时进行，残余排队会把对照值压低，反而把「错误路径更便宜」的结论夸大。
	storm := authStorm(t, env, paths, results, stormDuration, stormConcurrency)

	authReport(t, results, storm)
}

// ---- 前置装配 ----

// authSetup 起独立控制面并准备三条路径所需的真实凭据。
func authSetup(t *testing.T, adminPass, authSecret string) *authEnv {
	t.Helper()
	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	workDir := filepath.Join(repoRoot, ".tmp", "loadtest")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("创建压测工作目录失败：%v", err)
	}
	authRemoveStaleDB(t, workDir)
	t.Cleanup(func() { authCleanupDBFiles(t, workDir) })

	binPath := filepath.Join(workDir, authBinName)
	if err := buildBeaconTo(repoRoot, binPath); err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}
	t.Cleanup(func() { _ = os.Remove(binPath) })

	cpCfg := harness.ControlPlaneConfig{
		BinPath: binPath, RepoRoot: repoRoot,
		DBDriver: "sqlite", DBDSN: filepath.Join(workDir, authDBName),
		AdminPassword: adminPass, AuthSecret: authSecret, BootstrapToken: bootstrapToken,
		LogPrefix: authLogPrefix,
		// WARN：鉴权风暴会产生海量 401，INFO 级访问日志会把 .tmp 写爆并让磁盘 I/O 混进时延。
		ExtraEnv: map[string]string{"BEACON_LOG_LEVEL": "WARN"},
	}
	cp, baseURL, metricsBase := startControlPlaneWithMetrics(t, cpCfg, controlPlaneStartAttempts)
	harness.CleanupControlPlane(t, cp)
	_, stderrPath := cp.LogPaths()
	t.Logf("控制面已就绪：%s（stderr：%s）", baseURL, stderrPath)

	adminToken, err := harness.Login(baseURL, adminUser, adminPass, cp)
	if err != nil {
		t.Fatalf("管理员登录失败：%v", err)
	}

	// v2 上报路径需要一个真实 active 身份（否则每请求止步于 403 agent_not_confirmed，
	// 量到的就不是「鉴权通过后的 2 次 DB 查询」）。
	accessToken, _ := createV2Namespace(t, baseURL, adminToken, authNamespace, cp)
	identityID := uuid.NewString()
	bootID := uuid.NewString()
	registerV2Proxy(t, baseURL, accessToken, identityID, authServerID, bootID, 25699, cp)
	activateV2Identity(t, baseURL, adminToken, identityID, authServerID, cp)

	pid := findProcessPIDByCmdline(binPath)
	if pid <= 0 {
		pid = findProcessPIDByCmdline(authBinName)
	}
	if pid <= 0 {
		t.Log("未能定位控制面进程 PID：CPU 成本列将为空（不影响 RPS / 分位数结论）")
	}

	return &authEnv{
		cp: cp, baseURL: baseURL, adminToken: adminToken, adminPass: adminPass,
		repoRoot: repoRoot, accessToken: accessToken,
		identityID: identityID, bootID: bootID, pid: pid,
		metricsBase: metricsBase,
	}
}

// authBuildPaths 构造三条被测鉴权路径（外加一条「无鉴权基线」用于对照）。
func authBuildPaths(t *testing.T, env *authEnv) []authPath {
	t.Helper()
	base := strings.TrimRight(env.baseURL, "/")
	loginBody, err := json.Marshal(map[string]string{"username": adminUser, "password": env.adminPass})
	if err != nil {
		t.Fatalf("编码登录请求体失败：%v", err)
	}
	// v2 上报请求体：samples 只给 1 条（成本焦点在鉴权，不在入队）。
	reportBody, err := json.Marshal(map[string]any{
		"namespace": authNamespace, "serverId": authServerID, "kind": model.ServerKindProxy,
		"samples": []map[string]any{{
			"bucketStartMs": time.Now().UnixMilli() - 5000, "sampleCount": 1,
			"cpuPctAvg": 10.0, "cpuPctMax": 20.0, "memUsedMbAvg": 512.0, "memMaxMb": 1024,
			"connAvg": 4, "connMax": 8, "backendUp": 1, "backendTotal": 2, "backendRttMsAvg": 3.0,
		}},
	})
	if err != nil {
		t.Fatalf("编码 v2 上报请求体失败：%v", err)
	}
	return []authPath{
		{
			Name: "POST /admin/v1/auth/login", Method: http.MethodPost, URL: base + "/admin/v1/auth/login",
			Body: loginBody, Headers: map[string]string{"Content-Type": "application/json"},
			WantStatus: http.StatusOK,
			CostModel:  "bcrypt.CompareHashAndPassword（DefaultCost=10，2^10 轮）+ 登录审计写；纯 CPU，无鉴权 DB 查询",
			SourceRef:  "internal/auth/auth.go:70（Login）",
		},
		{
			Name: "GET /admin/v2/namespaces（HMAC 令牌）", Method: http.MethodGet,
			URL:        base + "/admin/v2/namespaces",
			Headers:    map[string]string{"Authorization": "Bearer " + env.adminToken},
			WantStatus: http.StatusOK,
			CostModel:  "splitToken（2 次 base64 解码）+ hmac.Equal（1 次 HMAC-SHA256）+ 过期比较；不查 DB",
			SourceRef:  "internal/server/middleware.go:99（adminAuthMiddleware）→ internal/auth/auth.go:80（Verify）",
		},
		{
			Name: "POST /beacon/v2/agent/metrics/report（v2 上报）", Method: http.MethodPost,
			URL: base + "/beacon/v2/agent/metrics/report", Body: reportBody,
			Headers: map[string]string{
				"X-Beacon-Token": env.accessToken, "X-Beacon-Identity": env.identityID,
				"X-Beacon-Boot": env.bootID, "Content-Type": "application/json",
			},
			WantStatus: http.StatusAccepted,
			CostModel:  "namespaceByToken（按 token sha256 查 namespace 表）+ findIdentityByID（查 agent_identity 表）；2 次 DB 查询 + 1 次 sha256",
			SourceRef:  "internal/server/middleware.go:47 → internal/service/v2_control_plane_service.go:321（AuthenticateAgentReport）",
		},
		{
			Name: "GET /metrics（抓取端点基线）", Method: http.MethodGet, URL: scrapeMetricsURL(env.metricsBase),
			Headers:    headersToMap(metricsAuthHeaders()),
			WantStatus: http.StatusOK,
			CostModel:  "无鉴权（抓取端点，见 ADR-0090；未配凭据时不校验 Authorization）；用于把「网络 + 路由 + 序列化」的固定成本从鉴权成本里减掉",
			SourceRef:  "apps/server/internal/metrics/endpoint.go（EndpointHandler）",
		},
	}
}

// ---- 压测核心 ----

// authMeasure 以固定并发压一条路径 duration 时长，返回 RPS / 分位数 / 状态码分布 / CPU 成本。
func authMeasure(t *testing.T, env *authEnv, path *authPath, concurrency int, duration time.Duration) authResult {
	t.Helper()
	res := authResult{
		Path: path.Name, CostModel: path.CostModel, SourceRef: path.SourceRef,
		Concurrency: concurrency,
	}
	client := authNewClient(concurrency)
	ctx, cancel := context.WithTimeout(context.Background(), duration+authRequestTimeout)
	defer cancel()

	// 预热：把连接建好、把 bcrypt 的页面缓存焐热，不让首批请求把冷启动成本算进结果。
	authPrime(t, client, path)

	cpuBefore := authCPUSeconds(env.pid)
	// 状态码用「每 worker 一张小 map」统计，而不是预分配切片：
	// 64 并发 × 8s 在 HMAC 路径上能打到数万请求，任何固定容量的切片都会截断状态码分布
	// （首版用 concurrency*4096 的切片，结果只记下 8175 个样本，与 Requests 严重不符）。
	statusMaps := make([]map[int]int, concurrency)
	latencies := make([][]int64, concurrency)
	var total, success, failed, netErr atomic.Int64

	deadline := time.Now().Add(duration)
	started := time.Now()
	var wg sync.WaitGroup
	for w := range concurrency {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			local := make([]int64, 0, 4096)
			seen := make(map[int]int, 8)
			for time.Now().Before(deadline) {
				if ctx.Err() != nil {
					break
				}
				status, elapsedNs, err := authSendOnce(ctx, client, path)
				total.Add(1)
				switch {
				case err != nil:
					netErr.Add(1)
					status = -1
				case status == path.WantStatus:
					success.Add(1)
				default:
					failed.Add(1)
				}
				seen[status]++
				// 有界采样：每 worker 最多留 authMaxSamples/concurrency 条，超了只计数。
				if len(local) < authMaxSamples/concurrency {
					local = append(local, elapsedNs)
				}
			}
			statusMaps[worker] = seen
			latencies[worker] = local
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(started)
	res.DurationMs = float64(elapsed.Microseconds()) / 1000.0
	res.ServerCPUSeconds = authCPUSeconds(env.pid) - cpuBefore

	res.Requests = int(total.Load())
	res.Success = int(success.Load())
	res.Failed = int(failed.Load())
	res.NetErrors = int(netErr.Load())
	res.ErrorRate = authRatio(res.Failed+res.NetErrors, res.Requests)
	res.RPS = authRate(res.Requests, elapsed)
	res.StatusCodes = authMergeStatusMaps(statusMaps)

	res.Latency = authLatencyStats(authFlattenSamples(latencies))
	if res.Requests > 0 {
		res.CPUPerRequestMs = res.ServerCPUSeconds * 1000.0 / float64(res.Requests)
	}
	res.Verdict = authVerdict(path, &res)
	return res
}

// authPrime 预热一条路径（不计入统计）。
func authPrime(t *testing.T, client *http.Client, path *authPath) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), authRequestTimeout)
	defer cancel()
	for range authPrimeCount {
		status, _, err := authSendOnce(ctx, client, path)
		if err != nil {
			t.Logf("预热请求失败（%s）：%v", path.Name, err)
			return
		}
		if status != path.WantStatus {
			t.Logf("预热请求状态码异常（%s）：期望 %d 实际 %d", path.Name, path.WantStatus, status)
			return
		}
	}
}

// authSendOnce 发一次请求并读空响应体，返回状态码与耗时。
func authSendOnce(ctx context.Context, client *http.Client, path *authPath) (int, int64, error) {
	var body io.Reader
	if path.Body != nil {
		body = bytes.NewReader(path.Body)
	}
	req, err := http.NewRequestWithContext(ctx, path.Method, path.URL, body)
	if err != nil {
		return 0, 0, err
	}
	for k, v := range path.Headers {
		req.Header.Set(k, v)
	}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, time.Since(started).Nanoseconds(), err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, time.Since(started).Nanoseconds(), nil
}

// ---- 错误凭据风暴 ----

// authStormResult 是错误凭据风暴的实测结果。
type authStormResult struct {
	DurationMs  float64 `json:"durationMs"`
	Concurrency int     `json:"concurrency"`
	// 三条路径的错误凭据轰击量。
	LoginAttempts int     `json:"loginAttempts"`
	LoginRPS      float64 `json:"loginRps"`
	LoginStatuses string  `json:"loginStatuses"`
	LoginP50Ms    float64 `json:"loginP50Ms"`
	LoginP95Ms    float64 `json:"loginP95Ms"`

	AdminV2Attempts int     `json:"adminV2Attempts"`
	AdminV2RPS      float64 `json:"adminV2Rps"`
	AdminV2Statuses string  `json:"adminV2Statuses"`
	AdminV2P50Ms    float64 `json:"adminV2P50Ms"`
	AdminV2P95Ms    float64 `json:"adminV2P95Ms"`

	V2ReportAttempts int     `json:"v2ReportAttempts"`
	V2ReportRPS      float64 `json:"v2ReportRps"`
	V2ReportStatuses string  `json:"v2ReportStatuses"`
	V2ReportP50Ms    float64 `json:"v2ReportP50Ms"`
	V2ReportP95Ms    float64 `json:"v2ReportP95Ms"`

	// 正常请求在风暴期间的表现（与风暴前基线的对比是「是否被放大」的判据）。
	ProbeBefore  authLatency `json:"probeBefore"`
	ProbeDuring  authLatency `json:"probeDuring"`
	ProbeAfter   authLatency `json:"probeAfter"`
	ProbeDeltaMs float64     `json:"probeDeltaMs"`

	// 错误路径 vs 成功路径的成本比（同一并发档下 RPS 的比值）。
	WrongVsRightLogin float64 `json:"wrongVsRightLogin"`
	WrongVsRightAdmin float64 `json:"wrongVsRightAdmin"`
	WrongVsRightV2    float64 `json:"wrongVsRightV2"`
}

// authStorm 并发轰击三条路径的错误凭据，同时以低并发探「正常请求」时延是否被拖慢。
// results 是前面已测出的成功路径结果（用于算错误/成功成本比，取同并发档）。
func authStorm(t *testing.T, env *authEnv, paths []authPath, results []authResult,
	duration time.Duration, concurrency int) authStormResult {
	t.Helper()
	res := authStormResult{DurationMs: float64(duration.Microseconds()) / 1000.0, Concurrency: concurrency}
	base := strings.TrimRight(env.baseURL, "/")

	// 三组错误凭据路径：
	//   - login：口令错误。注意用户名比较与口令校验都不短路，故成本与成功登录同价。
	//   - admin v2：Authorization 头格式合法但签名不符（走 splitToken → hmac.Equal 失败）。
	//   - v2 上报：token 有效但 identity 不是合法 UUID（服务端在查库前即返回 401）——
	//     这是最便宜的失败形态，正好用来量「错误路径比成功路径便宜多少」。
	wrongLoginBody, err := json.Marshal(map[string]string{"username": adminUser, "password": "definitely-wrong-password"})
	if err != nil {
		t.Fatalf("编码错误登录请求体失败：%v", err)
	}
	wrongPaths := []authPath{
		{
			Name: "login(错误口令)", Method: http.MethodPost, URL: base + "/admin/v1/auth/login",
			Body: wrongLoginBody, Headers: map[string]string{"Content-Type": "application/json"},
			WantStatus: http.StatusUnauthorized,
		},
		{
			Name: "admin v2(错误签名)", Method: http.MethodGet, URL: base + "/admin/v2/namespaces",
			Headers:    map[string]string{"Authorization": "Bearer " + authForgedToken()},
			WantStatus: http.StatusUnauthorized,
		},
		{
			Name: "v2 上报(非法 identity)", Method: http.MethodPost, URL: base + "/beacon/v2/agent/metrics/report",
			Body: []byte(`{}`), Headers: map[string]string{
				"X-Beacon-Token": env.accessToken, "X-Beacon-Identity": "not-a-uuid",
				"X-Beacon-Boot": env.bootID, "Content-Type": "application/json",
			},
			WantStatus: http.StatusUnauthorized,
		},
	}

	// 基线：风暴前的正常请求时延（用 HMAC 令牌路径，成本最低、最容易被噪声放大，是敏感探针）。
	probePath := &paths[1]
	probeClient := authNewClient(1)
	res.ProbeBefore = authProbeLatency(t, probeClient, probePath, 200)

	// 正常请求探针：独立低并发 worker 在整个风暴期间持续打，量「被拖慢了多少」。
	probeCtx, probeCancel := context.WithCancel(context.Background())
	probeLatencies := make([]int64, 0, 4096)
	var probeMu sync.Mutex
	var probeWG sync.WaitGroup
	probeWG.Add(1)
	go func() {
		defer probeWG.Done()
		client := authNewClient(1)
		for probeCtx.Err() == nil {
			status, ns, err := authSendOnce(probeCtx, client, probePath)
			if err != nil {
				return
			}
			if status == probePath.WantStatus {
				probeMu.Lock()
				probeLatencies = append(probeLatencies, ns)
				probeMu.Unlock()
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	// 三条错误路径各起 concurrency 个 worker，同时轰 duration。
	// 每个 worker 各持自己的计数与样本，收尾时按路径合并——共享切片并发 append 是数据竞争。
	type stormStat struct {
		attempts atomic.Int64
		// 每 worker 一份（下标 = worker 序号）。
		statuses []map[int]int
		latency  [][]int64
	}
	stats := make([]*stormStat, len(wrongPaths))
	for i := range stats {
		stats[i] = &stormStat{
			statuses: make([]map[int]int, concurrency),
			latency:  make([][]int64, concurrency),
		}
	}
	deadline := time.Now().Add(duration)
	stormStarted := time.Now()
	var wg sync.WaitGroup
	for i := range wrongPaths {
		for w := range concurrency {
			wg.Add(1)
			go func(pathIdx, worker int) {
				defer wg.Done()
				client := authNewClient(1)
				stat := stats[pathIdx]
				path := &wrongPaths[pathIdx]
				seen := make(map[int]int, 4)
				local := make([]int64, 0, 4096)
				for time.Now().Before(deadline) {
					status, ns, err := authSendOnce(context.Background(), client, path)
					stat.attempts.Add(1)
					if err != nil {
						status = -1
					}
					seen[status]++
					if len(local) < authMaxSamples/concurrency {
						local = append(local, ns)
					}
				}
				stat.statuses[worker] = seen
				stat.latency[worker] = local
			}(i, w)
		}
	}
	wg.Wait()
	stormElapsed := time.Since(stormStarted)
	probeCancel()
	probeWG.Wait()

	probeMu.Lock()
	during := append([]int64(nil), probeLatencies...)
	probeMu.Unlock()

	res.LoginAttempts = int(stats[0].attempts.Load())
	res.LoginRPS = authRate(res.LoginAttempts, stormElapsed)
	res.LoginStatuses = authMergeStatusMaps(stats[0].statuses)
	res.LoginP50Ms = authLatencyStats(authFlattenSamples(stats[0].latency)).P50Ms
	res.LoginP95Ms = authLatencyStats(authFlattenSamples(stats[0].latency)).P95Ms

	res.AdminV2Attempts = int(stats[1].attempts.Load())
	res.AdminV2RPS = authRate(res.AdminV2Attempts, stormElapsed)
	res.AdminV2Statuses = authMergeStatusMaps(stats[1].statuses)
	res.AdminV2P50Ms = authLatencyStats(authFlattenSamples(stats[1].latency)).P50Ms
	res.AdminV2P95Ms = authLatencyStats(authFlattenSamples(stats[1].latency)).P95Ms

	res.V2ReportAttempts = int(stats[2].attempts.Load())
	res.V2ReportRPS = authRate(res.V2ReportAttempts, stormElapsed)
	res.V2ReportStatuses = authMergeStatusMaps(stats[2].statuses)
	res.V2ReportP50Ms = authLatencyStats(authFlattenSamples(stats[2].latency)).P50Ms
	res.V2ReportP95Ms = authLatencyStats(authFlattenSamples(stats[2].latency)).P95Ms

	res.ProbeDuring = authLatencyStats(during)
	res.ProbeAfter = authProbeLatency(t, probeClient, probePath, 200)
	res.ProbeDeltaMs = res.ProbeDuring.P95Ms - res.ProbeBefore.P95Ms

	// 错误路径 vs 成功路径的成本比：同一并发档下 RPS 比值 >1 表示错误路径更便宜（更快拒绝）。
	// 对照值取前面已测出的成功路径 RPS（同并发档，无风暴干扰）。
	res.WrongVsRightLogin = authRatioFloat(res.LoginRPS, authRightRPS(results, paths[0].Name, concurrency))
	res.WrongVsRightAdmin = authRatioFloat(res.AdminV2RPS, authRightRPS(results, paths[1].Name, concurrency))
	res.WrongVsRightV2 = authRatioFloat(res.V2ReportRPS, authRightRPS(results, paths[2].Name, concurrency))
	return res
}

// authRightRPS 从已测结果里取「某路径 × 某并发档」的成功 RPS（无匹配返回 0，比值即显示为 0）。
func authRightRPS(results []authResult, path string, concurrency int) float64 {
	for i := range results {
		if results[i].Path == path && results[i].Concurrency == concurrency {
			return results[i].RPS
		}
	}
	return 0
}

// authProbeLatency 以单并发打 count 次，返回时延分布（用于「是否被拖慢」的对照）。
func authProbeLatency(t *testing.T, client *http.Client, path *authPath, count int) authLatency {
	t.Helper()
	samples := make([]int64, 0, count)
	ctx, cancel := context.WithTimeout(context.Background(), authRequestTimeout*2)
	defer cancel()
	for range count {
		status, ns, err := authSendOnce(ctx, client, path)
		if err != nil || status != path.WantStatus {
			continue
		}
		samples = append(samples, ns)
	}
	return authLatencyStats(samples)
}

// authForgedToken 构造一个结构合法但签名不符的令牌（base64("admin|<未来时间>").base64(32 字节伪签名)）。
// 结构必须合法：否则 splitToken 会在 base64 解码阶段就失败，量到的就不是「hmac.Equal 失败」这条路径。
// 编码用 base64.RawURLEncoding（无填充 URL-safe），与 internal/auth/auth.go 的 issue 完全同形。
func authForgedToken() string {
	payload := []byte(fmt.Sprintf("%s|%d", adminUser, time.Now().Add(time.Hour).Unix()))
	sig := sha256.Sum256([]byte("forged-signature-not-the-real-secret"))
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig[:])
}

// ---- 报告 ----

// authReport 打印全部结果（Markdown 表格 + 结论行）。
func authReport(t *testing.T, results []authResult, storm authStormResult) {
	t.Helper()
	t.Log("== 三路径鉴权成本实测（并发档位 × 路径）==")
	t.Log("| 路径 | 并发 | RPS | p50(ms) | p95(ms) | p99(ms) | 成功 | 失败 | 网络错误 | 状态码 | 控制面CPU/请求(ms) |")
	t.Log("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, r := range results {
		t.Logf("| %s | %d | %.1f | %.2f | %.2f | %.2f | %d | %d | %d | %s | %.3f |",
			r.Path, r.Concurrency, r.RPS, r.Latency.P50Ms, r.Latency.P95Ms, r.Latency.P99Ms,
			r.Success, r.Failed, r.NetErrors, r.StatusCodes, r.CPUPerRequestMs)
	}

	t.Log("== 各路径成本模型与代码位置 ==")
	seen := map[string]bool{}
	for _, r := range results {
		if seen[r.Path] {
			continue
		}
		seen[r.Path] = true
		t.Logf("- %s：%s（%s）", r.Path, r.CostModel, r.SourceRef)
	}

	t.Log("== 单请求成本结论（低并发档，无排队）==")
	for _, r := range results {
		if r.Concurrency != 1 || r.Verdict == "" {
			continue
		}
		t.Logf("- %s：%s", r.Path, r.Verdict)
	}

	t.Log("== 错误凭据风暴（三路径同时被轰击）==")
	t.Logf("风暴时长 %.0fms，每条错误路径 %d 并发", storm.DurationMs, storm.Concurrency)
	t.Log("| 错误路径 | 尝试次数 | RPS | p50(ms) | p95(ms) | 状态码分布 |")
	t.Log("| --- | --- | --- | --- | --- | --- |")
	t.Logf("| login（错误口令） | %d | %.1f | %.2f | %.2f | %s |",
		storm.LoginAttempts, storm.LoginRPS, storm.LoginP50Ms, storm.LoginP95Ms, storm.LoginStatuses)
	t.Logf("| admin v2（错误签名） | %d | %.1f | %.2f | %.2f | %s |",
		storm.AdminV2Attempts, storm.AdminV2RPS, storm.AdminV2P50Ms, storm.AdminV2P95Ms, storm.AdminV2Statuses)
	t.Logf("| v2 上报（非法 identity） | %d | %.1f | %.2f | %.2f | %s |",
		storm.V2ReportAttempts, storm.V2ReportRPS, storm.V2ReportP50Ms, storm.V2ReportP95Ms, storm.V2ReportStatuses)

	t.Log("== 错误路径是否拖慢正常请求（单并发探针，HMAC 令牌路径）==")
	t.Logf("风暴前 p50/p95=%.2f/%.2f ms；风暴中 p50/p95=%.2f/%.2f ms（样本 %d）；风暴后 p50/p95=%.2f/%.2f ms",
		storm.ProbeBefore.P50Ms, storm.ProbeBefore.P95Ms,
		storm.ProbeDuring.P50Ms, storm.ProbeDuring.P95Ms, storm.ProbeDuring.Count,
		storm.ProbeAfter.P50Ms, storm.ProbeAfter.P95Ms)
	t.Logf("风暴中相对风暴前的 p95 增量：%.2f ms", storm.ProbeDeltaMs)

	t.Log("== 错误路径 vs 成功路径成本比（RPS 比，>1 表示错误路径更快拒绝）==")
	t.Logf("login 错误/成功 = %.2f；admin v2 错误/成功 = %.2f；v2 上报 错误/成功 = %.2f",
		storm.WrongVsRightLogin, storm.WrongVsRightAdmin, storm.WrongVsRightV2)
}

// authVerdict 为一条结果生成一句话结论。
func authVerdict(path *authPath, res *authResult) string {
	switch {
	case strings.Contains(path.Name, "login"):
		// 单请求 CPU 成本 → 单核每秒能验多少次（这是 bcrypt 成为瓶颈时的量化口径）。
		perCore := 0.0
		if res.CPUPerRequestMs > 0 {
			perCore = 1000.0 / res.CPUPerRequestMs
		}
		return fmt.Sprintf("单请求 p50=%.1fms、控制面 CPU %.3fms/请求 → 单核约 %.0f 次/秒（bcrypt 是纯 CPU 成本，"+
			"并发再高也不会变快，只会排队）", res.Latency.P50Ms, res.CPUPerRequestMs, perCore)
	case strings.Contains(path.Name, "HMAC"):
		return fmt.Sprintf("单请求 p50=%.2fms（其中控制面 CPU %.3fms/请求）——最便宜的鉴权路径，纯 CPU 无 DB",
			res.Latency.P50Ms, res.CPUPerRequestMs)
	case strings.Contains(path.Name, "v2 上报"):
		return fmt.Sprintf("单请求 p50=%.2fms、控制面 CPU %.3fms/请求——2 次 DB 查询的固定成本，"+
			"在真实 sqlite 上比纯 CPU 路径贵", res.Latency.P50Ms, res.CPUPerRequestMs)
	default:
		return fmt.Sprintf("单请求 p50=%.2fms——无鉴权基线，可作为「网络+路由+序列化」的固定成本参考", res.Latency.P50Ms)
	}
}

// ---- 小工具 ----

// authNewClient 构造压测客户端（独立连接池，Proxy 置 nil 避免环境代理）。
func authNewClient(maxIdle int) *http.Client {
	if maxIdle <= 0 {
		maxIdle = 1
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:          maxIdle,
			MaxIdleConnsPerHost:   maxIdle,
			IdleConnTimeout:       30 * time.Second,
			DisableCompression:    true,
			ForceAttemptHTTP2:     false,
			ResponseHeaderTimeout: authRequestTimeout,
		},
	}
}

// authCPUSeconds 读某进程累计 CPU 秒数（utime+stime）。pid<=0 返回 0。
func authCPUSeconds(pid int) float64 {
	if pid <= 0 {
		return 0
	}
	return float64(readProcCPUTicks(pid)) / linuxClockTicks
}

// authLatencyStats 由纳秒样本算分位数（线性插值，单位毫秒）。
func authLatencyStats(samples []int64) authLatency {
	if len(samples) == 0 {
		return authLatency{}
	}
	sorted := make([]int64, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var sum int64
	for _, v := range sorted {
		sum += v
	}
	return authLatency{
		Count: len(sorted),
		AvgMs: float64(sum) / float64(len(sorted)) / 1e6,
		P50Ms: authQuantile(sorted, 0.50),
		P95Ms: authQuantile(sorted, 0.95),
		P99Ms: authQuantile(sorted, 0.99),
		MaxMs: float64(sorted[len(sorted)-1]) / 1e6,
	}
}

// authQuantile 在已排序的纳秒样本上做线性插值分位，返回毫秒。
func authQuantile(sorted []int64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return float64(sorted[0]) / 1e6
	}
	pos := p * float64(len(sorted)-1)
	lo := int(pos)
	hi := min(lo+1, len(sorted)-1)
	frac := pos - float64(lo)
	value := float64(sorted[lo]) + (float64(sorted[hi])-float64(sorted[lo]))*frac
	return value / 1e6
}

// authRate 由计数与时长算每秒速率。
func authRate(count int, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(count) / elapsed.Seconds()
}

// authRatio 返回 part/total（total<=0 时返回 0）。
func authRatio(part, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total)
}

// authRatioFloat 返回 a/b（b<=0 时返回 0）。
func authRatioFloat(a, b float64) float64 {
	if b <= 0 {
		return 0
	}
	return a / b
}

// authMergeStatusMaps 合并「每 worker 一张状态码计数表」并渲染成可读文本。
// 用 map 而非定长切片：高并发档能打到数万请求，定长切片会静默截断分布。
func authMergeStatusMaps(maps []map[int]int) string {
	merged := map[int]int{}
	for _, m := range maps {
		for status, count := range m {
			merged[status] += count
		}
	}
	keys := make([]int, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		label := strconv.Itoa(k)
		if k < 0 {
			label = "网络错误"
		}
		parts = append(parts, fmt.Sprintf("%s×%d", label, merged[k]))
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, ", ")
}

// authFlattenSamples 把「每 worker 一份的时延样本」摊平成一维（供统一分位计算）。
func authFlattenSamples(perWorker [][]int64) []int64 {
	out := make([]int64, 0, authMaxSamples)
	for _, l := range perWorker {
		out = append(out, l...)
	}
	return out
}

// authLoadText 读宿主机 loadavg（宿主负载是最大的混淆变量，必须随报告记录）。
func authLoadText() string {
	la := readLoadAvg()
	return fmt.Sprintf("1m=%.2f 5m=%.2f 15m=%.2f", la.Load1, la.Load5, la.Load15)
}

// authRemoveStaleDB 只清理本用例自己的库文件（含 WAL / SHM）。
func authRemoveStaleDB(t *testing.T, dir string) {
	t.Helper()
	if err := authRemoveDBFiles(dir); err != nil {
		t.Fatalf("清理本用例遗留库文件失败：%v", err)
	}
}

// authCleanupDBFiles 是收尾用的非致命版本。
func authCleanupDBFiles(t *testing.T, dir string) {
	t.Helper()
	if err := authRemoveDBFiles(dir); err != nil {
		t.Logf("收尾：清理库文件失败（可手工删除）：%v", err)
	}
}

// authRemoveDBFiles 删除本用例的库文件三件套。
func authRemoveDBFiles(dir string) error {
	base := filepath.Join(dir, authDBName)
	for _, path := range []string{base, base + "-wal", base + "-shm"} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除 %s：%w", path, err)
		}
	}
	return nil
}
