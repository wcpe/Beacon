package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/internal/auth"
	"github.com/wcpe/Beacon/internal/handler"
)

// Handlers 汇集各 HTTP 处理器，供路由装配（避免过长的位置参数）。
type Handlers struct {
	Namespace        *handler.NamespaceHandler
	Config           *handler.ConfigHandler
	File             *handler.FileHandler
	OverrideSet      *handler.OverrideSetHandler
	Agent            *handler.AgentHandler
	Stream           *handler.StreamHandler
	Instance         *handler.InstanceHandler
	Topology         *handler.TopologyHandler
	Zone             *handler.ZoneHandler
	Scheduling       *handler.SchedulingHandler
	Audit            *handler.AuditHandler
	Alert            *handler.AlertHandler
	AlertEvent       *handler.AlertEventHandler
	Metric           *handler.MetricHandler
	System           *handler.SystemHandler
	Observability    *handler.ObservabilityHandler
	Auth             *handler.AuthHandler
	APIKey           *handler.APIKeyHandler
	Command          *handler.CommandHandler
	AgentLog         *handler.AgentLogHandler
	ReverseFetchTask *handler.ReverseFetchTaskHandler
	ReverseFetchRule *handler.ReverseFetchIgnoreRuleHandler
	Settings         *handler.SettingsHandler
	Metrics          http.Handler // 运维指标端点 /metrics（Prometheus 文本，内网信任、不挂鉴权，见 ADR-0020）
	Web              http.Handler
}

