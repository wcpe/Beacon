package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/runtime"
	"github.com/wcpe/Beacon/internal/service"
)

// regTopoInstance 注册一个实例到注册表（拓扑 handler 测试辅助）。
func regTopoInstance(t *testing.T, r *runtime.Registry, inst *runtime.Instance) {
	t.Helper()
	if _, err := r.Register(inst, 30*time.Second, time.Now().UTC()); err != nil {
		t.Fatalf("注册实例 %s 失败: %v", inst.ServerID, err)
	}
}

// TestTopologyHandlerReturnsGraph 验证端点返回节点 + bc→bukkit 边 + 分组，且角色 / 状态字段就位。
func TestTopologyHandlerReturnsGraph(t *testing.T) {
	r := runtime.NewRegistry()
	regTopoInstance(t, r, &runtime.Instance{
		Namespace: "prod", ServerID: "bc-1", Role: "bungee", ResolvedGroup: "area1",
		Address: "10.0.0.1:25577", Backends: []string{"lobby-1"},
	})
	regTopoInstance(t, r, &runtime.Instance{
		Namespace: "prod", ServerID: "lobby-1", Role: "bukkit", ResolvedGroup: "area1", ResolvedZone: "z1",
		Address: "10.0.0.2:25565",
	})
	h := NewTopologyHandler(service.NewTopologyService(r))

	rec := httptest.NewRecorder()
	h.Topology(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/topology?namespace=prod", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应为 200，实际 %d", rec.Code)
	}
	var body topologyView
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体解析失败: %v", err)
	}
	if body.Namespace != "prod" {
		t.Fatalf("namespace 应为 prod，实际 %q", body.Namespace)
	}
	if len(body.Nodes) != 2 {
		t.Fatalf("节点数应为 2，实际 %d", len(body.Nodes))
	}
	// 首节点 bc-1：bungee 角色、未分配 zone（zone=null）
	if body.Nodes[0].ServerID != "bc-1" || body.Nodes[0].Role != "bungee" {
		t.Fatalf("首节点应为 bc-1(bungee)，实际 %+v", body.Nodes[0])
	}
	if body.Nodes[0].Zone != nil {
		t.Fatalf("bc-1 未分配 zone，应为 null，实际 %v", *body.Nodes[0].Zone)
	}
	// lobby-1 的 zone 应为 z1
	if body.Nodes[1].Zone == nil || *body.Nodes[1].Zone != "z1" {
		t.Fatalf("lobby-1 zone 应为 z1，实际 %+v", body.Nodes[1])
	}
	if len(body.Edges) != 1 || body.Edges[0].Source != "bc-1" || body.Edges[0].Target != "lobby-1" {
		t.Fatalf("应有一条 bc-1→lobby-1 边，实际 %+v", body.Edges)
	}
	// bc-1（zone=null）与 lobby-1（zone=z1）同大区不同 zone，故聚成两组（均属 area1）。
	if len(body.Groups) != 2 {
		t.Fatalf("应聚为两组（area1/null 与 area1/z1），实际 %+v", body.Groups)
	}
	// 组按 group→zone 排序，空 zone 排在 z1 前
	if body.Groups[0].Group != "area1" || body.Groups[0].Zone != nil || body.Groups[0].Members[0] != "bc-1" {
		t.Fatalf("首组应为 area1/null{bc-1}，实际 %+v", body.Groups[0])
	}
	if body.Groups[1].Zone == nil || *body.Groups[1].Zone != "z1" || body.Groups[1].Members[0] != "lobby-1" {
		t.Fatalf("次组应为 area1/z1{lobby-1}，实际 %+v", body.Groups[1])
	}
}

// TestTopologyHandlerRequiresNamespace 验证缺 namespace 参数返回 400。
func TestTopologyHandlerRequiresNamespace(t *testing.T) {
	h := NewTopologyHandler(service.NewTopologyService(runtime.NewRegistry()))

	rec := httptest.NewRecorder()
	h.Topology(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/topology", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 namespace 应返回 400，实际 %d", rec.Code)
	}
}

// TestTopologyHandlerEmptyNamespace 验证空拓扑返回 200 且各列表为空数组（非 null）。
func TestTopologyHandlerEmptyNamespace(t *testing.T) {
	h := NewTopologyHandler(service.NewTopologyService(runtime.NewRegistry()))

	rec := httptest.NewRecorder()
	h.Topology(rec, httptest.NewRequest(http.MethodGet, "/admin/v1/topology?namespace=prod", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应为 200，实际 %d", rec.Code)
	}
	// 断言序列化为空数组而非 null，前端无须判空
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("响应体解析失败: %v", err)
	}
	for _, key := range []string{"nodes", "edges", "groups"} {
		if string(raw[key]) != "[]" {
			t.Fatalf("%s 应序列化为空数组 []，实际 %s", key, raw[key])
		}
	}
}
