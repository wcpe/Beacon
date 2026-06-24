// Package runtime 持有进程内运行态：实例注册表与健康扫描（注册/健康的内存真源）。
// 三处共享态（Registry / Hub / Health）各自独立锁、互不嵌套，DB IO 全在锁外，结构上杜绝死锁。
package runtime

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// 健康状态（按心跳陈旧度递进，degraded 介于 online 与 lost 之间，FR-28）。
const (
	StatusOnline   = "online"
	StatusDegraded = "degraded" // 亚健康：心跳已变陈旧但尚未达 TTL
	StatusLost     = "lost"
	StatusOffline  = "offline"
)

// ErrDuplicateServerID 表示同 (namespace, serverId) 已有仍新鲜的另一 address 在线实例。
var ErrDuplicateServerID = errors.New("重复 serverId：已有仍新鲜的不同地址实例在线")

// CPULoadUnavailable 是 CPU / 延迟不可用哨兵（与 agent 约定一致：取不到为 -1.0）。
const CPULoadUnavailable = -1.0

// ProxyMetrics 是 bc（bungee 代理）专属负载指标（FR-34，仅展示不参与决策；bukkit 恒为零值）。
// 仅含负载计数事实（连接 / 线程 / 运行时长 / 后端可达性·延迟），不含玩家名单 / 身份（看人归③层，越界）。
type ProxyMetrics struct {
	OnlineConnections   int     // 代理在线连接数
	ThreadCount         int     // JVM 活动线程数
	UptimeMs            int64   // JVM 运行毫秒数
	BackendUp           int     // 可达后端子服数
	BackendTotal        int     // 配置的后端子服总数
	BackendAvgLatencyMs float64 // 到可达后端的平均 ping 延迟（毫秒），-1.0=无可达后端（不可用）
}

// Instance 是实例运行态条目（内存真源；标签即发现过滤维度，无 canary）。
type Instance struct {
	Namespace     string
	ServerID      string
	Role          string // bukkit / bungee
	GroupHint     string // agent 提示的大区
	ResolvedGroup string // 控制面解析回填的权威大区
	ResolvedZone  string // 控制面解析回填的权威小区（未分配为空）
	Assigned      bool   // 是否已在 zone_assignment 有归属
	Address       string // ip:port
	Version       string
	// AgentVersion 是 agent 自身构建版本（FR-86，见 ADR-0039）：agent 注册时自报，仅展示不参与决策；
	// 旧 agent 不报则空。与 Version（业务版本）语义不同，仅内存、随注册刷新、不落 DB。
	AgentVersion  string
	Capacity      int
	Weight        int
	Metadata      map[string]string
	Status        string // online / degraded / lost / offline
	PrevStatus    string // SweepExpired 变更前的旧状态（仅变更快照填，供告警判定来源；不参与展示）
	LastHeartbeat time.Time
	AppliedMD5    string  // agent 已 apply 的有效配置 md5（仅展示）
	PlayerCount   int     // 仅展示，不参与决策
	TPS           float64 // 仅展示，不参与决策
	MemUsed       int64   // JVM 已用堆字节；与 PlayerCount/TPS 同列健康事实，仅展示不参与决策（FR-32）
	MemMax        int64   // JVM 最大堆字节；仅展示不参与决策（FR-32）
	CPULoad       float64 // 进程 CPU 负载[0,1]，-1.0=不可用（近似值）；仅展示不参与决策（FR-32）
	// Proxy 是该实例（仅 bungee 代理）专属负载指标，由 bc agent 上报、控制面只存的事实（FR-34）。
	// 仅 bc 填、bukkit 恒空（零值）；与上面负载字段同列，仅展示不参与决策。
	Proxy ProxyMetrics
	// Backends 是该实例（仅 bungee 代理）当前代理的后端子服 serverId 集合，由 agent 上报、控制面只存的事实（FR-36）。
	// 仅 bc 填、bukkit 恒空；供拓扑 bc→bukkit 连线消费（FR-37）。随注册/上报刷新，仅内存、不落 DB。
	Backends     []string
	RegisteredAt time.Time
}

// clone 返回深拷贝（含 Metadata map 与 Backends 切片），供读路径在锁外安全使用。
func (i *Instance) clone() *Instance {
	c := *i
	if i.Metadata != nil {
		c.Metadata = make(map[string]string, len(i.Metadata))
		for k, v := range i.Metadata {
			c.Metadata[k] = v
		}
	}
	if i.Backends != nil {
		c.Backends = make([]string, len(i.Backends))
		copy(c.Backends, i.Backends)
	}
	return &c
}

