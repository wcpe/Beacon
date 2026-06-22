package repository

import (
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/gitexport"
	"github.com/wcpe/Beacon/internal/model"
)

// ExportSourceRepository 为 git 单向导出（FR-47，见 ADR-0030）提供源层只读读取。
//
// 关键：它**直读 config_item / file_object 原始行、不经 ConfigItemRepository 的解密**——
// 敏感配置项库内 content 即 enc:v1: 密文，原样读出正是导出要的密文（解密反而泄明文，违 ADR-0030 决策4）。
// 这是唯一新增的 DB 读路径，只读、不写、与发布事务无关（导出在事务提交后异步跑）。
type ExportSourceRepository struct {
	db *gorm.DB
}

// NewExportSourceRepository 构造导出源只读仓库。
func NewExportSourceRepository(db *gorm.DB) *ExportSourceRepository {
	return &ExportSourceRepository{db: db}
}

// active 返回仅含未软删记录的查询（deleted_at = 哨兵）。
func (r *ExportSourceRepository) active() *gorm.DB {
	return r.db.Where("deleted_at = ?", model.SoftDeleteSentinel)
}

// LoadSourceLayers 读取全量源层（配置 + 文件树各覆盖层）组装为待导出列表。
// 只取未软删、enabled=true 的行；文件仅取通用托管文件（override_set_id=0，排除覆盖集成员，见 ADR-0030 范围）。
// 敏感配置项 content 原样（密文）、文件 SensitiveExcluded 透传，敏感处理交 gitexport.BuildSnapshot 纯逻辑。
func (r *ExportSourceRepository) LoadSourceLayers() ([]gitexport.SourceLayer, error) {
	var items []model.ConfigItem
	if err := r.active().Where("enabled = ?", true).
		Order("namespace_code, group_code, data_id, scope_level, scope_target").
		Find(&items).Error; err != nil {
		return nil, err
	}

	var files []model.FileObject
	if err := r.active().Where("enabled = ?", true).Where("override_set_id = ?", 0).
		Order("namespace_code, group_code, path, scope_level, scope_target").
		Find(&files).Error; err != nil {
		return nil, err
	}

	layers := make([]gitexport.SourceLayer, 0, len(items)+len(files))
	for _, it := range items {
		// 注意：此处 it.Content 对敏感项为 enc:v1: 密文（未经 decrypt），正是导出所需，绝不解密。
		layers = append(layers, gitexport.SourceLayer{
			Kind:        gitexport.KindConfig,
			Namespace:   it.NamespaceCode,
			Group:       it.GroupCode,
			ScopeLevel:  it.ScopeLevel,
			ScopeTarget: it.ScopeTarget,
			Name:        it.DataID,
			Content:     it.Content,
		})
	}
	for _, f := range files {
		layers = append(layers, gitexport.SourceLayer{
			Kind:        gitexport.KindFile,
			Namespace:   f.NamespaceCode,
			Group:       f.GroupCode,
			ScopeLevel:  f.ScopeLevel,
			ScopeTarget: f.ScopeTarget,
			Name:        f.Path,
			Content:     f.Content,
			Excluded:    f.SensitiveExcluded, // path 级敏感排除：BuildSnapshot 据此剔除
		})
	}
	return layers, nil
}
