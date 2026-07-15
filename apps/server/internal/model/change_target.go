package model

import "time"

// ChangeTarget 是变更单内的单个目标服执行记录（FR-166/FR-167，规格 v2-delivery-orchestration.md §3.4）。
// 启动时按 selector 解析并快照固化，此后集群拓扑变化不影响本单目标集。
// 回滚推进用独立 rollback_status 字段、不复用主状态：一个目标同时保留「正推结果」与「回滚结果」两份事实。
// 按规格 §3.4 只保留 updated_at 通用时间列（目标由启动一次性批量快照生成，无独立 created 列）。
type ChangeTarget struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 归属变更单
	OrderID uint `gorm:"column:order_id;not null;index:idx_change_target_order;uniqueIndex:uk_change_target_server,priority:1"`
	// 归属批次
	BatchID uint `gorm:"column:batch_id;not null;index:idx_change_target_batch"`
	// 目标 serverId；唯一约束 (order_id, server_id)
	ServerID string `gorm:"column:server_id;size:64;not null;uniqueIndex:uk_change_target_server,priority:2"`
	// 目标状态机（§4.1）：pending / pushing / pushed / activating / activated / skipped / failed
	Status string `gorm:"column:status;size:32;not null"`
	// 推送完成时间（可空）
	PushedAt *time.Time `gorm:"column:pushed_at"`
	// 生效完成时间（可空）
	ActivatedAt *time.Time `gorm:"column:activated_at"`
	// agent 回执的实际变更文件数
	ChangedFileCount int `gorm:"column:changed_file_count;not null;default:0"`
	// agent 回执的跳过（本地同 hash）文件数
	SkippedFileCount int `gorm:"column:skipped_file_count;not null;default:0"`
	// agent 回执是否已生成本地备份（回滚预检依据）
	BackupPresent bool `gorm:"column:backup_present;not null;default:false"`
	// 失败原因（脱敏，展示到前端，ADR-0057；可空）
	Error string `gorm:"column:error;size:1024"`
	// 回滚状态（独立于主状态，可空）：pending / running / rolled_back / failed
	RollbackStatus string `gorm:"column:rollback_status;size:32"`
	// 回滚失败原因（脱敏，可空）
	RollbackError string `gorm:"column:rollback_error;size:1024"`
	// 更新时间（UTC）
	UpdatedAt time.Time
}

// TableName 固定表名为 change_target。
func (ChangeTarget) TableName() string { return "change_target" }
