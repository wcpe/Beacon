package runtime

import (
	"testing"
	"time"
)

var (
	t0           = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ttl          = 30 * time.Second
	offlineGrace = 120 * time.Second
)

func sample(ns, serverID, addr string) *Instance {
	return &Instance{
		Namespace: ns, ServerID: serverID, Role: "bukkit", GroupHint: "area1",
		ResolvedGroup: "area1", ResolvedZone: "zoneA", Assigned: true,
		Address: addr, Version: "1.0", Capacity: 200, Weight: 100,
		Metadata: map[string]string{"region": "cn-east"},
	}
}

// TestRegisterAndGet 注册后可取到快照且状态 online。
func TestRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Register(sample("prod", "lobby-1", "10.0.0.1:25565"), ttl, t0); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	got := r.Get("prod", "lobby-1")
	if got == nil || got.Status != StatusOnline || !got.LastHeartbeat.Equal(t0) {
		t.Fatalf("注册后状态错误: %+v", got)
	}
}

// TestDuplicateServerIDGuardFresh 仍新鲜的不同地址 → 拒绝。
func TestDuplicateServerIDGuardFresh(t *testing.T) {
	r := NewRegistry()
	_, _ = r.Register(sample("prod", "lobby-1", "10.0.0.1:25565"), ttl, t0)
	_, err := r.Register(sample("prod", "lobby-1", "10.0.0.2:25565"), ttl, t0.Add(10*time.Second))
	if err != ErrDuplicateServerID {
		t.Fatalf("仍新鲜的不同地址应被拒，实际 err=%v", err)
	}
}

// TestDuplicateServerIDTakeoverStale 旧条目超 TTL（故障换机）→ 允许新地址顶替。
func TestDuplicateServerIDTakeoverStale(t *testing.T) {
	r := NewRegistry()
	_, _ = r.Register(sample("prod", "lobby-1", "10.0.0.1:25565"), ttl, t0)
	got, err := r.Register(sample("prod", "lobby-1", "10.0.0.2:25565"), ttl, t0.Add(31*time.Second))
	if err != nil {
		t.Fatalf("故障换机应允许顶替，却报错: %v", err)
	}
	if got.Address != "10.0.0.2:25565" {
		t.Fatalf("顶替后地址应更新，实际 %s", got.Address)
	}
}

// TestSameAddressReconnectIdempotent 同地址重连幂等覆盖、保留注册时间。
func TestSameAddressReconnectIdempotent(t *testing.T) {
	r := NewRegistry()
	first, _ := r.Register(sample("prod", "lobby-1", "10.0.0.1:25565"), ttl, t0)
	again, err := r.Register(sample("prod", "lobby-1", "10.0.0.1:25565"), ttl, t0.Add(5*time.Second))
	if err != nil {
		t.Fatalf("同地址重连不应报错: %v", err)
	}
	if !again.RegisteredAt.Equal(first.RegisteredAt) {
		t.Fatalf("同地址重连应保留注册时间，%v != %v", again.RegisteredAt, first.RegisteredAt)
	}
}

// TestHealthTTLTransitions online→lost→offline，offline 保留不删。
func TestHealthTTLTransitions(t *testing.T) {
	r := NewRegistry()
	_, _ = r.Register(sample("prod", "lobby-1", "10.0.0.1:25565"), ttl, t0)

	// 未超时：保持 online
	if changed := r.SweepExpired(t0.Add(ttl), ttl, offlineGrace); len(changed) != 0 {
		t.Fatalf("未超 TTL 不应变更，实际 %d", len(changed))
	}
	// 超 TTL：online→lost
	r.SweepExpired(t0.Add(ttl+time.Second), ttl, offlineGrace)
	if r.Get("prod", "lobby-1").Status != StatusLost {
		t.Fatalf("超 TTL 应转 lost")
	}
	// 超 offlineGrace：lost→offline
	r.SweepExpired(t0.Add(offlineGrace+time.Second), ttl, offlineGrace)
	off := r.Get("prod", "lobby-1")
	if off == nil || off.Status != StatusOffline {
		t.Fatalf("超 offlineGrace 应转 offline 且保留，实际 %+v", off)
	}
}

