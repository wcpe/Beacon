//go:build e2e && loadtest

// 控制面「1000+ 子服规模」容量实测（本文件回答的是规模问题，不是接口功能问题）。
//
// 要回答的三个问题：
//  1. 64 台 → 1000 台（15.6 倍）时，同口径下的 RPS / 分位数如何变化——线性、次线性还是出现拐点？
//  2. 若出现拐点，瓶颈在控制面内部（DB 连接池排队 / 注册表锁 / GC）还是宿主机 CPU 饱和？
//  3. 1000 台规模下控制面能稳定承载多少心跳 QPS / 上报 QPS，余量多少，结论的适用边界是什么。
//
// 方法论上必须先立住「同口径」这一条，否则一切数字都不可比：
//   - 两个规模各起一个**独立控制面进程 + 全新 sqlite 库**，在同一测试进程内前后紧邻执行，
//     使宿主机状态尽量一致；
//   - 每个档位都记录当时的 /proc/loadavg、核数、控制面进程 RSS/VmHWM/CPU 核数占用、
//     以及压测端自身 CPU 占用——上一轮实测同一档位在 load 15 与 load 36 下差了 2.3 倍
//     （9918 RPS vs 4303 RPS），不记环境口径的对照表会把环境差异读成系统退化；
//   - 同时抓控制面 /metrics（进程内直方图分位数、goroutine 数、GC 次数、RSS）与
//     /admin/v1/system/observability（DB 连接池等待次数增量）——这两者是把
//     「宿主机 CPU 饱和」与「控制面内部池排队」区分开的硬证据：
//     CPU 饱和时池不排队（WaitCount 增量 = 0），池排队时进程 CPU 通常没打满。
//
// 运行：
//
//	export E2E_ADMIN_PASS='...' E2E_AUTH_SECRET='...'
//	go test -tags "e2e loadtest" -timeout=90m -v -run '^TestControlPlaneScale' ./apps/server/test/loadtest/
//
// 环境变量（均有默认值）：
//
//	LOADTEST_SCALE_SIZES           对照的注册规模列表，默认 64,1000
//	LOADTEST_SCALE_LEVELS          v1 核心负载并发档位，默认 1,8,32,128
//	LOADTEST_SCALE_FAMILY_LEVELS   心跳 / v2 单类负载并发档位，默认 32,128
//	LOADTEST_SCALE_LEVEL_DURATION  每档时长，默认 20s
//	LOADTEST_SCALE_REGISTER_CONCURRENCY  注册并发度，默认 16
//	LOADTEST_REGISTER_CONCURRENCY  既有主流程的注册并发度（本文件不读，仅作说明保留）
//	LOADTEST_SCALE_SKIP_V2         置 1 跳过 v2 上报负载（v2 前置要建身份，耗时较长）
package loadtest_e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	// scaleDBName 是本用例专用的 sqlite 库名。刻意不带任何既有前缀：
	// 并行开发期的其它用例（backpressure_429 / midnight / 主压测）各有自己的库，
	// 共用同名库会互相删文件、互相污染。
	scaleDBName = "beacon-scale1000.db"
	// scaleLogPrefix 是控制面日志前缀（与库名同源，便于把日志与库对上）。
	scaleLogPrefix = "beacon-scale"
	// scaleWorkDirName 是工作目录名（在仓库 .tmp 下）。
	scaleWorkDirName = "loadtest-scale"
	// scaleSamplingPeriod 是资源采样周期。1s 对 20~30s 的档位足够密，
	// 又不会让 /proc 读取本身成为可测量的干扰。
	scaleSamplingPeriod = time.Second
)

// scaleConfig 是一次规模压测的运行参数（全部可由环境变量覆盖）。
type scaleConfig struct {
	Sizes         []int
	Levels        []int
	FamilyLevels  []int
	LevelDuration time.Duration
	RegisterConc  int
	SkipV2        bool
	// IncludeIsolation 是否加测「纯内存」与「纯DB读」两个隔离族。
	// 默认开启：它们是把退化拆成「HTTP/CPU」与「DB 往返」两块的唯一手段，关掉就只能给笼统结论。
	IncludeIsolation bool
	AdminPass        string
	AuthSecret       string
	ControlPlaneBin  string
	RepoRoot         string
}

// scalePoint 是「某个规模 × 某个负载族 × 某个并发档」的一次测量，含数据与环境口径。
// 数据与口径必须绑在同一个结构里：分开存会在报告阶段配错，而配错的对照表比没有更糟。
type scalePoint struct {
	Scale      int             `json:"scale"`
	Family     string          `json:"family"`
	Concurrent int             `json:"concurrent"`
	Label      string          `json:"label"`
	Result     *loadResult     `json:"result"`
	Resources  resourceSummary `json:"resources"`
	// PoolWait 是本次档位内 DB 连接池的等待增量（after - before）。>0 即「有请求没立刻拿到连接」。
	PoolWait poolDelta `json:"poolWait"`
	// ServerP95Ms / ServerP99Ms 是控制面进程内直方图算出的分位（毫秒），按 family 关心的路由取。
	// 取的是「本档位前后两次 /metrics 快照的直方图增量」，不是进程累计值——见 histogramDelta 注释。
	ServerP95Ms      float64 `json:"serverP95Ms"`
	ServerP99Ms      float64 `json:"serverP99Ms"`
	ServerRoute      string  `json:"serverRoute"`
	ServerSamples    int64   `json:"serverSamples"`
	ServerDeltaValid bool    `json:"serverDeltaValid"`
	// Goroutines / RSSMiB / GC 次数是档位结束时控制面的即时读数，用于识别 GC / 内存压力。
	// GCCount 是进程累计值，GCPerSec 是档位内每秒 GC 次数（用它判断 GC 是否成为干扰）。
	Goroutines   int64   `json:"goroutines"`
	RSSMiB       float64 `json:"rssMiB"`
	GCCount      int64   `json:"gcCount"`
	GCCountDelta int64   `json:"gcCountDelta"`
	GCPerSec     float64 `json:"gcPerSec"`
	ClientCores  float64 `json:"clientCores"`
	ServerCores  float64 `json:"serverCores"`
	Error        string  `json:"error,omitempty"`
}

