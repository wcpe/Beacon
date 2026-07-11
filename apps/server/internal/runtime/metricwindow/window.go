// Package metricwindow 持有每实例最近 60s（默认 12 个 5s 批）的内存指标窗口（FR-144，见 §3.6）。
// 它是控制面「每实例最新指标」的内存真源，供后续健康计算（P4b）与 /dashboard 实时读消费。
//
// 独立 RWMutex，与三大运行态锁（Registry / Hub / Health）互不嵌套；读返深拷贝，DB IO 一律在锁外
// （本包不碰 DB）。仿 runtime/alert.InboxAlerter 的进程内固定容量环形结构。
package metricwindow

import (
	"sort"
	"sync"
)

// DefaultCapacity 是每实例窗口保留的批数（60s / 5s = 12）。
const DefaultCapacity = 12

// Sample 是一个 5s 批的聚合样本（内存态，字段对齐 model.MetricSampleV2 但不绑 GORM）。
type Sample struct {
	NamespaceID     uint
	ServerID        string
	Kind            string
	BucketStartMs   int64
	SampleCount     int
	CPUPctAvg       float64
	CPUPctMax       float64
	MemUsedMbAvg    float64
	MemMaxMb        int
	TPSAvg          float64
	TPSMin          float64
	OnlineAvg       int
	OnlineMax       int
	MaxOnline       int
	ConnAvg         int
	ConnMax         int
	BackendUp       int
	BackendTotal    int
	BackendRttMsAvg float64
	ReportRttMs     int
	// ReceivedAtMs 是控制面接收该批的时刻（毫秒），供活性 / 老化判定（P4b 健康计算消费）。
	ReceivedAtMs int64
}

// serverKey 是窗口按 (namespace, server) 定位的键（serverId 仅 namespace 内唯一，跨 ns 须带 ns 区分）。
type serverKey struct {
	namespaceID uint
	serverID    string
}

// Store 是全实例的 60s 指标窗口集合，独立 RWMutex 保护。
type Store struct {
	mu       sync.RWMutex
	capacity int
	// byServer：每实例一个按 bucket_start_ms 升序的环形样本切片（长度 ≤ capacity）。
	byServer map[serverKey][]Sample
}

// New 构造窗口；capacity<=0 兜底为 DefaultCapacity。
func New(capacity int) *Store {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Store{capacity: capacity, byServer: make(map[serverKey][]Sample)}
}

// Upsert 把一个批样本并入对应实例窗口，返回该 bucket 是否为窗口新增。
//
//   - 若窗口已含同 bucket_start_ms（补报 / 重放）→ 原地更新其内容，返回 false（去重信号）。
//   - 否则按 bucket_start_ms 有序插入；超容量则淘汰最旧批（保留最近 capacity 个）。返回 true。
func (s *Store) Upsert(sample Sample) (isNew bool) {
	key := serverKey{namespaceID: sample.NamespaceID, serverID: sample.ServerID}
	s.mu.Lock()
	defer s.mu.Unlock()
	ring := s.byServer[key]
	// 已存在同桶：原地更新（补报覆盖），非新增。
	for i := range ring {
		if ring[i].BucketStartMs == sample.BucketStartMs {
			ring[i] = sample
			return false
		}
	}
	// 新桶：追加后按桶起点升序排序，超容量砍掉最旧的。
	ring = append(ring, sample)
	sort.Slice(ring, func(i, j int) bool { return ring[i].BucketStartMs < ring[j].BucketStartMs })
	if len(ring) > s.capacity {
		ring = ring[len(ring)-s.capacity:]
	}
	s.byServer[key] = ring
	return true
}

// Contains 判断某实例窗口是否已含指定 bucket_start_ms（只读，不改状态）。
// 供接收服务在「写窗口 / 入队」前先分类新桶 vs 重放桶——分类只读、提交（Upsert）在入队成功后，
// 避免入队满回 429 却已改窗口，导致 agent 重发时被误判重放而丢数据。
func (s *Store) Contains(namespaceID uint, serverID string, bucketStartMs int64) bool {
	key := serverKey{namespaceID: namespaceID, serverID: serverID}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, smp := range s.byServer[key] {
		if smp.BucketStartMs == bucketStartMs {
			return true
		}
	}
	return false
}

// List 返回某实例窗口的样本快照（深拷贝，最旧在前）；无数据返回空切片。
func (s *Store) List(namespaceID uint, serverID string) []Sample {
	key := serverKey{namespaceID: namespaceID, serverID: serverID}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ring := s.byServer[key]
	out := make([]Sample, len(ring))
	copy(out, ring)
	return out
}

// Latest 返回某实例窗口最新一个批样本；无数据返回 (Sample{}, false)。
func (s *Store) Latest(namespaceID uint, serverID string) (Sample, bool) {
	key := serverKey{namespaceID: namespaceID, serverID: serverID}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ring := s.byServer[key]
	if len(ring) == 0 {
		return Sample{}, false
	}
	return ring[len(ring)-1], true
}

// ServerCount 返回当前持有窗口数据的实例数（自观测 / 测试用）。
func (s *Store) ServerCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byServer)
}
