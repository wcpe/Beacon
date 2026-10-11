//go:build e2e && loadtest

// 控制面端到端 HTTP 压测主入口（纯 Go harness，不依赖 k6 / vegeta / wrk 等外部压测工具）。
//
// 编排：构建控制面二进制 → 起真实进程（独立 sqlite + 动态端口）→ 登录 → 注册一批虚拟 agent →
// 场景预检 → 分级并发压测 → soak（逐分钟采样）→ 出 Markdown + JSON 报告（落 .tmp/loadtest/）。
//
// 运行（完整压测）：
//
//	export E2E_ADMIN_PASS='...' E2E_AUTH_SECRET='...'
//	go test -tags "e2e loadtest" -timeout=60m -v -run '^TestLoad' ./apps/server/test/loadtest/
//
// 冒烟（几十秒，只验证 harness 本身可用）：
//
//	LOADTEST_LEVELS=1,4 LOADTEST_LEVEL_DURATION=5s LOADTEST_SOAK_DURATION=5s \
//	  go test -tags "e2e loadtest" -timeout=10m -v -run '^TestLoad' ./apps/server/test/loadtest/
//
// 环境变量开关（均带合理默认值，见下方常量）：
//
//	LOADTEST_LEVELS             并发档位列表，默认 1,8,32,128
//	LOADTEST_LEVEL_DURATION     每档时长，默认 30s
//	LOADTEST_SOAK_CONCURRENCY   soak 并发，默认 32（取中间档）
//	LOADTEST_SOAK_DURATION      soak 时长，默认 5m
//	LOADTEST_AGENT_SERVERS      虚拟 agent 数量（心跳场景的轮转池），默认 64
//	LOADTEST_CP_LOG_LEVEL       控制面日志级别，默认 WARN
//	LOADTEST_ENABLE_WRITE_SCENARIOS  置 1 才纳入写路径场景；默认不纳入（写操作逐条落审计，
//	                                 压出来的是审计写入能力而非接口能力，且会污染 audit_log）
//	LOADTEST_INGEST_ROUNDS / LOADTEST_INGEST_BATCH / LOADTEST_INGEST_CONCURRENCY
//	                             v2 上报通道背压探测规模，默认 200 轮 × 批量 200 × 并发 16
//
// 覆盖范围（四类，口径不同，报告分节给）：
//   - 请求-响应型：6 个只读端点 + v2 agent 数据面（上报 / 调度）+ 可选写路径，用并发与分位数衡量；
//   - soak：长时逐分钟分桶，看时延与错误率是否随时间漂移；
//   - 挂起型：长轮询与 SSE 单独探测，衡量挂起容量与唤醒时延（无 RPS 口径）；
//   - 背压：v2 上报通道的有界队列（429）与等 DB 连接超时（503）是否可达。
package loadtest_e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	// 默认档位与时长：档位覆盖「无竞争 → 明显排队」的区间，soak 取中间档跑长时。
	defaultLevels          = "1,8,32,128"
	defaultLevelDuration   = "30s"
	defaultSoakConcurrency = 32
	defaultSoakDuration    = "5m"
	defaultAgentServers    = 64
	// defaultCPLogLevel 默认压制逐请求 INFO 访问日志：百万级请求会把 .tmp 写爆，
	// 也让测出来的数字混进磁盘 I/O。需要完整访问日志时用 LOADTEST_CP_LOG_LEVEL=INFO 还原。
	defaultCPLogLevel = "WARN"

	// v2Namespace / v2Servers 是 v2 agent 数据面探测用的环境与身份数。
	// 独立于 agentNamespace：v2 身份需审批成 active，与 v1 虚拟 agent 的生命周期分开，
	// 避免一边清理一边把另一边的身份判失效。
	v2Namespace = "loadtest-v2"
	v2Servers   = 8

	// 上报背压探测默认值：spec 声称「连接/消息上报 p99 仅含队列入队耗时、队列满 429」，
	// 要触到那条边界需要足够大的批量与并发；这里给一组「明显超出稳态」的默认值。
	defaultIngestRounds      = 200
	defaultIngestBatch       = 200
	defaultIngestConcurrency = 16

	// 挂起探测时长：长轮询要覆盖一次完整挂起（服务端 max-hold-ms=30s），故至少 40s；
	// SSE 要看到保活帧（默认 30s 一帧），故 100s。均可经环境变量缩短以便冒烟。
	suspendedProbeDuration = 45 * time.Second
	suspendedProbeWakeAt   = 12 * time.Second
	sseProbeDuration       = 100 * time.Second

	// adminUser 是控制面管理员用户名（口令由 E2E_ADMIN_PASS 注入，见 harness.Login）。
	adminUser = "admin"
	// bootstrapToken 是本次压测用的 agent 共享令牌（测试专用固定值，仅连本地控制面）。
	bootstrapToken = "beacon-loadtest-bootstrap"
	// agentNamespace 是虚拟 agent 注册所用环境 code：控制面启动时由 SeedDefaults 预置 prod / test。
	// 环境存在但 server 行不存在时，注册与心跳的准入闸放行（service.ensureServerActiveForNamespace），
	// 于是虚拟实例只活在内存注册表里、不写 server 表——压测因此不污染拓扑真源。
	agentNamespace = "prod"
	// logPrefix / sqliteDBName 是控制面日志前缀与独立库文件名（都在 .tmp/loadtest/ 下）。
	logPrefix    = "beacon-loadtest"
	sqliteDBName = "beacon-loadtest.db"

	// controlPlaneStartAttempts 是「探端口 → 起进程」的竞态重试次数。
	controlPlaneStartAttempts = 3
	// requestTimeout 是单请求上限：压测端点都是短请求，10s 足够，且能及时暴露卡死而非挂住整档。
	requestTimeout = 10 * time.Second
	// observationTimeout 是旁路观测请求的上限（观测失败不影响主流程）。
	observationTimeout = 10 * time.Second
	// metricsReadLimit 是 /metrics 响应读取上限（旁路观测只要摘要，不需要全量时序）。
	metricsReadLimit = 8 << 20
	// setupTimeout 是前置动作（登录后的注册 / 预检）的单次请求上限。
	setupTimeout = 30 * time.Second
	// soakBucketInterval 是 soak 的分段采样间隔（逐分钟采样）。
	soakBucketInterval = time.Minute
)

