package service

import (
	"encoding/json"
	"log/slog"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

const (
	// schedReportLocalMaxBatch 单批补报决策数上限（spec §5.1：≤100 条/批，超限 400）。
	schedReportLocalMaxBatch = 100
	// schedReportDedupCapacity 每 (namespace, server) 的补报判重集合容量（FIFO 淘汰最旧；
	// 与 agent 侧补报队列同量级，spec §8 待定 13 工程默认值）。DB trace_id 唯一索引兜底幂等。
	schedReportDedupCapacity = 512
)

// LocalDecisionReport 是 agent 降级期一条本地决策的补报（spec §4.6 降级路径 / §5.1 report-local 行）。
type LocalDecisionReport struct {
	LocalTraceID   string
	TsMs           int64
	Zone           string
	Plugin         string
	Purpose        string
	CandidateCount int
	Excluded       []SchedExcluded
	ChosenServerID string
	FailReason     string
}

// ReportLocalResult 是一次补报的处理结果（对齐 §5.1 的 202 响应 accepted / deduplicated）。
type ReportLocalResult struct {
	Accepted     int // 本次新接收并入队的决策数
	Deduplicated int // 被判重（内存判重集合命中 / 批内重复）的决策数
}

// ReportLocal 接收降级期本地决策批量补报：校验 → 内存有界集合按 localTraceId 判重 →
// 映射为 source=local_fallback 决策行经异步通道入库（请求 goroutine 零 DB，best-effort，
// spec §8 待定 7）。入队成功后才登记判重集合，DB trace_id 唯一索引兜底幂等。
func (s *SchedulingV2Service) ReportLocal(id agentauth.Identity, decisions []LocalDecisionReport) (ReportLocalResult, error) {
	if len(decisions) > schedReportLocalMaxBatch {
		return ReportLocalResult{}, apperr.ErrInvalidParam
	}
	for _, d := range decisions {
		if err := validateLocalDecision(d); err != nil {
			return ReportLocalResult{}, err
		}
	}
	if len(decisions) == 0 {
		return ReportLocalResult{}, nil
	}
	fresh, deduplicated := s.filterReportedTraces(id, decisions)
	if len(fresh) == 0 {
		return ReportLocalResult{Deduplicated: deduplicated}, nil
	}
	rows := make([]model.SchedDecisionV2, 0, len(fresh))
	for _, d := range fresh {
		rows = append(rows, toLocalFallbackRow(id, d))
	}
	if s.enqueue == nil || !s.enqueue.Enqueue(rows) {
		// best-effort：队列满（或未装配）本批不入库、不登记判重，仅 WARN 让丢弃可见（ADR-0057 精神）。
		slog.Warn("降级补报写入队列已满，本批补报未入库",
			"namespace", id.Namespace, "serverId", id.ServerID, "条数", len(fresh))
		return ReportLocalResult{Deduplicated: deduplicated}, nil
	}
	s.markReportedTraces(id, fresh)
	return ReportLocalResult{Accepted: len(fresh), Deduplicated: deduplicated}, nil
}

// filterReportedTraces 按 (namespace, server) 判重集合过滤已补报过的决策（含批内重复），锁内纯内存。
func (s *SchedulingV2Service) filterReportedTraces(id agentauth.Identity, decisions []LocalDecisionReport) (fresh []LocalDecisionReport, deduplicated int) {
	s.reportMu.Lock()
	defer s.reportMu.Unlock()
	set := s.reportSeenSet(id)
	inBatch := make(map[string]struct{}, len(decisions))
	fresh = make([]LocalDecisionReport, 0, len(decisions))
	for _, d := range decisions {
		if _, dup := inBatch[d.LocalTraceID]; dup || set.Has(d.LocalTraceID) {
			deduplicated++
			continue
		}
		inBatch[d.LocalTraceID] = struct{}{}
		fresh = append(fresh, d)
	}
	return fresh, deduplicated
}

// markReportedTraces 入队成功后登记判重集合（先入队后登记：队列满时不误标已补报）。
func (s *SchedulingV2Service) markReportedTraces(id agentauth.Identity, fresh []LocalDecisionReport) {
	s.reportMu.Lock()
	defer s.reportMu.Unlock()
	set := s.reportSeenSet(id)
	for _, d := range fresh {
		set.Add(d.LocalTraceID)
	}
}

// reportSeenSet 取（或懒建）某 (namespace, server) 的判重集合；调用方须持 reportMu。
func (s *SchedulingV2Service) reportSeenSet(id agentauth.Identity) *boundedTraceSet {
	key := reportSeenKey{namespaceID: id.NamespaceID, serverID: id.ServerID}
	set, ok := s.reportSeen[key]
	if !ok {
		set = newBoundedTraceSet(schedReportDedupCapacity)
		s.reportSeen[key] = set
	}
	return set
}

// validateLocalDecision 校验单条补报字段：localTraceId 必填且各字段不超日表列宽（防坏行毒化 flush 批）。
func validateLocalDecision(d LocalDecisionReport) error {
	if d.LocalTraceID == "" || len(d.LocalTraceID) > 36 ||
		d.Zone == "" || len(d.Zone) > schedZoneNameMaxLen ||
		len(d.Plugin) > schedPluginMaxLen || len(d.Purpose) > schedPurposeMaxLen ||
		len(d.ChosenServerID) > 64 || len(d.FailReason) > 255 ||
		d.TsMs <= 0 || d.CandidateCount < 0 {
		return apperr.ErrInvalidParam
	}
	return nil
}

// toLocalFallbackRow 把一条补报映射为 source=local_fallback 决策行（trace_id=localTraceId，
// 归属取权威身份；本地决策无权重版本 / 无耗时 / 无选中分数，按缺省值 0 / -1 落库，spec §3.4）。
func toLocalFallbackRow(id agentauth.Identity, d LocalDecisionReport) model.SchedDecisionV2 {
	excluded := d.Excluded
	if excluded == nil {
		excluded = []SchedExcluded{}
	}
	// SchedExcluded 仅含字符串字段，序列化不可失败。
	raw, _ := json.Marshal(excluded)
	return model.SchedDecisionV2{
		TraceID:           d.LocalTraceID,
		TsMs:              d.TsMs,
		NamespaceID:       id.NamespaceID,
		RequesterServerID: id.ServerID,
		Plugin:            d.Plugin,
		Purpose:           d.Purpose,
		ZoneName:          d.Zone,
		Strategy:          SchedStrategyHighestScore,
		Source:            SchedSourceLocalFallback,
		CandidateCount:    d.CandidateCount,
		Excluded:          string(raw),
		ChosenServerID:    d.ChosenServerID,
		ChosenScore:       -1,
		FailReason:        d.FailReason,
	}
}

// reportSeenKey 是补报判重集合的定位键（serverId 仅 namespace 内唯一，须带 ns 区分）。
type reportSeenKey struct {
	namespaceID uint
	serverID    string
}

// boundedTraceSet 是容量有界的 trace 判重集合：满员按 FIFO 淘汰最旧（环形槽位 + map 存在性）。
// 非并发安全，由持有方（SchedulingV2Service.reportMu）串行访问。
type boundedTraceSet struct {
	capacity int
	seen     map[string]struct{}
	ring     []string
	next     int
	wrapped  bool
}

// newBoundedTraceSet 构造给定容量的判重集合。
func newBoundedTraceSet(capacity int) *boundedTraceSet {
	return &boundedTraceSet{
		capacity: capacity,
		seen:     make(map[string]struct{}, capacity),
		ring:     make([]string, capacity),
	}
}

// Has 判断 trace 是否已登记。
func (b *boundedTraceSet) Has(trace string) bool {
	_, ok := b.seen[trace]
	return ok
}

// Add 登记 trace；容量满先淘汰最旧一条（FIFO）。已存在时为幂等空操作。
func (b *boundedTraceSet) Add(trace string) {
	if b.Has(trace) {
		return
	}
	if b.wrapped {
		delete(b.seen, b.ring[b.next])
	}
	b.ring[b.next] = trace
	b.seen[trace] = struct{}{}
	b.next++
	if b.next == b.capacity {
		b.next = 0
		b.wrapped = true
	}
}
