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

// DeclarationLabelReader 是「节点自声明标签」的**只读**真源（FR-243 的写入面 = 声明端点，
// 此处只读，故本接口不含任何写入路径）。
//
// 调度决策按准入作用域收窄时（FR-244）读它：被排除的候选要的是"**这台节点自己声明了什么**"，
// 不是控制面另登记的一套标签（那会形成第二真源、且与配置分叉时不报错）。
//
// 抽成接口而不是直接用 *runtime.Registry：决策服务与注册表之间保持窄依赖，
// 单测可以注入替身而无需建整个注册表；装配期未接（nil）时带作用域的请求按"判不了"报 503
// （见 ErrSchedAdmissionUnavailable），不静默忽略作用域。
type DeclarationLabelReader interface {
	// DeclaredLabels 返回某节点当前自声明的键值标签（namespace 为环境 code）。
	// 节点不在册 / 无标签一律返回 **nil** —— "读不到声明"与"声明了空集"在准入判定上同效（都不满足非空作用域）。
	DeclaredLabels(namespace string, serverID string) map[string]string
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
	// SchedFailNoCandidateInScope 圈定 zone 后的候选**全部**因不满足本次准入作用域被排除（FR-244，成功响应携带）。
	// 与 SchedFailNoCandidate 分开只为诊断能读出来"是没候选还是都被作用域滤掉了"：作用域排除是**稳定事实**
	// （换服 / 调范围才能改口），而 no_candidate 里的健康排除是等一等会过去的**当前状态**。
	SchedFailNoCandidateInScope = "no_candidate_in_scope"
	// SchedExcludedAdmissionMismatch 单台候选因不满足准入作用域被排除的明细原因码（FR-244，落 excluded 列）。
	// 取值字符串是前后端与 agent 侧对接的事实，与常量名解耦：改名不改值。
	SchedExcludedAdmissionMismatch = "admission_scope_mismatch"
	// SchedFailZoneNotFound 请求方 namespace 内无该 zone 名（HTTP 404，决策行仍落库可查）。
	SchedFailZoneNotFound = "zone_not_found"
	// SchedScopeZone 保持既有按小区调度的缺省作用域。
	SchedScopeZone = "zone"
	// SchedScopeLobby 是 namespace 唯一大厅集群的调度作用域。
	SchedScopeLobby = "lobby"
)

// 请求字段长度上限（与 spec §3.4 列宽一致；超限 400 拒绝，防止坏行毒化异步 flush 批）。
const (
	schedZoneNameMaxLen = 64
	schedPluginMaxLen   = 64
	schedPurposeMaxLen  = 128
	// schedAdmissionMaxAlternatives 是准入作用域的备选个数上限（FR-244）：本版 8。
	// 界存在的理由是"请求体有界"，不是为了限制表达力——表达"不限制 ∨ 若干取值"两三条就够。
	schedAdmissionMaxAlternatives = 8
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
	// AdmissionExcludedCount 是本次**因准入作用域**被排除的候选台数（FR-244）。
	// 它是"决策阶段确实收窄了"的可读信号：只统计作用域那一类排除（健康原因不算），
	// 故 >0 时调用方可以确定地说"这次收窄发生了"。
	AdmissionExcludedCount int
	ChosenServerID         string
	ChosenScore            int
	FailReason             string
	DurationMs             int
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
	// labels 节点自声明标签的只读真源（FR-244；可选装配）。**只有**请求带非空准入作用域时才会被读，
	// 而那种请求在未装配时会被入口按 503 拦下——故"装配缺失"不会被读成"没有符合条件的候选"。
	labels DeclarationLabelReader
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
	return s.DecideScoped(id, SchedScopeZone, zone, purpose, plugin)
}

// DecideScoped 在 zone 或 lobby 作用域内执行一次 highest_score 调度决策。
// 空 scope 仅在 handler 层归一为 zone；服务层调用必须显式给出有效 scope。
//
// 不带准入作用域（等价于 admission 为空）——既有调用方与既有行为逐位不变。
func (s *SchedulingV2Service) DecideScoped(id agentauth.Identity, scope, zone, purpose, plugin string) (SchedDecisionOutcome, error) {
	return s.DecideScopedWithAdmission(id, scope, zone, purpose, plugin, nil)
}

