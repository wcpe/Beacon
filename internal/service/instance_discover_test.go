package service

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/runtime"
)

// TestDiscoverIncludesDegradedExcludesLost 验证服务发现包含 online + degraded（亚健康仍可用），
// 但排除 lost/offline（FR-28 引入 degraded 后的回归保护：degraded 不应被过早摘除）。
func TestDiscoverIncludesDegradedExcludesLost(t *testing.T) {
	const (
		degradedAfter = 15 * time.Second
		ttl           = 30 * time.Second
		offlineGrace  = 120 * time.Second
	)
	reg := runtime.NewRegistry()
	// 注册/健康真源是内存，Discover 只读 registry，发现路径不触达 DB 仓库，故传 nil。
	svc := NewInstanceService(reg, nil, nil, 10*time.Second, ttl)

	t0 := time.Now().UTC()
	mk := func(id string) *runtime.Instance {
		return &runtime.Instance{Namespace: "prod", ServerID: id, Role: "bukkit", Address: "10.0.0.1:25565"}
	}
	if _, err := reg.Register(mk("fresh"), ttl, t0); err != nil {
		t.Fatalf("注册 fresh 失败: %v", err)
	}
	if _, err := reg.Register(mk("stale"), ttl, t0); err != nil {
		t.Fatalf("注册 stale 失败: %v", err)
	}

	// t1：fresh 续心跳保持 online；stale 心跳停在 t0 → degraded
	t1 := t0.Add(degradedAfter + time.Second)
	reg.Heartbeat("prod", "fresh", t1)
	reg.SweepExpired(t1, degradedAfter, ttl, offlineGrace)

	got := serverIDSet(svc.Discover(runtime.Filter{Namespace: "prod"}))
	if !got["fresh"] || !got["stale"] {
		t.Fatalf("degraded 实例应保留在发现结果，期望含 {fresh, stale}，实际 %v", got)
	}
	if len(got) != 2 {
		t.Fatalf("期望发现 2 个（online+degraded），实际 %d：%v", len(got), got)
	}

	// t2：stale 心跳已超 ttl → lost；fresh 续心跳仍 online
	t2 := t1.Add(ttl)
	reg.Heartbeat("prod", "fresh", t2)
	reg.SweepExpired(t2, degradedAfter, ttl, offlineGrace)

	got2 := serverIDSet(svc.Discover(runtime.Filter{Namespace: "prod"}))
	if got2["stale"] {
		t.Fatalf("lost 实例应被排除出发现结果，实际仍含 stale：%v", got2)
	}
	if !got2["fresh"] || len(got2) != 1 {
		t.Fatalf("期望发现仅 {fresh}，实际 %v", got2)
	}
}

func serverIDSet(insts []*runtime.Instance) map[string]bool {
	s := make(map[string]bool, len(insts))
	for _, i := range insts {
		s[i.ServerID] = true
	}
	return s
}
