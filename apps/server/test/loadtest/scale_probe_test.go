//go:build e2e && loadtest

// 规模容量压测的观测底座：宿主机 / 控制面进程 / 压测端进程的资源采样、有界并发注册、
// /metrics 直方图解析与分位数计算。
//
// **本文件不含测试用例**（无 Test/Benchmark），是纯 helper 库——带 `_test.go` 后缀与
// `e2e && loadtest` tag 只为跟同目录用例一起编译。其中纯计算部分（直方图分位数 / 做差、
// 判定函数）的单测在 `scale_probe_quantile_test.go` 与 `scale_verdict_test.go`。
//
// 平台前提：本目录用例依赖 `/proc`（loadavg、进程 status、net/tcp），**仅 Linux 可运行**。
//
// 为什么单独成文件、不复用 loadtest_test.go 里的既有观测：
//  1. 规模压测的最大风险是「把环境差异误判成系统退化」——上一轮实测同一档位在不同宿主机负载下
//     差了 2.3 倍（load 15/40 核 → 9918 RPS；load 36 → 4303 RPS）。因此每次测量必须把宿主机口径
//     （loadavg / 核数 / 控制面 RSS / 控制面 CPU 时间 / 压测端自身 CPU 时间）与数据一起落盘，
//     既有 observe() 只抓 /metrics 与自观测文本，没有这一层；
//  2. 并行开发期尽量不动既有文件，新能力全部收在本文件内。
//
// 关键判别手段：同时采样「控制面进程 CPU 核数」与「压测端自身 CPU 核数」。
// 只看 RPS 下降无法区分三种完全不同的结论——宿主机被别的任务打满 / 控制面内部瓶颈 / 压测端自己先饱和；
// 把两者的 CPU 占用与宿主机 load 对着看才能区分。
package loadtest_e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// 采样与解析用的常量。
const (
	// linuxClockTicks 是 Linux USER_HZ，/proc/<pid>/stat 的 utime/stime 以该频率计数。
	// 写死 100：x86_64 / arm64 上恒为 100，且此处只用于算「这一档烧了多少 CPU 秒」的相对量，
	// 不引入 getconf 子进程（压测期 fork 子进程本身就是干扰）。
	linuxClockTicks = 100.0
	// defaultRegisterConcurrency 是注册阶段默认并发度（1 = 既有串行行为，保持向后兼容）。
	defaultRegisterConcurrency = 1
	// registerRequestTimeout 是单个注册请求的上限：注册要走 4~5 次 DB 往返 + 1 条审计写入，
	// 池化后单请求通常 <50ms；给 30s 是为了在池枯竭时明确失败而不是静默挂住。
	registerRequestTimeout = 30 * time.Second
	// registerTotalTimeout 是整批注册的总预算。1000 台串行时曾经需要几十秒以上，
	// 故总预算与「单请求预算」分开，避免把「批太大」误报成「单请求超时」。
	registerTotalTimeout = 10 * time.Minute
)

// ---- 宿主机与进程资源采样 ----

// loadAvg 是一次 /proc/loadavg 读数。
type loadAvg struct {
	Load1  float64
	Load5  float64
	Load15 float64
}

// readLoadAvg 读 /proc/loadavg；失败返回零值（不阻断压测，报告里会显示为空）。
func readLoadAvg() loadAvg {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return loadAvg{}
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 3 {
		return loadAvg{}
	}
	l1, _ := strconv.ParseFloat(fields[0], 64)
	l5, _ := strconv.ParseFloat(fields[1], 64)
	l15, _ := strconv.ParseFloat(fields[2], 64)
	return loadAvg{Load1: l1, Load5: l5, Load15: l15}
}

// procStats 是单个进程的一次资源读数。
type procStats struct {
	RSSKiB   int64 // 当前常驻集
	HWMKiB   int64 // 历史峰值常驻集（VmHWM）
	PeakKiB  int64 // 历史峰值虚拟内存（VmPeak）
	Threads  int   // 线程数（Go 进程含 M 对应的 OS 线程）
	CPUTicks int64 // utime + stime 累计（USER_HZ）
}

