//go:build e2e && loadtest

// 压测报告生成（压测 harness 的「怎么出结论」部分）：Markdown 给人看，JSON 给流水线消费。
package loadtest_e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// scenarioRow 是报告里的场景清单行（端点 / 方法 / 权重 / 期望状态码 / 鉴权）。
type scenarioRow struct {
	Group      string `json:"group"`
	Name       string `json:"name"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Weight     int    `json:"weight"`
	WantStatus int    `json:"wantStatus"`
	Auth       string `json:"auth"`
}

// observation 是一次旁路观测（/metrics 摘要与 /admin/v1/system/observability 快照，尽力而为）。
type observation struct {
	At     string `json:"at"`
	Phase  string `json:"phase"`
	Target string `json:"target"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Error  string `json:"error,omitempty"`
}

// soakReport 是 soak 档结果：整体聚合 + 分段采样（默认每段 1 分钟，即逐分钟采样）。
type soakReport struct {
	Concurrent int           `json:"concurrency"`
	DurationMs int64         `json:"durationMs"`
	BucketMs   int64         `json:"bucketMs"`
	Total      *loadResult   `json:"total"`
	Segments   []bucketStats `json:"segments"`
}

// suspendedReport 是长轮询 / SSE 挂起探测的结果（口径与 RPS 型压测完全不同，故单独一节）。
// 挂起端点衡量三件事：能同时挂多少、挂得稳不稳、变更多久能推下去——没有「越快越好」。
type suspendedReport struct {
	Kind              string       `json:"kind"` // 长轮询 or SSE
	URL               string       `json:"url"`
	Held              int          `json:"held"`
	Connected         int          `json:"connected"`
	CompletedByWake   int          `json:"completedByWake"`
	CompletedByTimeout int         `json:"completedByTimeout"`
	Errors            int          `json:"errors"`
	ErrorKinds        map[string]int `json:"errorKinds,omitempty"`
	HoldLatencyMs     latencyStats `json:"holdLatencyMs"`
	WakeLatencyMs     latencyStats `json:"wakeLatencyMs"`
	SuspendedObserved int          `json:"suspendedObserved"`
	Notes             []string     `json:"notes,omitempty"`
}

// ingestProbeReport 是 v2 上报通道背压探测的结果：回答 spec 里
// 「连接/消息上报接口 p99 响应仅含队列入队耗时（队列满 429）」这句话是否可达。
type ingestProbeReport struct {
	URL             string         `json:"url"`
	Requests        int            `json:"requests"`
	BatchSize       int            `json:"batchSize"`
	Concurrency     int            `json:"concurrency"`
	StatusCounts    map[int]int    `json:"statusCounts"`
	SawBackpressure bool           `json:"sawBackpressure"` // 见到 429 = 有界队列真的会拒
	SawUnavailable  bool           `json:"sawUnavailable"`  // 见到 503 = 等 DB 连接超时被触发
	DurationMs      int64          `json:"durationMs"`
	Error           string         `json:"error,omitempty"`
}

// report 是一次完整压测（分级 + soak）的产物。
type report struct {
	GeneratedAt   string             `json:"generatedAt"`
	BaseURL       string             `json:"baseUrl"`
	RepoRoot      string             `json:"repoRoot"`
	BinaryVersion string             `json:"binaryVersion"`
	GoVersion     string             `json:"goVersion"`
	Scenarios     []scenarioRow      `json:"scenarios"`
	Levels        []*loadResult      `json:"levels"`
	Soak          *soakReport        `json:"soak,omitempty"`
	Suspended     []suspendedReport  `json:"suspended,omitempty"`
	IngestProbe   *ingestProbeReport `json:"ingestProbe,omitempty"`
	Observations  []observation      `json:"observations"`
	Notes         []string           `json:"notes"`
}

