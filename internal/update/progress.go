package update

import "sync"

// Phase 是更新进度阶段（进程内瞬态、不落库，FR-97 见 ADR-0044 决策8）。
type Phase string

const (
	PhaseIdle         Phase = "idle"          // 空闲：未在更新
	PhaseChecking     Phase = "checking"      // 查 Release 比对版本中
	PhaseDownloading  Phase = "downloading"   // 下载资产中
	PhaseVerifying    Phase = "verifying"     // SHA256 校验中
	PhaseStaging      Phase = "staging"       // 原子落位 pending 中
	PhaseReadyRestart Phase = "ready-restart" // 已落位、已请求重启交还 launcher
	PhaseFailed       Phase = "failed"        // 任一阶段失败（保留旧二进制、进程不退）
)

// Progress 是更新进度快照（对外只读复制，不暴露内部锁）。
type Progress struct {
	Phase         Phase  // 当前阶段
	Percent       int    // 下载百分比 0-100（仅下载阶段有意义，其余为阶段性快照值）
	TargetVersion string // 目标版本（如 v1.2.0）；空表示尚未确定
	Error         string // 失败原因（仅 PhaseFailed 非空）
}

// progressTracker 是线程安全的更新进度态。进程内瞬态、随进程消失，不建数据库表。
type progressTracker struct {
	mu   sync.RWMutex
	snap Progress
}

// newProgressTracker 构造进度态，初始为 idle。
func newProgressTracker() *progressTracker {
	return &progressTracker{snap: Progress{Phase: PhaseIdle}}
}

// Snapshot 返回当前进度的只读副本。
func (p *progressTracker) Snapshot() Progress {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.snap
}

// setPhase 切换阶段并清空错误（进入新阶段意味着上一步已通过）。
// 不动 Percent：保留最后下载进度以便观测，新一轮 apply 由 reset 归零。
func (p *progressTracker) setPhase(phase Phase, targetVersion string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snap.Phase = phase
	if targetVersion != "" {
		p.snap.TargetVersion = targetVersion
	}
	p.snap.Error = ""
}

// fail 标记失败并记录原因（保留 targetVersion 便于观测「哪个版本更新失败」）。
func (p *progressTracker) fail(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snap.Phase = PhaseFailed
	p.snap.Error = reason
}

// reset 把进度归零到 checking 起点（开始新一轮 apply 时调用）。
func (p *progressTracker) reset(targetVersion string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snap = Progress{Phase: PhaseChecking, TargetVersion: targetVersion}
}
