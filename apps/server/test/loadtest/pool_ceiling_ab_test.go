//go:build e2e && loadtest

// 「1000 台规模下控制面的瓶颈到底在哪」的判定实验（A/B 对照，交替轮次）。
//
// 为什么需要这个实验：规模对照只能证明「1000 台时同并发吞吐下降」，无法证明**为什么**下降。
// 候选原因至少有三种，且结论完全不同：
//  1. 宿主机 CPU 饱和 —— 那不该改控制面，该换机器或降并发；
//  2. 控制面 DB 连接池上限（sqlite 默认 4）排队 —— 那是**配置**问题，抬高上限即可；
//  3. 每条请求的 CPU 成本随注册表规模上升（内存注册表遍历 / 深拷贝 / GC）—— 那是**代码**问题。
//
// 实验设计（关键在「交替」而不是「先后」）：
//
//	同一二进制、同一注册规模、同一并发档，只改「DB 连接池上限」这一个自变量，跑 A/B 两组。
//	A 组＝仓库存量配置（max-open-conns = 4），B 组＝同配置副本（仅把该键抬高）。
//
// **两组控制面进程同时常驻、测量轮次交替进行（A→B→A→B…）**，而不是「先跑完 A 再跑完 B」。
// 原因来自上一轮实测的教训：本宿主机负载在 8~19 之间漂移，同一档位在不同时刻能差 2.3 倍
// （load 15 → 9918 RPS，load 36 → 4303 RPS）。先后执行的 A/B 会把「测量时刻的环境差异」
// 整个算进「配置差异」，得到的倍数完全不可信。交替轮次让两组共享同一段环境条件，
// 取多轮中位数后，残余漂移对两组是同向的，可相互抵消。
//
// 结论读法：
//   - B 组中位吞吐显著高于 A 组（≥20%）→ 瓶颈是**池上限**（配置）；
//   - 两组相当、且 A 组池等待很多 → 瓶颈不在池，转查 CPU / 代码路径；
//   - 两组「控制面 CPU 核数」都远低于核数上限却又都跑不上去 → 既非 CPU 也非池。
//
// 实现约束：**不修改生产代码、也不修改仓库根 config.yml**（会污染其它并行用例）。
// 做法是把 config.yml 原样复制到 .tmp 下、只改副本里的 max-open-conns，
// 再用控制面自身的 `-config` CLI 参数指向副本（见 cmd/beacon/cli.go）。
//
// 运行：
//
//	export E2E_ADMIN_PASS='...' E2E_AUTH_SECRET='...'
//	go test -tags "e2e loadtest" -timeout=40m -v -run '^TestControlPlanePoolCeiling' ./apps/server/test/loadtest/
package loadtest_e2e

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	// poolABScale 是 A/B 实验用的注册规模。取 1000：只在「真正退化的那个点」上做 A/B，
	// 小规模下池本来就不满，对照不出差异。
	poolABScale = 1000
	// poolABBumpOpenConns 是 B 组的池上限。取 32：远高于 4，足以让「池是否是瓶颈」的结论显性化；
	// 同时不取到几百——sqlite 多连接本身是双刃剑（写并发靠文件锁串行）。
	// 这里的目的只是判定瓶颈归属，不是推荐生产值。
	poolABBumpOpenConns = 32
	// poolABRounds 是交替轮次。取 3：3 轮 × 2 组 × 2 族 = 12 次测量，够取中位数看出组间差异，
	// 又不至于把总时长拉到不可接受。
	poolABRounds = 3
)

