package repository

import (
	"time"

	"gorm.io/gorm"

	"beacon/internal/model"
)

// metricInsertBatchSize 是批量插入的分批大小（避免单条 SQL 参数过多触达驱动上限）。
// 约 50 服规模下单轮采样远小于此值，分批主要作上界保护。
const metricInsertBatchSize = 200

// MetricSampleRepository 提供 metric_sample 表的数据访问：批量写样本、按时间窗查询、保留期清理（FR-32）。
type MetricSampleRepository struct {
	db *gorm.DB
}

// NewMetricSampleRepository 构造仓库。
func NewMetricSampleRepository(db *gorm.DB) *MetricSampleRepository {
	return &MetricSampleRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本（供 service 在事务内复用）。
func (r *MetricSampleRepository) WithTx(tx *gorm.DB) *MetricSampleRepository {
	return &MetricSampleRepository{db: tx}
}

// InsertBatch 批量写入一轮采样样本；空批为安全空操作（无在线实例时）。
func (r *MetricSampleRepository) InsertBatch(samples []model.MetricSample) error {
	if len(samples) == 0 {
		return nil
	}
	return r.db.CreateInBatches(samples, metricInsertBatchSize).Error
}

// Query 按 [from, to] 闭区间时间窗查询某 namespace 的样本，按 sampledAt 升序返回（便于出图）。
// serverID 为空时返回该 namespace 全部子服样本；非空时只返回该子服。
func (r *MetricSampleRepository) Query(namespace, serverID string, from, to time.Time) ([]model.MetricSample, error) {
	q := r.db.Model(&model.MetricSample{}).
		Where("namespace = ?", namespace).
		Where("sampled_at >= ? AND sampled_at <= ?", from, to)
	if serverID != "" {
		q = q.Where("server_id = ?", serverID)
	}
	var items []model.MetricSample
	if err := q.Order("sampled_at asc, id asc").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// DeleteBefore 删除早于 cutoff（不含）的样本，返回删除行数（保留期滚动清理用）。
func (r *MetricSampleRepository) DeleteBefore(cutoff time.Time) (int64, error) {
	res := r.db.Where("sampled_at < ?", cutoff).Delete(&model.MetricSample{})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
