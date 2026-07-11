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