// readProcStats 读 /proc/<pid>/status 与 /proc/<pid>/stat 拼出一次读数。
// 两个文件都要读：status 没有 CPU 时间，stat 没有 RSS 峰值。
// 读失败（进程退出 / 权限）返回零值，采样器照常继续——观测失败不该让压测失败。
func readProcStats(pid int) procStats {
	var st procStats
	if pid <= 0 {
		return st
	}
	if raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
		for line := range strings.SplitSeq(string(raw), "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			field := strings.Fields(value)
			if len(field) == 0 {
				continue
			}
			n, err := strconv.ParseInt(field[0], 10, 64)
			if err != nil {
				continue
			}
			switch key {
			case "VmRSS":
				st.RSSKiB = n
			case "VmHWM":
				st.HWMKiB = n
			case "VmPeak":
				st.PeakKiB = n
			case "Threads":
				st.Threads = int(n)
			}
		}
	}
	st.CPUTicks = readProcCPUTicks(pid)
	return st
}

// readProcCPUTicks 读 /proc/<pid>/stat 的 utime+stime。
// comm 字段可能含空格与括号，故必须从最后一个 ')' 之后切分，不能按列数硬数。
func readProcCPUTicks(pid int) int64 {
	if pid <= 0 {
		return 0
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	line := string(raw)
	closeIdx := strings.LastIndexByte(line, ')')
	if closeIdx < 0 {
		return 0
	}
	// 跳过 ") " 后，fields[0] 是第 3 字段 state，故 utime（第 14 字段）= fields[11]，stime = fields[12]。
	fields := strings.Fields(line[closeIdx+1:])
	if len(fields) < 13 {
		return 0
	}
	utime, err1 := strconv.ParseInt(fields[11], 10, 64)
	stime, err2 := strconv.ParseInt(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0
	}
	return utime + stime
}

// findProcessPIDByCmdline 在 /proc 下按 argv[0] 精确定位进程。
// harness 用 exec.Command(绝对二进制路径) 起控制面，argv[0] 即该绝对路径，
// 故按 cmdline 首段精确匹配即可，不依赖 ss/lsof 的外部进程与端口解析（那两者在容器里常缺能力）。
func findProcessPIDByCmdline(argv0 string) int {
	if argv0 == "" {
		return 0
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
		if err != nil {
			continue
		}
		parts := strings.Split(string(raw), "\x00")
		if len(parts) > 0 && parts[0] == argv0 {
			return pid
		}
	}
	return 0
}

// resourceSampler 在后台按固定间隔采样若干进程与宿主机负载。
type resourceSampler struct {
	targets map[string]int // 标签 → pid
	period  time.Duration

	mu      sync.Mutex
	samples []resSample
	stop    chan struct{}
	done    chan struct{}
}

// resSample 是一次采样：宿主机负载 + 各目标进程读数。
type resSample struct {
	At    time.Time
	Host  loadAvg
	Procs map[string]procStats
}

// startResourceSampler 起一个后台采样器；period ≤ 0 时用 1s。
func startResourceSampler(targets map[string]int, period time.Duration) *resourceSampler {
	if period <= 0 {
		period = time.Second
	}
	s := &resourceSampler{
		targets: targets, period: period,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	go s.loop()
	return s
}

func (s *resourceSampler) loop() {
	defer close(s.done)
	ticker := time.NewTicker(s.period)
	defer ticker.Stop()
	s.collect() // 先采一次，保证极短档位也有样本
	for {
		select {
		case <-s.stop:
			s.collect()
			return
		case <-ticker.C:
			s.collect()
		}
	}
}

func (s *resourceSampler) collect() {
	sample := resSample{At: time.Now(), Host: readLoadAvg(), Procs: make(map[string]procStats, len(s.targets))}
	for label, pid := range s.targets {
		sample.Procs[label] = readProcStats(pid)
	}
	s.mu.Lock()
	s.samples = append(s.samples, sample)
	s.mu.Unlock()
}

// Stop 停止采样并返回全部样本副本。
func (s *resourceSampler) Stop() []resSample {
	if s == nil {
		return nil
	}
	close(s.stop)
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]resSample, len(s.samples))
	copy(out, s.samples)
	return out
}

