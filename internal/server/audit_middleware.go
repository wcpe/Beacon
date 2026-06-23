package server

import (
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/internal/auth"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/render"
)

// adminAPIPrefix 是管理面 API 的统一前缀，推导资源词时从 RoutePattern 中剥除。
const adminAPIPrefix = "/admin/v1/"

// auditCreator 是兜底审计落库的最小依赖（仅追加一条），由 repository.AuditLogRepository 满足。
// 抽成接口便于中间件单测用内存假实现驱动，不连库。
type auditCreator interface {
	Create(entry *model.AuditLog) error
}

// coveredWriteRoutes 是「已被专项审计覆盖的写路由」集中清单（method + chi RoutePattern）。
// 命中清单的写请求由各 service 在事务内自记领域审计，兜底中间件不再补记，避免双记。
// 维护约定：新增「自带专项审计」的写端点须把其 (method, pattern) 加入本集合；
// 新增「无专项审计」的写端点不必登记，会被中间件自动兜底。
var coveredWriteRoutes = map[string]struct{}{
	// 登出（auth.logout）
	"POST /admin/v1/auth/logout": {},
	// 环境 CRUD（namespace.create / update / delete）
	"POST /admin/v1/namespaces":          {},
	"PUT /admin/v1/namespaces/{code}":    {},
	"DELETE /admin/v1/namespaces/{code}": {},
	// 配置中心（config.create / publish / delete / rollback / gray-*）
	"POST /admin/v1/configs":                   {},
	"PUT /admin/v1/configs/{id}":               {},
	"DELETE /admin/v1/configs/{id}":            {},
	"POST /admin/v1/configs/{id}/rollback":     {},
	"POST /admin/v1/configs/{id}/gray":         {},
	"POST /admin/v1/configs/{id}/gray/promote": {},
	"DELETE /admin/v1/configs/{id}/gray":       {},
	// 文件树托管（file.create / import / publish / delete / rollback）
	"POST /admin/v1/files":               {},
	"POST /admin/v1/files/import":        {},
	"PUT /admin/v1/files/{id}":           {},
	"DELETE /admin/v1/files/{id}":        {},
	"POST /admin/v1/files/{id}/rollback": {},
	// 三方覆盖集（override-set.create / publish / delete / rollback）
	"POST /admin/v1/override-sets":               {},
	"PUT /admin/v1/override-sets/{id}":           {},
	"DELETE /admin/v1/override-sets/{id}":        {},
	"POST /admin/v1/override-sets/{id}/rollback": {},
	// 实例下线 / 反向抓取 / 拓印（instance.offline / online，file.reverse-fetch-scan / imprint-fetch / imprint）
	"POST /admin/v1/instances/{serverId}/offline":       {},
	"DELETE /admin/v1/instances/{serverId}/offline":     {},
	"POST /admin/v1/instances/{serverId}/reverse-fetch": {},
	"POST /admin/v1/instances/{serverId}/imprint":       {},
	"POST /admin/v1/imprints/{commandId}/confirm":       {},
	// 反向抓取受管任务·提交选定 / 取消（FR-58：file.reverse-fetch-submit / cancel，各在事务内或服务内自记专项审计）
	"POST /admin/v1/reverse-fetch/tasks/{id}/submit": {},
	"POST /admin/v1/reverse-fetch/tasks/{id}/cancel": {},
	// 反向抓取冲突审核 resolve + 持久忽略规则建 / 删（FR-59：file.reverse-fetch-ingest / ignore-rule-add / -remove，服务内自记专项审计）
	"POST /admin/v1/reverse-fetch/tasks/{id}/resolve":  {},
	"POST /admin/v1/reverse-fetch/ignore-rules":        {},
	"DELETE /admin/v1/reverse-fetch/ignore-rules/{id}": {},
	// zone 分配与默认入口（zone.assign / unassign / set-default-entry / clear-default-entry）
	"PUT /admin/v1/zones/assignments":      {},
	"DELETE /admin/v1/zones/assignments":   {},
	"PUT /admin/v1/zones/default-entry":    {},
	"DELETE /admin/v1/zones/default-entry": {},
	// 流量调度（scheduling.drain / undrain）
	"PUT /admin/v1/scheduling/drains":    {},
	"DELETE /admin/v1/scheduling/drains": {},
	// 管理面 API 密钥（apikey.create / revoke / reset；明文绝不入 detail）
	"POST /admin/v1/api-keys":            {},
	"DELETE /admin/v1/api-keys/{id}":     {},
	"POST /admin/v1/api-keys/{id}/reset": {},
}

// specialActionVerbs 是 RoutePattern 末段静态词到审计动词的特例映射；
// 命中则动词取该词，否则按 HTTP 方法映射（见 methodVerb）。
var specialActionVerbs = map[string]string{
	"rollback":      "rollback",
	"offline":       "offline",
	"drains":        "drain",
	"reset":         "reset",
	"promote":       "promote",
	"confirm":       "confirm",
	"reverse-fetch": "reverse-fetch",
	"imprint":       "imprint",
	"gray":          "gray",
}

