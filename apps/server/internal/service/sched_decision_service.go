package service

import (
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
)

// RouteKindSchedDecision 是调度决策行在异步日表写入通道中的路由键（FR-146，见 §4.3：
// 决策记录复用与指标相同的写入通道、不同表路由、独立攒批）。
const RouteKindSchedDecision = "sched_decision"

// schedDecisionEnqueuer 是决策服务对异步写入池的窄依赖：非阻塞投递决策行，队列满返回 false。
// 抽成接口便于单测注入替身断言入库行内容与队列满行为。
type schedDecisionEnqueuer interface {
	Enqueue(rows []model.SchedDecisionV2) bool
}

// SchedDecisionEnqueuer 把泛化异步日表写入通道绑定到 sched_decision 路由（装配用）。
type SchedDecisionEnqueuer struct {
	// Writer 泛化异步日表写入通道（须已注册 RouteKindSchedDecision 路由）。
	Writer *AsyncDailyWriter
}

// Enqueue 非阻塞投递一批决策行；队列满返回 false。
func (e SchedDecisionEnqueuer) Enqueue(rows []model.SchedDecisionV2) bool {
	return EnqueueRows(e.Writer, RouteKindSchedDecision, rows)
}

// 调度决策记录的枚举字面值：strategy / source 的落库真源在 model（sched_decision_v2.go），
// 此处按服务层口径转引导出；失败原因码为决策流程产物，定义于此（见 spec §3.4/§4.6）。
const (
	// SchedStrategyHighestScore 本版唯一调度策略：分数最高者胜（spec §8 待定 11）。
	SchedStrategyHighestScore = model.SchedStrategyHighestScore
	// SchedSourceControlPlane 控制面在线决策。
	SchedSourceControlPlane = model.SchedSourceControlPlane
	// SchedSourceLocalFallback 降级期 agent 本地决策的补报（spec §4.6 降级路径）。
	SchedSourceLocalFallback = model.SchedSourceLocalFallback
	// SchedFailNoCandidate 圈定 zone 后无任何可调度候选（成功响应携带，非 HTTP 错误）。
	SchedFailNoCandidate = "no_candidate"
	// SchedFailZoneNotFound 请求方 namespace 内无该 zone 名（HTTP 404，决策行仍落库可查）。
	SchedFailZoneNotFound = "zone_not_found"
)

// 请求字段长度上限（与 spec §3.4 列宽一致；超限 400 拒绝，防止坏行毒化异步 flush 批）。
const (
	schedZoneNameMaxLen = 64
	schedPluginMaxLen   = 64
	schedPurposeMaxLen  = 128
)

// SchedExcluded 是决策中单台被排除的明细（序列化为 excluded 列的 json 数组元素，spec §3.4）。
type SchedExcluded struct {
	ServerID string `json:"serverId"`
	Reason   string `json:"reason"`
}

// SchedDecisionOutcome 是一次调度决策的完整产出：既供 handler 组装响应（traceId / chosen /
// candidateCount / excludedCount / failReason），也是决策日表行的内存形态（spec §3.4 全字段）。
type SchedDecisionOutcome struct {
	TraceID           string
	TsMs              int64
	NamespaceID       uint
	CrossNamespace    bool
	RequesterServerID string
	Plugin            string
	Purpose           string
	ZoneName          string
	Strategy          string
	Source            string
	WeightsRev        int
	CandidateCount    int
	Excluded          []SchedExcluded
	ChosenServerID    string
	ChosenScore       int
	FailReason        string
	DurationMs        int
}

// Chosen 返回是否选出了候选（失败时 ChosenServerID 为空、ChosenScore 为 -1）。
func (o SchedDecisionOutcome) Chosen() bool { return o.ChosenServerID != "" }

