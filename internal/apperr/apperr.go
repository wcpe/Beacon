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
)
