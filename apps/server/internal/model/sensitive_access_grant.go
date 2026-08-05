package model

import "time"

const (
	// SensitiveAccessGrantStatusPending 表示等待 Agent 回传内容。
	SensitiveAccessGrantStatusPending = "pending"
	// SensitiveAccessGrantStatusActive 表示可由原申请主体消费。
	SensitiveAccessGrantStatusActive = "active"
	// SensitiveAccessGrantStatusConsumed 表示已成功消费。
	SensitiveAccessGrantStatusConsumed = "consumed"
	// SensitiveAccessGrantStatusExpired 表示已过期。
	SensitiveAccessGrantStatusExpired = "expired"
	// SensitiveAccessGrantStatusRevoked 表示命令失败或批准失效。
	SensitiveAccessGrantStatusRevoked = "revoked"
)

// SensitiveAccessGrant 是审批批准后签发的一次性敏感内容访问授权。
type SensitiveAccessGrant struct {
	ID                uint   `gorm:"primaryKey;autoIncrement"`
	GrantID           string `gorm:"size:64;not null;uniqueIndex"`
	ApprovalRequestID string `gorm:"size:64;not null;uniqueIndex"`
	RequesterType     string `gorm:"size:16;not null;index"`
	RequesterID       string `gorm:"size:128;not null;index"`
	Operation         string `gorm:"size:128;not null"`
	// PairID 非空时表示该授权只能与同一 pair 的另一侧原子消费，禁止单侧正文读取。
	PairID             string `gorm:"size:96;index"`
	PairSide           string `gorm:"size:8"`
	TargetRef          string `gorm:"size:255;not null"`
	ContentVersionHash string `gorm:"size:128;not null"`
	MaxUses            int    `gorm:"not null"`
	UsedAt             *time.Time
	ExpiresAt          time.Time `gorm:"not null;index"`
	Status             string    `gorm:"size:16;not null;index"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// TableName 返回敏感内容访问授权表名。
func (SensitiveAccessGrant) TableName() string { return "sensitive_access_grant" }
