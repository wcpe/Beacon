package model

import "time"

const (
	// ConfigPendingChangePending 表示配置变更仍等待审批执行。
	ConfigPendingChangePending = "pending"
	// ConfigPendingChangeApplied 表示变更已在审批执行事务中应用。
	ConfigPendingChangeApplied = "applied"
	// ConfigPendingChangeInvalidated 表示拒绝、撤回或过期后不可再应用。
	ConfigPendingChangeInvalidated = "invalidated"

	// ConfigPendingChangePublish 是待审批的新版本发布。
	ConfigPendingChangePublish = "publish"
	// ConfigPendingChangeRollback 是待审批的历史版本回滚。
	ConfigPendingChangeRollback     = "rollback"
	ConfigPendingChangeGrayPublish  = "gray_publish"
	ConfigPendingChangeGrayPromote  = "gray_promote"
	ConfigPendingChangeDelete       = "delete"
	ConfigPendingChangeBatchDelete  = "batch_delete"
	ConfigPendingChangeBatchDisable = "batch_disable"
	ConfigPendingChangeBatchEnable  = "batch_enable"
)

// ConfigPendingChange 保存审批前冻结的配置内容。Ciphertext 必须始终为密文，通用审批载荷不保存内容。
type ConfigPendingChange struct {
	ID                  uint   `gorm:"primaryKey;autoIncrement"`
	ChangeID            string `gorm:"column:change_id;size:96;uniqueIndex;not null"`
	ApprovalRequestID   string `gorm:"column:approval_request_id;size:96;uniqueIndex;not null"`
	ConfigItemID        uint   `gorm:"column:config_item_id;not null;index"`
	ChangeType          string `gorm:"column:change_type;size:32;not null"`
	ExpectedVersion     int64  `gorm:"column:expected_version;not null"`
	ExpectedGrayVersion int64  `gorm:"column:expected_gray_version;not null;default:0"`
	ContentSHA256       string `gorm:"column:content_sha256;size:64;not null"`
	Ciphertext          string `gorm:"column:ciphertext;type:text;not null"`
	Status              string `gorm:"column:status;size:32;not null;index"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// TableName 固定待审批配置变更表名。
func (ConfigPendingChange) TableName() string { return "config_pending_change" }
