package model

import "time"

// DeliveryConfigArtifact 是交付数据面「配置灰度冻结渲染工件」（FR-171，见 ADR-0071）。
// config_change 项由控制面按每个目标一次性渲染灰度生效明文并写入内容寻址 blob；渲染 sha 是
// per-(项, 目标) 的、无法落进 change_order_item.sha256（配置项该列恒 NULL），故单独持久冻结为本表：
// manifest 下发、下载授权反查、清理护栏三处都读这份冻结记录，不再重渲染（消除 head 漂移竞态）。
// 一条工件 = 某单某目标某落盘路径的一份渲染结果；(order_id, server_id, path) 唯一（重跑 start 覆盖同键）。
type DeliveryConfigArtifact struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 归属变更单；(order_id, server_id) 前缀支撑 manifest 按目标取工件
	OrderID uint `gorm:"column:order_id;not null;index:idx_delivery_config_artifact_order;uniqueIndex:uk_delivery_config_artifact,priority:1"`
	// 目标 serverId（渲染绑定的目标；下载授权即以此校验「本目标」）
	ServerID string `gorm:"column:server_id;size:64;not null;uniqueIndex:uk_delivery_config_artifact,priority:2"`
	// 落盘相对路径 = 配置文件逻辑名（如 plugins/Foo/config.yml）
	Path string `gorm:"column:path;size:512;not null;uniqueIndex:uk_delivery_config_artifact,priority:3"`
	// 渲染明文 sha256 小写 hex（内容寻址 blob 身份；授权反查与清理保护按此列）
	SHA256 string `gorm:"column:sha256;size:64;not null;index:idx_delivery_config_artifact_sha"`
	// 明文字节数
	SizeBytes int64 `gorm:"column:size_bytes;not null;default:0"`
	// 创建时间（UTC）
	CreatedAt time.Time
}

// TableName 固定表名为 delivery_config_artifact。
func (DeliveryConfigArtifact) TableName() string { return "delivery_config_artifact" }
