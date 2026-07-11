package model

import "time"

// SchedDecisionV2 是调度决策日表 sched_decision_YYYYMMDD 的行模型（FR-146，见 v2-metrics-health-scheduling.md §3.4）。
//
// 每行 = 一次调度决策（control_plane 在线决策或 local_fallback 降级补报）。日表按 UTC 日期后缀
// 分片（禁分区语法），首次写入当日数据时经 store.EnsureDailyTable 按需建表。
//
// 全部基础数值 / 字符串类型，禁 JSON/ENUM 列与方言专有 SQL（守 DB 可移植，可切 Postgres）：
// strategy / source 等枚举落 VARCHAR + 应用层校验，excluded 逐台排除明细落 TEXT（json 数组文本）。
// 索引用 composite 空名式让 GORM 按「当日表名」自动生成索引名——避免同字面索引名在 sqlite
// （索引名全库唯一）跨日建表冲突（与 MetricSampleV2 同口径）。
type SchedDecisionV2 struct {
	// 自增主键（GORM 抽象，不绑方言自增）
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 决策唯一标识（UUID / 补报 localTraceId），唯一索引支撑补报重放幂等去重
	TraceID string `gorm:"column:trace_id;size:36;not null;uniqueIndex:,composite:trace"`
	// 决策时刻（毫秒）；与 namespace / chosen / requester 组三条复合索引支撑管理面按时间过滤
	TsMs int64 `gorm:"column:ts_ms;not null;index:,composite:ns_ts,priority:2;index:,composite:chosen_ts,priority:2;index:,composite:req_ts,priority:2"`
	// 请求方 namespace（权威取自已鉴权身份，非请求体自报）
	NamespaceID uint `gorm:"column:namespace_id;not null;index:,composite:ns_ts,priority:1"`
	// 跨 namespace 候选进入决策时为 true；本版 decide 无 ns 参数、恒 false（spec §2.2 默认拒绝）
	CrossNamespace bool `gorm:"column:cross_namespace;not null;default:false"`
	// 发起决策的 agent 所在服
	RequesterServerID string `gorm:"column:requester_server_id;size:64;not null;index:,composite:req_ts,priority:1"`
	// 业务插件名（agent-api 透传，可空存空串）
	Plugin string `gorm:"column:plugin;size:64;not null"`
	// 业务用途说明（可空存空串，如 lobby-transfer）
	Purpose string `gorm:"column:purpose;size:128;not null"`
	// 请求目标小区（按名称寻址，spec §8 待定 8）
	ZoneName string `gorm:"column:zone_name;size:64;not null"`
	// 决策所用策略名（本版恒 highest_score）
	Strategy string `gorm:"column:strategy;size:32;not null"`
	// control_plane / local_fallback（降级补报）
	Source string `gorm:"column:source;size:16;not null"`
	// 决策时权重版本（source=control_plane 时有值，补报为 0）
	WeightsRev int `gorm:"column:weights_rev;not null;default:0"`
	// 进入评估的候选总数
	CandidateCount int `gorm:"column:candidate_count;not null;default:0"`
	// 逐台排除原因 json 数组文本：[{serverId, reason}]（落 TEXT 不用 JSON 列，守可移植）
	Excluded string `gorm:"column:excluded;type:text"`
	// 最终选择（失败为空串）
	ChosenServerID string `gorm:"column:chosen_server_id;size:64;not null;index:,composite:chosen_ts,priority:1"`
	// 选择时健康分（失败为 -1）
	ChosenScore int `gorm:"column:chosen_score;not null;default:-1"`
	// 失败原因（成功为空串），如 no_candidate / zone_not_found
	FailReason string `gorm:"column:fail_reason;size:255;not null"`
	// 决策耗时（毫秒）
	DurationMs int `gorm:"column:duration_ms;not null;default:0"`
	// 入库时间（UTC）
	CreatedAt time.Time
}

// TableName 返回基表名；实际写入表名由 db.Table(dailyName) 覆盖（见 store.EnsureDailyTable）。
// 本模型不进 AutoMigrate——日表按 UTC 日期后缀由 store.EnsureDailyTable 按需建。
func (SchedDecisionV2) TableName() string { return "sched_decision" }
