package auth

import "context"

// ctxKey 是本包私有的 context key 类型，避免键碰撞。
type ctxKey int

const (
	operatorKey ctxKey = iota
	roleKey
	principalKey
	clientIPKey
)

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

// WithRole 把认证后的角色（full / readonly）放入 context（由鉴权中间件调用）。
// 角色用 model.RoleFull / model.RoleReadonly 的字符串值，本叶子包不反向依赖 model。
func WithRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, roleKey, role)
}

// Role 从 context 取出认证角色；不存在返回空串。
// 只读拒写由 server 中间件据此统一裁决，handler 不碰角色。
func Role(ctx context.Context) string {
	if v, ok := ctx.Value(roleKey).(string); ok {
		return v
	}
	return ""
}

// WithClientIP 把认证请求的客户端地址放入 context（由 MCP 入口在注入主体时一并注入，FR-240 §3.1）。
//
// 地址是**观测字段而非安全边界**（受信反代以 X-Forwarded-For 首段注入；直连部署下可能为空，空串照记）。
func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey, ip)
}

// ClientIPFromContext 从 context 取出客户端地址；不存在返回空串。
func ClientIPFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(clientIPKey).(string); ok {
		return v
	}
	return ""
}
