// 命令 beacon 是控制面入口：装配依赖并启动 HTTP 服务。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"syscall"
	"time"

	"gorm.io/gorm"

	beacon "github.com/wcpe/Beacon"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/embedweb"
	"github.com/wcpe/Beacon/apps/server/internal/gitexport"
	"github.com/wcpe/Beacon/apps/server/internal/handler"
	"github.com/wcpe/Beacon/apps/server/internal/httpx"
	"github.com/wcpe/Beacon/apps/server/internal/metrics"
	"github.com/wcpe/Beacon/apps/server/internal/pkg/log"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/alert"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/bootwatch"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/longpoll"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/metricwindow"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/roster"
	"github.com/wcpe/Beacon/apps/server/internal/secret"
	"github.com/wcpe/Beacon/apps/server/internal/server"
	"github.com/wcpe/Beacon/apps/server/internal/service"
	"github.com/wcpe/Beacon/apps/server/internal/store"
	"github.com/wcpe/Beacon/apps/server/internal/update"
	"github.com/wcpe/Beacon/apps/server/internal/version"
)

func main() {
	// 单进程自替换模型（FR-119，见 ADR-0053）：正常退出与自替换重启均返回 nil（exit 0），致命错误 exit 1。
	// 进程崩溃的自动重启交外部监督（docker restart / systemd Restart=，见 docs/OPERATIONS.md）。
	if err := run(); err != nil {
		slog.Error("Beacon 退出", "错误", err)
		os.Exit(1)
	}
}

