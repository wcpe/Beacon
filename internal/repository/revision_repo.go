package repository

import (
	"errors"

	"gorm.io/gorm"

	"beacon/internal/model"
)

// ConfigRevisionRepository 提供 config_revision 表的数据访问（append-only）。
type ConfigRevisionRepository struct {
	db *gorm.DB
}

// NewConfigRevisionRepository 构造仓库。
func NewConfigRevisionRepository(db *gorm.DB) *ConfigRevisionRepository {
	return &ConfigRevisionRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *ConfigRevisionRepository) WithTx(tx *gorm.DB) *ConfigRevisionRepository {
	return &ConfigRevisionRepository{db: tx}
}

// Create 追加一条版本快照。
func (r *ConfigRevisionRepository) Create(rev *model.ConfigRevision) error {
	return r.db.Create(rev).Error
}

// ListByItem 按版本升序列出某配置项的全部历史版本。
func (r *ConfigRevisionRepository) ListByItem(itemID uint) ([]model.ConfigRevision, error) {
	var revs []model.ConfigRevision
	if err := r.db.Where("config_item_id = ?", itemID).Order("version asc").Find(&revs).Error; err != nil {
		return nil, err
	}
	return revs, nil
}

// FindByItemAndVersion 取某配置项的指定版本；不存在返回 (nil, nil)。
func (r *ConfigRevisionRepository) FindByItemAndVersion(itemID uint, version int64) (*model.ConfigRevision, error) {
	var rev model.ConfigRevision
	err := r.db.Where("config_item_id = ? AND version = ?", itemID, version).First(&rev).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rev, nil
}
