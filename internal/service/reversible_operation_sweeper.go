package service

import (
	"context"
	"log/slog"
	"time"
)

// 可逆操作过期清理的固定扫描节律（陈旧阈值不固定——按设置 store 的 undo.window-hours 热改，每轮读，FR-116）。
// 扫描间隔属控制面内部 hygiene、非运维调优旋钮，走常量不引新配置项（与命令 / 受管任务清理器同源理由）。
const reversibleOpSweepInterval = 5 * time.Minute

// reversibleOpExpirer 是清理器对撤回服务的窄依赖（由 ReversibleOperationService 实现），便于以测试替身验证调用。
type reversibleOpExpirer interface {
	ExpireStale(before time.Time) (int64, error)
	WindowHours() int
}

// ReversibleOperationSweeper 是陈旧可逆账目清理器（FR-116，单后台 goroutine）：周期把创建超可撤回窗口仍
// reversible 的账目标 expired 并清空反向快照瞬态（inverse_payload TEXT），避免放弃的账目把快照长期滞留在库。
// 可撤回窗口（小时）每轮从设置 store 读、热生效；结构参照 ReverseFetchTaskSweeper / CommandSweeper。
type ReversibleOperationSweeper struct {
	svc      reversibleOpExpirer
	interval time.Duration
}

// NewReversibleOperationSweeper 构造清理器（用内置固定扫描间隔；陈旧阈值按窗口热改）。
func NewReversibleOperationSweeper(svc reversibleOpExpirer) *ReversibleOperationSweeper {
	return &ReversibleOperationSweeper{svc: svc, interval: reversibleOpSweepInterval}
}

// Run 启动清理循环，直到 ctx 取消（随进程关停退出）。
func (s *ReversibleOperationSweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	slog.Info("配置撤回账目过期清理已启动", "扫描间隔", s.interval.String())
	for {
		select {
		case <-ctx.Done():
			slog.Info("配置撤回账目过期清理已停止")
			return
		case now := <-ticker.C:
			s.sweepOnce(now.UTC())
		}
	}
}

// sweepOnce 执行一轮清理：把创建早于 now-窗口、仍 reversible 的账目标 expired 并清空反向快照瞬态。
func (s *ReversibleOperationSweeper) sweepOnce(now time.Time) {
	window := time.Duration(s.svc.WindowHours()) * time.Hour
	n, err := s.svc.ExpireStale(now.Add(-window))
	if err != nil {
		slog.Error("配置撤回账目过期清理失败", "错误", err)
		return
	}
	if n > 0 {
		slog.Info("配置撤回账目过期清理完成", "过期条数", n)
	}
}
