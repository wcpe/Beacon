package model

import "time"

// AgentCommand 是控制面下发给指定在线 agent 的命令（FR-39，见 ADR-0027）。
// 本期唯一类型 ingest-plugins：令目标 agent 读其真实 plugins 目录的文本配置回传 ingest。
// 真源落库：可跨 SSE 断连重拉、可审计、有生命周期（pending→fetched→done/failed/expired）。
type AgentCommand struct {
	// 自增主键，兼作命令 id（agent 回传时带回引用）
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 目标实例所属环境
	NamespaceCode string `gorm:"column:namespace;size:64;not null;index:idx_agent_command_lookup,priority:1"`
	// 目标实例 serverId
	ServerID string `gorm:"column:server_id;size:128;not null;index:idx_agent_command_lookup,priority:2"`
	// 命令类型（落 VARCHAR + 应用层校验，不绑方言）；本期仅 ingest-plugins
	Type string `gorm:"column:type;size:32;not null"`
	// 命令载荷（JSON 文本）：本期记 ingest 目标（scope / group 等）；落 TEXT 不用 JSON 列（DB 可移植）
	Payload string `gorm:"column:payload;type:text"`
	// 状态：pending / fetched / done / failed / expired（落 VARCHAR + 应用层校验）
	Status string `gorm:"column:status;size:16;not null;index:idx_agent_command_lookup,priority:3"`
	// 结果摘要（失败原因 / 入库文件数等）；**绝不含敏感文件内容**
	ResultDetail string `gorm:"column:result_detail;type:text"`
	// 触发操作者（admin 认证身份，非手填）
	Operator string `gorm:"column:operator;size:128;not null"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）；每次状态迁移刷新
	UpdatedAt time.Time
}

// TableName 固定表名为 agent_command。
func (AgentCommand) TableName() string { return "agent_command" }

// IsValidCommandType 校验命令类型取值。
func IsValidCommandType(t string) bool {
	switch t {
	case CommandTypeIngestPlugins:
		return true
	default:
		return false
	}
}
