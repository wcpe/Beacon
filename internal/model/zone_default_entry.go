package model

import "time"

// ZoneDefaultEntry 是小区（zone）级「默认入口」：每个 (namespace, group, zone) 唯一指定一个
// 默认入口 serverId，指向已指派该 zone 的在线 bukkit 子服（FR-48）。
// 它与 zone_assignment 同类——控制面 DB 权威的拓扑分配事实，供发现下发给 BC agent 设默认/fallback 服。
// 清除即硬删（单值、无历史诉求）；改派即覆盖（按 (ns, group, zone) 唯一）。
type ZoneDefaultEntry struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 环境编码
	NamespaceCode string `gorm:"column:namespace_code;size:64;not null;uniqueIndex:uk_default_entry_zone,priority:1"`
	// 所属大区
	GroupCode string `gorm:"column:group_code;size:64;not null;uniqueIndex:uk_default_entry_zone,priority:2"`
	// 所属小区（zone）
	ZoneCode string `gorm:"column:zone_code;size:64;not null;uniqueIndex:uk_default_entry_zone,priority:3"`
	// 默认入口子服身份（须为已指派到该 (group, zone) 的 serverId，应用层校验）
	DefaultServerID string `gorm:"column:default_server_id;size:128;not null"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）
	UpdatedAt time.Time
}

// TableName 固定表名为 zone_default_entry。
func (ZoneDefaultEntry) TableName() string { return "zone_default_entry" }