// procSummary 是单个进程在采样窗口内的资源摘要。
type procSummary struct {
	RSSMaxKiB  int64   `json:"rssMaxKiB"`
	HWMMaxKiB  int64   `json:"hwmMaxKiB"`
	ThreadsMax int     `json:"threadsMax"`
	CPUSeconds float64 `json:"cpuSeconds"`
	// AvgCores 是窗口内平均占用的 CPU 核数（CPU 秒增量 / 窗口秒数）。
	// 这是判别「宿主机饱和」与「进程自身饱和」的关键量：控制面 AvgCores 接近 0.1 而 RPS 很低，
	// 说明瓶颈不在控制面 CPU；AvgCores 接近核数上限则说明它就是 CPU 打满。
	AvgCores float64 `json:"avgCores"`
}

// resourceSummary 是一次测量的环境口径，必须与 RPS / 分位数一起看。
type resourceSummary struct {
	CPUCores int `json:"cpuCores"`
	Samples  int `json:"samples"`
	// WindowSec 是采样窗口长度（首末样本间隔），AvgCores 以它为分母。
	WindowSec float64 `json:"windowSec"`
	// Load1 是窗口起点 / 终点的 1 分钟负载，Load1Max 是窗口内采样峰值。
	Load1Start float64 `json:"load1Start"`
	Load1End   float64 `json:"load1End"`
	Load1Max   float64 `json:"load1Max"`
	Load15Avg  float64 `json:"load15Avg"`
	// Load1PerCore 是峰值负载折算到单核的倍率（>1 表示宿主机已排不上队）。
	Load1PerCore float64                `json:"load1PerCore"`
	Procs        map[string]procSummary `json:"procs"`
	Error        string                 `json:"error,omitempty"`
}

// summarize 把采样序列压成一份可读的环境口径。
func summarizeResources(samples []resSample) resourceSummary {
	summary := resourceSummary{CPUCores: numCPU(), Procs: map[string]procSummary{}}
	if len(samples) == 0 {
		summary.Error = "无采样样本"
		return summary
	}
	summary.Samples = len(samples)
	summary.Load1Start = samples[0].Host.Load1
	summary.Load1End = samples[len(samples)-1].Host.Load1
	last := samples[len(samples)-1]
	first := samples[0]
	summary.WindowSec = last.At.Sub(first.At).Seconds()

	var load15Sum float64
	for _, sample := range samples {
		if sample.Host.Load1 > summary.Load1Max {
			summary.Load1Max = sample.Host.Load1
		}
		load15Sum += sample.Host.Load15
	}
	summary.Load15Avg = load15Sum / float64(len(samples))
	if summary.CPUCores > 0 {
		summary.Load1PerCore = summary.Load1Max / float64(summary.CPUCores)
	}

	// 逐进程聚合：RSS / HWM / 线程取窗口内最大值，CPU 取首末差值（计数器）。
	labels := make(map[string]struct{})
	for _, sample := range samples {
		for label := range sample.Procs {
			labels[label] = struct{}{}
		}
	}
	for label := range labels {
		agg := procSummary{}
		var firstTicks, lastTicks int64
		for i, sample := range samples {
			st, ok := sample.Procs[label]
			if !ok {
				continue
			}
			if st.RSSKiB > agg.RSSMaxKiB {
				agg.RSSMaxKiB = st.RSSKiB
			}
			if st.HWMKiB > agg.HWMMaxKiB {
				agg.HWMMaxKiB = st.HWMKiB
			}
			if st.Threads > agg.ThreadsMax {
				agg.ThreadsMax = st.Threads
			}
			if i == 0 {
				firstTicks = st.CPUTicks
			}
			lastTicks = st.CPUTicks
		}
		if lastTicks >= firstTicks && lastTicks > 0 {
			agg.CPUSeconds = float64(lastTicks-firstTicks) / linuxClockTicks
		}
		if summary.WindowSec > 0 {
			agg.AvgCores = agg.CPUSeconds / summary.WindowSec
		}
		summary.Procs[label] = agg
	}
	return summary
}