// run 完成配置加载、依赖装配与服务启动，返回首个致命错误。
func run() error {
	// 进程启动时间：供控制面自身状态页眉计算运行时长（FR-33）。在 run 入口记录，尽量贴近真实启动点。
	startedAt := time.Now().UTC()

	// 换版后启动自检 + 自动回滚（FR-119，见 ADR-0053）：新版反复起不来则自动回退上一版本。
	// 须在 HTTP 起之前尽早执行；自身路径解析失败则跳过自检与后续自替换（极罕见，退化为无自动回滚）。
	selfPath, selfErr := os.Executable()
	if selfErr != nil {
		slog.Warn("解析自身可执行路径失败，跳过换版自检与自替换", "错误", selfErr)
	} else {
		update.CheckAndAutoRollback(selfPath)
	}

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

	// 热冷归档核心（FR-151，见 ADR-0066）：第二个独立归档连接 + 任务表 + 后台工作器。
	// 归档库不可达时降级——archiveDB=nil，overview 标不可用、拒绝创建任务，绝不阻断控制面启动（fail-static）。
	archiveDB, archiveInfo := openArchiveConn(cfg.Database, cfg.Archive)
	defer store.Close(archiveDB) // nil 安全（不可达降级时 archiveDB=nil）
	archiveService := service.NewArchiveService(db, archiveDB, archiveInfo,
		repository.NewArchiveJobRepository(db), settingsService, auditRepo)
	// 归档管理面 handler（FR-153，见 spec §5）：挂 /admin/v2/archive/*，薄接 archiveService。
	v2ArchiveHandler := handler.NewV2ArchiveHandler(archiveService)

	// 配置中心 V2（FR-160/161）：文件 + 五层作用域不可变版本链 + 有效解析 / diff / 校验，
	// 挂 /admin/v2/config-files* 与 /admin/v2/config-versions*（spec §5 全 17 端点）。
	configCenterService := service.NewConfigCenterService(db,
		repository.NewConfigFileRepository(db), repository.NewConfigLayerVersionRepository(db), auditRepo)
	v2ConfigCenterHandler := handler.NewV2ConfigCenterHandler(configCenterService)

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
	v2ControlPlaneService := service.NewV2ControlPlaneService(db)
	v2ControlPlaneHandler := handler.NewV2ControlPlaneHandler(v2ControlPlaneService)

	// env 展示维度（FR-178，见 v2-zone-authority.md §3.4/§4.1）：env 增删改 + 整体替换 env→namespace 映射。
	// env 是纯展示 / 过滤维度，不参与隔离判定 / 调度 / 配置作用域链；复用 nsRepo 解析映射 namespace 名与校验存在性。
	envService := service.NewEnvService(db, repository.NewEnvRepository(db), nsRepo, auditRepo)
	envHandler := handler.NewEnvHandler(envService)

	// 心跳周期仍为启动期固定项（agent 注册时一次性下发，非热改白名单内）；
	// ttl 供实例服务做注册期重复守卫，取设置 store 当前值（FR-61 健康阈值已移入 store）。
	heartbeatInterval := time.Duration(cfg.Health.HeartbeatIntervalSec) * time.Second
	ttl := time.Duration(settingsService.GetInt(service.SettingHealthTTLSec)) * time.Second

	// 告警事件留痕（FR-89，ADR-0041）：把每条告警额外落 alert_event 供管理台「事件」页历史信息流。
	alertEventService := service.NewAlertEventService(db, repository.NewAlertEventRepository(db), auditRepo)

	// 并发身份冲突检测（FR-177，spec §4.5）：bootId 活跃注册表（进程内真源，map+锁，不引中间件）+
	// 冲突窗口从设置 store 热读 + 冲突告警复用告警留痕出口。装配后 register/report 路径即启用往复检测。
	bootRegistry := bootwatch.New()
	v2ControlPlaneService.SetConflictWatch(
		bootRegistry,
		func() time.Duration {
			return time.Duration(settingsService.GetInt(service.SettingIdentityConflictWindowSec)) * time.Second
		},
		alertEventService,
	)
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
	zoneService := service.NewZoneService(db, assignRepo, auditRepo, registry)
	// 发现/实例视图按小区默认入口标 zoneDefaultEntry（FR-48）：真源为 v2 server.is_default_entry（ADR-0067），
	// 管理台分配勾选 / toggle 的默认入口经此下发给 BC fallback 注入。
	instanceService.SetDefaultEntryResolver(v2ControlPlaneService.DefaultEntryServerIDs)

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
	// 文件浏览结果（FR-110）：serverId 级唤醒 Hub，与命令待办独立；agent 回传浏览结果时唤醒等待中的 admin 请求。
	// 与 commandHub 分立——commandHub 唤醒 agent 拉命令，browseHub 唤醒 admin 取结果，二者信号不互相干扰。
	browseHub := longpoll.NewHub()
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
	zoneHandler := handler.NewZoneHandler(zoneService, v2ControlPlaneService)
	schedulingHandler := handler.NewSchedulingHandler(schedulingService)
	auditHandler := handler.NewAuditHandler(service.NewAuditService(auditRepo), settingsService)
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

	// 只读文件浏览（FR-110，见 ADR-0049 决策 9）：复用同一 commandService（fs-browse 类型）经命令生命周期代理。
	// 注入 browseHub 供 admin 请求注册结果 waiter、agent 回传后唤醒；命令提交后经 notifier 唤醒目标 agent。
	commandService.SetBrowseResultHub(browseHub)
	browseHandler := handler.NewBrowseHandler(commandService, instanceService)

	// P8 装配点：文件资产索引（FR-163，见 v2-file-assets.md）：agent 面清单上报（增量 / 全量分片，摘要校准）+
	// 管理面搜索 / 概要 / 跨服比对 / 批量重扫。重扫复用同一 commandRepo（asset-rescan 类型）经既有长轮询命令通道下发，
	// 建命令提交后经 notifier 唤醒目标 agent。本域对目标文件系统零写入（只读索引）。
	assetService := service.NewAssetService(db,
		repository.NewFileAssetRepository(db), repository.NewFileAssetScanRepository(db), commandRepo, auditRepo)
	assetService.SetNotifier(notifier)
	v2AssetsHandler := handler.NewV2AssetsHandler(assetService)

	// 文件资产内容预览 / diff / 敏感规则（FR-164，见 spec §4.5/§4.6/§4.7）：控制面不存文件内容，
	// 经 asset-read 命令下发向 agent 现取 + 内存中继同步透传。复用 commandRepo（命令生命周期）/ notifier（唤醒 agent）/
	// instanceService（在线校验）/ auditRepo；结果 waiter 用独立 assetHub（与命令待办 / 浏览结果 Hub 分立，互不干扰）；
	// 敏感规则读写底层设置项 assets.sensitive-path-patterns（非热改白名单、/settings 不重复暴露）。
	assetHub := longpoll.NewHub()
	assetPreviewService := service.NewAssetPreviewService(db, commandRepo, repository.NewFileAssetRepository(db),
		repository.NewSettingRepository(db), auditRepo, assetHub, notifier, instanceService)
	assetHandler := handler.NewAssetHandler(assetPreviewService)

	// 多级灰度文件同步中心（FR-129/FR-131）：当前切片只装配任务真源、目标规划、控制动作与管理台 SSE。
	fileSyncService := service.NewFileSyncService(db, repository.NewFileSyncRepository(db), instanceService, auditRepo, service.NewFileSyncEventHub())
	fileSyncHandler := handler.NewFileSyncHandler(fileSyncService)

	// 取 agent 日志（FR-88，见 ADR-0040）：编排取自身脱敏日志的命令-回传周期（触发 + 单活跃限速 + 回传转存瞬态 + 查询）。
	// 复用同一 agent_command 通路（tail-logs 类型），命令提交后经 notifier 唤醒目标 agent。
	agentLogService := service.NewAgentLogService(db, commandRepo, auditRepo)
	agentLogService.SetNotifier(notifier)
	agentLogHandler := handler.NewAgentLogHandler(agentLogService, instanceService)

	// 控制面自观测页（FR-82）：聚合控制面进程内部运行态——DB 连接池（与 FR-33 同一 sqlDB）、
	// 长轮询四通道挂起数（配置 / 文件 / 拓扑 / 命令 Hub）、注册表规模（按健康状态）、命令队列深度（按状态）。
	// 只读、不参与决策；区别于 FR-33 页眉条与 FR-32 agent 网络负载。
	observabilityService := service.NewObservabilityService(sqlDB, registry, hub, fileHub, topologyHub, commandHub, commandRepo)

	// P4 指标采样入库（FR-144，见 v2-metrics-health-scheduling.md §4）：agent 5s 批上报 → 接收端只校验 +
	// 更 60s 内存窗口 + 非阻塞入队 → 后台写入池事务批插当日日表。请求 goroutine 不碰 DB、队列满回 429 背压。
	// 60s 窗口是「每实例最新指标」内存真源（独立锁），供后续健康计算与 dashboard 实时读消费。
	// 写入通道按路由键分发（§4.3）：指标 / 健康快照 / 调度决策各注册自己的 flusher、各自攒批互不阻塞。
	metricWindow := metricwindow.New(metricwindow.DefaultCapacity)
	metricSampleV2Repo := repository.NewMetricSampleV2Repository(db)
	asyncDailyWriter := service.NewAsyncDailyWriter()
	service.RegisterFlusher(asyncDailyWriter, service.RouteKindMetricSample, metricSampleV2Repo.FlushDaily)
	metricIngestService := service.NewMetricIngestService(metricWindow, service.MetricSampleEnqueuer{Writer: asyncDailyWriter})
	v2MetricsHandler := handler.NewV2MetricsHandler(metricIngestService)
	// 写入失败累计丢弃计数暴露到 /system 自观测（错误不静默，ADR-0057）。
	observabilityService.SetMetricWriteDiscardCounter(asyncDailyWriter)
	observabilityHandler := handler.NewObservabilityHandler(observabilityService)

	// P4b 装配：健康域（FR-147，见 v2-metrics-health-scheduling.md §3.3/§4.4）。
	// 健康权重版本化存储与热更：启动载入最新 rev（表空种子默认配置 rev=1、operator=system），
	// PUT 校验 → 事务内写设置镜像 + 插入新 rev + 写审计 → 提交后内存原子替换（下一轮健康计算生效）。
	healthWeightsService, err := service.NewHealthWeightsService(
		db, repository.NewHealthWeightsRepository(db), repository.NewSettingRepository(db), auditRepo)
	if err != nil {
		return err
	}
	// 健康视图内存真源（§3.6）+ 健康计算轮（§4.4）：每 5s 锁外读 DB 事实、聚合 60s 指标窗口、
	// 整批替换全实例视图；每 6 轮（30s）把全量视图转快照行经异步写入通道落 health_snapshot 日表
	// （flusher 注册必须先于 asyncDailyWriter.Start）。计算轮 goroutine 在下方随关停信号统一启动。
	healthViewStore := healthview.NewStore()
	healthSnapshotRepo := repository.NewHealthSnapshotRepository(db)
	service.RegisterFlusher(asyncDailyWriter, service.RouteKindHealthSnapshot, healthSnapshotRepo.FlushDaily)
	healthComputeService := service.NewHealthComputeService(repository.NewHealthFactsRepository(db),
		metricWindow, healthViewStore, healthWeightsService, service.HealthSnapshotEnqueuer{Writer: asyncDailyWriter},
		alertEventService)
	// 管理面只读查询（§5.2）：实时走内存视图 + 60s 窗口，回放走快照 / 指标日表（缺表跳过、禁隐式建表）。
	healthQueryService := service.NewHealthQueryService(healthViewStore, metricWindow, healthSnapshotRepo, metricSampleV2Repo)
	v2HealthHandler := handler.NewV2HealthHandler(healthQueryService, healthWeightsService, settingsService)
	// 指标上报响应回填自身健康视图（§5.1 self）：接收端注入视图存储，尚无视图时响应 null。
	metricIngestService.SetHealthViews(healthViewStore)

	// P9 装配点：交付编排 V2 变更单 M1（FR-162，见 v2-delivery-orchestration.md §4.1/§4.2/§5.1）。
	// 生命周期（组单 CRUD / 提审 / 撤回 / 审批 / 驳回）与差异面（同步 diff-scan / 影响预览 / file-diff）分服务承载：
	// 差异读文件资产最新快照（不下发重扫）、file-diff 复用 FR-164 安全预览通道（敏感路径 / 在线 / 查看审计）、
	// 影响预览的在线 / 健康读 healthViewStore 内存真源、审批职责分离开关走设置 store 热改。
	changeOrderRepo := repository.NewChangeOrderRepository(db)
	deliveryOrderService := service.NewDeliveryOrderService(db, changeOrderRepo,
		repository.NewConfigLayerVersionRepository(db), auditRepo, settingsService, healthViewStore)
	deliveryDiffService := service.NewDeliveryDiffService(db, changeOrderRepo,
		repository.NewFileAssetRepository(db), auditRepo, assetPreviewService, healthViewStore)

	// P9 M2 交付数据面装配（FR-165，见 ADR-0069）：全局 sha256 内容寻址 blob 中转存储 + 流式 / agent 面端点 + 后台清理器。
	deliveryBlobRepo := repository.NewDeliveryBlobRepository(db)
	deliveryBlobService := service.NewDeliveryBlobService(db, deliveryBlobRepo, changeOrderRepo, commandRepo, settingsService)
	// 配置灰度渲染器装配（ADR-0071）：把 config_change 项按目标渲染为生效明文文件（控制面写内容寻址 blob、
	// restart 读盘生效），只读调用配置中心 EffectivePlaintext 接缝、配置域代码零改动。
	deliveryBlobService.SetConfigRenderer(configCenterService,
		repository.NewConfigLayerVersionRepository(db), repository.NewConfigFileRepository(db))
	deliveryStreamHandler := handler.NewDeliveryStreamHandler(deliveryBlobService)
	deliveryAgentHandler := handler.NewDeliveryAgentHandler(deliveryBlobService)
	deliveryBlobCleaner := service.NewDeliveryBlobCleaner(deliveryBlobService, auditRepo)

	// P9 M3 灰度编排推进器装配（FR-166/171，见 v2-delivery-orchestration.md §4.1/§4.4/§4.6）：
	// 进程内单 goroutine 驱动 rolling 单批次推进 → 命令下发 → 回执驱动三层状态机 → 熔断 / 推进门 → 完成；
	// 回执经 blob 服务 SetProgressWaker 即时唤醒推进器（单一驱动源）、观察窗序列经 SetObserveProvider 供 /observe 接真。
	deliveryOrchestrator := service.NewDeliveryOrchestrator(db, changeOrderRepo, deliveryBlobService,
		commandRepo, auditRepo, healthViewStore, metricWindow, notifier)
	deliveryBlobService.SetProgressWaker(deliveryOrchestrator)
	deliveryOrderService.SetObserveProvider(deliveryOrchestrator)
	deliveryHandler := handler.NewDeliveryAdminHandler(deliveryOrderService, deliveryDiffService, deliveryOrchestrator)

	// 命令观测 / 审查（FR-104，增强 FR-17/FR-82）：复用同一 commandRepo，只读查询 + 聚合控制面↔agent 命令的双向生命周期。
	// 区别于 FR-82 控制面健康（仅命令队列计数）——本服务把队列升级为逐条 + 历史过滤 + 趋势；绝不带出瞬态敏感内容（投影在 repo 排除）。
	commandObserveHandler := handler.NewCommandObserveHandler(service.NewCommandObserveService(commandRepo))

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

	// P4b 装配点：调度域（FR-146）
	// 调度决策日表落库（见 v2-metrics-health-scheduling.md §3.4/§4.3）：决策行复用同一异步写入
	// 通道（sched_decision 路由独立攒批），trace_id 唯一键幂等去重、跨日按 ts_ms 拆表。
	schedDecisionRepo := repository.NewSchedDecisionV2Repository(db)
	service.RegisterFlusher(asyncDailyWriter, service.RouteKindSchedDecision, schedDecisionRepo.FlushDaily)
	// 决策服务在健康视图内存真源上纯内存决策（§4.6，随机决胜用默认时钟种子源）；
	// 健康视图与健康域计算轮填充的 Store 为同一实例（单一真源，§4.5）。
	schedulingV2Service := service.NewSchedulingV2Service(healthViewStore, nil)
	schedulingV2Service.SetDecisionEnqueuer(service.SchedDecisionEnqueuer{Writer: asyncDailyWriter})
	v2SchedHandler := handler.NewV2SchedHandler(schedulingV2Service)
	// 决策记录管理面查询（§5.2）：跨日并表列表 / 详情 / 概览，只读、查询侧不隐式建日表。
	schedDecisionAdminHandler := handler.NewSchedDecisionAdminHandler(service.NewSchedDecisionQueryService(schedDecisionRepo), settingsService)

	// P5a 装配点：连接明细采集（FR-145，见 v2-connection-message-storage.md §3.2/§4.1）。
	// proxy 上报 open/close 事件 → 接收端只校验 + 更内存名册 + 非阻塞入队 → 后台写入池按 conn_id 内嵌时间
	// 事务批插当日日表；同驱动「玩家 → 所在服」名册（供按玩家寻址消息解析）与孤儿会话对账（proxy 重启补 close）。
	connDetailRepo := repository.NewConnDetailRepository(db)
	service.RegisterFlusher(asyncDailyWriter, service.RouteKindConnEvent, connDetailRepo.FlushDaily)
	playerRoster := roster.NewStore()
	connIngestService := service.NewConnIngestService(service.ConnEventEnqueuer{Writer: asyncDailyWriter}, playerRoster, connDetailRepo)
	v2ConnectionHandler := handler.NewV2ConnectionHandler(connIngestService)

	// P5a 装配点：跨服消息控制面中转（FR-149/150，见 v2-connection-message-storage.md §4.2、ADR-0063）。
	// send 上行 → 内存投递队列（每服有界），poll 长轮询下行，ack 回执；accepted→dispatched→终态状态机
	// 由中转维护、终态一次性经 message_trace 路由把 msg_trace + msg_payload 同事务落库。按玩家寻址查上面的名册，
	// 跨域放行查 v2ControlPlaneHandler 的 namespace_trust(capability=message)。请求 goroutine 全程不碰 DB。
	messageRepo := repository.NewMessageRepository(db)
	service.RegisterFlusher(asyncDailyWriter, service.RouteKindMessageTrace, messageRepo.FlushDaily)
	messageRelay := service.NewMessageRelay(service.MessageRecordEnqueuer{Writer: asyncDailyWriter})
	// 广播寻址（FR-180）复用健康视图内存真源解析当前在线服集合（与调度决策同一份事实，读深拷贝无锁嵌套）。
	messageService := service.NewMessageService(messageRelay, playerRoster, v2ControlPlaneService, healthViewStore)
	v2MessageHandler := handler.NewV2MessageHandler(messageService)

	// 连接明细 / 消息元数据 / payload 查看管理面查询装配（FR-145/149/150，见 §5.2）：复用 P5a 的
	// connDetailRepo / messageRepo / auditRepo；查询侧请求 goroutine 可读 DB（读非采集面），但仍走
	// 游标分页 + 逐表短路防全量扫描；payload 受控查看先写 message.payload.view 审计后返回内容。
	v2ConnectionAdminHandler := handler.NewV2ConnectionAdminHandler(service.NewConnQueryService(connDetailRepo), settingsService)
	v2MessageAdminHandler := handler.NewV2MessageAdminHandler(
		service.NewMessageQueryService(messageRepo),
		service.NewMessagePayloadService(messageRepo, auditRepo),
		settingsService,
	)

	// 冷查询双连接注入（FR-152，见 ADR-0066 决策 5）：把 P6a 的归档连接注入 6 个查询 repo，
	// includeArchived 时对热 / 冷两连接同构查询后应用层归并。archiveDB 可能为 nil（不可达降级）——
	// repo 侧 HasArchive 判空，冷查询前上层返 503，绝不静默只返回热库结果（fail-static 不阻断热查询）。
	auditRepo.SetArchiveDB(archiveDB)
	metricSampleV2Repo.SetArchiveDB(archiveDB)
	healthSnapshotRepo.SetArchiveDB(archiveDB)
	schedDecisionRepo.SetArchiveDB(archiveDB)
	connDetailRepo.SetArchiveDB(archiveDB)
	messageRepo.SetArchiveDB(archiveDB)

	// 配置操作级撤回子系统（FR-116，见 ADR-0051）：可逆账目仓库 + 服务（记账 + 撤回编排 + 幂等 + 过期/被覆盖双闸）+ 处理器。
	// 撤回复用 ConfigService/FileService 的事务内回滚核；记账器注入三类大操作落地处（发布/下发同事务记、ingest 落库后补记）。
	// 提交成功后经 notifier 唤醒受影响长轮询（与正向发布同一唤醒机制）。
	reversibleOpRepo := repository.NewReversibleOperationRepository(db)
	reversibleOpService := service.NewReversibleOperationService(db, reversibleOpRepo, configService, fileService, auditRepo, settingsService)
	reversibleOpService.SetNotifier(notifier)
	configService.SetReversibleRecorder(reversibleOpService)
	fileService.SetReversibleRecorder(reversibleOpService)
	reverseFetchTaskService.SetReversibleRecorder(reversibleOpService)
	reversibleOpHandler := handler.NewReversibleOperationHandler(reversibleOpService)

	// 陈旧命令后台清理（FR-39/FR-46）：周期把创建超期仍未终结的命令标 expired 并清空拓印瞬态明文，避免放弃的 ready 命令明文滞留。
	commandSweeper := service.NewCommandSweeper(commandService)
	// 陈旧受管任务后台清理（FR-58）：周期把创建超期仍未终结的任务标 expired 并清空清单瞬态，避免大树清单 TEXT 长期滞留。
	reverseFetchTaskSweeper := service.NewReverseFetchTaskSweeper(reverseFetchTaskService)
	// 陈旧可逆账目后台清理（FR-116）：周期把创建超可撤回窗口仍 reversible 的账目标 expired 并清空反向快照瞬态。
	reversibleOpSweeper := service.NewReversibleOperationSweeper(reversibleOpService)

	// git 单向导出镜像（FR-47，见 ADR-0030）：发布 / 回滚 / 改派提交后异步 best-effort 把源层导出 commit。
	// 仅 enabled 时装配并接线触发器；git 仓是单向派生镜像、失败仅 WARN 不阻断发布。
	// gitRepo 端口：未启用 NopGitRepo、启用用 go-git 实现的 GoGitRepo（见 newGitRepo / ADR-0030 决策5）。
	exportSourceRepo := repository.NewExportSourceRepository(db)
	gitExportService := service.NewGitExportService(exportSourceRepo, newGitRepo(cfg.GitExport))
	wireGitExport(cfg.GitExport, gitExportService, configService, fileService, zoneService)

	// 控制面在线更新核心（FR-97/FR-119，见 ADR-0044/ADR-0053）：按渠道查 Release → 下载 → SHA256 → 落位 pending → 主进程自替换重启。
	// updateRestartCh 是「更新就绪请求重启」的进程内信号：更新服务落位 pending 成功后关闭它，
	// run 的 select 据此优雅关停后执行自替换（rename 让位 + spawn 新进程），本进程随后退出（exit 0）。
	// 出站经 internal/httpx 工厂（带代理 + 超时，FR-98）；代理地址由触发端点（FR-99）从设置 store 读后传入。
	updateRestartCh := make(chan struct{})
	rollbackCh := make(chan struct{})
	updateService := update.NewService(update.Config{
		CurrentVersion: version.Version,
		PendingPath:    resolvePendingPath(),
		RunPath:        selfPath,
		NewHTTPClient:  httpx.NewClient,
		// 仅关一次：sync.Once 保证多次触发不重复关闭 channel（panic 防护）。
		RequestRestart:  sync.OnceFunc(func() { close(updateRestartCh) }),
		RequestRollback: sync.OnceFunc(func() { close(rollbackCh) }),
		Audit:           auditRepo,
	})
	// HTTP 触发面（FR-99，见 ADR-0044）：把更新核心接到 admin 端点——检查（只读、服务端缓存 + ?force 刷新）/
	// 状态（读内存进度）/ 触发应用（写、readonly 403 + 审计）。渠道 / 代理 / 缓存 TTL 从设置 store 读、热生效（FR-101）。
	updateAPIService := service.NewUpdateService(updateService, settingsService)
	updateHandler := handler.NewUpdateHandler(updateAPIService)
	slog.Info("控制面在线更新核心已就绪",
		"初始阶段", string(updateService.Snapshot().Phase), "pending 路径", resolvePendingPath())

	// 内嵌前端：去掉 apps/web/dist 前缀后交给 SPA 处理器
	dist, err := fs.Sub(beacon.WebDist, "apps/web/dist")
	if err != nil {
		return err
	}
	router := server.NewRouter(server.Handlers{
		Namespace: nsHandler, Env: envHandler, V2: v2ControlPlaneHandler, V2Metrics: v2MetricsHandler, V2Health: v2HealthHandler, V2Sched: v2SchedHandler, V2Connection: v2ConnectionHandler, V2Message: v2MessageHandler, V2ConnectionAdmin: v2ConnectionAdminHandler, V2MessageAdmin: v2MessageAdminHandler, V2Archive: v2ArchiveHandler, V2ConfigCenter: v2ConfigCenterHandler, V2Assets: v2AssetsHandler, Delivery: deliveryHandler, DeliveryStream: deliveryStreamHandler, DeliveryAgent: deliveryAgentHandler, SchedDecision: schedDecisionAdminHandler, Config: configHandler, File: fileHandler, OverrideSet: overrideSetHandler,
		Agent: agentHandler, Stream: streamHandler, Instance: instanceHandler, Topology: topologyHandler, Zone: zoneHandler, Scheduling: schedulingHandler,
		Audit: auditHandler, Alert: alertHandler, AlertEvent: alertEventHandler, Metric: metricHandler, System: systemHandler, Observability: observabilityHandler, CommandObserve: commandObserveHandler, Update: updateHandler, Auth: authHandler, APIKey: apiKeyHandler, Command: commandHandler, Browse: browseHandler, Asset: assetHandler, FileSync: fileSyncHandler, AgentLog: agentLogHandler, ReverseFetchTask: reverseFetchTaskHandler, ReverseFetchRule: reverseFetchIgnoreRuleHandler, Settings: settingsHandler, ReversibleOp: reversibleOpHandler, Metrics: metricsSet.Handler(), Web: embedweb.Handler(dist),
	}, cfg.AgentToken, authn, apiKeyService, auditRepo)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 关停信号 ctx 作所有请求的根 context（fix-b）：Ctrl+C / SIGTERM 时取消所有在途请求——
	// SSE / 长轮询等长连接据 r.Context() 即时退出、连接随之 drain，Shutdown 不必干等到 35s 超时（"关不掉"观感）。
	srv.BaseContext = func(net.Listener) context.Context { return ctx }
	// 异步在线更新下载挂到关停信号 ctx（fix-b）：Ctrl+C / 关停时取消进行中的下载，下载不再脱离进程生命周期。
	updateAPIService.SetBaseContext(ctx)

	// 启动后台健康扫描（随关停信号取消退出）
	go healthScanner.Run(ctx)

	// 启动后台指标采样器（FR-32）：恒常驻，每轮从设置 store 读 metric.enabled 决定本轮是否采样 / 清理（FR-61）。
	// 不再启动期一次性决定起不起——运维改 metric.enabled 即热生效停 / 起采样，免重启。
	go metricSampler.Run(ctx)

	// 启动异步日表写入通道（FR-144）：每路由多 worker 共享该路由有界队列，攒批事务批插当日日表，随关停信号退出。
	asyncDailyWriter.Start(ctx)

	// 启动热冷归档后台工作器（FR-151，见 ADR-0066）：每日 schedule-hour-utc 自动归档 + 手动任务搬运，随关停信号退出。
	// 归档库不可达时 Run 内部直接返回不启动搬运循环（overview 仍标不可用）。
	go archiveService.Run(ctx)

	// P5a：进程启动期从 status=open 连接行重建「玩家 → 所在服」名册（FR-145，spec §4.1），并启动孤儿会话对账 worker
	// （消费 proxy 重启对账请求补 close，DB 写全在后台、请求线程不碰 DB），随关停信号退出。
	connIngestService.RebuildRoster()
	go connIngestService.Run(ctx)

	// P5a：启动消息中转清理轮（FR-149，spec §4.2）：周期推进 TTL 过期 / 重投 / ack 超时，把终态消息经写入通道落库，随关停信号退出。
	go messageRelay.Run(ctx)

	// 启动健康计算轮（FR-147）：每 5s 重算全实例健康视图、每 30s 全量快照经写入通道落库，随关停信号退出。
	go healthComputeService.Run(ctx)

	// 启动 git 导出 worker（FR-47）：单 worker 串行消费导出信号，随关停信号退出；未启用时也起（空转无害、无信号即不动）
	if cfg.GitExport.Enabled {
		go gitExportService.Run(ctx.Done())
	}

	// 启动陈旧命令清理器（FR-39/FR-46）：常驻 hygiene，随关停信号退出
	go commandSweeper.Run(ctx)

	// 启动陈旧受管任务清理器（FR-58）：常驻 hygiene，把超期未终结任务标 expired 并清空清单瞬态，随关停信号退出
	go reverseFetchTaskSweeper.Run(ctx)

	// 启动陈旧可逆账目清理器（FR-116）：常驻 hygiene，把超可撤回窗口仍 reversible 的账目标 expired 并清空反向快照瞬态，随关停信号退出
	go reversibleOpSweeper.Run(ctx)
	// P9 M2：交付中转 blob 清理器（FR-165，spec §4.5.4）：周期清终态超保留期 blob 与上传残留。
	go deliveryBlobCleaner.Run(ctx)
	// P9 M3：交付灰度编排推进器（FR-166，spec §4.1）：启动先按库内状态恢复 rolling / paused 单，再 ticker + 回执唤醒双驱动推进。
	go deliveryOrchestrator.Run(ctx)

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
		// 正常关停（管理员 / docker stop 介入）= 新版已被接受：确认更新成功、清理 sentinel 与 .old（FR-119，见 ADR-0053）。
		if selfErr == nil {
			update.ConfirmUpdateSuccess(selfPath)
		}
		// 给关停一个上限：略大于长轮询上限，到点强制结束
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case <-updateRestartCh:
		// 更新已落位 pending，优雅关停释放端口后自替换二进制并重启（FR-119，见 ADR-0053）：
		// rename 让位三步换二进制 + spawn 新进程，本进程随后退出（exit 0）；换失败就地回退、重启旧版兜底。
		slog.Info("更新已就绪，优雅关停后自替换二进制并重启")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		if selfErr != nil {
			return fmt.Errorf("自替换失败：无法解析自身可执行路径: %w", selfErr)
		}
		return update.SwapAndRespawn(selfPath, resolvePendingPath(), updateService.Snapshot().TargetVersion)
	case <-rollbackCh:
		// 手动回滚已触发（FR-120）：优雅关停释放端口后回退到上一版本（.old → 运行路径）并重启旧版。
		slog.Info("手动回滚已触发，优雅关停后回退到上一版本并重启")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		if selfErr != nil {
			return fmt.Errorf("回滚失败：无法解析自身可执行路径: %w", selfErr)
		}
		return update.RollbackAndRespawn(selfPath)
	case err := <-errCh:
		return err
	}
}

