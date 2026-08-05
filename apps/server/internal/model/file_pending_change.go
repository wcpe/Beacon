package model

import "time"

const (
	// FilePendingChangePending 表示文件或覆盖集变更仍等待审批执行。
	FilePendingChangePending = "pending"
	// FilePendingChangeApplied 表示变更已在审批执行事务中应用。
	FilePendingChangeApplied = "applied"
	// FilePendingChangeInvalidated 表示拒绝、撤回或过期后不可再应用。
	FilePendingChangeInvalidated = "invalidated"
)

// FilePendingChange 保存审批前冻结的文件或覆盖集变更；内容和命令参数仅保存为密文。
type FilePendingChange struct {
	ID                uint   `gorm:"primaryKey;autoIncrement"`
	ChangeID          string `gorm:"column:change_id;size:96;uniqueIndex;not null"`
	ApprovalRequestID string `gorm:"column:approval_request_id;size:96;uniqueIndex;not null"`
	TargetType        string `gorm:"column:target_type;size:32;not null;index"`
	TargetID          uint   `gorm:"column:target_id;not null;index"`
	ChangeType        string `gorm:"column:change_type;size:32;not null"`
	ExpectedVersion   int64  `gorm:"column:expected_version;not null"`
	ContentSHA256     string `gorm:"column:content_sha256;size:64;not null"`
	Ciphertext        string `gorm:"column:ciphertext;type:text;not null"`
	Status            string `gorm:"column:status;size:32;not null;index"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// TableName 固定待审批文件变更表名。
func (FilePendingChange) TableName() string { return "file_pending_change" }