// TestLoadHTTPControlPlane 编排整套压测：分级并发 + soak + 报告。
// 缺凭据时跳过（与既有 e2e 用例的 requireEnv 惯例一致）。
func TestLoadHTTPControlPlane(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")

	levels := parseLevels(t, envOr("LOADTEST_LEVELS", defaultLevels))
	levelDuration := durationEnv(t, "LOADTEST_LEVEL_DURATION", defaultLevelDuration)
	soakConcurrent := intEnv(t, "LOADTEST_SOAK_CONCURRENCY", defaultSoakConcurrency)
	soakDuration := durationEnv(t, "LOADTEST_SOAK_DURATION", defaultSoakDuration)
	agentServers := intEnv(t, "LOADTEST_AGENT_SERVERS", defaultAgentServers)
	cpLogLevel := envOr("LOADTEST_CP_LOG_LEVEL", defaultCPLogLevel)
	maxConcurrent := max(slices.Max(levels), soakConcurrent)

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	workDir := filepath.Join(repoRoot, ".tmp", "loadtest")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("创建压测工作目录失败：%v", err)
	}
	removeStaleDB(t, workDir)

	t.Log("== 构建控制面二进制 ==")
	bin, err := harness.BuildBeacon(repoRoot)
	if err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}

	t.Log("== 起控制面（独立 sqlite + 动态端口）==")
	cp, baseURL := startControlPlane(t, harness.ControlPlaneConfig{
		BinPath: bin, RepoRoot: repoRoot,
		DBDriver: "sqlite", DBDSN: filepath.Join(workDir, sqliteDBName),
		AdminPassword: adminPass, AuthSecret: authSecret, BootstrapToken: bootstrapToken,
		LogPrefix: logPrefix,
		ExtraEnv:  map[string]string{"BEACON_LOG_LEVEL": cpLogLevel},
	}, controlPlaneStartAttempts)
	harness.CleanupControlPlane(t, cp)
	stdoutPath, stderrPath := cp.LogPaths()
	t.Logf("控制面已就绪：%s（日志：%s / %s）", baseURL, stdoutPath, stderrPath)

	token, err := harness.Login(baseURL, adminUser, adminPass, cp)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}

	client := newLoadClient(maxConcurrent)
	defer client.CloseIdleConnections()
	version := fetchBinaryVersion(t, client, baseURL, token)

	t.Logf("== 注册 %d 个虚拟 agent（环境 %s）==", agentServers, agentNamespace)
	serverIDs := registerVirtualAgents(t, client, baseURL, agentServers)

	scenarioCfg := scenarioConfig{
		BaseURL: baseURL, AdminToken: token,
		BootstrapToken: bootstrapToken, Namespace: agentNamespace, ServerIDs: serverIDs,
	}
	scenarios := buildScenarioSet(scenarioCfg)

	// v2 agent 数据面（上报 / 调度 / 连接 / 消息）是生产请求量最大的一面，此前零覆盖。
	// 引导顺序：建 namespace → 注册 proxy 身份 → 审批成 active，缺一步上报即 403 agent_not_confirmed。
	v2 := bootstrapV2Identity(t, baseURL, token, v2Namespace, v2Servers, cp)
	scenarios.appendScenarios(buildV2Scenarios(scenarioCfg, v2))
	// 写路径默认不纳入：admin 写逐条落审计，会显著放大 DB 写入并污染 audit_log，
	// 与「压出来的应是接口能力而非审计写入能力」相悖。确认要测写争用时显式开启。
	writeEnabled := os.Getenv("LOADTEST_ENABLE_WRITE_SCENARIOS") == "1"
	if writeEnabled {
		scenarios.appendScenarios(buildWriteScenarios(scenarioCfg, v2))
		t.Logf("已按 LOADTEST_ENABLE_WRITE_SCENARIOS=1 纳入写路径场景（会逐条落审计）")
	}
	probeScenarios(t, client, scenarios)

	observations := observe(t, client, baseURL, token, "基线")

	results := make([]*loadResult, 0, len(levels))
	for _, concurrent := range levels {
		label := fmt.Sprintf("并发 %d / %s", concurrent, levelDuration)
		t.Logf("== 压测档：%s ==", label)
		res, err := runLoad(context.Background(), &driverConfig{
			label: label, client: client, scenarios: scenarios,
			concurrent: concurrent, duration: levelDuration,
			requestTimeout: requestTimeout, guard: cp,
		})
		if err != nil {
			t.Fatalf("压测档 %s 失败：%v", label, err)
		}
		requireMeasured(t, res)
		results = append(results, res)
		t.Logf("档位完成：%s 请求 %d，成功 %d，失败 %d，RPS %.1f，p95 %.2fms",
			label, res.TotalRequests, res.Success, res.Failed, res.RPS, res.Latency.P95Ms)
		observations = append(observations, observe(t, client, baseURL, token, label)...)
	}

	soakLabel := fmt.Sprintf("soak 并发 %d / %s", soakConcurrent, soakDuration)
	t.Logf("== soak：%s（逐分钟采样）==", soakLabel)
	soakResult, err := runLoad(context.Background(), &driverConfig{
		label: soakLabel, client: client, scenarios: scenarios,
		concurrent: soakConcurrent, duration: soakDuration,
		requestTimeout: requestTimeout, bucketInterval: soakBucketInterval, guard: cp,
	})
	if err != nil {
		t.Fatalf("soak 失败：%v", err)
	}
	requireMeasured(t, soakResult)
	observations = append(observations, observe(t, client, baseURL, token, "soak 结束")...)

	// —— 挂起型端点探测与上报背压探测（口径与 RPS 型压测不同，单独成节）——
	suspended := runSuspendedProbes(t, baseURL, bootstrapToken, token, serverIDs, soakConcurrent)

	ingestRounds := intEnv(t, "LOADTEST_INGEST_ROUNDS", defaultIngestRounds)
	ingestBatch := intEnv(t, "LOADTEST_INGEST_BATCH", defaultIngestBatch)
	ingestConc := intEnv(t, "LOADTEST_INGEST_CONCURRENCY", defaultIngestConcurrency)
	t.Logf("== v2 上报通道背压探测：%d 轮 × 批量 %d × 并发 %d ==", ingestRounds, ingestBatch, ingestConc)
	ingestStarted := time.Now()
	ingestRes := probeIngestQueueHeadroom(t, client, baseURL, v2, cp, ingestRounds, ingestBatch, ingestConc)
	if ingestRes.Total == 0 {
		t.Logf("上报背压探测一条都没发出去：状态码分布 %v（可能身份未 active 或端点不可达）", ingestRes.StatusCounts)
	}
	ingest := ingestProbeReport{
		URL:             baseURL + "/beacon/v2/agent/connections/batch",
		Requests:        ingestRes.Total,
		BatchSize:       ingestBatch,
		Concurrency:     ingestConc,
		StatusCounts:    ingestRes.StatusCounts,
		SawBackpressure: ingestRes.SawBackpressure,
		SawUnavailable:  ingestRes.SawUnavailable,
		DurationMs:      time.Since(ingestStarted).Milliseconds(),
	}

	rep := &report{
		GeneratedAt: time.Now().Format(time.RFC3339), BaseURL: baseURL, RepoRoot: repoRoot,
		BinaryVersion: version,
		GoVersion:     runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH,
		Scenarios:     scenarios.rows(), Levels: results,
		Soak: &soakReport{
			Concurrent: soakConcurrent, DurationMs: soakResult.DurationMs,
			BucketMs: soakBucketInterval.Milliseconds(),
			Total:    soakResult, Segments: soakResult.Buckets,
		},
		Suspended:    suspended,
		IngestProbe:  &ingest,
		Observations: observations,
		Notes:        buildNotes(cpLogLevel, agentServers, levelDuration, soakDuration),
	}
	mdPath, jsonPath, err := rep.write(workDir, time.Now().Format("20060102-150405"))
	if err != nil {
		t.Fatalf("写压测报告失败：%v", err)
	}
	for _, line := range rep.summaryLines() {
		t.Log(line)
	}
	t.Logf("报告已写出：%s", mdPath)
	t.Logf("JSON 报告：%s", jsonPath)
}

