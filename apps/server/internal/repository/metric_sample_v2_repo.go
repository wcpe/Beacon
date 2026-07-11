package repository

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// metricV2InsertBatchSize 是单条批量插入的分批大小（避免单 SQL 参数过多触达驱动上限）。
// 写入协程每次 flush 攒约 200 行，分批主要作上界保护。
const metricV2InsertBatchSize = 200

// MetricSampleV2Repository 提供 P4 指标批日表（metric_sample_YYYYMMDD）的数据访问（FR-144）：
// 按行内 bucket_start_ms 定当日表、跨日批自动拆分、幂等批量写（唯一键冲突忽略并计去重）。
type MetricSampleV2Repository struct {
	db *gorm.DB
}

// NewMetricSampleV2Repository 构造仓库。
func NewMetricSampleV2Repository(db *gorm.DB) *MetricSampleV2Repository {
	return &MetricSampleV2Repository{db: db}
}

// WithTx 返回绑定到事务的仓库副本（供内部事务复用；对外接口仍以 FlushDaily 为主）。
func (r *MetricSampleV2Repository) WithTx(tx *gorm.DB) *MetricSampleV2Repository {
	return &MetricSampleV2Repository{db: tx}
}

// FlushDaily 幂等批量写一批（可能跨日）聚合行到各自当日表，返回被唯一键去重的行数。
//
// 流程：① 按 bucket_start_ms 的 UTC 日期分组；② 事务外按需建各当日表（DDL 在 MySQL 触发隐式提交，
// 必须在事务外，见 store.EnsureDailyTable）；③ 一个事务内逐日 OnConflict{DoNothing} 批插到其当日表，
// 汇总去重数。任一日写失败整事务回滚，交由上层重试（幂等，重放安全）。
func (r *MetricSampleV2Repository) FlushDaily(rows []model.MetricSampleV2) (deduplicated int, err error) {
	if len(rows) == 0 {
		return 0, nil
	}
	byDay := groupRowsByDay(rows)
	// 先在事务外确保所有目标日表存在（DDL 隐式提交，不能置于下面的事务内）。
	tableByDay := make(map[time.Time]string, len(byDay))
	for day := range byDay {
		name, ensureErr := store.EnsureDailyTable(r.db, &model.MetricSampleV2{}, day)
		if ensureErr != nil {
			return 0, ensureErr
		}
		tableByDay[day] = name
	}
	err = r.db.Transaction(func(tx *gorm.DB) error {
		for day, dayRows := range byDay {
			dedup, insErr := insertBatchIgnore(tx, tableByDay[day], dayRows)
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

// insertBatchIgnore 幂等批插到指定日表：唯一键 (server_id, bucket_start_ms) 冲突即忽略，
// 返回被去重（未插入）的行数（= 期望行数 − 实际插入行数）。
func insertBatchIgnore(tx *gorm.DB, tableName string, rows []model.MetricSampleV2) (int, error) {
	res := tx.Table(tableName).
		Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(&rows, metricV2InsertBatchSize)
	if res.Error != nil {
		return 0, res.Error
	}
	deduplicated := len(rows) - int(res.RowsAffected)
	if deduplicated < 0 {
		deduplicated = 0
	}
	return deduplicated, nil
}

// QueryRange 查一组 server 在 [fromMs, toMs] 的 5s 聚合行（跨日并表、bucket_start_ms 升序）。
// serverIDs 必填（1000+ 子服禁全量扫，§5.2）；查询侧严禁隐式建表——逐日 HasTable 判存在，缺表跳过。
func (r *MetricSampleV2Repository) QueryRange(serverIDs []string, fromMs, toMs int64) ([]model.MetricSampleV2, error) {
	if len(serverIDs) == 0 {
		return []model.MetricSampleV2{}, nil
	}
	out := make([]model.MetricSampleV2, 0, 256)
	for _, day := range utcDaysBetween(fromMs, toMs) {
		name := store.DailyTableName(model.MetricSampleV2{}.TableName(), day)
		if !r.db.Migrator().HasTable(name) {
			continue
		}
		var rows []model.MetricSampleV2
		if err := r.db.Table(name).
			Where("server_id IN ? AND bucket_start_ms >= ? AND bucket_start_ms <= ?", serverIDs, fromMs, toMs).
			Order("bucket_start_ms ASC").Find(&rows).Error; err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// groupRowsByDay 按 bucket_start_ms 对应的 UTC 日（零点）分组，支撑跨日批拆分。
func groupRowsByDay(rows []model.MetricSampleV2) map[time.Time]([]model.MetricSampleV2) {
	byDay := make(map[time.Time][]model.MetricSampleV2)
	for _, row := range rows {
		day := utcDayStart(row.BucketStartMs)
		byDay[day] = append(byDay[day], row)
	}
	return byDay
}

// utcDayStart 把毫秒时间戳归到其 UTC 当日零点，作日表分组键。
func utcDayStart(ms int64) time.Time {
	t := time.UnixMilli(ms).UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
