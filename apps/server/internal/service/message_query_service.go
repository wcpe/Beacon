package service

import (
	"sort"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// 消息异常链路聚合维度（spec §5.2 groupBy）。edge=边级异常链路（/topology 数据源，MessageEdgeStat 契约）；
// type=按消息类型计数（devmock 内联形态）。bucket 无独立冻结契约、无前端消费方，按 devmock 回退等价 edge。
const (
	msgStatsGroupEdge = "edge"
	msgStatsGroupType = "type"
	// unresolvedEdgeTarget 是未解析目标（resolved_server_id 空）在边聚合中的占位（对齐 devmock '(未解析)'）。
	unresolvedEdgeTarget = "(未解析)"
	// maxTopFailReasons 边聚合的 top 失败原因保留数（对齐 devmock slice(0,3)）。
	maxTopFailReasons = 3
	// maxEdgeSamples 边聚合的失败样本 messageId 保留数（对齐 devmock < 5）。
	maxEdgeSamples = 5
)

// MessageQueryService 是跨服消息的管理面查询服务（FR-149/150，见 spec §5.2）：
// messageId/correlationId 直查 / 条件游标分页 / 详情（hops + 关联摘要）/ 异常链路聚合。
// 只读、查询侧不隐式建日表；列表与详情永不含 payload（payload 仅经受控查看端点返回）。
type MessageQueryService struct {
	repo *repository.MessageRepository
}

// NewMessageQueryService 构造查询服务。
func NewMessageQueryService(repo *repository.MessageRepository) *MessageQueryService {
	return &MessageQueryService{repo: repo}
}

// ListMessagesParams 是消息列表查询入参（messageId/correlationId 直查免防护；否则走 §4.3 条件查询防护）。
type ListMessagesParams struct {
	MessageID      string
	CorrelationID  string
	ServerID       string
	PlayerUUID     string
	Status         string
	MsgType        string
	CrossNamespace *bool
	NamespaceID    uint
	FromMs         int64
	ToMs           int64
	Cursor         int
	Limit          int
}

// MsgPage 是消息元数据游标分页结果（NextCursor 空串表示无下一页）。
type MsgPage struct {
	Items      []model.MsgTrace
	NextCursor string
}

// List 查询消息元数据：messageId / correlationId 精确直查（免防护），否则校验查询防护后跨日并表游标分页（created_at 降序）。
func (s *MessageQueryService) List(p ListMessagesParams) (MsgPage, error) {
	if p.MessageID != "" {
		row, err := s.repo.FindByMessageID(p.MessageID)
		if err != nil {
			return MsgPage{}, err
		}
		items := make([]model.MsgTrace, 0, 1)
		if row != nil {
			items = append(items, *row)
		}
		return MsgPage{Items: items, NextCursor: ""}, nil
	}
	if p.CorrelationID != "" {
		rows, err := s.repo.FindByCorrelationID(p.CorrelationID)
		if err != nil {
			return MsgPage{}, err
		}
		return MsgPage{Items: rows, NextCursor: ""}, nil
	}
	if err := validateRangeFilter(p.ServerID != "" || p.PlayerUUID != "", p.FromMs, p.ToMs); err != nil {
		return MsgPage{}, err
	}
	limit := clampLimit(p.Limit)
	offset := clampOffset(p.Cursor)
	rows, hasMore, err := s.repo.QueryMessages(repository.MessageQuery{
		ServerID: p.ServerID, PlayerUUID: p.PlayerUUID, Status: p.Status, MsgType: p.MsgType,
		CrossNamespace: p.CrossNamespace, NamespaceID: p.NamespaceID,
		FromMs: p.FromMs, ToMs: p.ToMs, Offset: offset, Limit: limit,
	})
	if err != nil {
		return MsgPage{}, err
	}
	return MsgPage{Items: rows, NextCursor: nextCursorOf(offset, limit, hasMore)}, nil
}

// MessageDetailResult 是消息详情（元数据 + 关联消息，hops 解析在 handler 层）。
type MessageDetailResult struct {
	Trace      model.MsgTrace
	Correlated *model.MsgTrace // RPC 往返对手，无则 nil
}

// Detail 按 messageId 查单条消息详情（元数据 + 关联摘要）；未命中 404 message_not_found。
func (s *MessageQueryService) Detail(messageID string) (MessageDetailResult, error) {
	if messageID == "" || len(messageID) > 36 {
		return MessageDetailResult{}, apperr.ErrInvalidParam
	}
	trace, err := s.repo.FindByMessageID(messageID)
	if err != nil {
		return MessageDetailResult{}, err
	}
	if trace == nil {
		return MessageDetailResult{}, apperr.ErrMessageNotFound
	}
	corr, err := s.repo.FindCorrelated(trace.MessageID, trace.CorrelationID)
	if err != nil {
		return MessageDetailResult{}, err
	}
	return MessageDetailResult{Trace: *trace, Correlated: corr}, nil
}

// MessageStatsParams 是异常链路聚合入参（groupBy 走预定义维度，from/to 为已定窗口）。
type MessageStatsParams struct {
	GroupBy string
	FromMs  int64
	ToMs    int64
}

// MsgFailReasonCount 是边聚合中一条失败原因计数。
type MsgFailReasonCount struct {
	Reason string
	Count  int
}

// MsgEdgeStat 是一条 source→resolved 边的异常链路聚合（对齐 contracts MessageEdgeStat）。
type MsgEdgeStat struct {
	SourceServerID   string
	ResolvedServerID string
	Total            int
	Failed           int
	Expired          int
	FailRatePercent  float64
	P95DurationMs    int64
	TopFailReasons   []MsgFailReasonCount
	SampleMessageIDs []string
}

// MsgTypeStat 是按消息类型的计数（devmock 内联形态 {msgType,total,failed}）。
type MsgTypeStat struct {
	MsgType string
	Total   int
	Failed  int
}

// MessageStatsResult 是异常链路聚合结果（据 GroupBy 取 Edges 或 Types 其一）。
type MessageStatsResult struct {
	GroupBy string
	Edges   []MsgEdgeStat
	Types   []MsgTypeStat
}

// Stats 聚合窗口内消息的异常链路：groupBy=type 按类型计数，其余（edge/默认/bucket）按 source→resolved 边聚合。
// bucket 维度无独立冻结契约与前端消费方，按 devmock 回退等价 edge（不静默造未锚定形态）。
func (s *MessageQueryService) Stats(p MessageStatsParams) (MessageStatsResult, error) {
	rows, err := s.repo.ScanMessageStats(p.FromMs, p.ToMs)
	if err != nil {
		return MessageStatsResult{}, err
	}
	if p.GroupBy == msgStatsGroupType {
		return MessageStatsResult{GroupBy: msgStatsGroupType, Types: aggregateByType(rows)}, nil
	}
	return MessageStatsResult{GroupBy: msgStatsGroupEdge, Edges: aggregateByEdge(rows)}, nil
}

// edgeAccum 是边聚合的可变累加器（含 durations 供 p95 计算，输出时丢弃）。
type edgeAccum struct {
	stat      MsgEdgeStat
	durations []int64
	reasons   map[string]int
}

// aggregateByEdge 按 source→resolved 边聚合失败计数 / 失败率 / p95 耗时 / top 原因 / 失败样本（口径对齐 devmock）。
func aggregateByEdge(rows []repository.MsgStatRow) []MsgEdgeStat {
	edges := make(map[string]*edgeAccum)
	for i := range rows {
		row := rows[i]
		target := row.ResolvedServerID
		if target == "" {
			target = unresolvedEdgeTarget
		}
		key := row.SourceServerID + "\x00" + target
		acc := edges[key]
		if acc == nil {
			acc = &edgeAccum{
				stat:    MsgEdgeStat{SourceServerID: row.SourceServerID, ResolvedServerID: target},
				reasons: make(map[string]int),
			}
			edges[key] = acc
		}
		acc.stat.Total++
		switch row.Status {
		case model.MsgStatusFailed:
			acc.stat.Failed++
		case model.MsgStatusExpired:
			acc.stat.Expired++
		}
		if row.DurationMs != nil {
			acc.durations = append(acc.durations, *row.DurationMs)
		}
		if (row.Status == model.MsgStatusFailed || row.Status == model.MsgStatusExpired) &&
			len(acc.stat.SampleMessageIDs) < maxEdgeSamples {
			acc.stat.SampleMessageIDs = append(acc.stat.SampleMessageIDs, row.MessageID)
		}
		if row.FailReason != "" {
			acc.reasons[row.FailReason]++
		}
	}
	return finalizeEdges(edges)
}

// finalizeEdges 结算各边的失败率 / p95 / top 原因，规整为确定顺序（失败率降序、源升序、目标升序）的切片。
func finalizeEdges(edges map[string]*edgeAccum) []MsgEdgeStat {
	out := make([]MsgEdgeStat, 0, len(edges))
	for _, acc := range edges {
		stat := acc.stat
		if stat.Total > 0 {
			stat.FailRatePercent = roundPercent1(stat.Failed+stat.Expired, stat.Total)
		}
		stat.P95DurationMs = p95Of(acc.durations)
		stat.TopFailReasons = topReasons(acc.reasons)
		if stat.SampleMessageIDs == nil {
			stat.SampleMessageIDs = []string{}
		}
		out = append(out, stat)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FailRatePercent != out[j].FailRatePercent {
			return out[i].FailRatePercent > out[j].FailRatePercent
		}
		if out[i].SourceServerID != out[j].SourceServerID {
			return out[i].SourceServerID < out[j].SourceServerID
		}
		return out[i].ResolvedServerID < out[j].ResolvedServerID
	})
	return out
}