// runSuspendedProbes 跑长轮询与 SSE 两个挂起探测，转成报告结构。
//
// 为什么单独跑、不并进 worker 池：挂起型端点的正确指标是「挂起容量 / 唤醒时延」，
// 而一挂就是 30s。混进 RPS 型压测会把分位数彻底搅乱（挂起记录把 p99 拉到几十秒），
// 且「挂 16 条连接」与「打满 128 并发」是两种不同的资源压力，不能放在同一档里比。
func runSuspendedProbes(t *testing.T, baseURL, agentToken, adminToken string, serverIDs []string, held int) []suspendedReport {
	t.Helper()
	if held < 1 {
		held = 1
	}
	if len(serverIDs) == 0 {
		t.Fatalf("没有已注册的虚拟 agent，无法构造挂起探测目标")
	}
	base := suspendedConfig{
		BaseURL: baseURL, Token: agentToken, AdminToken: adminToken,
		Held: held, Duration: suspendedProbeDuration, WakeAt: suspendedProbeWakeAt,
	}
	var out []suspendedReport

	longTarget := suspendedTarget(baseURL, agentNamespace, serverIDs[0])
	longCfg := base
	longCfg.Target = longTarget
	t.Logf("== 长轮询挂起探测：Held=%d Duration=%s WakeAt=%s ==", held, suspendedProbeDuration, suspendedProbeWakeAt)
	t.Logf("长轮询目标：%s", longTarget)
	longRes, err := runSuspendedProbe(context.Background(), longCfg)
	if err != nil {
		t.Fatalf("长轮询挂起探测失败：%v", err)
	}
	out = append(out, toSuspendedReport("长轮询", longTarget, longRes))

	sseTargetURL := sseTarget(baseURL, agentNamespace, serverIDs[0])
	sseCfg := base
	sseCfg.Target = sseTargetURL
	sseCfg.WakeAt = suspendedProbeWakeAt
	t.Logf("== SSE 挂起探测：Held=%d Duration=%s ==", held, sseProbeDuration)
	sseRes, err := runSSEProbe(context.Background(), sseCfg)
	if err != nil {
		t.Fatalf("SSE 挂起探测失败：%v", err)
	}
	out = append(out, toSuspendedReport("SSE", sseTargetURL, sseRes))
	return out
}

