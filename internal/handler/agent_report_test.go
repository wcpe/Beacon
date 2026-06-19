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
