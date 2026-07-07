package model

import "time"

// ConfigRevision 是配置每次发布的不可变快照（append-only）。
// 回滚 = 读取目标版本内容作为新版本发布，并以 source_revision 记录来源。
type ConfigRevision struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 关联 config_item.id
	ConfigItemID uint `gorm:"column:config_item_id;not null;uniqueIndex:uk_revision_version,priority:1;index:idx_revision_item"`
	// 本次发布版本号（与 config_item.version 对齐）
	Version int64 `gorm:"column:version;not null;uniqueIndex:uk_revision_version,priority:2"`
	// 内容格式
	Format string `gorm:"column:format;size:16;not null"`
	// 该版本完整内容快照（不可变）
	Content string `gorm:"column:content;size:262144;not null"`
	// 内容 md5（小写 hex）
	ContentMD5 string `gorm:"column:content_md5;size:32;not null"`
	// 是否敏感项：与 config_item.sensitive 镜像，为真则该版本快照 content 加密入库（FR-20）
	Sensitive bool `gorm:"column:sensitive;not null;default:false"`
	// 回滚来源 revision id；正常发布为 NULL
	SourceRevision *uint `gorm:"column:source_revision"`
	// 发布说明
	Comment string `gorm:"column:comment;size:512"`
	// 发布人
	Operator string `gorm:"column:operator;size:128;not null"`
	// 创建时间（UTC）
	CreatedAt time.Time
}

// TableName 固定表名为 config_revision。
func (ConfigRevision) TableName() string { return "config_revision" }
