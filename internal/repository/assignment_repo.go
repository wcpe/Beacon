package repository

import (
	"errors"

	"gorm.io/gorm"

	"beacon/internal/model"
)

// ZoneAssignmentRepository 提供 zone_assignment 表的数据访问。
// M1 仅需按 serverId 解析归属（供有效配置解析）；指派 CRUD 在 M2 补充。
type ZoneAssignmentRepository struct {
	db *gorm.DB
}

// NewZoneAssignmentRepository 构造仓库。
func NewZoneAssignmentRepository(db *gorm.DB) *ZoneAssignmentRepository {
	return &ZoneAssignmentRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *ZoneAssignmentRepository) WithTx(tx *gorm.DB) *ZoneAssignmentRepository {
	return &ZoneAssignmentRepository{db: tx}
}

// FindByServer 解析某 serverId 在某环境的未软删归属；未指派返回 (nil, nil)。
func (r *ZoneAssignmentRepository) FindByServer(ns, serverID string) (*model.ZoneAssignment, error) {
	var a model.ZoneAssignment
	err := r.db.Where("namespace_code = ? AND server_id = ? AND deleted_at = ?",
		ns, serverID, model.SoftDeleteSentinel).First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}
