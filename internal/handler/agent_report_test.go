package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

// cpuLoadUnavailable 是 CPU 不可用哨兵（与 agent 约定一致：取不到时上报 / 缺省为 -1.0）。
const cpuLoadUnavailableSentinel = -1.0

// TestReportRequestParseNewKeys 验证新增三键 memUsed / memMax / cpuLoad 被正确解析。
func TestReportRequestParseNewKeys(t *testing.T) {
	body := `{"namespace":"prod","serverId":"lobby-1","appliedMd5":"m","playerCount":42,"tps":19.9,"memUsed":134217728,"memMax":536870912,"cpuLoad":0.35}`
	var req reportRequest
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&req); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	req.applyDefaults()
	if req.PlayerCount != 42 || req.TPS != 19.9 {
		t.Fatalf("人数 / TPS 解析错误：%+v", req)
	}
	if req.MemUsed != 134217728 || req.MemMax != 536870912 {
		t.Fatalf("内存解析错误：memUsed=%d memMax=%d", req.MemUsed, req.MemMax)
	}
	if req.CPULoad() != 0.35 {
		t.Fatalf("CPU 解析错误：%v", req.CPULoad())
	}
}

// TestReportRequestBackwardCompatMissingKeys 验证旧 agent 不发新键时向后兼容：内存缺省 0、CPU 缺省 -1.0（不可用）。
func TestReportRequestBackwardCompatMissingKeys(t *testing.T) {
	// 旧 agent 报文：仅含 playerCount / tps，无 memUsed / memMax / cpuLoad。
	body := `{"namespace":"prod","serverId":"lobby-1","appliedMd5":"m","playerCount":5,"tps":20.0}`
	var req reportRequest
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&req); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	req.applyDefaults()
	if req.MemUsed != 0 || req.MemMax != 0 {
		t.Fatalf("缺内存键应缺省为 0，实际 memUsed=%d memMax=%d", req.MemUsed, req.MemMax)
	}
	if req.CPULoad() != cpuLoadUnavailableSentinel {
		t.Fatalf("缺 cpuLoad 键应缺省为 -1.0（不可用），实际 %v", req.CPULoad())
	}
}

// TestReportRequestExplicitCPUZeroPreserved 验证显式上报 cpuLoad=0 不被当作缺省（与"缺键 → -1.0"区分）。
func TestReportRequestExplicitCPUZeroPreserved(t *testing.T) {
	body := `{"namespace":"prod","serverId":"lobby-1","playerCount":5,"tps":20.0,"cpuLoad":0}`
	var req reportRequest
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&req); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	req.applyDefaults()
	if req.CPULoad() != 0 {
		t.Fatalf("显式 cpuLoad=0 应保留为 0（非空闲哨兵），实际 %v", req.CPULoad())
	}
}

// TestReportRequestParseProxy 验证 bc 上报的 proxy 子对象被正确解析并映射为运行态 BC 指标（FR-34）。
func TestReportRequestParseProxy(t *testing.T) {
	body := `{"namespace":"prod","serverId":"bc-1","appliedMd5":"m","playerCount":50,"tps":0,` +
		`"proxy":{"onlineConnections":128,"threadCount":64,"uptimeMs":3600000,"backendUp":3,"backendTotal":4,"backendAvgLatencyMs":12.5}}`
	var req reportRequest
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&req); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	p := req.Proxy.toRuntime()
	if p == nil {
		t.Fatal("bc 上报含 proxy 子对象，toRuntime 不应为 nil")
	}
	if p.OnlineConnections != 128 || p.ThreadCount != 64 || p.UptimeMs != 3600000 ||
		p.BackendUp != 3 || p.BackendTotal != 4 || p.BackendAvgLatencyMs != 12.5 {
		t.Fatalf("proxy 字段解析错误：%+v", p)
	}
}

// TestReportRequestProxyMissingBackwardCompat 验证旧 agent / bukkit 缺 proxy 键时向后兼容：toRuntime 返回 nil（不刷新 BC 字段）。
func TestReportRequestProxyMissingBackwardCompat(t *testing.T) {
	// bukkit / 旧 agent 报文：无 proxy 子对象。
	body := `{"namespace":"prod","serverId":"lobby-1","appliedMd5":"m","playerCount":5,"tps":20.0}`
	var req reportRequest
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&req); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if req.Proxy != nil {
		t.Fatalf("缺 proxy 键时 Proxy 应为 nil，实际 %+v", req.Proxy)
	}
	if req.Proxy.toRuntime() != nil {
		t.Fatal("缺 proxy 键时 toRuntime 应返回 nil（不刷新 BC 字段，向后兼容）")
	}
}
