package runtime

import (
	"reflect"
	"testing"
	"time"
)

// declaredFixture 注册一个字段齐全的实例，供声明刷新用例比对「只动 Capacity / Metadata」。
// 除声明两项外的字段全部填非零值，使越界改写能被直接观察到。
func declaredFixture(t *testing.T, reg *Registry, now time.Time, capacity int, metadata map[string]string) *Instance {
	t.Helper()
	inst := &Instance{
		Namespace: "prod", ServerID: "lobby-1", Role: "bukkit",
		GroupHint: "area1", ResolvedGroup: "area1", ResolvedZone: "zoneA", Assigned: true,
		Address: "10.0.0.1:25565", Version: "1.20.1", AgentVersion: "0.6.0",
		Capacity: capacity, Weight: 100, Metadata: metadata, Backends: []string{"b-1"},
		AppliedMD5: "md5-old", PlayerCount: 7, TPS: 19.8, MemUsed: 1024, MemMax: 4096, CPULoad: 0.35,
		Proxy: ProxyMetrics{OnlineConnections: 3, ThreadCount: 40, UptimeMs: 1000, BackendUp: 1, BackendTotal: 1},
	}
	if _, err := reg.Register(inst, 30*time.Second, now); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	return reg.Get("prod", "lobby-1")
}

// TestSetDeclarationOnlyCapacity 验证只给 capacity 时只刷新容量，既有标签不动（部分刷新语义）。
func TestSetDeclarationOnlyCapacity(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	declaredFixture(t, reg, now, 200, map[string]string{"mode": "survival"})

	capacity := 350
	if ok := reg.SetDeclaration("prod", "lobby-1", &capacity, nil); !ok {
		t.Fatal("已注册实例 SetDeclaration 应返回 true")
	}
	got := reg.Get("prod", "lobby-1")
	if got.Capacity != 350 {
		t.Fatalf("容量应刷新为 350，实际 %d", got.Capacity)
	}
	if !reflect.DeepEqual(got.Metadata, map[string]string{"mode": "survival"}) {
		t.Fatalf("只给 capacity 时标签不得变动，实际 %v", got.Metadata)
	}
}

// TestSetDeclarationOnlyLabels 验证只给 labels 时只整体替换标签，容量不动（部分刷新语义）。
func TestSetDeclarationOnlyLabels(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	declaredFixture(t, reg, now, 200, map[string]string{"mode": "survival"})

	if ok := reg.SetDeclaration("prod", "lobby-1", nil, map[string]string{"mode": "creative", "tier": "core"}); !ok {
		t.Fatal("已注册实例 SetDeclaration 应返回 true")
	}
	got := reg.Get("prod", "lobby-1")
	if got.Capacity != 200 {
		t.Fatalf("只给 labels 时容量不得变动，实际 %d", got.Capacity)
	}
	if !reflect.DeepEqual(got.Metadata, map[string]string{"mode": "creative", "tier": "core"}) {
		t.Fatalf("标签应整体替换为声明集，实际 %v", got.Metadata)
	}
}

// TestSetDeclarationLabelsReplaceNotMerge 验证 labels 是整体替换而非合并：未出现的旧 key 必须消失。
func TestSetDeclarationLabelsReplaceNotMerge(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	declaredFixture(t, reg, now, 200, map[string]string{"mode": "survival", "tier": "core"})

	reg.SetDeclaration("prod", "lobby-1", nil, map[string]string{"tier": "edge"})
	got := reg.Get("prod", "lobby-1")
	if !reflect.DeepEqual(got.Metadata, map[string]string{"tier": "edge"}) {
		t.Fatalf("标签应整体替换（旧 key mode 消失），实际 %v", got.Metadata)
	}
}

// TestSetDeclarationEmptyLabelsClears 验证显式空 map 清空全部标签（与「缺键不刷新」区分）。
func TestSetDeclarationEmptyLabelsClears(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	declaredFixture(t, reg, now, 200, map[string]string{"mode": "survival"})

	if ok := reg.SetDeclaration("prod", "lobby-1", nil, map[string]string{}); !ok {
		t.Fatal("已注册实例 SetDeclaration 应返回 true")
	}
	if got := reg.Get("prod", "lobby-1"); len(got.Metadata) != 0 {
		t.Fatalf("显式空 map 应清空全部标签，实际 %v", got.Metadata)
	}
}