// scaleRegistration 是某个规模下注册阶段的实测结果。
type scaleRegistration struct {
	Scale       int             `json:"scale"`
	Requested   int             `json:"requested"`
	Succeeded   int             `json:"succeeded"`
	Failed      int             `json:"failed"`
	ElapsedMs   int64           `json:"elapsedMs"`
	PerSecond   float64         `json:"perSecond"`
	Concurrency int             `json:"concurrency"`
	Failures    []string        `json:"failures,omitempty"`
	Resources   resourceSummary `json:"resources"`
	// RegistryTotalAfter 是注册后控制面自报的注册表规模（交叉验证注册真的生效）。
	RegistryTotalAfter int `json:"registryTotalAfter"`
}

// scaleRun 是单个注册规模下的一整套测量。
type scaleRun struct {
	Scale        int               `json:"scale"`
	Registration scaleRegistration `json:"registration"`
	Points       []scalePoint      `json:"points"`
	// BaselineLoad / AfterLoad 是该规模压测开始前与结束后的宿主机负载，用于核对整段是否稳定。
	BaselineLoad  loadAvg  `json:"baselineLoad"`
	AfterLoad     loadAvg  `json:"afterLoad"`
	BinaryVersion string   `json:"binaryVersion,omitempty"`
	CPLogPaths    []string `json:"cpLogPaths,omitempty"`
	Notes         []string `json:"notes,omitempty"`
}

// scaleReport 是规模压测的完整产物。
type scaleReport struct {
	GeneratedAt   string     `json:"generatedAt"`
	RepoRoot      string     `json:"repoRoot"`
	GoVersion     string     `json:"goVersion"`
	BinaryVersion string     `json:"binaryVersion"`
	CPUCores      int        `json:"cpuCores"`
	LevelDuration string     `json:"levelDuration"`
	Runs          []scaleRun `json:"runs"`
	// HostLoadNotes 记录压测期间本机既有负载（另一个后台测试）的如实说明。
	HostLoadNotes []string `json:"hostLoadNotes"`
}

// ---- 主用例 ----

// TestControlPlaneScaleComparison 跑「64 台 vs 1000 台」同口径对照 + 单类负载容量。
func TestControlPlaneScaleComparison(t *testing.T) {
	cfg := newScaleConfig(t)
	requireEnv(t, "E2E_ADMIN_PASS")
	requireEnv(t, "E2E_AUTH_SECRET")

	t.Logf("== 规模对照压测配置 ===")
	t.Logf("注册规模：%v；v1 并发档：%v；单类负载并发档：%v；每档时长：%s；注册并发度：%d",
		cfg.Sizes, cfg.Levels, cfg.FamilyLevels, cfg.LevelDuration, cfg.RegisterConc)
	t.Logf("宿主机：%d 核；当前负载：%+v", numCPU(), readLoadAvg())

	rep := &scaleReport{
		GeneratedAt: time.Now().Format(time.RFC3339), RepoRoot: cfg.RepoRoot,
		GoVersion: goVersionString(), CPUCores: numCPU(),
		LevelDuration: cfg.LevelDuration.String(),
		HostLoadNotes: []string{
			"本机同时有另一个后台测试长时间运行（跨午夜用例，绝大多数时间约 1 请求/分钟，近乎零负载）。",
			"每个档位都记录了当时的 /proc/loadavg，跨规模对照必须在负载口径接近时才有意义。",
			"注册阶段写 1000 条 instance.register 审计属一次性前置成本，不参与压测循环口径。",
		},
	}

	workDir := filepath.Join(cfg.RepoRoot, ".tmp", scaleWorkDirName)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("创建规模压测工作目录失败：%v", err)
	}

	t.Log("== 构建控制面二进制 ==")
	bin, err := harness.BuildBeacon(cfg.RepoRoot)
	if err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}
	cfg.ControlPlaneBin = bin

	for _, size := range cfg.Sizes {
		t.Logf("================ 注册规模 %d ================", size)
		run := runScaleOnce(t, cfg, workDir, size)
		rep.Runs = append(rep.Runs, run)
		if rep.BinaryVersion == "" {
			rep.BinaryVersion = run.BinaryVersion
		}
	}

	mdPath, jsonPath, err := rep.write(workDir, time.Now().Format("20060102-150405"))
	if err != nil {
		t.Fatalf("写规模压测报告失败：%v", err)
	}
	for _, line := range rep.summaryLines() {
		t.Log(line)
	}
	t.Logf("规模压测报告（Markdown）：%s", mdPath)
	t.Logf("规模压测报告（JSON）：%s", jsonPath)
}