// ---- /metrics 解析与直方图分位数 ----

// bucketPoint 是直方图的一个累积桶（le 上界 + 累积计数）。
type bucketPoint struct {
	Upper float64
	Cum   int64
}

// serverMetrics 是从 /metrics 解析出的规模压测关心的量。
// 服务端进程内分位数与客户端观测分位数对照，是判别瓶颈位置的核心证据：
// 两者接近 → 时延产生在控制面内部；客户端远高于服务端 → 时延产生在排队 / 调度 / 宿主机争抢。
type serverMetrics struct {
	HTTPRequests   int64
	Goroutines     int64
	GCSeconds      float64
	GCCount        int64
	RSSBytes       float64
	CPUSeconds     float64
	RouteCount     map[string]int64
	RouteHistogram map[string][]bucketPoint
	// 控制面自身健康（beacon_self_*，见 ADR-0091）：从抓取端点直读，用于验证
	// 「自身健康指标确实反映了负载」以及为告警阈值提供实测依据。
	SelfDBPoolWaitCount      int64
	SelfDBPoolWaitSeconds    float64
	SelfDBPoolInUse          int64
	SelfMetricWriteDiscarded int64
	SelfCommandPending       int64
	SelfCommandSnapshotUnix  float64
	Error                    string
}

// routeHistogramQuantile 返回指定路由的服务端直方图分位数（毫秒）。样本不足返回 0。
func (m *serverMetrics) routeHistogramQuantile(route string, p float64) float64 {
	if m == nil {
		return 0
	}
	points, ok := m.RouteHistogram[route]
	if !ok || len(points) == 0 {
		return 0
	}
	return histogramQuantile(points, points[len(points)-1].Cum, p) * 1000
}

// routeQuantileFromDelta 用「档位前后两次 /metrics 快照的直方图增量」算服务端分位数（毫秒）。
// 返回 p95 / p99 与本次档位进入该路由直方图的样本数；增量不可用（缺前快照 / 桶回退）时返回 0,0,0。
func routeQuantileFromDelta(before, after serverMetrics, route string, p95, p99 float64) (float64, float64, int64) {
	if before.Error != "" || after.Error != "" || route == "" {
		return 0, 0, 0
	}
	delta := histogramDelta(before.RouteHistogram[route], after.RouteHistogram[route])
	if len(delta) == 0 {
		return 0, 0, 0
	}
	count := delta[len(delta)-1].Cum
	if count <= 0 {
		return 0, 0, 0
	}
	return histogramQuantile(delta, count, p95) * 1000, histogramQuantile(delta, count, p99) * 1000, count
}

// routeHistogramCount 返回指定路由进入直方图的累计样本数。
func (m *serverMetrics) routeHistogramCount(route string) int64 {
	if m == nil {
		return 0
	}
	points, ok := m.RouteHistogram[route]
	if !ok || len(points) == 0 {
		return 0
	}
	return points[len(points)-1].Cum
}

