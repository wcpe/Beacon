package runtime

import (
	"context"
	"log/slog"
	"time"
)

// HealthScanner 是单个后台 goroutine，定期按 TTL 推进实例健康状态机。
type HealthScanner struct {
	registry     *Registry
	ttl          time.Duration
	offlineGrace time.Duration
	scanInterval time.Duration
}

// NewHealthScanner 构造健康扫描器。
func NewHealthScanner(registry *Registry, ttl, offlineGrace, scanInterval time.Duration) *HealthScanner {
	return &HealthScanner{
		registry:     registry,
		ttl:          ttl,
		offlineGrace: offlineGrace,
		scanInterval: scanInterval,
	}
}

// Run 启动扫描循环，直到 ctx 取消。
func (s *HealthScanner) Run(ctx context.Context) {
	ticker := time.NewTicker(s.scanInterval)
	defer ticker.Stop()
	slog.Info("健康扫描已启动", "扫描周期", s.scanInterval.String(), "ttl", s.ttl.String())
	for {
		select {
		case <-ctx.Done():
			slog.Info("健康扫描已停止")
			return
		case now := <-ticker.C:
			for _, inst := range s.registry.SweepExpired(now.UTC(), s.ttl, s.offlineGrace) {
				slog.Warn("实例健康状态变更",
					"namespace", inst.Namespace, "serverId", inst.ServerID, "状态", inst.Status)
			}
		}
	}
}
