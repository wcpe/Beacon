package service

import (
	"sort"
	"time"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// 审计分页默认与上限。
const (
	defaultAuditPageSize = 20
	maxAuditPageSize     = 200
)

// 审计分析时间窗默认与上限（FR-73）。
const (
	defaultAuditAnalyticsWindow = 30 * 24 * time.Hour // 缺省窗口：to 往前 30 天
	maxAuditAnalyticsWindow     = 92 * 24 * time.Hour // 窗口上限 92 天，超出拒查防一次性捞过量行
	auditAnalyticsDayLayout     = "2006-01-02"        // 按 UTC 日分桶的日期键格式
)

// AuditActionCount 是某动作的计数（byAction 元素）。
type AuditActionCount struct {
	Action string
	Count  int
}

// AuditDayCount 是某 UTC 日的计数（byDay 元素）。
type AuditDayCount struct {
	Date  string
	Count int
}

// AuditAnalytics 是窗口内审计活动的聚合结果（FR-73 契约）。
type AuditAnalytics struct {
	From      time.Time
	To        time.Time
	Total     int
	OKCount   int
	FailCount int
	ByAction  []AuditActionCount // 按 count 降序
	ByDay     []AuditDayCount    // 按 date 升序
}

// AuditService 提供审计查询。
type AuditService struct {
	repo *repository.AuditLogRepository
}

// NewAuditService 构造服务。
func NewAuditService(repo *repository.AuditLogRepository) *AuditService {
	return &AuditService{repo: repo}
}

// List 分页查询审计；规整 page/size 后委托仓库。
func (s *AuditService) List(f repository.AuditFilter) ([]model.AuditLog, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size < 1 {
		f.Size = defaultAuditPageSize
	}
	if f.Size > maxAuditPageSize {
		f.Size = maxAuditPageSize
	}
	return s.repo.List(f)
}

// Analytics 聚合窗口内审计活动（计数 / 成功率 / 按动作分布 / 每日趋势）。
// 时间窗缺省与 92 天上限在此校验（超限返 ErrInvalidParam → handler 转 400）；
// 日分桶 + 计数在 Go 侧单遍完成（禁方言日期函数，保 Postgres 可移植）。
func (s *AuditService) Analytics(f repository.AuditFilter) (*AuditAnalytics, error) {
	if f.To.IsZero() {
		f.To = time.Now().UTC()
	}
	if f.From.IsZero() {
		f.From = f.To.Add(-defaultAuditAnalyticsWindow)
	}
	if f.From.After(f.To) {
		return nil, apperr.ErrInvalidParam
	}
	if f.To.Sub(f.From) > maxAuditAnalyticsWindow {
		return nil, apperr.ErrInvalidParam
	}
	rows, err := s.repo.ScanForAnalytics(f)
	if err != nil {
		return nil, err
	}
	return aggregateAuditAnalytics(f.From, f.To, rows), nil
}

// aggregateAuditAnalytics 单遍扫描投影行，计 total/ok/fail 与 byAction/byDay 分桶，再排序成结果。
// 空结果时 ByAction/ByDay 为空切片（非 nil），保证序列化为 []，total=0 不 panic。
func aggregateAuditAnalytics(from, to time.Time, rows []repository.AuditAnalyticsRow) *AuditAnalytics {
	actionCounts := make(map[string]int)
	dayCounts := make(map[string]int)
	result := &AuditAnalytics{
		From:     from,
		To:       to,
		Total:    len(rows),
		ByAction: []AuditActionCount{},
		ByDay:    []AuditDayCount{},
	}
	for _, row := range rows {
		if row.Result == model.ResultOK {
			result.OKCount++
		} else {
			result.FailCount++
		}
		actionCounts[row.Action]++
		dayCounts[row.CreatedAt.UTC().Format(auditAnalyticsDayLayout)]++
	}
	for action, count := range actionCounts {
		result.ByAction = append(result.ByAction, AuditActionCount{Action: action, Count: count})
	}
	// 按 count 降序；同 count 按 action 字典序，保证输出稳定。
	sort.Slice(result.ByAction, func(i, j int) bool {
		if result.ByAction[i].Count != result.ByAction[j].Count {
			return result.ByAction[i].Count > result.ByAction[j].Count
		}
		return result.ByAction[i].Action < result.ByAction[j].Action
	})
	for date, count := range dayCounts {
		result.ByDay = append(result.ByDay, AuditDayCount{Date: date, Count: count})
	}
	// 按 date 升序（YYYY-MM-DD 字典序即时间序）。
	sort.Slice(result.ByDay, func(i, j int) bool {
		return result.ByDay[i].Date < result.ByDay[j].Date
	})
	return result
}