// histogramQuantile 在 le 直方图上做线性插值求分位数（Prometheus histogram_quantile 的等价实现）。
// 落在 +Inf 桶时退回最后一个有限上界：只给保守下界，不外推编造数值。
func histogramQuantile(points []bucketPoint, count int64, p float64) float64 {
	if count <= 0 || len(points) == 0 {
		return 0
	}
	rank := p * float64(count)
	prevUpper, prevCum := 0.0, 0.0
	for i, point := range points {
		if float64(point.Cum) < rank {
			prevUpper, prevCum = point.Upper, float64(point.Cum)
			continue
		}
		// +Inf 桶没有有限上界，无法插值，只能退回上一桶上界（保守下界）。
		if math.IsInf(point.Upper, 1) {
			return prevUpper
		}
		span := float64(point.Cum) - prevCum
		// 第一个桶也要插值：否则低负载下 p95 落在 ≤1ms 桶时会一律读成 0，
		// 而「0ms」在报告里会被读成「量不出成本」，与实际不符。
		if span <= 0 {
			if i == 0 {
				return point.Upper
			}
			return prevUpper
		}
		return prevUpper + (rank-prevCum)/span*(point.Upper-prevUpper)
	}
	return prevUpper
}

// histogramDelta 返回 after - before 的桶增量（同一路由、同一次进程内）。
//
// 为什么必须做差：/metrics 的直方图是**进程累计**计数器，不清零。
// 若直接用累计值算分位数，先跑的档位（如并发 1、几十万请求）会把后面所有档位的分位数都拽住，
// 于是「并发 128 的 p99」显示的其实是「整轮压测至今的 p99」——这会让瓶颈定位的结论整体失效。
func histogramDelta(before, after []bucketPoint) []bucketPoint {
	if len(after) == 0 {
		return nil
	}
	// 按上界对齐：两个快照的桶边界来自同一份注册表定义，理论上一致；仍按上界查表以容忍新增桶。
	byUpper := make(map[float64]int64, len(before))
	for _, point := range before {
		byUpper[point.Upper] = point.Cum
	}
	out := make([]bucketPoint, 0, len(after))
	for _, point := range after {
		delta := point.Cum - byUpper[point.Upper]
		if delta < 0 {
			// 桶计数回退说明期间发生过进程重启，差值不可用；返回 nil 让调用方标记为未知。
			return nil
		}
		out = append(out, bucketPoint{Upper: point.Upper, Cum: delta})
	}
	return out
}

