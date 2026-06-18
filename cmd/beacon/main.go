// 命令 beacon 是控制面入口：装配依赖并启动 HTTP 服务。
package main

import (
	"context"
	"errors"
	"flag"
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
	"beacon/internal/pkg/log"
	"beacon/internal/repository"
	"beacon/internal/runtime"
	"beacon/internal/runtime/longpoll"
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

	configRepo := repository.NewConfigItemRepository(db)
	revRepo := repository.NewConfigRevisionRepository(db)
	auditRepo := repository.NewAuditLogRepository(db)
	assignRepo := repository.NewZoneAssignmentRepository(db)
	configService := service.NewConfigService(db, configRepo, revRepo, auditRepo)

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
	ttl := time.Duration(cfg.Health.TTLSec) * time.Second
	offlineGrace := time.Duration(cfg.Health.OfflineGraceSec) * time.Second
	scanInterval := time.Duration(cfg.Health.ScanIntervalSec) * time.Second
	healthScanner := runtime.NewHealthScanner(registry, ttl, offlineGrace, scanInterval)

	instanceService := service.NewInstanceService(registry, assignRepo, auditRepo, heartbeatInterval, ttl)
	zoneService := service.NewZoneService(db, assignRepo, auditRepo, registry)

	// 长轮询：配置与文件各持独立 Hub（唤醒集合分开，互不触发无谓重算）+ 有效解析 + 事务后唤醒
	hub := longpoll.NewHub()
	fileHub := longpoll.NewHub()
	effectiveService := service.NewEffectiveService(configRepo, assignRepo, hub)
	// 配置 admin 处理器持有 effectiveService 以支持有效配置只读预览（FR-22）
	configHandler := handler.NewConfigHandler(configService, effectiveService)
	fileEffectiveService := service.NewFileEffectiveService(fileRepo, assignRepo, fileHub)
	// 三方覆盖集投递（FR-15）：复用 fileHub 唤醒集合（同属通道B），解析适用覆盖集 + 成员内容
	overrideEffectiveService := service.NewOverrideEffectiveService(overrideSetRepo, fileRepo, assignRepo, fileHub)
	notifier := service.NewChangeNotifier(hub, fileHub, registry, assignRepo)
	configService.SetNotifier(notifier)
	fileService.SetNotifier(notifier)
	overrideSetService.SetNotifier(notifier)
	zoneService.SetNotifier(notifier)
	maxHold := time.Duration(cfg.Longpoll.MaxHoldMs) * time.Millisecond

	// 单条 SSE 推送流（FR-24）：合并配置/文件树/覆盖集三条长轮询，复用同源唤醒集合 + 连接即对账。
	// 保活间隔取长轮询挂起上限（无变更时按此节奏发注释行心跳，穿透反代空闲超时）。
	streamService := service.NewStreamService(effectiveService, fileEffectiveService, overrideEffectiveService, hub, fileHub, maxHold)

	agentHandler := handler.NewAgentHandler(instanceService, effectiveService, maxHold)
	streamHandler := handler.NewStreamHandler(instanceService, streamService)
	fileHandler := handler.NewFileHandler(fileService, fileEffectiveService, overrideEffectiveService, instanceService, maxHold)
	instanceHandler := handler.NewInstanceHandler(instanceService)
	zoneHandler := handler.NewZoneHandler(zoneService)
	auditHandler := handler.NewAuditHandler(service.NewAuditService(auditRepo))
	authHandler := handler.NewAuthHandler(authn)

	// 内嵌前端：去掉 web/dist 前缀后交给 SPA 处理器
	dist, err := fs.Sub(beacon.WebDist, "web/dist")
	if err != nil {
		return err
	}
	router := server.NewRouter(server.Handlers{
		Namespace: nsHandler, Config: configHandler, File: fileHandler, OverrideSet: overrideSetHandler,
		Agent: agentHandler, Stream: streamHandler, Instance: instanceHandler, Zone: zoneHandler, Audit: auditHandler,
		Auth: authHandler, Web: embedweb.Handler(dist),
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
