package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"beacon/internal/model"
)

// OverrideSetFilter 是覆盖集列表查询的可选过滤条件。
type OverrideSetFilter struct {
	Namespace  string
	Group      string
	ScopeLevel string
}

// FileOverrideSetRepository 提供 file_override_set 表的数据访问（FR-15）。
type FileOverrideSetRepository struct {
	db *gorm.DB
}

// NewFileOverrideSetRepository 构造仓库。
func NewFileOverrideSetRepository(db *gorm.DB) *FileOverrideSetRepository {
	return &FileOverrideSetRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *FileOverrideSetRepository) WithTx(tx *gorm.DB) *FileOverrideSetRepository {
	return &FileOverrideSetRepository{db: tx}
}

// active 返回仅含未软删记录的查询（deleted_at = 哨兵）。
func (r *FileOverrideSetRepository) active() *gorm.DB {
	return r.db.Where("deleted_at = ?", model.SoftDeleteSentinel)
}

// Create 插入一个覆盖集。
func (r *FileOverrideSetRepository) Create(s *model.FileOverrideSet) error {
	return r.db.Create(s).Error
}

// Save 全量保存覆盖集（发布更新 target_root/reload_command/version 等）。
func (r *FileOverrideSetRepository) Save(s *model.FileOverrideSet) error {
	return r.db.Save(s).Error
}

// FindByID 按主键查找未软删项；不存在返回 (nil, nil)。
func (r *FileOverrideSetRepository) FindByID(id uint) (*model.FileOverrideSet, error) {
	var s model.FileOverrideSet
	err := r.active().Where("id = ?", id).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// FindByIdentity 按标识（namespace/group/name/scope_level/scope_target）查找未软删项；不存在返回 (nil, nil)。
func (r *FileOverrideSetRepository) FindByIdentity(ns, group, name, scopeLevel, scopeTarget string) (*model.FileOverrideSet, error) {
	var s model.FileOverrideSet
	err := r.active().Where(
		"namespace_code = ? AND group_code = ? AND name = ? AND scope_level = ? AND scope_target = ?",
		ns, group, name, scopeLevel, scopeTarget,
	).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// List 按可选条件列出未软删覆盖集。
func (r *FileOverrideSetRepository) List(f OverrideSetFilter) ([]model.FileOverrideSet, error) {
	q := r.active()
	if f.Namespace != "" {
		q = q.Where("namespace_code = ?", f.Namespace)
	}
	if f.Group != "" {
		q = q.Where("group_code = ?", f.Group)
	}
	if f.ScopeLevel != "" {
		q = q.Where("scope_level = ?", f.ScopeLevel)
	}
	var sets []model.FileOverrideSet
	if err := q.Order("namespace_code, group_code, name, scope_level, scope_target").Find(&sets).Error; err != nil {
		return nil, err
	}
	return sets, nil
}

// SoftDelete 软删覆盖集：填真实删除时间并置 enabled=false。
func (r *FileOverrideSetRepository) SoftDelete(id uint, deletedAt time.Time) error {
	return r.db.Model(&model.FileOverrideSet{}).
		Where("id = ? AND deleted_at = ?", id, model.SoftDeleteSentinel).
		Updates(map[string]any{"deleted_at": deletedAt, "enabled": false}).Error
}