// poolABSide 是 A/B 的一侧：一个常驻控制面 + 其配置与连接。
type poolABSide struct {
	Label     string
	OpenConns int
	Config    string
	CP        *harness.ControlPlane
	BaseURL   string
	// MetricsBase 是该侧抓取端点的基址（独立监听，见 ADR-0090）。
	// 两侧**必须各有一个**：A/B 是「同一时刻常驻两个控制面」的对照实验，
	// 若两侧共用同一抓取地址，抓到的会是同一个进程的指标而两组数字看起来都正常——
	// 这种错位比缺数据更危险（结论会指向错误的池上限）。
	MetricsBase string
	PID         int
	Client      *http.Client
	Token       string
	HB          *scenarioSet
	Read        *scenarioSet
	Reg         scaleRegistration
}

// poolABSample 是一次单轮测量的样本。
type poolABSample struct {
	Round     int     `json:"round"`
	Family    string  `json:"family"`
	RPS       float64 `json:"rps"`
	P95Ms     float64 `json:"p95Ms"`
	P99Ms     float64 `json:"p99Ms"`
	Failed    int     `json:"failed"`
	ServerP95 float64 `json:"serverP95Ms"`
	ServerP99 float64 `json:"serverP99Ms"`
	WaitCount int64   `json:"waitCount"`
	WaitMs    int64   `json:"waitMs"`
	// OpenConns 是实验**意图**的池上限；ObservedMaxOpen 是控制面自报的**实际**池上限。
	// 两者必须一起记录：若配置没生效（路径错、被 env 覆盖），只看意图值会把「两组配置其实相同」
	// 的实验读成「池不是瓶颈」——这是一个方向完全相反的错误结论。
	OpenConns       int     `json:"openConns"`
	ObservedMaxOpen int     `json:"observedMaxOpen"`
	CPServerCores   float64 `json:"cpServerCores"`
	ClientCores     float64 `json:"clientCores"`
	Load1Max        float64 `json:"load1Max"`
	Load1End        float64 `json:"load1End"`
	Valid           bool    `json:"valid"`
}

// TestControlPlanePoolCeiling 在同一二进制上做「池上限 4 vs 抬高」的交替 A/B，判定瓶颈归属。
func TestControlPlanePoolCeiling(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")

	scale := intEnv(t, "LOADTEST_POOL_AB_SCALE", poolABScale)
	bump := intEnv(t, "LOADTEST_POOL_AB_OPEN_CONNS", poolABBumpOpenConns)
	concurrency := intEnv(t, "LOADTEST_POOL_AB_CONCURRENCY", 32)
	duration := durationEnv(t, "LOADTEST_POOL_AB_DURATION", "15s")
	registerConc := intEnv(t, "LOADTEST_POOL_AB_REGISTER_CONCURRENCY", 16)
	rounds := intEnv(t, "LOADTEST_POOL_AB_ROUNDS", poolABRounds)

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	workDir := filepath.Join(repoRoot, ".tmp", scaleWorkDirName)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("创建实验工作目录失败：%v", err)
	}

	t.Log("== 构建控制面二进制（A/B 共用同一二进制，确保差异只来自配置）==")
	bin, err := harness.BuildBeacon(repoRoot)
	if err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}

	baseConfig := filepath.Join(repoRoot, "config.yml")
	if _, err := os.Stat(baseConfig); err != nil {
		t.Fatalf("仓库根 config.yml 不可读（A/B 需要以它为基底复制）：%v", err)
	}
	bConfig := filepath.Join(workDir, fmt.Sprintf("config-pool-%d.yml", bump))
	if err := writeConfigWithPool(baseConfig, bConfig, bump); err != nil {
		t.Fatalf("生成 B 组配置失败：%v", err)
	}
	t.Logf("B 组配置：%s（仅 database.max-open-conns 与基底不同）", bConfig)

	sides := []*poolABSide{
		{Label: "A：存量配置", OpenConns: 4, Config: baseConfig},
		{Label: fmt.Sprintf("B：池上限 %d", bump), OpenConns: bump, Config: bConfig},
	}
	// 两个控制面同时常驻，且**在全部轮次开始前一起注册完**：
	// 让「注册」这段一次性写入负载不落在任何一组的测量窗口内。
	for _, side := range sides {
		startPoolABSide(t, side, bin, repoRoot, workDir, scale, registerConc,
			max(concurrency, 8), adminPass, authSecret)
	}
	defer func() {
		for _, side := range sides {
			side.Client.CloseIdleConnections()
			if err := side.CP.StopE(); err != nil {
				t.Errorf("停止 %s 控制面失败：%v", side.Label, err)
			}
		}
	}()

	samples := make([]poolABSample, 0, rounds*2*2)
	for round := 1; round <= rounds; round++ {
		t.Logf("---- 第 %d/%d 轮（A→B 交替，两组共享同一段环境条件）----", round, rounds)
		// 每轮内部固定 A→B 顺序；跨轮重复，使先后顺序带来的系统偏差在两组间均匀分摊。
		for _, side := range sides {
			for _, family := range []string{"心跳", "只读"} {
				sample := measurePoolAB(t, side, family, round, concurrency, duration, scale)
				samples = append(samples, sample)
			}
		}
	}

	t.Log("================ A/B 交替轮次结论 ================")
	for _, line := range poolABVerdict(sides, samples, concurrency, rounds) {
		t.Log(line)
	}

	// 兜底断言：A/B 结论必须建立在「两组各自都测到了完整轮次」之上。
	// 否则任一组测量失败时，poolABVerdict 会把「缺数据」算成「无差异」，
	// 得出方向完全相反的结论（「池不是瓶颈」），而用例照样绿。
	assertPoolABCovered(t, sides, samples, rounds)
}

