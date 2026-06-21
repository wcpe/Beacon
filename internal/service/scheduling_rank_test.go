package service

import (
	"testing"

	"github.com/wcpe/Beacon/internal/runtime"
)

// inst 构造一个在线实例样本（落位排序只看 status/weight/capacity/serverId）。
func inst(serverID string, weight, capacity int, status string) *runtime.Instance {
	return &runtime.Instance{
		Namespace: "prod", ServerID: serverID, Role: "bukkit",
		ResolvedGroup: "area1", ResolvedZone: "zoneA", Assigned: true,
		Address: serverID + ":25565", Version: "1.0",
		Weight: weight, Capacity: capacity, Status: status,
		// playerCount/tps 故意置非零，验证落位不读它们（仅展示、不参与决策）
		PlayerCount: 999, TPS: 0.1,
	}
}

// ids 抽取候选的 serverId 顺序，便于断言排序。
func ids(cands []PlacementCandidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.ServerID)
	}
	return out
}

// TestRankPlacementOrder weight 降序 → capacity 降序 → serverId 升序确定性排序。
func TestRankPlacementOrder(t *testing.T) {
	insts := []*runtime.Instance{
		inst("c", 50, 100, runtime.StatusOnline),  // 低权重，垫底
		inst("a", 100, 200, runtime.StatusOnline), // 同权重、容量大 → 居前
		inst("b", 100, 100, runtime.StatusOnline), // 同权重、容量小 → 居后
		inst("d", 100, 100, runtime.StatusOnline), // 与 b 同权重同容量 → 按 serverId 升序，d 在 b 之后
	}
	got := ids(RankPlacement(insts, nil))
	want := []string{"a", "b", "d", "c"}
	if len(got) != len(want) {
		t.Fatalf("候选数错误：want %v got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("排序错误：want %v got %v", want, got)
		}
	}
}

// TestRankPlacementExcludesNonOnline 非 online（lost/offline）实例不进候选。
func TestRankPlacementExcludesNonOnline(t *testing.T) {
	insts := []*runtime.Instance{
		inst("a", 100, 100, runtime.StatusOnline),
		inst("b", 200, 100, runtime.StatusLost),    // lost 不计
		inst("c", 200, 100, runtime.StatusOffline), // offline 不计
	}
	got := ids(RankPlacement(insts, nil))
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("仅 online 应进候选，实际 %v", got)
	}
}

// TestRankPlacementDrainExcluded 被 drain 的实例即使在线也从候选剔除。
func TestRankPlacementDrainExcluded(t *testing.T) {
	insts := []*runtime.Instance{
		inst("a", 100, 100, runtime.StatusOnline),
		inst("b", 200, 100, runtime.StatusOnline), // 本应居首，但被 drain
	}
	drained := map[string]bool{"b": true}
	got := ids(RankPlacement(insts, drained))
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("被 drain 的应被剔除，实际 %v", got)
	}
}

// TestRankPlacementEmpty 空集 → 空候选（不 panic、不报错）。
func TestRankPlacementEmpty(t *testing.T) {
	if got := RankPlacement(nil, nil); len(got) != 0 {
		t.Fatalf("空集应返回空候选，实际 %v", ids(got))
	}
	if got := RankPlacement([]*runtime.Instance{}, nil); len(got) != 0 {
		t.Fatalf("空切片应返回空候选，实际 %v", ids(got))
	}
}

// TestRankPlacementAllBusy 全部 drain 或全部离线 → 空候选。
func TestRankPlacementAllBusy(t *testing.T) {
	allOnline := []*runtime.Instance{
		inst("a", 100, 100, runtime.StatusOnline),
		inst("b", 100, 100, runtime.StatusOnline),
	}
	allDrained := map[string]bool{"a": true, "b": true}
	if got := RankPlacement(allOnline, allDrained); len(got) != 0 {
		t.Fatalf("全部 drain 应返回空候选，实际 %v", ids(got))
	}
	allOffline := []*runtime.Instance{
		inst("a", 100, 100, runtime.StatusOffline),
		inst("b", 100, 100, runtime.StatusLost),
	}
	if got := RankPlacement(allOffline, nil); len(got) != 0 {
		t.Fatalf("全部离线应返回空候选，实际 %v", ids(got))
	}
}

// TestPlacementCandidateCarriesFacts 候选携带 serverId/address/weight/capacity/drain 等事实供数据面落位。
func TestPlacementCandidateCarriesFacts(t *testing.T) {
	insts := []*runtime.Instance{inst("a", 100, 200, runtime.StatusOnline)}
	got := RankPlacement(insts, nil)
	if len(got) != 1 {
		t.Fatalf("应有 1 个候选，实际 %d", len(got))
	}
	c := got[0]
	if c.ServerID != "a" || c.Address != "a:25565" || c.Weight != 100 || c.Capacity != 200 || c.Drained {
		t.Fatalf("候选事实字段错误：%+v", c)
	}
}
