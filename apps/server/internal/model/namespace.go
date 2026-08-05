// Package model 定义 GORM 实体与领域枚举。
// 通用约定：BIGINT 自增主键、UTC 时间戳；禁用 MySQL 专有特性（枚举落 VARCHAR、
// json 落 TEXT、不写方言专有 gorm type），保证可切 Postgres。
package model

import (
	"time"

	"gorm.io/gorm"
)

// Namespace 表示一个环境隔离单元（如 prod / test）。
type Namespace struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 环境编码，全局唯一（如 prod / test）
	Code string `gorm:"column:code;size:64;uniqueIndex;not null"`
	// 环境显示名
	Name string `gorm:"column:name;size:128;not null"`
	// v2 描述文本；Legacy 未使用，第二版 namespace 管理使用。
	Description string `gorm:"column:description;size:255"`
	// v2 namespace 接入 token 的 sha256 摘要；明文只在创建 / 轮换响应返回一次。
	AccessTokenHash string `gorm:"column:access_token_hash;size:64;index"`
	// 生命周期状态：active / archived / tombstoned。
	Lifecycle string `gorm:"column:lifecycle;size:16;not null;default:active;index"`
	// 归档时间；仅 archived 状态有值。
	ArchivedAt *time.Time `gorm:"column:archived_at"`
	// 发起归档的审批申请人。
	ArchivedBy string `gorm:"column:archived_by;size:128"`
	// 归档审批原因。
	ArchiveReason string `gorm:"column:archive_reason;size:255"`
	// 永久墓碑时间；墓碑记录保留以阻止同 code 重用。
	TombstonedAt *time.Time `gorm:"column:tombstoned_at"`
	// 发起永久墓碑的审批申请人。
	TombstonedBy string `gorm:"column:tombstoned_by;size:128"`
	// 永久墓碑审批原因。
	TombstoneReason string `gorm:"column:tombstone_reason;size:255"`
	// 永久墓碑审批请求标识。
	TombstoneApprovalRequestID string `gorm:"column:tombstone_approval_request_id;size:64;index"`
	// 永久墓碑冻结影响集合哈希。
	TombstoneImpactHash string `gorm:"column:tombstone_impact_hash;size:64"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）
	UpdatedAt time.Time
}

// TableName 固定表名为 namespace。
func (Namespace) TableName() string { return "namespace" }

func (n *Namespace) BeforeSave(*gorm.DB) error {
	if n.Code == "" {
		n.Code = n.Name
	}
	if n.Name == "" {
		n.Name = n.Code
	}
	if n.Lifecycle == "" {
		n.Lifecycle = NamespaceLifecycleActive
	}
	return nil
}