// auditWriteMiddleware 兜底审计中间件（FR-72，增强 FR-7）：对 /admin/v1 下尚无专项审计的写端点，
// 在 handler 执行后补记一条 audit_log（operator + action + target + result + clientIP）。
// 兜底审计 detail 一律不含请求体（敏感豁免）；落库失败只记 WARN、绝不阻断主响应（旁路语义）。
func auditWriteMiddleware(creator auditCreator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 读方法直接放行、不记。
			if !isWriteMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			// 包装捕获状态码，供 result 判定。
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)

			pattern := chi.RouteContext(r.Context()).RoutePattern()
			// 已被专项审计覆盖的写路由：专项审计已记，跳过兜底避免双记。
			if _, covered := coveredWriteRoutes[r.Method+" "+pattern]; covered {
				return
			}
			recordWriteAudit(creator, r, pattern, sw.status)
		})
	}
}

// recordWriteAudit 组装并落一条兜底审计；落库失败只记 WARN，不影响已发回的主响应。
func recordWriteAudit(creator auditCreator, r *http.Request, pattern string, status int) {
	action, targetType, targetRef := deriveAuditTarget(r, pattern)
	entry := &model.AuditLog{
		Operator:   auth.Operator(r.Context()),
		Action:     action,
		TargetType: targetType,
		TargetRef:  targetRef,
		Result:     resultOf(status),
		ClientIP:   serverClientIP(r),
		// detail 留空：兜底审计仅记元数据，绝不写请求体（敏感豁免）。
	}
	if err := creator.Create(entry); err != nil {
		slog.Warn("兜底写审计落库失败", "动作", action, "对象", targetType+"/"+targetRef,
			"路径", r.URL.Path, "原因", err, "traceId", render.TraceID(r.Context()))
	}
}

// deriveAuditTarget 由 HTTP 方法与 chi RoutePattern 推导 (action, targetType, targetRef)，纯函数便于单测。
// 资源词 = 前缀后首个静态段单数化；动词 = 末段特例映射或方法映射；targetRef = 路径参数值或退回资源词。
func deriveAuditTarget(r *http.Request, pattern string) (action, targetType, targetRef string) {
	resource := singularize(firstResourceSegment(pattern))
	verb := deriveVerb(r.Method, pattern)
	action = resource + "." + verb
	targetType = resource
	targetRef = pathParamsRef(r)
	if targetRef == "" {
		targetRef = resource
	}
	return action, targetType, targetRef
}

// firstResourceSegment 取 RoutePattern 剥除 /admin/v1/ 前缀后的首个路径段（如 configs / instances）。
func firstResourceSegment(pattern string) string {
	trimmed := strings.TrimPrefix(pattern, adminAPIPrefix)
	if i := strings.IndexByte(trimmed, '/'); i >= 0 {
		trimmed = trimmed[:i]
	}
	return trimmed
}

// deriveVerb 推导审计动词：RoutePattern 末段静态词命中特例映射则用之，否则按方法映射。
func deriveVerb(method, pattern string) string {
	if last := lastStaticSegment(pattern); last != "" {
		if v, ok := specialActionVerbs[last]; ok {
			return v
		}
	}
	return methodVerb(method)
}

// lastStaticSegment 取 RoutePattern 最后一个非路径参数（非 {..}）静态段。
func lastStaticSegment(pattern string) string {
	segs := strings.Split(strings.Trim(pattern, "/"), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		s := segs[i]
		if s != "" && !strings.HasPrefix(s, "{") {
			return s
		}
	}
	return ""
}

// methodVerb 把写方法映射为通用动词。
func methodVerb(method string) string {
	switch method {
	case http.MethodPost:
		return "create"
	case http.MethodPut, http.MethodPatch:
		return "update"
	case http.MethodDelete:
		return "delete"
	default:
		return "write"
	}
}

// pathParamsRef 取 chi 路径参数值按出现顺序用 / 拼接（如 {id} → 实际 id），无参数返回空串。
func pathParamsRef(r *http.Request) string {
	rc := chi.RouteContext(r.Context())
	if rc == nil {
		return ""
	}
	vals := rc.URLParams.Values
	nonEmpty := make([]string, 0, len(vals))
	for _, v := range vals {
		if v != "" {
			nonEmpty = append(nonEmpty, v)
		}
	}
	return strings.Join(nonEmpty, "/")
}

// singularize 把资源词单数化（仅去结尾 's'），用于由复数路由段得对象类型 / 动作前缀。
func singularize(word string) string {
	if len(word) > 1 && strings.HasSuffix(word, "s") {
		return word[:len(word)-1]
	}
	return word
}

// resultOf 把响应状态码映射为审计结果：2xx → ok，否则 fail。
func resultOf(status int) string {
	if status >= 200 && status < 300 {
		return model.ResultOK
	}
	return model.ResultFail
}

// serverClientIP 在 server 包内提取来源 IP，与 handler 侧口径一致：
// X-Forwarded-For 首跳 → X-Real-IP → RemoteAddr 的 host。
func serverClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xrip := strings.TrimSpace(r.Header.Get("X-Real-IP")); xrip != "" {
		return xrip
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
