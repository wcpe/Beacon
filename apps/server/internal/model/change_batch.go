package model

import "time"

// ChangeBatch 是变更单的一批灰度目标（FR-166，规格 v2-delivery-orchestration.md §3.3）。
// 批次在启动时一次性生成落库、执行中不重划；每批走「推送 → 生效 → 观察窗 → 人工确认」推进门。
// 按规格 §3.3 只保留 started_at / observe_started_at / finished_at 业务时间，不设通用 created/updated 列。
type ChangeBatch struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 归属变更单
	OrderID uint `gorm:"column:order_id;not null;index:idx_change_batch_order;uniqueIndex:uk_change_batch_no,priority:1"`
	// 批次序号，从 1 递增；唯一约束 (order_id, batch_no)
	BatchNo int `gorm:"column:batch_no;not null;uniqueIndex:uk_change_batch_no,priority:2"`
	// 批状态机（§4.1）：pending / running / observing / awaiting_confirm / completed / skipped / failed
	Status string `gorm:"column:status;size:32;not null"`
	// 批内目标数
	PlannedCount int `gorm:"column:planned_count;not null;default:0"`
	// 终态成功数
	SuccessCount int `gorm:"column:success_count;not null;default:0"`
	// 终态失败数
	FailedCount int `gorm:"column:failed_count;not null;default:0"`
	// 终态跳过数
	SkippedCount int `gorm:"column:skipped_count;not null;default:0"`
	// 批启动时间（可空）
	StartedAt *time.Time `gorm:"column:started_at"`
	// 观察窗开始时间（可空）
	ObserveStartedAt *time.Time `gorm:"column:observe_started_at"`
	// 批结束时间（可空）
	FinishedAt *time.Time `gorm:"column:finished_at"`
	// 推进门人工确认人（可空）
	GateConfirmedBy string `gorm:"column:gate_confirmed_by;size:64"`
	// 推进门人工确认时间（可空）
	GateConfirmedAt *time.Time `gorm:"column:gate_confirmed_at"`
	// 熔断原因（含触发阈值与实测值，脱敏文案，可空）
	BreakReason string `gorm:"column:break_reason;size:512"`
}

// TableName 固定表名为 change_batch。
func (ChangeBatch) TableName() string { return "change_batch" }
