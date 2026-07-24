package repository

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// FileAssetScanRepository 提供每服扫描概要 file_asset_scan 的数据访问（FR-163，规格 v2-file-assets.md §3.2）。
// 一服一行，随清单上报整体刷新；manifest_digest 是增量上报的基线校准锚点。
type FileAssetScanRepository struct {
	db *gorm.DB
}

// NewFileAssetScanRepository 构造仓库。
func NewFileAssetScanRepository(db *gorm.DB) *FileAssetScanRepository {
	return &FileAssetScanRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本（供 service 在外层事务内复用）。
func (r *FileAssetScanRepository) WithTx(tx *gorm.DB) *FileAssetScanRepository {
	return &FileAssetScanRepository{db: tx}
}

// FindByServer 按 server_id 取概要行；不存在返回 (nil, nil)（首次上报前无行）。
func (r *FileAssetScanRepository) FindByServer(serverID uint) (*model.FileAssetScan, error) {
	var scan model.FileAssetScan
	err := r.db.Where("server_id = ?", serverID).First(&scan).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &scan, nil
}

// Upsert 按唯一键 server_id upsert 概要行（一服一行）：命中即整体刷新摘要 / 计数 / 扫描时刻。
// 显式声明冲突列避免 MySQL 空 `ON DUPLICATE KEY UPDATE` 语法错；created_at 不进 DoUpdates。须在外层事务内调用。
func (r *FileAssetScanRepository) Upsert(scan *model.FileAssetScan) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "server_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"namespace_id", "manifest_digest", "file_count", "total_size",
			"truncated", "scanned_at", "scan_duration_ms", "updated_at",
		}),
	}).Create(scan).Error
}

// ListByNamespace 分页列某 namespace 的扫描概要（管理面 scan-status，规格 §5.2）。
// serverID=0 表示不按服过滤；按 server_id 升序稳定排序。
func (r *FileAssetScanRepository) ListByNamespace(namespaceID, serverID uint, offset, limit int) ([]model.FileAssetScan, int64, error) {
	query := r.db.Model(&model.FileAssetScan{}).Where("namespace_id = ?", namespaceID)
	if serverID != 0 {
		query = query.Where("server_id = ?", serverID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.FileAssetScan
	if err := query.Order("server_id ASC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}
