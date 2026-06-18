package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"beacon/internal/runtime"
)

// TestMetricsExposeRegisteredCollectors 验证 /metrics 暴露四类核心指标且可抓取。
func TestMetricsExposeRegisteredCollectors(t *testing.T) {
	reg := runtime.NewRegistry()
	// 注册一个实例，使纯标签驱动的 instances_registered gauge 至少有一条样本（否则 Prometheus 不输出 HELP）
	if _, err := reg.Register(&runtime.Instance{
		Namespace: "prod", ServerID: "s1", Role: "bukkit", Address: "10.0.0.1:25565",
	}, 30*time.Second, time.Now().UTC()); err != nil {
		t.Fatalf("注册实例失败: %v", err)
	}
	m := New(reg)

	body := scrape(t, m)
	for _, name := range []string{
		"beacon_instances_registered",
		"beacon_instances_status",
		"beacon_config_publish_total",
		"beacon_push_notify_total",
	} {
		// HELP 行在任何样本前总会输出，确保指标已注册到独立 registry
		if !strings.Contains(body, "# HELP "+name) {
			t.Fatalf("/metrics 应包含已注册指标 %s，实际输出：\n%s", name, body)
		}
	}
}

// TestInstancesGaugeReflectsRegistry 注册一个在线实例后，注册数与状态分布 gauge 应反映之。
func TestInstancesGaugeReflectsRegistry(t *testing.T) {
	reg := runtime.NewRegistry()
	if _, err := reg.Register(&runtime.Instance{
		Namespace: "prod", ServerID: "s1", Role: "bukkit", Address: "10.0.0.1:25565",
	}, 30*time.Second, time.Now().UTC()); err != nil {
		t.Fatalf("注册实例失败: %v", err)
	}
	m := New(reg)

	body := scrape(t, m)
	// 注册数：namespace=prod、role=bukkit 计 1
	if !containsSample(body, `beacon_instances_registered{namespace="prod",role="bukkit"} 1`) {
		t.Fatalf("注册数 gauge 应为 prod/bukkit=1，实际：\n%s", body)
	}
	// 状态分布：online=1
	if !containsSample(body, `beacon_instances_status{status="online"} 1`) {
		t.Fatalf("状态分布 gauge 应为 online=1，实际：\n%s", body)
	}
	// degraded 须有 0 基线序列（无 degraded 实例时也输出，避免曲线断点）
	if !containsSample(body, `beacon_instances_status{status="degraded"} 0`) {
		t.Fatalf("状态分布 gauge 应含 degraded=0 基线，实际：\n%s", body)
	}
}

// TestCountersIncrement 配置发布与推送计数器自增后应在 /metrics 反映。
func TestCountersIncrement(t *testing.T) {
	m := New(runtime.NewRegistry())
	m.IncConfigPublish()
	m.IncConfigPublish()
	m.IncPushNotify()

	body := scrape(t, m)
	if !containsSample(body, "beacon_config_publish_total 2") {
		t.Fatalf("配置发布计数应为 2，实际：\n%s", body)
	}
	if !containsSample(body, "beacon_push_notify_total 1") {
		t.Fatalf("推送计数应为 1，实际：\n%s", body)
	}
}

// scrape 抓取一次 /metrics 文本。
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("/metrics 应返回 200，实际 %d", w.Code)
	}
	return w.Body.String()
}

// containsSample 判断抓取文本是否包含某条样本行（按行精确匹配，避免子串误判）。
func containsSample(body, sample string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == sample {
			return true
		}
	}
	return false
}
