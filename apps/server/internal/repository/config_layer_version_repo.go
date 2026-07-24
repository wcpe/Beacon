package repository

import (
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// ConfigLayerVersionRepository 提供配置中心 V2 层版本不可变链的数据访问（spec §3.3）。
// 本表只 INSERT，永不 UPDATE / DELETE 单行——仓库刻意不提供更新接口；
// 唯一例外是文件 purge 时按 config_file_id 连带物理删除整链（spec §4.9）。
type ConfigLayerVersionRepository struct {
	db *gorm.DB
}

// NewConfigLayerVersionRepository 构造仓库。
func NewConfigLayerVersionRepository(db *gorm.DB) *ConfigLayerVersionRepository {
	return &ConfigLayerVersionRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本（供 service 在外层事务内复用）。
func (r *ConfigLayerVersionRepository) WithTx(tx *gorm.DB) *ConfigLayerVersionRepository {
	return &ConfigLayerVersionRepository{db: tx}
}

// Insert 追加一条版本；链内 (config_file_id, scope_level, scope_ref_id, version_no) 唯一索引
// 兜底并发插入（冲突经 TranslateError 转为 gorm.ErrDuplicatedKey，由 service 映射 409）。
func (r *ConfigLayerVersionRepository) Insert(v *model.ConfigLayerVersion) error {
	return r.db.Create(v).Error
}

// FindByID 按 id 取版本；不存在返回 (nil, nil)。
func (r *ConfigLayerVersionRepository) FindByID(id uint) (*model.ConfigLayerVersion, error) {
	var v model.ConfigLayerVersion
	err := r.db.First(&v, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// Head 取某链当前头（version_no 最大的一行）；链不存在返回 (nil, nil)。
func (r *ConfigLayerVersionRepository) Head(fileID uint, scopeLevel string, scopeRefID uint) (*model.ConfigLayerVersion, error) {
	var v model.ConfigLayerVersion
	err := r.db.Where("config_file_id = ? AND scope_level = ? AND scope_ref_id = ?", fileID, scopeLevel, scopeRefID).
		Order("version_no DESC").First(&v).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// HeadsByFileIDs 一次查出多个文件全部链的 head（NOT EXISTS 反连接，标准 SQL 保持可移植），
// 供列表 / 概览 / 有效解析批量取头，禁循环内逐链查库（N+1）。
func (r *ConfigLayerVersionRepository) HeadsByFileIDs(fileIDs []uint) ([]model.ConfigLayerVersion, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	var heads []model.ConfigLayerVersion
	err := r.db.Table("config_layer_version AS v").
		Where("v.config_file_id IN ?", fileIDs).
		Where("NOT EXISTS (SELECT 1 FROM config_layer_version v2 WHERE v2.config_file_id = v.config_file_id" +
			" AND v2.scope_level = v.scope_level AND v2.scope_ref_id = v.scope_ref_id AND v2.version_no > v.version_no)").
		Find(&heads).Error
	return heads, err
}

// HeadsByFile 一次查出单个文件全部链的 head。
func (r *ConfigLayerVersionRepository) HeadsByFile(fileID uint) ([]model.ConfigLayerVersion, error) {
	return r.HeadsByFileIDs([]uint{fileID})
}

// ListChain 分页列出某链版本（新 → 旧，spec §5 版本列表端点）。
func (r *ConfigLayerVersionRepository) ListChain(fileID uint, scopeLevel string, scopeRefID uint, page, pageSize int) ([]model.ConfigLayerVersion, int64, error) {
	q := r.db.Model(&model.ConfigLayerVersion{}).
		Where("config_file_id = ? AND scope_level = ? AND scope_ref_id = ?", fileID, scopeLevel, scopeRefID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	var versions []model.ConfigLayerVersion
	if err := q.Order("version_no DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&versions).Error; err != nil {
		return nil, 0, err
	}
	return versions, total, nil
}

// DeleteByFileID 按文件物理删除全部层版本（仅文件 purge 连带使用，须与删文件同事务，spec §4.9）。
func (r *ConfigLayerVersionRepository) DeleteByFileID(fileID uint) error {
	return r.db.Where("config_file_id = ?", fileID).Delete(&model.ConfigLayerVersion{}).Error
}