// write 把报告写进 dir（Markdown + JSON 同名双份），返回两个文件的完整路径。
func (r *report) write(dir, stamp string) (string, string, error) {
	base := filepath.Join(dir, "loadtest-"+stamp)
	jsonPath := base + ".json"
	mdPath := base + ".md"
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("编码压测报告 JSON 失败：%w", err)
	}
	if err := os.WriteFile(jsonPath, append(raw, '\n'), 0o644); err != nil {
		return "", "", fmt.Errorf("写压测报告 JSON 失败：%w", err)
	}
	if err := os.WriteFile(mdPath, []byte(r.markdown()), 0o644); err != nil {
		return "", "", fmt.Errorf("写压测报告 Markdown 失败：%w", err)
	}
	return mdPath, jsonPath, nil
}

// markdown 渲染报告的 Markdown 文本（控制台只打摘要，明细全在这里）。
func (r *report) markdown() string {
	var b strings.Builder
	b.WriteString("# 控制面 HTTP 压测报告\n\n")
	writeKeyValues(&b, [][2]string{
		{"生成时间", r.GeneratedAt},
		{"目标地址", r.BaseURL},
		{"控制面二进制版本", r.BinaryVersion},
		{"运行环境", r.GoVersion},
		{"仓库根", r.RepoRoot},
	})
	writeScenarioTable(&b, r.Scenarios)
	writeLevelTable(&b, r.Levels)
	writeEndpointTables(&b, r.Levels)
	writeSoakSection(&b, r.Soak)
	writeSuspendedSection(&b, r.Suspended)
	writeIngestProbeSection(&b, r.IngestProbe)
	writeObservationTable(&b, r.Observations)
	writeNotes(&b, r.Notes)
	return b.String()
}

// summaryLines 返回控制台摘要行（明细落文件，控制台只给结论）。
func (r *report) summaryLines() []string {
	lines := []string{
		fmt.Sprintf("压测目标：%s（控制面版本 %s）", r.BaseURL, r.BinaryVersion),
		fmt.Sprintf("%-14s %10s %10s %8s %8s %9s %9s %9s %9s %9s", "并发/时长", "总请求", "失败", "错误率", "RPS", "p50(ms)", "p95(ms)", "p99(ms)", "max(ms)", "采样数"),
	}
	for i := range r.Levels {
		level := r.Levels[i]
		lines = append(lines, fmt.Sprintf("%-14s %10d %10d %7.2f%% %8.1f %9.2f %9.2f %9.2f %9.2f %9d",
			level.Label, level.TotalRequests, level.Failed, level.ErrorRate*100, level.RPS,
			level.Latency.P50Ms, level.Latency.P95Ms, level.Latency.P99Ms, level.Latency.MaxMs, level.Sampled))
	}
	if r.Soak != nil {
		lines = append(lines, fmt.Sprintf("soak（并发 %d / %s，每段 %s）分段采样：",
			r.Soak.Concurrent, msToText(r.Soak.DurationMs), msToText(r.Soak.BucketMs)))
		for i := range r.Soak.Segments {
			segment := &r.Soak.Segments[i]
			lines = append(lines, fmt.Sprintf("  第 %d 段（起点 %s）：请求 %d 失败 %d 错误率 %.2f%% RPS %.1f p50 %.2f p95 %.2f p99 %.2f max %.2f",
				segment.Index+1, msToText(segment.OffsetMs), segment.Requests, segment.Failed,
				segment.ErrorRate*100, segment.RPS,
				segment.Latency.P50Ms, segment.Latency.P95Ms, segment.Latency.P99Ms, segment.Latency.MaxMs))
		}
	}
	for i := range r.Suspended {
		s := &r.Suspended[i]
		lines = append(lines, fmt.Sprintf("挂起探测[%s] 目标%s 挂起%d 已连%d 唤醒%d 超时%d 错误%d 服务端峰值%d 唤醒时延 p50 %.2f / p95 %.2f / p99 %.2f",
			s.Kind, s.URL, s.Held, s.Connected, s.CompletedByWake, s.CompletedByTimeout,
			s.Errors, s.SuspendedObserved, s.WakeLatencyMs.P50Ms, s.WakeLatencyMs.P95Ms, s.WakeLatencyMs.P99Ms))
	}
	if r.IngestProbe != nil {
		p := r.IngestProbe
		lines = append(lines, fmt.Sprintf("上报背压探测 目标%s 请求%d×批量%d 并发%d 状态%s 触429=%t 触503=%t",
			p.URL, p.Requests, p.BatchSize, p.Concurrency,
			statusText(intCountsToString(p.StatusCounts)), p.SawBackpressure, p.SawUnavailable))
	}
	for i := range r.Observations {
		obs := &r.Observations[i]
		lines = append(lines, fmt.Sprintf("旁路观测[%s] %s：%s", obs.Phase, obs.Target, observationText(obs)))
	}
	return lines
}

