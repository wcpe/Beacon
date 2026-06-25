// 命令 beacon 是控制面入口：装配依赖并启动 HTTP 服务。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"syscall"
	"time"

	beacon "github.com/wcpe/Beacon"

	"github.com/wcpe/Beacon/internal/auth"
	"github.com/wcpe/Beacon/internal/config"
	"github.com/wcpe/Beacon/internal/embedweb"
	"github.com/wcpe/Beacon/internal/exitcode"
	"github.com/wcpe/Beacon/internal/gitexport"
	"github.com/wcpe/Beacon/internal/handler"
	"github.com/wcpe/Beacon/internal/httpx"
	"github.com/wcpe/Beacon/internal/metrics"
	"github.com/wcpe/Beacon/internal/pkg/log"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime"
	"github.com/wcpe/Beacon/internal/runtime/alert"
	"github.com/wcpe/Beacon/internal/runtime/longpoll"
	"github.com/wcpe/Beacon/internal/secret"
	"github.com/wcpe/Beacon/internal/server"
	"github.com/wcpe/Beacon/internal/service"
	"github.com/wcpe/Beacon/internal/store"
	"github.com/wcpe/Beacon/internal/update"
	"github.com/wcpe/Beacon/internal/version"
)

// errRequestUpdateRestart 是「请求 launcher 换二进制后重启」的出口哨兵（FR-96 退出码协议出口，见 ADR-0045）。
// FR-97（见 ADR-0044）已接通：更新服务落位 pending 成功后经 updateRestartCh 触发 run 优雅关停并返回本哨兵，
// main 据 errors.Is 以 exitcode.RequestUpdateRestart（70）退出，由 launcher 据约定换二进制重启。
var errRequestUpdateRestart = errors.New("请求更新重启")

func main() {
	// 退出码协议（FR-96，见 internal/exitcode 与 ADR-0045）：正常返回=0、请求更新重启=70、其余致命错误=崩溃码 1。
	// launcher 据退出码决策不重启 / 换二进制后重启 / 崩溃重启。
	err := run()
	switch {
	case err == nil:
		os.Exit(exitcode.OK)
	case errors.Is(err, errRequestUpdateRestart):
		slog.Info("控制面请求更新重启，以约定退出码交还 launcher 换二进制", "退出码", exitcode.RequestUpdateRestart)
		os.Exit(exitcode.RequestUpdateRestart)
	default:
		slog.Error("Beacon 启动失败", "错误", err)
		os.Exit(exitcode.Crash)
	}
}

