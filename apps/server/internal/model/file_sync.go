package model

import "time"

// FileSyncTask 是多级灰度文件同步任务主表（FR-129/FR-131）。
// 这里只保存控制面编排事实；真实文件内容不进入本表、命令 payload 或审计 detail。
type FileSyncTask struct {
	// 自增主键，兼作任务 id
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 环境编码
	NamespaceCode string `gorm:"column:namespace_code;size:64;not null;index:idx_file_sync_task_ns"`
	// 黄金模板源 serverId
	SourceServerID string `gorm:"column:source_server_id;size:128;not null"`
	// 服务器根内相对目录，已做字符串级安全归一
	Directory string `gorm:"column:directory;size:512;not null"`
	// 任务状态，落 VARCHAR
	Status string `gorm:"column:status;size:32;not null;index:idx_file_sync_task_status"`
	// 源扫描命令 id
	SourceCommandID uint `gorm:"column:source_command_id;not null;default:0"`
	// 源清单与 blob 缓存是否已就绪
	SourceReady bool `gorm:"column:source_ready;not null;default:false"`
	// 源清单文件数
	SourceFileCount int `gorm:"column:source_file_count;not null;default:0"`
	// 源清单总字节数
	SourceTotalBytes int64 `gorm:"column:source_total_bytes;not null;default:0"`
	// 单批目标数
	BatchSize int `gorm:"column:batch_size;not null"`
	// 批间等待秒数
	IntervalSec int `gorm:"column:interval_sec;not null"`
	// 单批失败率阈值百分比
	FailureThresholdPercent int `gorm:"column:failure_threshold_percent;not null"`
	// 触发操作者
	Operator string `gorm:"column:operator;size:128;not null"`
	// 规划出的目标总数
	TargetCount int `gorm:"column:target_count;not null;default:0"`
	// 规划出的批次数
	BatchCount int `gorm:"column:batch_count;not null;default:0"`
	// 启动时间
	StartedAt *time.Time `gorm:"column:started_at"`
	// 结束时间
	FinishedAt *time.Time `gorm:"column:finished_at"`
	// 创建时间
	CreatedAt time.Time
	// 更新时间
	UpdatedAt time.Time
}

// TableName 固定表名。
func (FileSyncTask) TableName() string { return "file_sync_task" }

// FileSyncFile 是源服务器扫描得到的文件清单。
type FileSyncFile struct {
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 所属任务
	TaskID uint `gorm:"column:task_id;not null;index:idx_file_sync_file_task,priority:1;uniqueIndex:uk_file_sync_file_path,priority:1"`
	// 相对同步目录的文件路径
	Path string `gorm:"column:path;size:512;not null;uniqueIndex:uk_file_sync_file_path,priority:2"`
	// 文件大小
	Size int64 `gorm:"column:size;not null;default:0"`
	// sha256 十六进制摘要
	Hash string `gorm:"column:hash;size:64;not null;index:idx_file_sync_file_hash"`
	// blob 缓存键，当前等于 hash
	BlobKey   string `gorm:"column:blob_key;size:128;not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName 固定表名。
func (FileSyncFile) TableName() string { return "file_sync_file" }

// FileSyncBatch 是一次任务的分批计划。
type FileSyncBatch struct {
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 所属任务
	TaskID uint `gorm:"column:task_id;not null;index:idx_file_sync_batch_task,priority:1;uniqueIndex:uk_file_sync_batch_no,priority:1"`
	// 批次序号，从 1 开始
	BatchNo int `gorm:"column:batch_no;not null;index:idx_file_sync_batch_task,priority:2;uniqueIndex:uk_file_sync_batch_no,priority:2"`
	// 批次状态，落 VARCHAR
	Status string `gorm:"column:status;size:32;not null"`
	// 规划目标数
	PlannedCount int `gorm:"column:planned_count;not null;default:0"`
	// 成功目标数
	SuccessCount int `gorm:"column:success_count;not null;default:0"`
	// 失败目标数
	FailedCount int        `gorm:"column:failed_count;not null;default:0"`
	StartedAt   *time.Time `gorm:"column:started_at"`
	FinishedAt  *time.Time `gorm:"column:finished_at"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TableName 固定表名。
func (FileSyncBatch) TableName() string { return "file_sync_batch" }

// FileSyncTarget 是一次任务内的单个目标执行记录。
type FileSyncTarget struct {
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 所属任务
	TaskID uint `gorm:"column:task_id;not null;index:idx_file_sync_target_task,priority:1;uniqueIndex:uk_file_sync_target_server,priority:1"`
	// 所属批次记录 id
	BatchID uint `gorm:"column:batch_id;not null;index:idx_file_sync_target_batch"`
	// 冗余批次序号，便于列表展示
	BatchNo int `gorm:"column:batch_no;not null;index:idx_file_sync_target_task,priority:2"`
	// 目标 serverId
	ServerID string `gorm:"column:server_id;size:128;not null;uniqueIndex:uk_file_sync_target_server,priority:2"`
	// 目标状态，落 VARCHAR
	Status string `gorm:"column:status;size:32;not null"`
	// 下发给该目标的命令 id
	CommandID uint `gorm:"column:command_id;not null;default:0"`
	// 覆盖前备份路径
	BackupPath string `gorm:"column:backup_path;size:512"`
	// 当前文件数
	CurrentFileCount int `gorm:"column:current_file_count;not null;default:0"`
	// 变化文件数
	ChangedFileCount int `gorm:"column:changed_file_count;not null;default:0"`
	// 跳过文件数
	SkippedFileCount int `gorm:"column:skipped_file_count;not null;default:0"`
	// 需传输总字节数
	BytesTotal int64 `gorm:"column:bytes_total;not null;default:0"`
	// 已传输字节数
	BytesDone int64 `gorm:"column:bytes_done;not null;default:0"`
	// 脱敏后的失败原因
	LastError  string     `gorm:"column:last_error;size:512"`
	StartedAt  *time.Time `gorm:"column:started_at"`
	FinishedAt *time.Time `gorm:"column:finished_at"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// TableName 固定表名。
func (FileSyncTarget) TableName() string { return "file_sync_target" }

// FileSyncLog 是任务进度日志摘要，供刷新后回放和 SSE 尾随。
type FileSyncLog struct {
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 所属任务
	TaskID uint `gorm:"column:task_id;not null;index:idx_file_sync_log_task,priority:1"`
	// 可选批次 id
	BatchID uint `gorm:"column:batch_id;index:idx_file_sync_log_batch"`
	// 可选目标 serverId
	ServerID string `gorm:"column:server_id;size:128"`
	// 日志级别，落 VARCHAR
	Level string `gorm:"column:level;size:16;not null"`
	// 中文日志消息，不含文件内容
	Message string `gorm:"column:message;size:512;not null"`
	// 创建时间，按 id 和时间升序回放
	CreatedAt time.Time `gorm:"index:idx_file_sync_log_task,priority:2"`
}

// TableName 固定表名。
func (FileSyncLog) TableName() string { return "file_sync_log" }
