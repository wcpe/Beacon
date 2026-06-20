package service

import (
	"testing"
	"time"

	"beacon/internal/runtime"
)

// reg 注册一个实例到给定注册表（测试辅助）。
func regInstance(t *testing.T, r *runtime.Registry, inst *runtime.Instance) {
	t.Helper()
	if _, err := r.Register(inst, 30*time.Second, time.Now().UTC()); err != nil {
		t.Fatalf("注册实例 %s 失败: %v", inst.ServerID, err)
	}
}

// TestTopologyBuildNodesAndEdges 验证：节点含全部可用实例，bc→bukkit 边按 backends 事实生成。
func TestTopologyBuildNodesAndEdges(t *testing.T) {
	r := runtime.NewRegistry()
	regInstance(t, r, &runtime.Instance{
		Namespace: "prod", ServerID: "bc-1", Role: "bungee", ResolvedGroup: "area1", ResolvedZone: "",
		Address: "10.0.0.1:25577", Backends: []string{"lobby-1", "pvp-1"},
	})
	regInstance(t, r, &runtime.Instance{
		Namespace: "prod", ServerID: "lobby-1", Role: "bukkit", ResolvedGroup: "area1", ResolvedZone: "z1",
		Address: "10.0.0.2:25565",
	})
	regInstance(t, r, &runtime.Instance{
		Namespace: "prod", ServerID: "pvp-1", Role: "bukkit", ResolvedGroup: "area1", ResolvedZone: "z2",
		Address: "10.0.0.3:25565",
	})

	topo := NewTopologyService(r).Build("prod")

	if len(topo.Nodes) != 3 {
		t.Fatalf("节点数应为 3，实际 %d", len(topo.Nodes))
	}
	// 节点按 serverId 排序：bc-1 / lobby-1 / pvp-1
	if topo.Nodes[0].ServerID != "bc-1" || topo.Nodes[0].Role != "bungee" {
		t.Fatalf("首节点应为 bc-1(bungee)，实际 %+v", topo.Nodes[0])
	}
	// 两条边：bc-1→lobby-1、bc-1→pvp-1
	if len(topo.Edges) != 2 {
		t.Fatalf("边数应为 2，实际 %d: %+v", len(topo.Edges), topo.Edges)
	}
	if topo.Edges[0].Source != "bc-1" || topo.Edges[0].Target != "lobby-1" {
		t.Fatalf("首边应为 bc-1→lobby-1，实际 %+v", topo.Edges[0])
	}
	if topo.Edges[1].Target != "pvp-1" {
		t.Fatalf("次边目标应为 pvp-1，实际 %+v", topo.Edges[1])
	}
}

// TestTopologyEdgeSkipsOfflineBackend 验证：backends 中已不在可用集合的后端不画悬挂边。
func TestTopologyEdgeSkipsOfflineBackend(t *testing.T) {
	r := runtime.NewRegistry()
	// bc 报了两个后端，但 ghost-1 从未注册（不在可用集合）→ 不应成边。
	regInstance(t, r, &runtime.Instance{
		Namespace: "prod", ServerID: "bc-1", Role: "bungee", ResolvedGroup: "area1",
		Address: "10.0.0.1:25577", Backends: []string{"lobby-1", "ghost-1"},
	})
	regInstance(t, r, &runtime.Instance{
		Namespace: "prod", ServerID: "lobby-1", Role: "bukkit", ResolvedGroup: "area1", ResolvedZone: "z1",
		Address: "10.0.0.2:25565",
	})

	topo := NewTopologyService(r).Build("prod")

	if len(topo.Edges) != 1 {
		t.Fatalf("仅 lobby-1 在册，边数应为 1，实际 %d: %+v", len(topo.Edges), topo.Edges)
	}
	if topo.Edges[0].Target != "lobby-1" {
		t.Fatalf("唯一边目标应为 lobby-1，实际 %+v", topo.Edges[0])
	}
}

