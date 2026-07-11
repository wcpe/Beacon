package model

import "time"

// MetricSampleV2 是第二版指标批日表 metric_sample_YYYYMMDD 的行模型（FR-144，见 v2-metrics-health-scheduling.md §3.1）。
//
// 每行 = 一台服务器一个 5s 批的批内聚合（原始 1s 样本不落库，仅在 agent 缓冲与控制面 60s 内存窗口存在）。
// 日表按 UTC 日期后缀分片（禁分区语法），首次写入当日数据时经 store.EnsureDailyTable 按需建表。
//
// 全部基础数值 / 字符串类型，禁 JSON/ENUM 列与方言专有 SQL，经 GORM 抽象（守 DB 可移植，可切 Postgres）。
// 唯一索引用 composite 空名式（uniqueIndex:,composite:xxx）让 GORM 按「当日表名」自动生成索引名——
// 避免同字面索引名在 sqlite（索引名全库唯一）跨日建表冲突，MySQL 索引名本就按表隔离、同样正确。
type MetricSampleV2 struct {
	// 自增主键（GORM 抽象，不绑方言自增）
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 归属 namespace（权威取自已鉴权身份，非请求体自报）
	NamespaceID uint `gorm:"column:namespace_id;not null"`
	// namespace 内唯一 serverId；与 bucket_start_ms 组唯一键支撑补报 / 重放幂等去重
	ServerID string `gorm:"column:server_id;size:64;not null;uniqueIndex:,composite:server_bucket,priority:1"`
	// 实例角色 proxy / backend（VARCHAR 落库 + 应用层校验，守可移植）；据此解释不适用列
	Kind string `gorm:"column:kind;size:16;not null"`
	// 5s 桶起点（agent 采样时钟，ts − ts%5000）
	BucketStartMs int64 `gorm:"column:bucket_start_ms;not null;uniqueIndex:,composite:server_bucket,priority:2"`
	// 桶内实际样本数（1~5）
	SampleCount int `gorm:"column:sample_count;not null;default:0"`
	// 进程 CPU 使用率 %（均值 / 峰值）
	CPUPctAvg float64 `gorm:"column:cpu_pct_avg;not null;default:0"`
	CPUPctMax float64 `gorm:"column:cpu_pct_max;not null;default:0"`
	// 已用堆内存 MB（均值）
	MemUsedMbAvg float64 `gorm:"column:mem_used_mb_avg;not null;default:0"`
	// 最大堆内存 MB
	MemMaxMb int `gorm:"column:mem_max_mb;not null;default:0"`
	// 仅 backend：TPS 均值 / 谷值；proxy 恒 0
	TPSAvg float64 `gorm:"column:tps_avg;not null;default:0"`
	TPSMin float64 `gorm:"column:tps_min;not null;default:0"`
	// 仅 backend：在线玩家数均值 / 峰值
	OnlineAvg int `gorm:"column:online_avg;not null;default:0"`
	OnlineMax int `gorm:"column:online_max;not null;default:0"`
	// 仅 backend：容量上限
	MaxOnline int `gorm:"column:max_online;not null;default:0"`
	// 仅 proxy：当前连接数均值 / 峰值
	ConnAvg int `gorm:"column:conn_avg;not null;default:0"`
	ConnMax int `gorm:"column:conn_max;not null;default:0"`
	// 仅 proxy：可达后端 / 配置后端数
	BackendUp    int `gorm:"column:backend_up;not null;default:0"`
	BackendTotal int `gorm:"column:backend_total;not null;default:0"`
	// 仅 proxy：可达后端 TCP RTT 均值，不可用为 -1
	BackendRttMsAvg float64 `gorm:"column:backend_rtt_ms_avg;not null;default:-1"`
	// agent 上一批上报 HTTP RTT，未知为 -1
	ReportRttMs int `gorm:"column:report_rtt_ms;not null;default:-1"`
	// 入库时间（UTC）
	CreatedAt time.Time
}

// TableName 返回基表名；实际写入表名由 db.Table(dailyName) 覆盖（见 store.EnsureDailyTable）。
// 本模型不进 AutoMigrate——日表按 UTC 日期后缀由 store.EnsureDailyTable 按需建。
func (MetricSampleV2) TableName() string { return "metric_sample" }
