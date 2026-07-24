package repository

import (
	"sort"
	"strconv"
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
	db        *gorm.DB
	archiveDB *gorm.DB // 归档库连接，供冷查询并表（FR-152）；nil 表示不可达 / 未配置
}

// NewHealthSnapshotRepository 构造仓库。
func NewHealthSnapshotRepository(db *gorm.DB) *HealthSnapshotRepository {
	return &HealthSnapshotRepository{db: db}
}

// SetArchiveDB 注入归档库连接供冷查询（includeArchived）并表（FR-152，见 ADR-0066）。
func (r *HealthSnapshotRepository) SetArchiveDB(archiveDB *gorm.DB) { r.archiveDB = archiveDB }

// HasArchive 归档库连接是否就绪（冷查询可用性）。
func (r *HealthSnapshotRepository) HasArchive() bool { return r.archiveDB != nil }

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

// QueryRange 查某 server 在 [fromMs, toMs] 的快照（跨日并表、ts_ms 升序）。
// 查询侧严禁隐式建表：逐日 Migrator().HasTable 判存在，缺表跳过（该日无数据）。
func (r *HealthSnapshotRepository) QueryRange(serverID string, fromMs, toMs int64) ([]model.HealthSnapshot, error) {
	return r.queryRangeOn(r.db, serverID, fromMs, toMs)
}

// QueryRangeCold 冷查询并表（FR-152，spec §4.4）：对热 + 归档连接执行同构区间查询，
// 应用层按 (server_id, ts_ms) 去重保热侧并表（无分页，整段交上层聚合）。归并后仍按 ts_ms 升序。
func (r *HealthSnapshotRepository) QueryRangeCold(serverID string, fromMs, toMs int64) ([]model.HealthSnapshot, error) {
	hot, err := r.queryRangeOn(r.db, serverID, fromMs, toMs)
	if err != nil {
		return nil, err
	}
	arc, err := r.queryRangeOn(r.archiveDB, serverID, fromMs, toMs)
	if err != nil {
		return nil, err
	}
	merged := unionColdRows(hot, arc, healthSnapshotDedupKey)
	sort.Slice(merged, func(i, j int) bool { return merged[i].TsMs < merged[j].TsMs })
	return merged, nil
}

// queryRangeOn 在指定连接上按 [fromMs, toMs] 跨日并表查快照（ts_ms 升序、缺表跳过、禁隐式建表）。
func (r *HealthSnapshotRepository) queryRangeOn(db *gorm.DB, serverID string, fromMs, toMs int64) ([]model.HealthSnapshot, error) {
	out := make([]model.HealthSnapshot, 0, 64)
	for _, day := range utcDaysBetween(fromMs, toMs) {
		name := store.DailyTableName(model.HealthSnapshot{}.TableName(), day)
		if !db.Migrator().HasTable(name) {
			continue
		}
		var rows []model.HealthSnapshot
		if err := db.Table(name).
			Where("server_id = ? AND ts_ms >= ? AND ts_ms <= ?", serverID, fromMs, toMs).
			Order("ts_ms ASC").Find(&rows).Error; err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// healthSnapshotDedupKey 冷查询去重键：快照无唯一键，身份 = (server_id, ts_ms)。
func healthSnapshotDedupKey(s model.HealthSnapshot) string {
	return s.ServerID + "\x00" + strconv.FormatInt(s.TsMs, 10)
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