// DecideScopedWithAdmission 在 zone 或 lobby 作用域内执行一次调度决策，并按**准入作用域**收窄候选
// （FR-244）：作用域是若干**备选**，每个备选是一组「候选节点必须**自己声明过**的键值标签」
// （备选之间 OR、备选之内 AND，逐项精确相等）。
//
// <h3>候选在**生成的那一刻**就被收窄</h3>
//
// 不满足作用域的节点进不了 eligible，因此**没有被选中的机会**——这与"选出来之后由调用方校验并拒掉"
// 是两件事：后者会让本次调用白跑一圈直接失败，前者根本不会选中它。
//
// <h3>三条结论分类</h3>
//
//   - 选了 → ChosenServerID 非空；
//   - 候选全被作用域滤掉 → 成功返回 + FailReason=SchedFailNoCandidateInScope（**稳定事实**：
//     重试不会改口，恢复动作是换服 / 调服务范围）；
//   - 作用域非空但读取真源未装配 → ErrSchedAdmissionUnavailable（**当前状态**、可重试）——
//     **不**退化成"忽略作用域照旧全量决策"（那是静默放宽准入），也**不**报成"没有候选"（那是把
//     "判不了"印成稳定结论）。
//
// 空 admission 时逐位等于 DecideScoped（一次都不读标签真源）。**无约束**的作用域形态与之同效：
// 备选全为空 map（`[{}]` / `[{},{}]`——任何候选都满足、一台都滤不掉）在校验通过后被归一为 nil，
// 故不读真源、不因真源未装配报 503、也不会被印成 no_candidate_in_scope。
func (s *SchedulingV2Service) DecideScopedWithAdmission(id agentauth.Identity, scope, zone, purpose, plugin string,
	admission []map[string]string) (SchedDecisionOutcome, error) {
	if err := validateScopedDecideParams(scope, zone, purpose, plugin); err != nil {
		return SchedDecisionOutcome{}, err
	}
	if err := validateAdmissionScope(admission); err != nil {
		return SchedDecisionOutcome{}, err
	}
	// 无约束作用域归一为 nil：与缺键逐位一致，把"有没有约束力"这一个判断收在入口一次做掉。
	if !admissionHasConstraints(admission) {
		admission = nil
	}
	// 非空作用域必须先有判定真源：判不了就报"当前状态"，绝不静默忽略作用域。
	if len(admission) > 0 && s.labels == nil {
		return SchedDecisionOutcome{}, apperr.ErrSchedAdmissionUnavailable
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
	views, found := s.scopedViews(id.NamespaceID, scope, zone)
	if !found {
		outcome.FailReason = SchedFailZoneNotFound
		s.finish(&outcome, started)
		return outcome, apperr.ErrSchedZoneNotFound
	}
	eligible, excluded := s.partitionEligible(id.Namespace, views, admission)
	outcome.CandidateCount = len(views)
	outcome.Excluded = excluded
	outcome.AdmissionExcludedCount = countAdmissionExcluded(excluded)
	if len(eligible) == 0 {
		// 空结果的两条诊断分开：zone 里本来就没可调度候选 / 候选都被本次作用域滤掉了。
		// 分类依据是"这条结论稳不稳定"：作用域排除是**稳定事实**（换服 / 调范围才能改口），
		// 健康排除是**当前状态**（等一等会过去）——把后者印成稳定事实正是 503 那套设计要避免的。
		// 故只有"全部候选都因作用域被排除"才报 SchedFailNoCandidateInScope；混合原因仍报
		// SchedFailNoCandidate（作用域排掉几台由 admissionExcludedCount 与 excluded 明细带出）。
		outcome.FailReason = SchedFailNoCandidate
		if len(admission) > 0 && len(views) > 0 && outcome.AdmissionExcludedCount == len(views) {
			outcome.FailReason = SchedFailNoCandidateInScope
		}
		if len(views) > 0 {
			outcome.WeightsRev = views[0].WeightsRev
		}
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

// SetDeclarationLabels 装配「节点自声明标签」的只读真源（FR-244，main 装配期调用）。
//
// 只有带**非空**准入作用域的请求才会读它；未装配时那种请求按 503 ErrSchedAdmissionUnavailable 报出
// （当前状态、可重试），**不**退化成"忽略作用域照旧全量决策"（静默放宽准入），
// 也**不**报成"没有候选"（把判不了印成稳定结论）。
func (s *SchedulingV2Service) SetDeclarationLabels(reader DeclarationLabelReader) {
	s.labels = reader
}

// countAdmissionExcluded 数出 excluded 里因准入作用域被排除的台数。
func countAdmissionExcluded(excluded []SchedExcluded) int {
	count := 0
	for _, item := range excluded {
		if item.Reason == SchedExcludedAdmissionMismatch {
			count++
		}
	}
	return count
}

// logAdmissionNarrowed 在**准入作用域确实剔除了候选**时留一条读数行（FR-244）。
//
// 只记非正常路径：没被收窄的派房一条都不打（那是正常路径，噪声会淹掉这条）。
// 它是"决策阶段收窄"在控制面侧最直接的痕迹，与决策行里的 excluded 明细互为印证。
func logAdmissionNarrowed(o SchedDecisionOutcome) {
	if o.AdmissionExcludedCount == 0 {
		return
	}
	slog.Info("调度决策按准入作用域收窄（候选在生成阶段即被排除）",
		"traceId", o.TraceID, "zone", o.ZoneName,
		"剔除", o.AdmissionExcludedCount, "候选总数", o.CandidateCount,
		"中选", o.ChosenServerID, "失败原因", o.FailReason)
}

// finish 结算决策耗时（用注入时钟，测试可确定断言）并把决策行推入异步入库通道。
func (s *SchedulingV2Service) finish(o *SchedDecisionOutcome, started time.Time) {
	o.DurationMs = int(s.now().Sub(started).Milliseconds())
	logAdmissionNarrowed(*o)
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

// validateScopedDecideParams 校验 scope 与其目标字段形状；lobby 不接受非空 zone。
func validateScopedDecideParams(scope, zone, purpose, plugin string) error {
	if len(purpose) > schedPurposeMaxLen || len(plugin) > schedPluginMaxLen {
		return apperr.ErrInvalidParam
	}
	switch scope {
	case SchedScopeZone:
		if zone == "" || len(zone) > schedZoneNameMaxLen {
			return apperr.ErrInvalidParam
		}
	case SchedScopeLobby:
		if zone != "" {
			return apperr.ErrInvalidParam
		}
	default:
		return apperr.ErrInvalidParam
	}
	return nil
}

// validateAdmissionScope 校验准入作用域的键值形状（FR-244）：逐项沿用 FR-227 与声明端点**同一套**
// 约束（key 字符集 / key ≤ 32 / value ≤ 128），单个备选的条数 ≤ 单节点标签数上限，
// 备选个数 ≤ 一个很小的界（本版 8：够表达"不限制 ∨ 若干取值"，又不让请求体无界）。
//
// 空 / nil 作用域合法（= 不按标签收窄）。**判定侧不做任何归一化**：归一化（trim key）只由写声明的
// 那一方做一次（见 normalizeDeclaredLabels），真源里存的因此都是**规整 key**——判定侧遇到带空白的
// key 永远命中不了，那就是一个坏请求，当场 400 比"校验放过、判定永不命中"更诚实。
// value 同样不归一化：逐字符精确相等是作用域的语义本身。
func validateAdmissionScope(admission []map[string]string) error {
	if len(admission) > schedAdmissionMaxAlternatives {
		return apperr.ErrInvalidParam
	}
	for _, alternative := range admission {
		if len(alternative) > model.ServerTagMaxPerServer {
			return apperr.ErrInvalidParam
		}
		for key, value := range alternative {
			normalized, err := normalizeTagEntry(key, value)
			if err != nil || normalized != key {
				return apperr.ErrInvalidParam
			}
		}
	}
	return nil
}

// scopedViews 返回目标作用域的健康视图。lobby 即使没有成员也视为有效作用域并返回空集，
// 让调用方以 no_candidate 表达业务结果；只有 zone 才使用 zone_not_found。
func (s *SchedulingV2Service) scopedViews(namespaceID uint, scope, zone string) ([]healthview.View, bool) {
	if scope == SchedScopeLobby {
		return s.lobbyViews(namespaceID), true
	}
	views := s.zoneViews(namespaceID, zone)
	return views, len(views) > 0
}

// lobbyViews 仅从本 namespace 同一大厅集群的健康视图取候选，不触达 DB。
func (s *SchedulingV2Service) lobbyViews(namespaceID uint) []healthview.View {
	all := s.views.List()
	clusterID := namespaceLobbyClusterID(all, namespaceID)
	if clusterID == 0 {
		return []healthview.View{}
	}
	out := make([]healthview.View, 0)
	for _, v := range all {
		if v.NamespaceID == namespaceID && v.LobbyClusterID == clusterID {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServerID < out[j].ServerID })
	return out
}

// namespaceLobbyClusterID 从健康快照取 namespace 权威大厅集群 ID。
func namespaceLobbyClusterID(views []healthview.View, namespaceID uint) uint {
	for _, v := range views {
		if v.NamespaceID == namespaceID && v.NamespaceLobbyClusterID != 0 {
			return v.NamespaceLobbyClusterID
		}
	}
	return 0
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

// partitionEligible 是带上准入作用域（FR-244）的逐台判定，是 partitionSchedulable 的超集：
//
//   - 准入作用域**为空**时直接交给 partitionSchedulable：一次都不读自声明标签真源，
//     判定逐位等于改动前（这是本改动向后兼容的关键一条）；
//   - 非空时先按作用域判（不满足 → excluded，原因 admission_scope_mismatch），再把通过者交给
//     partitionSchedulable 判可调度性，两份排除明细按"作用域不满足在前"拼接。
//
// 顺序刻意如此：先答"这台服该不该服务我"，再答"它此刻健不健康"。两者叠加时排除原因报的是
// **作用域不满足**——那才是调用方能动手改的那一条（调灰度 / 换服），而"不健康"是等一等会过去的当前状态。
func (s *SchedulingV2Service) partitionEligible(namespace string, views []healthview.View,
	admission []map[string]string) (eligible []healthview.View, excluded []SchedExcluded) {
	if len(admission) == 0 {
		return partitionSchedulable(views)
	}
	admitted := make([]healthview.View, 0, len(views))
	notAdmitted := make([]SchedExcluded, 0)
	for _, v := range views {
		if satisfiesAdmission(s.declaredLabels(namespace, v.ServerID), admission) {
			admitted = append(admitted, v)
			continue
		}
		notAdmitted = append(notAdmitted, SchedExcluded{ServerID: v.ServerID, Reason: SchedExcludedAdmissionMismatch})
	}
	eligible, excluded = partitionSchedulable(admitted)
	return eligible, append(notAdmitted, excluded...)
}

// admissionHasConstraints 判定准入作用域是否**确有约束力**：存在任一非空备选即为 true。
//
// 备选全为空 map（`[{}]` / `[{},{}]`）时 satisfiesAlternative 恒真、一台候选都滤不掉，
// 那种形态与缺键同效（见 DecideScopedWithAdmission 的入口归一）。
func admissionHasConstraints(admission []map[string]string) bool {
	for _, alternative := range admission {
		if len(alternative) > 0 {
			return true
		}
	}
	return false
}

// satisfiesAdmission 判定一组节点自声明标签是否满足准入作用域：**备选之间 OR、备选之内 AND**，
// 逐项精确相等，**不做**任何归一化（归一化只由写声明的那一方做一次）。空作用域恒满足（不读标签）。
//
// 为什么要 OR：调用方的真值表里「没有收窄服务范围（不限制）」与「收窄到正好包含这个取值」是**并列**的
// 两种受理形态，用单一 AND 表达不了；而让本仓去解释某个键的**值结构**（如逗号分列的清单）
// 是另一条路，本仓明确不走（FR-244 的边界：不解释 key，也不解释值结构）。
func satisfiesAdmission(declared map[string]string, admission []map[string]string) bool {
	if len(admission) == 0 {
		return true
	}
	for _, alternative := range admission {
		if satisfiesAlternative(declared, alternative) {
			return true
		}
	}
	return false
}

// satisfiesAlternative 判定单个备选是否成立：备选内每一对都要在声明里精确命中。
// 空备选恒成立（调用方若写出这种形态，是它的条件本身没有约束力）。
func satisfiesAlternative(declared map[string]string, alternative map[string]string) bool {
	if len(alternative) == 0 {
		return true
	}
	for key, want := range alternative {
		got, ok := declared[key]
		if !ok || got != want {
			return false
		}
	}
	return true
}

// declaredLabels 读某节点当前自声明的标签；读取真源**未装配时返回 nil**
// （调用方只在**非空**作用域下才会走到这里，而未装配的情形已在入口按 503 拦下）。
func (s *SchedulingV2Service) declaredLabels(namespace string, serverID string) map[string]string {
	if s.labels == nil {
		return nil
	}
	return s.labels.DeclaredLabels(namespace, serverID)
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
	// Labels 是该节点**自己声明**的键值标签（FR-243 的写入面，FR-244 起随候选一起下发），
	// 供调用方在"选之前"按自己的命名空间收窄候选。三种形态必须分开读（见 schedCandidateOf）：
	// 非 nil 空 map = 这台节点没声明过标签；非 nil 非空 = 声明了这些；**nil** = 本进程未装配标签真源，
	// 即"看不到声明"——调用方带非空作用域时须归入不可用，不得读成"没有声明"。
	Labels map[string]string
}

// SchedZoneCandidates 是一个 zone 的候选集。
type SchedZoneCandidates struct {
	Zone       string
	Candidates []SchedCandidate
}

// SchedLobbyCandidates 是 namespace 大厅集群的候选快照。
type SchedLobbyCandidates struct {
	ClusterID  uint
	Ready      bool
	Candidates []SchedCandidate
}

// SchedCandidatesResult 是候选快照结果（对齐 §5.1 candidates 响应）。
type SchedCandidatesResult struct {
	GeneratedAtMs int64
	Lobby         SchedLobbyCandidates
	Zones         []SchedZoneCandidates
}

// Candidates 返回请求方 namespace 内全部 zone 与大厅的当前可调度候选快照（纯内存，零 DB）。
// lobby 即使无候选也返回 ready=false 的空段，避免 Agent 把模型缺失和暂无候选混淆。
//
// 每台候选随带它**自己声明**的键值标签（FR-244）：调用方据此在"选之前"收窄候选，
// 或直接读出来展示。标签读取真源**未装配时返回 nil**（adapter 层据此不下发该键），
// 与"装配了但该节点没声明"（非 nil 空 map，会下发 `{}`）是两件事。
func (s *SchedulingV2Service) Candidates(id agentauth.Identity) SchedCandidatesResult {
	all := s.views.List()
	byZone := map[string][]SchedCandidate{}
	for _, v := range all {
		if v.NamespaceID != id.NamespaceID || v.ZoneName == "" || !v.Schedulable {
			continue
		}
		byZone[v.ZoneName] = append(byZone[v.ZoneName], s.schedCandidateOf(id.Namespace, v))
	}
	zones := make([]SchedZoneCandidates, 0, len(byZone))
	for zone, candidates := range byZone {
		sortSchedCandidates(candidates)
		zones = append(zones, SchedZoneCandidates{Zone: zone, Candidates: candidates})
	}
	sort.Slice(zones, func(i, j int) bool { return zones[i].Zone < zones[j].Zone })

	clusterID := namespaceLobbyClusterID(all, id.NamespaceID)
	lobbyCandidates := make([]SchedCandidate, 0)
	if clusterID != 0 {
		for _, v := range all {
			if v.NamespaceID == id.NamespaceID && v.LobbyClusterID == clusterID && v.Schedulable {
				lobbyCandidates = append(lobbyCandidates, s.schedCandidateOf(id.Namespace, v))
			}
		}
	}
	sortSchedCandidates(lobbyCandidates)
	return SchedCandidatesResult{
		GeneratedAtMs: s.now().UnixMilli(), Zones: zones,
		Lobby: SchedLobbyCandidates{ClusterID: clusterID, Ready: len(lobbyCandidates) > 0, Candidates: lobbyCandidates},
	}
}

// schedCandidateOf 把一台健康视图翻成候选条目；标签取自自声明真源。
//
// 标签字段分两种形态，**不要**把它们读成同一件事：
//   - 非 nil（含空 map）：本进程**装配了**标签真源——空 map 是"这台节点当前没声明过标签"（稳定事实），
//     非空是它声明的那些键值；
//   - **nil**：本进程没有装配标签真源（装配缺失）——调用方据此判"看不到声明"，
//     带非空准入作用域时应归入**不可用**（可重试），不得读成"没有声明"（稳定事实）。
//
// 判定依据是"真源装配没装配"（控制面能力），不是"这台节点有没有标签"：后者是稳定事实，
// 拿它当"看不到"会让一个真没声明标签的节点把整批决策拖成不可用。
func (s *SchedulingV2Service) schedCandidateOf(namespace string, v healthview.View) SchedCandidate {
	if s.labels == nil {
		return SchedCandidate{ServerID: v.ServerID, Score: v.Score, Level: v.Level, Schedulable: v.Schedulable,
			OnlineCount: v.OnlineCount, MaxOnline: v.MaxOnline, Labels: nil}
	}
	declared := s.declaredLabels(namespace, v.ServerID)
	// 拷贝一份，并保证非 nil：三态里"装配了但该节点没声明"必须是**非 nil** 空 map（nil 表示"看不到声明"）。
	// 拷贝的理由是与任意 DeclarationLabelReader 实现隔离——接口没有约定它返回的 map 不可变，
	// 候选快照不应与真源共享可变引用（注册表那一路虽已自行深拷贝，这里不作依赖）。
	copied := make(map[string]string, len(declared))
	for k, val := range declared {
		copied[k] = val
	}
	return SchedCandidate{ServerID: v.ServerID, Score: v.Score, Level: v.Level, Schedulable: v.Schedulable,
		OnlineCount: v.OnlineCount, MaxOnline: v.MaxOnline, Labels: copied}
}

func sortSchedCandidates(candidates []SchedCandidate) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].ServerID < candidates[j].ServerID
	})
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