// toSuspendedReport 把探测结果转成报告结构（探测里保留的注释差异在这里丢弃，只留可结论的数字）。
func toSuspendedReport(kind, url string, res *suspendedResult) suspendedReport {
	return suspendedReport{
		Kind: kind, URL: url,
		Held: res.Held, Connected: res.Connected,
		CompletedByWake: res.CompletedByWake, CompletedByTimeout: res.CompletedByTimeout,
		Errors: res.Errors, ErrorKinds: res.ErrorKinds,
		HoldLatencyMs: res.HoldLatency, WakeLatencyMs: res.WakeLatency,
		SuspendedObserved: res.SuspendedObserved,
	}
}

// requireEnv 读必需环境变量，缺失即跳过（与既有 e2e 用例一致：只在显式注入密钥时运行）。
func requireEnv(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Skipf("跳过：缺少必需环境变量 %s（仅在显式 -tags \"e2e loadtest\" 且注入密钥时运行）", key)
	}
	return value
}

// envOr 读环境变量，缺失取默认值。
func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// parseLevels 解析并发档位列表（逗号分隔的正整数），空项跳过、非法值直接失败。
func parseLevels(t *testing.T, raw string) []int {
	t.Helper()
	parts := strings.Split(raw, ",")
	levels := make([]int, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		concurrent, err := strconv.Atoi(value)
		if err != nil || concurrent < 1 {
			t.Fatalf("并发档位 %q 非法（应为 ≥1 的整数，列表形如 1,8,32,128）", value)
		}
		levels = append(levels, concurrent)
	}
	if len(levels) == 0 {
		t.Fatalf("并发档位列表为空（LOADTEST_LEVELS=%q）", raw)
	}
	return levels
}

