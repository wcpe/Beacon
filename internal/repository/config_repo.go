package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"beacon/internal/model"
)

// ConfigFilter 是配置项列表查询的可选过滤条件。
type ConfigFilter struct {
	Namespace  string
	Group      string
	DataID     string
	ScopeLevel string
}

// ConfigItemRepository 提供 config_item 表的数据访问。
type ConfigItemRepository struct {
	db *gorm.DB
}

// NewConfigItemRepository 构造仓库。
func NewConfigItemRepository(db *gorm.DB) *ConfigItemRepository {
	return &ConfigItemRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本（供 service 在事务内复用）。
func (r *ConfigItemRepository) WithTx(tx *gorm.DB) *ConfigItemRepository {
	return &ConfigItemRepository{db: tx}
}

// active 返回仅含未软删记录的查询（deleted_at = 哨兵）。
func (r *ConfigItemRepository) active() *gorm.DB {
	return r.db.Where("deleted_at = ?", model.SoftDeleteSentinel)
}

// Create 插入一个配置项。
func (r *ConfigItemRepository) Create(item *model.ConfigItem) error {
	return r.db.Create(item).Error
}

// Save 全量保存配置项（发布更新 content/md5/current_revision/version 等）。
func (r *ConfigItemRepository) Save(item *model.ConfigItem) error {
	return r.db.Save(item).Error
}

// FindByID 按主键查找未软删项；不存在返回 (nil, nil)。
func (r *ConfigItemRepository) FindByID(id uint) (*model.ConfigItem, error) {
	var item model.ConfigItem
	err := r.active().Where("id = ?", id).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// FindByIdentity 按标识五元组查找未软删项；不存在返回 (nil, nil)。
func (r *ConfigItemRepository) FindByIdentity(ns, group, dataID, scopeLevel, scopeTarget string) (*model.ConfigItem, error) {
	var item model.ConfigItem
	err := r.active().Where(
		"namespace_code = ? AND group_code = ? AND data_id = ? AND scope_level = ? AND scope_target = ?",
		ns, group, dataID, scopeLevel, scopeTarget,
	).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// FindFormatByDataID 取同一 (ns, dataId) 下任一未软删项的格式（跨层格式一致性校验）。
// 同一 dataId 在全网各层必须同格式（含 __GLOBAL__ 层），故按 ns+dataId 而非按 group 查。
// 不存在返回空串。
func (r *ConfigItemRepository) FindFormatByDataID(ns, dataID string) (string, error) {
	var item model.ConfigItem
	err := r.active().Where("namespace_code = ? AND data_id = ?", ns, dataID).
		First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return item.Format, nil
}

// List 按可选条件列出未软删配置项。
func (r *ConfigItemRepository) List(f ConfigFilter) ([]model.ConfigItem, error) {
	q := r.active()
	if f.Namespace != "" {
		q = q.Where("namespace_code = ?", f.Namespace)
	}
	if f.Group != "" {
		q = q.Where("group_code = ?", f.Group)
	}
	if f.DataID != "" {
		q = q.Where("data_id = ?", f.DataID)
	}
	if f.ScopeLevel != "" {
		q = q.Where("scope_level = ?", f.ScopeLevel)
	}
	var items []model.ConfigItem
	if err := q.Order("namespace_code, group_code, data_id, scope_level, scope_target").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// SoftDelete 软删配置项：填真实删除时间并置 enabled=false。
func (r *ConfigItemRepository) SoftDelete(id uint, deletedAt time.Time) error {
	return r.db.Model(&model.ConfigItem{}).
		Where("id = ? AND deleted_at = ?", id, model.SoftDeleteSentinel).
		Updates(map[string]any{"deleted_at": deletedAt, "enabled": false}).Error
}

// FindEffectiveCandidates 拉取某 agent 身份的四层候选配置（已 enabled 且未软删）。
// 一条查询拉全 global/group/zone/server 四层，由上层按 dataId 分桶合并。
func (r *ConfigItemRepository) FindEffectiveCandidates(ns, group, zone, serverID string) ([]model.ConfigItem, error) {
	levelCond := r.db.
		Where("scope_level = ? AND group_code = ?", model.ScopeGlobal, model.GlobalGroupCode).
		Or("scope_level = ? AND group_code = ?", model.ScopeGroup, group).
		Or("scope_level = ? AND group_code = ? AND scope_target = ?", model.ScopeZone, group, zone).
		Or("scope_level = ? AND group_code = ? AND scope_target = ?", model.ScopeServer, group, serverID)

	var items []model.ConfigItem
	err := r.db.
		Where("deleted_at = ?", model.SoftDeleteSentinel).
		Where("enabled = ?", true).
		Where("namespace_code = ?", ns).
		Where(levelCond).
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}
