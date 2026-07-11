package repository

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// schedDecisionInsertBatchSize 是单条批量插入的分批大小（上界保护，防单 SQL 参数触达驱动上限）。
const schedDecisionInsertBatchSize = 200

// SchedDecisionV2Repository 提供调度决策日表（sched_decision_YYYYMMDD）的数据访问（FR-146）：
// 按行内 ts_ms 定当日表、跨日批自动拆分、幂等批量写（trace_id 唯一键冲突忽略并计去重）。
type SchedDecisionV2Repository struct {
	db *gorm.DB
}

// NewSchedDecisionV2Repository 构造仓库。
func NewSchedDecisionV2Repository(db *gorm.DB) *SchedDecisionV2Repository {
	return &SchedDecisionV2Repository{db: db}
}

// FlushDaily 幂等批量写一批（可能跨日）决策行到各自当日表，返回被唯一键去重的行数。
//
// 流程：① 按 ts_ms 的 UTC 日期分组；② 事务外按需建各当日表（DDL 在 MySQL 触发隐式提交，
// 必须在事务外，见 store.EnsureDailyTable）；③ 一个事务内逐日 OnConflict{trace_id, DoNothing}
// 批插到其当日表，汇总去重数。任一日写失败整事务回滚，交由写入通道重试（幂等，重放安全）。
func (r *SchedDecisionV2Repository) FlushDaily(rows []model.SchedDecisionV2) (deduplicated int, err error) {
	if len(rows) == 0 {
		return 0, nil
	}
	byDay := groupByUTCDay(rows, func(row model.SchedDecisionV2) int64 { return row.TsMs })
	// 先在事务外确保所有目标日表存在（DDL 隐式提交，不能置于下面的事务内）。
	tableByDay := make(map[time.Time]string, len(byDay))
	for day := range byDay {
		name, ensureErr := store.EnsureDailyTable(r.db, &model.SchedDecisionV2{}, day)
		if ensureErr != nil {
			return 0, ensureErr
		}
		tableByDay[day] = name
	}
	err = r.db.Transaction(func(tx *gorm.DB) error {
		for day, dayRows := range byDay {
			dedup, insErr := insertSchedBatchIgnore(tx, tableByDay[day], dayRows)
			if insErr != nil {
				return insErr
			}
			deduplicated += dedup
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deduplicated, nil
}

// insertSchedBatchIgnore 幂等批插到指定日表：trace_id 唯一键冲突即忽略，
// 返回被去重（未插入）的行数（= 期望行数 − 实际插入行数）。
func insertSchedBatchIgnore(tx *gorm.DB, tableName string, rows []model.SchedDecisionV2) (int, error) {
	res := tx.Table(tableName).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "trace_id"}}, DoNothing: true}).
		CreateInBatches(&rows, schedDecisionInsertBatchSize)
	if res.Error != nil {
		return 0, res.Error
	}
	deduplicated := len(rows) - int(res.RowsAffected)
	if deduplicated < 0 {
		deduplicated = 0
	}
	return deduplicated, nil
}

// groupByUTCDay 按行内毫秒时间戳把行归组到其 UTC 当日零点，支撑跨日批拆分（泛型，键函数取时间戳）。
func groupByUTCDay[T any](rows []T, msOf func(T) int64) map[time.Time][]T {
	byDay := make(map[time.Time][]T)
	for _, row := range rows {
		day := utcDayStart(msOf(row))
		byDay[day] = append(byDay[day], row)
	}
	return byDay
}

// SchedDecisionQuery 是决策记录跨日并表查询的过滤与分页参数（spec §5.2 列表端点）。
type SchedDecisionQuery struct {
	NamespaceID uint
	Zone        string
	ServerID    string // 匹配 requester 或 chosen 任一（对齐 devmock 语义）
	Result      string // "" 全部 / success（fail_reason 空）/ failed（fail_reason 非空）
	FromMs      int64
	ToMs        int64
	Offset      int
	Limit       int
}

// QueryRange 跨日并表分页查询决策记录（ts_ms 降序）：只查范围内**已存在**的日表
// （Migrator().HasTable 判存，缺表跳过，查询侧严禁隐式建表），逐表计数后按偏移跨表取页。
func (r *SchedDecisionV2Repository) QueryRange(q SchedDecisionQuery) ([]model.SchedDecisionV2, int64, error) {
	tables := r.existingTablesInRange(q.FromMs, q.ToMs)
	counts := make([]int64, len(tables))
	var total int64
	for i, tbl := range tables {
		if err := applySchedFilters(r.db.Table(tbl), q).Count(&counts[i]).Error; err != nil {
			return nil, 0, err
		}
		total += counts[i]
	}
	rows := make([]model.SchedDecisionV2, 0, q.Limit)
	offset := int64(q.Offset)
	for i, tbl := range tables {
		if len(rows) >= q.Limit {
			break
		}
		if offset >= counts[i] {
			offset -= counts[i]
			continue
		}
		var chunk []model.SchedDecisionV2
		err := applySchedFilters(r.db.Table(tbl), q).
			Order("ts_ms DESC, id DESC").
			Offset(int(offset)).Limit(q.Limit - len(rows)).
			Find(&chunk).Error
		if err != nil {
			return nil, 0, err
		}
		rows = append(rows, chunk...)
		offset = 0
	}
	return rows, total, nil
}

