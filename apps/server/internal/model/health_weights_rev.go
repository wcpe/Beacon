package model

import "time"

// HealthWeightsRev 是健康权重版本表 health_weights_rev 的行模型（FR-147，见 v2-metrics-health-scheduling.md §3.3）。
//
// 普通表（非日表）：rev 单调递增、由应用层在事务内取 max(rev)+1 指派（非方言自增），
// 每次修改权重设置即插入新行、旧行不改不删——健康快照与调度决策带 rev 即可精确回放「当时为什么是这个分」。
// config 落 TEXT 存完整配置 json（§4.4 结构），禁 JSON 列（守 DB 可移植，可切 Postgres）。
type HealthWeightsRev struct {
	// 单调递增版本号（应用层指派，主键防并发重复）
	Rev int `gorm:"column:rev;primaryKey;autoIncrement:false"`
	// 完整权重 + 阈值配置 json（service.HealthWeightsConfig 序列化）
	Config string `gorm:"column:config;type:text;not null"`
	// 操作人（种子行为 system）
	Operator string `gorm:"column:operator;size:64;not null"`
	// 生效时间（UTC）
	CreatedAt time.Time
}

// TableName 指定表名。
func (HealthWeightsRev) TableName() string { return "health_weights_rev" }
