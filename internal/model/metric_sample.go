package model

import "time"

// MetricSample 是负载指标时序样本（FR-32 / ADR-0023）：控制面按固定间隔对在线实例采样落表，
// 形成历史趋势供管理台看板出图。仅存「负载数字（健康事实）」——人数 / TPS / 内存 / CPU，
// 不含玩家名单 / 身份（看人归③层业务插件，越界）。
//
// 全部基础数值 / 字符串类型，禁 JSON/ENUM 列与方言专有 SQL，经 GORM 抽象（守 DB 可移植，可切 Postgres）。
type MetricSample struct {
	// 自增主键（GORM 抽象，不绑方言自增）
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 环境编码；与 sampled_at 组成复合索引支撑按时间窗 + 环境查询
	Namespace string `gorm:"column:namespace;size:64;not null;index:idx_metric_window,priority:1"`
	// 子服标识；按 serverId 可选过滤分服趋势
	ServerID string `gorm:"column:server_id;size:128;not null;index:idx_metric_window,priority:2"`
	// 采样时刻（UTC）；时间窗查询主维度，置于复合索引末位
	SampledAt time.Time `gorm:"column:sampled_at;not null;index:idx_metric_window,priority:3"`
	// 在线人数（仅展示，不参与决策）
	PlayerCount int `gorm:"column:player_count;not null;default:0"`
	// TPS（仅展示，不参与决策）
	TPS float64 `gorm:"column:tps;not null;default:0"`
	// JVM 已用堆字节（仅展示，不参与决策）
	MemUsed int64 `gorm:"column:mem_used;not null;default:0"`
	// JVM 最大堆字节（仅展示，不参与决策）
	MemMax int64 `gorm:"column:mem_max;not null;default:0"`
	// 进程 CPU 负载[0,1]，-1.0=不可用（近似值，仅展示，不参与决策）
	CpuLoad float64 `gorm:"column:cpu_load;not null;default:0"`
}

// TableName 固定表名为 metric_sample。
func (MetricSample) TableName() string { return "metric_sample" }
