package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"beacon/internal/model"
)

// FileFilter 是文件对象列表查询的可选过滤条件。
type FileFilter struct {
	Namespace  string
	Group      string
	Path       string
	ScopeLevel string
}

// FileObjectRepository 提供 file_object 表的数据访问（文件树托管通道B）。
type FileObjectRepository struct {
	db *gorm.DB
}

// NewFileObjectRepository 构造仓库。
func NewFileObjectRepository(db *gorm.DB) *FileObjectRepository {
	return &FileObjectRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本（供 service 在事务内复用）。
func (r *FileObjectRepository) WithTx(tx *gorm.DB) *FileObjectRepository {
	return &FileObjectRepository{db: tx}
}

// active 返回仅含未软删记录的查询（deleted_at = 哨兵）。
func (r *FileObjectRepository) active() *gorm.DB {
	return r.db.Where("deleted_at = ?", model.SoftDeleteSentinel)
}

// Create 插入一个文件对象。
func (r *FileObjectRepository) Create(obj *model.FileObject) error {
	return r.db.Create(obj).Error
}

// Save 全量保存文件对象（发布更新 content/md5/current_revision/version 等）。
func (r *FileObjectRepository) Save(obj *model.FileObject) error {
	return r.db.Save(obj).Error
}

// FindByID 按主键查找未软删项；不存在返回 (nil, nil)。
func (r *FileObjectRepository) FindByID(id uint) (*model.FileObject, error) {
	var obj model.FileObject
	err := r.active().Where("id = ?", id).First(&obj).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &obj, nil
}

// FindByIdentity 按标识五元组查找未软删项；不存在返回 (nil, nil)。
func (r *FileObjectRepository) FindByIdentity(ns, group, path, scopeLevel, scopeTarget string) (*model.FileObject, error) {
	var obj model.FileObject
	err := r.active().Where(
		"namespace_code = ? AND group_code = ? AND path = ? AND scope_level = ? AND scope_target = ?",
		ns, group, path, scopeLevel, scopeTarget,
	).First(&obj).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &obj, nil
}

// List 按可选条件列出未软删文件对象。
func (r *FileObjectRepository) List(f FileFilter) ([]model.FileObject, error) {
	q := r.active()
	if f.Namespace != "" {
		q = q.Where("namespace_code = ?", f.Namespace)
	}
	if f.Group != "" {
		q = q.Where("group_code = ?", f.Group)
	}
	if f.Path != "" {
		q = q.Where("path = ?", f.Path)
	}
	if f.ScopeLevel != "" {
		q = q.Where("scope_level = ?", f.ScopeLevel)
	}
	var objs []model.FileObject
	if err := q.Order("namespace_code, group_code, path, scope_level, scope_target").Find(&objs).Error; err != nil {
		return nil, err
	}
	return objs, nil
}

// ListByOverrideSet 列出某覆盖集（FR-15）的全部未软删成员文件，按 path 字典序稳定排序。
func (r *FileObjectRepository) ListByOverrideSet(setID uint) ([]model.FileObject, error) {
	var objs []model.FileObject
	if err := r.active().Where("override_set_id = ?", setID).Order("path").Find(&objs).Error; err != nil {
		return nil, err
	}
	return objs, nil
}

// SoftDelete 软删文件对象：填真实删除时间并置 enabled=false。
func (r *FileObjectRepository) SoftDelete(id uint, deletedAt time.Time) error {
	return r.db.Model(&model.FileObject{}).
		Where("id = ? AND deleted_at = ?", id, model.SoftDeleteSentinel).
		Updates(map[string]any{"deleted_at": deletedAt, "enabled": false}).Error
}

// FindEffectiveCandidates 拉取某 agent 身份的四层候选文件（已 enabled 且未软删）。
// 一条查询拉全 global/group/zone/server 四层，由上层按 path 解析整文件覆盖。
func (r *FileObjectRepository) FindEffectiveCandidates(ns, group, zone, serverID string) ([]model.FileObject, error) {
	levelCond := r.db.
		Where("scope_level = ? AND group_code = ?", model.ScopeGlobal, model.GlobalGroupCode).
		Or("scope_level = ? AND group_code = ?", model.ScopeGroup, group).
		Or("scope_level = ? AND group_code = ? AND scope_target = ?", model.ScopeZone, group, zone).
		Or("scope_level = ? AND group_code = ? AND scope_target = ?", model.ScopeServer, group, serverID)

	var objs []model.FileObject
	err := r.db.
		Where("deleted_at = ?", model.SoftDeleteSentinel).
		Where("enabled = ?", true).
		Where("namespace_code = ?", ns).
		Where(levelCond).
		Find(&objs).Error
	if err != nil {
		return nil, err
	}
	return objs, nil
}
