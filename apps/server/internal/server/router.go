package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/handler"
)

// Handlers 汇集各 HTTP 处理器，供路由装配（避免过长的位置参数）。
type Handlers struct {
	Namespace         *handler.NamespaceHandler
	Env               *handler.EnvHandler
	V2                *handler.V2ControlPlaneHandler
	V2Metrics         *handler.V2MetricsHandler
	V2Health          *handler.V2HealthHandler
	V2Sched           *handler.V2SchedHandler
	V2Connection      *handler.V2ConnectionHandler
	V2Message         *handler.V2MessageHandler
	V2ConnectionAdmin *handler.V2ConnectionAdminHandler
	V2MessageAdmin    *handler.V2MessageAdminHandler
	V2Archive         *handler.V2ArchiveHandler
	V2ConfigCenter    *handler.V2ConfigCenterHandler
	V2Assets          *handler.V2AssetsHandler
	Delivery          *handler.DeliveryAdminHandler
	SchedDecision     *handler.SchedDecisionAdminHandler
	Config            *handler.ConfigHandler
	File              *handler.FileHandler
	OverrideSet       *handler.OverrideSetHandler
	Agent             *handler.AgentHandler
	Stream            *handler.StreamHandler
	Instance          *handler.InstanceHandler
	Topology          *handler.TopologyHandler
	Zone              *handler.ZoneHandler
	Scheduling        *handler.SchedulingHandler
	Audit             *handler.AuditHandler
	Alert             *handler.AlertHandler
	AlertEvent        *handler.AlertEventHandler
	Metric            *handler.MetricHandler
	System            *handler.SystemHandler
	Observability     *handler.ObservabilityHandler
	CommandObserve    *handler.CommandObserveHandler
	Update            *handler.UpdateHandler
	Auth              *handler.AuthHandler
	APIKey            *handler.APIKeyHandler
	Command           *handler.CommandHandler
	Browse            *handler.BrowseHandler
	Asset             *handler.AssetHandler
	FileSync          *handler.FileSyncHandler
	AgentLog          *handler.AgentLogHandler
	ReverseFetchTask  *handler.ReverseFetchTaskHandler
	ReverseFetchRule  *handler.ReverseFetchIgnoreRuleHandler
	Settings          *handler.SettingsHandler
	ReversibleOp      *handler.ReversibleOperationHandler
	Metrics           http.Handler // 运维指标端点 /metrics（Prometheus 文本，内网信任、不挂鉴权，见 ADR-0020）
	Web               http.Handler
}

