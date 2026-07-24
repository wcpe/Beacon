package bootwatch

import (
	"testing"
	"time"
)

const testWindow = 10 * time.Minute

// TestOneWaySwitchNoConflict 是本包最关键的回归：单向切换（故障换机 / 正常重启）绝不误判为冲突（spec §4.6）。
// A 注册 → B 顶替（新机新 boot）→ A 再无注册，仅有一两次拖尾上报 → 不应判冲突。
func TestOneWaySwitchNoConflict(t *testing.T) {
	r := New()
	base := time.Now().UTC()

	if out := r.OnRegister("id-1", "boot-A", "10.0.0.1", base, testWindow); out.ConflictDetected {
		t.Fatalf("首次注册不应判冲突")
	}
	// 故障换机：数据目录迁到新机，新进程新 bootId，新地址注册。
	if out := r.OnRegister("id-1", "boot-B", "10.0.0.2", base.Add(time.Second), testWindow); out.ConflictDetected {
		t.Fatalf("单向换手（A→B）不应判冲突")
	}
	// 旧机 A 已死，仅有一次在途拖尾上报到达（上报不改 current，不构成往复）。
	if out := r.OnReport("id-1", "boot-A", "10.0.0.1", base.Add(2*time.Second), testWindow); out.Evicted {
		t.Fatalf("拖尾上报不应被判落败")
	}
	// 再来一次拖尾上报，仍不应触发任何冲突（只有再注册才可能构成往复，而已死的 A 不会再注册）。
	_ = r.OnReport("id-1", "boot-A", "10.0.0.1", base.Add(3*time.Second), testWindow)
	// B 继续正常注册刷新（同 boot），不应判冲突。
	if out := r.OnRegister("id-1", "boot-B", "10.0.0.2", base.Add(4*time.Second), testWindow); out.ConflictDetected {
		t.Fatalf("current boot 刷新注册不应判冲突")
	}
}

// TestAlternatingBootConflict 验证并发双实例往复（A→B→A 再注册）判为冲突（spec §4.5 T12）。
func TestAlternatingBootConflict(t *testing.T) {
	r := New()
	base := time.Now().UTC()

	r.OnRegister("id-1", "boot-A", "10.0.0.1", base, testWindow)
	r.OnRegister("id-1", "boot-B", "10.0.0.2", base.Add(time.Second), testWindow)
	// 被顶替的 A 重新注册（真实系统里由数据面 401 促其重注册）→ 往复 → 冲突。
	out := r.OnRegister("id-1", "boot-A", "10.0.0.1", base.Add(2*time.Second), testWindow)
	if !out.ConflictDetected {
		t.Fatalf("A→B→A 往复应判冲突")
	}
	if len(out.Peers) < 2 {
		t.Fatalf("冲突应带 ≥2 个 peer，实际 %d", len(out.Peers))
	}
	// peers 含双方 bootId。
	boots := map[string]bool{}
	for _, p := range out.Peers {
		boots[p.BootID] = true
	}
	if !boots["boot-A"] || !boots["boot-B"] {
		t.Fatalf("peers 应含 boot-A 与 boot-B，实际 %+v", out.Peers)
	}
}

// TestSequentialRestartsNoConflict 验证连续多次重启（各产生全新 boot，前任已死）不误判（A→B→C）。
func TestSequentialRestartsNoConflict(t *testing.T) {
	r := New()
	base := time.Now().UTC()
	r.OnRegister("id-1", "boot-A", "10.0.0.1", base, testWindow)
	if out := r.OnRegister("id-1", "boot-B", "10.0.0.1", base.Add(time.Second), testWindow); out.ConflictDetected {
		t.Fatalf("第二次重启不应判冲突")
	}
	if out := r.OnRegister("id-1", "boot-C", "10.0.0.1", base.Add(2*time.Second), testWindow); out.ConflictDetected {
		t.Fatalf("第三次重启不应判冲突")
	}
}

