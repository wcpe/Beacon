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
	// ErrFormatInconsistent 同一 dataId 跨层格式不一致。
	ErrFormatInconsistent = New(http.StatusUnprocessableEntity, "FORMAT_INCONSISTENT", "同一 dataId 跨层格式不一致")

	// ErrFileNotFound 文件对象不存在（通道B）。
	ErrFileNotFound = New(http.StatusNotFound, "FILE_NOT_FOUND", "文件对象不存在")
	// ErrFileConflict 同标识文件对象已存在（通道B）。
	ErrFileConflict = New(http.StatusConflict, "FILE_CONFLICT", "同标识文件对象已存在")
	// ErrInvalidPath 文件相对 path 不合法（通道B）。
	ErrInvalidPath = New(http.StatusBadRequest, "INVALID_PATH", "文件路径不合法")

	// ErrUnauthorized agent 端缺少或错误的 token。
	ErrUnauthorized = New(http.StatusUnauthorized, "UNAUTHORIZED", "缺少或非法的 token")
	// ErrBadCredentials 管理台登录用户名或口令错误。
	ErrBadCredentials = New(http.StatusUnauthorized, "BAD_CREDENTIALS", "用户名或口令错误")
	// ErrAdminUnauthorized 管理台缺少或非法的登录令牌。
	ErrAdminUnauthorized = New(http.StatusUnauthorized, "ADMIN_UNAUTHORIZED", "缺少或非法的登录令牌")
	// ErrIdentityRequired 注册缺少必要身份（serverId/namespace）。
	ErrIdentityRequired = New(http.StatusBadRequest, "IDENTITY_REQUIRED", "缺少必要的身份标识")
	// ErrDuplicateServerID 同 serverId 已有仍新鲜的不同地址实例在线。
	ErrDuplicateServerID = New(http.StatusConflict, "DUPLICATE_SERVER_ID", "serverId 冲突：已有不同地址实例在线")
	// ErrNotRegistered 心跳/上报时实例未注册。
	ErrNotRegistered = New(http.StatusNotFound, "NOT_REGISTERED", "实例未注册")
	// ErrInstanceNotFound 实例不存在。
	ErrInstanceNotFound = New(http.StatusNotFound, "INSTANCE_NOT_FOUND", "实例不存在")
	// ErrAssignmentNotFound zone 指派不存在。
	ErrAssignmentNotFound = New(http.StatusNotFound, "ASSIGNMENT_NOT_FOUND", "zone 指派不存在")
)