// assertPoolABCovered 断言 A/B 两组都完成了预期轮次、且每条样本都带实测样本与生效的池配置。
//
// 不断言 RPS 数值（受宿主机负载影响，写死阈值会误报）；只挡「数据缺失被当成无差异」这一类假绿。
func assertPoolABCovered(t *testing.T, sides []*poolABSide, samples []poolABSample, rounds int) {
	t.Helper()
	for _, side := range sides {
		got := 0
		for _, s := range samples {
			if s.OpenConns != side.OpenConns {
				continue
			}
			got++
			if s.RPS <= 0 {
				t.Fatalf("池=%d 的样本 RPS=%.1f（第 %d 轮）——该轮未测到有效吞吐",
					side.OpenConns, s.RPS, s.Round)
			}
			// 池上限必须真的生效，否则两组其实是同一配置，结论反向。
			if s.ObservedMaxOpen != side.OpenConns {
				t.Fatalf("池=%d 的控制面自报上限为 %d——配置未生效，本组结论不可信",
					side.OpenConns, s.ObservedMaxOpen)
			}
		}
		if got == 0 {
			t.Fatalf("池=%d 一组没有任何样本——A/B 对照缺一侧，结论无意义", side.OpenConns)
		}
	}
}

// startPoolABSide 起一个常驻控制面、注册虚拟 agent、构造两组负载集。
func startPoolABSide(
	t *testing.T, side *poolABSide, bin, repoRoot, workDir string,
	scale, registerConc, maxConcurrent int, adminPass, authSecret string,
) {
	t.Helper()
	dbPath := filepath.Join(workDir, fmt.Sprintf("beacon-poolab-%d.db", side.OpenConns))
	removeStaleDBFile(t, dbPath)

	cpCfg := harness.ControlPlaneConfig{
		BinPath: bin, RepoRoot: repoRoot,
		DBDriver: "sqlite", DBDSN: dbPath,
		AdminPassword: adminPass, AuthSecret: authSecret, BootstrapToken: bootstrapToken,
		LogPrefix: fmt.Sprintf("beacon-poolab-%d", side.OpenConns),
		ExtraEnv:  map[string]string{"BEACON_LOG_LEVEL": defaultCPLogLevel},
		// 显式指向本组配置：A 组即仓库根 config.yml，B 组是 max-open-conns 被抬高的副本。
		Args: []string{"-config", side.Config},
	}
	// A/B 两组控制面**同时常驻**，抓取端点必须各占一个空闲端口（下方 helper 逐次分配）；
	// 否则第二组会在 bind 默认 127.0.0.1:8849 时失败并直接退出（见 ADR-0090），
	// 表现为「B 组起不来」，与本实验要量的是两码事。
	cp, baseURL, metricsBase := startControlPlaneWithMetrics(t, cpCfg, controlPlaneStartAttempts)
	side.MetricsBase = metricsBase
	side.CP, side.BaseURL = cp, baseURL
	t.Logf("%s 抓取端点：%s/metrics", side.Label, side.MetricsBase)
	// 必须取进程句柄上的 PID，不能按二进制路径去 /proc 里找：A/B 两组用的是**同一个二进制**，
	// 按路径查找会两组都命中先启动的那个，于是 B 组的资源口径实际是 A 组的——
	// 数据看起来正常却整体错位，比缺失更有害。
	side.PID = cp.PID()
	if side.PID == 0 {
		t.Errorf("%s 未能取得控制面 PID，本组资源口径不可用", side.Label)
	} else {
		t.Logf("%s 控制面 PID=%d", side.Label, side.PID)
	}

	token, err := harness.Login(baseURL, adminUser, adminPass, cp)
	if err != nil {
		t.Fatalf("%s 登录失败：%v", side.Label, err)
	}
	side.Token = token
	side.Client = newLoadClient(maxConcurrent)

	reg := registerVirtualAgentsConcurrent(t, side.Client, baseURL, scale, registerConc, side.PID, os.Getpid())
	side.Reg = scaleRegistration{
		Scale: scale, Requested: scale, Succeeded: reg.Succeeded, Failed: len(reg.Failures),
		ElapsedMs: reg.ElapsedMs, PerSecond: reg.RPS, Concurrency: registerConc,
		Failures: reg.Failures, Resources: reg.Resources,
	}
	t.Logf("%s 注册：成功 %d/%d，耗时 %dms（%.1f 台/秒），失败 %d",
		side.Label, reg.Succeeded, scale, reg.ElapsedMs, reg.RPS, len(reg.Failures))
	if reg.Succeeded == 0 {
		t.Fatalf("%s 一台都没注册成功，A/B 无法进行", side.Label)
	}

	scenarioCfg := scenarioConfig{
		BaseURL: baseURL, AdminToken: token,
		BootstrapToken: bootstrapToken, Namespace: agentNamespace, ServerIDs: reg.IDs,
		MetricsBase: side.MetricsBase,
	}
	full := buildScenarioSet(scenarioCfg)
	side.HB = filterScenariosByName(full, "agent_heartbeat")
	// 只读族作为对照：纯内存端点（system_status）不碰池。
	// 若它也随池上限变化，说明观察到的差异另有来源，结论就不能只归给池。
	side.Read = filterScenariosByName(full, "system_status")
	probeScenarios(t, side.Client, side.HB)
}

