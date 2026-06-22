package service

import (
	"context"
	"log/slog"
	"time"
)

// 命令过期清理的固定节律（走常量、不散落硬编码；属控制面内部 hygiene，非运维调优旋钮，
// 故不引入新的 env / yaml 配置项以免与受保护的 .env.example 漂移）。
const (
	// 扫描间隔：每隔多久清一次陈旧命令。
	commandSweepInterval = 5 * time.Minute
	// 陈旧阈值：创建超过此时长仍未终结（pending/fetched/ready）的命令判为陈旧并过期。
	// 须够宽以覆盖整条拓印生命周期（触发 → agent 回传 → 人工审 diff → 确认）。
	commandStaleAfter = 1 * time.Hour
)

// commandExpirer 是清理器对命令服务的窄依赖（由 AgentCommandService 实现），便于以测试替身验证调用。
type commandExpirer interface {
	ExpireStale(before time.Time) (int64, error)
}

// CommandSweeper 是陈旧命令清理器（FR-39 / FR-46，单后台 goroutine）：周期把创建超期仍未终结的命令标 expired
// 并清空拓印瞬态明文 imprint_content，避免放弃的 ready 命令把磁盘原文长期滞留在库。结构参照 FR-32 指标采样器。
type CommandSweeper struct {
	svc        commandExpirer
	interval   time.Duration
	staleAfter time.Duration
}

// NewCommandSweeper 构造清理器（用内置固定节律）。
func NewCommandSweeper(svc commandExpirer) *CommandSweeper {
	return &CommandSweeper{svc: svc, interval: commandSweepInterval, staleAfter: commandStaleAfter}
}

// Run 启动清理循环，直到 ctx 取消（随进程关停退出）。
func (s *CommandSweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	slog.Info("命令过期清理已启动", "扫描间隔", s.interval.String(), "陈旧阈值", s.staleAfter.String())
	for {
		select {
		case <-ctx.Done():
			slog.Info("命令过期清理已停止")
			return
		case now := <-ticker.C:
			s.sweepOnce(now.UTC())
		}
	}
}

// sweepOnce 执行一轮清理：把创建早于 now-staleAfter 仍未终结的命令标 expired 并清空瞬态。
func (s *CommandSweeper) sweepOnce(now time.Time) {
	n, err := s.svc.ExpireStale(now.Add(-s.staleAfter))
	if err != nil {
		slog.Error("命令过期清理失败", "错误", err)
		return
	}
	if n > 0 {
		slog.Info("命令过期清理完成", "过期条数", n)
	}
}
