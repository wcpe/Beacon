package runtime

import (
	"testing"
	"time"
)

// TestHealthReason 覆盖各状态的原因文案：online 空串、degraded/lost/offline 各档显「Ns 未心跳 > <阈值名> Ns」（FR-81）。
func TestHealthReason(t *testing.T) {
	degradedAfter := 15 * time.Second
	ttl := 30 * time.Second
	offlineGrace := 120 * time.Second

	cases := []struct {
		name   string
		age    time.Duration
		status string
		want   string
	}{
		{"online 无原因", 5 * time.Second, StatusOnline, ""},
		{"degraded 显 degraded-after 阈值", 20 * time.Second, StatusDegraded, "20s 未心跳 > degraded-after 15s"},
		{"lost 显 ttl 阈值", 35 * time.Second, StatusLost, "35s 未心跳 > ttl 30s"},
		{"offline 显 offline-grace 阈值", 130 * time.Second, StatusOffline, "130s 未心跳 > offline-grace 120s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := HealthReason(c.age, degradedAfter, ttl, offlineGrace, c.status)
			if got != c.want {
				t.Fatalf("HealthReason(%v,%s) = %q，期望 %q", c.age, c.status, got, c.want)
			}
		})
	}
}

// TestHealthReasonAgeFloorsToSeconds age 取整到秒（如 35.9s 显 35s），保证文案稳定不带小数。
func TestHealthReasonAgeFloorsToSeconds(t *testing.T) {
	got := HealthReason(35900*time.Millisecond, 15*time.Second, 30*time.Second, 120*time.Second, StatusLost)
	want := "35s 未心跳 > ttl 30s"
	if got != want {
		t.Fatalf("HealthReason 取整到秒应得 %q，实际 %q", want, got)
	}
}

// TestHealthReasonUnknownStatus 未知状态回退空串（防御性，不臆造文案）。
func TestHealthReasonUnknownStatus(t *testing.T) {
	if got := HealthReason(99*time.Second, 15*time.Second, 30*time.Second, 120*time.Second, "weird"); got != "" {
		t.Fatalf("未知状态应得空串，实际 %q", got)
	}
}