// run 完成配置加载、依赖装配与服务启动，返回首个致命错误。
func run() error {
	// 进程启动时间：供控制面自身状态页眉计算运行时长（FR-33）。在 run 入口记录，尽量贴近真实启动点。
	startedAt := time.Now().UTC()

	var cfgPath string
	flag.StringVar(&cfgPath, "config", "config.yml", "配置文件路径")
	flag.Parse()

	// 首启脚手架：把配置模板释放为 config.yml 并就地填入随机强鉴权凭据（开箱即跑、config.yml 即真源；
	// 已存在则跳过，绝不覆盖用户文件，FR-25）。凭据不再走自动生成的 .env——避免 .env 静默盖掉 config.yml。
	if released, err := config.EnsureConfigFile(cfgPath, beacon.ConfigExampleYAML); err != nil {
		return err
	} else if released {
		slog.Warn("首次启动：已释放 config.yml（含随机管理员口令与签名密钥，sqlite 可直接运行），请打开它查看 auth.password 后登录管理台", "文件", cfgPath)
	}

	// 从当前目录 .env 加载环境变量（仅填补未设置项，真实环境变量优先）；.env 非自动生成，
	// 仅当运维手动放置时生效，供既有 applyEnv 覆盖链消费（FR-25）
	if err := config.LoadDotEnv(".env"); err != nil {
		return err
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	log.Setup(cfg.Log.Level)
	slog.Info("Beacon 控制面启动中", "版本", version.Version, "监听地址", cfg.HTTPAddr)

	// 管理面鉴权：单操作者认证器（凭据/密钥走配置，env 注入）
	authn, err := auth.New(cfg.Auth.Username, cfg.Auth.Password, cfg.Auth.Secret,
		time.Duration(cfg.Auth.TokenTTLSec)*time.Second)
	if err != nil {
		return err
	}

	db, err := store.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer store.Close(db)

	// 装配：repository → service → handler（手工注入，不引 DI 框架）
	auditRepo := repository.NewAuditLogRepository(db)

	// 运维设置 store（FR-61，见 ADR-0038）：热改项真源由 config.yml 移到 DB store。
	// 启动载入全量缓存 + 首启种子（store 缺该 key 才用 config.yml 值填），之后 store 为热改项真源。
	// 须在各热改消费者（健康扫描 / 采样 / 长轮询 / 告警 / 反向抓取）装配前就绪并注入。
	settingsService, err := service.NewSettingsService(db, repository.NewSettingRepository(db), auditRepo)
	if err != nil {
		return err
	}
	if err := settingsService.SeedFromConfig(cfg); err != nil {
		return err
	}
	settingsHandler := handler.NewSettingsHandler(settingsService)

	nsRepo := repository.NewNamespaceRepository(db)
	// 环境服务（含改名 / 删除守卫，FR-53）依赖注册表 / zone 指派 / 配置仓库查在用数据，
	// 故其构造延后到 registry、assignRepo、configRepo 就绪之后（见下方）。

	// 配置加密 cipher（FR-20）：密钥仅从 env 读，绝不入库 / 不入仓 / 不打日志。
	// 空密钥得到"未启用"cipher；后续若库中已有敏感项则 fail-fast。
	configCipher, err := secret.NewCipher(os.Getenv("BEACON_CONFIG_ENCRYPTION_KEY"))
	if err != nil {
		return err
	}

	configRepo := repository.NewConfigItemRepository(db, configCipher)
	revRepo := repository.NewConfigRevisionRepository(db, configCipher)
	grayRepo := repository.NewConfigGrayRepository(db, configCipher)
	assignRepo := repository.NewZoneAssignmentRepository(db)
	// 小区默认入口（FR-48）：每 zone 唯一指定默认入口 serverId，供 BC 设默认/fallback 服
	defaultEntryRepo := repository.NewZoneDefaultEntryRepository(db)
	// 主动下线拒绝态（FR-49）：server_offline 仓库，供注册前查拒绝表与下线/取消下线落库
	offlineRepo := repository.NewServerOfflineRepository(db)
	configService := service.NewConfigService(db, configRepo, revRepo, auditRepo)
	// 配置灰度 / Beta（FR-9）：复用 configService 发布路径完成 promote，敏感灰度走同一加密边界
	configGrayService := service.NewConfigGrayService(db, configService, configRepo, grayRepo, auditRepo)

	// fail-fast：库中已存在敏感配置项却未配置加密密钥 → 拒绝启动，绝不以密文 / 乱码继续。
	if !configCipher.IsEnabled() {
		n, err := configRepo.CountSensitive()
		if err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("启动失败: 库中存在 %d 个敏感配置项，但未配置加密密钥 BEACON_CONFIG_ENCRYPTION_KEY（base64 的 32 字节），无法解密下发", n)
		}
	}

	// 文件树托管（通道B）：file_object/file_revision 仓库 + 服务
	fileRepo := repository.NewFileObjectRepository(db)
	fileRevRepo := repository.NewFileRevisionRepository(db)
	fileService := service.NewFileService(db, fileRepo, fileRevRepo, auditRepo)

	// 三方插件文件覆盖兼容（FR-15）：覆盖集仓库 + 服务（存"目标根 + 受限重载命令 + 成员清单"事实，提供 dry-run 预览）
	overrideSetRepo := repository.NewFileOverrideSetRepository(db)
	overrideSetRevRepo := repository.NewFileOverrideSetRevisionRepository(db)
	overrideSetService := service.NewOverrideSetService(db, overrideSetRepo, overrideSetRevRepo, fileRepo, auditRepo)
	overrideSetHandler := handler.NewOverrideSetHandler(overrideSetService)

	// 注册/健康运行态：内存注册表 + 健康扫描（注册/健康的内存真源）
	registry := runtime.NewRegistry()

	// 环境服务（FR-53）：registry / assignRepo / configRepo / fileRepo / overrideSetRepo 就绪后构造，供删除守卫查在用数据
	nsService := service.NewNamespaceService(db, nsRepo, assignRepo, configRepo, fileRepo, overrideSetRepo, registry, auditRepo)
	if err := nsService.SeedDefaults(); err != nil {
		return err
	}
	nsHandler := handler.NewNamespaceHandler(nsService)

	// 心跳周期仍为启动期固定项（agent 注册时一次性下发，非热改白名单内）；
	// ttl 供实例服务做注册期重复守卫，取设置 store 当前值（FR-61 健康阈值已移入 store）。
	heartbeatInterval := time.Duration(cfg.Health.HeartbeatIntervalSec) * time.Second
	ttl := time.Duration(settingsService.GetInt(service.SettingHealthTTLSec)) * time.Second

	// 告警事件留痕（FR-89，ADR-0041）：把每条告警额外落 alert_event 供管理台「事件」页历史信息流。
	alertEventService := service.NewAlertEventService(repository.NewAlertEventRepository(db))
	// 健康告警通道（FR-28，ADR-0019）：站内信常驻；webhook 通道恒挂载、靠设置 store 的 url 空与否动态启停（FR-61）；
	// persist 通道把告警额外留痕（FR-89，落库失败仅 WARN、不阻断扫描，见 Dispatcher 兜错）。
	inbox := alert.NewInboxAlerter(cfg.Alert.InboxCapacity)
	alertChannels := []alert.Alerter{inbox, alert.NewWebhookAlerter(settingsService), alert.NewPersistAlerter(alertEventService)}
	// 健康阈值 / 扫描周期由健康扫描器每轮从设置 store 读、热生效（FR-61）。
	healthScanner := runtime.NewHealthScanner(
		registry, settingsService, alert.NewDispatcher(alertChannels...))

	// 可观测性指标（注册/健康 gauge 抓取时读内存注册表；发布/推送 counter 由事件处自增，见 ADR-0020）
	metricsSet := metrics.New(registry)

	instanceService := service.NewInstanceService(db, registry, assignRepo, offlineRepo, auditRepo, heartbeatInterval, ttl)
	zoneService := service.NewZoneService(db, assignRepo, defaultEntryRepo, auditRepo, registry)
	// 发现/实例视图按小区默认入口标 zoneDefaultEntry（FR-48）：解析器由 zoneService 提供（handler 不碰仓库）
	instanceService.SetDefaultEntryResolver(zoneService.DefaultEntryServerIDs)

	// 负载指标看板（FR-32，ADR-0023）：metric_sample 仓库 + 服务（聚合实时读注册表、趋势查库降采样）
	metricRepo := repository.NewMetricSampleRepository(db)
	metricService := service.NewMetricService(registry, metricRepo)
	metricHandler := handler.NewMetricHandler(metricService)
	// 采样器：按间隔对在线实例采样落库 + 按保留期清理（开关 / 间隔 / 保留期从设置 store 读、热生效，FR-61）。
	// 恒启动常驻：每轮读 metric.enabled，false 则跳过本轮采样 / 清理（不再启动期一次性决定起不起）。
	metricSampler := service.NewMetricSampler(registry, metricRepo, settingsService)

	// 控制面自身状态页眉（FR-33）：DB 连通经底层连接池 Ping（不经 GORM 业务路径），在线实例数读内存注册表。
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("获取底层连接池失败: %w", err)
	}
	// 进程 CPU% 采样器（gopsutil）：构造时预热一次基线，端点每次取自上次调用以来的占比。
	cpuSampler := service.NewGopsutilCPUSampler()
	// 采样器启用状态从设置 store 读、热生效（FR-61）：metric.enabled 改了页眉即反映新值。
	systemService := service.NewSystemService(version.Version, startedAt, sqlDB, registry,
		func() bool { return settingsService.GetBool(service.SettingMetricEnabled) }, cpuSampler)
	systemHandler := handler.NewSystemHandler(systemService)

	// 流量调度（FR-10）：drain 标记落 DB + 落位建议（query-only），控制面只给决策不执行玩家连接（ADR-0017）
	drainRepo := repository.NewServerDrainRepository(db)
	schedulingService := service.NewSchedulingService(db, drainRepo, auditRepo, registry)

	// 长轮询：配置与文件各持独立 Hub（唤醒集合分开，互不触发无谓重算）+ 有效解析 + 事务后唤醒
	hub := longpoll.NewHub()
	fileHub := longpoll.NewHub()
	// 拓扑 watch（FR-29）：namespace 级唤醒 Hub，与配置/文件独立；实例上线/下线/改派时唤醒订阅方
	topologyHub := longpoll.NewHub()
	// 命令待办（FR-39）：serverId 级唤醒 Hub，与上面三通道独立；建反向抓取命令时唤醒目标 agent 的 SSE 流
	commandHub := longpoll.NewHub()
	// revRepo 注入供 per-server 有效配置变更时间线聚合该服覆盖链各 config 项的发布历史（FR-80）
	effectiveService := service.NewEffectiveService(configRepo, assignRepo, grayRepo, revRepo, hub)
	// 发布影响面预览（FR-79）：registry（在线真源）+ assignRepo（zone 归属真源）求交算受影响在线子服
	impactService := service.NewImpactService(registry, assignRepo)
	// 配置 admin 处理器持有 effectiveService 以支持有效配置只读预览（FR-22）+ 灰度 svc（FR-9）+ 影响面预览（FR-79）
	configHandler := handler.NewConfigHandler(configService, effectiveService, configGrayService, impactService)
	fileEffectiveService := service.NewFileEffectiveService(fileRepo, assignRepo, fileHub)
	// 三方覆盖集投递（FR-15）：复用 fileHub 唤醒集合（同属通道B），解析适用覆盖集 + 成员内容
	overrideEffectiveService := service.NewOverrideEffectiveService(overrideSetRepo, fileRepo, assignRepo, fileHub)
	notifier := service.NewChangeNotifier(hub, fileHub, topologyHub, commandHub, registry, assignRepo)
	notifier.SetMetrics(metricsSet)
	configService.SetNotifier(notifier)
	configService.SetMetrics(metricsSet)
	// 灰度发布 / promote / abort 提交后按受影响 serverId 唤醒（复用配置通道 Hub，FR-9）
	configGrayService.SetNotifier(notifier)
	// promote 走发布路径，同样计入 beacon_config_publish_total（FR-30）
	configGrayService.SetMetrics(metricsSet)
	fileService.SetNotifier(notifier)
	overrideSetService.SetNotifier(notifier)
	zoneService.SetNotifier(notifier)
	// 实例注册/下线唤醒拓扑 watch；健康扫描转 lost/offline 也唤醒（FR-29）
	instanceService.SetNotifier(notifier)
	healthScanner.SetTopologyNotifier(notifier)

	// 单条 SSE 推送流（FR-24）：合并配置/文件树/覆盖集三条长轮询 + 拓扑 watch（FR-29），复用同源唤醒集合 + 连接即对账。
	// 保活间隔取长轮询挂起上限（longpoll.max-hold-ms）：从设置 store 读、热生效（FR-61）。
	streamService := service.NewStreamService(effectiveService, fileEffectiveService, overrideEffectiveService, registry, hub, fileHub, topologyHub, commandHub, settingsService)

	// agent / file 长轮询挂起点的挂起上限从设置 store 读、热生效（FR-61）。
	agentHandler := handler.NewAgentHandler(instanceService, effectiveService, settingsService)
	streamHandler := handler.NewStreamHandler(instanceService, streamService)
	fileHandler := handler.NewFileHandler(fileService, fileEffectiveService, overrideEffectiveService, instanceService, settingsService)
	// 实例视图渲染健康原因（FR-81）须读当前健康阈值（设置 store 热改项 FR-61），故注入 settingsService；
	// effectiveService 供 per-server 有效配置变更时间线端点（FR-80）。
	instanceHandler := handler.NewInstanceHandler(instanceService, settingsService, effectiveService)
	topologyHandler := handler.NewTopologyHandler(service.NewTopologyService(registry))
	zoneHandler := handler.NewZoneHandler(zoneService)
	schedulingHandler := handler.NewSchedulingHandler(schedulingService)
	auditHandler := handler.NewAuditHandler(service.NewAuditService(auditRepo))
	alertHandler := handler.NewAlertHandler(inbox)
	alertEventHandler := handler.NewAlertEventHandler(alertEventService)
	authHandler := handler.NewAuthHandler(authn, service.NewAuthAuditService(auditRepo))

	// 管理面 API 密钥（FR-42，见 ADR-0026）：运行时创建/吊销/重置 + 只读角色，落库只存哈希。
	// apiKeyService 同时作为 API 密钥校验器注入鉴权中间件（真源在库、查库比对哈希、不引会话存储）。
	apiKeyRepo := repository.NewAPIKeyRepository(db)
	apiKeyService := service.NewAPIKeyService(db, apiKeyRepo, auditRepo)
	apiKeyHandler := handler.NewAPIKeyHandler(apiKeyService)

	// 配置导入·在线实例反向抓取（FR-39，见 ADR-0027）：命令仓库 + 服务（建命令 / 拉取 / ingest 复用 FileService.Import）+ 处理器。
	// 建命令提交后经 notifier 唤醒目标 agent 的 SSE 流发 command-pending。
	commandRepo := repository.NewAgentCommandRepository(db)
	commandService := service.NewAgentCommandService(db, commandRepo, fileService, auditRepo)
	commandService.SetNotifier(notifier)
	// 按需拓印 diff 取期望合并值复用 FR-45 有效文件树解析（FR-46）。
	commandService.SetFileEffectiveService(fileEffectiveService)
	commandHandler := handler.NewCommandHandler(commandService, instanceService)

	// 取 agent 日志（FR-88，见 ADR-0040）：编排取自身脱敏日志的命令-回传周期（触发 + 单活跃限速 + 回传转存瞬态 + 查询）。
	// 复用同一 agent_command 通路（tail-logs 类型），命令提交后经 notifier 唤醒目标 agent。
	agentLogService := service.NewAgentLogService(db, commandRepo, auditRepo)
	agentLogService.SetNotifier(notifier)
	agentLogHandler := handler.NewAgentLogHandler(agentLogService, instanceService)

	// 控制面自观测页（FR-82）：聚合控制面进程内部运行态——DB 连接池（与 FR-33 同一 sqlDB）、
	// 长轮询四通道挂起数（配置 / 文件 / 拓扑 / 命令 Hub）、注册表规模（按健康状态）、命令队列深度（按状态）。
	// 只读、不参与决策；区别于 FR-33 页眉条与 FR-32 agent 网络负载。
	observabilityService := service.NewObservabilityService(sqlDB, registry, hub, fileHub, topologyHub, commandHub, commandRepo)
	observabilityHandler := handler.NewObservabilityHandler(observabilityService)

	// 反向抓取受管任务（FR-58，见 ADR-0037）：任务仓库 + 服务（建任务 + 单实例互斥、scan 回传存清单、
	// submit 编排、ingest 复用 FileService.Import 落库、取消、过期）+ 处理器。任务是真源、命令是其执行手段。
	reverseFetchTaskRepo := repository.NewReverseFetchTaskRepository(db)
	// 反向抓取单文件上限从设置 store 读、热生效（FR-61）：ReceiveScan 用该上限 + agent size 重算 overThreshold。
	reverseFetchTaskService := service.NewReverseFetchTaskService(db, reverseFetchTaskRepo, commandRepo, fileService, auditRepo, settingsService)
	reverseFetchTaskService.SetNotifier(notifier)
	// agent 复用同一 /files/ingest 端点回传 submit 选定内容，控制面据命令 mode=submit 转交受管任务编排落库。
	commandService.SetSubmitIngestReceiver(reverseFetchTaskService)
	// 反向抓取持久忽略规则（FR-59）：规则仓库 + 服务（建 / 列 / 删 + 审计），供扫描清单标 ignoredByRule。
	reverseFetchIgnoreRuleRepo := repository.NewReverseFetchIgnoreRuleRepository(db)
	reverseFetchIgnoreRuleService := service.NewReverseFetchIgnoreRuleService(db, reverseFetchIgnoreRuleRepo, auditRepo)
	reverseFetchTaskHandler := handler.NewReverseFetchTaskHandler(reverseFetchTaskService, instanceService, reverseFetchIgnoreRuleService)
	reverseFetchIgnoreRuleHandler := handler.NewReverseFetchIgnoreRuleHandler(reverseFetchIgnoreRuleService)

	// 陈旧命令后台清理（FR-39/FR-46）：周期把创建超期仍未终结的命令标 expired 并清空拓印瞬态明文，避免放弃的 ready 命令明文滞留。
	commandSweeper := service.NewCommandSweeper(commandService)
	// 陈旧受管任务后台清理（FR-58）：周期把创建超期仍未终结的任务标 expired 并清空清单瞬态，避免大树清单 TEXT 长期滞留。
	reverseFetchTaskSweeper := service.NewReverseFetchTaskSweeper(reverseFetchTaskService)

	// git 单向导出镜像（FR-47，见 ADR-0030）：发布 / 回滚 / 改派提交后异步 best-effort 把源层导出 commit。
	// 仅 enabled 时装配并接线触发器；git 仓是单向派生镜像、失败仅 WARN 不阻断发布。
	// gitRepo 端口：未启用 NopGitRepo、启用用 go-git 实现的 GoGitRepo（见 newGitRepo / ADR-0030 决策5）。
	exportSourceRepo := repository.NewExportSourceRepository(db)
	gitExportService := service.NewGitExportService(exportSourceRepo, newGitRepo(cfg.GitExport))
	if cfg.GitExport.Enabled {
		configService.SetGitExporter(gitExportService)
		fileService.SetGitExporter(gitExportService)
		zoneService.SetGitExporter(gitExportService)
		slog.Info("git 单向导出镜像已启用", "仓路径", cfg.GitExport.RepoPath,
			"远程", gitRemoteForLog(cfg.GitExport.RemoteURL))
	} else {
		slog.Info("git 单向导出镜像未启用（git-export.enabled=false）")
	}

	// 控制面在线更新核心（FR-97，见 ADR-0044）：按渠道查 Release → 下载 → SHA256 → 落位 pending → 以退出码 70 交还 launcher。
	// updateRestartCh 是「更新就绪请求重启」的进程内信号：更新服务落位 pending 成功后关闭它，
	// run 的 select 据此返回 errRequestUpdateRestart，main 既有 errors.Is 映射到退出码 70（FR-96 已备出口）。
	// 出站经 internal/httpx 工厂（带代理 + 超时，FR-98）；代理地址由触发端点（FR-99）从设置 store 读后传入。
	updateRestartCh := make(chan struct{})
	updateService := update.NewService(update.Config{
		CurrentVersion: version.Version,
		PendingPath:    resolvePendingPath(),
		NewHTTPClient:  httpx.NewClient,
		// 仅关一次：sync.Once 保证多次触发不重复关闭 channel（panic 防护）。
		RequestRestart: sync.OnceFunc(func() { close(updateRestartCh) }),
		Audit:          auditRepo,
	})
	// HTTP 触发面（FR-99，见 ADR-0044）：把更新核心接到 admin 端点——检查（只读、服务端缓存 + ?force 刷新）/
	// 状态（读内存进度）/ 触发应用（写、readonly 403 + 审计）。渠道 / 代理 / 缓存 TTL 从设置 store 读、热生效（FR-101）。
	updateAPIService := service.NewUpdateService(updateService, settingsService)
	updateHandler := handler.NewUpdateHandler(updateAPIService)
	slog.Info("控制面在线更新核心已就绪",
		"初始阶段", string(updateService.Snapshot().Phase), "pending 路径", resolvePendingPath())

	// 内嵌前端：去掉 web/dist 前缀后交给 SPA 处理器
	dist, err := fs.Sub(beacon.WebDist, "web/dist")
	if err != nil {
		return err
	}
	router := server.NewRouter(server.Handlers{
		Namespace: nsHandler, Config: configHandler, File: fileHandler, OverrideSet: overrideSetHandler,
		Agent: agentHandler, Stream: streamHandler, Instance: instanceHandler, Topology: topologyHandler, Zone: zoneHandler, Scheduling: schedulingHandler,
		Audit: auditHandler, Alert: alertHandler, AlertEvent: alertEventHandler, Metric: metricHandler, System: systemHandler, Observability: observabilityHandler, Update: updateHandler, Auth: authHandler, APIKey: apiKeyHandler, Command: commandHandler, AgentLog: agentLogHandler, ReverseFetchTask: reverseFetchTaskHandler, ReverseFetchRule: reverseFetchIgnoreRuleHandler, Settings: settingsHandler, Metrics: metricsSet.Handler(), Web: embedweb.Handler(dist),
	}, cfg.AgentToken, authn, apiKeyService, auditRepo)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 启动后台健康扫描（随关停信号取消退出）
	go healthScanner.Run(ctx)

	// 启动后台指标采样器（FR-32）：恒常驻，每轮从设置 store 读 metric.enabled 决定本轮是否采样 / 清理（FR-61）。
	// 不再启动期一次性决定起不起——运维改 metric.enabled 即热生效停 / 起采样，免重启。
	go metricSampler.Run(ctx)

	// 启动 git 导出 worker（FR-47）：单 worker 串行消费导出信号，随关停信号退出；未启用时也起（空转无害、无信号即不动）
	if cfg.GitExport.Enabled {
		go gitExportService.Run(ctx.Done())
	}

	// 启动陈旧命令清理器（FR-39/FR-46）：常驻 hygiene，随关停信号退出
	go commandSweeper.Run(ctx)

	// 启动陈旧受管任务清理器（FR-58）：常驻 hygiene，把超期未终结任务标 expired 并清空清单瞬态，随关停信号退出
	go reverseFetchTaskSweeper.Run(ctx)

	errCh := make(chan error, 1)
	go func() {
		slog.Info("HTTP 服务已就绪", "地址", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("收到关停信号，开始优雅关停")
		// 给关停一个上限：略大于长轮询上限，到点强制结束
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case <-updateRestartCh:
		// 更新已落位 pending，请求换二进制重启（FR-97，见 ADR-0044）：优雅关停释放端口后返回哨兵，
		// main 据 errors.Is 以退出码 70 退出，交还 launcher 做原子换二进制 + 重启（FR-96/ADR-0045）。
		slog.Info("更新已就绪，优雅关停后以约定退出码交还 launcher 换二进制重启")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return errRequestUpdateRestart
	case err := <-errCh:
		return err
	}
}