// TestHeartbeatRecoversFromLost 收到心跳即回 online。
func TestHeartbeatRecoversFromLost(t *testing.T) {
	r := NewRegistry()
	_, _ = r.Register(sample("prod", "lobby-1", "10.0.0.1:25565"), ttl, t0)
	r.SweepExpired(t0.Add(ttl+time.Second), ttl, offlineGrace) // → lost
	if !r.Heartbeat("prod", "lobby-1", t0.Add(40*time.Second)) {
		t.Fatal("心跳应成功")
	}
	if r.Get("prod", "lobby-1").Status != StatusOnline {
		t.Fatal("心跳后应回 online")
	}
}

// TestHeartbeatUnknownReturnsFalse 未注册心跳返回 false。
func TestHeartbeatUnknownReturnsFalse(t *testing.T) {
	r := NewRegistry()
	if r.Heartbeat("prod", "ghost", t0) {
		t.Fatal("未注册心跳应返回 false")
	}
}

// TestOfflineRemoves 手动下线后取不到。
func TestOfflineRemoves(t *testing.T) {
	r := NewRegistry()
	_, _ = r.Register(sample("prod", "lobby-1", "10.0.0.1:25565"), ttl, t0)
	if !r.Offline("prod", "lobby-1") {
		t.Fatal("下线应成功")
	}
	if r.Get("prod", "lobby-1") != nil {
		t.Fatal("下线后应取不到")
	}
}

// TestListFilter 标签过滤。
func TestListFilter(t *testing.T) {
	r := NewRegistry()
	_, _ = r.Register(sample("prod", "lobby-1", "10.0.0.1:1"), ttl, t0)
	bungee := sample("prod", "proxy-1", "10.0.0.2:2")
	bungee.Role = "bungee"
	bungee.ResolvedZone = "zoneB"
	_, _ = r.Register(bungee, ttl, t0)

	if got := r.List(Filter{Namespace: "prod", Role: "bungee"}); len(got) != 1 || got[0].ServerID != "proxy-1" {
		t.Fatalf("按 role 过滤错误：%v", got)
	}
	if got := r.List(Filter{Namespace: "prod", Zone: "zoneA"}); len(got) != 1 || got[0].ServerID != "lobby-1" {
		t.Fatalf("按 zone 过滤错误：%v", got)
	}
	if got := r.List(Filter{Namespace: "prod"}); len(got) != 2 {
		t.Fatalf("无过滤应返回全部，实际 %d", len(got))
	}
}

// TestDeepCopyIsolation 读返回深拷贝，改动不影响内部状态。
func TestDeepCopyIsolation(t *testing.T) {
	r := NewRegistry()
	_, _ = r.Register(sample("prod", "lobby-1", "10.0.0.1:1"), ttl, t0)
	snap := r.Get("prod", "lobby-1")
	snap.Status = "TAMPERED"
	snap.Metadata["region"] = "TAMPERED"
	fresh := r.Get("prod", "lobby-1")
	if fresh.Status != StatusOnline || fresh.Metadata["region"] != "cn-east" {
		t.Fatalf("快照被外部改动影响了内部状态：%+v", fresh)
	}
}

// TestUpdateAndClearAssignment 改派/取消指派刷新内存归属。
func TestUpdateAndClearAssignment(t *testing.T) {
	r := NewRegistry()
	_, _ = r.Register(sample("prod", "lobby-1", "10.0.0.1:1"), ttl, t0)
	r.UpdateAssignment("prod", "lobby-1", "area2", "zoneZ")
	if g := r.Get("prod", "lobby-1"); g.ResolvedGroup != "area2" || g.ResolvedZone != "zoneZ" {
		t.Fatalf("改派未刷新内存：%+v", g)
	}
	r.ClearAssignment("prod", "lobby-1")
	if g := r.Get("prod", "lobby-1"); g.Assigned || g.ResolvedZone != "" || g.ResolvedGroup != "area1" {
		t.Fatalf("取消指派应回退到 groupHint：%+v", g)
	}
}
