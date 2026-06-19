package runtime

import "testing"

// TestListTagFilterMatchAndExclude 验证 tag（metadata 键值）过滤：命中、缺键排除、多 tag 取交集。
func TestListTagFilterMatchAndExclude(t *testing.T) {
	r := NewRegistry()
	// east：region=cn-east, tier=premium
	east := sample("prod", "east-1", "10.0.0.1:1")
	east.Metadata = map[string]string{"region": "cn-east", "tier": "premium"}
	_, _ = r.Register(east, ttl, t0)
	// west：region=cn-west, tier=premium
	west := sample("prod", "west-1", "10.0.0.2:2")
	west.Metadata = map[string]string{"region": "cn-west", "tier": "premium"}
	_, _ = r.Register(west, ttl, t0)
	// 无元数据实例：任何 tag 过滤都应排除
	plain := sample("prod", "plain-1", "10.0.0.3:3")
	plain.Metadata = nil
	_, _ = r.Register(plain, ttl, t0)

	// 单 tag 命中
	if got := r.List(Filter{Namespace: "prod", Tags: map[string]string{"region": "cn-east"}}); len(got) != 1 || got[0].ServerID != "east-1" {
		t.Fatalf("单 tag region=cn-east 应仅命中 east-1，实际 %v", got)
	}
	// 多 tag 取交集：region=cn-east 且 tier=premium → 仅 east-1
	if got := r.List(Filter{Namespace: "prod", Tags: map[string]string{"region": "cn-east", "tier": "premium"}}); len(got) != 1 || got[0].ServerID != "east-1" {
		t.Fatalf("多 tag 交集应仅命中 east-1，实际 %v", got)
	}
	// 共同 tag：tier=premium → east-1 + west-1，排除无元数据的 plain-1
	if got := r.List(Filter{Namespace: "prod", Tags: map[string]string{"tier": "premium"}}); len(got) != 2 {
		t.Fatalf("tier=premium 应命中 2 个（排除无元数据），实际 %d：%v", len(got), got)
	}
	// 值不匹配：region=cn-south → 空
	if got := r.List(Filter{Namespace: "prod", Tags: map[string]string{"region": "cn-south"}}); len(got) != 0 {
		t.Fatalf("不存在的 tag 值应命中 0 个，实际 %v", got)
	}
	// 缺键排除：要求 dc=any，无实例含该键 → 空
	if got := r.List(Filter{Namespace: "prod", Tags: map[string]string{"dc": "any"}}); len(got) != 0 {
		t.Fatalf("无实例含 dc 键应命中 0 个，实际 %v", got)
	}
}

// TestTopologyDigestStableForSameTopology 同一拓扑（无关字段变化）摘要不变；拓扑相关字段变化摘要变。
func TestTopologyDigestStableForSameTopology(t *testing.T) {
	a := &Instance{Namespace: "prod", ServerID: "s1", Role: "bukkit", ResolvedGroup: "g1", ResolvedZone: "z1", Address: "10.0.0.1:1", Status: StatusOnline}
	b := &Instance{Namespace: "prod", ServerID: "s2", Role: "bungee", ResolvedGroup: "g1", ResolvedZone: "z2", Address: "10.0.0.2:2", Status: StatusOnline}

	base := TopologyDigest([]*Instance{a, b})
	// 入参顺序不影响摘要（内部排序）。
	if reordered := TopologyDigest([]*Instance{b, a}); reordered != base {
		t.Fatalf("入参顺序不应影响摘要：%q != %q", reordered, base)
	}
	// 非拓扑字段（playerCount/tps/心跳/版本）变化不应改变摘要。
	a2 := *a
	a2.PlayerCount = 99
	a2.TPS = 19.9
	a2.Version = "9.9"
	if same := TopologyDigest([]*Instance{&a2, b}); same != base {
		t.Fatalf("非拓扑字段变化不应改变摘要：%q != %q", same, base)
	}
}

// TestTopologyDigestChangesOnTopologyChange 上线/下线/改派/改状态都应改变摘要。
func TestTopologyDigestChangesOnTopologyChange(t *testing.T) {
	a := &Instance{Namespace: "prod", ServerID: "s1", Role: "bukkit", ResolvedGroup: "g1", ResolvedZone: "z1", Address: "10.0.0.1:1", Status: StatusOnline}
	base := TopologyDigest([]*Instance{a})

	// 新增实例（上线）
	b := &Instance{Namespace: "prod", ServerID: "s2", Role: "bukkit", ResolvedGroup: "g1", ResolvedZone: "z1", Address: "10.0.0.2:2", Status: StatusOnline}
	if TopologyDigest([]*Instance{a, b}) == base {
		t.Fatal("新增实例应改变摘要")
	}
	// 改派 zone
	reassigned := *a
	reassigned.ResolvedZone = "z2"
	if TopologyDigest([]*Instance{&reassigned}) == base {
		t.Fatal("改派 zone 应改变摘要")
	}
	// 改状态（online→degraded）
	degraded := *a
	degraded.Status = StatusDegraded
	if TopologyDigest([]*Instance{&degraded}) == base {
		t.Fatal("状态变化应改变摘要")
	}
	// 空拓扑（全下线）摘要应与非空不同。
	if TopologyDigest(nil) == base {
		t.Fatal("空拓扑摘要应不同于非空")
	}
}
