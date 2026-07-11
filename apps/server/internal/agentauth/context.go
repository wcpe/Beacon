// Package agentauth 承载已鉴权 v2 agent 身份在请求 context 中的传递（FR-144，见 §4.2/§5.1）。
// 它是叶子包（不依赖其它内部包），供 server 中间件注入、handler 读取，避免反向依赖成环。
package agentauth

import "context"

// ctxKey 是本包私有的 context key 类型，避免键碰撞。
type ctxKey int

const identityKey ctxKey = iota

// Identity 是一条已鉴权 v2 agent 身份的权威绑定（取自 agent_identity 表，非请求体自报）。
// 指标 / 调度端点据此把上报数据归属到权威 namespace / serverId / kind，绝不信任请求体自报身份。
type Identity struct {
	NamespaceID uint   // 归属 namespace 主键
	Namespace   string // namespace code（展示 / 日志用）
	ServerID    string // namespace 内唯一 serverId
	Kind        string // proxy / backend
	IdentityID  string // agent 身份 UUID
}

// WithIdentity 把已鉴权 agent 身份放入 context（由 agent v2 鉴权中间件调用）。
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

// FromContext 从 context 取出已鉴权 agent 身份；不存在返回 (零值, false)。
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey).(Identity)
	return id, ok
}
