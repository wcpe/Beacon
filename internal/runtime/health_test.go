package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/runtime/alert"
)

// capturingAlerter 收集分发到的告警，供断言触发判定。
type capturingAlerter struct {
	got []alert.Alert
}

func (c *capturingAlerter) Name() string { return "capturing" }

func (c *capturingAlerter) Notify(_ context.Context, a alert.Alert) error {
	c.got = append(c.got, a)
	return nil
}

// fakeHealthSettings 是 HealthSettings 的测试替身：以可变 map 驱动健康阈值 / 扫描周期热改（FR-61）。
type fakeHealthSettings struct {
	mu     sync.Mutex
	values map[string]int
}

func newFakeHealthSettings(values map[string]int) *fakeHealthSettings {
	return &fakeHealthSettings{values: values}
}

func (f *fakeHealthSettings) GetInt(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.values[key]
}

func (f *fakeHealthSettings) set(key string, v int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key] = v
}

// TestScannerAlertsOnAbnormalTransitions 进入 degraded/lost/offline 触发告警，恢复 online 不触发（FR-28）。
func TestScannerAlertsOnAbnormalTransitions(t *testing.T) {
	capturer := &capturingAlerter{}
	s := NewHealthScanner(NewRegistry(), newFakeHealthSettings(map[string]int{
		"health.degraded-after-sec": 15, "health.ttl-sec": 30,
		"health.offline-grace-sec": 120, "health.scan-interval-sec": 1,
	}), alert.NewDispatcher(capturer))

	changed := []*Instance{
		{Namespace: "prod", ServerID: "a", Address: "1.1.1.1:1", PrevStatus: StatusOnline, Status: StatusDegraded},
		{Namespace: "prod", ServerID: "b", Address: "1.1.1.2:1", PrevStatus: StatusDegraded, Status: StatusLost},
		{Namespace: "prod", ServerID: "c", Address: "1.1.1.3:1", PrevStatus: StatusLost, Status: StatusOffline},
		{Namespace: "prod", ServerID: "d", Address: "1.1.1.4:1", PrevStatus: StatusLost, Status: StatusOnline}, // 恢复，不告警
	}
	s.dispatchAlerts(context.Background(), changed)

	if len(capturer.got) != 3 {
		t.Fatalf("3 个异常转移应各告警 1 次（恢复不告警），实际 %d", len(capturer.got))
	}
	for _, a := range capturer.got {
		if a.Status == StatusOnline {
			t.Fatalf("恢复 online 不应告警，却收到 %+v", a)
		}
	}
}

// TestScannerRunReadsThresholdFromStore 健康扫描每轮从设置 store 读阈值热生效（FR-61）：
// 初始 ttl 很大不转 lost；运行中把 ttl 热改为极小，下一轮即据新阈值推进实例转 lost。
func TestScannerRunReadsThresholdFromStore(t *testing.T) {
	reg := NewRegistry()
	// 注册时刻置于 10s 前：实例自此未再心跳，其陈旧度恒 >=10s。
	// 初始阈值极大（不超时），热改阈值为 1s 后下一轮即据新值判其陈旧、推进出 online。
	t0 := time.Now().UTC().Add(-10 * time.Second)
	if _, err := reg.Register(&Instance{Namespace: "prod", ServerID: "stale", Role: "bukkit", Address: "10.0.0.1:25565"}, 30*time.Second, t0); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	// 初始阈值极大（实例永不超时）、扫描周期 1s（ticker 最小粒度为秒）。
	settings := newFakeHealthSettings(map[string]int{
		"health.degraded-after-sec": 1000000, "health.ttl-sec": 1000000,
		"health.offline-grace-sec": 1000000, "health.scan-interval-sec": 1,
	})
	s := NewHealthScanner(reg, settings, alert.NewDispatcher())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	// 先跑一轮多确认阈值极大时实例保持 online（证明扫描器确在读 store 的大阈值）。
	if !waitUntilTrue(1500*time.Millisecond, func() bool {
		insts := reg.List(Filter{Namespace: "prod"})
		return len(insts) == 1 && insts[0].Status == StatusOnline
	}) {
		t.Fatal("阈值极大时实例应保持 online（扫描器应读 store 大阈值）")
	}

	// 热改阈值为极小：后续扫描轮即据新值把陈旧实例推进出 online（证明每轮重读 store、热生效）。
	settings.set("health.degraded-after-sec", 1)
	settings.set("health.ttl-sec", 1)
	settings.set("health.offline-grace-sec", 1)

	if !waitUntilTrue(3*time.Second, func() bool {
		insts := reg.List(Filter{Namespace: "prod"})
		return len(insts) == 1 && insts[0].Status != StatusOnline
	}) {
		t.Fatal("热改 ttl 为极小后，实例应在后续扫描轮被推进出 online（设置热生效）")
	}
}

// waitUntilTrue 在超时内轮询条件，满足返回 true、超时返回 false（不 Fatal，供断言取反）。
func waitUntilTrue(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}
