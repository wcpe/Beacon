//go:build e2e && loadtest

// 并发压测引擎（压测 harness 的「怎么压」部分）。
//
// 设计取舍：
//   - 固定 worker 池（concurrency 个 goroutine）在 duration 内持续发请求，不做动态扩缩；
//   - 时延采样用「精确样本 + 有界配额」而非固定桶直方图：分位数在排序后的真实样本上做线性插值，
//     不被桶边界抹平；配额保证超长 soak 也不会把内存压爆，超出后继续发压与计数只是不再采样；
//     带时间桶的档位（soak）按桶各分一份配额，否则前段流量会耗尽全局配额、后段分位数恒为 0；
//     端点级分位数另有每端点独立配额（见 maxEndpointLatencySamples），刻意与整体 / 桶内配额分开，
//     否则两侧会互相挤占——SLA 是端点级的，整体分位数无法替代；
//   - 只输出聚合结果，绝不逐请求打印（否则测试输出会被淹没，也测不准）。
package loadtest_e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// maxLatencySamples 是单档压测保留的时延样本总预算（8 字节 × 100 万 ≈ 8MB）。
	// 无时间桶时由全局单一配额消耗；有时间桶时按桶均分，保证每个采样区间都有分位数可读。
	maxLatencySamples = 1_000_000
	// maxEndpointLatencySamples 是「每个端点」独立的时延样本配额（8 字节 × 20 万 ≈ 1.6MB/端点）。
	// 必须与 maxLatencySamples 分开，不能共用一份闸：共用会互相挤占——要么端点采样吃掉整体配额，
	// 要么整体采样先耗尽后端点只能拿到残样，两侧分位数都失真。
	// 端点是 SLA 的定义单位（内存读与 DB 读的时延画像完全不同），故值得按端点各留一份预算；
	// 6 个端点合计约 120 万个样本 ≈ 10MB，可接受。
	maxEndpointLatencySamples = 200_000
	// guardCheckInterval 是每多少个请求检查一次控制面早退 / 运行期取消（摊薄检查成本）。
	guardCheckInterval = 64
)

// ProcessGuard 描述压测期间可观察的外部进程生命周期。
// 与 e2e harness 的 ProcessGuard 同形（Done + CheckEarlyExit），按结构匹配接收 *harness.ControlPlane，
// 压测引擎无需 import harness 的具体类型。
type ProcessGuard interface {
	Done() <-chan struct{}
	CheckEarlyExit() error
}

// driverConfig 是单档压测的运行参数。
type driverConfig struct {
	// label 是档位标签（如「并发 32 / 30s」），仅用于结果回填与报告。
	label string
	// client 是压测自建客户端：独立 Transport、连接池按压测并发调优。
	// 不复用 harness 内部的 http.DefaultClient（其连接池未调优，只适合就绪探测 / 登录这类一次性请求）。
	client     *http.Client
	scenarios  *scenarioSet
	concurrent int
	duration   time.Duration
	// requestTimeout 是单请求上限；到期即记一次网络错误，避免个别卡死请求挂住整档。
	requestTimeout time.Duration
	// bucketInterval > 0 时按该间隔分桶统计（soak 靠它做逐分钟采样）；0 表示不分桶。
	bucketInterval time.Duration
	guard          ProcessGuard
}

// latencyStats 是时延分位统计（单位毫秒）。百分位取排序后线性插值，不是桶近似。
type latencyStats struct {
	Count int     `json:"count"`
	AvgMs float64 `json:"avgMs"`
	MinMs float64 `json:"minMs"`
	P50Ms float64 `json:"p50Ms"`
	P90Ms float64 `json:"p90Ms"`
	P95Ms float64 `json:"p95Ms"`
	P99Ms float64 `json:"p99Ms"`
	MaxMs float64 `json:"maxMs"`
}

// endpointStats 是单端点聚合。
// Latency 用该端点专属的采样配额算出（口径与整体分位数一致，但样本互不挤占），
// 端点是 SLA 的定义单位：内存读与 DB 读的时延画像不同，只有端点级分位数才能拿来定接口 SLA。
type endpointStats struct {
	Group         string         `json:"group"`
	Name          string         `json:"name"`
	Method        string         `json:"method"`
	Path          string         `json:"path"`
	Requests      int            `json:"requests"`
	Success       int            `json:"success"`
	Failed        int            `json:"failed"`
	NetworkErrors int            `json:"networkErrors"`
	ErrorRate     float64        `json:"errorRate"`
	StatusCounts  map[string]int `json:"statusCounts"`
	// Latency 的 Count 即该端点进入分位统计的样本数，恒 ≤ Requests（超配额后只计数不再采样）。
	Latency latencyStats `json:"latency"`
}

