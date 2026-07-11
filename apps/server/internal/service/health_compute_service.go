package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/metricwindow"
)

// RouteKindHealthSnapshot 是健康快照在异步日表写入通道中的路由键（FR-147，见 §4.3/§4.4）。
const RouteKindHealthSnapshot = "health_snapshot"

// 健康计算轮工程参数（§4.4：5s 一轮、每 6 轮 = 30s 全量快照；均为固定周期，非契约不入设置 store）。
const (
	// 计算轮周期
	healthComputeInterval = 5 * time.Second
	// 每多少轮产出一次全量快照（6 轮 × 5s = 30s）
	healthSnapshotEveryRounds = 6
	// 因子输入聚合窗口（最近 60s 接收的批）
	healthWindowMs = int64(60_000)
	// 超过多少毫秒无成功批判 lost（§4.2 活性判定权威）；窗口断供时沿用上一轮视图的上限同此值
	healthLostAfterMs = int64(30_000)
)

// healthFactsSource 是计算轮对 DB 事实读取的窄依赖（由 repository.HealthFactsRepository 实现，测试可替身）。
type healthFactsSource interface {
	ListAll() ([]repository.HealthFact, error)
}

// healthSnapshotEnqueuer 是计算轮对异步写入池的窄依赖：非阻塞投递一批快照行（测试可替身）。
type healthSnapshotEnqueuer interface {
	Enqueue(rows []model.HealthSnapshot) bool
}

// HealthSnapshotEnqueuer 把泛化异步日表写入通道绑定到 health_snapshot 路由（装配用）。
type HealthSnapshotEnqueuer struct {
	// Writer 泛化异步日表写入通道（须已注册 RouteKindHealthSnapshot 路由）。
	Writer *AsyncDailyWriter
}

// Enqueue 非阻塞投递一批快照行；队列满返回 false（快照丢本轮，不阻塞计算轮）。
func (e HealthSnapshotEnqueuer) Enqueue(rows []model.HealthSnapshot) bool {
	return EnqueueRows(e.Writer, RouteKindHealthSnapshot, rows)
}

// HealthComputeService 是健康计算轮（FR-147，见 §4.4）：每 5s 一轮——锁外读 DB 事实 →
// 逐实例聚合 60s 指标窗口 → 算因子 / score / level / schedulable → healthview.ReplaceAll 整批替换；
// 每 6 轮把全量视图转快照行经异步写入通道落 health_snapshot_YYYYMMDD（请求 / 计算 goroutine 不碰日表写）。
type HealthComputeService struct {
	facts    healthFactsSource
	window   *metricwindow.Store
	views    *healthview.Store
	weights  *HealthWeightsService
	snapshot healthSnapshotEnqueuer
	now      func() time.Time
	rounds   int
}