// durationEnv 读时长类环境变量（如 30s / 5m），缺失取默认值，非法直接失败。
func durationEnv(t *testing.T, key, fallback string) time.Duration {
	t.Helper()
	raw := envOr(key, fallback)
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		t.Fatalf("环境变量 %s=%q 非法（应为正时长，如 30s / 5m）", key, raw)
	}
	return value
}

// intEnv 读正整数类环境变量，缺失取默认值，非法直接失败。
func intEnv(t *testing.T, key string, fallback int) int {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		t.Fatalf("环境变量 %s=%q 非法（应为 ≥1 的整数）", key, raw)
	}
	return value
}

// newLoadClient 构造压测专用客户端：独立 Transport、连接池按最大并发调优。
// 不复用 harness 内部的 http.DefaultClient——它的连接池是给一次性探测请求准备的
// （DefaultTransport 的 MaxIdleConnsPerHost 默认只有 2），128 并发下会退化成频繁建连，
// 测出来的就不是控制面的处理能力而是本机的 TCP 握手速率。
func newLoadClient(maxConcurrent int) *http.Client {
	transport := &http.Transport{
		// Proxy 显式置 nil：忽略环境里的 HTTP_PROXY / NO_PROXY，避免本地压测被绕到代理上。
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          maxConcurrent * 2,
		MaxIdleConnsPerHost:   maxConcurrent,
		IdleConnTimeout:       90 * time.Second,
		DisableCompression:    true,
		ForceAttemptHTTP2:     false,
		ResponseHeaderTimeout: requestTimeout,
	}
	// 不设 Client.Timeout：单请求上限由每次请求的 context 负责（Client.Timeout 会把「读响应体」
	// 也算进去，容易在压测里制造难解释的超时）。
	return &http.Client{Transport: transport}
}

// startControlPlane 用动态端口起控制面并返回进程与地址。
// harness 的 HTTPAddrFromURL 只做「地址 → bind 串」转换、没有冲突兜底，压测与既有 e2e 用例并存时
// 固定端口容易撞车；而「探空闲端口 → 关闭监听 → 进程 bind」之间存在微小竞态，故失败时换端口重试。
func startControlPlane(t *testing.T, cfg harness.ControlPlaneConfig, attempts int) (*harness.ControlPlane, string) {
	t.Helper()
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		cfg.BaseURL = freeBaseURL(t)
		cp, err := harness.StartControlPlane(cfg)
		if err == nil {
			return cp, cfg.BaseURL
		}
		lastErr = err
		t.Logf("第 %d 次起控制面失败（端口 %s），换端口重试：%v", attempt, cfg.BaseURL, err)
	}
	t.Fatalf("起控制面失败（已重试 %d 次）：%v", attempts, lastErr)
	return nil, ""
}

// freeBaseURL 探一个本机空闲端口并返回对应的控制面地址（关闭监听后交还给进程 bind）。
func freeBaseURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("探测空闲端口失败：%v", err)
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		t.Fatalf("探测到的地址不是 TCP：%T", listener.Addr())
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("释放探测端口失败：%v", err)
	}
	return "http://127.0.0.1:" + strconv.Itoa(addr.Port)
}

// removeStaleDB 清掉上一轮遗留的 sqlite 文件（含 WAL / SHM 与同目录归档库），保证每轮从干净库开始。
func removeStaleDB(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.db*"))
	if err != nil {
		t.Fatalf("枚举遗留库文件失败：%v", err)
	}
	for _, path := range matches {
		if err := os.Remove(path); err != nil {
			t.Fatalf("删除遗留库文件 %s 失败：%v", path, err)
		}
	}
}

// registerVirtualAgents 经 v1 数据面挂载端点注册一批虚拟 serverId（心跳场景的前置）。
// 这一步会为每个 serverId 写一条 instance.register 审计（控制面既有行为，无法旁路），
// 故只做一次性前置、不进压测循环——否则测出来的就是审计写入能力而非接口能力。
func registerVirtualAgents(t *testing.T, client *http.Client, baseURL string, count int) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	ids := make([]string, 0, count)
	for i := range count {
		serverID := "loadtest-agent-" + strconv.Itoa(i)
		status, snippet, err := registerOne(ctx, client, baseURL, serverID, i)
		if err != nil {
			t.Fatalf("注册虚拟 agent %s 失败：%v", serverID, err)
		}
		if status != http.StatusOK {
			t.Fatalf("注册虚拟 agent %s 期望 %d，实际 %d；响应片段：%s",
				serverID, http.StatusOK, status, snippet)
		}
		ids = append(ids, serverID)
	}
	return ids
}