// SchedulingV2Service 是第二版调度决策服务（FR-146，见 spec §4.6）：
// 在健康视图内存真源上执行纯内存决策（请求 goroutine 全程零 DB 读写，目标 <5ms）。
//
// 跨 namespace 口径（spec §2.2 最小落地）：decide 请求不带 ns 参数，候选严格圈定在请求方
// namespace 内，跨 ns 请求形态不存在——cross_namespace 错误码与排除原因预留不可达，
// 决策行 cross_namespace 恒 false；信任放行规则归 v2-namespace-isolation.md。
type SchedulingV2Service struct {
	views *healthview.Store
	// mu 保护 rng：math/rand 的 Rand 非并发安全，并发 decide 请求须串行取随机数。
	mu  sync.Mutex
	rng *rand.Rand
	// now 可注入时钟（tsMs 与耗时计算），测试注入步进时钟得到确定值。
	now func() time.Time
	// newTraceID 可注入 traceId 生成器（默认 UUID v4），测试注入固定值。
	newTraceID func() string
	// enqueue 决策行异步入库通道（可选装配；nil 时仅决策不落库，单测用）。
	enqueue schedDecisionEnqueuer
	// reportMu 保护 reportSeen（补报判重集合，按 (namespace, server) 维度懒建）。
	reportMu   sync.Mutex
	reportSeen map[reportSeenKey]*boundedTraceSet
}

// NewSchedulingV2Service 构造调度决策服务；rng 注入随机源（同分同容量随机决胜），
// 传 nil 用时钟种子默认源，测试传固定种子得到确定排序。
func NewSchedulingV2Service(views *healthview.Store, rng *rand.Rand) *SchedulingV2Service {
	if rng == nil {
		now := uint64(time.Now().UnixNano())
		rng = rand.New(rand.NewPCG(now, now>>1))
	}
	return &SchedulingV2Service{
		views:      views,
		rng:        rng,
		now:        func() time.Time { return time.Now().UTC() },
		newTraceID: newUUIDv4,
		reportSeen: make(map[reportSeenKey]*boundedTraceSet),
	}
}

// Decide 在请求方 namespace 的目标 zone 内执行一次 highest_score 调度决策（spec §4.6 正常路径）。
// ns 内无该 zone 名 → ErrSchedZoneNotFound（决策行仍产出可查）；候选全被排除 → 成功返回但
// failReason=no_candidate。产出的 outcome 同时是响应数据与决策日表行的内存形态。
func (s *SchedulingV2Service) Decide(id agentauth.Identity, zone, purpose, plugin string) (SchedDecisionOutcome, error) {
	if err := validateDecideParams(zone, purpose, plugin); err != nil {
		return SchedDecisionOutcome{}, err
	}
	started := s.now()
	outcome := SchedDecisionOutcome{
		TraceID:           s.newTraceID(),
		TsMs:              started.UnixMilli(),
		NamespaceID:       id.NamespaceID,
		RequesterServerID: id.ServerID,
		Plugin:            plugin,
		Purpose:           purpose,
		ZoneName:          zone,
		Strategy:          SchedStrategyHighestScore,
		Source:            SchedSourceControlPlane,
		Excluded:          []SchedExcluded{},
		ChosenScore:       -1,
	}
	zoneViews := s.zoneViews(id.NamespaceID, zone)
	if len(zoneViews) == 0 {
		outcome.FailReason = SchedFailZoneNotFound
		s.finish(&outcome, started)
		return outcome, apperr.ErrSchedZoneNotFound
	}
	eligible, excluded := partitionSchedulable(zoneViews)
	outcome.CandidateCount = len(zoneViews)
	outcome.Excluded = excluded
	if len(eligible) == 0 {
		outcome.FailReason = SchedFailNoCandidate
		outcome.WeightsRev = zoneViews[0].WeightsRev
		s.finish(&outcome, started)
		return outcome, nil
	}
	chosen := s.pickHighestScore(eligible)
	outcome.ChosenServerID = chosen.ServerID
	outcome.ChosenScore = chosen.Score
	outcome.WeightsRev = chosen.WeightsRev
	s.finish(&outcome, started)
	return outcome, nil
}

