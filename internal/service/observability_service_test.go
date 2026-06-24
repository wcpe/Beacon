package service

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	rt "github.com/wcpe/Beacon/internal/runtime"
)

// fakeDBStats 是 dbStatsProvider 测试替身：返回预置的连接池统计。
type fakeDBStats struct {
	stats sql.DBStats
}

func (f fakeDBStats) Stats() sql.DBStats { return f.stats }

// fakeWaiter 是 waiterCounter 测试替身：返回预置挂起数。
type fakeWaiter int

func (f fakeWaiter) WaiterCount() int { return int(f) }

// fakeCmdCounter 是 commandCounter 测试替身：返回预置计数或错误。
type fakeCmdCounter struct {
	counts map[string]int
	err    error
}

func (f fakeCmdCounter) CountByStatus() (map[string]int, error) { return f.counts, f.err }

// newObsRegistry 构造含若干在线实例的注册表（供注册表规模断言）。
func newObsRegistry(t *testing.T, n int) *rt.Registry {
	t.Helper()
	reg := rt.NewRegistry()
	for i := 0; i < n; i++ {
		if _, err := reg.Register(&rt.Instance{
			Namespace: "prod", ServerID: serverIDOf(i), Address: addrOf(i),
		}, 30*time.Second, time.Now()); err != nil {
			t.Fatalf("注册实例失败: %v", err)
		}
	}
	return reg
}

func serverIDOf(i int) string { return "lobby-" + string(rune('a'+i)) }
func addrOf(i int) string     { return "10.0.0." + string(rune('1'+i)) + ":25565" }

// TestObservabilitySnapshot 验证四组指标聚合：连接池透传、多 hub 挂起合计、注册表规模、命令队列计数。
func TestObservabilitySnapshot(t *testing.T) {
	reg := newObsRegistry(t, 3) // 三个在线实例
	svc := NewObservabilityService(
		fakeDBStats{stats: sql.DBStats{
			MaxOpenConnections: 10, OpenConnections: 4, InUse: 1, Idle: 3,
			WaitCount: 7, WaitDuration: 250 * time.Millisecond,
		}},
		reg,
		fakeWaiter(2), fakeWaiter(1), fakeWaiter(0), fakeWaiter(5), // config/file/topology/command
		fakeCmdCounter{counts: map[string]int{"pending": 2, "fetched": 1}},
	)

	snap := svc.Snapshot()

	// DB 连接池透传（WaitDuration 转毫秒）。
	if snap.DBPool.MaxOpenConnections != 10 || snap.DBPool.OpenConnections != 4 ||
		snap.DBPool.InUse != 1 || snap.DBPool.Idle != 3 || snap.DBPool.WaitCount != 7 {
		t.Fatalf("连接池统计透传不一致: %+v", snap.DBPool)
	}
	if snap.DBPool.WaitDurationMs != 250 {
		t.Fatalf("WaitDuration 应转为 250ms，实际 %d", snap.DBPool.WaitDurationMs)
	}

	// 长轮询四通道挂起 + 合计。
	if snap.Longpoll.Config != 2 || snap.Longpoll.File != 1 || snap.Longpoll.Topology != 0 || snap.Longpoll.Command != 5 {
		t.Fatalf("长轮询各通道挂起不一致: %+v", snap.Longpoll)
	}
	if snap.Longpoll.Total != 8 {
		t.Fatalf("长轮询挂起合计应为 8，实际 %d", snap.Longpoll.Total)
	}

	// 注册表规模：3 在线、总数 3。
	if snap.RegistryTotal != 3 || snap.RegistryByStatus[rt.StatusOnline] != 3 {
		t.Fatalf("注册表规模不一致: total=%d byStatus=%+v", snap.RegistryTotal, snap.RegistryByStatus)
	}

	// 命令队列计数透传。
	if snap.CommandByStatus["pending"] != 2 || snap.CommandByStatus["fetched"] != 1 {
		t.Fatalf("命令队列计数不一致: %+v", snap.CommandByStatus)
	}
}

// TestObservabilityCommandCountError 命令计数失败时优雅降级为空 map，不影响其余指标。
func TestObservabilityCommandCountError(t *testing.T) {
	reg := newObsRegistry(t, 1)
	svc := NewObservabilityService(
		fakeDBStats{stats: sql.DBStats{OpenConnections: 1}},
		reg,
		fakeWaiter(0), fakeWaiter(0), fakeWaiter(0), fakeWaiter(0),
		fakeCmdCounter{err: errors.New("库已停")},
	)

	snap := svc.Snapshot()
	if snap.CommandByStatus == nil || len(snap.CommandByStatus) != 0 {
		t.Fatalf("命令计数失败时应降级为空 map，实际 %+v", snap.CommandByStatus)
	}
	// 其余指标仍正常采集。
	if snap.RegistryTotal != 1 || snap.DBPool.OpenConnections != 1 {
		t.Fatalf("命令计数失败不应影响其余指标: total=%d pool=%+v", snap.RegistryTotal, snap.DBPool)
	}
}
