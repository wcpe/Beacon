package auth

import "context"

// ctxKey 是本包私有的 context key 类型，避免键碰撞。
type ctxKey int

const operatorKey ctxKey = iota

// WithOperator 把认证后的操作者身份放入 context（由鉴权中间件调用）。
func WithOperator(ctx context.Context, operator string) context.Context {
	return context.WithValue(ctx, operatorKey, operator)
}

// Operator 从 context 取出认证操作者身份；不存在返回空串。
// 写操作处理器据此把 operator 入审计，取代前端手填值。
func Operator(ctx context.Context) string {
	if v, ok := ctx.Value(operatorKey).(string); ok {
		return v
	}
	return ""
}
