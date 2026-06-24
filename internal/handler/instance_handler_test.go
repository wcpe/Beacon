package handler

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/runtime"
)

// stubHealthSettings 是健康阈值窄读接口的测试替身（FR-61 同字面 key）。
type stubHealthSettings struct {
	values map[string]int
}

func (s stubHealthSettings) GetInt(key string) int { return s.values[key] }

func newStubHealthSettings() stubHealthSettings {
	return stubHealthSettings{values: map[string]int{
		"health.degraded-after-sec": 15,
		"health.ttl-sec":            30,
		"health.offline-grace-sec":  120,
	}}
}

// TestToInstanceViewHealthReason 渲染视图按当前时刻 + 阈值算 lastHeartbeatAgeSec 与 healthReason（FR-81）。
func TestToInstanceViewHealthReason(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	h := &InstanceHandler{health: newStubHealthSettings(), now: func() time.Time { return now }}

	cases := []struct {
		name       string
		status     string
		hbAgo      time.Duration
		wantAgeSec int
		wantReason string
	}{
		{"online 空原因", runtime.StatusOnline, 5 * time.Second, 5, ""},
		{"lost 显 ttl", runtime.StatusLost, 35 * time.Second, 35, "35s 未心跳 > ttl 30s"},
		{"degraded 显 degraded-after", runtime.StatusDegraded, 20 * time.Second, 20, "20s 未心跳 > degraded-after 15s"},
		{"offline 显 offline-grace", runtime.StatusOffline, 130 * time.Second, 130, "130s 未心跳 > offline-grace 120s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			inst := &runtime.Instance{
				Namespace: "prod", ServerID: "a", Status: c.status,
				LastHeartbeat: now.Add(-c.hbAgo),
			}
			v := toInstanceView(inst, map[string]bool{}, h.renderCtx())
			if v.LastHeartbeatAgeSec != c.wantAgeSec {
				t.Fatalf("lastHeartbeatAgeSec = %d，期望 %d", v.LastHeartbeatAgeSec, c.wantAgeSec)
			}
			if v.HealthReason != c.wantReason {
				t.Fatalf("healthReason = %q，期望 %q", v.HealthReason, c.wantReason)
			}
		})
	}
}

// TestToInstanceViewAgeNeverNegative 心跳时刻晚于当前时刻（时钟回拨）时 age 归零，不出负值。
func TestToInstanceViewAgeNeverNegative(t *testing.T) {
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	h := &InstanceHandler{health: newStubHealthSettings(), now: func() time.Time { return now }}
	inst := &runtime.Instance{
		Namespace: "prod", ServerID: "a", Status: runtime.StatusOnline,
		LastHeartbeat: now.Add(3 * time.Second), // 未来时刻
	}
	v := toInstanceView(inst, map[string]bool{}, h.renderCtx())
	if v.LastHeartbeatAgeSec != 0 {
		t.Fatalf("时钟回拨时 age 应归零，实际 %d", v.LastHeartbeatAgeSec)
	}
}
