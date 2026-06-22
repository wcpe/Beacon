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
	// ErrInstanceNotFound 实例不存在。
	ErrInstanceNotFound = New(http.StatusNotFound, "INSTANCE_NOT_FOUND", "实例不存在")
	// ErrAssignmentNotFound zone 指派不存在。
	ErrAssignmentNotFound = New(http.StatusNotFound, "ASSIGNMENT_NOT_FOUND", "zone 指派不存在")
	// ErrZoneNotAssignableToBC zone 仅供 bukkit 子服归派，不可分配给 BC 代理实例（FR-8/FR-35）。
	ErrZoneNotAssignableToBC = New(http.StatusBadRequest, "ZONE_NOT_ASSIGNABLE_TO_BC", "zone 不可分配给 BC 代理实例")
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
)
