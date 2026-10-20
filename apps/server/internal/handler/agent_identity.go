package handler

import (
	"net/http"
	"strings"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/render"
)

// agentReportAuthenticator 以 DB 权威绑定校验 agent 数据面调用者身份（token↔namespace + identity 已确认）。
//
// 为什么 v1 数据面每个端点都必须自己调一次：
// v1 组只挂 agentTokenMiddleware，而它核的是**共享 token**——全集群 agent 共用同一把凭据，
// token 通过只代表「某台已接入的 agent」，不代表「自称的那台」。而 v1 端点的 namespace / serverId
// 一律由请求体或 query 自报、完全可伪造。于是共享 token 之下任意一次请求都能冒充另一台：
// 顶替别人心跳（把别人的实例保活成 online）、读走别人的有效配置与文件正文、把文件写进别的分组、
// 抢走别人待办命令。身份因此只能取自 DB 权威绑定，绝不能信请求自报值。
type agentReportAuthenticator interface {
	AuthenticateAgentReport(token, identityID, bootID, addr string) (agentauth.Identity, error)
}

// authenticateAgentReport 解析调用者的权威身份；失败时已写响应并返回 ok=false。
//
// 状态码沿用 service 的语义，不在此处归并：token / 身份非法 401、身份未确认 403、
// 陈旧 boot 404（促 agent 重注册，喂养并发双实例往复检测）、冲突 / 落败 409。
// 校验器未注入时按 401 失败关闭——宁可拒全部，也不能在缺校验器时静默放行自报身份。
func authenticateAgentReport(w http.ResponseWriter, r *http.Request, authn agentReportAuthenticator) (agentauth.Identity, bool) {
	if authn == nil {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return agentauth.Identity{}, false
	}
	identity, err := authn.AuthenticateAgentReport(
		r.Header.Get("X-Beacon-Token"),
		r.Header.Get("X-Beacon-Identity"),
		r.Header.Get("X-Beacon-Boot"),
		r.RemoteAddr,
	)
	if err != nil {
		render.WriteError(w, r, err)
		return agentauth.Identity{}, false
	}
	return identity, true
}

// resolveAgentIdentity 是 authenticateAgentReport 的不写响应版本，供「身份可选」的引导端点自行决定拒绝方式。
//
// 只用于注册端点：该端点在**共享 token 的机器注册通道**下可能确实尚无身份（引导阶段），
// 不能像数据面端点那样无条件要求身份；但经 v2 身份分支到达的请求身份是可校验的（见 Register 注释），
// 此时必须比对归属，否则同一把 namespace token 仍可把拓扑注册进任意环境。
// 校验器未注入时返回 ok=false，由调用方按 fail-closed 处理。
func resolveAgentIdentity(r *http.Request, authn agentReportAuthenticator) (agentauth.Identity, bool) {
	if authn == nil {
		return agentauth.Identity{}, false
	}
	identity, err := authn.AuthenticateAgentReport(
		r.Header.Get("X-Beacon-Token"),
		r.Header.Get("X-Beacon-Identity"),
		r.Header.Get("X-Beacon-Boot"),
		r.RemoteAddr,
	)
	if err != nil {
		return agentauth.Identity{}, false
	}
	return identity, true
}

// bindAgentTarget 校验请求自报的 namespace / serverId 与权威身份一致，并回传后续应当使用的归属值。
//
// 只做**比对**、不做回填：缺参的失败口径仍由各端点原有校验负责（INVALID_PARAM / IDENTITY_REQUIRED 等），
// 在这里补默认值等于悄悄放宽入参契约——把「agent 必须自报归属」变成「可以不报」。
// 显式给出但与身份不一致一律拒；拒绝统一用 COMMAND_NOT_FOUND（404）而非 403 ——
// 403 等于告诉调用方「这台确实存在但你没权限」，可被用来枚举他服是否存在；
// 404 对「不存在」与「非归属」给同一答案，不泄露存在性。
func bindAgentTarget(w http.ResponseWriter, r *http.Request, identity agentauth.Identity, namespace, serverID string) (string, string, bool) {
	ns := strings.TrimSpace(namespace)
	sid := strings.TrimSpace(serverID)
	if (ns != "" && ns != identity.Namespace) || (sid != "" && sid != identity.ServerID) {
		render.WriteError(w, r, apperr.ErrCommandNotFound)
		return "", "", false
	}
	return ns, sid, true
}

// bindAgentNamespace 是只按 namespace 定位的端点（发现）用的收敛器：
// 缺省或显式等于身份 namespace 时返回身份 namespace；显式跨环境一律拒（同样 404 不泄露存在性）。
//
// 发现端点必须收敛：它原先在 namespace 缺省时会把**全部环境**的在线实例一起吐出来，
// 共享 token 之下等于把整个集群拓扑（含他环境 host/port）开放给任一 agent。
func bindAgentNamespace(w http.ResponseWriter, r *http.Request, identity agentauth.Identity, namespace string) (string, bool) {
	ns := strings.TrimSpace(namespace)
	if ns == "" {
		return identity.Namespace, true
	}
	if ns != identity.Namespace {
		render.WriteError(w, r, apperr.ErrCommandNotFound)
		return "", false
	}
	return ns, true
}
