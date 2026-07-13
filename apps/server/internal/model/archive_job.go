package model

import "time"

// 归档任务模式（archive_job.mode，VARCHAR 落库 + 应用层校验，守可移植；FR-151，spec §3.2）。
const (
	// ArchiveModeDryRun 预览：只统计 rows_expected，零写归档、零删热库。
	ArchiveModeDryRun = "dry_run"
	// ArchiveModeExecute 执行：分批搬运 + 校验 + 校验通过后删热库。
	ArchiveModeExecute = "execute"
)

// IsValidArchiveMode 校验任务模式取值。
func IsValidArchiveMode(mode string) bool {
	return mode == ArchiveModeDryRun || mode == ArchiveModeExecute
}

// 归档任务触发方式（archive_job.trigger，spec §3.2）。
const (
	// ArchiveTriggerScheduled 每日自动触发（operator 固定 system）。
	ArchiveTriggerScheduled = "scheduled"
	// ArchiveTriggerManual 页面手动触发。
	ArchiveTriggerManual = "manual"
)

// 归档任务状态机（archive_job.status，spec §4.3 状态机）。
const (
	ArchiveJobPending    = "pending"
	ArchiveJobRunning    = "running"
	ArchiveJobSucceeded  = "succeeded"
	ArchiveJobFailed     = "failed"
	ArchiveJobCancelling = "cancelling"
	ArchiveJobCancelled  = "cancelled"
)

// IsArchiveJobActive 判断任务是否处于活跃（未终结）态：活跃任务受单飞约束（同一时刻至多一个）。
func IsArchiveJobActive(status string) bool {
	switch status {
	case ArchiveJobPending, ArchiveJobRunning, ArchiveJobCancelling:
		return true
	default:
		return false
	}
}

// 归档工作项阶段（archive_job_item.phase，spec §4.3 单 item 流水线）。
const (
	ArchiveItemPending   = "pending"
	ArchiveItemCopying   = "copying"
	ArchiveItemVerifying = "verifying"
	ArchiveItemDeleting  = "deleting"
	ArchiveItemDone      = "done"
	ArchiveItemFailed    = "failed"
	ArchiveItemSkipped   = "skipped"
)

// IsArchiveItemDone 判断工作项是否已抵终态（done / skipped）：重试时跳过、不重复处理（spec §4.3 断点续跑）。
func IsArchiveItemDone(phase string) bool {
	return phase == ArchiveItemDone || phase == ArchiveItemSkipped
}

// ArchiveJob 是归档任务（落热库，控制面事实，不随数据归档；FR-151，见 spec §3.2）。
//
// 全部基础数值 / 字符串类型：mode / trigger / status 枚举落 VARCHAR + 应用层校验；domains / cutoffs
// 落 TEXT 存 json；时间统一 UTC。禁 JSON/ENUM 列与方言专有 SQL（守 DB 可移植，可切 Postgres）。
type ArchiveJob struct {
	// 自增主键（GORM 抽象，不绑方言自增）
	ID uint `gorm:"primaryKey;autoIncrement"`
	// dry_run / execute
	Mode string `gorm:"column:mode;size:16;not null"`
	// scheduled（每日自动）/ manual（页面触发）
	Trigger string `gorm:"column:trigger;size:16;not null"`
	// pending / running / succeeded / failed / cancelling / cancelled
	Status string `gorm:"column:status;size:16;not null;index:idx_archive_job_status"`
	// 本次任务包含的 domain（json 数组文本；空数组 = 全部域）
	Domains string `gorm:"column:domains;type:text"`
	// 创建时按当时保留期快照的各域 cutoff（json 对象文本 {domain: RFC3339}），执行期不随设置热更漂移
	Cutoffs string `gorm:"column:cutoffs;type:text"`
	// 操作人；自动任务固定 system
	Operator string `gorm:"column:operator;size:128;not null"`
	// 失败原因（脱敏后可直接展示前端，ADR-0057），成功为空
	Error string `gorm:"column:error;type:text"`
	// 开始执行时间（UTC），未开始为 NULL
	StartedAt *time.Time `gorm:"column:started_at"`
	// 收尾时间（UTC），未收尾为 NULL
	FinishedAt *time.Time `gorm:"column:finished_at"`
	// 创建时间（UTC）
	CreatedAt time.Time `gorm:"column:created_at;not null;index:idx_archive_job_created"`
}

// TableName 固定表名为 archive_job。
func (ArchiveJob) TableName() string { return "archive_job" }

// ArchiveJobItem 是任务内以「域 × 表 / 区间」为粒度的工作项，也是断点续跑的检查点（FR-151，见 spec §3.2）。
//
// verify_* 系列在校验阶段前为 NULL（未校验），故用指针；cursor 空串 = 未开始。禁方言专有列（守可移植）。
type ArchiveJobItem struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 所属任务
	JobID uint `gorm:"column:job_id;not null;index:idx_archive_item_job;index:idx_archive_item_job_phase,priority:1"`
	// §3.1 域枚举
	Domain string `gorm:"column:domain;size:32;not null"`
	// 目标表名（日期后缀表为具体表名；单表为表名本身）。
	// 字段名避开 TableName——后者是 GORM 表名约定方法名，避免字段与方法同名冲突。
	TargetTable string `gorm:"column:table_name;size:128;not null"`
	// 单表形态的区间上界（= cutoff）；日期后缀表为 NULL
	RangeTo *time.Time `gorm:"column:range_to"`
	// pending / copying / verifying / deleting / done / failed / skipped
	Phase string `gorm:"column:phase;size:16;not null;index:idx_archive_item_job_phase,priority:2"`
	// 已搬运的最大主键（断点续跑游标）；空 = 未开始
	Cursor string `gorm:"column:cursor;size:128;not null;default:''"`
	// 预计归档行数（dry_run 只填本项）
	RowsExpected int64 `gorm:"column:rows_expected;not null;default:0"`
	// 已搬运行数
	RowsCopied int64 `gorm:"column:rows_copied;not null;default:0"`
	// 已删除行数
	RowsDeleted int64 `gorm:"column:rows_deleted;not null;default:0"`
	// 行数校验双侧结果（未校验为 NULL）
	VerifyRowsHot     *int64 `gorm:"column:verify_rows_hot"`
	VerifyRowsArchive *int64 `gorm:"column:verify_rows_archive"`
	// 抽样条数与随机种子（可复算，未校验为 NULL）
	VerifySampleSize *int   `gorm:"column:verify_sample_size"`
	VerifySampleSeed *int64 `gorm:"column:verify_sample_seed"`
	// 抽样哈希（sha256 小写 hex），未校验为空
	VerifyHashHot     string `gorm:"column:verify_hash_hot;size:64;not null;default:''"`
	VerifyHashArchive string `gorm:"column:verify_hash_archive;size:64;not null;default:''"`
	// 校验结论；仅 true 才允许进入 deleting（未校验为 NULL）
	VerifyPassed *bool `gorm:"column:verify_passed"`
	// 本项失败原因（脱敏），成功为空
	Error string `gorm:"column:error;type:text"`
}

// TableName 固定表名为 archive_job_item。
func (ArchiveJobItem) TableName() string { return "archive_job_item" }
