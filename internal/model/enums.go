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

// 管理面角色（落 VARCHAR + 应用层校验，不绑方言；FR-42，见 ADR-0026）。
const (
	RoleFull     = "full"     // 读写：等同现操作者，可访问全部 admin 端点
	RoleReadonly = "readonly" // 只读：仅可访问读端点（GET），任何写端点一律 403
)

// IsValidRole 校验角色取值。
func IsValidRole(role string) bool {
	switch role {
	case RoleFull, RoleReadonly:
		return true
	default:
		return false
	}
}

// agent 命令类型（FR-39，落 VARCHAR + 应用层校验；本期仅反向抓取 plugins，见 ADR-0027）。
const (
	CommandTypeIngestPlugins = "ingest-plugins"
)

// agent 命令生命周期状态（FR-39）。
const (
	CommandStatusPending = "pending" // 已建、待目标 agent 拉取
	CommandStatusFetched = "fetched" // 已被 agent 拉取、执行中
	CommandStatusDone    = "done"    // 回传并 ingest 成功
	CommandStatusFailed  = "failed"  // 执行 / 回传 / 入库失败
	CommandStatusExpired = "expired" // 超时未完成（agent 离线等）
)

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
	// 小区默认入口（FR-48，每 zone 唯一指定默认入口 serverId，供 BC 设默认/fallback 服）
	ActionZoneSetDefaultEntry   = "zone.set-default-entry"
	ActionZoneClearDefaultEntry = "zone.clear-default-entry"
	ActionFileCreate        = "file.create"
	ActionFilePublish       = "file.publish"
	ActionFileRollback      = "file.rollback"
	ActionFileDelete        = "file.delete"
	// 配置导入（FR-38，通道B 之上批量上传整文件到组，一次导入记一条审计）
	ActionFileImport = "file.import"
	// 在线实例反向抓取触发（FR-39，见 ADR-0027；ingest 落盘复用上面的 file.import 审计）
	ActionFileReverseFetch = "file.reverse-fetch"
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
	// 管理面 API 密钥（FR-42，运行时创建/吊销/重置，明文不入审计 detail，见 ADR-0026）
	ActionAPIKeyCreate = "apikey.create"
	ActionAPIKeyRevoke = "apikey.revoke"
	ActionAPIKeyReset  = "apikey.reset"
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
	// 管理面 API 密钥的审计对象类型
	TargetTypeAPIKey = "apikey"
	// agent 命令（FR-39 反向抓取）的审计对象类型
	TargetTypeCommand = "command"
)

// OverrideModeFileOverride 是覆盖集模式的唯一取值（落 VARCHAR；FR-15 锁死为"文件覆盖"，
// 不开放 jar 替换 / 进程重启等 P3 发布编排能力，见 ADR-0011 决策 2）。
const OverrideModeFileOverride = "file-override"

// 审计结果。
const (
	ResultOK   = "ok"
	ResultFail = "fail"
)
