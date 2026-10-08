package model

import "time"

// ChangeRollbackRecord 是一次回滚动作的记录（FR-270 / FR-271，规格 delivery-rollback-resilience.md §3.1）。
// 回滚从「整单一个原子动作」扩展为可按目标子集执行后，必须能回答「谁 / 何时 / 为何 / 哪些台 / 每台结果」。
// 与 change_target.rollback_status 的分工：那张表回答「某台**现在**是什么回滚态」（会被后续动作覆写），
// 本表回答「发生过**哪些**回滚动作、当时打到了哪几台、每台结果如何」——两次事实，互不替代。
type ChangeRollbackRecord struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 归属变更单
	OrderID uint `gorm:"column:order_id;not null;index:idx_change_rollback_record_order"`
	// 动作种类：order（整单回滚 / 重试）/ targets（目标级子集回滚）
	Kind string `gorm:"column:kind;size:32;not null"`
	// 本次动作原因（必填，来自审批冻结载荷）
	Reason string `gorm:"column:reason;size:1024;not null"`
	// 发起人（审计引用，如 human:ops 或 mcp 主体）
	Operator string `gorm:"column:operator;size:128;not null"`
	// 本次动作是否回退了配置版本：整单回滚首次进入为真，子集回滚与重试恒为假（界面据此明示「配置未回退」）
	ConfigRolledBack bool `gorm:"column:config_rolled_back;not null;default:false"`
	// 本次动作涉及的目标数
	TargetCount int `gorm:"column:target_count;not null;default:0"`
	// 动作发起时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）
	UpdatedAt time.Time
}

// TableName 固定表名为 change_rollback_record。
func (ChangeRollbackRecord) TableName() string { return "change_rollback_record" }

// ChangeRollbackRecordTarget 是回滚动作内的逐台结果快照（FR-271）。
// 结果在动作创建时按目标当时真实回滚态初始化，并在该台进入终态（rolled_back / failed）时由推进器更新；
// 不直接读 change_target 是为了让「第一次回滚失败、第二次重试成功」两次动作各自留下自己的结果。
type ChangeRollbackRecordTarget struct {
	// 自增主键
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 归属回滚动作
	RecordID uint `gorm:"column:record_id;not null;index:idx_change_rollback_record_target"`
	// 目标 serverId
	ServerID string `gorm:"column:server_id;size:64;not null"`
	// 该台在本次动作里的结果：pending / running / rolled_back / failed
	Result string `gorm:"column:result;size:32"`
	// 该台失败原因（脱敏，ADR-0057；可空）
	Error string `gorm:"column:error;size:1024"`
	// 更新时间（UTC）
	UpdatedAt time.Time
}

// TableName 固定表名为 change_rollback_record_target。
func (ChangeRollbackRecordTarget) TableName() string { return "change_rollback_record_target" }

// CurrentDeliveredVersion 是某台服「当前交付版本」的读模型投影（FR-271）。
// 口径：该服最近一条 status=activated **且未被回滚**（rollback_status 非 rolled_back）的目标记录所属变更单。
// 不落库、不新增状态列——直接建在既有 change_target / change_order 上，避免第二真源；
// 该服被回滚后对应目标行转 rolled_back，被排除出候选，展示自然回退到上一单。
type CurrentDeliveredVersion struct {
	// 目标服 serverId
	ServerID string
	// 交付该版本的变更单号
	OrderID uint
	// 变更单标题
	OrderTitle string
	// 该服在该单内的生效时间（UTC）
	ActivatedAt time.Time
}