// observationText 把观测结果压成一行（失败时给出原因，不影响主流程）。
func observationText(obs *observation) string {
	if obs.Error != "" {
		return "失败（" + obs.Error + "）"
	}
	return obs.Detail
}

// writeKeyValues 写报告的键值头部。
func writeKeyValues(b *strings.Builder, pairs [][2]string) {
	for _, pair := range pairs {
		fmt.Fprintf(b, "- %s：%s\n", pair[0], pair[1])
	}
	b.WriteString("\n")
}

// writeScenarioTable 写场景清单表。
func writeScenarioTable(b *strings.Builder, rows []scenarioRow) {
	b.WriteString("## 压测场景\n\n")
	b.WriteString("| 分组 | 端点 | 方法 | 路径 | 权重 | 期望状态码 | 鉴权 |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
	for i := range rows {
		row := &rows[i]
		fmt.Fprintf(b, "| %s | %s | %s | `%s` | %d | %d | %s |\n",
			row.Group, row.Name, row.Method, row.Path, row.Weight, row.WantStatus, row.Auth)
	}
	b.WriteString("\n")
}

// writeLevelTable 写分级并发结果表。
func writeLevelTable(b *strings.Builder, levels []*loadResult) {
	b.WriteString("## 分级并发结果\n\n")
	b.WriteString("| 并发 | 实际时长 | 总请求 | 成功 | 失败 | 网络错误 | 错误率 | RPS | p50(ms) | p90(ms) | p95(ms) | p99(ms) | max(ms) | avg(ms) |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for i := range levels {
		level := levels[i]
		fmt.Fprintf(b, "| %d | %s | %d | %d | %d | %d | %.2f%% | %.1f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f |\n",
			level.Concurrent, msToText(level.DurationMs), level.TotalRequests, level.Success, level.Failed,
			level.NetworkErrors, level.ErrorRate*100, level.RPS,
			level.Latency.P50Ms, level.Latency.P90Ms, level.Latency.P95Ms, level.Latency.P99Ms,
			level.Latency.MaxMs, level.Latency.AvgMs)
	}
	b.WriteString("\n")
}

// writeEndpointTables 写各档的端点明细表（定位退化到具体端点用）。
// 时延列只覆盖被采样的请求，故「采样」列与「请求」列并列：两者不等即说明分位数不是全量口径。
func writeEndpointTables(b *strings.Builder, levels []*loadResult) {
	b.WriteString("### 端点明细\n\n")
	for i := range levels {
		level := levels[i]
		fmt.Fprintf(b, "#### %s\n\n", level.Label)
		b.WriteString("| 分组 | 端点 | 请求 | 成功 | 失败 | 网络错误 | 错误率 | p50(ms) | p95(ms) | p99(ms) | max(ms) | 采样 | 状态码分布 |\n")
		b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for j := range level.Endpoints {
			endpoint := &level.Endpoints[j]
			fmt.Fprintf(b, "| %s | %s | %d | %d | %d | %d | %.2f%% | %.2f | %.2f | %.2f | %.2f | %d | %s |\n",
				endpoint.Group, endpoint.Name, endpoint.Requests, endpoint.Success, endpoint.Failed,
				endpoint.NetworkErrors, endpoint.ErrorRate*100,
				endpoint.Latency.P50Ms, endpoint.Latency.P95Ms, endpoint.Latency.P99Ms, endpoint.Latency.MaxMs,
				endpoint.Latency.Count, statusText(endpoint.StatusCounts))
		}
		writeEndpointSampleNote(b, level)
		b.WriteString("\n")
	}
}

// writeEndpointSampleNote 在端点明细表下补口径说明：有端点采样不足时点名，避免把分位数当成全量口径。
func writeEndpointSampleNote(b *strings.Builder, level *loadResult) {
	partial := make([]string, 0, len(level.Endpoints))
	for i := range level.Endpoints {
		endpoint := &level.Endpoints[i]
		if endpoint.Latency.Count < endpoint.Requests {
			partial = append(partial, fmt.Sprintf("%s（%d/%d）", endpoint.Name, endpoint.Latency.Count, endpoint.Requests))
		}
	}
	fmt.Fprintf(b, "\n> 时延分位数口径：每个端点各有一份独立的采样配额（上限 %d 个样本/端点），只用该端点「采样」列对应的请求计算；「采样」= 「请求」时为全量覆盖。\n", maxEndpointLatencySamples)
	if len(partial) > 0 {
		fmt.Fprintf(b, "> 本档以下端点采样不足，分位数未覆盖全部请求：%s。\n", strings.Join(partial, "、"))
	}
}

// writeSoakSection 写 soak 档的分段采样表（默认每段 1 分钟，即逐分钟采样）。
func writeSoakSection(b *strings.Builder, soak *soakReport) {
	if soak == nil {
		return
	}
	fmt.Fprintf(b, "## soak 分段采样（并发 %d，总时长 %s，每段 %s）\n\n",
		soak.Concurrent, msToText(soak.DurationMs), msToText(soak.BucketMs))
	b.WriteString("| 段 | 起点(ms) | 跨度(ms) | 请求 | 失败 | 错误率 | RPS | p50(ms) | p95(ms) | p99(ms) | max(ms) |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for i := range soak.Segments {
		segment := &soak.Segments[i]
		fmt.Fprintf(b, "| %d | %d | %d | %d | %d | %.2f%% | %.1f | %.2f | %.2f | %.2f | %.2f |\n",
			segment.Index+1, segment.OffsetMs, segment.SpanMs, segment.Requests, segment.Failed,
			segment.ErrorRate*100, segment.RPS, segment.Latency.P50Ms, segment.Latency.P95Ms,
			segment.Latency.P99Ms, segment.Latency.MaxMs)
	}
	b.WriteString("\n")
}

// writeSuspendedSection 写长轮询 / SSE 挂起探测结果。
// 这一节的数字没有 RPS 列：挂起端点「挂得久」是正常而非慢，衡量的是挂起容量与唤醒时延。
func writeSuspendedSection(b *strings.Builder, items []suspendedReport) {
	if len(items) == 0 {
		return
	}
	b.WriteString("## 长轮询 / SSE 挂起探测\n\n")
	b.WriteString("挂起型端点不能用 RPS 衡量（挂起 30s 是设计如此）。此处衡量三件事：能同时挂多少、挂得稳不稳、变更多久能推下去。\n\n")
	b.WriteString("| 类型 | 目标 | 挂起数 | 已连接 | 唤醒返回 | 超时返回 | 网络错误 | 服务端观测挂起峰值 | 挂起时延 p95(ms) | 唤醒时延 p95(ms) |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for i := range items {
		s := &items[i]
		fmt.Fprintf(b, "| %s | %s | %d | %d | %d | %d | %d | %d | %.2f | %.2f |\n",
			s.Kind, s.URL, s.Held, s.Connected, s.CompletedByWake, s.CompletedByTimeout,
			s.Errors, s.SuspendedObserved, s.HoldLatencyMs.P95Ms, s.WakeLatencyMs.P95Ms)
	}
	b.WriteString("\n")
	for i := range items {
		s := &items[i]
		for _, note := range s.Notes {
			fmt.Fprintf(b, "> **%s** %s\n", s.Kind, note)
		}
	}
	b.WriteString("\n")
}

// writeIngestProbeSection 写 v2 上报通道背压探测结果。
// 关键不是「有没有 429」，而是「429/503 这条边界到底可不可达」——spec 声明了背压语义，就得有证据。
func writeIngestProbeSection(b *strings.Builder, p *ingestProbeReport) {
	if p == nil {
		return
	}
	fmt.Fprintf(b, "## v2 上报通道背压探测（%s）\n\n", p.URL)
	b.WriteString("| 项 | 值 |\n| --- | --- |\n")
	fmt.Fprintf(b, "| 请求数 × 批量 | %d × %d |\n", p.Requests, p.BatchSize)
	fmt.Fprintf(b, "| 并发 | %d |\n", p.Concurrency)
	fmt.Fprintf(b, "| 耗时(ms) | %d |\n", p.DurationMs)
	fmt.Fprintf(b, "| 状态码分布 | %s |\n", statusText(intCountsToString(p.StatusCounts)))
	fmt.Fprintf(b, "| 触到 429（有界队列拒收） | %t |\n", p.SawBackpressure)
	fmt.Fprintf(b, "| 触到 503（等 DB 连接超时） | %t |\n", p.SawUnavailable)
	if p.Error != "" {
		fmt.Fprintf(b, "| 探测失败原因 | %s |\n", p.Error)
	}
	b.WriteString("\n")
	if !p.SawBackpressure && !p.SawUnavailable {
		b.WriteString("> 未触到背压边界：说明当前批量与并发下，入队通道与落库速率仍有余量。" +
			"这**不是**「背压不存在」的证据——spec 的 4096 有界队列与 429 语义需用更大批量 / 更高并发才可能触及。\n\n")
	}
}

// intCountsToString 把 int 键的计数表转成字符串键，复用报告既有的 statusText 渲染。
func intCountsToString(counts map[int]int) map[string]int {
	out := make(map[string]int, len(counts))
	for code, n := range counts {
		out[strconv.Itoa(code)] = n
	}
	return out
}

// writeObservationTable 写旁路观测表（/metrics 与自观测端点，尽力而为）。
func writeObservationTable(b *strings.Builder, observations []observation) {
	if len(observations) == 0 {
		return
	}
	b.WriteString("## 旁路观测\n\n")
	b.WriteString("| 阶段 | 目标 | HTTP | 摘要 |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for i := range observations {
		obs := &observations[i]
		fmt.Fprintf(b, "| %s | %s | %d | %s |\n", obs.Phase, obs.Target, obs.Status, observationText(obs))
	}
	b.WriteString("\n")
}

// writeNotes 写报告说明（口径与已知取舍，避免数字被误读）。
func writeNotes(b *strings.Builder, notes []string) {
	if len(notes) == 0 {
		return
	}
	b.WriteString("## 说明\n\n")
	for _, note := range notes {
		fmt.Fprintf(b, "- %s\n", note)
	}
	b.WriteString("\n")
}

// statusText 把状态码分布压成 `200×12, 404×1` 形式（空分布给出占位）。
func statusText(counts map[string]int) string {
	if len(counts) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"×"+strconv.Itoa(counts[key]))
	}
	return strings.Join(parts, ", ")
}

// msToText 把毫秒渲染成 `30s` / `5m0s` 这类人读文本。
func msToText(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).String()
}