// TestSetDeclarationZeroCapacityApplies 验证 capacity=0 是合法声明值并生效（不与「缺键」混淆）。
func TestSetDeclarationZeroCapacityApplies(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	declaredFixture(t, reg, now, 200, map[string]string{"mode": "survival"})

	zero := 0
	if ok := reg.SetDeclaration("prod", "lobby-1", &zero, nil); !ok {
		t.Fatal("已注册实例 SetDeclaration 应返回 true")
	}
	if got := reg.Get("prod", "lobby-1"); got.Capacity != 0 {
		t.Fatalf("capacity=0 应生效，实际 %d", got.Capacity)
	}
}

// TestSetDeclarationIdempotent 验证同一声明重复上报幂等：不新增条目、标签不叠加、结果与一次上报一致。
func TestSetDeclarationIdempotent(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	declaredFixture(t, reg, now, 200, map[string]string{"mode": "survival"})

	capacity := 500
	labels := map[string]string{"mode": "creative", "tier": "core"}
	for i := 0; i < 3; i++ {
		if ok := reg.SetDeclaration("prod", "lobby-1", &capacity, labels); !ok {
			t.Fatalf("第 %d 次声明应返回 true", i+1)
		}
	}
	got := reg.Get("prod", "lobby-1")
	if got.Capacity != 500 || !reflect.DeepEqual(got.Metadata, map[string]string{"mode": "creative", "tier": "core"}) {
		t.Fatalf("重复声明应幂等（结果与一次一致），实际 capacity=%d labels=%v", got.Capacity, got.Metadata)
	}
	if n := reg.CountByNamespace("prod"); n != 1 {
		t.Fatalf("声明刷新不得新增实例条目，实际 %d 条", n)
	}
}

// TestSetDeclarationUnregisteredReturnsFalse 验证未注册实例返回 false，且不凭空创建条目（对应 404 NOT_REGISTERED）。
func TestSetDeclarationUnregisteredReturnsFalse(t *testing.T) {
	reg := NewRegistry()
	capacity := 100
	if ok := reg.SetDeclaration("prod", "ghost", &capacity, map[string]string{"k": "v"}); ok {
		t.Fatal("未注册实例 SetDeclaration 应返回 false")
	}
	if n := reg.CountByNamespace("prod"); n != 0 {
		t.Fatalf("未注册声明不得创建条目，实际 %d 条", n)
	}
}

// TestSetDeclarationInputIsolated 验证写入前深拷贝：改外部入参不影响内存，读快照也不影响内存。
func TestSetDeclarationInputIsolated(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	declaredFixture(t, reg, now, 200, map[string]string{"mode": "survival"})

	labels := map[string]string{"mode": "creative"}
	reg.SetDeclaration("prod", "lobby-1", nil, labels)
	labels["mode"] = "tampered" // 改入参不得污染内存
	if got := reg.Get("prod", "lobby-1"); got.Metadata["mode"] != "creative" {
		t.Fatalf("内存标签应与入参隔离，实际 %v", got.Metadata)
	}
	snapshot := reg.Get("prod", "lobby-1")
	snapshot.Metadata["mode"] = "tampered" // 改快照同样不得污染内存
	if got := reg.Get("prod", "lobby-1"); got.Metadata["mode"] != "creative" {
		t.Fatalf("内存标签应与读快照隔离，实际 %v", got.Metadata)
	}
}

// TestSetDeclarationKeepsOtherFields 验证声明刷新只动 Capacity / Metadata：
// Status / Resolved* / LastHeartbeat / Backends / 指标字段（各有独立真源）逐项不变。
func TestSetDeclarationKeepsOtherFields(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	before := declaredFixture(t, reg, now, 200, map[string]string{"mode": "survival"})

	capacity := 999
	reg.SetDeclaration("prod", "lobby-1", &capacity, map[string]string{"mode": "creative"})
	after := reg.Get("prod", "lobby-1")

	// 比对除 Capacity / Metadata 外的全部字段（含 Backends 切片与 Proxy 结构）。
	want, got := *before, *after
	want.Capacity, got.Capacity = 0, 0
	want.Metadata, got.Metadata = nil, nil
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("声明刷新不得改动 Capacity / Metadata 之外的字段：\n前 %+v\n后 %+v", want, got)
	}
	if !before.LastHeartbeat.Equal(after.LastHeartbeat) {
		t.Fatalf("声明刷新不得动 LastHeartbeat：前 %v 后 %v", before.LastHeartbeat, after.LastHeartbeat)
	}
}