// SetDecisionEnqueuer 装配决策行异步入库通道（main 装配期调用；请求 goroutine 仍零 DB）。
func (s *SchedulingV2Service) SetDecisionEnqueuer(e schedDecisionEnqueuer) {
	s.enqueue = e
}

// finish 结算决策耗时（用注入时钟，测试可确定断言）并把决策行推入异步入库通道。
func (s *SchedulingV2Service) finish(o *SchedDecisionOutcome, started time.Time) {
	o.DurationMs = int(s.now().Sub(started).Milliseconds())
	s.persistOutcome(*o)
}

// persistOutcome 把决策行经异步写入通道入库（spec §4.6 步骤 3：不阻塞响应）。
// 入队失败（队列满 / 未装配）记 WARN 中文日志，不影响决策响应；DB trace_id 唯一索引兜底幂等。
func (s *SchedulingV2Service) persistOutcome(o SchedDecisionOutcome) {
	if s.enqueue == nil {
		return
	}
	if !s.enqueue.Enqueue([]model.SchedDecisionV2{toSchedDecisionRow(o)}) {
		slog.Warn("调度决策写入队列已满，本条决策记录被丢弃", "traceId", o.TraceID, "zone", o.ZoneName)
	}
}

// toSchedDecisionRow 把决策产出映射为日表行模型（excluded 序列化为 json 数组文本，spec §3.4）。
func toSchedDecisionRow(o SchedDecisionOutcome) model.SchedDecisionV2 {
	// SchedExcluded 仅含字符串字段，序列化不可失败；Excluded 恒非 nil（初始化为空切片）→ "[]"。
	excluded, _ := json.Marshal(o.Excluded)
	return model.SchedDecisionV2{
		TraceID:           o.TraceID,
		TsMs:              o.TsMs,
		NamespaceID:       o.NamespaceID,
		CrossNamespace:    o.CrossNamespace,
		RequesterServerID: o.RequesterServerID,
		Plugin:            o.Plugin,
		Purpose:           o.Purpose,
		ZoneName:          o.ZoneName,
		Strategy:          o.Strategy,
		Source:            o.Source,
		WeightsRev:        o.WeightsRev,
		CandidateCount:    o.CandidateCount,
		Excluded:          string(excluded),
		ChosenServerID:    o.ChosenServerID,
		ChosenScore:       o.ChosenScore,
		FailReason:        o.FailReason,
		DurationMs:        o.DurationMs,
	}
}

// validateDecideParams 校验决策请求字段：zone 必填且各字段不超日表列宽（防坏行毒化异步 flush 批）。
func validateDecideParams(zone, purpose, plugin string) error {
	if zone == "" || len(zone) > schedZoneNameMaxLen ||
		len(purpose) > schedPurposeMaxLen || len(plugin) > schedPluginMaxLen {
		return apperr.ErrInvalidParam
	}
	return nil
}

// zoneViews 取请求方 namespace 内目标 zone 的全部健康视图，按 serverId 排序（确定枚举序）。
func (s *SchedulingV2Service) zoneViews(namespaceID uint, zone string) []healthview.View {
	all := s.views.List()
	out := make([]healthview.View, 0, len(all))
	for _, v := range all {
		if v.NamespaceID == namespaceID && v.ZoneName == zone {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServerID < out[j].ServerID })
	return out
}

// partitionSchedulable 逐台判定：不可调度者记入 excluded（取第一条命中原因码，spec §4.6），
// 其余为进入排序的候选。degraded 仍可调度、不进排除表（spec §8 待定 10）。
func partitionSchedulable(views []healthview.View) (eligible []healthview.View, excluded []SchedExcluded) {
	eligible = make([]healthview.View, 0, len(views))
	excluded = make([]SchedExcluded, 0)
	for _, v := range views {
		if v.Schedulable {
			eligible = append(eligible, v)
			continue
		}
		reason := ""
		if len(v.Reasons) > 0 {
			reason = v.Reasons[0]
		}
		excluded = append(excluded, SchedExcluded{ServerID: v.ServerID, Reason: reason})
	}
	return eligible, excluded
}