// openArchiveConn 建立归档库连接（FR-151，见 ADR-0066）：不可达时 WARN + 返回 nil 连接，
// 归档能力降级为不可用、绝不阻断控制面启动（fail-static）；ArchiveInfo 恒返回供 overview 展示目标。
func openArchiveConn(mainDB config.DatabaseConfig, arc config.ArchiveConfig) (*gorm.DB, store.ArchiveInfo) {
	archiveDB, info, err := store.OpenArchive(mainDB, arc)
	if err != nil {
		slog.Warn("归档库不可达，归档能力降级不可用（不阻断控制面启动）",
			"目标模式", info.Mode, "库", info.Database, "错误", err)
		return nil, info
	}
	slog.Info("归档库已连接", "目标模式", info.Mode, "库", info.Database)
	return archiveDB, info
}

// resolvePendingPath 推导 pending 新二进制路径（运行二进制同目录 beacon.new[.exe]，FR-119/ADR-0053）。
// 更新服务据此原子落位，自替换时由 update.SwapAndRespawn rename 让位换上。
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

// wireGitExport 按配置接线 git 单向导出触发器（FR-47，见 ADR-0030）：仅 enabled 时把导出器
// 注入发布 / 回滚 / 改派链路（从 run 原样提取，行为不变，控制 run 圈复杂度）。
func wireGitExport(cfg config.GitExportConfig, gitExportService *service.GitExportService,
	configService *service.ConfigService, fileService *service.FileService, zoneService *service.ZoneService) {
	if !cfg.Enabled {
		slog.Info("git 单向导出镜像未启用（git-export.enabled=false）")
		return
	}
	configService.SetGitExporter(gitExportService)
	fileService.SetGitExporter(gitExportService)
	zoneService.SetGitExporter(gitExportService)
	slog.Info("git 单向导出镜像已启用", "仓路径", cfg.RepoPath, "远程", gitRemoteForLog(cfg.RemoteURL))
}