// Filter 是实例列表/发现的标签过滤条件（空字段不过滤）。
type Filter struct {
	Namespace string
	Group     string
	Zone      string
	Role      string
	Status    string
	// Tags 是自定义元数据（metadata）键值过滤：实例 Metadata 须含全部 k=v 才命中（多 tag 取交集）；空/nil 不过滤。
	Tags map[string]string
}

// Registry 是实例注册表（内存真源），两级 map + RWMutex。
type Registry struct {
	mu    sync.RWMutex
	items map[string]map[string]*Instance // namespace -> serverId -> *Instance
}

// NewRegistry 构造空注册表。
func NewRegistry() *Registry {
	return &Registry{items: map[string]map[string]*Instance{}}
}

// Register 注册或重连一个实例，写内存注册表。
// 重复 serverId 守卫：同 (ns, serverId) 已有仍新鲜（TTL 内续约）的不同 address → 返回 ErrDuplicateServerID；
// 旧条目已超 TTL（僵尸/失联）→ 允许新 address 顶替（故障换机不误杀）；同 address 重连 → 幂等覆盖。
// 返回注册后实例的快照。
func (r *Registry) Register(in *Instance, ttl time.Duration, now time.Time) (*Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	nsMap := r.items[in.Namespace]
	if nsMap == nil {
		nsMap = map[string]*Instance{}
		r.items[in.Namespace] = nsMap
	}
	existing := nsMap[in.ServerID]
	if existing != nil && existing.Address != in.Address && now.Sub(existing.LastHeartbeat) <= ttl {
		return nil, ErrDuplicateServerID
	}

	inst := in.clone()
	inst.Status = StatusOnline
	inst.LastHeartbeat = now
	if existing != nil && existing.Address == in.Address {
		inst.RegisteredAt = existing.RegisteredAt // 同址重连保留注册时间
	} else {
		inst.RegisteredAt = now
	}
	nsMap[in.ServerID] = inst
	return inst.clone(), nil
}

// Heartbeat 刷新心跳并置 online；未注册返回 false。
func (r *Registry) Heartbeat(ns, serverID string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst := r.lookup(ns, serverID)
	if inst == nil {
		return false
	}
	inst.LastHeartbeat = now
	inst.Status = StatusOnline
	return true
}

// Report 写入 agent 上报的运行指标（人数 / TPS / 内存 / CPU，均仅展示不参与决策）；未注册返回 false。
// cpuLoad 取值 [0,1]，-1.0 表示不可用（由展示层判定），控制面不做归一化。
// proxy 为 bc 专属指标（FR-34）：非 nil 时刷新 Proxy 字段（仅 bc 上报）；nil 时不动（bukkit / 旧 agent 缺键，向后兼容）。
func (r *Registry) Report(ns, serverID, appliedMD5 string, playerCount int, tps float64, memUsed, memMax int64, cpuLoad float64, proxy *ProxyMetrics) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst := r.lookup(ns, serverID)
	if inst == nil {
		return false
	}
	inst.AppliedMD5 = appliedMD5
	inst.PlayerCount = playerCount
	inst.TPS = tps
	inst.MemUsed = memUsed
	inst.MemMax = memMax
	inst.CPULoad = cpuLoad
	if proxy != nil {
		inst.Proxy = *proxy
	}
	return true
}

// SetBackends 刷新该 bc 实例当前代理的后端子服 serverId 集合（FR-36 事实，仅内存、不涉 DB IO）；未注册返回 false。
// 传 nil/空切片表示当前无后端（清空）；写入前深拷贝，与调用方入参隔离。
func (r *Registry) SetBackends(ns, serverID string, backends []string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst := r.lookup(ns, serverID)
	if inst == nil {
		return false
	}
	if len(backends) == 0 {
		inst.Backends = nil
		return true
	}
	cp := make([]string, len(backends))
	copy(cp, backends)
	inst.Backends = cp
	return true
}

// Offline 手动下线：从内存移除条目；不存在返回 false。
func (r *Registry) Offline(ns, serverID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	nsMap := r.items[ns]
	if nsMap == nil {
		return false
	}
	if _, ok := nsMap[serverID]; !ok {
		return false
	}
	delete(nsMap, serverID)
	return true
}

// Get 返回单实例快照；不存在返回 nil。
func (r *Registry) Get(ns, serverID string) *Instance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	inst := r.lookup(ns, serverID)
	if inst == nil {
		return nil
	}
	return inst.clone()
}

// List 按标签过滤返回实例快照切片（深拷贝，锁外安全）。
func (r *Registry) List(f Filter) []*Instance {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Instance, 0)
	for ns, nsMap := range r.items {
		if f.Namespace != "" && f.Namespace != ns {
			continue
		}
		for _, inst := range nsMap {
			if matches(inst, f) {
				out = append(out, inst.clone())
			}
		}
	}
	return out
}

