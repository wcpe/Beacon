package model

import "time"

// FileOverrideSetRevision 是覆盖集（FR-15）每次发布的不可变快照（append-only）。
// 记录该版本的目标根 + 重载命令 + 成员清单快照（path 列表），回滚即读取目标版本作为新版本发布。
// 成员清单以 TEXT 存（逗号或换行分隔的 path 列表），保 GORM 可移植（禁 JSON 列）。
// 与 ConfigRevision/FileRevision 同款 source_revision 思路。
type FileOverrideSetRevision struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 关联 file_override_set.id
	OverrideSetID uint `gorm:"column:override_set_id;not null;uniqueIndex:uk_override_rev_version,priority:1;index:idx_override_rev_set"`
	// 本次发布版本号（与 file_override_set.version 对齐）
	Version int64 `gorm:"column:version;not null;uniqueIndex:uk_override_rev_version,priority:2"`
	// 该版本目标根目录快照
	TargetRoot string `gorm:"column:target_root;size:512;not null"`
	// 该版本重载命令快照
	ReloadCommand string `gorm:"column:reload_command;size:512;not null;default:''"`
	// 该版本成员文件 path 清单快照（换行分隔，落 TEXT）
	MemberPaths string `gorm:"column:member_paths;type:text"`
	// 回滚来源 revision id；正常发布为 NULL
	SourceRevision *uint `gorm:"column:source_revision"`
	// 发布说明
	Comment string `gorm:"column:comment;size:512"`
	// 发布人
	Operator string `gorm:"column:operator;size:128;not null"`
	// 创建时间（UTC）
	CreatedAt time.Time
}

// TableName 固定表名为 file_override_set_revision。
func (FileOverrideSetRevision) TableName() string {
	return "file_override_set_revision"
}
