package model

import (
	"time"

	"gorm.io/gorm"
)

// ServerOffline 是某 serverId 的主动下线拒绝标记（FR-49，见 ADR 实例主动下线态）。
// 与 server_drain（排空、仍可连）、健康 TTL（自动衰退）三者职责分离、互不混用：
// 本表语义是"运维按死该实例、拒绝其注册接入"，须跨控制面重启存活、要可审计，故落 DB
// （与 zone_assignment / server_drain 同源类别），不放内存运行态。
// 记录存在即"已下线"，取消下线即软删（沿用 SoftDeleteSentinel 模式）。
type ServerOffline struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 环境编码
	NamespaceCode string `gorm:"column:namespace_code;size:64;not null;uniqueIndex:uk_offline_server,priority:1"`
	// 子服身份（agent 上报的 serverId）
	ServerID string `gorm:"column:server_id;size:128;not null;uniqueIndex:uk_offline_server,priority:2"`
	// 下线原因 / 备注（如"故障下架"），可空
	Reason string `gorm:"column:reason;size:512"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）
	UpdatedAt time.Time
	// 软删时间；未删为哨兵值，纳入唯一键允许同 serverId 取消后再次下线
	DeletedAt time.Time `gorm:"column:deleted_at;not null;uniqueIndex:uk_offline_server,priority:3"`
}

// TableName 固定表名为 server_offline。
func (ServerOffline) TableName() string { return "server_offline" }

// BeforeCreate 在插入前为未删记录写入软删哨兵值。
func (o *ServerOffline) BeforeCreate(*gorm.DB) error {
	if o.DeletedAt.IsZero() {
		o.DeletedAt = SoftDeleteSentinel
	}
	return nil
}
