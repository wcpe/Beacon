// Package healthview 持有每实例当前健康视图的内存真源（FR-147，见 §3.6）：
// score / level / schedulable / reasons / factors / 计算时刻由健康计算轮整批产出，
// 供管理面查询与调度决策共用同一份判定结果（单一真源）；快照日表仅为回放副本。
//
// 仿 runtime/metricwindow：独立 RWMutex，与三大运行态锁（Registry / Hub / Health）互不嵌套；
// 锁内纯内存操作、读返深拷贝，DB IO 一律在锁外（本包不碰 DB）。
package healthview

import "sync"

// 健康等级常量（§4.4：score ≥ healthyMin → healthy；≥ degradedMin → degraded；否则 unhealthy）。
const (
	// LevelHealthy 健康。
	LevelHealthy = "healthy"
	// LevelDegraded 降级（仍可调度，仅作决策排序劣势）。
	LevelDegraded = "degraded"
	// LevelUnhealthy 不健康（不可调度原因之一）。
	LevelUnhealthy = "unhealthy"
)

// 不可调度原因码常量（§4.5 全枚举，可叠加进 View.Reasons）。
const (
	// ReasonKindNotSchedulable kind ≠ backend（proxy 不作调度候选，健康仅展示）。
	ReasonKindNotSchedulable = "kind_not_schedulable"
	// ReasonPendingConfirm agent 身份未人工确认。
	ReasonPendingConfirm = "pending_confirm"
	// ReasonUnassigned 未分配到小区（zone 为空）。
	ReasonUnassigned = "unassigned"
	// ReasonDisabled 身份被禁用。
	ReasonDisabled = "disabled"
	// ReasonDraining 排空中。
	ReasonDraining = "draining"
	// ReasonLost 超过 30s 无指标批（失联）。
	ReasonLost = "lost"
	// ReasonUnhealthy 健康等级为 unhealthy。
	ReasonUnhealthy = "unhealthy"
)

// Factor 单因子明细（回放解释用）
type Factor struct {
	Factor     string  // 因子名：tps/cpu/capacity/conn/latency/alert
	Raw        float64 // 原始输入值
	Normalized float64 // 归一化 0~100
	Weight     float64 // 配置权重
	Applicable bool    // 是否适用（不适用不计分）
}

// View 单实例当前健康视图（内存真源；快照表仅回放副本）
type View struct {
	NamespaceID  uint
	Namespace    string // namespace code
	ServerID     string
	Kind         string // proxy / backend
	ZoneName     string // v2 zone 名，未分配为空
	Score        int    // 0-100
	Level        string // healthy / degraded / unhealthy
	Schedulable  bool
	Reasons      []string // §4.5 原因码，可叠加
	Factors      []Factor
	WeightsRev   int
	OnlineCount  int
	MaxOnline    int
	ComputedAtMs int64
}

// serverKey 是视图按 (namespace, server) 定位的键（serverId 仅 namespace 内唯一，跨 ns 须带 ns 区分）。
type serverKey struct {
	namespaceID uint
	serverID    string
}

// Store 是全实例健康视图集合，独立 RWMutex 保护；每轮健康计算经 ReplaceAll 整批原子替换。
type Store struct {
	mu       sync.RWMutex
	byServer map[serverKey]View
}

// NewStore 构造空视图集合。
func NewStore() *Store {
	return &Store{byServer: make(map[serverKey]View)}
}

// ReplaceAll 用本轮计算结果整批原子替换全部视图：先在锁外深拷贝建新 map，锁内仅指针交换，
// 旧视图（含未出现在本批的实例）随之整体消失，不残留陈旧条目。
func (s *Store) ReplaceAll(views []View) {
	next := make(map[serverKey]View, len(views))
	for _, v := range views {
		next[serverKey{namespaceID: v.NamespaceID, serverID: v.ServerID}] = cloneView(v)
	}
	s.mu.Lock()
	s.byServer = next
	s.mu.Unlock()
}

// Get 返回某实例当前健康视图的深拷贝；未命中返回 (View{}, false)。
func (s *Store) Get(namespaceID uint, serverID string) (View, bool) {
	s.mu.RLock()
	v, ok := s.byServer[serverKey{namespaceID: namespaceID, serverID: serverID}]
	s.mu.RUnlock()
	if !ok {
		return View{}, false
	}
	return cloneView(v), true
}

// List 返回全部视图的深拷贝快照（顺序不保证），调用方自行筛选。
func (s *Store) List() []View {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]View, 0, len(s.byServer))
	for _, v := range s.byServer {
		out = append(out, cloneView(v))
	}
	return out
}

// Count 返回当前持有视图的实例数（自观测 / 测试用）。
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byServer)
}

// cloneView 深拷贝一份视图：View 按值复制，切片字段（Reasons / Factors）另拷贝底层数组，
// 使存入与读出的视图与调用方互不共享可变内存。
func cloneView(v View) View {
	if v.Reasons != nil {
		rs := make([]string, len(v.Reasons))
		copy(rs, v.Reasons)
		v.Reasons = rs
	}
	if v.Factors != nil {
		fs := make([]Factor, len(v.Factors))
		copy(fs, v.Factors)
		v.Factors = fs
	}
	return v
}
