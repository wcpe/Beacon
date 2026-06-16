package model

import "time"

// FileRevision 是文件树托管（通道B）每次发布的不可变快照（append-only）。
// 回滚 = 读取目标版本内容作为新版本发布，并以 source_revision 记录来源（与 ConfigRevision 同款思路）。
type FileRevision struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 关联 file_object.id
	FileObjectID uint `gorm:"column:file_object_id;not null;uniqueIndex:uk_file_revision_version,priority:1;index:idx_file_revision_object"`
	// 本次发布版本号（与 file_object.version 对齐）
	Version int64 `gorm:"column:version;not null;uniqueIndex:uk_file_revision_version,priority:2"`
	// 该版本完整内容快照（不可变，整文件文本）
	Content string `gorm:"column:content;size:1048576;not null"`
	// 内容 md5（小写 hex）
	ContentMD5 string `gorm:"column:content_md5;size:32;not null"`
	// 回滚来源 revision id；正常发布为 NULL
	SourceRevision *uint `gorm:"column:source_revision"`
	// 发布说明
	Comment string `gorm:"column:comment;size:512"`
	// 发布人
	Operator string `gorm:"column:operator;size:128;not null"`
	// 创建时间（UTC）
	CreatedAt time.Time
}

// TableName 固定表名为 file_revision。
func (FileRevision) TableName() string { return "file_revision" }