// registerOne 注册单个虚拟 agent，返回状态码与响应片段。
func registerOne(ctx context.Context, client *http.Client, baseURL, serverID string, seq int) (int, string, error) {
	body, err := json.Marshal(map[string]any{
		"namespace": agentNamespace, "serverId": serverID, "role": "bukkit",
		"address": "127.0.0.1:" + strconv.Itoa(20000+seq), "version": "loadtest",
	})
	if err != nil {
		return 0, "", fmt.Errorf("编码注册请求体失败：%w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/beacon/v1/agent/register", bytes.NewReader(body))
	if err != nil {
		return 0, "", fmt.Errorf("构造注册请求失败：%w", err)
	}
	req.Header.Set("X-Beacon-Token", bootstrapToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("注册请求失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	snippet, err := io.ReadAll(io.LimitReader(resp.Body, probeSnippetLimit))
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("读取注册响应失败：%w", err)
	}
	return resp.StatusCode, string(snippet), nil
}

// probeSnippetLimit 是预检 / 注册失败时回带的响应片段上限（够定位问题，不刷屏）。
const probeSnippetLimit = 512

// probeScenarios 对每个场景各打一发预检请求，状态码与期望不符立即失败。
// 压测报告只有在端点真的按预期应答时才有意义，否则只是一份「404 吞吐量」。
func probeScenarios(t *testing.T, client *http.Client, set *scenarioSet) {
	t.Helper()
	for i := range set.items {
		item := &set.items[i]
		status, snippet, err := probeOne(client, item)
		if err != nil {
			t.Fatalf("预检 %s（%s %s）失败：%v", item.Name, item.Method, item.Path, err)
		}
		if status != item.WantStatus {
			t.Fatalf("预检 %s（%s %s）期望 %d，实际 %d；响应片段：%s",
				item.Name, item.Method, item.Path, item.WantStatus, status, snippet)
		}
		t.Logf("预检通过：%-16s %s %-32s → %d", item.Name, item.Method, item.Path, status)
	}
}

// probeOne 发一发预检请求，返回状态码与响应片段。
func probeOne(client *http.Client, item *scenario) (int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()
	var body io.Reader
	if len(item.bodies) > 0 {
		body = bytes.NewReader(item.bodies[0])
	}
	req, err := http.NewRequestWithContext(ctx, item.Method, item.URL, body)
	if err != nil {
		return 0, "", fmt.Errorf("构造预检请求失败：%w", err)
	}
	for _, h := range item.headers {
		req.Header.Set(h.key, h.value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("预检请求失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	snippet, err := io.ReadAll(io.LimitReader(resp.Body, probeSnippetLimit))
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("读取预检响应失败：%w", err)
	}
	return resp.StatusCode, string(snippet), nil
}

// requireMeasured 守住「压测真的压到了」：零请求或零成功说明 harness / 环境坏了，
// 这种报告没有解释价值，直接失败比留下好看的空表更诚实。
func requireMeasured(t *testing.T, res *loadResult) {
	t.Helper()
	if res.TotalRequests == 0 {
		t.Fatalf("档位 %s 未产生任何请求", res.Label)
	}
	if res.Success == 0 {
		t.Fatalf("档位 %s 无一成功请求（失败 %d，网络错误 %d）", res.Label, res.Failed, res.NetworkErrors)
	}
}

// buildNotes 汇总报告口径与已知取舍，避免数字被误读。
func buildNotes(cpLogLevel string, agentServers int, levelDuration, soakDuration time.Duration) []string {
	return []string{
		"只压 GET 与幂等写（agent 心跳）：admin 写端点经 auditWriteMiddleware 逐条落审计，压它们测出来的是审计写入能力而非接口能力，故一律不纳入。",
		fmt.Sprintf("分位数口径：样本排序后线性插值（非桶近似）；单档最多保留 %d 个样本，带时间桶的 soak 按桶均分该预算（保证每段都有分位数可读），Sampled 列给出分位数的实际覆盖请求数。", maxLatencySamples),
		fmt.Sprintf("端点级分位数口径：端点明细表的时延列来自每个端点各自独立的采样配额（上限 %d 个样本/端点），与整体分位数的 %d 样本预算互不挤占；表内「采样」列是该端点进入分位统计的请求数，小于「请求」列即表示分位数未覆盖全部请求（超配额后继续发压与计数，只是不再采样）。", maxEndpointLatencySamples, maxLatencySamples),
		"时延为端到端耗时（含响应体读尽与连接复用），RPS 用档位实际跨度计算，不含档间旁路观测与准备时间。",
		fmt.Sprintf("虚拟 agent（%d 个）只活在控制面内存注册表，不写 server 表；注册前置会写 %d 条 instance.register 审计（一次性，不参与压测循环）。", agentServers, agentServers),
		fmt.Sprintf("控制面日志级别 = %s：压测期抑制逐请求 INFO 访问日志，避免把 .tmp 写爆并让磁盘 I/O 混进结果；需要完整访问日志时用 LOADTEST_CP_LOG_LEVEL=INFO。", cpLogLevel),
		fmt.Sprintf("档位时长 %s、soak 时长 %s；两者均可经环境变量缩短以便冒烟（LOADTEST_LEVEL_DURATION / LOADTEST_SOAK_DURATION）。", levelDuration, soakDuration),
		"分位数口径：样本排序后线性插值（非桶近似）；单档最多保留 1000000 个样本，带时间桶的 soak 按桶均分该预算（保证每段都有分位数可读），Sampled 列给出分位数的实际覆盖请求数；每个端点另有 200000 样本的独立配额（采样列 < 请求列即未全覆盖）。",
		"v2 agent 数据面（metrics/report、schedule/*）需身份审批成 active 后才会被放行，故压测前置会真实建 namespace 与身份；连接/消息上报通道的有界队列（429）与等 DB 连接超时（503）由本批的背压探测单独验证。",
		"长轮询与 SSE 是挂起型端点，单独成节、不用 RPS 口径：指标是「服务端观测到的并发挂起数」与「变更发出到连接返回的唤醒时延」。唤醒时延用的是 global 层新建配置项，走 notifier → Hub 唤醒该 namespace 全部 waiter；若唤醒语义落在窄 scope 上，唤醒条数会少于挂起数，读表时以唤醒返回列为准。",
		"本报告不含逐请求明细：压测只输出聚合结果，避免把测试输出淹没。",
	}
}

// observabilitySnapshot 只取压测关心的自观测字段：DB 连接池等待、注册表规模、长轮询挂起。
type observabilitySnapshot struct {
	DBPool struct {
		MaxOpenConnections int   `json:"maxOpenConnections"`
		OpenConnections    int   `json:"openConnections"`
		InUse              int   `json:"inUse"`
		Idle               int   `json:"idle"`
		WaitCount          int64 `json:"waitCount"`
		WaitDurationMs     int64 `json:"waitDurationMs"`
	} `json:"dbPool"`
	RegistryTotal int `json:"registryTotal"`
	Longpoll      struct {
		Total int `json:"total"`
	} `json:"longpoll"`
}

// observe 采集一次旁路观测：/metrics 摘要 + 自观测快照。
// 尽力而为：任一失败只记进观测列表，不影响压测主流程。
func observe(t *testing.T, client *http.Client, baseURL, token, phase string) []observation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), observationTimeout)
	defer cancel()
	observations := make([]observation, 0, 2)
	observations = append(observations, fetchMetricsSummary(ctx, client, baseURL, phase))
	observations = append(observations, fetchObservability(ctx, client, baseURL, token, phase))
	return observations
}

// fetchMetricsSummary 抓一次 /metrics 并汇总 HTTP RED 指标。
func fetchMetricsSummary(ctx context.Context, client *http.Client, baseURL, phase string) observation {
	obs := newObservation(phase, "GET /metrics")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/metrics", nil)
	if err != nil {
		obs.Error = err.Error()
		return obs
	}
	resp, err := client.Do(req)
	if err != nil {
		obs.Error = err.Error()
		return obs
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, metricsReadLimit))
	if err != nil {
		obs.Status = resp.StatusCode
		obs.Error = err.Error()
		return obs
	}
	obs.Status = resp.StatusCode
	if resp.StatusCode != http.StatusOK {
		obs.Error = "状态码非 200"
		return obs
	}
	obs.Detail = summarizeMetrics(string(raw))
	return obs
}