// runScaleOnce 在独立控制面进程 + 全新库上完成一个规模的注册与全部档位测量。
func runScaleOnce(t *testing.T, cfg scaleConfig, workDir string, size int) scaleRun {
	t.Helper()
	run := scaleRun{Scale: size, BaselineLoad: readLoadAvg()}

	// 每个规模用独立的库文件：跨规模复用同一库会让「1000 规模的审计表更大」这种副作用
	// 混进对照结果，而本实验要隔离的自变量只有一个——注册表规模。
	dbPath := filepath.Join(workDir, fmt.Sprintf("beacon-scale-%d.db", size))
	removeStaleDBFile(t, dbPath)

	t.Logf("起控制面（库 %s）", dbPath)
	cp, baseURL := startControlPlane(t, harness.ControlPlaneConfig{
		BinPath: cfg.ControlPlaneBin, RepoRoot: cfg.RepoRoot,
		DBDriver: "sqlite", DBDSN: dbPath,
		AdminPassword: cfg.AdminPass, AuthSecret: cfg.AuthSecret, BootstrapToken: bootstrapToken,
		LogPrefix: fmt.Sprintf("%s-%d", scaleLogPrefix, size),
		ExtraEnv:  map[string]string{"BEACON_LOG_LEVEL": defaultCPLogLevel},
	}, controlPlaneStartAttempts)
	// 注意：不在每个规模结束时立刻 CleanupControlPlane，而是在本函数返回前显式 StopE，
	// 以保证 1000 台规模起第二个进程时第一个已完全退出（两个控制面同时跑会互相抢 CPU，
	// 把「规模差异」和「两个进程并发争抢」混在一起）。
	defer func() {
		if err := cp.StopE(); err != nil {
			t.Errorf("停止控制面失败：%v", err)
		}
	}()

	stdoutPath, stderrPath := cp.LogPaths()
	run.CPLogPaths = []string{stdoutPath, stderrPath}
	// 控制面 PID 直接取自进程句柄；取不到就退化为按二进制路径查找。
	// 本用例同一时刻只跑一个控制面，两种方式等价，但句柄是权威来源。
	cpPID := cp.PID()
	if cpPID == 0 {
		cpPID = waitControlPlanePID(cfg.ControlPlaneBin)
	}
	if cpPID > 0 {
		t.Logf("控制面 PID=%d，日志：%s / %s", cpPID, stdoutPath, stderrPath)
	} else {
		t.Logf("未能定位控制面 PID（资源口径将缺失进程级读数）；日志：%s / %s", stdoutPath, stderrPath)
	}

	token, err := harness.Login(baseURL, adminUser, cfg.AdminPass, cp)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}

	client := newLoadClient(max(slicesMax(cfg.Levels), slicesMax(cfg.FamilyLevels)))
	defer client.CloseIdleConnections()

	// 版本号取自控制面自报（同一二进制跨规模不变，取首次成功读数即可）。
	run.BinaryVersion = fetchBinaryVersion(t, client, baseURL, token)

	t.Logf("== 注册 %d 个虚拟 agent（并发度 %d）==", size, cfg.RegisterConc)
	regStarted := time.Now()
	regOutcome := registerVirtualAgentsConcurrent(t, client, baseURL, size, cfg.RegisterConc, cpPID, os.Getpid())
	run.Registration = scaleRegistration{
		Scale: size, Requested: regOutcome.Requested, Succeeded: regOutcome.Succeeded,
		Failed: len(regOutcome.Failures), ElapsedMs: regOutcome.ElapsedMs, PerSecond: regOutcome.RPS,
		Concurrency: cfg.RegisterConc, Failures: regOutcome.Failures, Resources: regOutcome.Resources,
	}
	t.Logf("注册完成：成功 %d / 请求 %d，失败 %d，耗时 %dms（%.1f 台/秒）",
		regOutcome.Succeeded, size, len(regOutcome.Failures), regOutcome.ElapsedMs, regOutcome.RPS)
	t.Logf("注册阶段宿主机负载：起点 load1=%.2f 峰值 load1=%.2f；控制面 CPU 均值 %.2f 核；压测端 CPU 均值 %.2f 核",
		regOutcome.Resources.Load1Start, regOutcome.Resources.Load1Max,
		regOutcome.Resources.Procs["控制面"].AvgCores, regOutcome.Resources.Procs["压测端"].AvgCores)
	if len(regOutcome.Failures) > 0 {
		// 失败明细只打前 5 条，避免刷屏；完整清单在报告 JSON 里。
		t.Errorf("注册失败 %d 台（前 5 条）：%s", len(regOutcome.Failures),
			strings.Join(firstNStrings(regOutcome.Failures, 5), "；"))
	}
	if regOutcome.Succeeded == 0 {
		t.Fatalf("规模 %d 一台都没注册成功，后续测量无意义", size)
	}
	_ = regStarted

	// 交叉验证注册真的进了控制面内存注册表（自观测的 registryTotal）。
	if snap, err := fetchObservabilityRaw(context.Background(), client, baseURL, token); err == nil {
		run.Registration.RegistryTotalAfter = snap.RegistryTotal
		t.Logf("控制面自报注册表规模 = %d（期望 ≈ %d）", snap.RegistryTotal, regOutcome.Succeeded)
		if snap.RegistryTotal < regOutcome.Succeeded {
			t.Errorf("控制面注册表规模 %d 小于注册成功数 %d，注册未全部生效",
				snap.RegistryTotal, regOutcome.Succeeded)
		}
	}

	serverIDs := regOutcome.IDs
	scenarioCfg := scenarioConfig{
		BaseURL: baseURL, AdminToken: token,
		BootstrapToken: bootstrapToken, Namespace: agentNamespace, ServerIDs: serverIDs,
	}
	full := buildScenarioSet(scenarioCfg)

	// v2 上报通道要真实建 namespace 与身份（含审批成 active），是写路径的一部分；
	// 它对规模的敏感性来自 DB 写入而非注册表规模，故单独成族，不和 v1 混在一起算。
	var v2 *v2Bootstrap
	if !cfg.SkipV2 {
		v2 = bootstrapV2Identity(t, baseURL, token, v2Namespace, v2Servers, cp)
	}
	var v2Set *scenarioSet
	if v2 != nil {
		v2Set = newScenarioSetFrom(buildV2Scenarios(scenarioCfg, v2))
	}

	v1Set := filterScenariosByGroup(full, groupMemoryRead, groupDBRead, groupAgentHeartbeat, groupMetricsScrape)
	hbSet := filterScenariosByName(full, "agent_heartbeat")
	readSet := filterScenariosByGroup(full, groupMemoryRead, groupDBRead)
	// 隔离族（只在开启时纳入）：把「HTTP 层 + Go 运行时」与「DB 往返」分开量。
	// 纯内存族的端点一次 DB 往返都不发（system_status 只读内存快照），故它给出的是
	// 「这台机器 + 这个 Go 进程」在零 DB 成本下的吞吐天花板；
	// 纯 DB 族只打一条走 DB 的只读端点。两者与心跳（内存写 + 2 次 DB 读）并排看，
	// 就能把退化拆成「HTTP/CPU 部分」与「每条 DB 往返的边际成本」两块，
	// 而不是笼统地说「控制面变慢了」。
	isoSet := filterScenariosByName(full, "system_status")
	dbOnlySet := filterScenariosByName(full, "list_servers")

	t.Logf("负载族场景数：v1 核心 %d；心跳 %d；只读 %d；v2 %d",
		len(v1Set.items), len(hbSet.items), len(readSet.items), v2SetItems(v2Set))

	// 档位前预检一次：报告只有在端点真的按预期应答时才有意义。
	probeScenarios(t, client, v1Set)

	// 并发档的先后顺序固定为「低 → 高」：从低档开始可以让首个档位落在冷启动期之外，
	// 也让「高档位才出现退化」这类结论不被前序档位的热状态掩盖。
	type familyPlan struct {
		name   string
		set    *scenarioSet
		levels []int
	}
	plans := []familyPlan{
		{name: "v1核心", set: v1Set, levels: cfg.Levels},
		{name: "心跳", set: hbSet, levels: cfg.FamilyLevels},
		{name: "只读", set: readSet, levels: cfg.FamilyLevels},
	}
	if cfg.IncludeIsolation {
		plans = append(plans,
			familyPlan{name: "纯内存", set: isoSet, levels: cfg.FamilyLevels},
			familyPlan{name: "纯DB读", set: dbOnlySet, levels: cfg.FamilyLevels},
		)
	}
	if v2Set != nil {
		plans = append(plans, familyPlan{name: "v2上报", set: v2Set, levels: cfg.FamilyLevels})
	}

	for _, plan := range plans {
		if plan.set == nil || len(plan.set.items) == 0 {
			continue
		}
		for _, concurrent := range plan.levels {
			point := measureScalePoint(t, cfg, cp, client, cpPID, plan.set, plan.name, concurrent, size, baseURL, token)
			run.Points = append(run.Points, point)
		}
	}

	run.AfterLoad = readLoadAvg()
	t.Logf("规模 %d 完成；结束后宿主机负载 load1=%.2f/%.2f/%.2f",
		size, run.AfterLoad.Load1, run.AfterLoad.Load5, run.AfterLoad.Load15)
	return run
}

