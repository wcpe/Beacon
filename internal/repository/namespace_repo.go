// Package repository 是数据访问层：各表的纯 GORM CRUD，不含业务规则。
package repository

import (
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
)

// NamespaceRepository 提供 namespace 表的数据访问。
type NamespaceRepository struct {
	db *gorm.DB
}

// NewNamespaceRepository 构造仓库。
func NewNamespaceRepository(db *gorm.DB) *NamespaceRepository {
	return &NamespaceRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *NamespaceRepository) WithTx(tx *gorm.DB) *NamespaceRepository {
	return &NamespaceRepository{db: tx}
}

// List 返回全部环境（按 code 升序）。
func (r *NamespaceRepository) List() ([]model.Namespace, error) {
	var items []model.Namespace
	if err := r.db.Order("code asc").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// Count 返回环境总数（用于预置时的幂等判断）。
func (r *NamespaceRepository) Count() (int64, error) {
	var n int64
	if err := r.db.Model(&model.Namespace{}).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// FindByCode 按 code 查找；不存在返回 (nil, nil)。
func (r *NamespaceRepository) FindByCode(code string) (*model.Namespace, error) {
	var ns model.Namespace
	err := r.db.Where("code = ?", code).First(&ns).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ns, nil
}

// Create 插入一个环境。
func (r *NamespaceRepository) Create(ns *model.Namespace) error {
	return r.db.Create(ns).Error
}

// UpdateName 按 code 更新环境显示名；返回命中行数（0 表示该 code 不存在）。
func (r *NamespaceRepository) UpdateName(code, name string) (int64, error) {
	res := r.db.Model(&model.Namespace{}).Where("code = ?", code).Update("name", name)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// DeleteByCode 按 code 硬删环境；返回命中行数（0 表示该 code 不存在）。
// namespace 表无软删需求（删除仅在环境内已无在用数据时放行），故直接硬删（见 docs/specs/namespace-crud.md §3）。
func (r *NamespaceRepository) DeleteByCode(code string) (int64, error) {
	res := r.db.Where("code = ?", code).Delete(&model.Namespace{})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
