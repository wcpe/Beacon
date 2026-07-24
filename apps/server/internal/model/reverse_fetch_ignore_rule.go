package model

import (
	"time"

	"gorm.io/gorm"
)

// 忽略规则类型（FR-59，落 VARCHAR + 应用层校验，不绑方言）。
const (
	IgnoreRuleExact  = "exact"  // 单文件精确：path == pattern 命中
	IgnoreRulePrefix = "prefix" // 目录前缀：strings.HasPrefix(path, pattern) 命中
)

// IsValidIgnoreRuleType 校验忽略规则类型取值。
func IsValidIgnoreRuleType(t string) bool {
	switch t {
	case IgnoreRuleExact, IgnoreRulePrefix:
		return true
	default:
		return false
	}
}

// ReverseFetchIgnoreRule 是反向抓取的持久忽略规则（FR-59，增强 FR-39）：把运行时垃圾等下次扫描该排除的
// 文件 / 目录登记成规则，扫描清单返回时对命中当前任务作用域活跃规则的文件标 ignoredByRule（纯展示标记）。
// 规则按 ns / 大区(group) / 实例(scope=server 时的 serverId) 维度，类型 exact（单文件）/ prefix（目录前缀）。
//
// GORM 可移植：状态 / 类型落 VARCHAR + 应用层校验、软删哨兵唯一键（取消即软删、同标识可再建），
// 不用 ENUM/SET/JSON 列（沿 server_drain / zone_assignment 软删范式）。
type ReverseFetchIgnoreRule struct {
	// 自增主键，兼作规则 id
	ID uint `gorm:"primaryKey;autoIncrement"`
	// 规则所属环境
	NamespaceCode string `gorm:"column:namespace_code;size:64;not null;uniqueIndex:uk_ignore_rule,priority:1;index:idx_ignore_ns_group,priority:1"`
	// 作用域层：group（大区层规则，作用该大区下任意实例的抓取）/ server（仅作用某 serverId 的抓取）
	Scope string `gorm:"column:scope;size:16;not null;uniqueIndex:uk_ignore_rule,priority:2"`
	// 所属大区
	GroupCode string `gorm:"column:group_code;size:64;not null;uniqueIndex:uk_ignore_rule,priority:3;index:idx_ignore_ns_group,priority:2"`
	// scope=server 时的目标 serverId（scope=group 留空）
	ScopeTarget string `gorm:"column:scope_target;size:128;uniqueIndex:uk_ignore_rule,priority:4"`
	// 规则类型：exact（单文件精确）/ prefix（目录前缀）
	RuleType string `gorm:"column:rule_type;size:16;not null;uniqueIndex:uk_ignore_rule,priority:5"`
	// 匹配模式：exact 为完整相对 path；prefix 为目录前缀（如 ServerProbe/）。
	// size 取 255（而非 512）：本字段在 uk_ignore_rule 唯一键内，512 在 utf8mb4 下达 2048 字节，
	// 连同其余键列超 MySQL 3072 字节上限致建表失败（架构不变量#4 可移植）；plugins 下相对 path/前缀远短于 255。
	Pattern string `gorm:"column:pattern;size:255;not null;uniqueIndex:uk_ignore_rule,priority:6"`
	// 备注（如"运行时指标垃圾"），无敏感内容
	Comment string `gorm:"column:comment;size:512"`
	// 建规则操作者（admin 认证身份，非手填）
	Operator string `gorm:"column:operator;size:128;not null"`
	// 创建时间（UTC）
	CreatedAt time.Time
	// 更新时间（UTC）
	UpdatedAt time.Time
	// 软删时间；未删为哨兵值，纳入唯一键允许同标识取消后再建（沿 SoftDeleteSentinel 范式）
	DeletedAt time.Time `gorm:"column:deleted_at;not null;uniqueIndex:uk_ignore_rule,priority:7"`
}

// TableName 固定表名为 reverse_fetch_ignore_rule。
func (ReverseFetchIgnoreRule) TableName() string { return "reverse_fetch_ignore_rule" }

// BeforeCreate 在插入前为未删记录写入软删哨兵值（与 server_drain 软删哨兵同源范式）。
func (r *ReverseFetchIgnoreRule) BeforeCreate(*gorm.DB) error {
	if r.DeletedAt.IsZero() {
		r.DeletedAt = SoftDeleteSentinel
	}
	return nil
}
