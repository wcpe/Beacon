package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"beacon/internal/auth"
	"beacon/internal/handler"
)

// Handlers 汇集各 HTTP 处理器，供路由装配（避免过长的位置参数）。
type Handlers struct {
	Namespace   *handler.NamespaceHandler
	Config      *handler.ConfigHandler
	File        *handler.FileHandler
	OverrideSet *handler.OverrideSetHandler
	Agent       *handler.AgentHandler
	Stream      *handler.StreamHandler
	Instance    *handler.InstanceHandler
	Topology    *handler.TopologyHandler
	Zone        *handler.ZoneHandler
	Scheduling  *handler.SchedulingHandler
	Audit       *handler.AuditHandler
	Alert       *handler.AlertHandler
	Metric      *handler.MetricHandler
	System      *handler.SystemHandler
	Auth        *handler.AuthHandler
	ApiKey      *handler.ApiKeyHandler
	Metrics     http.Handler // 运维指标端点 /metrics（Prometheus 文本，内网信任、不挂鉴权，见 ADR-0020）
	Web         http.Handler
}

// NewRouter 装配 HTTP 路由：agent API（挂 token）+ admin API（登录除外挂鉴权 + 只读拒写中间件）+ 内嵌前端（SPA 回退）。
// 中间件自外向内：recover → traceId → 访问日志。admin 组内：鉴权（登录令牌 / API 密钥）→ 只读拒写裁决。
func NewRouter(h Handlers, agentToken string, authn *auth.Authenticator, apiKeys ApiKeyVerifier) http.Handler {
	r := chi.NewRouter()
	r.Use(recoverMiddleware, traceMiddleware, accessLog)

	// agent 侧：内网信任，仅以共享 token 防误连
	r.Route("/beacon/v1/agent", func(r chi.Router) {
		r.Use(agentTokenMiddleware(agentToken))
		r.Post("/register", h.Agent.Register)
		r.Post("/heartbeat", h.Agent.Heartbeat)
		r.Get("/config/effective", h.Agent.Effective)
		// 单条 SSE 推送流（FR-24）：合并配置/文件树/覆盖集三条长轮询，只发变更通知 + 连接即对账
		r.Get("/stream", h.Stream.Stream)
		r.Get("/files/manifest", h.File.Manifest)
		r.Get("/files/content", h.File.Content)
		// 三方插件文件覆盖兼容（FR-15）：投递适用覆盖集（目标根 + 受限重载命令 + 成员清单）与成员内容
		r.Get("/override-sets", h.File.OverrideManifest)
		r.Get("/override-sets/content", h.File.OverrideContent)
		r.Post("/report", h.Agent.Report)
		r.Get("/discovery", h.Agent.Discover)
	})

	// 运维指标：Prometheus 文本格式，与 agent 端点同属内网信任面，不挂管理台鉴权（见 ADR-0020）
	if h.Metrics != nil {
		r.Method(http.MethodGet, "/metrics", h.Metrics)
	}

	// 管理台登录：签发令牌，自身不挂令牌中间件
	r.Post("/admin/v1/auth/login", h.Auth.Login)

	// admin 侧：除登录外一律校验身份（登录令牌 / API 密钥），再经只读拒写裁决
	r.Route("/admin/v1", func(r chi.Router) {
		r.Use(adminAuthMiddleware(authn, apiKeys))
		r.Use(readonlyWriteGuard)
		// 登出：仅记审计（令牌无状态、服务端无会话可吊销），故挂在鉴权中间件内以取认证身份
		r.Post("/auth/logout", h.Auth.Logout)
		r.Get("/namespaces", h.Namespace.List)
		r.Post("/namespaces", h.Namespace.Create)

		// 配置中心
		r.Get("/configs", h.Config.List)
		r.Post("/configs", h.Config.Create)
		// 有效配置只读预览（FR-22）：chi 静态路由优先于 {id} 通配（与注册顺序无关），此处置前仅为可读性
		r.Get("/configs/effective", h.Config.Effective)
		// 配置灰度 / Beta 列活跃灰度（FR-9，静态路由，与 effective 同理优先于 {id}）
		r.Get("/configs/gray", h.Config.ListGray)
		r.Get("/configs/{id}", h.Config.Get)
		r.Put("/configs/{id}", h.Config.Publish)
		r.Delete("/configs/{id}", h.Config.Delete)
		r.Get("/configs/{id}/revisions", h.Config.ListRevisions)
		r.Get("/configs/{id}/revisions/{version}", h.Config.GetRevision)
		r.Post("/configs/{id}/rollback", h.Config.Rollback)
		r.Get("/configs/{id}/diff", h.Config.Diff)
		// 配置灰度 / Beta（FR-9）：发布灰度 / 晋升 / 中止（见 ADR-0021）
		r.Post("/configs/{id}/gray", h.Config.PublishGray)
		r.Post("/configs/{id}/gray/promote", h.Config.PromoteGray)
		r.Delete("/configs/{id}/gray", h.Config.AbortGray)

		// 文件树托管（通道B）
		r.Get("/files", h.File.List)
		r.Post("/files", h.File.Create)
		// 配置导入（FR-38）：把一份目录批量上传到某组（multipart，静态路由置于 {id} 前以免被通配吞掉）
		r.Post("/files/import", h.File.Import)
		r.Get("/files/{id}", h.File.Get)
		r.Put("/files/{id}", h.File.Publish)
		r.Delete("/files/{id}", h.File.Delete)
		r.Get("/files/{id}/revisions", h.File.ListRevisions)
		r.Get("/files/{id}/revisions/{version}", h.File.GetRevision)
		r.Post("/files/{id}/rollback", h.File.Rollback)

		// 三方插件文件覆盖兼容：覆盖集 CRUD/发布/历史/回滚 + 发布前 dry-run 只读预览（FR-15）
		r.Get("/override-sets", h.OverrideSet.List)
		r.Post("/override-sets", h.OverrideSet.Create)
		r.Get("/override-sets/{id}", h.OverrideSet.Get)
		r.Put("/override-sets/{id}", h.OverrideSet.Publish)
		r.Delete("/override-sets/{id}", h.OverrideSet.Delete)
		r.Get("/override-sets/{id}/revisions", h.OverrideSet.ListRevisions)
		r.Post("/override-sets/{id}/rollback", h.OverrideSet.Rollback)
		r.Get("/override-sets/{id}/dry-run", h.OverrideSet.DryRun)

		// 实例与健康
		r.Get("/instances", h.Instance.List)
		r.Get("/instances/{serverId}", h.Instance.Get)
		r.Post("/instances/{serverId}/offline", h.Instance.Offline)

		// 集群拓扑（FR-37）：bc→bukkit 真实连线 + 大区/zone 分组，读内存注册表快照
		r.Get("/topology", h.Topology.Topology)

		// 健康告警站内信（FR-28）
		r.Get("/alerts", h.Alert.List)

		// zone 分配
		r.Get("/zones/assignments", h.Zone.ListAssignments)
		r.Put("/zones/assignments", h.Zone.Assign)
		r.Delete("/zones/assignments", h.Zone.Unassign)
		r.Get("/zones", h.Zone.Summary)

		// 流量调度（FR-10）：落位建议（query-only）+ drain 标记，控制面只给决策不执行玩家连接（ADR-0017）
		r.Get("/scheduling/placement", h.Scheduling.Placement)
		r.Get("/scheduling/drains", h.Scheduling.ListDrains)
		r.Put("/scheduling/drains", h.Scheduling.Drain)
		r.Delete("/scheduling/drains", h.Scheduling.Undrain)

		// 审计
		r.Get("/audits", h.Audit.List)

		// 管理面 API 密钥（FR-42，见 ADR-0026）：只读角色 + 运行时创建/吊销/重置
		// 创建/吊销/重置为写方法，readonly 角色经 readonlyWriteGuard 一律 403
		r.Get("/api-keys", h.ApiKey.List)
		r.Post("/api-keys", h.ApiKey.Create)
		r.Delete("/api-keys/{id}", h.ApiKey.Revoke)
		r.Post("/api-keys/{id}/reset", h.ApiKey.Reset)

		// 负载指标看板（FR-32，见 ADR-0023）：当前快照聚合 + 历史趋势；仅负载数字、不含名单
		if h.Metric != nil {
			r.Get("/metrics/summary", h.Metric.Summary)
			r.Get("/metrics/trend", h.Metric.Trend)
		}

		// 控制面自身状态页眉（FR-33）：版本/运行时长/DB 连通/在线实例数/采样器状态 + Go 运行时资源
		r.Get("/system/status", h.System.Status)
	})

	// 非 API、非静态文件的路径交给内嵌前端（含 SPA history 回退）
	r.NotFound(h.Web.ServeHTTP)
	return r
}
