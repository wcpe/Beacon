package service

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// 审计导出支持的格式与流式批大小。
const (
	auditExportFormatCSV  = "csv"
	auditExportFormatJSON = "json"
	auditExportBatchSize  = 500 // 游标分批大小，平衡查询次数与内存峰值
)

// auditCSVHeader 是导出 CSV 的表头（与 auditExportRow 顺序一致）。
var auditCSVHeader = []string{
	"id", "namespace", "operator", "action", "targetType", "targetRef", "detail", "result", "clientIp", "createdAt",
}

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

// auditExportRow 是审计导出的对外行（小驼峰 JSON 键，与 handler 的 auditView 同口径）。
type auditExportRow struct {
	ID         uint      `json:"id"`
	Namespace  string    `json:"namespace"`
	Operator   string    `json:"operator"`
	Action     string    `json:"action"`
	TargetType string    `json:"targetType"`
	TargetRef  string    `json:"targetRef"`
	Detail     string    `json:"detail"`
	Result     string    `json:"result"`
	ClientIP   string    `json:"clientIp"`
	CreatedAt  time.Time `json:"createdAt"`
}

// toExportRow 把模型转为对外导出行。
func toExportRow(a model.AuditLog) auditExportRow {
	return auditExportRow{
		ID: a.ID, Namespace: a.NamespaceCode, Operator: a.Operator, Action: a.Action,
		TargetType: a.TargetType, TargetRef: a.TargetRef, Detail: a.Detail,
		Result: a.Result, ClientIP: a.ClientIP, CreatedAt: a.CreatedAt,
	}
}

// Export 按过滤条件流式导出全部命中审计（不分页）到 w，format 为 csv / json。
// 复用与 List 相同的过滤（含 DetailKeyword），按 repo.Stream 游标分批边查边写，不一次性载入内存（FR-84）。
// format 非法返回 ErrInvalidParam（handler 在写出响应头前转 400）。
func (s *AuditService) Export(f repository.AuditFilter, format string, w io.Writer) error {
	switch format {
	case auditExportFormatCSV:
		return s.exportCSV(f, w)
	case auditExportFormatJSON:
		return s.exportJSON(f, w)
	default:
		return apperr.ErrInvalidParam
	}
}

// exportCSV 先写表头，再按游标分批逐行写 CSV（时间倒序）。
func (s *AuditService) exportCSV(f repository.AuditFilter, w io.Writer) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(auditCSVHeader); err != nil {
		return err
	}
	if err := s.repo.Stream(f, auditExportBatchSize, func(batch []model.AuditLog) error {
		for _, a := range batch {
			if err := cw.Write([]string{
				strconv.FormatUint(uint64(a.ID), 10), a.NamespaceCode, a.Operator, a.Action,
				a.TargetType, a.TargetRef, a.Detail, a.Result, a.ClientIP,
				a.CreatedAt.UTC().Format(time.RFC3339),
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

// exportJSON 把全部命中审计按游标分批流式写成一个 JSON 数组（时间倒序），手写括号 / 逗号避免整体载入内存。
func (s *AuditService) exportJSON(f repository.AuditFilter, w io.Writer) error {
	if _, err := io.WriteString(w, "["); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	first := true
	if err := s.repo.Stream(f, auditExportBatchSize, func(batch []model.AuditLog) error {
		for _, a := range batch {
			if !first {
				if _, err := io.WriteString(w, ","); err != nil {
					return err
				}
			}
			first = false
			// Encode 自带换行分隔，作为数组元素间分隔无碍 JSON 合法性
			if err := enc.Encode(toExportRow(a)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	_, err := io.WriteString(w, "]")
	return err
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
