package model

import (
	"time"

	"gorm.io/gorm"
)

// 可逆操作类型枚举（FR-116，落 VARCHAR + 应用层校验，见 ADR-0051 决策 2/4）。
// 纳入"会改受管真源或服务器实况"的三类大操作；删除 / 重命名 / 移动不纳入（其撤回等同回滚上一版本，被 publish 撤回覆盖）。
const (
	ReversibleOpPush    = "push"    // 下发：把某层文件覆盖 apply 到目标层（反向 = 回滚到下发前版本）
	ReversibleOpPublish = "publish" // 发布：编辑 / 上传配置后发布为某层新版本（反向 = 回滚到发布前版本）
	ReversibleOpFetch   = "fetch"   // 反向抓取 ingest：把扫描选定文件入库纳管（反向 = 撤销 ingest 纳管）
)

// IsValidReversibleOpType 校验可逆操作类型取值。
func IsValidReversibleOpType(t string) bool {
	switch t {
	case ReversibleOpPush, ReversibleOpPublish, ReversibleOpFetch:
		return true
	default:
		return false
	}
}

// 可逆操作状态枚举（FR-116，落 VARCHAR + 应用层校验，见 ADR-0051 决策 6/8）。
// status 兼作幂等闸（仅 reversible 可撤、撤后 reversed）与过期 / 被覆盖双闸。
const (
	ReversibleStatusReversible = "reversible" // 可撤回
	ReversibleStatusReversed   = "reversed"   // 已撤回（撤回成功后 CAS 翻转）
	ReversibleStatusExpired    = "expired"    // 超时窗口，不可撤回（清理器置，清空 inverse_payload）
	ReversibleStatusSuperseded = "superseded" // 被后续操作覆盖，不可撤回（同 scope 新操作落地时置）
)

// IsReversibleTerminal 判断状态是否为不可再撤回的终态（reversed / expired / superseded）。
func IsReversibleTerminal(status string) bool {
	switch status {
	case ReversibleStatusReversed, ReversibleStatusExpired, ReversibleStatusSuperseded:
		return true
	default:
		return false
	}
}

// ReversibleOperation 是配置操作级撤回子系统（FR-116，见 ADR-0051）的可逆账目：
// 每个大操作（push/publish/fetch）落地时连同其反向指令快照同事务记一条，撤回 = 读这条按反向指令把真源改回操作前。
//
// GORM 可移植（守架构不变量 #4）：op_type / status 枚举落 VARCHAR + 应用层校验、
// inverse_payload 反向快照落 TEXT（JSON 文本），不用 MySQL 专有 ENUM/SET/JSON 列、自增走 GORM 抽象、
// 软删用哨兵默认值（承 ADR-0008 决策1）。
type ReversibleOperation struct {
	// 自增主键，兼作可逆操作 id
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 操作落在哪个环境
	NamespaceCode string `gorm:"column:namespace_code;size:64;not null;index:idx_revop_scope,priority:1"`
	// 操作类型枚举：push / publish / fetch（应用层校验）
	OpType string `gorm:"column:op_type;size:16;not null"`
	// 覆盖层（global/group/zone/server）；fetch 为其落库层
	Scope string `gorm:"column:scope;size:16;not null;index:idx_revop_scope,priority:2"`
	// 该层目标键：global/group='' ；zone=zone编码；server=serverId（被覆盖判定用）
	ScopeTarget string `gorm:"column:scope_target;size:128;not null;default:'';index:idx_revop_scope,priority:3"`
	// 被操作对象的标识引用（publish/push=config_item/file_object 标识串、fetch=任务标识），供同 scope 被覆盖判定
	ForwardRef string `gorm:"column:forward_ref;size:256;not null;default:''"`
	// 状态枚举：reversible / reversed / expired / superseded（应用层校验，兼作幂等 / 过期 / 被覆盖闸）
	Status string `gorm:"column:status;size:16;not null;index:idx_revop_status"`
	// 反向指令 / 可逆快照（JSON 文本）：publish/push 记 {itemId,preVersion}；
	// fetch 记 {taskId,created:[fileId...],updated:[{fileId,preVersion}]}，供撤回时精确还原。
	// 过期 / 撤回后清空瞬态（避免快照 TEXT 长期滞留）。
	InversePayload string `gorm:"column:inverse_payload;type:text"`
	// 人类可读摘要（无敏感内容）：供操作日志展示，如"发布 mysql.yml @ 组 main"
	Summary string `gorm:"column:summary;size:512;not null;default:''"`
	// 操作人（与审计同源）
	Operator string `gorm:"column:operator;size:128;not null"`
	// 撤回人（撤回后回填）
	ReversedBy string `gorm:"column:reversed_by;size:128;not null;default:''"`
	// 撤回时间（撤回后回填；未撤回为哨兵零值）
	ReversedAt time.Time `gorm:"column:reversed_at"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）
	UpdatedAt time.Time
	// 软删时间；未删为哨兵值（见 SoftDeleteSentinel）。本表当前不软删账目，哨兵默认仅为可移植范式留位、保持与全表一致。
	DeletedAt time.Time `gorm:"column:deleted_at;not null"`
}

// TableName 固定表名为 reversible_operation。
func (ReversibleOperation) TableName() string { return "reversible_operation" }

// BeforeCreate 在插入前为未删记录写入软删哨兵值（非 NULL，与全表软删范式一致）。
func (o *ReversibleOperation) BeforeCreate(*gorm.DB) error {
	if o.DeletedAt.IsZero() {
		o.DeletedAt = SoftDeleteSentinel
	}
	return nil
}
