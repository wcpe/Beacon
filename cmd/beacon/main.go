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
	"beacon/internal/config"
	"beacon/internal/embedweb"
	"beacon/internal/handler"
	"beacon/internal/pkg/log"
	"beacon/internal/repository"
	"beacon/internal/runtime"
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
	flag.StringVar(&cfgPath, "config", "config.yaml", "配置文件路径")
	flag.Parse()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	log.Setup(cfg.Log.Level)
	slog.Info("Beacon 控制面启动中", "监听地址", cfg.HTTPAddr)

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
	configHandler := handler.NewConfigHandler(configService)

	// 注册/健康运行态：内存注册表 + 健康扫描（注册/健康的内存真源）
	registry := runtime.NewRegistry()
	heartbeatInterval := time.Duration(cfg.Health.HeartbeatIntervalSec) * time.Second
	ttl := time.Duration(cfg.Health.TTLSec) * time.Second
	offlineGrace := time.Duration(cfg.Health.OfflineGraceSec) * time.Second
	scanInterval := time.Duration(cfg.Health.ScanIntervalSec) * time.Second
	healthScanner := runtime.NewHealthScanner(registry, ttl, offlineGrace, scanInterval)

	instanceService := service.NewInstanceService(registry, assignRepo, auditRepo, heartbeatInterval, ttl)
	zoneService := service.NewZoneService(db, assignRepo, auditRepo, registry)
	agentHandler := handler.NewAgentHandler(instanceService)
	instanceHandler := handler.NewInstanceHandler(instanceService)
	zoneHandler := handler.NewZoneHandler(zoneService)

	// 内嵌前端：去掉 web/dist 前缀后交给 SPA 处理器
	dist, err := fs.Sub(beacon.WebDist, "web/dist")
	if err != nil {
		return err
	}
	router := server.NewRouter(server.Handlers{
		Namespace: nsHandler, Config: configHandler, Agent: agentHandler,
		Instance: instanceHandler, Zone: zoneHandler, Web: embedweb.Handler(dist),
	}, cfg.AgentToken)

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