// NewHealthComputeService 构造健康计算轮。
func NewHealthComputeService(facts healthFactsSource, window *metricwindow.Store, views *healthview.Store,
	weights *HealthWeightsService, snapshot healthSnapshotEnqueuer) *HealthComputeService {
	return &HealthComputeService{
		facts: facts, window: window, views: views, weights: weights, snapshot: snapshot,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// Run 启动计算循环，直到 ctx 取消（随关停信号优雅退出）。
func (s *HealthComputeService) Run(ctx context.Context) {
	slog.Info("健康计算轮已启动", "周期", healthComputeInterval.String(), "快照轮距", healthSnapshotEveryRounds)
	ticker := time.NewTicker(healthComputeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("健康计算轮已停止")
			return
		case <-ticker.C:
			s.runRound()
		}
	}
}

// runRound 执行一轮：读事实（锁外 DB IO）→ 逐实例算视图 → 整批替换 → 按轮距产快照。
// DB 读失败跳过本轮（保留上一轮视图，fail-static 精神），不让瞬时故障清空健康视图。
func (s *HealthComputeService) runRound() {
	facts, err := s.facts.ListAll()
	if err != nil {
		slog.Warn("健康计算轮读取 DB 事实失败，跳过本轮", "错误", err)
		return
	}
	nowMs := s.now().UnixMilli()
	weights := s.weights.Current()
	prev := s.prevViewIndex()
	views := make([]healthview.View, 0, len(facts))
	for i := range facts {
		fact := facts[i]
		samples := s.window.List(fact.NamespaceID, fact.ServerID)
		views = append(views, computeInstanceView(fact, samples, prev[viewKey{fact.NamespaceID, fact.ServerID}], nowMs, weights))
	}
	s.views.ReplaceAll(views)

	s.rounds++
	if s.rounds%healthSnapshotEveryRounds != 0 {
		return
	}
	rows := snapshotRowsOf(views, nowMs)
	if len(rows) > 0 && !s.snapshot.Enqueue(rows) {
		// 队列满丢弃本轮快照（内存视图仍最新）；WARN 让丢失可见（错误不静默，ADR-0057）。
		slog.Warn("健康快照写入队列已满，丢弃本轮快照", "行数", len(rows))
	}
}

// viewKey 按 (namespace, server) 定位上一轮视图。
type viewKey struct {
	namespaceID uint
	serverID    string
}

// prevViewIndex 把上一轮视图建索引（沿用判定用）。
func (s *HealthComputeService) prevViewIndex() map[viewKey]*healthview.View {
	list := s.views.List()
	byKey := make(map[viewKey]*healthview.View, len(list))
	for i := range list {
		byKey[viewKey{list[i].NamespaceID, list[i].ServerID}] = &list[i]
	}
	return byKey
}

// computeInstanceView 计算单实例本轮健康视图（纯函数，无副作用）：
//   - 最近 30s 内有成功批（活性正常）→ 用窗口内最近 60s 接收的批聚合因子输入，正常计算；
//   - 最近一批距今 >30s → 判 lost（§4.2 活性判定权威）：score=0、level=unhealthy、
//     reasons 含 lost、不输出因子；
//   - 窗口完全无数据（如控制面重启后内存窗口清空）→ 上一轮视图距今 ≤30s 且非 lost 则沿用
//     其分数 / 因子（仅按当前事实重判 schedulable），超 30s 同判 lost。
func computeInstanceView(fact repository.HealthFact, samples []metricwindow.Sample,
	prev *healthview.View, nowMs int64, weights HealthWeightsSnapshot) healthview.View {
	lastSeen := lastReceivedMs(samples)
	if lastSeen > 0 && nowMs-lastSeen <= healthLostAfterMs {
		return freshView(fact, recentSamples(samples, nowMs), nowMs, weights)
	}
	if lastSeen == 0 && prev != nil && nowMs-prev.ComputedAtMs <= healthLostAfterMs &&
		!containsReason(prev.Reasons, healthview.ReasonLost) {
		return carriedView(fact, prev)
	}
	return lostView(fact, nowMs, weights.Rev)
}

// lastReceivedMs 返回窗口中最近一批的接收时刻（无数据为 0），作活性判定输入。
func lastReceivedMs(samples []metricwindow.Sample) int64 {
	var lastSeen int64
	for i := range samples {
		if samples[i].ReceivedAtMs > lastSeen {
			lastSeen = samples[i].ReceivedAtMs
		}
	}
	return lastSeen
}

// recentSamples 取窗口中最近 60s 接收的批（§4.4 因子输入 = 最近 60s 窗口；
// 调用前提为活性正常，故结果必非空——最近一批即在 30s 内）。
func recentSamples(samples []metricwindow.Sample, nowMs int64) []metricwindow.Sample {
	out := make([]metricwindow.Sample, 0, len(samples))
	for i := range samples {
		if nowMs-samples[i].ReceivedAtMs <= healthWindowMs {
			out = append(out, samples[i])
		}
	}
	return out
}

// freshView 用窗口聚合输入完成正常计算路径。
func freshView(fact repository.HealthFact, fresh []metricwindow.Sample, nowMs int64, weights HealthWeightsSnapshot) healthview.View {
	in, online, maxOnline := aggregateFactorInputs(fact.Kind, fresh)
	factors := ComputeHealthFactors(in, weights.Config)
	score := HealthScore(factors)
	level := HealthLevelOf(score, weights.Config.Levels)
	reasons := SchedulableReasons(scheduleFactsOf(fact, false, level))
	return healthview.View{
		NamespaceID: fact.NamespaceID, Namespace: fact.Namespace,
		ServerID: fact.ServerID, Kind: fact.Kind, ZoneName: fact.ZoneName,
		Score: score, Level: level,
		Schedulable: len(reasons) == 0, Reasons: reasons, Factors: factors,
		WeightsRev: weights.Rev, OnlineCount: online, MaxOnline: maxOnline,
		ComputedAtMs: nowMs,
	}
}

// carriedView 沿用上一轮计算结果（分数 / 等级 / 因子 / 权重版本 / 计算时刻均保留——
// ComputedAtMs 不刷新，保证断供 30s 后自然滑入 lost），仅按当前 DB 事实重判 schedulable。
func carriedView(fact repository.HealthFact, prev *healthview.View) healthview.View {
	reasons := SchedulableReasons(scheduleFactsOf(fact, false, prev.Level))
	return healthview.View{
		NamespaceID: fact.NamespaceID, Namespace: fact.Namespace,
		ServerID: fact.ServerID, Kind: fact.Kind, ZoneName: fact.ZoneName,
		Score: prev.Score, Level: prev.Level,
		Schedulable: len(reasons) == 0, Reasons: reasons, Factors: prev.Factors,
		WeightsRev: prev.WeightsRev, OnlineCount: prev.OnlineCount, MaxOnline: prev.MaxOnline,
		ComputedAtMs: prev.ComputedAtMs,
	}
}

// lostView 失联视图：不再输出分数（score=0、level=unhealthy、factors 空），reasons 含 lost（§4.4/§4.5）。
func lostView(fact repository.HealthFact, nowMs int64, weightsRev int) healthview.View {
	reasons := SchedulableReasons(scheduleFactsOf(fact, true, healthview.LevelUnhealthy))
	return healthview.View{
		NamespaceID: fact.NamespaceID, Namespace: fact.Namespace,
		ServerID: fact.ServerID, Kind: fact.Kind, ZoneName: fact.ZoneName,
		Score: 0, Level: healthview.LevelUnhealthy,
		Schedulable: false, Reasons: reasons, Factors: []healthview.Factor{},
		WeightsRev: weightsRev, ComputedAtMs: nowMs,
	}
}

// scheduleFactsOf 组装 §4.5 判定事实（DB 事实 + 本轮活性 / 等级）。
func scheduleFactsOf(fact repository.HealthFact, lost bool, level string) HealthScheduleFacts {
	return HealthScheduleFacts{
		Kind: fact.Kind, IdentityStatus: fact.IdentityStatus,
		Unassigned: fact.Unassigned, Draining: fact.Draining,
		Lost: lost, Level: level,
	}
}

// aggregateFactorInputs 把窗口批聚合为因子输入（§4.4 口径）：cpu / tps 取批 avg 均值、
// 在线与连接取 max 保守值、latency 按 kind 取 reportRttMs / backendRttMsAvg 的有效均值。
//
// 不可用哨兵对称处理：cpuPctAvg<0 的批（JVM getProcessCpuLoad 毛刺、agent 侧全不可用才为 -1）
// 不计入 cpu 均值，窗口内全 <0 → cpu 置 -1（因子不适用并权重重归一，与 latency rtt=-1 同构）。
func aggregateFactorInputs(kind string, samples []metricwindow.Sample) (in HealthFactorInputs, online, maxOnline int) {
	var cpuSum float64
	var cpuN int
	var tpsSum float64
	var rttSum float64
	var rttN int
	var onlineMax, connMax, maxOnlineMax int
	for i := range samples {
		s := &samples[i]
		if s.CPUPctAvg >= 0 {
			cpuSum += s.CPUPctAvg
			cpuN++
		}
		tpsSum += s.TPSAvg
		if rtt, ok := sampleRtt(kind, s); ok {
			rttSum += rtt
			rttN++
		}
		if s.OnlineMax > onlineMax {
			onlineMax = s.OnlineMax
		}
		if s.ConnMax > connMax {
			connMax = s.ConnMax
		}
		if s.MaxOnline > maxOnlineMax {
			maxOnlineMax = s.MaxOnline
		}
	}
	in = HealthFactorInputs{
		Kind:   kind,
		CPUPct: -1, // 全部批次 cpu 不可用 → 因子不适用
		TPS:    tpsSum / float64(len(samples)),
		RttMs:  -1, // 无有效 rtt → 因子不适用
	}
	if cpuN > 0 {
		in.CPUPct = cpuSum / float64(cpuN)
	}
	if rttN > 0 {
		in.RttMs = rttSum / float64(rttN)
	}
	in.OnlineCount, in.MaxOnline, in.ConnCount = onlineMax, maxOnlineMax, connMax
	return in, onlineMax, maxOnlineMax
}

// sampleRtt 按角色取单批延迟原始值（§4.4：backend 取 reportRttMs、proxy 取 backendRttMsAvg）；<0 视为无效。
func sampleRtt(kind string, s *metricwindow.Sample) (float64, bool) {
	var rtt float64
	if kind == model.ServerKindProxy {
		rtt = s.BackendRttMsAvg
	} else {
		rtt = float64(s.ReportRttMs)
	}
	if rtt < 0 {
		return 0, false
	}
	return rtt, true
}

// HealthFactorView 是因子明细的对外 json 形态（快照 factors 列与管理端点共用，
// 键与 contracts HealthFactor / spec §3.2 逐字一致）。
type HealthFactorView struct {
	Factor     string  `json:"factor"`
	Raw        float64 `json:"raw"`
	Normalized float64 `json:"normalized"`
	Weight     float64 `json:"weight"`
	Applicable bool    `json:"applicable"`
}

// factorViewsOf 把内存因子转为 json 形态切片（nil 安全，恒返回非 nil）。
func factorViewsOf(factors []healthview.Factor) []HealthFactorView {
	out := make([]HealthFactorView, 0, len(factors))
	for _, f := range factors {
		out = append(out, HealthFactorView{
			Factor: f.Factor, Raw: f.Raw, Normalized: f.Normalized,
			Weight: f.Weight, Applicable: f.Applicable,
		})
	}
	return out
}

// snapshotRowsOf 把全量视图转为快照日表行（reasons / factors 序列化为 json 数组文本，ts_ms=本轮时刻）。
func snapshotRowsOf(views []healthview.View, nowMs int64) []model.HealthSnapshot {
	rows := make([]model.HealthSnapshot, 0, len(views))
	for i := range views {
		v := &views[i]
		reasons := v.Reasons
		if reasons == nil {
			reasons = []string{}
		}
		reasonsJSON, err := json.Marshal(reasons)
		if err != nil {
			continue // 纯值序列化不应失败；防御性跳过坏行
		}
		factorsJSON, err := json.Marshal(factorViewsOf(v.Factors))
		if err != nil {
			continue
		}
		rows = append(rows, model.HealthSnapshot{
			TsMs: nowMs, NamespaceID: v.NamespaceID, ServerID: v.ServerID, Kind: v.Kind,
			Score: v.Score, Level: v.Level, Schedulable: v.Schedulable,
			Reasons: string(reasonsJSON), Factors: string(factorsJSON), WeightsRev: v.WeightsRev,
		})
	}
	return rows
}

// containsReason 判断原因码是否在列表中。
func containsReason(reasons []string, reason string) bool {
	for _, r := range reasons {
		if r == reason {
			return true
		}
	}
	return false
}