// TestTopologyExcludesUnavailableInstances 验证：lost/offline 实例不进节点、其后端边亦不生成。
func TestTopologyExcludesUnavailableInstances(t *testing.T) {
	r := runtime.NewRegistry()
	regInstance(t, r, &runtime.Instance{
		Namespace: "prod", ServerID: "bc-1", Role: "bungee", ResolvedGroup: "area1",
		Address: "10.0.0.1:25577", Backends: []string{"lobby-1"},
	})
	regInstance(t, r, &runtime.Instance{
		Namespace: "prod", ServerID: "lobby-1", Role: "bukkit", ResolvedGroup: "area1", ResolvedZone: "z1",
		Address: "10.0.0.2:25565",
	})
	// 把 lobby-1 推进到 offline（心跳极旧），其应被剔除出可用集合。
	r.SweepExpired(time.Now().Add(time.Hour), 10*time.Second, 20*time.Second, 30*time.Second)
	// 仅让 lobby-1 过期：重新心跳 bc-1 维持其在线。
	r.Heartbeat("prod", "bc-1", time.Now().UTC())

	topo := NewTopologyService(r).Build("prod")

	for _, n := range topo.Nodes {
		if n.ServerID == "lobby-1" {
			t.Fatalf("offline 的 lobby-1 不应进节点，实际 %+v", topo.Nodes)
		}
	}
	if len(topo.Edges) != 0 {
		t.Fatalf("后端 lobby-1 已离线，bc-1 不应有边，实际 %+v", topo.Edges)
	}
}

// TestTopologyGroupsAggregateByGroupZone 验证：按 (group,zone) 聚合成员且稳定排序。
func TestTopologyGroupsAggregateByGroupZone(t *testing.T) {
	r := runtime.NewRegistry()
	regInstance(t, r, &runtime.Instance{Namespace: "prod", ServerID: "a2", Role: "bukkit", ResolvedGroup: "area1", ResolvedZone: "z1", Address: "10.0.0.1:1"})
	regInstance(t, r, &runtime.Instance{Namespace: "prod", ServerID: "a1", Role: "bukkit", ResolvedGroup: "area1", ResolvedZone: "z1", Address: "10.0.0.2:1"})
	regInstance(t, r, &runtime.Instance{Namespace: "prod", ServerID: "b1", Role: "bukkit", ResolvedGroup: "area2", ResolvedZone: "z9", Address: "10.0.0.3:1"})

	topo := NewTopologyService(r).Build("prod")

	if len(topo.Groups) != 2 {
		t.Fatalf("应聚合为 2 组，实际 %d: %+v", len(topo.Groups), topo.Groups)
	}
	// 组按 group→zone 排序：area1/z1 在前
	if topo.Groups[0].Group != "area1" || topo.Groups[0].Zone != "z1" {
		t.Fatalf("首组应为 area1/z1，实际 %+v", topo.Groups[0])
	}
	// 成员按 serverId 排序：a1 在 a2 前
	if len(topo.Groups[0].Members) != 2 || topo.Groups[0].Members[0] != "a1" {
		t.Fatalf("area1/z1 成员应为 [a1 a2]，实际 %+v", topo.Groups[0].Members)
	}
	if topo.Groups[1].Group != "area2" {
		t.Fatalf("次组应为 area2，实际 %+v", topo.Groups[1])
	}
}

// TestTopologyNamespaceIsolation 验证：只返回指定 namespace 的实例。
func TestTopologyNamespaceIsolation(t *testing.T) {
	r := runtime.NewRegistry()
	regInstance(t, r, &runtime.Instance{Namespace: "prod", ServerID: "p1", Role: "bukkit", Address: "10.0.0.1:1"})
	regInstance(t, r, &runtime.Instance{Namespace: "test", ServerID: "t1", Role: "bukkit", Address: "10.0.0.2:1"})

	topo := NewTopologyService(r).Build("prod")

	if len(topo.Nodes) != 1 || topo.Nodes[0].ServerID != "p1" {
		t.Fatalf("prod 拓扑应仅含 p1，实际 %+v", topo.Nodes)
	}
}
