package service

import (
	"math"
	"sort"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// schedDecisionRetentionDays 是决策日表保留窗（天，spec §3.7 默认 60）：
// 列表时间范围上限与详情逆序查表深度均以此为界；清理执行归 P6 归档域。
const schedDecisionRetentionDays = 60

// schedSummaryDefaultWindow 是概览端点缺省时间窗（spec §5.2 示例 window=1h）。
const schedSummaryDefaultWindow = "1h"

// SchedDecisionQueryService 是调度决策记录的管理面查询服务（FR-146，见 spec §5.2）：
// 跨日并表列表 / 单条详情 / 概览聚合。只读、不参与决策；查询侧不隐式建日表。
type SchedDecisionQueryService struct {
	repo *repository.SchedDecisionV2Repository
	// now 可注入时钟（summary 时间窗锚点），测试注入固定值。
	now func() time.Time
}

// NewSchedDecisionQueryService 构造查询服务。
func NewSchedDecisionQueryService(repo *repository.SchedDecisionV2Repository) *SchedDecisionQueryService {
	return &SchedDecisionQueryService{repo: repo, now: func() time.Time { return time.Now().UTC() }}
}

// ListSchedDecisionsParams 是列表查询入参（from/to 必填，范围 ≤ 保留窗 60 天）。
type ListSchedDecisionsParams struct {
	NamespaceID uint
	Zone        string
	ServerID    string
	Result      string // "" / success / failed
	FromMs      int64
	ToMs        int64
	Page        int
	PageSize    int
	// IncludeArchived 为 true 时跨热 / 冷并表查询（FR-152）；ColdCursor 为冷查询 keyset 令牌。
	IncludeArchived bool
	ColdCursor      string
}

// List 分页查询决策记录（ts_ms 降序）；参数非法一律 400（from/to 缺失 / 倒置 / 超保留窗 / result 非法）。
func (s *SchedDecisionQueryService) List(p ListSchedDecisionsParams) ([]model.SchedDecisionV2, int64, error) {
	if err := validateListParams(p); err != nil {
		return nil, 0, err
	}
	return s.repo.QueryRange(repository.SchedDecisionQuery{
		NamespaceID: p.NamespaceID,
		Zone:        p.Zone,
		ServerID:    p.ServerID,
		Result:      p.Result,
		FromMs:      p.FromMs,
		ToMs:        p.ToMs,
		Offset:      pageOffset(p.Page, p.PageSize),
		Limit:       pageSize(p.PageSize),
	})
}

// ColdPage 是决策记录冷查询游标分页结果（NextCursor 空串表示无下一页）。
type ColdPage struct {
	Items      []model.SchedDecisionV2
	NextCursor string
}

// ListCold 冷查询并表（FR-152，spec §4.4）：归档不可达即 503，跨热 / 冷 keyset 并表（时间范围与
// cold-query-max-days 跨度已由 handler 校验）。result 枚举仍校验，非法 400。
func (s *SchedDecisionQueryService) ListCold(p ListSchedDecisionsParams) (ColdPage, error) {
	if !s.repo.HasArchive() {
		return ColdPage{}, apperr.ErrArchiveUnavailable
	}
	switch p.Result {
	case "", "success", "failed":
	default:
		return ColdPage{}, apperr.ErrInvalidParam
	}
	rows, nextToken, err := s.repo.QueryRangeCold(repository.SchedDecisionQuery{
		NamespaceID: p.NamespaceID, Zone: p.Zone, ServerID: p.ServerID, Result: p.Result,
		FromMs: p.FromMs, ToMs: p.ToMs,
	}, p.ColdCursor, pageSize(p.PageSize))
	if err != nil {
		return ColdPage{}, err
	}
	return ColdPage{Items: rows, NextCursor: nextToken}, nil
}

// validateListParams 校验列表参数：from/to 必填且有序、范围 ≤60 天、result 枚举合法。
func validateListParams(p ListSchedDecisionsParams) error {
	if p.FromMs <= 0 || p.ToMs <= 0 || p.FromMs > p.ToMs {
		return apperr.ErrInvalidParam
	}
	if p.ToMs-p.FromMs > int64(schedDecisionRetentionDays)*24*int64(time.Hour/time.Millisecond) {
		return apperr.ErrInvalidParam
	}
	switch p.Result {
	case "", "success", "failed":
		return nil
	default:
		return apperr.ErrInvalidParam
	}
}

// Detail 按 traceId 查单条决策：自今日起在保留窗内逆序逐日表查，未命中 404 decision_not_found。
func (s *SchedDecisionQueryService) Detail(traceID string) (model.SchedDecisionV2, error) {
	if traceID == "" || len(traceID) > 36 {
		return model.SchedDecisionV2{}, apperr.ErrInvalidParam
	}
	row, err := s.repo.FindByTraceID(traceID, schedDecisionRetentionDays)
	if err != nil {
		return model.SchedDecisionV2{}, err
	}
	if row == nil {
		return model.SchedDecisionV2{}, apperr.ErrSchedDecisionNotFound
	}
	return *row, nil
}

// SchedFailReasonCount 是失败原因 Top 中的一项。
type SchedFailReasonCount struct {
	Reason string
	Count  int64
}

// SchedDecisionSummaryResult 是决策概览聚合结果（字段对齐 contracts SchedDecisionSummary）。
type SchedDecisionSummaryResult struct {
	Window               string
	Total                int64
	SuccessCount         int64
	SuccessRatePercent   float64
	FailReasonTop        []SchedFailReasonCount
	LocalFallbackPercent float64
}

// Summary 聚合最近 window 时间窗内的决策概览：总数、成功率、失败原因 Top、降级补报占比。
// window 支持 Go 时长写法（至少 1h / 24h），缺省 1h；非法 / 非正 / 超保留窗 → 400。
func (s *SchedDecisionQueryService) Summary(window string) (SchedDecisionSummaryResult, error) {
	if window == "" {
		window = schedSummaryDefaultWindow
	}
	d, err := time.ParseDuration(window)
	if err != nil || d <= 0 || d > schedDecisionRetentionDays*24*time.Hour {
		return SchedDecisionSummaryResult{}, apperr.ErrInvalidParam
	}
	to := s.now()
	agg, err := s.repo.Summarize(to.Add(-d).UnixMilli(), to.UnixMilli())
	if err != nil {
		return SchedDecisionSummaryResult{}, err
	}
	return SchedDecisionSummaryResult{
		Window:               window,
		Total:                agg.Total,
		SuccessCount:         agg.SuccessCount,
		SuccessRatePercent:   percentOf(agg.SuccessCount, agg.Total, 100),
		FailReasonTop:        sortFailReasons(agg.FailReasonCounts),
		LocalFallbackPercent: percentOf(agg.FallbackCount, agg.Total, 0),
	}, nil
}

// percentOf 计算 part/total 的百分比（保留 1 位小数）；total 为 0 时返回 empty 缺省值
// （成功率空窗按 100、降级占比空窗按 0，对齐 devmock 口径）。
func percentOf(part, total int64, empty float64) float64 {
	if total == 0 {
		return empty
	}
	return math.Round(float64(part)/float64(total)*1000) / 10
}

// sortFailReasons 把失败原因计数整理为 Top 列表：按计数降序，同数按原因码升序（输出确定）。
func sortFailReasons(counts map[string]int64) []SchedFailReasonCount {
	top := make([]SchedFailReasonCount, 0, len(counts))
	for reason, count := range counts {
		top = append(top, SchedFailReasonCount{Reason: reason, Count: count})
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Count != top[j].Count {
			return top[i].Count > top[j].Count
		}
		return top[i].Reason < top[j].Reason
	})
	return top
}
