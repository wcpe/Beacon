package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"beacon/internal/model"
)

// ZoneAssignmentRepository 提供 zone_assignment 表的数据访问。
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

// Upsert 新增或改派某 serverId 的归属（按 (ns, serverId) 唯一）。
func (r *ZoneAssignmentRepository) Upsert(ns, serverID, group, zone, note string) (*model.ZoneAssignment, error) {
	existing, err := r.FindByServer(ns, serverID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		existing.GroupCode, existing.ZoneCode, existing.Note = group, zone, note
		if err := r.db.Save(existing).Error; err != nil {
			return nil, err
		}
		return existing, nil
	}
	a := &model.ZoneAssignment{NamespaceCode: ns, ServerID: serverID, GroupCode: group, ZoneCode: zone, Note: note}
	if err := r.db.Create(a).Error; err != nil {
		return nil, err
	}
	return a, nil
}

// List 按可选条件列出未软删的指派。
func (r *ZoneAssignmentRepository) List(ns, group, zone string) ([]model.ZoneAssignment, error) {
	q := r.db.Where("deleted_at = ?", model.SoftDeleteSentinel)
	if ns != "" {
		q = q.Where("namespace_code = ?", ns)
	}
	if group != "" {
		q = q.Where("group_code = ?", group)
	}
	if zone != "" {
		q = q.Where("zone_code = ?", zone)
	}
	var list []model.ZoneAssignment
	if err := q.Order("namespace_code, group_code, zone_code, server_id").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// SoftDelete 软删某 serverId 的归属；返回是否命中。
func (r *ZoneAssignmentRepository) SoftDelete(ns, serverID string, deletedAt time.Time) (bool, error) {
	res := r.db.Model(&model.ZoneAssignment{}).
		Where("namespace_code = ? AND server_id = ? AND deleted_at = ?", ns, serverID, model.SoftDeleteSentinel).
		Update("deleted_at", deletedAt)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