// TestWindowExpiryResetsCurrent 验证超窗后旧 boot 记录清理、current 复位，旧 boot 再现不误判。
func TestWindowExpiryResetsCurrent(t *testing.T) {
	r := New()
	base := time.Now().UTC()
	r.OnRegister("id-1", "boot-A", "10.0.0.1", base, testWindow)
	r.OnRegister("id-1", "boot-B", "10.0.0.2", base.Add(time.Second), testWindow)
	// 超过一个窗口之后，A 的窗口内 wasCurrent 已被清理（现实中 UUID 不复现，此处仅验证清理逻辑不误判）。
	if out := r.OnRegister("id-1", "boot-A", "10.0.0.1", base.Add(testWindow+time.Minute), testWindow); out.ConflictDetected {
		t.Fatalf("超窗后旧 boot 记录应已清理，不应判冲突")
	}
}

// TestReportStaleAndEmptyBoot 验证陈旧 boot 上报判 Stale、空 boot 上报不判定（兼容旧行为）。
func TestReportStaleAndEmptyBoot(t *testing.T) {
	r := New()
	base := time.Now().UTC()
	r.OnRegister("id-1", "boot-A", "10.0.0.1", base, testWindow)
	// 与 current 一致：不 Stale。
	if out := r.OnReport("id-1", "boot-A", "10.0.0.1", base.Add(time.Second), testWindow); out.Stale {
		t.Fatalf("当前 boot 上报不应判陈旧")
	}
	// 与 current 不一致：Stale（促重注册）。
	if out := r.OnReport("id-1", "boot-B", "10.0.0.2", base.Add(2*time.Second), testWindow); !out.Stale {
		t.Fatalf("异于 current 的 boot 上报应判陈旧")
	}
	// 空 boot：兼容旧行为，不判定。
	if out := r.OnReport("id-1", "", "10.0.0.1", base.Add(3*time.Second), testWindow); out.Stale || out.Evicted {
		t.Fatalf("空 boot 上报不应判定")
	}
}

// TestResolveEvictsLoser 验证冲突处置后：落败方持续判落败、保留方正常、且保留方不被立刻复判冲突。
func TestResolveEvictsLoser(t *testing.T) {
	r := New()
	base := time.Now().UTC()
	r.OnRegister("id-1", "boot-A", "10.0.0.1", base, testWindow)
	r.OnRegister("id-1", "boot-B", "10.0.0.2", base.Add(time.Second), testWindow)
	if out := r.OnRegister("id-1", "boot-A", "10.0.0.1", base.Add(2*time.Second), testWindow); !out.ConflictDetected {
		t.Fatalf("前置：应先进入冲突")
	}
	// 处置：保留 A。
	r.Resolve("id-1", "boot-A", base.Add(3*time.Second), testWindow)
	// 落败方 B 继续上报 → 持续落败。
	if out := r.OnReport("id-1", "boot-B", "10.0.0.2", base.Add(4*time.Second), testWindow); !out.Evicted {
		t.Fatalf("落败方 B 上报应判落败")
	}
	// 落败方 B 试图以同 boot 重新注册 → 拒绝。
	if out := r.OnRegister("id-1", "boot-B", "10.0.0.2", base.Add(5*time.Second), testWindow); !out.Evicted {
		t.Fatalf("落败方 B 重新注册应被拒")
	}
	// 保留方 A 上报正常、不落败、不 Stale。
	if out := r.OnReport("id-1", "boot-A", "10.0.0.1", base.Add(6*time.Second), testWindow); out.Evicted || out.Stale {
		t.Fatalf("保留方 A 上报应正常")
	}
	// 保留方 A 刷新注册不应被复判冲突。
	if out := r.OnRegister("id-1", "boot-A", "10.0.0.1", base.Add(7*time.Second), testWindow); out.ConflictDetected {
		t.Fatalf("保留方刷新注册不应复判冲突")
	}
}

// TestReportSeedsCurrentAfterRestart 验证控制面重启后注册表为空时，首个上报播种 current、不误判。
func TestReportSeedsCurrentAfterRestart(t *testing.T) {
	r := New()
	base := time.Now().UTC()
	// 注册表为空，直接来上报（模拟控制面重启后 agent 仍在活跃上报）。
	if out := r.OnReport("id-1", "boot-A", "10.0.0.1", base, testWindow); out.Stale || out.Evicted {
		t.Fatalf("空注册表首个上报应播种 current、不判定")
	}
	// 同 boot 再上报：不 Stale。
	if out := r.OnReport("id-1", "boot-A", "10.0.0.1", base.Add(time.Second), testWindow); out.Stale {
		t.Fatalf("播种后同 boot 上报不应判陈旧")
	}
}
