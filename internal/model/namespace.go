// Package model 定义 GORM 实体与领域枚举。
// 通用约定：BIGINT 自增主键、UTC 时间戳；禁用 MySQL 专有特性（枚举落 VARCHAR、
// json 落 TEXT、不写方言专有 gorm type），保证可切 Postgres。
package model

import "time"

// Namespace 表示一个环境隔离单元（如 prod / test）。
type Namespace struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 环境编码，全局唯一（如 prod / test）
	Code string `gorm:"column:code;size:64;uniqueIndex;not null"`
	// 环境显示名
	Name string `gorm:"column:name;size:128;not null"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）
	UpdatedAt time.Time
}

// TableName 固定表名为 namespace。
func (Namespace) TableName() string { return "namespace" }
