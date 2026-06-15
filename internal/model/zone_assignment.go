package model

import (
	"time"

	"gorm.io/gorm"
)

// ZoneAssignment 是 serverId → (group, zone) 的权威指派（方案 b：zone 归属由控制面权威）。
// 一个 serverId 在一个环境内唯一归属一个 (group, zone)；换区只改这一行，agent 零改动。
type ZoneAssignment struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 环境编码
	NamespaceCode string `gorm:"column:namespace_code;size:64;not null;uniqueIndex:uk_assignment_server,priority:1;index:idx_assignment_zone,priority:1"`
	// 子服身份（agent 上报的 serverId）
	ServerID string `gorm:"column:server_id;size:128;not null;uniqueIndex:uk_assignment_server,priority:2"`
	// 所属大区
	GroupCode string `gorm:"column:group_code;size:64;not null;index:idx_assignment_zone,priority:2"`
	// 所属小区（zone）
	ZoneCode string `gorm:"column:zone_code;size:64;not null;index:idx_assignment_zone,priority:3"`
	// 备注
	Note string `gorm:"column:note;size:512"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）
	UpdatedAt time.Time
	// 软删时间；未删为哨兵值，纳入唯一键允许同 serverId 重新指派
	DeletedAt time.Time `gorm:"column:deleted_at;not null;uniqueIndex:uk_assignment_server,priority:3"`
}

// TableName 固定表名为 zone_assignment。
func (ZoneAssignment) TableName() string { return "zone_assignment" }

// BeforeCreate 在插入前为未删记录写入软删哨兵值。
func (z *ZoneAssignment) BeforeCreate(*gorm.DB) error {
	if z.DeletedAt.IsZero() {
		z.DeletedAt = SoftDeleteSentinel
	}
	return nil
}