// fetchServerMetrics 抓一次 /metrics 并解析出规模压测关心的量（尽力而为）。
// metricsBase 是抓取端点基址（独立监听，见 ADR-0090），**不是**控制面主端口地址：
// 主端口上已不再提供 /metrics，传错会得到 404 并把全部服务端口径静默读成 0。
func fetchServerMetrics(ctx context.Context, client *http.Client, metricsBase string) serverMetrics {
	out := serverMetrics{RouteCount: map[string]int64{}, RouteHistogram: map[string][]bucketPoint{}}
	req, err := newMetricsRequest(ctx, metricsBase)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	resp, err := client.Do(req)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, metricsReadLimit))
	if err != nil {
		out.Error = err.Error()
		return out
	}

	const (
		requestsMetric   = "beacon_http_requests_total"
		durationMetric   = "beacon_http_request_duration_seconds"
		goroutinesMetric = "go_goroutines"
		gcSecondsMetric  = "go_gc_duration_seconds_sum"
		gcCountMetric    = "go_gc_duration_seconds_count"
		rssMetric        = "process_resident_memory_bytes"
		cpuMetric        = "process_cpu_seconds_total"
		// 控制面自身健康（ADR-0091）：这些是本次压测要验证的新指标。
		selfWaitCountMetric = "beacon_self_db_pool_wait_total"
		selfWaitDurMetric   = "beacon_self_db_pool_wait_duration_seconds_total"
		selfInUseMetric     = "beacon_self_db_pool_connections"
		selfDiscardedMetric = "beacon_self_metric_write_discarded_total"
		selfCommandsMetric  = "beacon_self_commands"
		selfCmdSnapMetric   = "beacon_self_commands_snapshot_timestamp_seconds"
	)
	// buckets 先按路由收集 (上界 → 累积值)，最后统一排序，避免依赖 /metrics 的输出顺序。
	buckets := map[string]map[float64]int64{}
	for line := range strings.SplitSeq(string(raw), "\n") {
		name, labels, value, ok := floatSample(line)
		if !ok {
			continue
		}
		switch name {
		case requestsMetric:
			out.HTTPRequests += int64(value)
		case goroutinesMetric:
			out.Goroutines = int64(value)
		case gcSecondsMetric:
			out.GCSeconds = value
		case gcCountMetric:
			out.GCCount = int64(value)
		case rssMetric:
			out.RSSBytes = value
		case cpuMetric:
			out.CPUSeconds = value
		case selfWaitCountMetric:
			out.SelfDBPoolWaitCount = int64(value)
		case selfWaitDurMetric:
			out.SelfDBPoolWaitSeconds = value
		case selfInUseMetric:
			// 带 state 标签，只取 in_use 一条（池实际被占用的连接数）。
			if labelValue(labels, "state") == "in_use" {
				out.SelfDBPoolInUse = int64(value)
			}
		case selfDiscardedMetric:
			out.SelfMetricWriteDiscarded = int64(value)
		case selfCommandsMetric:
			// 队列积压 = pending + fetched（尚未终结的命令），与告警规则同口径。
			if s := labelValue(labels, "status"); s == "pending" || s == "fetched" {
				out.SelfCommandPending += int64(value)
			}
		case selfCmdSnapMetric:
			out.SelfCommandSnapshotUnix = value
		case durationMetric + "_bucket":
			route := labelValue(labels, "route")
			upper, err := strconv.ParseFloat(labelValue(labels, "le"), 64)
			if err != nil {
				continue
			}
			if buckets[route] == nil {
				buckets[route] = map[float64]int64{}
			}
			buckets[route][upper] = int64(value)
		case durationMetric + "_count":
			out.RouteCount[labelValue(labels, "route")] += int64(value)
		}
	}
	for route, byUpper := range buckets {
		points := make([]bucketPoint, 0, len(byUpper))
		for upper, cum := range byUpper {
			points = append(points, bucketPoint{Upper: upper, Cum: cum})
		}
		sort.Slice(points, func(i, j int) bool { return points[i].Upper < points[j].Upper })
		out.RouteHistogram[route] = points
	}
	return out
}

// floatSample 解析一行 Prometheus 样本，返回指标名 / 标签串 / 浮点值。
// 与 loadtest_test.go 的 parseSample 分开：那个把值截断成 int，会把 gc 分位数与秒级时延压平。
func floatSample(line string) (name, labels string, value float64, ok bool) {
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", 0, false
	}
	spaceIdx := strings.LastIndexByte(line, ' ')
	if spaceIdx < 0 {
		return "", "", 0, false
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(line[spaceIdx+1:]), 64)
	if err != nil {
		return "", "", 0, false
	}
	nameAndLabels := line[:spaceIdx]
	name, labels = nameAndLabels, ""
	if openIdx := strings.IndexByte(nameAndLabels, '{'); openIdx >= 0 {
		if !strings.HasSuffix(nameAndLabels, "}") {
			return "", "", 0, false
		}
		name = nameAndLabels[:openIdx]
		labels = nameAndLabels[openIdx+1 : len(nameAndLabels)-1]
	}
	return name, labels, parsed, true
}