// bucketStats 是分桶统计（soak 逐分钟采样）：桶内独立算 RPS / 错误率 / 分位数。
type bucketStats struct {
	Index     int          `json:"index"`
	OffsetMs  int64        `json:"offsetMs"`
	SpanMs    int64        `json:"spanMs"`
	Requests  int          `json:"requests"`
	Failed    int          `json:"failed"`
	ErrorRate float64      `json:"errorRate"`
	RPS       float64      `json:"rps"`
	Latency   latencyStats `json:"latency"`
}

// loadResult 是一档压测的聚合结果。
type loadResult struct {
	Label         string  `json:"label"`
	Concurrent    int     `json:"concurrency"`
	DurationMs    int64   `json:"durationMs"`
	StartedAt     string  `json:"startedAt"`
	TotalRequests int     `json:"totalRequests"`
	Success       int     `json:"success"`
	Failed        int     `json:"failed"`
	NetworkErrors int     `json:"networkErrors"`
	ErrorRate     float64 `json:"errorRate"`
	RPS           float64 `json:"rps"`
	// Sampled 是实际进入分位统计的请求数（受 maxLatencySamples 约束），恒 ≤ TotalRequests。
	Sampled   int             `json:"sampled"`
	Latency   latencyStats    `json:"latency"`
	Endpoints []endpointStats `json:"endpoints"`
	Buckets   []bucketStats   `json:"buckets,omitempty"`
}

// latencySampler 是全局有界的时延采样闸：只有前 limit 个请求会进样本，之后只计数。
type latencySampler struct {
	limit int64
	taken atomic.Int64
}

// tryAcquire 占用一个采样位；返回 true 表示本次请求的耗时应被收进样本。
func (s *latencySampler) tryAcquire() bool {
	return s.taken.Add(1) <= s.limit
}

// endpointAgg 是单 worker 对单端点的计数与时延样本（状态码分布按需分配 map）。
type endpointAgg struct {
	requests  int
	success   int
	failed    int
	netErrors int
	statuses  map[int]int
	latencies []int64
}

// bucketAgg 是单 worker 在单个时间桶内的计数与时延样本。
type bucketAgg struct {
	requests  int
	failed    int
	latencies []int64
}

// workerAgg 是单 worker 的私有聚合：不共享、不加锁，避免压测热路径上的锁竞争。
type workerAgg struct {
	cfg     *driverConfig
	sampler *latencySampler
	// bucketSamplers 是各时间桶的专属采样闸（仅带时间桶的档位非 nil），下标与 buckets 一一对应。
	bucketSamplers []*latencySampler
	// endpointSamplers 是各端点的专属采样闸，下标与 endpoints / 场景定义一致。
	endpointSamplers []*latencySampler
	started          time.Time

	latencies []int64
	requests  int
	success   int
	failed    int
	netErrors int
	endpoints []endpointAgg
	buckets   []bucketAgg
}

// newWorkerAgg 分配单 worker 的私有聚合（按端点计数恒有，分桶仅在 soak 档分配）。
func newWorkerAgg(cfg *driverConfig, sampler *latencySampler, bucketSamplers, endpointSamplers []*latencySampler, started time.Time) workerAgg {
	agg := workerAgg{
		cfg: cfg, sampler: sampler, bucketSamplers: bucketSamplers,
		endpointSamplers: endpointSamplers, started: started,
		endpoints: make([]endpointAgg, len(cfg.scenarios.items)),
	}
	if len(bucketSamplers) > 0 {
		agg.buckets = make([]bucketAgg, len(bucketSamplers))
	}
	return agg
}

// bucketCount 返回时间桶数量（向上取整）。
// 用向上取整而非「整除加一」：档位时长正好是采样间隔整数倍时，后者会多出一个只装尾部几毫秒余量的桶，
// 该桶的 RPS 会被极短跨度放大成假峰值。
func bucketCount(cfg *driverConfig) int {
	if cfg.bucketInterval <= 0 {
		return 0
	}
	count := int((cfg.duration + cfg.bucketInterval - 1) / cfg.bucketInterval)
	if count < 1 {
		count = 1
	}
	return count
}