// measureScalePoint 跑单个「规模 × 负载族 × 并发档」测量，并把它与当时的环境口径绑在一起。
// 顺序：抓池快照 → 起采样器 → 跑档位 → 停采样器 → 抓池快照与 /metrics → 组装。
func measureScalePoint(
	t *testing.T, cfg scaleConfig, cp *harness.ControlPlane, client *http.Client, cpPID int,
	set *scenarioSet, family string, concurrent, size int, baseURL, token string,
) scalePoint {
	t.Helper()
	label := fmt.Sprintf("规模 %d / %s / 并发 %d / %s", size, family, concurrent, cfg.LevelDuration)
	t.Logf("-- 档位：%s --", label)

	ctx := context.Background()
	poolBefore, beforeErr := fetchObservabilityRaw(ctx, client, baseURL, token)
	if beforeErr != nil {
		t.Logf("档位前抓自观测快照失败（本档池增量将标记为不可用）：%v", beforeErr)
	}
	// /metrics 也取前后两次：GC 次数是进程累计计数器，只有增量能说明「这一档的 GC 压力」。
	metricsBefore := fetchServerMetrics(ctx, client, baseURL)

	sampler := startResourceSampler(map[string]int{"控制面": cpPID, "压测端": os.Getpid()}, scaleSamplingPeriod)
	res, runErr := runLoad(ctx, &driverConfig{
		label: label, client: client, scenarios: set,
		concurrent: concurrent, duration: cfg.LevelDuration,
		requestTimeout: requestTimeout, guard: cp,
	})
	resources := summarizeResources(sampler.Stop())

	point := scalePoint{
		Scale: size, Family: family, Concurrent: concurrent, Label: label,
		Result: res, Resources: resources,
		ClientCores: resources.Procs["压测端"].AvgCores,
		ServerCores: resources.Procs["控制面"].AvgCores,
	}
	if runErr != nil {
		point.Error = runErr.Error()
		t.Errorf("档位 %s 执行失败：%v", label, runErr)
		return point
	}

	poolAfter, afterErr := fetchObservabilityRaw(ctx, client, baseURL, token)
	if afterErr != nil {
		t.Logf("档位后抓自观测快照失败（本档池增量将标记为不可用）：%v", afterErr)
	}
	point.PoolWait = subtractPool(poolBefore, poolAfter, beforeErr == nil && afterErr == nil)

	metrics := fetchServerMetrics(ctx, client, baseURL)
	if metrics.Error != "" {
		t.Logf("抓 /metrics 失败（不阻断）：%s", metrics.Error)
	}
	point.Goroutines = metrics.Goroutines
	point.RSSMiB = metrics.RSSBytes / (1024 * 1024)
	point.GCCount = metrics.GCCount
	point.GCCountDelta = metrics.GCCount - metricsBefore.GCCount
	if metricsBefore.Error == "" && res.DurationMs > 0 {
		point.GCPerSec = float64(point.GCCountDelta) / (float64(res.DurationMs) / 1000)
	}
	point.ServerRoute = primaryRouteForFamily(family)
	point.ServerP95Ms, point.ServerP99Ms, point.ServerSamples =
		routeQuantileFromDelta(metricsBefore, metrics, point.ServerRoute, 0.95, 0.99)
	point.ServerDeltaValid = point.ServerSamples > 0

	t.Logf("档位完成：%s 请求 %d 成功 %d 失败 %d | RPS %.0f p95 %.2fms p99 %.2fms",
		label, res.TotalRequests, res.Success, res.Failed, res.RPS, res.Latency.P95Ms, res.Latency.P99Ms)
	t.Logf("  环境口径：load1 %.2f→%.2f（峰值 %.2f，%.2f/核）；控制面 %.2f 核 RSS %.0fMiB VmHWM %.0fMiB；压测端 %.2f 核",
		resources.Load1Start, resources.Load1End, resources.Load1Max, resources.Load1PerCore,
		point.ServerCores, float64(resources.Procs["控制面"].RSSMaxKiB)/1024,
		float64(resources.Procs["控制面"].HWMMaxKiB)/1024, point.ClientCores)
	t.Logf("  内部证据：DB 池等待 %d 次 / %dms（上限 %d，结束时使用中 %d）；注册表 %d；goroutine %d；GC %d 次（%.1f 次/秒）",
		point.PoolWait.WaitCount, point.PoolWait.WaitDurationMs, point.PoolWait.MaxOpen,
		point.PoolWait.InUseAfter, point.PoolWait.RegistryTotal, point.Goroutines,
		point.GCCountDelta, point.GCPerSec)
	t.Logf("  服务端直方图（%s）：p95 %.2fms p99 %.2fms（样本 %d）",
		point.ServerRoute, point.ServerP95Ms, point.ServerP99Ms, point.ServerSamples)
	return point
}