// poolDelta 是 DB 连接池在两次自观测快照之间的增量。
// WaitCount 增量 > 0 就是「有请求真的没拿到连接、排过队」的直接证据；
// 这是区分「控制面内部瓶颈」与「宿主机 CPU 饱和」最硬的指标——CPU 饱和时池不会排队。
type poolDelta struct {
	// Valid 为 false 时说明前后两次快照没有都取到，增量不可用（不是 0，是未知）。
	Valid bool `json:"valid"`
	// WaitCount / WaitDurationMs 是档位内增量（after - before），两个计数器在 sql.DBStats 里单调递增。
	WaitCount      int64 `json:"waitCount"`
	WaitDurationMs int64 `json:"waitDurationMs"`
	// OpenAfter / InUseAfter 是档位**结束时**的瞬时值（不是窗口峰值——快照只取前后两次，
	// 峰值要靠高频轮询，那本身会干扰被测系统）。判断是否池枯竭看 InUseAfter 是否顶到 MaxOpen。
	OpenAfter  int `json:"openAfter"`
	InUseAfter int `json:"inUseAfter"`
	MaxOpen    int `json:"maxOpen"`
	// RegistryTotal 是档位结束后控制面自报的注册表规模（交叉验证规模变量真的生效）。
	RegistryTotal int `json:"registryTotal"`
}

// subtractPool 用 after - before 得到增量；两次快照缺任一次即标记为不可用。
// 必须显式标记有效性：否则「档位前抓取失败」会被算成 before=0，
// 于是增量 = 进程启动至今的累计值，看起来像一次巨大的池排队——这是纯粹的假证据。
func subtractPool(before, after observabilitySnapshot, ok bool) poolDelta {
	if !ok {
		return poolDelta{Valid: false}
	}
	return poolDelta{
		Valid:          true,
		WaitCount:      after.DBPool.WaitCount - before.DBPool.WaitCount,
		WaitDurationMs: after.DBPool.WaitDurationMs - before.DBPool.WaitDurationMs,
		OpenAfter:      after.DBPool.OpenConnections,
		InUseAfter:     after.DBPool.InUse,
		MaxOpen:        after.DBPool.MaxOpenConnections,
		RegistryTotal:  after.RegistryTotal,
	}
}

