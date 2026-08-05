package model

import "time"

// 系统执行状态。
const (
	SystemExecutionStatusPending   = "pending"
	SystemExecutionStatusRunning   = "running"
	SystemExecutionStatusSucceeded = "succeeded"
	SystemExecutionStatusFailed    = "failed"
)

// SystemExecution 是跨重启可追溯的控制面危险操作执行事实。
type SystemExecution struct {
	ID        uint   `gorm:"primaryKey;autoIncrement"`
	RequestID string `gorm:"size:64;not null;uniqueIndex:uk_system_execution_request"`
	Operation string `gorm:"size:64;not null"`
	Nonce     string `gorm:"size:64;not null;uniqueIndex:uk_system_execution_nonce"`
	Status    string `gorm:"size:16;not null"`
	// 冻结升级资产；回滚和设置操作按需留空。
	TargetVersion  string `gorm:"size:64"`
	AssetName      string `gorm:"size:256"`
	AssetSHA256    string `gorm:"size:64"`
	SettingKey     string `gorm:"size:128"`
	SettingValue   string `gorm:"size:1024"`
	SettingVersion int
	FailureReason  string `gorm:"type:text"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (SystemExecution) TableName() string { return "system_execution" }