// FindByTraceID 自今日起在保留窗内逆序逐日表按 trace_id 查（缺表跳过），命中即返；未命中返回 nil。
func (r *SchedDecisionV2Repository) FindByTraceID(traceID string, retentionDays int) (*model.SchedDecisionV2, error) {
	day := utcDayStart(time.Now().UTC().UnixMilli())
	for i := 0; i < retentionDays; i++ {
		name := store.DailyTableName(model.SchedDecisionV2{}.TableName(), day)
		day = day.AddDate(0, 0, -1)
		if !r.db.Migrator().HasTable(name) {
			continue
		}
		var row model.SchedDecisionV2
		res := r.db.Table(name).Where("trace_id = ?", traceID).Limit(1).Find(&row)
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected > 0 {
			return &row, nil
		}
	}
	return nil, nil
}

// SchedDecisionAggregate 是概览聚合的原始计数（比率换算归 service 层）。
type SchedDecisionAggregate struct {
	Total            int64
	SuccessCount     int64
	FallbackCount    int64            // source=local_fallback（降级补报）
	FailReasonCounts map[string]int64 // 仅失败行，按 fail_reason 计数
}

// schedGroupCount 是聚合查询的分组行投影。
type schedGroupCount struct {
	FailReason string
	Source     string
	N          int64
}

// Summarize 聚合时间窗内决策总数 / 成功数 / 降级补报数 / 失败原因分布（跨已存在日表累加，缺表跳过）。
func (r *SchedDecisionV2Repository) Summarize(fromMs, toMs int64) (SchedDecisionAggregate, error) {
	agg := SchedDecisionAggregate{FailReasonCounts: map[string]int64{}}
	for _, tbl := range r.existingTablesInRange(fromMs, toMs) {
		var groups []schedGroupCount
		err := r.db.Table(tbl).
			Select("fail_reason, source, COUNT(*) AS n").
			Where("ts_ms >= ? AND ts_ms <= ?", fromMs, toMs).
			Group("fail_reason").Group("source").
			Find(&groups).Error
		if err != nil {
			return SchedDecisionAggregate{}, err
		}
		for _, g := range groups {
			agg.Total += g.N
			if g.FailReason == "" {
				agg.SuccessCount += g.N
			} else {
				agg.FailReasonCounts[g.FailReason] += g.N
			}
			if g.Source == model.SchedSourceLocalFallback {
				agg.FallbackCount += g.N
			}
		}
	}
	return agg, nil
}

// existingTablesInRange 枚举 [fromMs, toMs] 覆盖的 UTC 日中**已存在**的决策日表名（新→旧）。
// 查询侧只判存不建表（HasTable），避免只读查询隐式产生空日表。
func (r *SchedDecisionV2Repository) existingTablesInRange(fromMs, toMs int64) []string {
	base := model.SchedDecisionV2{}.TableName()
	first := utcDayStart(fromMs)
	tables := make([]string, 0, 4)
	for day := utcDayStart(toMs); !day.Before(first); day = day.AddDate(0, 0, -1) {
		name := store.DailyTableName(base, day)
		if r.db.Migrator().HasTable(name) {
			tables = append(tables, name)
		}
	}
	return tables
}

// applySchedFilters 套用决策记录的时间窗与过滤条件（result 语义对齐 contracts / devmock）。
func applySchedFilters(q *gorm.DB, p SchedDecisionQuery) *gorm.DB {
	q = q.Where("ts_ms >= ? AND ts_ms <= ?", p.FromMs, p.ToMs)
	if p.NamespaceID != 0 {
		q = q.Where("namespace_id = ?", p.NamespaceID)
	}
	if p.Zone != "" {
		q = q.Where("zone_name = ?", p.Zone)
	}
	if p.ServerID != "" {
		q = q.Where("requester_server_id = ? OR chosen_server_id = ?", p.ServerID, p.ServerID)
	}
	switch p.Result {
	case "success":
		q = q.Where("fail_reason = ''")
	case "failed":
		q = q.Where("fail_reason <> ''")
	}
	return q
}