// newBucketSamplers 为每个时间桶分配独立采样配额（总预算仍为 maxLatencySamples，内存占用不变）。
// 必须按桶独立：全局单一配额会被 soak 前段流量耗尽，使后段——恰恰是观察时延漂移的关键区间——分位数恒为 0。
func newBucketSamplers(cfg *driverConfig) []*latencySampler {
	count := bucketCount(cfg)
	if count == 0 {
		return nil
	}
	perBucket := maxLatencySamples / count
	if perBucket < 1 {
		perBucket = 1
	}
	samplers := make([]*latencySampler, count)
	for i := range samplers {
		samplers[i] = &latencySampler{limit: int64(perBucket)}
	}
	return samplers
}

// newEndpointSamplers 为每个端点分配独立采样配额（每端点均分不到整体预算，互不挤占）。
// 端点采样不走时间桶：soak 的漂移由整体与桶内分位数刻画，端点级只回答「哪个端点的时延画像如何」。
func newEndpointSamplers(cfg *driverConfig) []*latencySampler {
	samplers := make([]*latencySampler, len(cfg.scenarios.items))
	for i := range samplers {
		samplers[i] = &latencySampler{limit: maxEndpointLatencySamples}
	}
	return samplers
}

// record 记一次请求结果：端点计数与成功判定恒记，时延样本受采样闸约束。
// 热路径上只做计数与「每 worker 私有切片 append」，共用闸仅原子自增——不加锁、不排序。
func (a *workerAgg) record(idx, want, status int, elapsed time.Duration, reqErr error) {
	a.requests++
	endpoint := &a.endpoints[idx]
	endpoint.requests++
	failed := true
	switch {
	case reqErr != nil:
		a.netErrors++
		endpoint.netErrors++
	case status == want:
		failed = false
		a.success++
		endpoint.success++
		endpoint.countStatus(status)
	default:
		endpoint.countStatus(status)
	}
	if failed {
		a.failed++
		endpoint.failed++
	}
	ns := int64(elapsed)
	// 端点样本走该端点的专属闸，与整体 / 桶内配额各自独立，两侧分位数不互相挤占。
	if a.endpointSamplers[idx].tryAcquire() {
		endpoint.latencies = append(endpoint.latencies, ns)
	}
	if a.buckets != nil {
		idx := a.bucketIndex()
		bucket := &a.buckets[idx]
		bucket.requests++
		if failed {
			bucket.failed++
		}
		// 桶内样本走该桶专属配额，保证每个采样区间都有分位数可读。
		if a.bucketSamplers[idx].tryAcquire() {
			a.latencies = append(a.latencies, ns)
			bucket.latencies = append(bucket.latencies, ns)
		}
		return
	}
	if a.sampler.tryAcquire() {
		a.latencies = append(a.latencies, ns)
	}
}

// bucketIndex 返回当前时刻所属的时间桶下标（末桶吸收尾部余量，避免越界）。
func (a *workerAgg) bucketIndex() int {
	idx := int(time.Since(a.started) / a.cfg.bucketInterval)
	if idx >= len(a.buckets) {
		return len(a.buckets) - 1
	}
	return idx
}

// countStatus 记一次状态码分布（仅在有 HTTP 响应时调用，网络错误无状态码）。
func (e *endpointAgg) countStatus(status int) {
	if e.statuses == nil {
		e.statuses = make(map[int]int, 4)
	}
	e.statuses[status]++
}

// runLoad 以固定 worker 池在 duration 内持续发压并返回聚合结果。
// 任一 worker 观察到控制面早退（或运行期取消）即停掉整档并返回该错误：否则「控制面已死」
// 会被压成 100% 错误率，看起来像性能问题而不是环境问题。
func runLoad(ctx context.Context, cfg *driverConfig) (*loadResult, error) {
	if cfg.concurrent < 1 {
		return nil, errors.New("并发数必须 ≥ 1")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sampler := &latencySampler{limit: maxLatencySamples}
	bucketSamplers := newBucketSamplers(cfg)
	endpointSamplers := newEndpointSamplers(cfg)
	started := time.Now()
	aggs := make([]workerAgg, cfg.concurrent)
	var wg sync.WaitGroup
	var failOnce sync.Once
	var failErr error

	for i := range aggs {
		aggs[i] = newWorkerAgg(cfg, sampler, bucketSamplers, endpointSamplers, started)
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if err := workerLoop(runCtx, cfg, uint64(idx), &aggs[idx]); err != nil {
				failOnce.Do(func() { failErr = err })
				cancel()
			}
		}(i)
	}
	wg.Wait()
	finished := time.Now()
	if failErr != nil {
		return nil, failErr
	}
	return mergeResult(cfg, aggs, started, finished), nil
}

