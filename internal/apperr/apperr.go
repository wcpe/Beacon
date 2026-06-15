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
)
