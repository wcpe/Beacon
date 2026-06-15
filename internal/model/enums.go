package model

// 覆盖层枚举（落 VARCHAR + 应用层校验，不绑方言）。
const (
	ScopeGlobal = "global" // 全局层：跨大区默认
	ScopeGroup  = "group"  // 大区层
	ScopeZone   = "zone"   // 小区层
	ScopeServer = "server" // 子服层
)

// GlobalGroupCode 是 global 层 group_code 的占位保留字。
const GlobalGroupCode = "__GLOBAL__"

// IsValidScopeLevel 校验覆盖层取值。
func IsValidScopeLevel(level string) bool {
	switch level {
	case ScopeGlobal, ScopeGroup, ScopeZone, ScopeServer:
		return true
	default:
		return false
	}
}

// 审计动作（动词点分命名）。
const (
	ActionConfigCreate   = "config.create"
	ActionConfigPublish  = "config.publish"
	ActionConfigRollback = "config.rollback"
	ActionConfigDelete   = "config.delete"
)

// 审计对象类型。
const (
	TargetTypeConfig = "config"
)

// 审计结果。
const (
	ResultOK   = "ok"
	ResultFail = "fail"
)
