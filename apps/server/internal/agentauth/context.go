// Package agentauth 承载已鉴权 v2 agent 身份在请求 context 中的传递（FR-144，见 §4.2/§5.1）。
// 它是叶子包（不依赖其它内部包），供 server 中间件注入、handler 读取，避免反向依赖成环。
package agentauth

import "context"

// ctxKey 是本包私有的 context key 类型，避免键碰撞。
type ctxKey int

const (
	identityKey ctxKey = iota
	// trustedInternalKey 是「受信内部调用方」标记的 context key（FR-222）。
	trustedInternalKey
)

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

// WithTrustedInternal 标记本请求来自受信内部调用方（FR-222，见 specs/internal-trust-channel.md §3.3）：
// 由 agentTokenMiddleware 在命中 X-Beacon-Token 共享 token 分支时注入。
//
// 判定依据是中间件的 token 比对结果，**绝不由请求体字段推导**——调用方无法通过伪造请求体
// 把自己标成受信内部调用方；agent 自持身份（X-Beacon-Identity/Boot）分支不带本标记。
func WithTrustedInternal(ctx context.Context) context.Context {
	return context.WithValue(ctx, trustedInternalKey, true)
}

// IsTrustedInternal 报告本请求是否来自受信内部调用方（未标记即 false）。
func IsTrustedInternal(ctx context.Context) bool {
	marked, _ := ctx.Value(trustedInternalKey).(bool)
	return marked
}
