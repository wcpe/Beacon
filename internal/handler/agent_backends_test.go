package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/internal/runtime"
)

// TestRegisterRequestParsesBackends 验证注册请求体解析 bc 上报的后端 serverId 集合（FR-36）。
func TestRegisterRequestParsesBackends(t *testing.T) {
	body := `{"namespace":"prod","serverId":"bc-1","role":"bungee","backends":["lobby-1","lobby-2"]}`
	var req registerRequest
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&req); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(req.Backends) != 2 || req.Backends[0] != "lobby-1" || req.Backends[1] != "lobby-2" {
		t.Fatalf("backends 解析错误：%v", req.Backends)
	}
}

// TestRegisterRequestBackwardCompatNoBackends 验证旧 agent / bukkit 不发 backends 时缺省为 nil（向后兼容）。
func TestRegisterRequestBackwardCompatNoBackends(t *testing.T) {
	body := `{"namespace":"prod","serverId":"lobby-1","role":"bukkit"}`
	var req registerRequest
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&req); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if req.Backends != nil {
		t.Fatalf("缺 backends 键应缺省为 nil，实际 %v", req.Backends)
	}
}

// TestReportRequestParsesBackendsPointer 验证上报请求体用指针区分「缺键」与「显式空集」。
func TestReportRequestParsesBackendsPointer(t *testing.T) {
	// 显式空集：bc 当前无后端 → 指针非空、切片空。
	bodyEmpty := `{"namespace":"prod","serverId":"bc-1","backends":[]}`
	var reqEmpty reportRequest
	if err := json.NewDecoder(strings.NewReader(bodyEmpty)).Decode(&reqEmpty); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if reqEmpty.Backends == nil {
		t.Fatal("显式 backends:[] 应解析为非空指针（区分缺键）")
	}
	if len(*reqEmpty.Backends) != 0 {
		t.Fatalf("显式空集应为空切片，实际 %v", *reqEmpty.Backends)
	}

	// 缺键：旧 agent / bukkit → 指针为 nil。
	bodyMissing := `{"namespace":"prod","serverId":"lobby-1"}`
	var reqMissing reportRequest
	if err := json.NewDecoder(strings.NewReader(bodyMissing)).Decode(&reqMissing); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if reqMissing.Backends != nil {
		t.Fatalf("缺 backends 键应缺省为 nil 指针，实际 %v", *reqMissing.Backends)
	}
}

// TestInstanceViewOutputsBackends 验证实例视图输出 backends（供 FR-37 拓扑消费）。
func TestInstanceViewOutputsBackends(t *testing.T) {
	view := toInstanceView(&runtime.Instance{
		Namespace: "prod", ServerID: "bc-1", Role: "bungee",
		Backends: []string{"lobby-1", "lobby-2"},
	}, nil)
	out, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if !strings.Contains(string(out), `"backends":["lobby-1","lobby-2"]`) {
		t.Fatalf("实例视图应输出 backends，实际 %s", out)
	}
}

// TestInstanceViewMarksZoneDefaultEntry 验证实例视图按默认入口集合标 zoneDefaultEntry（FR-48）。
func TestInstanceViewMarksZoneDefaultEntry(t *testing.T) {
	defaults := map[string]bool{"lobby-1": true}
	// 命中默认入口集合的 bukkit → true
	hit := toInstanceView(&runtime.Instance{Namespace: "prod", ServerID: "lobby-1", Role: "bukkit"}, defaults)
	if !hit.ZoneDefaultEntry {
		t.Fatalf("命中默认入口集合的实例应标 zoneDefaultEntry=true")
	}
	// 未命中 → false
	miss := toInstanceView(&runtime.Instance{Namespace: "prod", ServerID: "lobby-2", Role: "bukkit"}, defaults)
	if miss.ZoneDefaultEntry {
		t.Fatalf("未命中默认入口集合的实例应标 zoneDefaultEntry=false")
	}
	// JSON 字段存在
	out, err := json.Marshal(hit)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if !strings.Contains(string(out), `"zoneDefaultEntry":true`) {
		t.Fatalf("实例视图应输出 zoneDefaultEntry，实际 %s", out)
	}
}