// measurePoolAB 跑一轮单族测量并采集环境口径。
func measurePoolAB(
	t *testing.T, side *poolABSide, family string, round, concurrency int, duration time.Duration, scale int,
) poolABSample {
	t.Helper()
	set := side.HB
	if family == "只读" {
		set = side.Read
	}
	label := fmt.Sprintf("%s / %s / 第 %d 轮", side.Label, family, round)
	t.Logf("  轮次：%s", label)

	before, beforeErr := fetchObservabilityRaw(t.Context(), side.Client, side.BaseURL, side.Token)
	metricsBefore := fetchServerMetrics(t.Context(), side.Client, side.MetricsBase)

	sampler := startResourceSampler(map[string]int{"控制面": side.PID, "压测端": os.Getpid()}, time.Second)
	res, runErr := runLoad(t.Context(), &driverConfig{
		label: label, client: side.Client, scenarios: set,
		concurrent: concurrency, duration: duration,
		requestTimeout: requestTimeout, guard: side.CP,
	})
	resources := summarizeResources(sampler.Stop())

	sample := poolABSample{
		Round: round, Family: family, OpenConns: side.OpenConns,
		CPServerCores: resources.Procs["控制面"].AvgCores,
		ClientCores:   resources.Procs["压测端"].AvgCores,
		Load1Max:      resources.Load1Max, Load1End: resources.Load1End,
	}
	if runErr != nil {
		t.Errorf("轮次 %s 执行失败：%v", label, runErr)
		return sample
	}
	sample.Valid = true
	sample.RPS = res.RPS
	sample.P95Ms = res.Latency.P95Ms
	sample.P99Ms = res.Latency.P99Ms
	sample.Failed = res.Failed

	after, afterErr := fetchObservabilityRaw(t.Context(), side.Client, side.BaseURL, side.Token)
	delta := subtractPool(before, after, beforeErr == nil && afterErr == nil)
	sample.WaitCount, sample.WaitMs = delta.WaitCount, delta.WaitDurationMs
	sample.ObservedMaxOpen = delta.MaxOpen
	if delta.Valid && delta.MaxOpen != side.OpenConns {
		// 配置未生效必须立刻报错：否则本实验会得出与事实相反的结论（见字段注释）。
		t.Errorf("轮次 %s：控制面自报池上限 %d，与预期 %d 不符——本组配置未生效，A/B 结论不可信",
			label, delta.MaxOpen, side.OpenConns)
	}

	metrics := fetchServerMetrics(t.Context(), side.Client, side.MetricsBase)
	route := "/beacon/v1/agent/heartbeat"
	if family == "只读" {
		route = "/admin/v1/system/status"
	}
	sample.ServerP95, sample.ServerP99, _ = routeQuantileFromDelta(metricsBefore, metrics, route, 0.95, 0.99)

	t.Logf("    %s：RPS %.0f，p95 %.2fms，p99 %.2fms，失败 %d；池上限自报 %d（预期 %d）等待 %d 次/%dms；控制面 %.2f 核；load1 峰 %.2f 终 %.2f",
		family, sample.RPS, sample.P95Ms, sample.P99Ms, sample.Failed,
		sample.ObservedMaxOpen, sample.OpenConns, sample.WaitCount, sample.WaitMs, sample.CPServerCores,
		sample.Load1Max, sample.Load1End)
	return sample
}

