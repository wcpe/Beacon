package model

import "time"

// ChangeOrder 是交付编排 V2 的变更单主表（FR-162，规格 v2-delivery-orchestration.md §3.1）。
// 一次交付 = 一个变更单：黄金模板源文件差异 + 配置变更绑成一单，一起预览、审批、灰度、回滚。
// 只保存控制面编排事实；文件内容一律走流式数据面 blob，不进本表 / 命令 payload / 审计 detail。
type ChangeOrder struct {
	// 自增主键，兼作变更单 id
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 归属 namespace；单内一切实体不得跨 namespace（FR-162）
	NamespaceID uint `gorm:"column:namespace_id;not null;index:idx_change_order_ns_status,priority:1"`
	// 标题
	Title string `gorm:"column:title;size:128;not null"`
	// 说明（可空）
	Description string `gorm:"column:description;type:text"`
	// 黄金模板源 serverId；纯配置变更单为空
	SourceServerID string `gorm:"column:source_server_id;size:64"`
	// 差异扫描的服务器根内相对目录范围（如 plugins/），可空；重扫 / 重算用同一范围
	ScanDir string `gorm:"column:scan_dir;size:512"`
	// 单状态机（§4.1）：draft / pending_approval / approved / rolling / completed / paused / cancelled / rolling_back / rolled_back
	Status string `gorm:"column:status;size:32;not null;index:idx_change_order_ns_status,priority:2;index:idx_change_order_status"`
	// 暂停来源（可空）：manual / circuit_break / prepare_failed
	PauseKind string `gorm:"column:pause_kind;size:16"`
	// 暂停 / 熔断原因（脱敏文案，可空）
	PauseReason string `gorm:"column:pause_reason;size:512"`
	// 目标筛选器 JSON 快照（§4.3.1）；落 TEXT 不用 JSON 列（DB 可移植）
	Selector string `gorm:"column:selector;type:text"`
	// 批次规划模式：percent / count
	BatchMode string `gorm:"column:batch_mode;size:16;not null"`
	// 批次比例 / 台数 JSON 数组，如 [5,20,75]（percent）或 [1,10,50]（count）
	BatchSizes string `gorm:"column:batch_sizes;size:255;not null"`
	// 生效方式：restart / hot_reload / push_only，单级配置、全批继承
	ActivationMethod string `gorm:"column:activation_method;size:16;not null"`
	// 观察窗时长（秒），默认 120
	ObserveWindowSec int `gorm:"column:observe_window_sec;not null;default:120"`
	// 生效超时（秒），默认 300
	ActivateTimeoutSec int `gorm:"column:activate_timeout_sec;not null;default:300"`
	// 批内失败率熔断阈值（1-100；0 = 关闭该熔断）
	FailureRateThresholdPercent int `gorm:"column:failure_rate_threshold_percent;not null;default:0"`
	// 观察窗内批内 unhealthy 占比熔断阈值（1-100；0 = 关闭）
	UnhealthyRateThresholdPercent int `gorm:"column:unhealthy_rate_threshold_percent;not null;default:0"`
	// blob 就绪度：pending / uploading / ready / failed
	PayloadState string `gorm:"column:payload_state;size:16;not null"`
	// 差异计算依据的文件资产快照时间（可空）
	DiffSnapshotAt *time.Time `gorm:"column:diff_snapshot_at"`
	// 创建人
	CreatedBy string `gorm:"column:created_by;size:64;not null;index:idx_change_order_creator,priority:1"`
	// 提交审批时间（可空）
	SubmittedAt *time.Time `gorm:"column:submitted_at"`
	// 审批人（可空）
	ApprovedBy string `gorm:"column:approved_by;size:64"`
	// 审批通过时间（可空）
	ApprovedAt *time.Time `gorm:"column:approved_at"`
	// 最近一次驳回原因（可空）
	RejectReason string `gorm:"column:reject_reason;size:512"`
	// 执行起始时间（可空）
	StartedAt *time.Time `gorm:"column:started_at"`
	// 执行结束时间（可空）
	FinishedAt *time.Time `gorm:"column:finished_at"`
	// 紧急终止原因（可空）
	CancelReason string `gorm:"column:cancel_reason;size:512"`
	// 整单回滚发起人（可空）
	RollbackBy string `gorm:"column:rollback_by;size:64"`
	// 整单回滚原因（可空）
	RollbackReason string `gorm:"column:rollback_reason;size:512"`
	// 整单回滚时间（可空）
	RollbackAt *time.Time `gorm:"column:rollback_at"`
	// 创建时间（UTC）；(created_by, created_at) 复合索引尾列
	CreatedAt time.Time `gorm:"index:idx_change_order_creator,priority:2"`
	// 更新时间（UTC）
	UpdatedAt time.Time
}

// TableName 固定表名为 change_order。
func (ChangeOrder) TableName() string { return "change_order" }

// ChangeOrderItem 是变更单内的一个变更项（FR-162，规格 §3.2）。
// 两种载荷二选一：kind=file_diff（文件项，填 path/action/sha256/size_bytes）或
// kind=config_change（配置项，填 config_scope_* / config_*_version_id）；另一半列为 NULL。
// 可空列一律用指针以保 NULL：两条唯一索引靠 SQL「NULL 互不相等」实现「文件项按 path 唯一、
// 配置项按作用域唯一」而互不干扰（file 项 config 列全 NULL、config 项 path 列 NULL，跨方言一致）。
type ChangeOrderItem struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 归属变更单
	OrderID uint `gorm:"column:order_id;not null;index:idx_change_order_item_order;uniqueIndex:uk_change_order_item_file,priority:1;uniqueIndex:uk_change_order_item_config,priority:1"`
	// 载荷类型：file_diff / config_change
	Kind string `gorm:"column:kind;size:16;not null;uniqueIndex:uk_change_order_item_file,priority:2;uniqueIndex:uk_change_order_item_config,priority:2"`
	// 文件项：服务器根内相对路径（配置项为 NULL）
	Path *string `gorm:"column:path;size:512;uniqueIndex:uk_change_order_item_file,priority:3"`
	// 文件项：add / update / delete（相对目标语义在执行期按目标本地清单重判，§4.2.3）
	Action *string `gorm:"column:action;size:16"`
	// 文件项：模板源侧内容哈希（delete 项 / 配置项为 NULL）
	SHA256 *string `gorm:"column:sha256;size:64"`
	// 文件项：字节数（配置项为 NULL）
	SizeBytes *int64 `gorm:"column:size_bytes"`
	// 配置项：作用域层级（五层之一，定义引用 v2-config-center.md；文件项为 NULL）
	ConfigScopeKind *string `gorm:"column:config_scope_kind;size:16;uniqueIndex:uk_change_order_item_config,priority:3"`
	// 配置项：作用域实体 id（文件项为 NULL）
	ConfigScopeID *uint `gorm:"column:config_scope_id;uniqueIndex:uk_change_order_item_config,priority:4"`
	// 配置项：组单时该作用域当前生效版本快照（回滚锚点，可空）
	ConfigFromVersionID *uint `gorm:"column:config_from_version_id"`
	// 配置项：要发布的目标版本（可空）
	ConfigToVersionID *uint `gorm:"column:config_to_version_id"`
	// 创建时间（UTC）；项一经创建即不可变
	CreatedAt time.Time
}

// TableName 固定表名为 change_order_item。
func (ChangeOrderItem) TableName() string { return "change_order_item" }
