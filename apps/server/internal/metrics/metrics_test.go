package metrics

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/runtime"
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

// TestRuntimeAndProcessCollectorsExposed 验证 Go 运行时与进程级 collector 已随空 registry 显式注册：
// 既有 HELP / TYPE 行证明已注册，又有实际正样本值证明采集器真的在抓取时刻采到了数据（而非只注册没人采）。
func TestRuntimeAndProcessCollectorsExposed(t *testing.T) {
	m := New(runtime.NewRegistry())

	body := scrape(t, m)
	// 1) HELP / TYPE 行存在，证明 collector 已注册进独立 registry
	for _, name := range []string{
		"go_goroutines",
		"go_memstats_alloc_bytes",
		"process_resident_memory_bytes",
	} {
		if !strings.Contains(body, "# HELP "+name) {
			t.Fatalf("/metrics 应含运行时/进程指标 %s 的 HELP 行，实际输出：\n%s", name, body)
		}
		if !strings.Contains(body, "# TYPE "+name) {
			t.Fatalf("/metrics 应含运行时/进程指标 %s 的 TYPE 行，实际输出：\n%s", name, body)
		}
	}

	// 2) 有实际样本值且 >0，证明采集器确实工作：这三个都是进程运行期必为正的 gauges
	for _, name := range []string{
		"go_goroutines",
		"go_memstats_alloc_bytes",
		"process_resident_memory_bytes",
	} {
		v, ok := sampleValue(body, name)
		if !ok {
			t.Fatalf("/metrics 应含指标 %s 的样本行，实际输出：\n%s", name, body)
		}
		if v <= 0 {
			t.Fatalf("指标 %s 应有正样本值，实际 %v", name, v)
		}
	}
}

// TestObserveHTTPExposesREDMetrics 验证 HTTP 层 RED 指标：请求计数按方法 / 路由模板 / 状态码分维，
// 时延直方图输出分桶与 _count / _sum 序列（Prometheus 据此算 p95 / p99）。
func TestObserveHTTPExposesREDMetrics(t *testing.T) {
	m := New(runtime.NewRegistry())
	const route = "/admin/v2/config-files/{id}"
	// 耗时取 0.25 / 0.5 这类可精确表示的浮点数，_sum 断言不被二进制浮点误差干扰
	m.ObserveHTTP(http.MethodGet, route, http.StatusOK, 0.25)
	m.ObserveHTTP(http.MethodGet, route, http.StatusOK, 0.5)
	m.ObserveHTTP(http.MethodPost, route, http.StatusUnauthorized, 0.001)

	body := scrape(t, m)
	// 计数：同维度累加；不同状态码分属不同序列（元组与 Prometheus 文本输出顺序一致）
	if !containsSample(body, `beacon_http_requests_total{method="GET",route="/admin/v2/config-files/{id}",status="200"} 2`) {
		t.Fatalf("GET / 200 请求计数应为 2，实际：\n%s", body)
	}
	if !containsSample(body, `beacon_http_requests_total{method="POST",route="/admin/v2/config-files/{id}",status="401"} 1`) {
		t.Fatalf("POST / 401 请求计数应为 1，实际：\n%s", body)
	}
	// 直方图聚合序列：_count = 观测次数、_sum = 耗时总和（rate 后供 histogram_quantile 使用）
	if !containsSample(body, `beacon_http_request_duration_seconds_count{method="GET",route="/admin/v2/config-files/{id}"} 2`) {
		t.Fatalf("GET 时延直方图 _count 应为 2，实际：\n%s", body)
	}
	if !containsSample(body, `beacon_http_request_duration_seconds_sum{method="GET",route="/admin/v2/config-files/{id}"} 0.75`) {
		t.Fatalf("GET 时延直方图 _sum 应为 0.75，实际：\n%s", body)
	}
	// 分桶（累计上界）：0.25 落 le=0.25，0.5 落 le=0.5，两次观测都落 +Inf
	if !containsSample(body, `beacon_http_request_duration_seconds_bucket{method="GET",route="/admin/v2/config-files/{id}",le="0.25"} 1`) {
		t.Fatalf("le=0.25 桶应为 1，实际：\n%s", body)
	}
	if !containsSample(body, `beacon_http_request_duration_seconds_bucket{method="GET",route="/admin/v2/config-files/{id}",le="0.5"} 2`) {
		t.Fatalf("le=0.5 桶应为 2，实际：\n%s", body)
	}
	if !containsSample(body, `beacon_http_request_duration_seconds_bucket{method="GET",route="/admin/v2/config-files/{id}",le="+Inf"} 2`) {
		t.Fatalf("+Inf 桶应为 2，实际：\n%s", body)
	}
}

// TestObserveHTTPSlowRequestBucket 验证 >10s 的长请求有专属分桶：分桶若止于 Prometheus 默认的 10s，
// 30s 长轮询 / 流式传输的观测值会全部堆进 +Inf 桶，分位数被上界拉平、完全失真。
func TestObserveHTTPSlowRequestBucket(t *testing.T) {
	m := New(runtime.NewRegistry())
	const route = "/beacon/v1/agent/stream"
	m.ObserveHTTP(http.MethodGet, route, http.StatusOK, 35)

	body := scrape(t, m)
	if !containsSample(body, `beacon_http_request_duration_seconds_bucket{method="GET",route="/beacon/v1/agent/stream",le="30"} 0`) {
		t.Fatalf("35s 观测不应落进 le=30 桶，实际：\n%s", body)
	}
	if !containsSample(body, `beacon_http_request_duration_seconds_bucket{method="GET",route="/beacon/v1/agent/stream",le="60"} 1`) {
		t.Fatalf("35s 观测应落进 le=60 桶，实际：\n%s", body)
	}
}

// scrape 抓取一次 /metrics 文本。
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
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

// sampleValue 从抓取文本取出某**无标签**指标的样本值（如 "go_goroutines 5" → 5）。
// 匹配前缀带尾随空格，故不会误中带后缀的同名指标（如 go_memstats_alloc_bytes_total），
// 含 "{" / "}" 的带标签样本行也会被跳过。
func sampleValue(body, name string) (float64, bool) {
	prefix := name + " "
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := strings.TrimPrefix(line, prefix)
		if strings.ContainsAny(rest, "{}") {
			continue
		}
		if v, err := strconv.ParseFloat(rest, 64); err == nil {
			return v, true
		}
	}
	return 0, false
}
