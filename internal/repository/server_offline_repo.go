package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
)

// ServerOfflineRepository 提供 server_offline 表的数据访问（FR-49，主动下线拒绝态）。
type ServerOfflineRepository struct {
	db *gorm.DB
}

// NewServerOfflineRepository 构造仓库。
func NewServerOfflineRepository(db *gorm.DB) *ServerOfflineRepository {
	return &ServerOfflineRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *ServerOfflineRepository) WithTx(tx *gorm.DB) *ServerOfflineRepository {
	return &ServerOfflineRepository{db: tx}
}

// FindByServer 查某 serverId 在某环境的未软删下线标记；未下线返回 (nil, nil)。
func (r *ServerOfflineRepository) FindByServer(ns, serverID string) (*model.ServerOffline, error) {
	var o model.ServerOffline
	err := r.db.Where("namespace_code = ? AND server_id = ? AND deleted_at = ?",
		ns, serverID, model.SoftDeleteSentinel).First(&o).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// Upsert 标记下线（已存在则更新 reason，幂等）。
func (r *ServerOfflineRepository) Upsert(ns, serverID, reason string) (*model.ServerOffline, error) {
	existing, err := r.FindByServer(ns, serverID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		existing.Reason = reason
		if err := r.db.Save(existing).Error; err != nil {
			return nil, err
		}
		return existing, nil
	}
	o := &model.ServerOffline{NamespaceCode: ns, ServerID: serverID, Reason: reason}
	if err := r.db.Create(o).Error; err != nil {
		return nil, err
	}
	return o, nil
}

// ListActive 列出某环境内未软删的下线标记（ns 为空则全量）。
func (r *ServerOfflineRepository) ListActive(ns string) ([]model.ServerOffline, error) {
	q := r.db.Where("deleted_at = ?", model.SoftDeleteSentinel)
	if ns != "" {
		q = q.Where("namespace_code = ?", ns)
	}
	var list []model.ServerOffline
	if err := q.Order("namespace_code, server_id").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// SoftDelete 取消某 serverId 的下线（软删）；返回是否命中。
func (r *ServerOfflineRepository) SoftDelete(ns, serverID string, deletedAt time.Time) (bool, error) {
	res := r.db.Model(&model.ServerOffline{}).
		Where("namespace_code = ? AND server_id = ? AND deleted_at = ?", ns, serverID, model.SoftDeleteSentinel).
		Update("deleted_at", deletedAt)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