// primaryRouteForFamily 返回该负载族里最能代表控制面成本的路由模板。
// 心跳是「内存写 + 2 次 DB 读」的复合路径，v1 核心取心跳（它是最重且最高频的一条），
// 只读取 DB 读端点，v2 取上报端点。
func primaryRouteForFamily(family string) string {
	switch family {
	case "心跳", "v1核心":
		return "/beacon/v1/agent/heartbeat"
	case "只读":
		return "/admin/v2/servers"
	case "纯内存":
		return "/admin/v1/system/status"
	case "纯DB读":
		return "/admin/v2/servers"
	case "v2上报":
		return "/beacon/v2/agent/metrics/report"
	default:
		return ""
	}
}

// waitControlPlanePID 按 argv[0] 定位控制面进程 PID，最多重试若干秒。
// harness 以绝对路径 exec 该二进制，故 cmdline 首段即二进制绝对路径，可精确匹配。
func waitControlPlanePID(binPath string) int {
	for range 20 {
		if pid := findProcessPIDByCmdline(binPath); pid > 0 {
			return pid
		}
		time.Sleep(250 * time.Millisecond)
	}
	return 0
}

// removeStaleDBFile 只删本用例自己的库文件（含 WAL / SHM）。
// 刻意不用 removeStaleDB：那个 helper 会清目录下全部 *.db*，会误删并行运行的其他用例的库。
func removeStaleDBFile(t *testing.T, dbPath string) {
	t.Helper()
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatalf("删除遗留库文件 %s 失败：%v", path, err)
		}
	}
}

// v2SetItems 安全取场景集合长度（nil 安全）。
func v2SetItems(set *scenarioSet) int {
	if set == nil {
		return 0
	}
	return len(set.items)
}

// firstNStrings 取前 n 项（不足则全取），用于失败明细的截断展示。
func firstNStrings(items []string, n int) []string {
	if len(items) <= n {
		return items
	}
	return items[:n]
}

// slicesMax 取切片最大值（空切片返回 0）。
func slicesMax(values []int) int {
	best := 0
	for _, v := range values {
		if v > best {
			best = v
		}
	}
	return best
}

// newScaleConfig 从环境变量装配规模压测参数。
func newScaleConfig(t *testing.T) scaleConfig {
	t.Helper()
	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	return scaleConfig{
		Sizes:         parseIntList(t, envOr("LOADTEST_SCALE_SIZES", "64,1000"), "LOADTEST_SCALE_SIZES"),
		Levels:        parseIntList(t, envOr("LOADTEST_SCALE_LEVELS", defaultLevels), "LOADTEST_SCALE_LEVELS"),
		FamilyLevels:  parseIntList(t, envOr("LOADTEST_SCALE_FAMILY_LEVELS", "32,128"), "LOADTEST_SCALE_FAMILY_LEVELS"),
		LevelDuration: durationEnv(t, "LOADTEST_SCALE_LEVEL_DURATION", "20s"),
		RegisterConc:  intEnv(t, "LOADTEST_SCALE_REGISTER_CONCURRENCY", 16),
		SkipV2:        os.Getenv("LOADTEST_SCALE_SKIP_V2") == "1",
		// 隔离族默认开启，要缩短总时长时才用 LOADTEST_SCALE_SKIP_ISOLATION=1 关掉。
		IncludeIsolation: os.Getenv("LOADTEST_SCALE_SKIP_ISOLATION") != "1",
		AdminPass:        os.Getenv("E2E_ADMIN_PASS"),
		AuthSecret:       os.Getenv("E2E_AUTH_SECRET"),
		RepoRoot:         repoRoot,
	}
}

// parseIntList 解析逗号分隔的正整数列表（空项跳过，全空或非法直接失败）。
func parseIntList(t *testing.T, raw, key string) []int {
	t.Helper()
	values := make([]int, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 1 {
			t.Fatalf("环境变量 %s=%q 含非法数值 %q（应为 ≥1 的整数）", key, raw, part)
		}
		values = append(values, value)
	}
	if len(values) == 0 {
		t.Fatalf("环境变量 %s=%q 解析后为空", key, raw)
	}
	return values
}

// goVersionString 返回运行环境的 Go 版本与平台（报告可读性用）。
func goVersionString() string {
	return runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH
}

