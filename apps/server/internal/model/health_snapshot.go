package model

import "time"

// HealthSnapshot 是健康快照日表 health_snapshot_YYYYMMDD 的行模型（FR-147，见 v2-metrics-health-scheduling.md §3.2）。
//
// 每行 = 一台服务器一次周期快照（默认 30s 一轮，全量在册实例）；内存健康视图是真源，本表仅回放副本。
// 日表按 UTC 日期后缀分片，首次写入当日数据时经 store.EnsureDailyTable 按需建表。
//
// reasons / factors 落 TEXT 存 json 数组，禁 JSON/ENUM 列与方言专有 SQL（守 DB 可移植，可切 Postgres）。
// 索引用 composite 空名式（index:,composite:xxx）让 GORM 按「当日表名」自动生成索引名——
// 避免同字面索引名在 sqlite（索引名全库唯一）跨日建表冲突，MySQL 索引名本就按表隔离、同样正确。
type HealthSnapshot struct {
	// 自增主键（GORM 抽象，不绑方言自增）
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 快照时刻（毫秒）；与 server_id / namespace_id 组 §3.2 两组查询索引
	TsMs int64 `gorm:"column:ts_ms;not null;index:,composite:server_ts,priority:2;index:,composite:ns_ts,priority:2"`
	// 归属 namespace
	NamespaceID uint `gorm:"column:namespace_id;not null;index:,composite:ns_ts,priority:1"`
	// namespace 内唯一 serverId
	ServerID string `gorm:"column:server_id;size:64;not null;index:,composite:server_ts,priority:1"`
	// 实例角色 proxy / backend（VARCHAR + 应用层校验，守可移植）
	Kind string `gorm:"column:kind;size:16;not null"`
	// 0-100 健康分（lost 为 0）
	Score int `gorm:"column:score;not null;default:0"`
	// 健康等级 healthy / degraded / unhealthy
	Level string `gorm:"column:level;size:16;not null"`
	// 是否可调度（§4.5 判定结果）
	Schedulable bool `gorm:"column:schedulable;not null;default:false"`
	// 不可调度原因码 json 数组（§4.5）
	Reasons string `gorm:"column:reasons;type:text"`
	// 因子明细 json 数组 [{factor, raw, normalized, weight, applicable}]，回放解释用
	Factors string `gorm:"column:factors;type:text"`
	// 计算时使用的权重配置版本（关联 health_weights_rev 精确回放）
	WeightsRev int `gorm:"column:weights_rev;not null;default:0"`
	// 入库时间（UTC）
	CreatedAt time.Time
}

// TableName 返回基表名；实际写入表名由 db.Table(dailyName) 覆盖（见 store.EnsureDailyTable）。
// 本模型不进 AutoMigrate——日表按 UTC 日期后缀由 store.EnsureDailyTable 按需建。
func (HealthSnapshot) TableName() string { return "health_snapshot" }
