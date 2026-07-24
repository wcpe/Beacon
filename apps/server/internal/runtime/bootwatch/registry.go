// Package bootwatch 在进程内内存维护每个 identityId 的 bootId 活跃轨迹，
// 用于识别「复制目录导致的并发双实例」（同 identityId 交替 bootId 往复活跃，spec §4.4 Q4 / §4.5）
// 并与「单向切换」（故障换机 / 正常重启，spec §4.6）区分——单向切换绝不误判为冲突。
//
// 遵循架构不变量：注册/健康类运行态真源 = Go 进程内存（map + 互斥锁），不落库、不引中间件（禁 Redis/MQ）。
// 本注册表只做「判定」；冲突落库（T12 转 conflict、写 conflict_peers、审计、告警）由调用方
// V2ControlPlaneService 在释放本锁之后完成（守「runtime 锁内不做 DB IO」）。
//
// 判定核心（往复识别，spec §4.5「bootId A 刷新注册后又收到 bootId B 的活跃请求，再次往复」）：
//   - 只有「注册」设定权威 current bootId；数据面上报只刷新活跃度、不改 current。
//   - 当某 bootId 重新注册成为 current，而它在窗口内「曾当过 current」（被别的 boot 顶替过）→ 判为往复 → 冲突。
//   - 单向切换 A→B：B 顶替 A 后 A 再不注册，current 不会「回到」A，故永不误判（含 A 的一两次拖尾上报也不触发，
//     因为上报不改 current，只有再注册才可能构成往复，而已死的 A 不会再注册）。
package bootwatch

import (
	"sort"
	"sync"
	"time"
)

// Peer 是一个 bootId 在冲突窗口内的活跃快照（供冲突详情 conflictPeers 展示，spec §5.2）。
type Peer struct {
	BootID   string
	LastAddr string
	LastSeen time.Time
}

// RegisterOutcome 是一次注册观测的判定结果。
type RegisterOutcome struct {
	// ConflictDetected 表示本次注册构成往复（被顶替过的旧 boot 重新成为 current）→ 判为并发双实例。
	ConflictDetected bool
	// Evicted 表示该 bootId 是 resolve 处置后的落败方以同一 boot 重新注册，应拒绝其重新抢占。
	Evicted bool
	// Peers 是冲突窗口内活跃的 boot 快照（冲突时至少含往复双方），供落库 conflict_peers。
	Peers []Peer
}

// ReportOutcome 是一次数据面上报观测的判定结果。
type ReportOutcome struct {
	// Evicted 表示该 bootId 是 resolve 落败方，应持续 409 + 指引（spec §4.5 处置）。
	Evicted bool
	// Stale 表示 bootId 非空且与当前 current 不一致（陈旧 boot 请求），调用方应促其重注册以喂养往复检测。
	Stale bool
}

// bootInfo 是单个 boot 的窗口内活跃快照。
type bootInfo struct {
	addr     string
	lastSeen time.Time
}

// identityState 是单个 identityId 的 boot 活跃状态（窗口内）。
type identityState struct {
	current    string               // 最近一次注册确立的权威 current bootId
	seen       map[string]*bootInfo // 窗口内出现过的 boot → 活跃快照
	wasCurrent map[string]struct{}  // 窗口内曾当过 current 的 boot 集合（识别往复）
	evicted    map[string]struct{}  // resolve 落败 boot 集合（持续拒绝）
}

// Registry 是 identityId → bootId 活跃状态的进程内注册表。
type Registry struct {
	mu  sync.Mutex
	ids map[string]*identityState
}

// New 构造空注册表。
func New() *Registry {
	return &Registry{ids: map[string]*identityState{}}
}

