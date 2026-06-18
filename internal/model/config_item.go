package model

import (
	"time"

	"gorm.io/gorm"
)

// ConfigItem 是一条"可独立发布的配置对象"：三元组 + scope 维度定位它在覆盖链的一格，
// content/content_md5 冗余在行上供热路径直读，current_revision/version 为当前版本指针。
type ConfigItem struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 环境编码（三元组 environment）
	NamespaceCode string `gorm:"column:namespace_code;size:64;not null;uniqueIndex:uk_config_identity,priority:1;index:idx_config_lookup,priority:1;index:idx_config_scope,priority:1"`
	// 大区编码（global 层用占位 __GLOBAL__）
	GroupCode string `gorm:"column:group_code;size:64;not null;uniqueIndex:uk_config_identity,priority:2;index:idx_config_lookup,priority:2;index:idx_config_scope,priority:2"`
	// 配置名（如 mysql.yml）
	DataID string `gorm:"column:data_id;size:128;not null;uniqueIndex:uk_config_identity,priority:3;index:idx_config_lookup,priority:3"`
	// 覆盖层：global/group/zone/server
	ScopeLevel string `gorm:"column:scope_level;size:16;not null;uniqueIndex:uk_config_identity,priority:4;index:idx_config_scope,priority:3"`
	// 该层目标键：global/group='' ；zone=zone编码；server=serverId
	ScopeTarget string `gorm:"column:scope_target;size:128;not null;default:'';uniqueIndex:uk_config_identity,priority:5;index:idx_config_scope,priority:4"`
	// 内容格式：yaml/properties/json
	Format string `gorm:"column:format;size:16;not null;default:'yaml'"`
	// 当前生效内容（=current_revision 内容镜像，便于直接读）
	Content string `gorm:"column:content;size:262144;not null"`
	// 当前内容 md5（小写 hex）
	ContentMD5 string `gorm:"column:content_md5;size:32;not null"`
	// 指向 config_revision.id；0=尚未发布
	CurrentRevision uint `gorm:"column:current_revision;not null;default:0"`
	// 单调递增发布序号（每发布 +1，回滚也 +1）
	Version int64 `gorm:"column:version;not null;default:0"`
	// 是否参与有效配置合并
	Enabled bool `gorm:"column:enabled;not null;default:true"`
	// 是否敏感项：为真则 content 加密入库（at-rest），读取/下发时在控制面解密（FR-20）
	Sensitive bool `gorm:"column:sensitive;not null;default:false"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）
	UpdatedAt time.Time
	// 软删时间；未删为哨兵值（见 SoftDeleteSentinel），纳入唯一键允许同标识重建
	DeletedAt time.Time `gorm:"column:deleted_at;not null;uniqueIndex:uk_config_identity,priority:6"`
}

// TableName 固定表名为 config_item。
func (ConfigItem) TableName() string { return "config_item" }

// BeforeCreate 在插入前为未删记录写入软删哨兵值（非 NULL，使唯一键生效）。
func (c *ConfigItem) BeforeCreate(*gorm.DB) error {
	if c.DeletedAt.IsZero() {
		c.DeletedAt = SoftDeleteSentinel
	}
	return nil
}
