package runtime

import (
	"context"
	"log/slog"
	"time"

	"beacon/internal/runtime/alert"
)

// HealthScanner 是单个后台 goroutine，定期按陈旧度推进实例健康状态机并对异常转移主动告警（FR-28）。
type HealthScanner struct {
	registry      *Registry
	degradedAfter time.Duration
	ttl           time.Duration
	offlineGrace  time.Duration
	scanInterval  time.Duration
	dispatcher    *alert.Dispatcher
}

// NewHealthScanner 构造健康扫描器（dispatcher 收口告警通道扇出）。
func NewHealthScanner(registry *Registry, degradedAfter, ttl, offlineGrace, scanInterval time.Duration, dispatcher *alert.Dispatcher) *HealthScanner {
	return &HealthScanner{
		registry:      registry,
		degradedAfter: degradedAfter,
		ttl:           ttl,
		offlineGrace:  offlineGrace,
		scanInterval:  scanInterval,
		dispatcher:    dispatcher,
	}
}

// Run 启动扫描循环，直到 ctx 取消。
func (s *HealthScanner) Run(ctx context.Context) {
	ticker := time.NewTicker(s.scanInterval)
	defer ticker.Stop()
	slog.Info("健康扫描已启动",
		"扫描周期", s.scanInterval.String(), "亚健康阈值", s.degradedAfter.String(), "ttl", s.ttl.String())
	for {
		select {
		case <-ctx.Done():
			slog.Info("健康扫描已停止")
			return
		case now := <-ticker.C:
			// 锁内仅推进状态、返回变更快照；告警分发在锁外（避免 HTTP IO 持锁，守三锁不嵌套）
			changed := s.registry.SweepExpired(now.UTC(), s.degradedAfter, s.ttl, s.offlineGrace)
			for _, inst := range changed {
				slog.Warn("实例健康状态变更",
					"namespace", inst.Namespace, "serverId", inst.ServerID, "旧状态", inst.PrevStatus, "状态", inst.Status)
			}
			s.dispatchAlerts(ctx, changed)
		}
	}
}

// dispatchAlerts 对进入异常态（degraded/lost/offline）的变更触发告警；恢复 online 不告警（避免噪音，FR-28）。
func (s *HealthScanner) dispatchAlerts(ctx context.Context, changed []*Instance) {
	for _, inst := range changed {
		if !isAbnormal(inst.Status) {
			continue
		}
		s.dispatcher.Dispatch(ctx, alert.Alert{
			Namespace:  inst.Namespace,
			ServerID:   inst.ServerID,
			Address:    inst.Address,
			PrevStatus: inst.PrevStatus,
			Status:     inst.Status,
			At:         time.Now().UTC(),
		})
	}
}

// isAbnormal 判断状态是否属需告警的异常集合。
func isAbnormal(status string) bool {
	switch status {
	case StatusDegraded, StatusLost, StatusOffline:
		return true
	default:
		return false
	}
}