// workerLoop 是单 worker 的发压循环：在档位时长内持续请求，直到到期、取消或控制面早退。
func workerLoop(ctx context.Context, cfg *driverConfig, workerID uint64, agg *workerAgg) error {
	deadline := time.Now().Add(cfg.duration)
	// 各 worker 从权重序列的不同位置起步，避免所有 worker 同相位地只打同一个端点。
	seq := workerID
	for time.Now().Before(deadline) {
		if seq%guardCheckInterval == 0 {
			if err := checkRun(ctx, cfg.guard); err != nil {
				return err
			}
		}
		idx, item := cfg.scenarios.pick(seq)
		elapsed, status, err := sendOnce(ctx, cfg, item, seq)
		agg.record(idx, item.WantStatus, status, elapsed, err)
		seq++
	}
	return nil
}

// sendOnce 发一次请求，返回端到端耗时（含响应体读尽）、状态码与网络错误。
// 响应体必须读尽再关闭，否则连接无法复用，压测会退化成「每请求一次 TCP 握手」。
func sendOnce(ctx context.Context, cfg *driverConfig, item *scenario, seq uint64) (time.Duration, int, error) {
	reqCtx, cancel := context.WithTimeout(ctx, cfg.requestTimeout)
	defer cancel()
	var body io.Reader
	if len(item.bodies) > 0 {
		body = bytes.NewReader(item.bodies[seq%uint64(len(item.bodies))])
	}
	req, err := http.NewRequestWithContext(reqCtx, item.Method, item.URL, body)
	if err != nil {
		return 0, 0, err
	}
	for _, h := range item.headers {
		req.Header.Set(h.key, h.value)
	}
	start := time.Now()
	resp, err := cfg.client.Do(req)
	if err != nil {
		return time.Since(start), 0, err
	}
	_, readErr := io.Copy(io.Discard, resp.Body)
	closeErr := resp.Body.Close()
	elapsed := time.Since(start)
	if readErr != nil {
		return elapsed, resp.StatusCode, readErr
	}
	if closeErr != nil {
		return elapsed, resp.StatusCode, closeErr
	}
	return elapsed, resp.StatusCode, nil
}

// checkRun 检查控制面是否早退、运行期上下文是否已被取消。
func checkRun(ctx context.Context, guard ProcessGuard) error {
	if guard != nil {
		if err := guard.CheckEarlyExit(); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("压测被取消：%w", err)
	}
	return nil
}

// mergeResult 把各 worker 的私有聚合并成档位结果（排序与插值只做一次）。
func mergeResult(cfg *driverConfig, aggs []workerAgg, started, finished time.Time) *loadResult {
	res := &loadResult{
		Label: cfg.label, Concurrent: cfg.concurrent,
		DurationMs: finished.Sub(started).Milliseconds(),
		StartedAt:  started.UTC().Format(time.RFC3339),
		Buckets:    mergeBuckets(cfg, aggs, finished.Sub(started)),
	}
	var latencies []int64
	for i := range aggs {
		agg := &aggs[i]
		res.TotalRequests += agg.requests
		res.Success += agg.success
		res.Failed += agg.failed
		res.NetworkErrors += agg.netErrors
		latencies = append(latencies, agg.latencies...)
	}
	res.ErrorRate = ratio(res.Failed, res.TotalRequests)
	res.RPS = rate(res.TotalRequests, finished.Sub(started))
	res.Latency = summarizeLatency(latencies)
	// Sampled 取实际进入整体分位统计的样本数：无桶档位等于全局闸配额，带桶档位由各桶配额累加。
	// 直接数合并后的样本最准——早期取全局闸计数，会让带桶档位（soak）恒报 0，与实际覆盖不符。
	res.Sampled = len(latencies)
	res.Endpoints = mergeEndpoints(cfg, aggs)
	return res
}