// markdown 渲染规模压测报告的 Markdown 文本。
// 组织顺序刻意是「结论 → 对照 → 证据 → 口径」：读的人先拿结论，再按需下钻；
// 把环境口径放最后不代表它次要，而是避免它挡住对照表——但所有数据行都带 load/CPU 列。
func (r *scaleReport) markdown() string {
	var b strings.Builder
	b.WriteString("# 控制面 1000+ 子服规模容量压测报告\n\n")
	writeScaleKeyValues(&b, [][2]string{
		{"生成时间", r.GeneratedAt},
		{"控制面二进制版本", r.BinaryVersion},
		{"运行环境", r.GoVersion},
		{"宿主机逻辑核数", fmt.Sprintf("%d", r.CPUCores)},
		{"每档时长", r.LevelDuration},
		{"仓库根", r.RepoRoot},
	})

	b.WriteString("## 0. 结论\n\n")
	b.WriteString(r.verdictMarkdown())

	b.WriteString("## 1. 注册阶段实测\n\n")
	writeScaleRegistrationTable(&b, r.Runs)

	b.WriteString("## 2. 同规模 × 负载族 × 并发档 全量数据\n\n")
	writeScalePointTable(&b, r.Runs)

	b.WriteString("## 3. 规模倍数效应（同族同并发：大规模 ÷ 64 台）\n\n")
	writeScaleRatioTable(&b, r.Runs)

	b.WriteString("## 4. 端点明细（瓶颈定位的直接证据）\n\n")
	b.WriteString("同一档位内各端点的成本差异，是把瓶颈归到「哪条路径」的依据：\n")
	b.WriteString("心跳是「内存写 + 2 次 DB 读（环境行 + server 行）」的复合路径，只读端点是纯内存/DB 读，\n")
	b.WriteString("v2 上报是异步入队（请求 goroutine 不碰 DB）。若心跳 p95 显著高于只读，成本差异就不在 HTTP 层。\n\n")
	writeScaleEndpointTable(&b, r.Runs)

	b.WriteString("## 5. 宿主机与进程口径说明\n\n")
	writeScaleHostNotes(&b, r.HostLoadNotes)
	return b.String()
}

// writeScaleEndpointTable 输出各档位的端点级明细（含每端点采样数，避免把抽样当全量）。
func writeScaleEndpointTable(b *strings.Builder, runs []scaleRun) {
	for _, run := range runs {
		for _, p := range run.Points {
			if p.Result == nil || len(p.Result.Endpoints) == 0 {
				continue
			}
			fmt.Fprintf(b, "#### 规模 %d / %s / 并发 %d（%s）\n\n", p.Scale, p.Family, p.Concurrent, p.Result.Label)
			b.WriteString("| 分组 | 端点 | 请求 | 失败 | 错误率 | p50(ms) | p95(ms) | p99(ms) | max(ms) |\n")
			b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
			for i := range p.Result.Endpoints {
				ep := &p.Result.Endpoints[i]
				fmt.Fprintf(b, "| %s | %s | %d | %d | %.2f%% | %.2f | %.2f | %.2f | %.2f |\n",
					ep.Group, ep.Name, ep.Requests, ep.Failed, ep.ErrorRate*100,
					ep.Latency.P50Ms, ep.Latency.P95Ms, ep.Latency.P99Ms, ep.Latency.MaxMs)
			}
			b.WriteString("\n")
		}
	}
}

// verdictMarkdown 汇总结论：伸缩性判定 + 瓶颈归属 + 容量与边界。
func (r *scaleReport) verdictMarkdown() string {
	var b strings.Builder
	low, high, ok := r.edgeRuns()
	if !ok {
		b.WriteString("（规模数不足两个，无法给出伸缩性判定；请至少设置两个 LOADTEST_SCALE_SIZES。）\n\n")
		return b.String()
	}
	ratio := float64(high.Scale) / float64(low.Scale)
	b.WriteString(fmt.Sprintf("**注册规模**：%d 台 → %d 台（%.1f 倍）。\n\n", low.Scale, high.Scale, ratio))

	// 逐个「负载族 × 并发档」列出规模倍数下的 RPS 变化，判定线性 / 次线性 / 拐点。
	b.WriteString("| 负载族 | 并发 | " + fmt.Sprintf("%d 台 RPS", low.Scale) + " | " +
		fmt.Sprintf("%d 台 RPS", high.Scale) + " | 吞吐量保持率 | " +
		fmt.Sprintf("%d 台 p95", low.Scale) + " | " + fmt.Sprintf("%d 台 p95", high.Scale) + " | 判定 |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, key := range pointKeys(low) {
		pLow, okLow := findPoint(low, key)
		pHigh, okHigh := findPoint(high, key)
		if !okLow || !okHigh || pLow.Result == nil || pHigh.Result == nil {
			continue
		}
		// 规模涨 N 倍但负载族与并发不变：理想情况下 RPS 应当持平（负载强度没变），
		// 真正会退化的是**每请求成本**——注册表变大让心跳/拓扑要遍历更多实例。
		keep := 1.0
		if pLow.Result.RPS > 0 {
			keep = pHigh.Result.RPS / pLow.Result.RPS
		}
		b.WriteString(fmt.Sprintf("| %s | %d | %.0f | %.0f | %.1f%% | %.2fms | %.2fms | %s |\n",
			key.Family, key.Concurrent, pLow.Result.RPS, pHigh.Result.RPS, keep*100,
			pLow.Result.Latency.P95Ms, pHigh.Result.Latency.P95Ms,
			scaleVerdict(keep, pLow.Result.Latency.P95Ms, pHigh.Result.Latency.P95Ms)))
	}
	b.WriteString("\n")

	// 瓶颈归属：把「池排队」「控制面 CPU 打满」「宿主机饱和」三类证据摆在一起。
	b.WriteString("**瓶颈归属证据**（同类同并发对比）：\n\n")
	b.WriteString("| 规模 | 负载族 | 并发 | 控制面 CPU 核 | 压测端 CPU 核 | load1 峰值 | load1/核 | DB 池等待次数 | DB 池等待耗时 | 判定 |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, run := range r.Runs {
		for _, p := range run.Points {
			waitCount, waitMs := "—", "—"
			if p.PoolWait.Valid {
				waitCount = fmt.Sprintf("%d", p.PoolWait.WaitCount)
				waitMs = fmt.Sprintf("%dms", p.PoolWait.WaitDurationMs)
			}
			b.WriteString(fmt.Sprintf("| %d | %s | %d | %.2f | %.2f | %.2f | %.2f | %s | %s | %s |\n",
				p.Scale, p.Family, p.Concurrent, p.ServerCores, p.ClientCores,
				p.Resources.Load1Max, p.Resources.Load1PerCore,
				waitCount, waitMs, bottleneckVerdict(&p)))
		}
	}
	b.WriteString("\n「DB 池等待」为「—」表示该档位前后有一次自观测快照没取到，增量不可用——不是「没排队」。\n\n")
	return b.String()
}