// NewRouter 装配 HTTP 路由：agent API（挂 token）+ admin API（登录除外挂鉴权 + 只读拒写 + 写审计兜底）+ 内嵌前端（SPA 回退）。
// 中间件自外向内：recover → traceId → 访问日志。admin 组内：鉴权（登录令牌 / API 密钥）→ 只读拒写裁决 → 写操作审计兜底（FR-72）。
func NewRouter(h Handlers, agentToken string, authn *auth.Authenticator, apiKeys APIKeyVerifier, audit auditCreator) http.Handler {
	r := chi.NewRouter()
	r.Use(recoverMiddleware, traceMiddleware, accessLog)

	var v2Auth AgentV2Authenticator
	if h.V2 != nil {
		v2Auth = h.V2
	}

	// agent 侧：内网信任，仅以共享 token 防误连
	r.Route("/beacon/v1/agent", func(r chi.Router) {
		r.Use(agentTokenMiddleware(agentToken, v2Auth))
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
		// 文件浏览结果回传（FR-110，见 ADR-0049）：agent 回传列目录 / 子树 / 文件内容，转存命令瞬态、唤醒等待的 admin
		r.Post("/files/browse-result", h.Browse.BrowseResult)
		// 多级灰度文件同步数据面（ADR-0058）：命令通道只编排，文件内容走流式 HTTP。
		r.Post("/file-sync/{taskId}/manifest", h.FileSync.ReceiveManifest)
		r.Put("/file-sync/{taskId}/blobs/{hash}", h.FileSync.UploadBlob)
		r.Get("/file-sync/{taskId}/blobs/{hash}", h.FileSync.DownloadBlob)
		r.Post("/file-sync/{taskId}/targets/result", h.FileSync.TargetResult)
	})

	// 运维指标：Prometheus 文本格式，与 agent 端点同属内网信任面，不挂管理台鉴权（见 ADR-0020）
	if h.Metrics != nil {
		r.Method(http.MethodGet, "/metrics", h.Metrics)
	}

	if h.V2 != nil {
		// v2 agent 侧：namespace token 在注册 handler 内按库中哈希校验，未确认身份仅开放 register / registration。
		r.Route("/beacon/v2/agent", func(r chi.Router) {
			r.Post("/register", h.V2.AgentRegister)
			r.Get("/registration", h.V2.AgentRegistration)
			// 指标上报（FR-144，见 §5.1）：挂 token↔namespace + identity 鉴权中间件（未确认 403）、
			// 注入权威身份供归属；接收端只校验 + 更内存窗口 + 异步入队，请求 goroutine 不碰 DB。
			if h.V2Metrics != nil {
				r.With(agentV2ReportMiddleware(h.V2)).Post("/metrics/report", h.V2Metrics.Report)
			}

			// P4b 挂载点：调度决策 agent 端点（FR-146，见 §5.1）：与指标上报同挂
			// token↔namespace + identity 鉴权中间件（未确认 403）；候选快照 / 在线决策 /
			// 降级补报全程纯内存 + 异步入库，请求 goroutine 不碰 DB。
			if h.V2Sched != nil {
				r.With(agentV2ReportMiddleware(h.V2)).Get("/schedule/candidates", h.V2Sched.Candidates)
				r.With(agentV2ReportMiddleware(h.V2)).Post("/schedule/decide", h.V2Sched.Decide)
				r.With(agentV2ReportMiddleware(h.V2)).Post("/schedule/report-local", h.V2Sched.ReportLocal)
			}

			// P5a 挂载点：连接明细采集 agent 端点（FR-145，见 §5.1）：与指标 / 调度上报同挂
			// token↔namespace + identity 鉴权中间件（未确认 403）；proxy 上报 open/close 事件，
			// 接收端只校验 + 更内存名册 + 异步入库，请求 goroutine 不碰 DB、队列满回 429。
			if h.V2Connection != nil {
				r.With(agentV2ReportMiddleware(h.V2)).Post("/connections/batch", h.V2Connection.Batch)
			}

			// P5a 挂载点：跨服消息 agent 端点（FR-149/150，见 §5.1、ADR-0063）：与采集面同挂鉴权中间件。
			// send 上行 / poll 长轮询下行 / ack 回执；寻址走内存名册、中转走内存队列、终态异步落库，请求 goroutine 不碰 DB。
			if h.V2Message != nil {
				r.With(agentV2ReportMiddleware(h.V2)).Post("/messages/send", h.V2Message.Send)
				r.With(agentV2ReportMiddleware(h.V2)).Post("/messages/poll", h.V2Message.Poll)
				r.With(agentV2ReportMiddleware(h.V2)).Post("/messages/ack", h.V2Message.Ack)
			}

			// P8 挂载点：文件资产清单上报（FR-163，见 §5.1）：与指标 / 连接采集面同挂 token↔namespace +
			// identity 鉴权中间件（未确认 403），注入权威身份归属清单；增量 / 全量分片入库走常规同步事务。
			// 抽成函数注册（内部 nil 守卫），避免本 v2 agent 组内联 if 累加触发 nestif。
			registerV2AssetsAgentRoutes(r, h)
			// 文件资产内容回传（FR-164，见 spec §5.1）：agent 读单文本文件回传供预览 / diff；
			// 与采集面同挂 token↔namespace + identity 鉴权中间件（未确认 403），归属以注入的权威身份为准；
			// 内容转存内存中继唤醒等待的 admin，**绝不落库**。与其它 agent 面端点一致无条件注册（assetHandler 恒构造）。
			r.With(agentV2ReportMiddleware(h.V2)).Post("/assets/content", h.Asset.ReceiveContent)
		})
	}

	// 管理台登录：签发令牌，自身不挂令牌中间件
	r.Post("/admin/v1/auth/login", h.Auth.Login)

	if h.V2 != nil {
		r.Route("/admin/v2", func(r chi.Router) {
			r.Use(adminAuthMiddleware(authn, apiKeys))
			r.Use(readonlyWriteGuard)
			r.Use(auditWriteMiddleware(audit))

			r.Get("/namespaces", h.V2.ListNamespaces)
			r.Post("/namespaces", h.V2.CreateNamespace)
			r.Get("/namespace-trusts", h.V2.ListNamespaceTrusts)
			r.Post("/namespace-trusts", h.V2.GrantNamespaceTrust)
			r.Post("/namespace-trusts/{id}/revoke", h.V2.RevokeNamespaceTrust)

			// env 展示维度（FR-178，见 v2-zone-authority.md §5）：env 增删改 + 整体替换 env→namespace 映射。
			// env 是纯展示 / 过滤维度，不参与隔离 / 调度 / 配置作用域链；写端点由 EnvService 在事务内自记专项审计
			// （env.create / update / delete / set-namespaces），已登记 coveredWriteRoutes 使兜底跳过、避免双记。
			// 与 /admin/v2 各 handler 一致无条件注册（h.Env 仅请求期解引用，构造期取方法值安全）。
			r.Get("/envs", h.Env.List)
			r.Post("/envs", h.Env.Create)
			r.Patch("/envs/{id}", h.Env.Update)
			r.Delete("/envs/{id}", h.Env.Delete)
			r.Put("/envs/{id}/namespaces", h.Env.SetNamespaces)
			r.Get("/agent-identities", h.V2.ListAgentIdentities)
			r.Get("/agent-identities/{identityId}", h.V2.GetAgentIdentity)
			r.Post("/agent-identities/{identityId}/approve", h.V2.ApproveAgentIdentity)
			r.Post("/agent-identities/{identityId}/reject", h.V2.RejectAgentIdentity)
			r.Post("/agent-identities/{identityId}/allow-reapply", h.V2.AllowAgentIdentityReapply)
			r.Post("/agent-identities/{identityId}/disable", h.V2.DisableAgentIdentity)
			r.Post("/agent-identities/{identityId}/enable", h.V2.EnableAgentIdentity)
			r.Post("/agent-identities/{identityId}/unbind", h.V2.UnbindAgentIdentity)
			r.Post("/agent-identities/{identityId}/resolve-conflict", h.V2.ResolveAgentIdentityConflict)

			// 健康与指标管理端点（FR-147，见 §5.2）
			if h.V2Health != nil {
				// 集群聚合概览 / 单服时序（serverId 必填、step 服务端桶聚合、跨日并表）
				r.Get("/metrics/summary", h.V2Health.MetricsSummary)
				r.Get("/metrics/series", h.V2Health.MetricsSeries)
				// 当前健康列表（内存实时，分页 + 筛选）；快照回放为静态路由，先于 {serverId} 通配
				r.Get("/health", h.V2Health.ListHealth)
				r.Get("/health/snapshots", h.V2Health.ListHealthSnapshots)
				r.Get("/health/{serverId}", h.V2Health.GetHealthDetail)
				// 健康权重版本化配置：读当前 + 历史 / 全量替换热更（校验 → 镜像 + 新 rev + 审计）
				r.Get("/settings/health-weights", h.V2Health.GetHealthWeights)
				r.Put("/settings/health-weights", h.V2Health.PutHealthWeights)
			}

			r.Post("/bc-clusters", h.V2.CreateBCCluster)
			r.Post("/regions", h.V2.CreateRegion)
			r.Post("/zones", h.V2.CreateZone)
			// 区服结构树只读聚合（FR-155）
			r.Get("/zone-tree", h.V2.ZoneTree)
			r.Get("/servers", h.V2.ListServers)
			r.Post("/server-assignments", h.V2.AssignServers)
			// 换区工单（FR-155）：已分配 server 改归属，解绑重确认编排
			r.Post("/server-rezones", h.V2.RezoneServers)
			// server 排空标记（业务 serverId）与默认入口（行数字 id）；chi 同段参数名须一致，统一用 serverRef
			r.Put("/servers/{serverRef}/draining", h.V2.SetServerDraining)
			r.Put("/servers/{serverRef}/default-entry", h.V2.SetServerDefaultEntry)

			// P4b 挂载点：调度决策管理端点（FR-146，见 §5.2）：决策记录跨日分页查询 /
			// 概览聚合 / 单条详情（静态 summary 置于 {traceId} 前，chi 静态路由本就优先，此处仅为可读性）。
			if h.SchedDecision != nil {
				r.Get("/sched-decisions", h.SchedDecision.List)
				r.Get("/sched-decisions/summary", h.SchedDecision.Summary)
				r.Get("/sched-decisions/{traceId}", h.SchedDecision.Detail)
			}

			// 连接明细管理面查询端点（FR-145，见 spec §5.2）：connId 直查或条件游标分页 / 单条详情 / 时间桶聚合。
			// 静态 stats 置于 {connId} 前（chi 静态路由本就优先，此处仅为可读性）。查询防护见 §4.3。
			if h.V2ConnectionAdmin != nil {
				r.Get("/connections", h.V2ConnectionAdmin.List)
				r.Get("/connections/stats", h.V2ConnectionAdmin.Stats)
				r.Get("/connections/{connId}", h.V2ConnectionAdmin.Detail)
			}

			// 跨服消息管理面查询端点（FR-149/150，见 spec §5.2）：messageId/correlationId 直查或条件游标分页 /
			// 详情（hops + 关联摘要）/ 异常链路聚合（/topology 数据源）/ payload 受控查看（权限点 message.payload.view：
			// POST 属写方法，readonly 经上面 readonlyWriteGuard 403）+ 必填原因 + 先审计后返回。列表与详情永不含 payload。
			// payload 端点由 service 自记 message.payload.view 专项审计（detail 不含 payload）。
			if h.V2MessageAdmin != nil {
				r.Get("/messages", h.V2MessageAdmin.List)
				r.Get("/messages/stats", h.V2MessageAdmin.Stats)
				r.Get("/messages/{messageId}", h.V2MessageAdmin.Detail)
				r.Post("/messages/{messageId}/payload", h.V2MessageAdmin.Payload)
			}

			// 热冷归档管理面端点（FR-153，见 spec §5、ADR-0066）：总览 / 建任务 / 列表 / 详情 / 重试 / 取消。
			// 写端点（建 / 重试 / 取消）为 POST，readonly 经上面 readonlyWriteGuard 403；创建 / 重试 / 取消的
			// 专项审计由 ArchiveService 在事务内自记（archive.job-create / -retry / -cancel），与其它 v2 写端点自审一致。
			// 与 /admin/v1 各 handler 一致无条件注册（V2Archive 在 main.go 恒构造；handler 仅请求期解引用），
			// 静态 overview / jobs 置于 {id} 前（chi 静态路由本就优先，此处仅为可读性）。
			r.Get("/archive/overview", h.V2Archive.Overview)
			r.Post("/archive/jobs", h.V2Archive.CreateJob)
			r.Get("/archive/jobs", h.V2Archive.ListJobs)
			r.Get("/archive/jobs/{id}", h.V2Archive.GetJob)
			r.Post("/archive/jobs/{id}/retry", h.V2Archive.RetryJob)
			r.Post("/archive/jobs/{id}/cancel", h.V2Archive.CancelJob)

			// 配置中心 V2（FR-160/161，spec §5 全 17 端点）：文件 CRUD / 回收站 / 五层作用域版本链 /
			// 有效解析 / diff / 校验。写端点专项审计由 ConfigCenterService 在事务内自记并登记 coveredWriteRoutes；
			// validate 为只读校验（POST 方法），按 spec §4.4 刻意不落库不审计，登记覆盖集使兜底跳过。
			// 静态 /config-files/trash 置于 {id} 前（chi 静态路由本就优先，此处仅为可读性）。
			r.Get("/config-files/trash", h.V2ConfigCenter.Trash)
			r.Get("/config-files", h.V2ConfigCenter.List)
			r.Post("/config-files", h.V2ConfigCenter.Create)
			r.Get("/config-files/{id}", h.V2ConfigCenter.Get)
			r.Patch("/config-files/{id}", h.V2ConfigCenter.Patch)
			r.Delete("/config-files/{id}", h.V2ConfigCenter.Delete)
			r.Post("/config-files/{id}/restore", h.V2ConfigCenter.Restore)
			r.Post("/config-files/{id}/purge", h.V2ConfigCenter.Purge)
			r.Get("/config-files/{id}/scopes", h.V2ConfigCenter.Scopes)
			r.Delete("/config-files/{id}/scopes/{scopeLevel}/{scopeRefId}", h.V2ConfigCenter.RemoveScope)
			r.Get("/config-files/{id}/versions", h.V2ConfigCenter.ListVersions)
			r.Post("/config-files/{id}/versions", h.V2ConfigCenter.SaveVersion)
			r.Post("/config-files/{id}/validate", h.V2ConfigCenter.Validate)
			r.Get("/config-files/{id}/effective", h.V2ConfigCenter.Effective)
			r.Get("/config-files/{id}/diff", h.V2ConfigCenter.Diff)
			r.Get("/config-versions/{versionId}", h.V2ConfigCenter.GetVersion)
			r.Post("/config-versions/{versionId}/rollback", h.V2ConfigCenter.Rollback)

			// 文件资产索引管理面（FR-163，见 §5.2）：搜索 / 每服概要 / 跨服比对（只读 GET）+ 批量重扫下发（POST 写）。
			// rescan 为写方法，readonly 经上面 readonlyWriteGuard 403；其 asset.rescan 专项审计由 AssetService 在事务内自记，
			// 已登记 coveredWriteRoutes 使兜底审计跳过、避免双记。抽成函数注册（内部 nil 守卫），避免内联 if 触发 nestif。
			registerV2AssetsAdminRoutes(r, h)
			// 文件资产内容预览 / diff / 敏感规则（FR-164，见 spec §5.2）：控制面不存文件内容，preview/diff 经命令
			// 下发向 agent 现取。preview/diff 为 POST（有建命令 / 唤醒 agent 写副作用，readonly 经 readonlyWriteGuard 403），
			// service 内在成功读取后自记 asset.preview / asset.diff 专项审计（先审计后返回，detail 绝不含文件内容）。
			// 敏感规则读写底层设置项 assets.sensitive-path-patterns（/settings 不重复暴露），PUT 自记 asset.sensitive_rule_update。
			// 三个写端点登记 coveredWriteRoutes 使兜底审计跳过、避免双记。与 archive / config-center 一致无条件注册
			// （assetHandler 在 main.go 恒构造；handler 仅请求期解引用，构造期不调用）。
			r.Post("/assets/preview", h.Asset.Preview)
			r.Post("/assets/diff", h.Asset.Diff)
			r.Get("/assets/sensitive-rules", h.Asset.GetSensitiveRules)
			r.Put("/assets/sensitive-rules", h.Asset.PutSensitiveRules)

			// 交付编排 V2 变更单（FR-162，spec §5.1；M1：组单 / 差异扫描 / 影响预览 / 审批链路 + 读端点）。
			// 写端点专项审计由 Delivery 两服务在事务内自记（delivery.order.*）并登记 coveredWriteRoutes；
			// M3+ 的 start / pause / resume / cancel / batches confirm / rollback 端点本切片不建路由。
			// 抽成函数注册（内部 nil 守卫），避免本组内联累加触发 nestif。
			registerV2DeliveryAdminRoutes(r, h)
		})
	}

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
		// 在线实例只读文件浏览（FR-110，见 ADR-0049 决策 9）：经命令生命周期代理列目录 / 读子树 / 读单文件。
		// 方法是 GET 但有写副作用（建命令 / 唤醒 agent / 入审计），故显式挂 requireFullRole 挡 readonly（403）；
		// 触发已在 service 内记 file.browse 专项审计（兜底审计中间件只覆盖写方法、GET 不进，无双记之虞）。
		r.With(requireFullRole).Get("/instances/{serverId}/browse", h.Browse.Browse)
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

		// 多级灰度文件同步中心（FR-129/FR-131）：任务真源 + 目标规划 + 控制动作 + 管理台 SSE。
		r.Get("/file-sync/tasks", h.FileSync.List)
		r.Post("/file-sync/tasks", h.FileSync.Create)
		r.Get("/file-sync/tasks/{id}", h.FileSync.Get)
		r.Post("/file-sync/tasks/{id}/plan", h.FileSync.Plan)
		r.Post("/file-sync/tasks/{id}/start", h.FileSync.Start)
		r.Post("/file-sync/tasks/{id}/pause", h.FileSync.Pause)
		r.Post("/file-sync/tasks/{id}/resume", h.FileSync.Resume)
		r.Post("/file-sync/tasks/{id}/terminate", h.FileSync.Terminate)
		r.Get("/file-sync/tasks/{id}/events", h.FileSync.Events)

		// 集群拓扑（FR-37）：bc→bukkit 真实连线 + 大区/zone 分组，读内存注册表快照
		r.Get("/topology", h.Topology.Topology)

		// 健康告警站内信（FR-28）
		r.Get("/alerts", h.Alert.List)
		// 告警历史 / 事件信息流（FR-89，见 ADR-0041）：持久化的告警事件按类型/级别/环境/时间过滤分页查询
		r.Get("/alert-events", h.AlertEvent.List)
		// 告警事件处理工作流（FR-157，见 ADR-0064）：确认 / 标记已处理（写方法，readonly 经 readonlyWriteGuard 403）；
		// service 内在事务中更新 status/handledBy/handledAt/handleNote 并写专项审计（含操作者 / 事件 id / 动作 / 原因）。
		r.Post("/alert-events/{id}/handle", h.AlertEvent.Handle)

		// zone 分配
		r.Get("/zones/assignments", h.Zone.ListAssignments)
		r.Put("/zones/assignments", h.Zone.Assign)
		r.Delete("/zones/assignments", h.Zone.Unassign)
		// 小区默认入口只读列表（FR-48）：真源为 v2 server.is_default_entry（ADR-0067）。
		// v1 写端点已移除（从未有 UI 调用者、写的表无人消费即静默失效陷阱）；写走 v2 分配 / toggle 端点。
		r.Get("/zones/default-entry", h.Zone.ListDefaultEntries)
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

		// 命令观测 / 审查（FR-104，增强 FR-17/FR-82）：观测控制面↔agent 控制命令的双向生命周期。
		// 只读 GET（full/readonly 皆可见，无需 readonlyWriteGuard，但仍走鉴权链）：
		// 列表（按 namespace/serverId/type/status/时间过滤 + 分页）+ 聚合（计数 + 趋势）。
		r.Get("/commands", h.CommandObserve.List)
		// 静态子路由置于 /commands 之后（无 {id} 通配冲突，此处仅为可读性分组）
		r.Get("/commands/analytics", h.CommandObserve.Analytics)

		// 控制面在线更新（FR-99，见 ADR-0044）：检查（只读、服务端缓存 + ?force 刷新）/ 状态（读内存进度）/ 触发应用。
		// POST 为写方法，readonly 经 readonlyWriteGuard 403，审计复用 system.update-apply（已登记 FR-72 覆盖集）。
		// 与其它写端点一致无条件注册（handler 仅请求期解引用，构造期不调用）。
		r.Get("/system/update-check", h.Update.Check)
		r.Get("/system/update", h.Update.Status)
		// 代理连通测试（FR-124，只读诊断）：用已配 update.proxy-url 试连 GitHub，回 {ok, message?}。
		r.Get("/system/proxy-test", h.Update.ProxyTest)
		r.Post("/system/update", h.Update.Apply)
		// 取消进行中的更新下载（FR-125）：写方法，readonly 经 readonlyWriteGuard 403，核心于下载中断时审计 system.update-cancel。
		r.Post("/system/update/cancel", h.Update.Cancel)
		// 手动回滚到上一版本（FR-120）：写方法，readonly 经 readonlyWriteGuard 403，审计 system.update-rollback（已登记 FR-72 覆盖集）。
		r.Post("/system/rollback", h.Update.Rollback)

		// 运维设置 store（FR-61，见 ADR-0038）：列全部热改项（读）+ 改单项（写，readonly 403，入审计）。
		// 与其它写端点一致无条件注册（handler 仅请求期解引用），PUT 已登记 FR-72 覆盖集。
		r.Get("/settings", h.Settings.List)
		r.Put("/settings/{key}", h.Settings.Update)

		// 配置操作级撤回子系统（FR-116，见 ADR-0051）：列可逆操作账目（读，供工作台操作日志）+
		// 撤回单条（写，幂等）。撤回有写副作用（回滚版本指针 / 软删受管项 + 唤醒重推 + 入审计），
		// 故显式挂 requireFullRole 挡 readonly（403）；撤回已在 service 内记 config.undo-* 专项审计（兜底审计中间件只覆盖写方法，无双记）。
		if h.ReversibleOp != nil {
			r.Get("/reversible-operations", h.ReversibleOp.List)
			r.With(requireFullRole).Post("/reversible-operations/{id}/undo", h.ReversibleOp.Undo)
		}
	})

	// 非 API、非静态文件的路径交给内嵌前端（含 SPA history 回退）
	r.NotFound(h.Web.ServeHTTP)
	return r
}

