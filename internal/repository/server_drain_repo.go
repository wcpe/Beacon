package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
)

// ServerDrainRepository 提供 server_drain 表的数据访问（FR-10，见 ADR-0017）。
type ServerDrainRepository struct {
	db *gorm.DB
}

// NewServerDrainRepository 构造仓库。
func NewServerDrainRepository(db *gorm.DB) *ServerDrainRepository {
	return &ServerDrainRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *ServerDrainRepository) WithTx(tx *gorm.DB) *ServerDrainRepository {
	return &ServerDrainRepository{db: tx}
}

// FindByServer 查某 serverId 在某环境的未软删 drain 标记；未 drain 返回 (nil, nil)。
func (r *ServerDrainRepository) FindByServer(ns, serverID string) (*model.ServerDrain, error) {
	var d model.ServerDrain
	err := r.db.Where("namespace_code = ? AND server_id = ? AND deleted_at = ?",
		ns, serverID, model.SoftDeleteSentinel).First(&d).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// Upsert 标记 drain（已存在则更新 reason，幂等）。
func (r *ServerDrainRepository) Upsert(ns, serverID, reason string) (*model.ServerDrain, error) {
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
	d := &model.ServerDrain{NamespaceCode: ns, ServerID: serverID, Reason: reason}
	if err := r.db.Create(d).Error; err != nil {
		return nil, err
	}
	return d, nil
}

// ListActive 列出某环境内未软删的 drain 标记（ns 为空则全量）。
func (r *ServerDrainRepository) ListActive(ns string) ([]model.ServerDrain, error) {
	q := r.db.Where("deleted_at = ?", model.SoftDeleteSentinel)
	if ns != "" {
		q = q.Where("namespace_code = ?", ns)
	}
	var list []model.ServerDrain
	if err := q.Order("namespace_code, server_id").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// SoftDelete 取消某 serverId 的 drain（软删）；返回是否命中。
func (r *ServerDrainRepository) SoftDelete(ns, serverID string, deletedAt time.Time) (bool, error) {
	res := r.db.Model(&model.ServerDrain{}).
		Where("namespace_code = ? AND server_id = ? AND deleted_at = ?", ns, serverID, model.SoftDeleteSentinel).
		Update("deleted_at", deletedAt)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