// aggregateByType 按 msg_type 聚合总数与失败数（failed/expired 计失败），按 total 降序、同数按类型升序。
func aggregateByType(rows []repository.MsgStatRow) []MsgTypeStat {
	byType := make(map[string]*MsgTypeStat)
	for i := range rows {
		row := rows[i]
		stat := byType[row.MsgType]
		if stat == nil {
			stat = &MsgTypeStat{MsgType: row.MsgType}
			byType[row.MsgType] = stat
		}
		stat.Total++
		if row.Status == model.MsgStatusFailed || row.Status == model.MsgStatusExpired {
			stat.Failed++
		}
	}
	out := make([]MsgTypeStat, 0, len(byType))
	for _, stat := range byType {
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].MsgType < out[j].MsgType
	})
	return out
}

// topReasons 把失败原因计数整理为 top 列表（按计数降序、同数按原因升序，取前 maxTopFailReasons 条）。
func topReasons(counts map[string]int) []MsgFailReasonCount {
	out := make([]MsgFailReasonCount, 0, len(counts))
	for reason, count := range counts {
		out = append(out, MsgFailReasonCount{Reason: reason, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Reason < out[j].Reason
	})
	if len(out) > maxTopFailReasons {
		out = out[:maxTopFailReasons]
	}
	return out
}

// p95Of 计算耗时切片的 p95（升序后取 min(len-1, floor(len*0.95)) 位，口径对齐 devmock）；空切片为 0。
func p95Of(durations []int64) int64 {
	if len(durations) == 0 {
		return 0
	}
	sorted := make([]int64, len(durations))
	copy(sorted, durations)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(float64(len(sorted)) * 0.95)
	if idx > len(sorted)-1 {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// roundPercent1 计算 part/total 的百分比并保留 1 位小数（对齐 devmock round(x*1000)/10）。
func roundPercent1(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(int(float64(part)/float64(total)*1000+0.5)) / 10
}
