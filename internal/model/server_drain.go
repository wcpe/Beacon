package model

import (
	"time"

	"gorm.io/gorm"
)

// ServerDrain 是某 serverId 的 drain（排空 / 维护）标记（流量调度，FR-10，见 ADR-0017）。
// drain 是运维决策、须跨控制面重启存活、要可审计，故落 DB（与 zone_assignment 同源类别），
// 不放内存运行态。记录存在即"已 drain"，取消即软删（沿用 SoftDeleteSentinel 模式）。
type ServerDrain struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 环境编码
	NamespaceCode string `gorm:"column:namespace_code;size:64;not null;uniqueIndex:uk_drain_server,priority:1"`
	// 子服身份（agent 上报的 serverId）
	ServerID string `gorm:"column:server_id;size:128;not null;uniqueIndex:uk_drain_server,priority:2"`
	// drain 原因 / 备注（如"滚动维护"）
	Reason string `gorm:"column:reason;size:512"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）
	UpdatedAt time.Time
	// 软删时间；未删为哨兵值，纳入唯一键允许同 serverId 取消后再次 drain
	DeletedAt time.Time `gorm:"column:deleted_at;not null;uniqueIndex:uk_drain_server,priority:3"`
}

// TableName 固定表名为 server_drain。
func (ServerDrain) TableName() string { return "server_drain" }

// BeforeCreate 在插入前为未删记录写入软删哨兵值。
func (d *ServerDrain) BeforeCreate(*gorm.DB) error {
	if d.DeletedAt.IsZero() {
		d.DeletedAt = SoftDeleteSentinel
	}
	return nil
}