// fetchObservability 抓一次自观测快照（连接池 / 注册表 / 长轮询）。
func fetchObservability(ctx context.Context, client *http.Client, baseURL, token, phase string) observation {
	obs := newObservation(phase, "GET /admin/v1/system/observability")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/admin/v1/system/observability", nil)
	if err != nil {
		obs.Error = err.Error()
		return obs
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		obs.Error = err.Error()
		return obs
	}
	defer func() { _ = resp.Body.Close() }()
	obs.Status = resp.StatusCode
	if resp.StatusCode != http.StatusOK {
		obs.Error = "状态码非 200"
		return obs
	}
	var snapshot observabilitySnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		obs.Error = "解析自观测响应失败：" + err.Error()
		return obs
	}
	obs.Detail = fmt.Sprintf(
		"DB 连接池 打开=%d/上限=%d 使用中=%d 空闲=%d 等待次数=%d 等待耗时=%dms；注册表实例=%d；长轮询挂起=%d",
		snapshot.DBPool.OpenConnections, snapshot.DBPool.MaxOpenConnections,
		snapshot.DBPool.InUse, snapshot.DBPool.Idle,
		snapshot.DBPool.WaitCount, snapshot.DBPool.WaitDurationMs,
		snapshot.RegistryTotal, snapshot.Longpoll.Total)
	return obs
}

