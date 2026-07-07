package model

import "time"

// AuditLog 是轻量审计记录（append-only）：谁/何时/对什么/做了什么/结果。
type AuditLog struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 涉及环境（可空，如全局操作）
	NamespaceCode string `gorm:"column:namespace_code;size:64;index:idx_audit_namespace,priority:1"`
	// 操作人（管理台用户 / system / agent）
	Operator string `gorm:"column:operator;size:128;not null"`
	// 动作（如 config.publish / config.rollback）
	Action string `gorm:"column:action;size:64;not null"`
	// 对象类型（config / zone / instance / namespace）
	TargetType string `gorm:"column:target_type;size:32;not null;index:idx_audit_target,priority:1"`
	// 对象定位（如 prod/area1/mysql.yml@server）
	TargetRef string `gorm:"column:target_ref;size:256;not null;index:idx_audit_target,priority:2"`
	// 结构化详情（json 文本，含 before/after 摘要、version 等）
	Detail string `gorm:"column:detail;type:text"`
	// 结果：ok / fail
	Result string `gorm:"column:result;size:16;not null;default:'ok'"`
	// 来源 IP
	ClientIP string `gorm:"column:client_ip;size:64"`
	// 创建时间（UTC）；支撑按时间倒序与按环境过滤
	CreatedAt time.Time `gorm:"index:idx_audit_time;index:idx_audit_namespace,priority:2"`
}

// TableName 固定表名为 audit_log。
func (AuditLog) TableName() string { return "audit_log" }