// OnRegister 记录一次注册观测并判定是否构成并发冲突（往复）。
// identityID / bootID 任一为空视为无效观测，返回零值（不判冲突）。
func (r *Registry) OnRegister(identityID, bootID, addr string, now time.Time, window time.Duration) RegisterOutcome {
	if identityID == "" || bootID == "" {
		return RegisterOutcome{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.stateFor(identityID)
	st.prune(now, window)

	// resolve 落败方以同一 boot 重新注册 → 拒绝（不得重新抢占，spec §4.5 处置）。
	if _, ok := st.evicted[bootID]; ok {
		return RegisterOutcome{Evicted: true, Peers: st.peers()}
	}

	var conflict bool
	switch {
	case st.current == "":
		st.current = bootID
	case bootID != st.current:
		// 换手：新 current 是否为窗口内曾当过 current 的旧 boot 重新登顶 → 往复 → 并发双实例。
		if _, returned := st.wasCurrent[bootID]; returned {
			conflict = true
		}
		st.current = bootID
	}
	st.seen[bootID] = &bootInfo{addr: addr, lastSeen: now}
	st.wasCurrent[bootID] = struct{}{}

	out := RegisterOutcome{ConflictDetected: conflict}
	if conflict {
		out.Peers = st.peers()
	}
	return out
}

// OnReport 记录一次数据面上报观测（不改变 current），返回落败 / 陈旧判定。
// bootID 为空表示未带 X-Beacon-Boot 头：兼容旧行为，不做冲突判定。
func (r *Registry) OnReport(identityID, bootID, addr string, now time.Time, window time.Duration) ReportOutcome {
	if identityID == "" {
		return ReportOutcome{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.stateFor(identityID)
	st.prune(now, window)

	if bootID == "" {
		return ReportOutcome{}
	}
	if _, ok := st.evicted[bootID]; ok {
		// 落败方持续活跃：刷新其 seen（保住 evicted 不被窗口回收），返回落败。
		st.seen[bootID] = &bootInfo{addr: addr, lastSeen: now}
		return ReportOutcome{Evicted: true}
	}
	st.seen[bootID] = &bootInfo{addr: addr, lastSeen: now}
	if st.current == "" {
		// 控制面重启后注册表为空：以首个上报 boot 播种 current（不判冲突，交由后续注册/上报再判）。
		st.current = bootID
		st.wasCurrent[bootID] = struct{}{}
		return ReportOutcome{}
	}
	if bootID != st.current {
		return ReportOutcome{Stale: true}
	}
	return ReportOutcome{}
}

// Resolve 落实冲突处置（T13）：以 keepBoot 为准，其余窗口内活跃 boot 记为落败（持续拒绝），并清往复历史避免立刻复判。
func (r *Registry) Resolve(identityID, keepBoot string, now time.Time, window time.Duration) {
	if identityID == "" || keepBoot == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.stateFor(identityID)
	st.prune(now, window)
	evicted := map[string]struct{}{}
	for boot := range st.seen {
		if boot != keepBoot {
			evicted[boot] = struct{}{}
		}
	}
	st.current = keepBoot
	st.wasCurrent = map[string]struct{}{keepBoot: {}}
	st.evicted = evicted
	if st.seen[keepBoot] == nil {
		st.seen[keepBoot] = &bootInfo{lastSeen: now}
	}
}

// Forget 清除某 identityId 的全部 boot 状态（解绑 / 拒绝等终结绑定后调用，令重新绑定从干净态开始）。
func (r *Registry) Forget(identityID string) {
	r.mu.Lock()
	delete(r.ids, identityID)
	r.mu.Unlock()
}

// Peers 返回某 identityId 冲突窗口内活跃的 boot 快照（供实时详情兜底；无状态返回 nil）。
func (r *Registry) Peers(identityID string, now time.Time, window time.Duration) []Peer {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.ids[identityID]
	if !ok {
		return nil
	}
	st.prune(now, window)
	return st.peers()
}

// stateFor 取（或建）某 identityId 的状态。
func (r *Registry) stateFor(id string) *identityState {
	st := r.ids[id]
	if st == nil {
		st = &identityState{
			seen:       map[string]*bootInfo{},
			wasCurrent: map[string]struct{}{},
			evicted:    map[string]struct{}{},
		}
		r.ids[id] = st
	}
	return st
}

// prune 移除窗口外的 boot 记录；current 自身过期则清空 current（令重启/长静默后重新播种）。
func (st *identityState) prune(now time.Time, window time.Duration) {
	cutoff := now.Add(-window)
	for boot, info := range st.seen {
		if info.lastSeen.Before(cutoff) {
			delete(st.seen, boot)
			delete(st.wasCurrent, boot)
			delete(st.evicted, boot)
			if st.current == boot {
				st.current = ""
			}
		}
	}
}

// peers 快照当前 seen（按最近活跃倒序，展示最近在前）。
func (st *identityState) peers() []Peer {
	out := make([]Peer, 0, len(st.seen))
	for boot, info := range st.seen {
		out = append(out, Peer{BootID: boot, LastAddr: info.addr, LastSeen: info.lastSeen})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}
