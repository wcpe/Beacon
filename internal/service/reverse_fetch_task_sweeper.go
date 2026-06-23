package service

import (
	"context"
	"log/slog"
	"time"
)

// 受管任务过期清理的固定节律（走常量、不散落硬编码；属控制面内部 hygiene，非运维调优旋钮，
// 与命令清理器同源理由不引入新 env / yaml 配置项以免与受保护的 .env.example 漂移）。
const (
	// 扫描间隔：每隔多久清一次陈旧任务。
	reverseFetchTaskSweepInterval = 5 * time.Minute
	// 陈旧阈值：创建超过此时长仍处非终态的任务判为陈旧并过期。
	// 须够宽以覆盖整条受管任务生命周期（触发扫描 → agent 回清单 → 人工审核选定 → agent 回选定内容 → 落库）。
	reverseFetchTaskStaleAfter = 1 * time.Hour
)

// reverseFetchTaskExpirer 是清理器对任务服务的窄依赖（由 ReverseFetchTaskService 实现），便于以测试替身验证调用。
type reverseFetchTaskExpirer interface {
	ExpireStale(before time.Time) (int64, error)
}

// ReverseFetchTaskSweeper 是陈旧受管任务清理器（FR-58，单后台 goroutine）：周期把创建超期仍处非终态的任务标
// expired 并清空清单瞬态（manifest TEXT），避免被放弃的任务把大树清单长期滞留在库、也解除其互斥占位。
// 结构参照 CommandSweeper / FR-32 指标采样器。
type ReverseFetchTaskSweeper struct {
	svc        reverseFetchTaskExpirer
	interval   time.Duration
	staleAfter time.Duration
}

// NewReverseFetchTaskSweeper 构造清理器（用内置固定节律）。
func NewReverseFetchTaskSweeper(svc reverseFetchTaskExpirer) *ReverseFetchTaskSweeper {
	return &ReverseFetchTaskSweeper{svc: svc, interval: reverseFetchTaskSweepInterval, staleAfter: reverseFetchTaskStaleAfter}
}

// Run 启动清理循环，直到 ctx 取消（随进程关停退出）。
func (s *ReverseFetchTaskSweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	slog.Info("反向抓取任务过期清理已启动", "扫描间隔", s.interval.String(), "陈旧阈值", s.staleAfter.String())
	for {
		select {
		case <-ctx.Done():
			slog.Info("反向抓取任务过期清理已停止")
			return
		case now := <-ticker.C:
			s.sweepOnce(now.UTC())
		}
	}
}

// sweepOnce 执行一轮清理：把创建早于 now-staleAfter 仍处非终态的任务标 expired 并清空清单瞬态。
func (s *ReverseFetchTaskSweeper) sweepOnce(now time.Time) {
	n, err := s.svc.ExpireStale(now.Add(-s.staleAfter))
	if err != nil {
		slog.Error("反向抓取任务过期清理失败", "错误", err)
		return
	}
	if n > 0 {
		slog.Info("反向抓取任务过期清理完成", "过期条数", n)
	}
}
