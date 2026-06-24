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

// agent 命令类型（落 VARCHAR + 应用层校验，见 ADR-0027 / ADR-0040）。
// FR-46 拓印复用 ingest-plugins 类型（agent 零改动、仍读整棵 plugins 树回传），落库 vs 转存待审由载荷 mode 区分。
const (
	CommandTypeIngestPlugins = "ingest-plugins"
	// CommandTypeTailLogs 取日志：令 agent 读自身日志环形缓冲快照回传（FR-88，见 ADR-0040；不读任意磁盘文件）。
	CommandTypeTailLogs = "tail-logs"
)

// agent 命令载荷 mode（FR-46 / FR-58）：区分 FR-39 直接落库、FR-46 拓印转存待审、FR-58 两段式扫描 / 提交。
const (
	IngestModeLand    = "land"    // 直接 ingest 落库（FR-39，空值同义，向后兼容）
	IngestModeImprint = "imprint" // 拓印转存待审，不落库、待单人自审确认（FR-46）
	IngestModeScan    = "scan"    // 受管任务两段式·扫描：agent 只列元信息清单、不读内容、永不失败（FR-58）
	IngestModeSubmit  = "submit"  // 受管任务两段式·提交：agent 仅读选定 path 子集内容回传（FR-58）
)

// 反向抓取受管任务生命周期状态（FR-58，落 VARCHAR + 应用层校验，见 ADR-0037）。
// 非终态（scanning/pending-review/fetching/ingesting）受单实例互斥约束；旁出 failed/cancelled/expired 为终态。
const (
	ReverseFetchTaskScanning       = "scanning"        // 已下发 scan 命令、待 agent 回清单
	ReverseFetchTaskPendingReview  = "pending-review"  // 清单已到、待人工审核选定
	ReverseFetchTaskFetching       = "fetching"        // 已下发 submit 命令、待 agent 回选定内容
	ReverseFetchTaskConflictReview = "conflict-review" // 选定内容已到但与目标已有版本冲突、暂存待人工 diff 确认（FR-59）
	ReverseFetchTaskIngesting      = "ingesting"       // 选定内容已到、落库中
	ReverseFetchTaskDone           = "done"            // 选定集 ingest 落库成功（终态）
	ReverseFetchTaskFailed         = "failed"          // 任一阶段失败（终态）
	ReverseFetchTaskCancelled      = "cancelled"       // 人工取消（终态）
	ReverseFetchTaskExpired        = "expired"         // 超时未完成，清单瞬态已清空（终态）
)

// IsReverseFetchTaskTerminal 判断任务状态是否为终态（终态不受互斥约束、不可再迁移）。
func IsReverseFetchTaskTerminal(status string) bool {
	switch status {
	case ReverseFetchTaskDone, ReverseFetchTaskFailed, ReverseFetchTaskCancelled, ReverseFetchTaskExpired:
		return true
	default:
		return false
	}
}

// agent 命令生命周期状态（FR-39 / FR-46）。
const (
	CommandStatusPending = "pending" // 已建、待目标 agent 拉取
	CommandStatusFetched = "fetched" // 已被 agent 拉取、执行中
	CommandStatusReady   = "ready"   // 拓印已抓取、待单人自审确认（FR-46，仅 imprint 模式）
	CommandStatusDone    = "done"    // 回传并 ingest 成功 / 拓印确认落库成功
	CommandStatusFailed  = "failed"  // 执行 / 回传 / 入库失败
	CommandStatusExpired = "expired" // 超时未完成（agent 离线等）
)