// pointKey 是「负载族 × 并发」的对照键（规模是自变量，不进键）。
type pointKey struct {
	Family     string
	Concurrent int
}

// pointKeys 按出现顺序取出某个规模的全部对照键。
func pointKeys(run scaleRun) []pointKey {
	keys := make([]pointKey, 0, len(run.Points))
	for _, p := range run.Points {
		keys = append(keys, pointKey{Family: p.Family, Concurrent: p.Concurrent})
	}
	return keys
}

// findPoint 在某个规模里找指定对照键的测量。
func findPoint(run scaleRun, key pointKey) (scalePoint, bool) {
	for _, p := range run.Points {
		if p.Family == key.Family && p.Concurrent == key.Concurrent {
			return p, true
		}
	}
	return scalePoint{}, false
}

// edgeRuns 返回规模最小与最大的两次运行（伸缩性判定的两端）。
func (r *scaleReport) edgeRuns() (scaleRun, scaleRun, bool) {
	if len(r.Runs) < 2 {
		return scaleRun{}, scaleRun{}, false
	}
	low, high := r.Runs[0], r.Runs[0]
	for _, run := range r.Runs[1:] {
		if run.Scale < low.Scale {
			low = run
		}
		if run.Scale > high.Scale {
			high = run
		}
	}
	return low, high, low.Scale != high.Scale
}

// scaleVerdict 按吞吐量保持率与时延变化给出伸缩性判定文案。
// 判据：负载强度不变时 RPS 应当基本持平；RPS 掉 >20% 或 p95 涨 >50% 即为该档位出现拐点。
func scaleVerdict(keepRatio, p95Low, p95High float64) string {
	switch {
	case keepRatio < 0.5:
		return "**明显拐点**（吞吐腰斩）"
	case keepRatio < 0.8:
		return "次线性（吞吐降 >20%）"
	case p95Low > 0 && p95High/p95Low > 1.5:
		return "时延拐点（p95 涨 >50%）"
	default:
		return "基本持平"
	}
}

// bottleneckVerdict 依据池排队 / 进程 CPU / 宿主机负载给出瓶颈归属。
// 三条判据互斥性来自因果：宿主机被别的负载打满时，控制面抢不到 CPU，因而**不会**在池上排队；
// 反过来说，池出现等待就意味着请求已经拿到 CPU 但拿不到连接，瓶颈在控制面内部的池。
func bottleneckVerdict(p *scalePoint) string {
	// 压测端自己先饱和的话，测出来的不是控制面的上限，必须显式标出以免误读。
	if p.ClientCores >= float64(p.Resources.CPUCores)*0.5 && p.ServerCores < p.ClientCores {
		return "**压测端先饱和**（数据仅为下界）"
	}
	// 池增量不可用时不得据此下结论，否则会把「没测到」说成「没排队」。
	if !p.PoolWait.Valid {
		if p.Resources.Load1Max > float64(p.Resources.CPUCores)*0.8 {
			return "宿主机 CPU 饱和（池增量缺失）"
		}
		return "池增量缺失，无法归属"
	}
	switch {
	case p.PoolWait.WaitCount > 0 && p.PoolWait.WaitDurationMs > 50:
		return "控制面内部：DB 连接池排队"
	case p.PoolWait.WaitCount > 0:
		return "控制面内部：池排队（轻度）"
	case p.Resources.Load1Max > float64(p.Resources.CPUCores)*0.8:
		return "宿主机 CPU 饱和"
	default:
		return "未见瓶颈"
	}
}

// writeScaleRegistrationTable 输出注册阶段实测表。
func writeScaleRegistrationTable(b *strings.Builder, runs []scaleRun) {
	b.WriteString("| 规模 | 请求 | 成功 | 失败 | 耗时 | 速率 | 并发度 | 注册期 load1 峰值 | 控制面 CPU 均值 | 压测端 CPU 均值 | 注册表规模 |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, run := range runs {
		reg := run.Registration
		b.WriteString(fmt.Sprintf("| %d | %d | %d | %d | %dms | %.1f 台/秒 | %d | %.2f | %.2f 核 | %.2f 核 | %d |\n",
			run.Scale, reg.Requested, reg.Succeeded, reg.Failed, reg.ElapsedMs, reg.PerSecond,
			reg.Concurrency, reg.Resources.Load1Max,
			reg.Resources.Procs["控制面"].AvgCores, reg.Resources.Procs["压测端"].AvgCores,
			reg.RegistryTotalAfter))
	}
	b.WriteString("\n注册阶段是一次性前置，不参与压测循环口径；但它本身会与压测共享同一个 4 连接 sqlite 池，\n")
	b.WriteString("故注册并发度需有界——无界并发会把「注册」变成一次连接池雪崩，测出来的是排队长度而非能力。\n\n")
}

// writeScalePointTable 输出全量数据表（数据 + 环境口径并排，避免分开读导致配错）。
func writeScalePointTable(b *strings.Builder, runs []scaleRun) {
	b.WriteString("| 规模 | 负载族 | 并发 | 请求 | 成功 | 失败 | RPS | p50 | p95 | p99 | 服务端 p95 | 服务端 p99 | 控制面 CPU | 压测端 CPU | load1 起→终(峰) | load1/核 | DB 池等待 | goroutine | RSS(MiB) | GC 次/秒 |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, run := range runs {
		for _, p := range run.Points {
			if p.Result == nil {
				b.WriteString(fmt.Sprintf("| %d | %s | %d | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — | — |（执行失败：%s）\n",
					p.Scale, p.Family, p.Concurrent, p.Error))
				continue
			}
			res := p.Result
			poolWait := "—"
			if p.PoolWait.Valid {
				poolWait = fmt.Sprintf("%d 次/%dms", p.PoolWait.WaitCount, p.PoolWait.WaitDurationMs)
			}
			b.WriteString(fmt.Sprintf("| %d | %s | %d | %d | %d | %d | %.0f | %.2fms | %.2fms | %.2fms | %.2fms | %.2fms | %.2f 核 | %.2f 核 | %.1f→%.1f(%.1f) | %.2f | %s | %d | %.0f | %.1f |\n",
				p.Scale, p.Family, p.Concurrent, res.TotalRequests, res.Success, res.Failed, res.RPS,
				res.Latency.P50Ms, res.Latency.P95Ms, res.Latency.P99Ms,
				p.ServerP95Ms, p.ServerP99Ms, p.ServerCores, p.ClientCores,
				p.Resources.Load1Start, p.Resources.Load1End, p.Resources.Load1Max, p.Resources.Load1PerCore,
				poolWait, p.Goroutines, p.RSSMiB, p.GCPerSec))
		}
	}
	b.WriteString("\n「服务端 p95/p99」来自控制面进程内直方图（/metrics），按该负载族主路由取值；\n")
	b.WriteString("与客户端 p95/p99 对照可判别时延产生在内核（两者接近）还是在排队与调度（客户端明显更高）。\n\n")
}