// registerV2AssetsAgentRoutes 注册文件资产 agent 面清单上报（FR-163，见 §5.1）；V2Assets 未装配则跳过。
// 抽成独立函数使 /beacon/v2/agent 组不因内联 if 累加触发 nestif；挂 v2 上报鉴权中间件（未确认 403）。
func registerV2AssetsAgentRoutes(r chi.Router, h Handlers) {
	if h.V2Assets == nil {
		return
	}
	r.With(agentV2ReportMiddleware(h.V2)).Post("/assets/manifest", h.V2Assets.Manifest)
}

// registerV2AssetsAdminRoutes 注册文件资产管理面端点（FR-163，见 §5.2）；V2Assets 未装配则跳过。
// 搜索 / 概要 / 比对为只读 GET；rescan 为写方法（readonly 经 readonlyWriteGuard 403，自审计已登记 coveredWriteRoutes）。
func registerV2AssetsAdminRoutes(r chi.Router, h Handlers) {
	if h.V2Assets == nil {
		return
	}
	r.Get("/assets", h.V2Assets.Search)
	r.Get("/assets/scan-status", h.V2Assets.ScanStatus)
	r.Get("/assets/compare", h.V2Assets.Compare)
	r.Post("/assets/rescan", h.V2Assets.Rescan)
}

