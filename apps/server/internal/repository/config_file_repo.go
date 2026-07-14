package repository

import (
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// ConfigFileFilter 是配置文件列表查询过滤（FR-160，spec §5）：namespace 必带 + 名称关键字 + 分页。
type ConfigFileFilter struct {
	NamespaceID uint
	Keyword     string
	Page        int
	PageSize    int
}

// ConfigFileRepository 提供配置中心 V2 config_file 表的数据访问。
// name 唯一性由应用层校验且只对未删除（deleted_at IS NULL）文件生效（spec §3.2/§4.9）。
type ConfigFileRepository struct {
	db *gorm.DB
}

// NewConfigFileRepository 构造仓库。
func NewConfigFileRepository(db *gorm.DB) *ConfigFileRepository {
	return &ConfigFileRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本（供 service 在外层事务内复用）。
func (r *ConfigFileRepository) WithTx(tx *gorm.DB) *ConfigFileRepository {
	return &ConfigFileRepository{db: tx}
}

// Create 插入一条配置文件。
func (r *ConfigFileRepository) Create(file *model.ConfigFile) error {
	return r.db.Create(file).Error
}

// Save 全量保存配置文件（元数据更新 / 软删标记 / 恢复）。
func (r *ConfigFileRepository) Save(file *model.ConfigFile) error {
	return r.db.Save(file).Error
}

// FindByID 按 id 取配置文件；includeTrashed=false 时回收站内文件视同不存在（spec §4.9）。
// 不存在返回 (nil, nil)。
func (r *ConfigFileRepository) FindByID(id uint, includeTrashed bool) (*model.ConfigFile, error) {
	var file model.ConfigFile
	err := r.db.First(&file, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if file.DeletedAt != nil && !includeTrashed {
		return nil, nil
	}
	return &file, nil
}

// ActiveNameExists 判断 namespace 内该逻辑名是否已被未删除文件占用（excludeID 排除自身，0 = 不排除）。
func (r *ConfigFileRepository) ActiveNameExists(namespaceID uint, name string, excludeID uint) (bool, error) {
	q := r.db.Model(&model.ConfigFile{}).
		Where("namespace_id = ? AND name = ? AND deleted_at IS NULL", namespaceID, name)
	if excludeID != 0 {
		q = q.Where("id <> ?", excludeID)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ListActive 分页列出未删除文件（按名称升序，spec §5 列表端点）。
func (r *ConfigFileRepository) ListActive(f ConfigFileFilter) ([]model.ConfigFile, int64, error) {
	return r.list(f, false)
}

// ListActiveAll 不分页列出 namespace 内全部未删除文件（serverId 过滤路径须先全量算生效贡献再分页）。
func (r *ConfigFileRepository) ListActiveAll(namespaceID uint, keyword string) ([]model.ConfigFile, error) {
	q := r.db.Where("namespace_id = ? AND deleted_at IS NULL", namespaceID)
	q = applyKeyword(q, keyword)
	var files []model.ConfigFile
	err := q.Order("name ASC").Find(&files).Error
	return files, err
}

// ListTrash 分页列出回收站内文件（按删除时间倒序，spec §4.9）。
func (r *ConfigFileRepository) ListTrash(f ConfigFileFilter) ([]model.ConfigFile, int64, error) {
	return r.list(f, true)
}

// list 是常规 / 回收站列表的共用实现。
func (r *ConfigFileRepository) list(f ConfigFileFilter, trashed bool) ([]model.ConfigFile, int64, error) {
	q := r.db.Model(&model.ConfigFile{}).Where("namespace_id = ?", f.NamespaceID)
	if trashed {
		q = q.Where("deleted_at IS NOT NULL")
	} else {
		q = q.Where("deleted_at IS NULL")
	}
	q = applyKeyword(q, f.Keyword)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page := f.Page
	if page < 1 {
		page = 1
	}
	size := f.PageSize
	if size < 1 {
		size = 20
	}
	order := "name ASC"
	if trashed {
		order = "deleted_at DESC"
	}
	var files []model.ConfigFile
	if err := q.Order(order).Offset((page - 1) * size).Limit(size).Find(&files).Error; err != nil {
		return nil, 0, err
	}
	return files, total, nil
}

// applyKeyword 追加名称关键字过滤（大小写不敏感的包含匹配，标准 SQL LOWER + LIKE 保持可移植）。
func applyKeyword(q *gorm.DB, keyword string) *gorm.DB {
	if keyword == "" {
		return q
	}
	return q.Where("LOWER(name) LIKE ?", "%"+strings.ToLower(keyword)+"%")
}

// DeleteByID 物理删除配置文件行（仅 purge 使用，须在事务内连带删除版本链，spec §4.9）。
func (r *ConfigFileRepository) DeleteByID(id uint) error {
	return r.db.Delete(&model.ConfigFile{}, id).Error
}
