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
	"syscall"
	"time"

	"beacon"
	"beacon/internal/auth"
	"beacon/internal/config"
	"beacon/internal/embedweb"
	"beacon/internal/handler"
	"beacon/internal/metrics"
	"beacon/internal/pkg/log"
	"beacon/internal/repository"
	"beacon/internal/runtime"
	"beacon/internal/runtime/alert"
	"beacon/internal/runtime/longpoll"
	"beacon/internal/secret"
	"beacon/internal/server"
	"beacon/internal/service"
	"beacon/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("Beacon 启动失败", "错误", err)
		os.Exit(1)
	}
}

// run 完成配置加载、依赖装配与服务启动，返回首个致命错误。
func run() error {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "config.yml", "配置文件路径")
	flag.Parse()

	// 首启脚手架：把配置模板释放到当前目录（已存在则跳过，绝不覆盖用户文件，FR-25）
	if released, err := config.EnsureFile(cfgPath, beacon.ConfigExampleYAML); err != nil {
		return err
	} else if released {
		slog.Info("首次启动：已释放配置模板", "文件", cfgPath)
	}

	// 首启生成可直接运行的 .env（随机强鉴权凭据），开箱即跑、不再 fail-fast（FR-25）
	if generated, err := config.EnsureBootstrapEnv(".env"); err != nil {
		return err
	} else if generated {
		slog.Warn("首次启动：已生成 .env（含随机管理员口令与密钥，sqlite 可直接运行），请打开 .env 查看 BEACON_ADMIN_PASSWORD 后登录管理台", "文件", ".env")
	}

	// 从当前目录 .env 加载环境变量（仅填补未设置项，真实环境变量优先，FR-25）
	if err := config.LoadDotEnv(".env"); err != nil {
		return err
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	log.Setup(cfg.Log.Level)
	slog.Info("Beacon 控制面启动中", "监听地址", cfg.HTTPAddr)

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
	nsRepo := repository.NewNamespaceRepository(db)
	nsService := service.NewNamespaceService(nsRepo)
	if err := nsService.SeedDefaults(); err != nil {
		return err
	}
	nsHandler := handler.NewNamespaceHandler(nsService)

	// 配置加密 cipher（FR-20）：密钥仅从 env 读，绝不入库 / 不入仓 / 不打日志。
	// 空密钥得到"未启用"cipher；后续若库中已有敏感项则 fail-fast。
	configCipher, err := secret.NewCipher(os.Getenv("BEACON_CONFIG_ENCRYPTION_KEY"))
	if err != nil {
		return err
	}

	configRepo := repository.NewConfigItemRepository(db, configCipher)
	revRepo := repository.NewConfigRevisionRepository(db, configCipher)
	grayRepo := repository.NewConfigGrayRepository(db, configCipher)
	auditRepo := repository.NewAuditLogRepository(db)
	assignRepo := repository.NewZoneAssignmentRepository(db)
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
	heartbeatInterval := time.Duration(cfg.Health.HeartbeatIntervalSec) * time.Second
	degradedAfter := time.Duration(cfg.Health.DegradedAfterSec) * time.Second
	ttl := time.Duration(cfg.Health.TTLSec) * time.Second
	offlineGrace := time.Duration(cfg.Health.OfflineGraceSec) * time.Second
	scanInterval := time.Duration(cfg.Health.ScanIntervalSec) * time.Second

	// 健康告警通道（FR-28，ADR-0019）：站内信常驻；webhook 仅在配置 url 非空时挂载
	inbox := alert.NewInboxAlerter(cfg.Alert.InboxCapacity)
	alertChannels := []alert.Alerter{inbox}
	if cfg.Alert.Webhook.URL != "" {
		alertChannels = append(alertChannels,
			alert.NewWebhookAlerter(cfg.Alert.Webhook.URL, time.Duration(cfg.Alert.Webhook.TimeoutMs)*time.Millisecond))
		slog.Info("健康告警 webhook 通道已启用", "url", cfg.Alert.Webhook.URL)
	}
	healthScanner := runtime.NewHealthScanner(
		registry, degradedAfter, ttl, offlineGrace, scanInterval, alert.NewDispatcher(alertChannels...))

	// 可观测性指标（注册/健康 gauge 抓取时读内存注册表；发布/推送 counter 由事件处自增，见 ADR-0020）
	metricsSet := metrics.New(registry)

	instanceService := service.NewInstanceService(registry, assignRepo, auditRepo, heartbeatInterval, ttl)
	zoneService := service.NewZoneService(db, assignRepo, auditRepo, registry)

	// 流量调度（FR-10）：drain 标记落 DB + 落位建议（query-only），控制面只给决策不执行玩家连接（ADR-0017）
	drainRepo := repository.NewServerDrainRepository(db)
	schedulingService := service.NewSchedulingService(db, drainRepo, auditRepo, registry)

	// 长轮询：配置与文件各持独立 Hub（唤醒集合分开，互不触发无谓重算）+ 有效解析 + 事务后唤醒
	hub := longpoll.NewHub()
	fileHub := longpoll.NewHub()
	// 拓扑 watch（FR-29）：namespace 级唤醒 Hub，与配置/文件独立；实例上线/下线/改派时唤醒订阅方
	topologyHub := longpoll.NewHub()
	effectiveService := service.NewEffectiveService(configRepo, assignRepo, grayRepo, hub)
	// 配置 admin 处理器持有 effectiveService 以支持有效配置只读预览（FR-22）+ 灰度 svc（FR-9）
	configHandler := handler.NewConfigHandler(configService, effectiveService, configGrayService)
	fileEffectiveService := service.NewFileEffectiveService(fileRepo, assignRepo, fileHub)
	// 三方覆盖集投递（FR-15）：复用 fileHub 唤醒集合（同属通道B），解析适用覆盖集 + 成员内容
	overrideEffectiveService := service.NewOverrideEffectiveService(overrideSetRepo, fileRepo, assignRepo, fileHub)
	notifier := service.NewChangeNotifier(hub, fileHub, topologyHub, registry, assignRepo)
	notifier.SetMetrics(metricsSet)
	configService.SetNotifier(notifier)
	configService.SetMetrics(metricsSet)
	// 灰度发布 / promote / abort 提交后按受影响 serverId 唤醒（复用配置通道 Hub，FR-9）
	configGrayService.SetNotifier(notifier)
	fileService.SetNotifier(notifier)
	overrideSetService.SetNotifier(notifier)
	zoneService.SetNotifier(notifier)
	// 实例注册/下线唤醒拓扑 watch；健康扫描转 lost/offline 也唤醒（FR-29）
	instanceService.SetNotifier(notifier)
	healthScanner.SetTopologyNotifier(notifier)
	maxHold := time.Duration(cfg.Longpoll.MaxHoldMs) * time.Millisecond

	// 单条 SSE 推送流（FR-24）：合并配置/文件树/覆盖集三条长轮询 + 拓扑 watch（FR-29），复用同源唤醒集合 + 连接即对账。
	// 保活间隔取长轮询挂起上限（无变更时按此节奏发注释行心跳，穿透反代空闲超时）。
	streamService := service.NewStreamService(effectiveService, fileEffectiveService, overrideEffectiveService, registry, hub, fileHub, topologyHub, maxHold)

	agentHandler := handler.NewAgentHandler(instanceService, effectiveService, maxHold)
	streamHandler := handler.NewStreamHandler(instanceService, streamService)
	fileHandler := handler.NewFileHandler(fileService, fileEffectiveService, overrideEffectiveService, instanceService, maxHold)
	instanceHandler := handler.NewInstanceHandler(instanceService)
	zoneHandler := handler.NewZoneHandler(zoneService)
	schedulingHandler := handler.NewSchedulingHandler(schedulingService)
	auditHandler := handler.NewAuditHandler(service.NewAuditService(auditRepo))
	alertHandler := handler.NewAlertHandler(inbox)
	authHandler := handler.NewAuthHandler(authn)

	// 内嵌前端：去掉 web/dist 前缀后交给 SPA 处理器
	dist, err := fs.Sub(beacon.WebDist, "web/dist")
	if err != nil {
		return err
	}
	router := server.NewRouter(server.Handlers{
		Namespace: nsHandler, Config: configHandler, File: fileHandler, OverrideSet: overrideSetHandler,
		Agent: agentHandler, Stream: streamHandler, Instance: instanceHandler, Zone: zoneHandler, Scheduling: schedulingHandler,
		Audit: auditHandler, Alert: alertHandler, Auth: authHandler, Metrics: metricsSet.Handler(), Web: embedweb.Handler(dist),
	}, cfg.AgentToken, authn)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 启动后台健康扫描（随关停信号取消退出）
	go healthScanner.Run(ctx)

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
	case err := <-errCh:
		return err
	}
}