// CountByNamespace 返回某环境下当前内存中的实例条目数（供环境删除守卫，FR-53）。
func (r *Registry) CountByNamespace(ns string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.items[ns])
}

// StatusCounts 返回当前内存注册表按健康状态的计数（跨全部环境汇总，仅观测，FR-82）。
// 持读锁一次遍历、锁内取计数（不深拷贝、不做 DB IO）；无某状态条目则该键缺省（不返回 0 键）。
func (r *Registry) StatusCounts() map[string]int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	counts := make(map[string]int)
	for _, nsMap := range r.items {
		for _, inst := range nsMap {
			counts[inst.Status]++
		}
	}
	return counts
}

// UpdateAssignment 改派后刷新内存实例的解析归属（实例不在线则无操作）。
func (r *Registry) UpdateAssignment(ns, serverID, group, zone string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst := r.lookup(ns, serverID)
	if inst == nil {
		return
	}
	inst.ResolvedGroup = group
	inst.ResolvedZone = zone
	inst.Assigned = true
}

// ClearAssignment 取消指派后回退内存实例归属（group 退回 groupHint，zone 清空）。
func (r *Registry) ClearAssignment(ns, serverID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inst := r.lookup(ns, serverID)
	if inst == nil {
		return
	}
	inst.ResolvedGroup = inst.GroupHint
	inst.ResolvedZone = ""
	inst.Assigned = false
}

// SweepExpired 按心跳陈旧度推进健康状态机 online→degraded→lost→offline（仅降级，不删除；offline 保留）。
// 阈值须满足 degradedAfter < ttl < offlineGrace；从严到宽分档判定。
// 返回状态发生变更的实例快照（含 PrevStatus）供日志与告警判定（FR-28）。
func (r *Registry) SweepExpired(now time.Time, degradedAfter, ttl, offlineGrace time.Duration) []*Instance {
	r.mu.Lock()
	defer r.mu.Unlock()
	var changed []*Instance
	for _, nsMap := range r.items {
		for _, inst := range nsMap {
			next := healthByAge(now.Sub(inst.LastHeartbeat), degradedAfter, ttl, offlineGrace, inst.Status)
			if next != inst.Status {
				prev := inst.Status
				inst.Status = next
				snap := inst.clone()
				snap.PrevStatus = prev
				changed = append(changed, snap)
			}
		}
	}
	return changed
}

// HealthReason 返回触发当前状态的原因文案（纯函数，FR-81）：按状态选对应阈值，统一「Ns 未心跳 > <阈值名> Ns」范式。
// age 取整到秒呈现；online / 未知状态返回空串（仅展示，不参与决策，与 healthByAge 同组阈值口径一致）。
func HealthReason(age, degradedAfter, ttl, offlineGrace time.Duration, status string) string {
	ageSec := int(age.Seconds())
	switch status {
	case StatusDegraded:
		return fmt.Sprintf("%ds 未心跳 > degraded-after %ds", ageSec, int(degradedAfter.Seconds()))
	case StatusLost:
		return fmt.Sprintf("%ds 未心跳 > ttl %ds", ageSec, int(ttl.Seconds()))
	case StatusOffline:
		return fmt.Sprintf("%ds 未心跳 > offline-grace %ds", ageSec, int(offlineGrace.Seconds()))
	default:
		return ""
	}
}

// healthByAge 按心跳年龄分档返回应处状态（纯函数）；未达 degradedAfter 维持原状态（含 online）。
func healthByAge(age, degradedAfter, ttl, offlineGrace time.Duration, current string) string {
	switch {
	case age > offlineGrace:
		return StatusOffline
	case age > ttl:
		return StatusLost
	case age > degradedAfter:
		return StatusDegraded
	default:
		return current
	}
}

// lookup 取内部指针（调用方须持锁）。
func (r *Registry) lookup(ns, serverID string) *Instance {
	nsMap := r.items[ns]
	if nsMap == nil {
		return nil
	}
	return nsMap[serverID]
}

// matches 判断实例是否匹配过滤条件。
func matches(i *Instance, f Filter) bool {
	if f.Group != "" && f.Group != i.ResolvedGroup {
		return false
	}
	if f.Zone != "" && f.Zone != i.ResolvedZone {
		return false
	}
	if f.Role != "" && f.Role != i.Role {
		return false
	}
	if f.Status != "" && f.Status != i.Status {
		return false
	}
	// tag 全匹配：每个要求的 k=v 都须在实例元数据中存在且相等（缺键即排除）。
	for k, v := range f.Tags {
		if i.Metadata[k] != v {
			return false
		}
	}
	return true
}