// pickHighestScore 按 highest_score 策略选一台：分数降序，同分优先容量占用率低者，再同随机
// （先整体洗牌再稳定排序——完全平手者保留洗牌相对序，等价均匀随机决胜，可用种子复现）。
func (s *SchedulingV2Service) pickHighestScore(eligible []healthview.View) healthview.View {
	s.mu.Lock()
	s.rng.Shuffle(len(eligible), func(i, j int) { eligible[i], eligible[j] = eligible[j], eligible[i] })
	s.mu.Unlock()
	sort.SliceStable(eligible, func(i, j int) bool {
		if eligible[i].Score != eligible[j].Score {
			return eligible[i].Score > eligible[j].Score
		}
		return occupancyRate(eligible[i]) < occupancyRate(eligible[j])
	})
	return eligible[0]
}

// SchedCandidate 是候选快照中的一台可调度服务器（agent 本地缓存 / 降级快照的数据源，spec §4.6 降级路径）。
type SchedCandidate struct {
	ServerID    string
	Score       int
	Level       string
	Schedulable bool
	OnlineCount int
	MaxOnline   int
}

// SchedZoneCandidates 是一个 zone 的候选集。
type SchedZoneCandidates struct {
	Zone       string
	Candidates []SchedCandidate
}

// SchedCandidatesResult 是候选快照结果（对齐 §5.1 candidates 响应）。
type SchedCandidatesResult struct {
	GeneratedAtMs int64
	Zones         []SchedZoneCandidates
}

// Candidates 返回请求方 namespace 内全部 zone 的当前可调度候选快照（纯内存，零 DB）：
// 仅含 Schedulable==true 候选（degraded 且可调度者含入），仅列出有候选的 zone；
// zone 按名、候选按分数降序（同分按 serverId）排序，输出确定。
func (s *SchedulingV2Service) Candidates(id agentauth.Identity) SchedCandidatesResult {
	byZone := map[string][]SchedCandidate{}
	for _, v := range s.views.List() {
		if v.NamespaceID != id.NamespaceID || v.ZoneName == "" || !v.Schedulable {
			continue
		}
		byZone[v.ZoneName] = append(byZone[v.ZoneName], SchedCandidate{
			ServerID: v.ServerID, Score: v.Score, Level: v.Level, Schedulable: v.Schedulable,
			OnlineCount: v.OnlineCount, MaxOnline: v.MaxOnline,
		})
	}
	zones := make([]SchedZoneCandidates, 0, len(byZone))
	for zone, candidates := range byZone {
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].Score != candidates[j].Score {
				return candidates[i].Score > candidates[j].Score
			}
			return candidates[i].ServerID < candidates[j].ServerID
		})
		zones = append(zones, SchedZoneCandidates{Zone: zone, Candidates: candidates})
	}
	sort.Slice(zones, func(i, j int) bool { return zones[i].Zone < zones[j].Zone })
	return SchedCandidatesResult{GeneratedAtMs: s.now().UnixMilli(), Zones: zones}
}

// occupancyRate 计算容量占用率 onlineCount/maxOnline；maxOnline≤0 视为占满（1.0），排序自然靠后。
func occupancyRate(v healthview.View) float64 {
	if v.MaxOnline <= 0 {
		return 1.0
	}
	return float64(v.OnlineCount) / float64(v.MaxOnline)
}

// newUUIDv4 用 crypto/rand 生成 UUID v4 文本（36 字符），作决策 traceId（spec §3.4）。
// 不引第三方 uuid 依赖（依赖管理纪律）；读随机失败沿用零字节（概率可忽略，与 traceId 中间件同口径）。
func newUUIDv4() string {
	var b [16]byte
	_, _ = crand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
