package metricwindow

import "testing"

func sampleAt(ns uint, server string, bucket int64) Sample {
	return Sample{NamespaceID: ns, ServerID: server, Kind: "backend", BucketStartMs: bucket, SampleCount: 5, TPSAvg: 20}
}

// TestUpsertNewVsDuplicate 校验新桶返回 true、同桶重放原地更新返回 false。
func TestUpsertNewVsDuplicate(t *testing.T) {
	w := New(DefaultCapacity)
	if !w.Upsert(sampleAt(1, "s1", 5000)) {
		t.Fatalf("首个桶应为新增")
	}
	// 同桶再报：非新增（去重信号），且值被更新。
	updated := sampleAt(1, "s1", 5000)
	updated.TPSAvg = 12.5
	if w.Upsert(updated) {
		t.Fatalf("同 bucket 重放应返回 false（非新增）")
	}
	got := w.List(1, "s1")
	if len(got) != 1 || got[0].TPSAvg != 12.5 {
		t.Fatalf("重放应原地更新为最新值，实际 %+v", got)
	}
}

// TestContainsReadOnly 校验 Contains 只读判存、不改状态。
func TestContainsReadOnly(t *testing.T) {
	w := New(DefaultCapacity)
	w.Upsert(sampleAt(1, "s1", 5000))
	if !w.Contains(1, "s1", 5000) {
		t.Fatalf("应含已写入的桶")
	}
	if w.Contains(1, "s1", 9999) {
		t.Fatalf("不应含未写入的桶")
	}
	// 不同 namespace 同 serverId 互不干扰。
	if w.Contains(2, "s1", 5000) {
		t.Fatalf("不同 namespace 的同 serverId 不应命中")
	}
}

// TestRingEviction 校验超容量淘汰最旧、按桶起点升序、只保留最近 capacity 个。
func TestRingEviction(t *testing.T) {
	capacity := 12
	w := New(capacity)
	// 乱序写入 20 个桶（步长 5000）。
	order := []int64{5, 1, 3, 2, 4, 6, 8, 7, 9, 10, 12, 11, 13, 15, 14, 16, 18, 17, 19, 20}
	for _, k := range order {
		w.Upsert(sampleAt(1, "s1", k*5000))
	}
	got := w.List(1, "s1")
	if len(got) != capacity {
		t.Fatalf("窗口应只保留 %d 个批，实际 %d", capacity, len(got))
	}
	// 应为最近 12 个（9..20 * 5000），升序。
	for i, s := range got {
		want := int64(9+i) * 5000
		if s.BucketStartMs != want {
			t.Fatalf("第 %d 个桶应为 %d，实际 %d（淘汰 / 排序不符）", i, want, s.BucketStartMs)
		}
	}
}

// TestListDeepCopy 校验 List 返回深拷贝，外部改动不影响内部。
func TestListDeepCopy(t *testing.T) {
	w := New(DefaultCapacity)
	w.Upsert(sampleAt(1, "s1", 5000))
	snap := w.List(1, "s1")
	snap[0].TPSAvg = -999
	again := w.List(1, "s1")
	if again[0].TPSAvg == -999 {
		t.Fatalf("List 应返回深拷贝，外部改动泄漏进了内部")
	}
}

// TestLatestAndServerCount 校验 Latest 取最新桶、ServerCount 统计有数据实例数。
func TestLatestAndServerCount(t *testing.T) {
	w := New(DefaultCapacity)
	if _, ok := w.Latest(1, "s1"); ok {
		t.Fatalf("空窗口 Latest 应为 false")
	}
	w.Upsert(sampleAt(1, "s1", 5000))
	w.Upsert(sampleAt(1, "s1", 10000))
	w.Upsert(sampleAt(1, "s2", 5000))
	latest, ok := w.Latest(1, "s1")
	if !ok || latest.BucketStartMs != 10000 {
		t.Fatalf("Latest 应为最新桶 10000，实际 ok=%v %+v", ok, latest)
	}
	if w.ServerCount() != 2 {
		t.Fatalf("应有 2 个实例有窗口数据，实际 %d", w.ServerCount())
	}
}
