// Package render 提供 HTTP 响应与统一错误体的写出，以及 traceId 的上下文读写。
// 它被 handler 与 server 中间件共用，作为叶子设施避免两者相互依赖成环。
package render

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"beacon/internal/apperr"
)

// ctxKey 是本包私有的 context key 类型，避免键碰撞。
type ctxKey int

const traceIDKey ctxKey = iota

// WithTraceID 把 traceId 放入 context。
func WithTraceID(ctx context.Context, tid string) context.Context {
	return context.WithValue(ctx, traceIDKey, tid)
}

// TraceID 从 context 取出 traceId，不存在返回空串。
func TraceID(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey).(string); ok {
		return v
	}
	return ""
}

// errorBody 是统一错误响应体 {code, message, traceId}。
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	TraceID string `json:"traceId,omitempty"`
}

// WriteJSON 以 application/json 写出成功响应。
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("写出响应失败", "错误", err)
	}
}

// WriteError 把错误转换为统一错误体；非业务错误按 500 处理并记录日志。
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	tid := TraceID(r.Context())
	var ae *apperr.Error
	if errors.As(err, &ae) {
		WriteJSON(w, ae.Status, errorBody{Code: ae.Code, Message: ae.Message, TraceID: tid})
		return
	}
	slog.Error("内部错误", "路径", r.URL.Path, "traceId", tid, "错误", err)
	WriteJSON(w, http.StatusInternalServerError, errorBody{Code: "INTERNAL", Message: "内部错误", TraceID: tid})
}
