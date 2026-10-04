package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// 失联孤儿告警清理的固定扫描节律（属控制面内部 hygiene、非运维调优旋钮，故走常量；
// 真正可调的是「多久算超时」，走设置 store 的 alert.orphan-timeout-hours 热改）。
const alertOrphanSweepInterval = 10 * time.Minute

// alertOrphanResolveNote 是系统自动消解写入 handle_note 的固定文案（配 handled_by=system，
// 让 UI 能区分「系统自动消解」与「人工已处理」，与 FR-232 其余自动消解触发点同口径）。
const alertOrphanResolveNote = "实例长期失联且不在受管目录，自动消解"

// alertOrphanIndex 是清理器对「孤儿候选 + 在册目录」的窄读依赖（由 repository.AlertOrphanRepository 满足）。
// 两项都是集合 / 聚合查询，禁逐实例循环查库（N+1）。
type alertOrphanIndex interface {
	// ListUnresolvedByServer 列出所有存在未处理告警的 (namespace, serverId) 及其最近触发时间（一条聚合查询）。
	ListUnresolvedByServer() ([]repository.AlertOrphanCandidate, error)
	// ActiveServerKeys 返回 server 表中仍处 active 的在册实例键集合（安全红线数据源）。
	ActiveServerKeys() (map[repository.AlertServerKey]struct{}, error)
}

// alertOrphanResolver 是清理器的关闭动作窄依赖（由 repository.AlertEventRepository.AutoResolveByServer 满足）：
// 一条 UPDATE 把某实例未 resolved 的告警置为 resolved（天然幂等），与人机一致的另一处自动消解复用同一写点。
type alertOrphanResolver interface {
	AutoResolveByServer(namespace, serverID string, now time.Time, note string) (int64, error)
}

// alertOrphanPresence 是清理器对运行时注册表的窄读依赖（由 *runtime.Registry 满足）。
// 语义是「该实例当前是否仍有在线 / 失联登记」——有登记说明它还活着（或刚断），不属外部消失。
type alertOrphanPresence interface {
	Get(namespace, serverID string) *runtime.Instance
}

// alertOrphanSettings 是清理器对运维设置的窄读依赖（由 SettingsService 满足）：超时阈值每轮读、热生效。
type alertOrphanSettings interface {
	GetInt(key string) int
}

// AlertOrphanSweeper 是「失联且不在受管目录」实例的未处理告警清理器（单后台 goroutine，结构参照 CommandSweeper）。
// 背景：实例被外部删除（压测实例用完即删 / 实例被直接销毁）时控制面收不到任何删除信号，其告警会永远停在 open
// （prod 实测压测结束滞留 30+ 条）。本清理器按四项判据**同时成立**才自动关闭，且关闭动作复用人机一致的
// 自动消解写点（status=resolved + handled_by=system + 固定 note）。
//
// 四项判据（缺一不可）：
//  1. 该 (namespace, serverId) 存在未处理（status <> resolved）告警；
//  2. 该实例当前不在运行时注册表（无任何在线 / 失联登记）——控制面自身也不知道它还在；
//  3. 该实例不在 server 表的活动目录中（不存在 lifecycle = active 的行）。**安全红线**：在册的真实实例
//     即使离线很久也绝不能自动关闭告警，运维必须看到；已归档 / 墓碑行不算在册，其告警由既有生命周期自动消解覆盖；
//  4. 该实例告警的最近触发时间（last_at，空则 created_at）距现在超过超时阈值（默认 24 小时，可配）。
type AlertOrphanSweeper struct {
	index    alertOrphanIndex
	resolver alertOrphanResolver
	presence alertOrphanPresence
	settings alertOrphanSettings
	interval time.Duration
}

// NewAlertOrphanSweeper 构造清理器（扫描间隔用内置常量；超时阈值每轮从设置 store 读）。
func NewAlertOrphanSweeper(index alertOrphanIndex, resolver alertOrphanResolver, presence alertOrphanPresence, settings alertOrphanSettings) *AlertOrphanSweeper {
	return &AlertOrphanSweeper{index: index, resolver: resolver, presence: presence, settings: settings, interval: alertOrphanSweepInterval}
}

// Run 启动清理循环，直到 ctx 取消（随进程关停退出）。
func (s *AlertOrphanSweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	slog.Info("失联孤儿告警清理已启动", "扫描间隔", s.interval.String())
	for {
		select {
		case <-ctx.Done():
			slog.Info("失联孤儿告警清理已停止")
			return
		case now := <-ticker.C:
			s.sweepOnce(now.UTC())
		}
	}
}

// orphanTimeoutHours 取当前超时阈值（小时，每轮读设置 store 即热生效）。
// 非正值回退默认：store 值经白名单校验（下界 1），此处仅防首启种子写入异常值把判据 4 变成恒真。
func (s *AlertOrphanSweeper) orphanTimeoutHours() int {
	hours := s.settings.GetInt(SettingAlertOrphanTimeoutHours)
	if hours <= 0 {
		return alertOrphanTimeoutDefaultHours
	}
	return hours
}

// sweepOnce 执行一轮清理：逐候选取四项判据全真者自动消解其未处理告警，返回本轮消解条数。
// 任一前置查询失败即本轮不关闭任何告警（fail-static：宁可留噪音，不可误关）。
func (s *AlertOrphanSweeper) sweepOnce(now time.Time) int64 {
	candidates, err := s.index.ListUnresolvedByServer()
	if err != nil {
		slog.Error("失联孤儿告警扫描失败（本轮不关闭任何告警）", "错误", err)
		return 0
	}
	if len(candidates) == 0 {
		return 0
	}
	active, err := s.index.ActiveServerKeys()
	if err != nil {
		slog.Error("在册实例目录读取失败（本轮不关闭任何告警）", "错误", err)
		return 0
	}

	cutoff := now.Add(-time.Duration(s.orphanTimeoutHours()) * time.Hour)
	var total int64
	for _, c := range candidates {
		// 判据 2：仍在运行时注册表（在线 / 失联登记都算）→ 不是外部消失，保持原状。
		if s.presence.Get(c.Namespace, c.ServerID) != nil {
			continue
		}
		// 判据 3（安全红线）：仍在 server 表活动目录 → 是在册的真实实例，即使离线很久也绝不自动关闭，运维必须看到。
		if _, ok := active[repository.AlertServerKey{Namespace: c.Namespace, ServerID: c.ServerID}]; ok {
			continue
		}
		// 判据 4：最近触发时间未超阈值 → 先不动，留出「外部删除后又被重新纳管」的观察窗。
		if !c.LastAt.Before(cutoff) {
			continue
		}
		n, err := s.resolver.AutoResolveByServer(c.Namespace, c.ServerID, now, alertOrphanResolveNote)
		if err != nil {
			// 单条失败不中断整轮：其余候选照常处理，本轮结束后下轮仍会重扫到它（幂等）。
			slog.Error("失联孤儿告警自动消解失败", "namespace", c.Namespace, "serverId", c.ServerID, "错误", err)
			continue
		}
		if n > 0 {
			total += n
			slog.Info("失联孤儿告警已自动消解",
				"namespace", c.Namespace, "serverId", c.ServerID, "消解条数", n, "最后触发", c.LastAt)
		}
	}
	if total > 0 {
		slog.Info("失联孤儿告警清理完成", "消解条数", total)
	}
	return total
}