// NewRouter 装配 HTTP 路由：agent API（挂 token）+ admin API（登录除外挂鉴权 + 只读拒写 + 写审计兜底）+ 内嵌前端（SPA 回退）。
// 中间件自外向内：recover → traceId → 访问日志。admin 组内：鉴权（登录令牌 / API 密钥）→ 只读拒写裁决 → 写操作审计兜底（FR-72）。
func NewRouter(h Handlers, agentToken string, authn *auth.Authenticator, apiKeys APIKeyVerifier, audit auditCreator) http.Handler {
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
		// 反向抓取命令（FR-39，见 ADR-0027）：拉本机待办命令 + 回传 plugins 文件集 ingest
		r.Get("/commands", h.Command.Pending)
		r.Post("/files/ingest", h.Command.Ingest)
		// 强制重同步命令结果回传（FR-91）：resync-config 命令无内容回传，仅推进命令 done / failed
		r.Post("/commands/result", h.Command.ReportResult)
		// 反向抓取受管任务·扫描回传（FR-58，见 ADR-0037）：回传只含元信息的扫描清单（无内容、永不失败）
		r.Post("/files/scan", h.ReverseFetchTask.Scan)
		// 反向抓取受管任务·错误回传（FR-87）：agent 执行 scan/submit 读盘失败回传错误，任务转 failed 记 lastError
		r.Post("/files/error", h.ReverseFetchTask.ReportError)
		// 取 agent 日志回传（FR-88，见 ADR-0040）：agent 回传自身脱敏日志环形缓冲快照，转存命令瞬态
		r.Post("/logs", h.AgentLog.Receive)
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
		// 写操作审计兜底（FR-72，增强 FR-7）：挂在鉴权 + 只读拒写之后（context 已有 operator、被拒写请求不进入兜底）。
		// 对尚无专项审计的写端点补记一条，命中覆盖集合的端点跳过避免双记；detail 不含请求体，落库失败只 WARN 不阻断。
		r.Use(auditWriteMiddleware(audit))
		// 登出：仅记审计（令牌无状态、服务端无会话可吊销），故挂在鉴权中间件内以取认证身份
		r.Post("/auth/logout", h.Auth.Logout)
		r.Get("/namespaces", h.Namespace.List)
		r.Post("/namespaces", h.Namespace.Create)
		// 环境改名 / 删除（FR-53）：写方法，readonly 角色经 readonlyWriteGuard 403；删除带在用数据守卫
		r.Put("/namespaces/{code}", h.Namespace.Update)
		r.Delete("/namespaces/{code}", h.Namespace.Delete)

		// 配置中心
		r.Get("/configs", h.Config.List)
		r.Post("/configs", h.Config.Create)
		// 有效配置只读预览（FR-22）：chi 静态路由优先于 {id} 通配（与注册顺序无关），此处置前仅为可读性
		r.Get("/configs/effective", h.Config.Effective)
		// 配置灰度 / Beta 列活跃灰度（FR-9，静态路由，与 effective 同理优先于 {id}）
		r.Get("/configs/gray", h.Config.ListGray)
		// 发布影响面预览（FR-79）：按 scope + zone_assignment + 注册表算受影响在线子服集合（静态路由置于 {id} 前）
		r.Get("/configs/impact", h.Config.Impact)
		// 批量删除 / 禁用 / 启用（FR-74，一事务原子）：静态路由置于 {id} 前以免被通配吞掉
		r.Post("/configs/batch", h.Config.Batch)
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
		// 有效文件树只读预览（FR-45）：逐文件合并结果 + 逐键来源，静态路由置于 {id} 前以免被通配吞掉
		r.Get("/files/effective", h.File.Effective)
		// 配置导入（FR-38）：把一份目录批量上传到某组（multipart，静态路由置于 {id} 前以免被通配吞掉）
		r.Post("/files/import", h.File.Import)
		// 批量删除 / 禁用 / 启用（FR-74，一事务原子）：静态路由置于 {id} 前以免被通配吞掉
		r.Post("/files/batch", h.File.Batch)
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
		// 主动下线标记列表（FR-49）：静态路由置于 {serverId} 前以免被通配吞掉
		r.Get("/instances/offline", h.Instance.ListOffline)
		r.Get("/instances/{serverId}", h.Instance.Get)
		// per-server 有效配置变更时间线（FR-80）：该服覆盖链各 config 项发布历史按时间倒序，只读
		r.Get("/instances/{serverId}/config-timeline", h.Instance.ConfigTimeline)
		// 主动下线（FR-49）：落 DB 拒绝态 + 移出可用集；DELETE 取消下线。二者为写方法，readonly 密钥经 readonlyWriteGuard 403
		r.Post("/instances/{serverId}/offline", h.Instance.Offline)
		r.Delete("/instances/{serverId}/offline", h.Instance.Online)
		// 取 agent 日志（FR-88，见 ADR-0040）：触发取自身脱敏日志（写，readonly 403）+ 查询最近一次结果（读）
		r.Post("/instances/{serverId}/logs", h.AgentLog.Request)
		r.Get("/instances/{serverId}/logs", h.AgentLog.Get)
		// 强制重同步（FR-91）：触发该实例重拉有效配置/文件树/覆盖集（写，readonly 403）
		r.Post("/instances/{serverId}/resync", h.Command.Resync)
		// 在线实例反向抓取·受管任务（FR-58，重定义旧一次性端点，见 ADR-0037）：建扫描任务 + 下发 scan 命令（写，readonly 403）
		r.Post("/instances/{serverId}/reverse-fetch", h.ReverseFetchTask.CreateScanTask)
		// 受管任务台 / 审核台（FR-58）：查 / 列任务（读）+ 提交选定集 / 取消（写，readonly 403）
		r.Get("/reverse-fetch/tasks", h.ReverseFetchTask.ListTasks)
		r.Get("/reverse-fetch/tasks/{id}", h.ReverseFetchTask.GetTask)
		r.Post("/reverse-fetch/tasks/{id}/submit", h.ReverseFetchTask.SubmitTask)
		r.Post("/reverse-fetch/tasks/{id}/cancel", h.ReverseFetchTask.CancelTask)
		// 冲突 diff 审核（FR-59）：冲突清单 / 逐文件 diff（读）+ resolve 落库（写，readonly 403）
		r.Get("/reverse-fetch/tasks/{id}/conflicts", h.ReverseFetchTask.ListConflicts)
		r.Get("/reverse-fetch/tasks/{id}/conflicts/diff", h.ReverseFetchTask.ConflictDiff)
		r.Post("/reverse-fetch/tasks/{id}/resolve", h.ReverseFetchTask.Resolve)
		// 持久忽略规则（FR-59）：列规则（读）+ 建 / 删（写，readonly 403）
		r.Get("/reverse-fetch/ignore-rules", h.ReverseFetchRule.List)
		r.Post("/reverse-fetch/ignore-rules", h.ReverseFetchRule.Create)
		r.Delete("/reverse-fetch/ignore-rules/{id}", h.ReverseFetchRule.Delete)
		// 按需拓印回写（FR-46）：触发拓印某文件（写）→ diff 本地实际值⟷期望合并值（读）→ 单人自审确认落库（写，readonly 403）
		r.Post("/instances/{serverId}/imprint", h.Command.Imprint)
		r.Get("/imprints/{commandId}", h.Command.ImprintStatus)
		r.Get("/imprints/{commandId}/diff", h.Command.ImprintDiff)
		r.Post("/imprints/{commandId}/confirm", h.Command.ConfirmImprint)

		// 集群拓扑（FR-37）：bc→bukkit 真实连线 + 大区/zone 分组，读内存注册表快照
		r.Get("/topology", h.Topology.Topology)

		// 健康告警站内信（FR-28）
		r.Get("/alerts", h.Alert.List)
		// 告警历史 / 事件信息流（FR-89，见 ADR-0041）：持久化的告警事件按类型/级别/环境/时间过滤分页查询
		r.Get("/alert-events", h.AlertEvent.List)

		// zone 分配
		r.Get("/zones/assignments", h.Zone.ListAssignments)
		r.Put("/zones/assignments", h.Zone.Assign)
		r.Delete("/zones/assignments", h.Zone.Unassign)
		// 小区默认入口（FR-48）：每 zone 唯一默认入口 serverId，供 BC 设 BungeeCord 默认/fallback 服
		r.Get("/zones/default-entry", h.Zone.ListDefaultEntries)
		r.Put("/zones/default-entry", h.Zone.SetDefaultEntry)
		r.Delete("/zones/default-entry", h.Zone.ClearDefaultEntry)
		r.Get("/zones", h.Zone.Summary)

		// 流量调度（FR-10）：落位建议（query-only）+ drain 标记，控制面只给决策不执行玩家连接（ADR-0017）
		r.Get("/scheduling/placement", h.Scheduling.Placement)
		r.Get("/scheduling/drains", h.Scheduling.ListDrains)
		r.Put("/scheduling/drains", h.Scheduling.Drain)
		r.Delete("/scheduling/drains", h.Scheduling.Undrain)

		// 审计
		r.Get("/audits", h.Audit.List)
		// 审计活动聚合（FR-73）：窗口内计数 / 成功率 / 按动作分布 / 每日趋势；静态路由置于 /audits 之后
		r.Get("/audits/analytics", h.Audit.Analytics)
		// 审计导出（FR-84）：复用 List 过滤（含 detailKeyword），按 format=csv|json 流式全量导出
		r.Get("/audits/export", h.Audit.Export)

		// 管理面 API 密钥（FR-42，见 ADR-0026）：只读角色 + 运行时创建/吊销/重置
		// 创建/吊销/重置为写方法，readonly 角色经 readonlyWriteGuard 一律 403
		r.Get("/api-keys", h.APIKey.List)
		r.Post("/api-keys", h.APIKey.Create)
		r.Delete("/api-keys/{id}", h.APIKey.Revoke)
		r.Post("/api-keys/{id}/reset", h.APIKey.Reset)

		// 负载指标看板（FR-32，见 ADR-0023）：当前快照聚合 + 历史趋势；仅负载数字、不含名单
		if h.Metric != nil {
			r.Get("/metrics/summary", h.Metric.Summary)
			r.Get("/metrics/trend", h.Metric.Trend)
		}

		// 控制面自身状态页眉（FR-33）：版本/运行时长/DB 连通/在线实例数/采样器状态 + Go 运行时资源
		r.Get("/system/status", h.System.Status)

		// 控制面自观测页（FR-82）：DB 连接池/长轮询挂起/注册表规模/命令队列深度，只读、控制面进程内部运行态
		r.Get("/system/observability", h.Observability.Observability)

		// 运维设置 store（FR-61，见 ADR-0038）：列全部热改项（读）+ 改单项（写，readonly 403，入审计）。
		// 与其它写端点一致无条件注册（handler 仅请求期解引用），PUT 已登记 FR-72 覆盖集。
		r.Get("/settings", h.Settings.List)
		r.Put("/settings/{key}", h.Settings.Update)
	})

	// 非 API、非静态文件的路径交给内嵌前端（含 SPA history 回退）
	r.NotFound(h.Web.ServeHTTP)
	return r
}