// poolABVerdict 依据交替轮次样本给出瓶颈归属结论（用中位数抵抗环境漂移）。
func poolABVerdict(sides []*poolABSide, samples []poolABSample, concurrency, rounds int) []string {
	lines := make([]string, 0, 24)
	lines = append(lines, fmt.Sprintf("设计：%d 轮交替（A→B），每轮每组每族各一次，并发 %d；判定用各轮中位数而非均值。",
		rounds, concurrency))
	for _, side := range sides {
		lines = append(lines, fmt.Sprintf("%s：注册 %d/%d 成功，耗时 %dms；配置 %s",
			side.Label, side.Reg.Succeeded, side.Reg.Requested, side.Reg.ElapsedMs, side.Config))
	}

	verdictFor := func(family string) {
		bySide := make([][]poolABSample, len(sides))
		for i := range samples {
			if samples[i].Family != family || !samples[i].Valid {
				continue
			}
			for j := range sides {
				if samples[i].OpenConns == sides[j].OpenConns {
					bySide[j] = append(bySide[j], samples[i])
				}
			}
		}
		if len(bySide) < 2 || len(bySide[0]) == 0 || len(bySide[1]) == 0 {
			lines = append(lines, fmt.Sprintf("【%s】样本不足，无法比较", family))
			return
		}
		rpsA, rpsB := medianRPS(bySide[0]), medianRPS(bySide[1])
		p95A, p95B := medianP95(bySide[0]), medianP95(bySide[1])
		waitA := medianWait(bySide[0])
		ratio := 0.0
		if rpsA > 0 {
			ratio = rpsB / rpsA
		}
		lines = append(lines, fmt.Sprintf("【%s】中位 RPS：池 %d → %d 为 %.0f → %.0f（%.2fx）；中位 p95 %.2fms → %.2fms；池等待中位 %d 次",
			family, sides[0].OpenConns, sides[1].OpenConns, rpsA, rpsB, ratio, p95A, p95B, waitA))
		switch {
		case ratio >= 1.2:
			lines = append(lines, "  → 抬高池上限带来 ≥20% 吞吐提升：**瓶颈是 DB 连接池上限（配置）**。")
		case ratio <= 1.05:
			lines = append(lines, "  → 抬高池上限几乎无改善：**瓶颈不在池**，须转查 CPU / 代码路径 / 客户端。")
		default:
			lines = append(lines, "  → 抬升有限：瓶颈是混合的，需结合 CPU 占用与端点明细判断主因。")
		}
	}
	verdictFor("心跳")
	verdictFor("只读")

	// CPU 是否打满，决定「该换机器」还是「该改配置 / 代码」。
	for i := range samples {
		if !samples[i].Valid {
			continue
		}
		side := sides[0].Label
		for _, s := range sides {
			if s.OpenConns == samples[i].OpenConns {
				side = s.Label
			}
		}
		// 核数从环境口径里的采样核数取（40 核本机），阈值 50% 作为「明显受 CPU 限制」的分界。
		if samples[i].CPServerCores > 0 {
			lines = append(lines, fmt.Sprintf("%s / %s / 第 %d 轮：控制面占 %.2f 核，压测端 %.2f 核，load1 峰值 %.2f",
				side, samples[i].Family, samples[i].Round,
				samples[i].CPServerCores, samples[i].ClientCores, samples[i].Load1Max))
		}
	}
	return lines
}