// mergeEndpoints 按端点合并各 worker 的计数与时延样本（顺序与场景定义一致）。
// 排序 / 插值只在合并期对每个端点各做一次，不在压测热路径上。
func mergeEndpoints(cfg *driverConfig, aggs []workerAgg) []endpointStats {
	items := cfg.scenarios.items
	stats := make([]endpointStats, len(items))
	latencies := make([][]int64, len(items))
	for i := range items {
		stats[i] = endpointStats{
			Group: items[i].Group, Name: items[i].Name,
			Method: items[i].Method, Path: items[i].Path,
			StatusCounts: make(map[string]int, 4),
		}
	}
	for a := range aggs {
		for i := range items {
			agg := &aggs[a].endpoints[i]
			stat := &stats[i]
			stat.Requests += agg.requests
			stat.Success += agg.success
			stat.Failed += agg.failed
			stat.NetworkErrors += agg.netErrors
			latencies[i] = append(latencies[i], agg.latencies...)
			for status, count := range agg.statuses {
				stat.StatusCounts[strconv.Itoa(status)] += count
			}
		}
	}
	for i := range stats {
		stats[i].ErrorRate = ratio(stats[i].Failed, stats[i].Requests)
		stats[i].Latency = summarizeLatency(latencies[i])
	}
	return stats
}

// mergeBuckets 合并各 worker 的时间桶（soak 逐分钟采样的来源）。
// 桶的 RPS 用该桶的实际跨度算，末桶可能不满一个采样间隔，不会被当成整间隔虚高。
func mergeBuckets(cfg *driverConfig, aggs []workerAgg, elapsed time.Duration) []bucketStats {
	if cfg.bucketInterval <= 0 {
		return nil
	}
	count := len(aggs[0].buckets)
	stats := make([]bucketStats, count)
	latencies := make([][]int64, count)
	for i := range stats {
		offset := time.Duration(i) * cfg.bucketInterval
		span := min(cfg.bucketInterval, elapsed-offset)
		if span < 0 {
			span = 0
		}
		stats[i] = bucketStats{Index: i, OffsetMs: offset.Milliseconds(), SpanMs: span.Milliseconds()}
	}
	for a := range aggs {
		for i := range aggs[a].buckets {
			bucket := &aggs[a].buckets[i]
			stats[i].Requests += bucket.requests
			stats[i].Failed += bucket.failed
			latencies[i] = append(latencies[i], bucket.latencies...)
		}
	}
	for i := range stats {
		stats[i].ErrorRate = ratio(stats[i].Failed, stats[i].Requests)
		stats[i].RPS = rate(stats[i].Requests, time.Duration(stats[i].SpanMs)*time.Millisecond)
		stats[i].Latency = summarizeLatency(latencies[i])
	}
	// 去掉尾部空桶：档位时长正好落在采样间隔边界时，会多出一个零请求的桶。
	for len(stats) > 1 && stats[len(stats)-1].Requests == 0 {
		stats = stats[:len(stats)-1]
	}
	return stats
}

// summarizeLatency 对样本排序后按线性插值算分位数：位置 = p×(n−1)，落在两样本之间按比例插值。
// 会就地排序传入切片（调用方传入的都是合并期新建的临时切片）。
func summarizeLatency(samples []int64) latencyStats {
	if len(samples) == 0 {
		return latencyStats{}
	}
	slices.Sort(samples)
	stats := latencyStats{
		Count: len(samples),
		MinMs: toMillis(samples[0]),
		P50Ms: toMillis(quantile(samples, 0.50)),
		P90Ms: toMillis(quantile(samples, 0.90)),
		P95Ms: toMillis(quantile(samples, 0.95)),
		P99Ms: toMillis(quantile(samples, 0.99)),
		MaxMs: toMillis(samples[len(samples)-1]),
	}
	var sum int64
	for _, ns := range samples {
		sum += ns
	}
	stats.AvgMs = toMillis(sum / int64(len(samples)))
	return stats
}

// quantile 返回升序样本中第 p 分位的线性插值结果（p ∈ [0,1]，返回纳秒）。
func quantile(sorted []int64, p float64) int64 {
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := p * float64(len(sorted)-1)
	lo := int(pos)
	if lo >= len(sorted)-1 {
		return sorted[len(sorted)-1]
	}
	frac := pos - float64(lo)
	delta := float64(sorted[lo+1]-sorted[lo]) * frac
	return sorted[lo] + int64(delta)
}

// toMillis 把纳秒转成毫秒（报告与 JSON 统一用毫秒）。
func toMillis(ns int64) float64 {
	return float64(ns) / float64(time.Millisecond)
}

// ratio 返回 part/total，total 为 0 时返回 0。
func ratio(part, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total)
}

// rate 返回 count/duration 的每秒速率，duration 非正时返回 0。
func rate(count int, duration time.Duration) float64 {
	if duration <= 0 {
		return 0
	}
	return float64(count) / duration.Seconds()
}
