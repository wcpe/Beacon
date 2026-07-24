package healthview

import "testing"

// sampleView 构造一条带切片字段的视图，供深拷贝 / 替换用例复用。
func sampleView(nsID uint, serverID string, score int) View {
	return View{
		NamespaceID: nsID,
		Namespace:   "prod",
		ServerID:    serverID,
		Kind:        "backend",
		ZoneName:    "zone-1",
		Score:       score,
		Level:       LevelHealthy,
		Schedulable: true,
		Reasons:     []string{},
		Factors: []Factor{
			{Factor: "tps", Raw: 19.8, Normalized: 98, Weight: 30, Applicable: true},
			{Factor: "cpu", Raw: 35, Normalized: 100, Weight: 20, Applicable: true},
		},
		WeightsRev:   1,
		OnlineCount:  10,
		MaxOnline:    100,
		ComputedAtMs: 1_000_000,
	}
}

// TestReplaceAllAtomic 校验整批原子替换：新批生效后旧批条目（含未再出现的实例）整体消失。
func TestReplaceAllAtomic(t *testing.T) {
	s := NewStore()
	s.ReplaceAll([]View{sampleView(1, "s-old", 90)})
	if s.Count() != 1 {
		t.Fatalf("首批应有 1 条视图，实际 %d", s.Count())
	}

	s.ReplaceAll([]View{sampleView(1, "s-a", 80), sampleView(2, "s-b", 70)})
	if s.Count() != 2 {
		t.Fatalf("替换后应有 2 条视图，实际 %d", s.Count())
	}
	if _, ok := s.Get(1, "s-old"); ok {
		t.Fatalf("旧批实例应随整批替换消失")
	}
	got, ok := s.Get(2, "s-b")
	if !ok || got.Score != 70 {
		t.Fatalf("新批实例应可查到且分数正确，实际 ok=%v score=%d", ok, got.Score)
	}
}

// TestDeepCopyIsolation 校验深拷贝隔离：改入参切片、改读出切片，均不影响 store 内视图。
func TestDeepCopyIsolation(t *testing.T) {
	src := sampleView(1, "s-1", 90)
	s := NewStore()
	s.ReplaceAll([]View{src})

	// 写入后篡改调用方持有的切片，store 不应被波及。
	src.Reasons = append(src.Reasons, ReasonDraining)
	src.Factors[0].Normalized = 0

	got, ok := s.Get(1, "s-1")
	if !ok {
		t.Fatalf("应能取到视图")
	}
	if len(got.Reasons) != 0 || got.Factors[0].Normalized != 98 {
		t.Fatalf("写入后篡改入参不应影响 store，实际 reasons=%v normalized=%v", got.Reasons, got.Factors[0].Normalized)
	}

	// 篡改读出的深拷贝，store 内值不变。
	got.Factors[1].Raw = 999
	got.Reasons = append(got.Reasons, ReasonLost)
	again, _ := s.Get(1, "s-1")
	if again.Factors[1].Raw != 35 || len(again.Reasons) != 0 {
		t.Fatalf("篡改读出副本不应影响 store，实际 raw=%v reasons=%v", again.Factors[1].Raw, again.Reasons)
	}

	// List 同样返回深拷贝。
	list := s.List()
	if len(list) != 1 {
		t.Fatalf("List 应返回 1 条，实际 %d", len(list))
	}
	list[0].Factors[0].Weight = -1
	final, _ := s.Get(1, "s-1")
	if final.Factors[0].Weight != 30 {
		t.Fatalf("篡改 List 副本不应影响 store，实际 weight=%v", final.Factors[0].Weight)
	}
}

// TestGetMiss 校验未命中返回零值与 false（空 store 与不同 namespace 同 serverId 均不命中）。
func TestGetMiss(t *testing.T) {
	s := NewStore()
	if _, ok := s.Get(1, "nobody"); ok {
		t.Fatalf("空 store 不应命中")
	}
	s.ReplaceAll([]View{sampleView(1, "s-1", 90)})
	if _, ok := s.Get(2, "s-1"); ok {
		t.Fatalf("不同 namespace 的同名 serverId 不应命中")
	}
	v, ok := s.Get(1, "missing")
	if ok || v.ServerID != "" {
		t.Fatalf("未命中应返回零值与 false，实际 ok=%v view=%+v", ok, v)
	}
}