// medianRPS / medianP95 / medianWait 取中位数：交替轮次下中位数比均值更抗单轮环境抖动。
func medianRPS(items []poolABSample) float64 {
	values := make([]float64, 0, len(items))
	for _, it := range items {
		values = append(values, it.RPS)
	}
	return medianFloat(values)
}

func medianP95(items []poolABSample) float64 {
	values := make([]float64, 0, len(items))
	for _, it := range items {
		values = append(values, it.P95Ms)
	}
	return medianFloat(values)
}

func medianWait(items []poolABSample) int64 {
	values := make([]int64, 0, len(items))
	for _, it := range items {
		values = append(values, it.WaitCount)
	}
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values[len(values)/2]
}

// medianFloat 返回中位数（偶数个取中间两个的均值）。
func medianFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// writeConfigWithPool 把基底配置复制到 dst，并把 database.max-open-conns 改写为 openConns。
//
// 逐行改写而不是「解析 YAML 再序列化」：config.yml 里有大量注释（含池上限取值的理由、
// 生产事故的教训），整体序列化会把它们全部丢掉——而这份副本是实验证据的一部分，
// 保留注释才能让人复核「确实只改了这一项」。
func writeConfigWithPool(src, dst string, openConns int) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("读基底配置 %s 失败：%w", src, err)
	}
	lines := strings.Split(string(raw), "\n")
	// 只在 database: 段内替换，避免命中别处同名的键。
	inDatabase := false
	replaced := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// 段头：顶格且非空、非注释的 key
		if line == trimmed && trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			inDatabase = strings.HasPrefix(trimmed, "database:")
		}
		if inDatabase && strings.HasPrefix(trimmed, "max-open-conns:") {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = indent + "max-open-conns: " + strconv.Itoa(openConns)
			replaced = true
		}
	}
	if !replaced {
		return fmt.Errorf("在 %s 的 database 段内未找到 max-open-conns 键，无法生成对照配置", src)
	}
	return os.WriteFile(dst, []byte(strings.Join(lines, "\n")), 0o600)
}