// writeScaleRatioTable 输出规模倍数效应表（大规模 ÷ 小规模）。
func writeScaleRatioTable(b *strings.Builder, runs []scaleRun) {
	if len(runs) < 2 {
		b.WriteString("（规模数不足两个，跳过。）\n\n")
		return
	}
	base := runs[0]
	for _, run := range runs[1:] {
		if run.Scale < base.Scale {
			base = run
		}
	}
	b.WriteString("| 规模 | 负载族 | 并发 | RPS vs 基线 | p95 vs 基线 | 心跳覆盖池 |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, run := range runs {
		for _, p := range run.Points {
			basePoint, ok := findPoint(base, pointKey{Family: p.Family, Concurrent: p.Concurrent})
			if !ok || basePoint.Result == nil || p.Result == nil {
				continue
			}
			rpsRatio, p95Ratio := 0.0, 0.0
			if basePoint.Result.RPS > 0 {
				rpsRatio = p.Result.RPS / basePoint.Result.RPS
			}
			if basePoint.Result.Latency.P95Ms > 0 {
				p95Ratio = p.Result.Latency.P95Ms / basePoint.Result.Latency.P95Ms
			}
			b.WriteString(fmt.Sprintf("| %d | %s | %d | %.2fx | %.2fx | %d 台 |\n",
				p.Scale, p.Family, p.Concurrent, rpsRatio, p95Ratio, run.Scale))
		}
	}
	b.WriteString("\n")
}

// writeScaleKeyValues 输出键值表（与既有报告的排版保持一致）。
func writeScaleKeyValues(b *strings.Builder, pairs [][2]string) {
	b.WriteString("| 项 | 值 |\n| --- | --- |\n")
	for _, pair := range pairs {
		b.WriteString("| " + pair[0] + " | " + pair[1] + " |\n")
	}
	b.WriteString("\n")
}

// writeScaleHostNotes 输出宿主机口径说明。
func writeScaleHostNotes(b *strings.Builder, notes []string) {
	b.WriteString("每个档位均在表格中带载当时口径，读表时须先确认这几列：\n\n")
	b.WriteString("- **load1 起→终(峰)**：档位期间的 1 分钟负载；跨规模对照必须在负载量级接近时才成立。\n")
	b.WriteString("- **load1/核**：峰值负载折算到单核的倍率，>0.8 表示宿主机已排不上队，此时任何 RPS 下降都先怀疑环境。\n")
	b.WriteString("- **控制面 / 压测端 CPU 核**：窗口内平均占用的 CPU 核数。控制面 CPU 很低而 RPS 也低、且池无等待 → 瓶颈既不在 CPU 也不在池，需查下游；\n")
	b.WriteString("  控制面 CPU 接近核数上限 → 就是 CPU 打满；压测端 CPU 高于控制面且接近核数 → 是压测端先饱和，数据只能当控制面能力的下界。\n")
	b.WriteString("- **DB 池等待**：控制面自观测里 sql.DBStats 的 WaitCount / WaitDuration 在档位内的增量。>0 是「请求拿到 CPU 但拿不到 DB 连接」的直接证据。\n\n")
	for _, note := range notes {
		b.WriteString("- " + note + "\n")
	}
	b.WriteString("\n")
}

// scaleReportJSON 是报告的 JSON 编码（供 write 复用）。
func (r *scaleReport) marshalJSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// write 把规模报告写进 dir（Markdown + JSON 同名双份）。
func (r *scaleReport) write(dir, stamp string) (string, string, error) {
	base := filepath.Join(dir, "loadtest-scale-"+stamp)
	jsonPath := base + ".json"
	mdPath := base + ".md"
	raw, err := r.marshalJSON()
	if err != nil {
		return "", "", fmt.Errorf("编码规模压测报告 JSON 失败：%w", err)
	}
	if err := os.WriteFile(jsonPath, append(raw, '\n'), 0o644); err != nil {
		return "", "", fmt.Errorf("写规模压测报告 JSON 失败：%w", err)
	}
	if err := os.WriteFile(mdPath, []byte(r.markdown()), 0o644); err != nil {
		return "", "", fmt.Errorf("写规模压测报告 Markdown 失败：%w", err)
	}
	return mdPath, jsonPath, nil
}

// summaryLines 返回控制台摘要（明细落文件）。
func (r *scaleReport) summaryLines() []string {
	lines := []string{
		fmt.Sprintf("规模压测完成：规模 %s，每档 %s，宿主机 %d 核", formatSizes(r.Runs), r.LevelDuration, r.CPUCores),
	}
	for _, run := range r.Runs {
		lines = append(lines, fmt.Sprintf("  规模 %d：注册成功 %d/%d（%.1f 台/秒，失败 %d）",
			run.Scale, run.Registration.Succeeded, run.Registration.Requested,
			run.Registration.PerSecond, run.Registration.Failed))
	}
	return lines
}

// formatSizes 把各规模的注册数拼成可读串。
func formatSizes(runs []scaleRun) string {
	parts := make([]string, 0, len(runs))
	for _, run := range runs {
		parts = append(parts, fmt.Sprintf("%d", run.Scale))
	}
	return strings.Join(parts, " / ")
}