// resolvePendingPath 推导 launcher 约定的 pending 新二进制路径（运行二进制同目录 beacon.new[.exe]）。
// 与 cmd/beacon-launcher 的 resolvePaths 同约定（ADR-0045），更新服务据此原子落位、launcher 据此换二进制。
// 解析自身路径失败时回退到工作目录相对名（极少见；落位时若不可写会在更新阶段失败、不影响正常运行）。
func resolvePendingPath() string {
	suffix := ""
	if goruntime.GOOS == "windows" {
		suffix = ".exe"
	}
	self, err := os.Executable()
	if err != nil {
		return "beacon.new" + suffix
	}
	return filepath.Join(filepath.Dir(self), "beacon.new"+suffix)
}

// newGitRepo 按导出配置构造 git 写入端口实现（FR-47，见 ADR-0030 决策5）。
// 未启用即 NopGitRepo（no-op）；启用则用 go-git（纯 Go、契合单二进制 alpine）实现的 GoGitRepo。
// 具体 git 库只在 gitexport.GoGitRepo 适配器里 import，纯逻辑 / 触发链路不依赖之（端口隔离）。
func newGitRepo(cfg config.GitExportConfig) gitexport.GitRepo {
	if !cfg.Enabled {
		return gitexport.NopGitRepo{}
	}
	return gitexport.NewGoGitRepo(gitexport.GoGitRepoConfig{
		RepoPath:     cfg.RepoPath,
		RemoteURL:    cfg.RemoteURL,
		RemoteBranch: cfg.RemoteBranch,
		AuthorName:   cfg.AuthorName,
		AuthorEmail:  cfg.AuthorEmail,
		RemoteToken:  cfg.RemoteToken,
	})
}

// gitRemoteForLog 把远程地址脱敏后用于日志（空则显示「仅本地」，绝不打印可能内嵌的凭据）。
func gitRemoteForLog(remoteURL string) string {
	if remoteURL == "" {
		return "仅本地"
	}
	return remoteURL
}
