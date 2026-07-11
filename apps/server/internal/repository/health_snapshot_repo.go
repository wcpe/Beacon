package repository

import (
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// healthSnapshotInsertBatchSize 是单条批量插入的分批大小（上界保护，避免单 SQL 参数过多）。
const healthSnapshotInsertBatchSize = 200

// HealthSnapshotRepository 提供健康快照日表（health_snapshot_YYYYMMDD）的数据访问（FR-147，见 §3.2）：
// 按行内 ts_ms 定当日表、跨日批自动拆分批量写（快照无唯一键，不做冲突去重）。
type HealthSnapshotRepository struct {
	db *gorm.DB
}

// NewHealthSnapshotRepository 构造仓库。
func NewHealthSnapshotRepository(db *gorm.DB) *HealthSnapshotRepository {
	return &HealthSnapshotRepository{db: db}
}

// FlushDaily 批量写一批（可能跨日）快照行到各自当日表；返回值 0 为写入通道契约的去重数占位
// （快照每 30s 单向产出、无重放语义，故无去重）。
//
// 流程：① 按 ts_ms 的 UTC 日期分组；② 事务外按需建各当日表（DDL 在 MySQL 触发隐式提交，
// 必须在事务外，见 store.EnsureDailyTable）；③ 一个事务内逐日批插。失败整事务回滚交上层重试。
func (r *HealthSnapshotRepository) FlushDaily(rows []model.HealthSnapshot) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	byDay := groupSnapshotsByDay(rows)
	// 先在事务外确保所有目标日表存在（DDL 隐式提交，不能置于下面的事务内）。
	tableByDay := make(map[time.Time]string, len(byDay))
	for day := range byDay {
		name, err := store.EnsureDailyTable(r.db, &model.HealthSnapshot{}, day)
		if err != nil {
			return 0, err
		}
		tableByDay[day] = name
	}
	err := r.db.Transaction(func(tx *gorm.DB) error {
		for day, dayRows := range byDay {
			if insErr := tx.Table(tableByDay[day]).
				CreateInBatches(&dayRows, healthSnapshotInsertBatchSize).Error; insErr != nil {
				return insErr
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return 0, nil
}

// groupSnapshotsByDay 按 ts_ms 对应的 UTC 日（零点）分组，支撑跨日批拆分。
func groupSnapshotsByDay(rows []model.HealthSnapshot) map[time.Time][]model.HealthSnapshot {
	byDay := make(map[time.Time][]model.HealthSnapshot)
	for _, row := range rows {
		day := utcDayStart(row.TsMs)
		byDay[day] = append(byDay[day], row)
	}
	return byDay
}
