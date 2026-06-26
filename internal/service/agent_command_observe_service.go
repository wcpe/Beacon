package service

import (
	"sort"
	"time"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// 命令观测分页默认与上限（FR-104，与审计同口径）。
const (
	defaultCommandPageSize = 20
	maxCommandPageSize     = 200
)

// 命令观测聚合时间窗默认与上限（FR-104，仿 FR-73）。
const (
	defaultCommandAnalyticsWindow = 30 * 24 * time.Hour // 缺省窗口：to 往前 30 天
	maxCommandAnalyticsWindow     = 92 * 24 * time.Hour // 窗口上限 92 天，超出拒查防一次性捞过量行
	commandAnalyticsDayLayout     = "2006-01-02"        // 按 UTC 日分桶的日期键格式
	commandServerTopN             = 10                  // 按服务器计数取 top-N（运维场景够用）
)

// CommandStatusCount 是某状态的计数（byStatus 元素）。
type CommandStatusCount struct {
	Status string
	Count  int
}

// CommandTypeCount 是某类型的计数（byType 元素）。
type CommandTypeCount struct {
	Type  string
	Count int
}

// CommandServerCount 是某服务器的计数（byServer 元素，top-N）。
type CommandServerCount struct {
	ServerID string
	Count    int
}

// CommandDayCount 是某 UTC 日的命令量分桶：下发（全部新建）/ 完成（done）/ 失败（failed/expired）。
type CommandDayCount struct {
	Date   string
	Issued int // 该日下发（创建）命令总数
	Done   int // 该日下发命令里终态为 done 的数
	Failed int // 该日下发命令里终态为 failed / expired 的数
}

// CommandAnalytics 是窗口内命令活动的聚合结果（FR-104 契约）。
type CommandAnalytics struct {
	From     time.Time
	To       time.Time
	Total    int
	ByStatus []CommandStatusCount // 按 count 降序，同 count 按 status 字典序
	ByType   []CommandTypeCount   // 按 count 降序，同 count 按 type 字典序
	ByServer []CommandServerCount // top-N，按 count 降序，同 count 按 serverId 字典序
	ByDay    []CommandDayCount    // 按 date 升序
}

// CommandObserveService 提供命令观测的只读查询与聚合（FR-104，增强 FR-17/FR-82）。
// 只读：不持有写命令 / 改命令的任何旁路；不暴露 imprint / log 瞬态敏感内容（投影在 repo 已排除）。
type CommandObserveService struct {
	repo *repository.AgentCommandRepository
}

// NewCommandObserveService 构造服务。
func NewCommandObserveService(repo *repository.AgentCommandRepository) *CommandObserveService {
	return &CommandObserveService{repo: repo}
}

// List 分页查询命令元数据；规整 page/size、校验 type/status 枚举后委托仓库（FR-104）。
// type / status 传入非法枚举返 ErrInvalidParam（handler 转 400）；空值不过滤。
func (s *CommandObserveService) List(f repository.CommandFilter) ([]repository.CommandMeta, int64, error) {
	if f.Type != "" && !model.IsValidCommandType(f.Type) {
		return nil, 0, apperr.ErrInvalidParam
	}
	if f.Status != "" && !model.IsValidCommandStatus(f.Status) {
		return nil, 0, apperr.ErrInvalidParam
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size < 1 {
		f.Size = defaultCommandPageSize
	}
	if f.Size > maxCommandPageSize {
		f.Size = maxCommandPageSize
	}
	return s.repo.List(f)
}

// Analytics 聚合窗口内命令活动（总数 / 按状态 / 按类型 / 按服务器 top-N / 命令量每日趋势）。
// 时间窗缺省与 92 天上限在此校验（超限返 ErrInvalidParam → handler 转 400）；
// 日分桶 + 计数在 Go 侧单遍完成（禁方言日期函数，保 Postgres 可移植，仿 FR-73）。
func (s *CommandObserveService) Analytics(f repository.CommandFilter) (*CommandAnalytics, error) {
	if f.To.IsZero() {
		f.To = time.Now().UTC()
	}
	if f.From.IsZero() {
		f.From = f.To.Add(-defaultCommandAnalyticsWindow)
	}
	if f.From.After(f.To) {
		return nil, apperr.ErrInvalidParam
	}
	if f.To.Sub(f.From) > maxCommandAnalyticsWindow {
		return nil, apperr.ErrInvalidParam
	}
	rows, err := s.repo.ScanForAnalytics(f)
	if err != nil {
		return nil, err
	}
	return aggregateCommandAnalytics(f.From, f.To, rows), nil
}

// aggregateCommandAnalytics 单遍扫描投影行，计 total 与 byStatus/byType/byServer/byDay 分桶，再排序成结果。
// 空结果时各切片为空切片（非 nil），保证序列化为 []，total=0 不 panic。
func aggregateCommandAnalytics(from, to time.Time, rows []repository.CommandAnalyticsRow) *CommandAnalytics {
	statusCounts := make(map[string]int)
	typeCounts := make(map[string]int)
	serverCounts := make(map[string]int)
	// 每日桶：date → {issued, done, failed}
	dayBuckets := make(map[string]*CommandDayCount)

	result := &CommandAnalytics{
		From:     from,
		To:       to,
		Total:    len(rows),
		ByStatus: []CommandStatusCount{},
		ByType:   []CommandTypeCount{},
		ByServer: []CommandServerCount{},
		ByDay:    []CommandDayCount{},
	}
	for _, row := range rows {
		statusCounts[row.Status]++
		typeCounts[row.Type]++
		serverCounts[row.ServerID]++

		day := row.CreatedAt.UTC().Format(commandAnalyticsDayLayout)
		bucket := dayBuckets[day]
		if bucket == nil {
			bucket = &CommandDayCount{Date: day}
			dayBuckets[day] = bucket
		}
		bucket.Issued++
		switch row.Status {
		case model.CommandStatusDone:
			bucket.Done++
		case model.CommandStatusFailed, model.CommandStatusExpired:
			bucket.Failed++
		}
	}

	for status, count := range statusCounts {
		result.ByStatus = append(result.ByStatus, CommandStatusCount{Status: status, Count: count})
	}
	sortCountDesc(result.ByStatus, func(i int) (int, string) {
		return result.ByStatus[i].Count, result.ByStatus[i].Status
	})

	for typ, count := range typeCounts {
		result.ByType = append(result.ByType, CommandTypeCount{Type: typ, Count: count})
	}
	sortCountDesc(result.ByType, func(i int) (int, string) {
		return result.ByType[i].Count, result.ByType[i].Type
	})

	for server, count := range serverCounts {
		result.ByServer = append(result.ByServer, CommandServerCount{ServerID: server, Count: count})
	}
	sortCountDesc(result.ByServer, func(i int) (int, string) {
		return result.ByServer[i].Count, result.ByServer[i].ServerID
	})
	// 取 top-N（按 count 降序后截断）。
	if len(result.ByServer) > commandServerTopN {
		result.ByServer = result.ByServer[:commandServerTopN]
	}

	for _, bucket := range dayBuckets {
		result.ByDay = append(result.ByDay, *bucket)
	}
	// 按 date 升序（YYYY-MM-DD 字典序即时间序）。
	sort.Slice(result.ByDay, func(i, j int) bool {
		return result.ByDay[i].Date < result.ByDay[j].Date
	})
	return result
}

// sortCountDesc 按 (count 降序, key 字典序升序) 稳定排序切片：key(i) 返回第 i 项的 (count, 字典序键)。
// 抽公共比较消除三处 byStatus/byType/byServer 排序的复制粘贴。
func sortCountDesc[T any](s []T, key func(i int) (int, string)) {
	sort.Slice(s, func(i, j int) bool {
		ci, ki := key(i)
		cj, kj := key(j)
		if ci != cj {
			return ci > cj
		}
		return ki < kj
	})
}
