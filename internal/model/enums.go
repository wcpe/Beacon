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
	// 配置灰度 / Beta（FR-9，cohort 名单按版本选择层叠加，见 ADR-0021）
	ActionConfigGrayPublish = "config.gray-publish"
	ActionConfigGrayPromote = "config.gray-promote"
	ActionConfigGrayAbort   = "config.gray-abort"
	ActionInstanceRegister  = "instance.register"
	ActionInstanceOffline   = "instance.offline"
	ActionZoneAssign        = "zone.assign"
	ActionZoneMove          = "zone.move"
	ActionZoneUnassign      = "zone.unassign"
	ActionFileCreate        = "file.create"
	ActionFilePublish       = "file.publish"
	ActionFileRollback      = "file.rollback"
	ActionFileDelete        = "file.delete"
	// 配置导入（FR-38，通道B 之上批量上传整文件到组，一次导入记一条审计）
	ActionFileImport = "file.import"
	// 三方插件文件覆盖兼容（FR-15，通道B 之上叠备份 + 受限重载命令，见 ADR-0011）
	ActionOverrideSetCreate   = "override-set.create"
	ActionOverrideSetPublish  = "override-set.publish"
	ActionOverrideSetRollback = "override-set.rollback"
	ActionOverrideSetDelete   = "override-set.delete"
	// 流量调度（FR-10，drain 排空 / 维护标记，见 ADR-0017）
	ActionSchedulingDrain   = "scheduling.drain"
	ActionSchedulingUndrain = "scheduling.undrain"
	// 环境（namespace）写操作（FR-7/FR-30）
	ActionNamespaceCreate = "namespace.create"
	// 管理面登录 / 登出（FR-7/FR-30，operator 取认证身份，detail 不含口令 / 令牌）
	ActionAuthLogin  = "auth.login"
	ActionAuthLogout = "auth.logout"
)

// 审计对象类型。
const (
	TargetTypeConfig      = "config"
	TargetTypeInstance    = "instance"
	TargetTypeZone        = "zone"
	TargetTypeFile        = "file"
	TargetTypeOverrideSet = "override-set"
	TargetTypeNamespace   = "namespace"
	// 认证会话（登录 / 登出）的审计对象类型
	TargetTypeAuth = "auth"
)

// OverrideModeFileOverride 是覆盖集模式的唯一取值（落 VARCHAR；FR-15 锁死为"文件覆盖"，
// 不开放 jar 替换 / 进程重启等 P3 发布编排能力，见 ADR-0011 决策 2）。
const OverrideModeFileOverride = "file-override"

// 审计结果。
const (
	ResultOK   = "ok"
	ResultFail = "fail"
)
