package model

import (
	"time"

	"gorm.io/gorm"
)

// ApiKey 是运行时签发给外部服务的管理面访问密钥（FR-42，见 ADR-0026）。
// 要运行时创建/吊销/重置 → 必须持久化落库；**只存明文的 SHA-256 哈希，绝不存明文**。
// 吊销沿用 SoftDeleteSentinel 软删模式（与 server_drain 同源类别）。
type ApiKey struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 人类可读名称（标签），创建时必填，用于列表识别与审计定位
	Name string `gorm:"column:name;size:128;not null"`
	// 明文密钥的 SHA-256 十六进制摘要（64 hex）；查库即按此比对，唯一索引含软删哨兵
	KeyHash string `gorm:"column:key_hash;size:64;not null;uniqueIndex:uk_apikey_hash,priority:1"`
	// 明文前缀片段（非机密，如 bk_AbC123），仅供列表识别，不能反推完整密钥
	KeyPrefix string `gorm:"column:key_prefix;size:32;not null"`
	// 角色：full（读写，等同操作者）/ readonly（只读）；落 VARCHAR + 应用层校验（DB 可移植）
	Role string `gorm:"column:role;size:16;not null"`
	// 过期时刻（UTC）；为空（NULL）表示永不过期
	ExpiresAt *time.Time `gorm:"column:expires_at"`
	// 最近一次成功认证时刻（UTC）；为空表示从未使用（节流写，至多每分钟一次）
	LastUsedAt *time.Time `gorm:"column:last_used_at"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）；重置密钥会刷新
	UpdatedAt time.Time
	// 软删时间；未删为哨兵值，吊销即填真实时间（纳入唯一键，沿用 ADR-0008 模式）
	DeletedAt time.Time `gorm:"column:deleted_at;not null;uniqueIndex:uk_apikey_hash,priority:2"`
}

// TableName 固定表名为 api_key。
func (ApiKey) TableName() string { return "api_key" }

// BeforeCreate 在插入前为未删记录写入软删哨兵值。
func (k *ApiKey) BeforeCreate(*gorm.DB) error {
	if k.DeletedAt.IsZero() {
		k.DeletedAt = SoftDeleteSentinel
	}
	return nil
}