// newObservation 构造一条带时间戳的观测记录。
func newObservation(phase, target string) observation {
	return observation{At: time.Now().UTC().Format(time.RFC3339), Phase: phase, Target: target}
}

// fetchBinaryVersion 从 /admin/v1/system/status 取控制面版本（失败只记空串，不影响压测）。
func fetchBinaryVersion(t *testing.T, client *http.Client, baseURL, token string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), observationTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/admin/v1/system/status", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Logf("取控制面版本失败（不影响压测）：%v", err)
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var status struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return ""
	}
	return status.Version
}

// summarizeMetrics 从 Prometheus 文本里汇总 HTTP RED 指标：请求总量 + 主要路由 + 时延直方图样本数。
// 只做字符串解析（不引第三方 Prometheus 解析库）：这里要的是「旁路交叉验证」，不是精确时序。
func summarizeMetrics(body string) string {
	const (
		requestMetric  = "beacon_http_requests_total"
		durationMetric = "beacon_http_request_duration_seconds_count"
	)
	byRoute := make(map[string]int)
	total := 0
	durationSamples := 0
	for line := range strings.SplitSeq(body, "\n") {
		name, labels, value, ok := parseSample(line)
		if !ok {
			continue
		}
		switch name {
		case requestMetric:
			byRoute[labelValue(labels, "route")] += value
			total += value
		case durationMetric:
			durationSamples += value
		}
	}
	if total == 0 {
		return "未发现 beacon_http_requests_total 样本"
	}
	return fmt.Sprintf("HTTP 请求累计=%d（时延直方图样本=%d）；主要路由：%s",
		total, durationSamples, topRoutes(byRoute, 4))
}

// parseSample 解析一行 Prometheus 样本 `name{labels} value`；注释 / 空行 / 格式不符返回 false。
func parseSample(line string) (string, string, int, bool) {
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", 0, false
	}
	spaceIdx := strings.LastIndexByte(line, ' ')
	if spaceIdx < 0 {
		return "", "", 0, false
	}
	value, err := strconv.ParseFloat(line[spaceIdx+1:], 64)
	if err != nil {
		return "", "", 0, false
	}
	nameAndLabels := line[:spaceIdx]
	name, labels := nameAndLabels, ""
	if openIdx := strings.IndexByte(nameAndLabels, '{'); openIdx >= 0 {
		if !strings.HasSuffix(nameAndLabels, "}") {
			return "", "", 0, false
		}
		name = nameAndLabels[:openIdx]
		labels = nameAndLabels[openIdx+1 : len(nameAndLabels)-1]
	}
	return name, labels, int(value), true
}

// labelValue 从标签串里取指定标签的值（未命中返回空串）。
func labelValue(labels, key string) string {
	prefix := key + "=\""
	idx := strings.Index(labels, prefix)
	if idx < 0 {
		return ""
	}
	rest := labels[idx+len(prefix):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// topRoutes 把路由计数按次数降序取前 limit 个，渲染成 `route=次数` 列表。
func topRoutes(byRoute map[string]int, limit int) string {
	routes := make([]string, 0, len(byRoute))
	for route := range byRoute {
		routes = append(routes, route)
	}
	slices.SortFunc(routes, func(a, b string) int {
		if byRoute[a] != byRoute[b] {
			return byRoute[b] - byRoute[a]
		}
		return strings.Compare(a, b)
	})
	if len(routes) > limit {
		routes = routes[:limit]
	}
	parts := make([]string, 0, len(routes))
	for _, route := range routes {
		parts = append(parts, route+"="+strconv.Itoa(byRoute[route]))
	}
	return strings.Join(parts, ", ")
}