// registerV2DeliveryAdminRoutes 注册交付编排变更单管理面端点（FR-162，spec §5.1 的 M1 子集）；
// Delivery 未装配则跳过。静态 diff-scan / impact 等在 {itemId} 通配前（chi 静态路由本就优先，此处仅为可读性）。
// file-diff 是 GET 但带写副作用（建 asset-read 命令 / 唤醒 agent / 记查看审计），显式挂 requireFullRole 挡 readonly。
func registerV2DeliveryAdminRoutes(r chi.Router, h Handlers) {
	if h.Delivery == nil {
		return
	}
	r.Get("/change-orders", h.Delivery.List)
	r.Post("/change-orders", h.Delivery.Create)
	r.Get("/change-orders/{id}", h.Delivery.Get)
	r.Patch("/change-orders/{id}", h.Delivery.Patch)
	r.Delete("/change-orders/{id}", h.Delivery.Delete)
	r.Post("/change-orders/{id}/diff-scan", h.Delivery.DiffScan)
	r.Get("/change-orders/{id}/impact", h.Delivery.Impact)
	r.Post("/change-orders/{id}/submit", h.Delivery.Submit)
	r.Post("/change-orders/{id}/withdraw", h.Delivery.Withdraw)
	r.Post("/change-orders/{id}/approve", h.Delivery.Approve)
	r.Post("/change-orders/{id}/reject", h.Delivery.Reject)
	r.Get("/change-orders/{id}/targets", h.Delivery.Targets)
	r.Get("/change-orders/{id}/observe", h.Delivery.Observe)
	r.Get("/change-orders/{id}/events", h.Delivery.Events)
	r.With(requireFullRole).Get("/change-orders/{id}/items/{itemId}/file-diff", h.Delivery.FileDiff)
}