// 审计动作（动词点分命名）。
const (
	ActionConfigCreate   = "config.create"
	ActionConfigPublish  = "config.publish"
	ActionConfigRollback = "config.rollback"
	ActionConfigDelete   = "config.delete"
	// 批量禁用 / 启用（FR-74）：批量置 enabled，单事务内逐项各记一条
	ActionConfigDisable = "config.disable"
	ActionConfigEnable  = "config.enable"
	// 配置灰度 / Beta（FR-9，cohort 名单按版本选择层叠加，见 ADR-0021）
	ActionConfigGrayPublish = "config.gray-publish"
	ActionConfigGrayPromote = "config.gray-promote"
	ActionConfigGrayAbort   = "config.gray-abort"
	ActionInstanceRegister  = "instance.register"
	ActionInstanceOffline   = "instance.offline"
	// 取消主动下线（FR-49，清除 server_offline 拒绝态使实例可重新接入）
	ActionInstanceOnline = "instance.online"
	ActionZoneAssign     = "zone.assign"
	ActionZoneMove       = "zone.move"
	ActionZoneUnassign   = "zone.unassign"
	// 小区默认入口（FR-48，每 zone 唯一指定默认入口 serverId，供 BC 设默认/fallback 服）
	ActionZoneSetDefaultEntry   = "zone.set-default-entry"
	ActionZoneClearDefaultEntry = "zone.clear-default-entry"
	ActionFileCreate            = "file.create"
	ActionFilePublish           = "file.publish"
	ActionFileRollback          = "file.rollback"
	ActionFileDelete            = "file.delete"
	// 批量禁用 / 启用（FR-74）：批量置 enabled，单事务内逐项各记一条
	ActionFileDisable = "file.disable"
	ActionFileEnable  = "file.enable"
	// 配置导入（FR-38，通道B 之上批量上传整文件到组，一次导入记一条审计）
	ActionFileImport = "file.import"
	// 在线实例反向抓取触发（FR-39，见 ADR-0027；ingest 落盘复用上面的 file.import 审计）
	ActionFileReverseFetch = "file.reverse-fetch"
	// 反向抓取受管任务·扫描（FR-58）：建任务并下发 scan 命令，令在线实例只回元信息清单（见 ADR-0037）
	ActionFileReverseFetchScan = "file.reverse-fetch-scan"
	// 反向抓取受管任务·提交（FR-58）：审核选定后下发 submit 命令，令实例仅回选定 path 内容
	ActionFileReverseFetchSubmit = "file.reverse-fetch-submit"
	// 反向抓取受管任务·入库（FR-58）：选定内容到、复用 FileService.Import 落库（detail 不含文件内容）
	ActionFileReverseFetchIngest = "file.reverse-fetch-ingest"
	// 反向抓取受管任务·取消（FR-58）：人工取消非终态任务
	ActionFileReverseFetchCancel = "file.reverse-fetch-cancel"
	// 反向抓取受管任务·错误回传（FR-87）：agent 执行 scan/submit 读盘失败回传错误，任务转 failed 记 lastError（detail 不含文件内容）
	ActionFileReverseFetchError = "file.reverse-fetch-error"
	// 反向抓取持久忽略规则·新增 / 删除（FR-59）：登记 / 撤销下次扫描该排除的文件 / 目录
	ActionReverseFetchIgnoreRuleAdd    = "reverse-fetch.ignore-rule-add"
	ActionReverseFetchIgnoreRuleRemove = "reverse-fetch.ignore-rule-remove"
	// 按需拓印触发（FR-46）：命令在线实例回传某文件磁盘内容、转存待审（不落库）
	ActionFileImprintFetch = "file.imprint-fetch"
	// 按需拓印确认落库（FR-46）：单人自审通过后落为某层文件覆盖（detail 不含文件内容）
	ActionFileImprint = "file.imprint"
	// 取 agent 日志（FR-88，见 ADR-0040）：admin 触发命令在线实例回传自身脱敏日志（detail 仅 commandId/serverId，不含日志内容）
	ActionInstanceTailLogs = "instance.tail-logs"
	// 三方插件文件覆盖兼容（FR-15，通道B 之上叠备份 + 受限重载命令，见 ADR-0011）
	ActionOverrideSetCreate   = "override-set.create"
	ActionOverrideSetPublish  = "override-set.publish"
	ActionOverrideSetRollback = "override-set.rollback"
	ActionOverrideSetDelete   = "override-set.delete"
	// 流量调度（FR-10，drain 排空 / 维护标记，见 ADR-0017）
	ActionSchedulingDrain   = "scheduling.drain"
	ActionSchedulingUndrain = "scheduling.undrain"
	// 环境（namespace）写操作（FR-7/FR-30；改名 / 删除补全见 FR-53）
	ActionNamespaceCreate = "namespace.create"
	ActionNamespaceUpdate = "namespace.update"
	ActionNamespaceDelete = "namespace.delete"
	// 管理面登录 / 登出（FR-7/FR-30，operator 取认证身份，detail 不含口令 / 令牌）
	ActionAuthLogin  = "auth.login"
	ActionAuthLogout = "auth.logout"
	// 管理面 API 密钥（FR-42，运行时创建/吊销/重置，明文不入审计 detail，见 ADR-0026）
	ActionAPIKeyCreate = "apikey.create"
	ActionAPIKeyRevoke = "apikey.revoke"
	ActionAPIKeyReset  = "apikey.reset"
	// 运维设置更新（FR-61，热改项真源由 config.yml 移到 DB store，detail 仅记 key + 新值、绝不含密钥，见 ADR-0038）
	ActionSettingsUpdate = "settings.update"
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
	// 反向抓取受管任务（FR-58）的审计对象类型
	TargetTypeReverseFetchTask = "reverse-fetch-task"
	// 反向抓取持久忽略规则（FR-59）的审计对象类型
	TargetTypeReverseFetchIgnoreRule = "reverse-fetch-ignore-rule"
	// 运维设置（FR-61）的审计对象类型
	TargetTypeSettings = "settings"
)

// OverrideModeFileOverride 是覆盖集模式的唯一取值（落 VARCHAR；FR-15 锁死为"文件覆盖"，
// 不开放 jar 替换 / 进程重启等 P3 发布编排能力，见 ADR-0011 决策 2）。
const OverrideModeFileOverride = "file-override"

// 审计结果。
const (
	ResultOK   = "ok"
	ResultFail = "fail"
)

// 告警事件类型（FR-89，落 VARCHAR + 应用层校验，见 ADR-0041）。
// 当前唯一真实触发点为健康流转；publish-fail / backend-unreachable 预置为枚举取值，
// 供将来在对应发生处接入告警扇出时复用，本期不凭空制造这两类触发（守范围纪律）。
const (
	AlertEventTypeHealthTransition   = "health-transition"   // 实例健康状态异常转移（degraded/lost/offline）
	AlertEventTypePublishFail        = "publish-fail"        // 配置/文件发布失败（预留枚举，当前无触发点）
	AlertEventTypeBackendUnreachable = "backend-unreachable" // 后端不可达（预留枚举，当前无触发点）
)

// 告警事件严重级别（FR-89，落 VARCHAR + 应用层校验，见 ADR-0041）。
const (
	AlertLevelInfo     = "info"
	AlertLevelWarning  = "warning"
	AlertLevelCritical = "critical"
)
