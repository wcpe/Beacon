// Package apperr 定义带业务码与 HTTP 状态的领域错误。
// 由 service 层产生、handler 层经 render 统一转换为对外错误响应体。
// 它是叶子包（不依赖其它内部包），供各层共用，避免反向依赖。
package apperr

import "net/http"

// Error 是带业务码与 HTTP 状态的领域错误。
type Error struct {
	// 业务码，如 NAMESPACE_CONFLICT
	Code string
	// 面向调用方的中文说明
	Message string
	// 对应的 HTTP 状态码
	Status int
}

// Error 实现 error 接口。
func (e *Error) Error() string { return e.Code + ": " + e.Message }

// New 构造一个业务错误。
func New(status int, code, message string) *Error {
	return &Error{Code: code, Message: message, Status: status}
}

// 预定义业务错误（按需新增，不预留未使用项）。
var (
	// ErrInvalidParam 参数错误。
	ErrInvalidParam = New(http.StatusBadRequest, "INVALID_PARAM", "参数错误")
	// ErrNamespaceConflict 同名环境已存在。
	ErrNamespaceConflict = New(http.StatusConflict, "NAMESPACE_CONFLICT", "同名环境已存在")
	// ErrNamespaceNotFound 环境不存在（改名 / 删除目标缺失，FR-53）。
	ErrNamespaceNotFound = New(http.StatusNotFound, "NAMESPACE_NOT_FOUND", "环境不存在")
	// ErrNamespaceHasInstances 环境下仍有已注册实例，禁删（FR-53 删除守卫①）。
	ErrNamespaceHasInstances = New(http.StatusConflict, "NAMESPACE_HAS_INSTANCES", "环境下仍有已注册实例，请先下线后再删除")
	// ErrNamespaceHasAssignments 环境下仍有已指派 zone，禁删（FR-53 删除守卫②）。
	ErrNamespaceHasAssignments = New(http.StatusConflict, "NAMESPACE_HAS_ASSIGNMENTS", "环境下仍有已指派的 zone，请先取消指派后再删除")
	// ErrNamespaceHasConfigs 环境下仍有配置项，禁删（FR-53 删除守卫③）。
	ErrNamespaceHasConfigs = New(http.StatusConflict, "NAMESPACE_HAS_CONFIGS", "环境下仍有配置，请先删除配置后再删除")
	// ErrNamespaceHasFiles 环境下仍有文件树（通道B），禁删（FR-53 删除守卫④）。
	ErrNamespaceHasFiles = New(http.StatusConflict, "NAMESPACE_HAS_FILES", "环境下仍有文件树，请先删除文件后再删除")
	// ErrNamespaceHasOverrideSets 环境下仍有覆盖集（FR-15），禁删（FR-53 删除守卫⑤）。
	ErrNamespaceHasOverrideSets = New(http.StatusConflict, "NAMESPACE_HAS_OVERRIDE_SETS", "环境下仍有覆盖集，请先删除覆盖集后再删除")

	// ErrInvalidScope 覆盖层或其目标键不合法。
	ErrInvalidScope = New(http.StatusBadRequest, "INVALID_SCOPE", "覆盖层或目标键不合法")
	// ErrConfigNotFound 配置项不存在。
	ErrConfigNotFound = New(http.StatusNotFound, "CONFIG_NOT_FOUND", "配置项不存在")
	// ErrRevisionNotFound 回滚目标版本不存在。
	ErrRevisionNotFound = New(http.StatusNotFound, "REVISION_NOT_FOUND", "目标版本不存在")
	// ErrConfigConflict 同标识配置项已存在。
	ErrConfigConflict = New(http.StatusConflict, "CONFIG_CONFLICT", "同标识配置项已存在")
	// ErrContentTooLarge 内容超出大小上限。
	ErrContentTooLarge = New(http.StatusUnprocessableEntity, "CONTENT_TOO_LARGE", "配置内容超出大小上限")
	// ErrContentInvalid 内容按声明格式解析失败。
	ErrContentInvalid = New(http.StatusUnprocessableEntity, "CONTENT_INVALID", "配置内容解析失败")
	// ErrContentSchemaInvalid 内容结构 / 类型 / 必填项校验不通过（FR-27，发布前拦截坏配置）。
	ErrContentSchemaInvalid = New(http.StatusUnprocessableEntity, "CONTENT_SCHEMA_INVALID", "配置结构或类型校验不通过")
	// ErrFormatInconsistent 同一 dataId 跨层格式不一致。
	ErrFormatInconsistent = New(http.StatusUnprocessableEntity, "FORMAT_INCONSISTENT", "同一 dataId 跨层格式不一致")
	// ErrGrayNotFound 灰度不存在（promote/abort 时无活跃灰度，FR-9）。
	ErrGrayNotFound = New(http.StatusNotFound, "GRAY_NOT_FOUND", "灰度不存在")
	// ErrEmptyCohort 灰度 cohort 名单为空（无意义灰度，FR-9）。
	ErrEmptyCohort = New(http.StatusBadRequest, "EMPTY_COHORT", "灰度 cohort 名单为空")

	// ErrFileNotFound 文件对象不存在（通道B）。
	ErrFileNotFound = New(http.StatusNotFound, "FILE_NOT_FOUND", "文件对象不存在")
	// ErrFileConflict 同标识文件对象已存在（通道B）。
	ErrFileConflict = New(http.StatusConflict, "FILE_CONFLICT", "同标识文件对象已存在")
	// ErrInvalidPath 文件相对 path 不合法（通道B）。
	ErrInvalidPath = New(http.StatusBadRequest, "INVALID_PATH", "文件路径不合法")
	// ErrTooManyFiles 单次导入文件数超出上限（FR-38）。
	ErrTooManyFiles = New(http.StatusUnprocessableEntity, "TOO_MANY_FILES", "单次导入文件数超出上限")
	// ErrCommandNotFound agent 命令不存在或已不可回传（过期 / 已完成 / 状态不符，FR-39）。
	ErrCommandNotFound = New(http.StatusNotFound, "COMMAND_NOT_FOUND", "命令不存在或已失效")
	// ErrImprintNotReady 拓印命令非 ready 态，不可 diff / confirm（FR-46）。
	ErrImprintNotReady = New(http.StatusConflict, "IMPRINT_NOT_READY", "拓印命令未就绪")
	// ErrImprintReviewMismatch 拓印确认自审 md5 与已抓取内容不符（强制看过 diff，FR-46）。
	ErrImprintReviewMismatch = New(http.StatusPreconditionFailed, "IMPRINT_REVIEW_MISMATCH", "拓印自审内容已变更，请重新查看 diff")
	// ErrAgentLogActive 该实例已有进行中的取日志命令，限速拒新建（FR-88，见 ADR-0040）。
	ErrAgentLogActive = New(http.StatusConflict, "AGENT_LOG_ACTIVE", "该实例已有进行中的取日志请求，请稍候")
	// ErrBrowseTimeout 文件浏览等待 agent 回传超时（agent 离线 / 未及时回传，FR-110，见 ADR-0049）。
	ErrBrowseTimeout = New(http.StatusGatewayTimeout, "BROWSE_TIMEOUT", "目标实例未在限期内返回浏览结果")
	// ErrBrowseTargetNotFound 文件浏览目标越权 / 非目录 / 非文本（agent 原语拒读，FR-110）。
	ErrBrowseTargetNotFound = New(http.StatusNotFound, "BROWSE_TARGET_NOT_FOUND", "浏览目标不存在或不可读")

	// ErrReverseFetchTaskNotFound 反向抓取受管任务不存在（FR-58）。
	ErrReverseFetchTaskNotFound = New(http.StatusNotFound, "REVERSE_FETCH_TASK_NOT_FOUND", "反向抓取任务不存在")
	// ErrReverseFetchTaskActive 该实例已有活跃（非终态）反向抓取任务，单实例互斥拒新建（FR-58，见 ADR-0037）。
	ErrReverseFetchTaskActive = New(http.StatusConflict, "REVERSE_FETCH_TASK_ACTIVE", "该实例已有活跃反向抓取任务，请先完成或取消")
	// ErrReverseFetchTaskState 反向抓取任务状态不符（当前态不允许该操作 / 已被并发终结，FR-58）。
	ErrReverseFetchTaskState = New(http.StatusConflict, "REVERSE_FETCH_TASK_STATE", "反向抓取任务状态不允许该操作")
	// ErrOverThresholdNotConfirmed 选定集含超单文件阈值的文件但未显式确认（只拒该文件，不拒整批，FR-58）。
	ErrOverThresholdNotConfirmed = New(http.StatusBadRequest, "OVER_THRESHOLD_NOT_CONFIRMED", "选定集含超阈值文件，须显式确认才能纳入")
	// ErrReverseFetchReviewMismatch 冲突审核确认覆盖时自审 md5 与抓取内容不符（强制看过 diff，盲确认拒，FR-59）。
	ErrReverseFetchReviewMismatch = New(http.StatusPreconditionFailed, "REVERSE_FETCH_REVIEW_MISMATCH", "冲突审核自审内容已变更，请重新查看 diff")
	// ErrReverseFetchConflictNotFound 请求的冲突 path 不在本任务冲突集内（diff / resolve 目标缺失，FR-59）。
	ErrReverseFetchConflictNotFound = New(http.StatusNotFound, "REVERSE_FETCH_CONFLICT_NOT_FOUND", "冲突文件不存在")

	// ErrOverrideSetNotFound 覆盖集不存在（FR-15）。
	ErrOverrideSetNotFound = New(http.StatusNotFound, "OVERRIDE_SET_NOT_FOUND", "覆盖集不存在")
	// ErrOverrideSetConflict 同标识覆盖集已存在（FR-15）。
	ErrOverrideSetConflict = New(http.StatusConflict, "OVERRIDE_SET_CONFLICT", "同标识覆盖集已存在")
	// ErrInvalidTargetRoot 覆盖集目标根目录不合法（FR-15，见 ADR-0011 决策 4）。
	ErrInvalidTargetRoot = New(http.StatusBadRequest, "INVALID_TARGET_ROOT", "目标根目录不合法")
	// ErrInvalidReloadCommand 重载命令不合法（含元字符 / 多条 / 越限等，FR-15，见 ADR-0011 决策 3）。
	ErrInvalidReloadCommand = New(http.StatusBadRequest, "INVALID_RELOAD_COMMAND", "重载命令不合法")

	// ErrUnauthorized agent 端缺少或错误的 token。
	ErrUnauthorized = New(http.StatusUnauthorized, "UNAUTHORIZED", "缺少或非法的 token")
	// ErrBadCredentials 管理台登录用户名或口令错误。
	ErrBadCredentials = New(http.StatusUnauthorized, "BAD_CREDENTIALS", "用户名或口令错误")
	// ErrAdminUnauthorized 管理台缺少或非法的登录令牌 / API 密钥（含过期 / 已吊销）。
	ErrAdminUnauthorized = New(http.StatusUnauthorized, "ADMIN_UNAUTHORIZED", "缺少或非法的登录令牌")
	// ErrForbidden 已认证但无权执行该操作（只读密钥访问写端点，FR-42，见 ADR-0026）。
	ErrForbidden = New(http.StatusForbidden, "FORBIDDEN", "只读密钥无权执行写操作")
	// ErrAPIKeyNotFound API 密钥不存在（吊销 / 重置目标不存在或已吊销，FR-42）。
	ErrAPIKeyNotFound = New(http.StatusNotFound, "API_KEY_NOT_FOUND", "API 密钥不存在")
	// ErrIdentityRequired 注册缺少必要身份（serverId/namespace）。
	ErrIdentityRequired = New(http.StatusBadRequest, "IDENTITY_REQUIRED", "缺少必要的身份标识")
	// ErrDuplicateServerID 同 serverId 已有仍新鲜的不同地址实例在线。
	ErrDuplicateServerID = New(http.StatusConflict, "DUPLICATE_SERVER_ID", "serverId 冲突：已有不同地址实例在线")
	// ErrNotRegistered 心跳/上报时实例未注册。
	ErrNotRegistered = New(http.StatusNotFound, "NOT_REGISTERED", "实例未注册")
	// ErrStreamingUnsupported 当前 ResponseWriter 不支持流式刷写（无 http.Flusher），无法承载 SSE 推送。
	ErrStreamingUnsupported = New(http.StatusInternalServerError, "STREAMING_UNSUPPORTED", "服务端不支持流式推送")
	// ErrInternal 服务端内部错误（依赖未装配等编程/装配错误的兜底）。
	ErrInternal = New(http.StatusInternalServerError, "INTERNAL", "服务端内部错误")
	// ErrInstanceNotFound 实例不存在。
	ErrInstanceNotFound = New(http.StatusNotFound, "INSTANCE_NOT_FOUND", "实例不存在")
	// ErrAssignmentNotFound zone 指派不存在。
	ErrAssignmentNotFound = New(http.StatusNotFound, "ASSIGNMENT_NOT_FOUND", "zone 指派不存在")
	// ErrZoneNotAssignableToBC zone 仅供 bukkit 子服归派，不可分配给 BC 代理实例（FR-8/FR-35）。
	ErrZoneNotAssignableToBC = New(http.StatusBadRequest, "ZONE_NOT_ASSIGNABLE_TO_BC", "zone 不可分配给 BC 代理实例")
	// ErrZoneServerOnlineNonempty 服务器在线且有玩家，禁止变更其区归属（排空门硬闸，FR-71/ADR-0036）。
	ErrZoneServerOnlineNonempty = New(http.StatusConflict, "ZONE_SERVER_ONLINE_NONEMPTY", "服务器在线且有玩家，禁止变更其区归属；请先排空（drain 或等玩家离开）后再操作")
	// ErrDefaultEntryServerNotInZone 默认入口指向的 serverId 未指派到该 (group, zone)（FR-48）。
	ErrDefaultEntryServerNotInZone = New(http.StatusBadRequest, "DEFAULT_ENTRY_SERVER_NOT_IN_ZONE", "默认入口子服未指派到该小区")
	// ErrDefaultEntryNotFound 清除默认入口时该小区无默认入口（FR-48）。
	ErrDefaultEntryNotFound = New(http.StatusNotFound, "DEFAULT_ENTRY_NOT_FOUND", "该小区未设默认入口")
	// ErrDrainNotFound 取消 drain 时该标记不存在（FR-10）。
	ErrDrainNotFound = New(http.StatusNotFound, "DRAIN_NOT_FOUND", "drain 标记不存在")
	// ErrInstanceOfflineRejected 实例已被主动下线，拒绝其注册接入（FR-49，区别于 NOT_REGISTERED / DUPLICATE_SERVER_ID）。
	ErrInstanceOfflineRejected = New(http.StatusForbidden, "INSTANCE_OFFLINE_REJECTED", "实例已被主动下线，禁止接入")
	// ErrOfflineNotFound 取消下线时该下线标记不存在（FR-49）。
	ErrOfflineNotFound = New(http.StatusNotFound, "OFFLINE_NOT_FOUND", "下线标记不存在")

	// ErrSettingKeyNotAllowed 设置 key 不在热改白名单内（写非白名单 / 启动 / 安全项一律拒，FR-61，见 ADR-0038）。
	ErrSettingKeyNotAllowed = New(http.StatusBadRequest, "SETTING_KEY_NOT_ALLOWED", "设置项不存在或不可热改")
	// ErrSettingValueInvalid 设置值非法（类型 / 范围 / 枚举校验不通过，FR-61）。
	ErrSettingValueInvalid = New(http.StatusBadRequest, "SETTING_VALUE_INVALID", "设置值不合法")

	// ErrReversibleOpNotFound 可逆操作账目不存在（撤回目标缺失，FR-116，见 ADR-0051）。
	ErrReversibleOpNotFound = New(http.StatusNotFound, "REVERSIBLE_OP_NOT_FOUND", "可逆操作不存在")
	// ErrReversibleOpExpired 可逆操作已超过可撤回时间窗，不可撤回（FR-116，ADR-0051 决策 8）。
	ErrReversibleOpExpired = New(http.StatusConflict, "REVERSIBLE_OP_EXPIRED", "该操作已超过可撤回时限，不可撤回")
	// ErrReversibleOpSuperseded 可逆操作已被后续操作覆盖，不可撤回（防脏撤回，FR-116，ADR-0051 决策 8）。
	ErrReversibleOpSuperseded = New(http.StatusConflict, "REVERSIBLE_OP_SUPERSEDED", "该操作已被后续操作覆盖，不可撤回")
	// ErrReversibleOpState 可逆操作状态不符 / 反向快照损坏，不可撤回（FR-116）。
	ErrReversibleOpState = New(http.StatusConflict, "REVERSIBLE_OP_STATE", "可逆操作状态不允许撤回")

	// ErrNoRollbackAvailable 无可回退的上一版本备份（.old 不存在，FR-120）。
	ErrNoRollbackAvailable = New(http.StatusConflict, "NO_ROLLBACK_AVAILABLE", "无可回退的上一版本")
)