// fetchObservabilityRaw 抓一次自观测快照原始结构（既有 fetchObservability 只回格式化文本）。
func fetchObservabilityRaw(ctx context.Context, client *http.Client, baseURL, token string) (observabilitySnapshot, error) {
	var snapshot observabilitySnapshot
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/admin/v1/system/observability", nil)
	if err != nil {
		return snapshot, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return snapshot, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return snapshot, fmt.Errorf("自观测端点返回 %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

// ---- 有界并发注册 ----

// registerOutcome 是一次批量注册的结果。
type registerOutcome struct {
	IDs       []string      `json:"ids"`
	Requested int           `json:"requested"`
	Succeeded int           `json:"succeeded"`
	Elapsed   time.Duration `json:"-"`
	ElapsedMs int64         `json:"elapsedMs"`
	RPS       float64       `json:"rps"`
	// Failures 是失败明细（服务端 ID + 状态码 / 错误），全成功时为空。
	Failures []string `json:"failures,omitempty"`
	// Resources 是注册阶段控制面与压测端的资源口径。
	Resources resourceSummary `json:"resources"`
}

// registerVirtualAgentsConcurrent 以有界并发注册一批虚拟 serverId。
//
// 为什么必须并发：注册不是单纯写内存——每个 serverId 要过 4~5 次 DB 往返
// （namespace 行、offline 拒绝表、placement 归属表、审计写入），并在 4 连接的 sqlite 池上排队。
// 串行注册 1000 台 = 1000 次串行 RTT，前置耗时会把整个压测拖到不可用；并发只改「在途请求数」，
// 不改注册语义与结果集合。
//
// 并发度必须**有界**：无界并发会让 1000 个请求同时抢 4 个连接，把注册期变成一次
// 连接池雪崩（大量 DB_WAIT_TIMEOUT），测出来的不是控制面能力而是排队长度。
func registerVirtualAgentsConcurrent(
	t *testing.T, client *http.Client, baseURL string, count, concurrency int,
	cpPID int, genPID int,
) registerOutcome {
	t.Helper()
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > count {
		concurrency = count
	}

	// 注册用独立客户端：连接池必须 ≥ 并发度，否则压测端的 MaxIdleConnsPerHost 会先成为瓶颈。
	regClient := newLoadClient(concurrency)
	defer regClient.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(context.Background(), registerTotalTimeout)
	defer cancel()

	sampler := startResourceSampler(map[string]int{"控制面": cpPID, "压测端": genPID}, time.Second)

	outcome := registerOutcome{Requested: count, IDs: make([]string, count)}
	ids := make([]string, count)
	failures := make([]string, count)
	started := time.Now()

	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for i := range count {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			serverID := "loadtest-agent-" + strconv.Itoa(idx)
			ids[idx] = serverID
			// 单请求独立预算：批总预算之外再压一层，避免个别卡死请求吃光整批时间。
			reqCtx, reqCancel := context.WithTimeout(ctx, registerRequestTimeout)
			defer reqCancel()
			status, snippet, err := registerOne(reqCtx, regClient, baseURL, serverID, idx)
			if err != nil {
				failures[idx] = fmt.Sprintf("%s：请求失败 %v", serverID, err)
				return
			}
			if status != http.StatusOK {
				failures[idx] = fmt.Sprintf("%s：期望 %d 实际 %d，响应片段 %s",
					serverID, http.StatusOK, status, snippet)
			}
		}(i)
	}
	wg.Wait()
	outcome.Elapsed = time.Since(started)
	outcome.Resources = summarizeResources(sampler.Stop())

	okIDs := make([]string, 0, count)
	for i := range count {
		if failures[i] != "" {
			outcome.Failures = append(outcome.Failures, failures[i])
			continue
		}
		okIDs = append(okIDs, ids[i])
	}
	outcome.IDs = okIDs
	outcome.Succeeded = len(okIDs)
	outcome.ElapsedMs = outcome.Elapsed.Milliseconds()
	if outcome.Elapsed > 0 {
		outcome.RPS = float64(outcome.Succeeded) / outcome.Elapsed.Seconds()
	}
	return outcome
}

// ---- 场景集合裁剪 ----

// newScenarioSetFrom 由给定场景切片构造场景集合（重建加权轮转序列）。
// 用于从完整混合负载里裁出「单类别」负载集，把心跳 / 只读 / v2 上报三类分别压出各自的容量，
// 否则混合负载的总 RPS 无法回答「1000 台能撑多少心跳 QPS」。
func newScenarioSetFrom(items []scenario) *scenarioSet {
	set := &scenarioSet{items: items}
	for i := range items {
		for range items[i].Weight {
			set.rotation = append(set.rotation, i)
		}
	}
	return set
}

// filterScenariosByGroup 按分组名裁剪场景集合（保持原顺序），未命中的分组被丢弃。
func filterScenariosByGroup(set *scenarioSet, groups ...string) *scenarioSet {
	want := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		want[g] = struct{}{}
	}
	items := make([]scenario, 0, len(set.items))
	for i := range set.items {
		if _, ok := want[set.items[i].Group]; ok {
			items = append(items, set.items[i])
		}
	}
	return newScenarioSetFrom(items)
}

// filterScenariosByName 按端点名裁剪场景集合（单端点隔离用）。
func filterScenariosByName(set *scenarioSet, names ...string) *scenarioSet {
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		want[n] = struct{}{}
	}
	items := make([]scenario, 0, len(set.items))
	for i := range set.items {
		if _, ok := want[set.items[i].Name]; ok {
			items = append(items, set.items[i])
		}
	}
	return newScenarioSetFrom(items)
}

// numCPU 返回宿主机逻辑核数（压测端可见的并行度上限）。
func numCPU() int { return runtime.NumCPU() }

// registerConcurrencyFromEnv 读注册并发度环境变量，缺失取 1（保持既有串行行为不变）。
func registerConcurrencyFromEnv() int {
	raw := os.Getenv("LOADTEST_REGISTER_CONCURRENCY")
	if raw == "" {
		return defaultRegisterConcurrency
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return defaultRegisterConcurrency
	}
	return value
}
