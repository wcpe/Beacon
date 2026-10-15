//go:build e2e && loadtest

// 规模压测「分位数计算」的纯函数单测。
//
// 为什么必须测这两个函数：它们算出的分位数**就是写进 PRD §5 的容量数字的来源**
// （`histogramQuantile` 把 /metrics 的 le 直方图转成分位值，`histogramDelta` 把进程累计
// 计数器转成档位增量）。这两个转换一旦有偏差，报告里的 p95/p99 会整体失真，
// 而失真方向未必是"偏大"——照抄进 PRD 就是错的容量口径。
//
// 本文件与本目录其它文件同 tag：它们只在显式 `-tags "e2e loadtest"` 时编译。
// 这些是纯计算函数，本可放进无 tag 文件让 CI 覆盖；放在此处是为了与被测函数同目录同 tag，
// 避免为「让 CI 跑到」而把 scale 相关代码拆散。**代价是 CI 不跑本文件**，
// 故本文件应随规模压测一起在本地执行（见文件头运行命令）。

package loadtest_e2e

import (
	"math"
	"testing"
)

// approx 比较浮点（分位插值是浮点运算，需要容差）。
func approx(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}

// TestHistogramQuantile 覆盖分位插值的关键形态。
//
// 最要守的两条（都是历史上真踩过的坑）：
//   - **第一个桶也要插值**：否则低负载下 p95 落在 ≤1ms 桶时一律读成 0，而报告里「0ms」
//     会被读成「量不出成本」，与实际不符。
//   - **+Inf 桶只能退回上一桶上界**（保守下界），绝不能外推编造数值。
func TestHistogramQuantile(t *testing.T) {
	// 典型 3 桶：10 个样本，≤10 有 5 个、≤20 有 8 个、≤30 有 10 个。
	typical := []bucketPoint{
		{Upper: 10, Cum: 5},
		{Upper: 20, Cum: 8},
		{Upper: 30, Cum: 10},
	}

	cases := []struct {
		name   string
		points []bucketPoint
		count  int64
		p      float64
		want   float64
	}{
		{"中位数落在第二桶", typical, 10, 0.5, 10},
		{"p90 落在第三桶", typical, 10, 0.9, 25},
		{"p95 落在第三桶", typical, 10, 0.95, 27.5},
		{"p100 取最大上界", typical, 10, 1.0, 30},
		{"p0 归零（rank=0 落在首桶起点）", typical, 10, 0, 0},
		// 第一个桶内插值：5 个样本落在 ≤1 桶，p10 → rank=1 → 0 + (1-0)/5*1 = 0.2。
		// 关键在「不为 0」：若不插值（更早的实现）会返回 0，报告读成「0ms = 量不出成本」。
		{
			"首个桶内插值（防低负载读成 0ms）",
			[]bucketPoint{{Upper: 1, Cum: 5}, {Upper: 10, Cum: 10}},
			10, 0.1, 0.2,
		},
		{
			"+Inf 桶退回上一桶上界（保守下界，不外推）",
			[]bucketPoint{{Upper: 1, Cum: 3}, {Upper: math.Inf(1), Cum: 10}},
			10, 0.9, 1,
		},
		{"零样本返回 0", typical, 0, 0.95, 0},
		{"空桶返回 0", nil, 10, 0.95, 0},
		{"单桶插值", []bucketPoint{{Upper: 4, Cum: 2}}, 2, 0.5, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := histogramQuantile(tc.points, tc.count, tc.p)
			if !approx(got, tc.want, 1e-9) {
				t.Fatalf("p%.2f 应为 %.4f，实际 %.4f", tc.p*100, tc.want, got)
			}
		})
	}
}

// TestHistogramQuantileMonotonic 分位数必须随 p 单调不减——违反即说明插值写错。
func TestHistogramQuantileMonotonic(t *testing.T) {
	points := []bucketPoint{{Upper: 1, Cum: 2}, {Upper: 5, Cum: 7}, {Upper: 10, Cum: 10}}
	prev := -1.0
	for _, p := range []float64{0, 0.1, 0.25, 0.5, 0.75, 0.9, 0.95, 0.99, 1} {
		got := histogramQuantile(points, 10, p)
		if got < prev {
			t.Fatalf("分位数应随 p 单调不减：p=%.2f 得 %.4f，小于上一个 %.4f", p, got, prev)
		}
		prev = got
	}
}

// TestHistogramDelta 覆盖「进程累计计数器 → 档位增量」的转换。
//
// 这是「分位数不被先跑的低并发档位拽住」的关键：/metrics 的直方图不清零，
// 若直接用累计值，并发 128 的 p99 显示的会是「整轮压测至今的 p99」，瓶颈定位整体失效。
func TestHistogramDelta(t *testing.T) {
	t.Run("正常做差", func(t *testing.T) {
		before := []bucketPoint{{Upper: 1, Cum: 100}, {Upper: 10, Cum: 150}}
		after := []bucketPoint{{Upper: 1, Cum: 130}, {Upper: 10, Cum: 220}}
		got := histogramDelta(before, after)
		want := []bucketPoint{{Upper: 1, Cum: 30}, {Upper: 10, Cum: 70}}
		if len(got) != len(want) {
			t.Fatalf("桶数应为 %d，实际 %d", len(want), len(got))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("第 %d 桶应为 %+v，实际 %+v", i, want[i], got[i])
			}
		}
	})

	t.Run("after 为空返回 nil", func(t *testing.T) {
		if got := histogramDelta([]bucketPoint{{Upper: 1, Cum: 5}}, nil); got != nil {
			t.Fatalf("after 为空应返回 nil，实际 %+v", got)
		}
	})

	t.Run("桶计数回退（进程重启）返回 nil", func(t *testing.T) {
		before := []bucketPoint{{Upper: 1, Cum: 100}}
		after := []bucketPoint{{Upper: 1, Cum: 3}} // 计数变小 = 期间重启过
		if got := histogramDelta(before, after); got != nil {
			t.Fatalf("计数回退应返回 nil（差值不可用），实际 %+v", got)
		}
	})

	t.Run("after 新增桶时按上界对齐", func(t *testing.T) {
		before := []bucketPoint{{Upper: 1, Cum: 10}}
		after := []bucketPoint{{Upper: 1, Cum: 15}, {Upper: 5, Cum: 18}}
		got := histogramDelta(before, after)
		if len(got) != 2 {
			t.Fatalf("应保留 2 个桶，实际 %d", len(got))
		}
		// 新增的 ≤5 桶在 before 里没有对应项，故差值为其自身累计值。
		if got[1].Upper != 5 || got[1].Cum != 18 {
			t.Fatalf("新增桶应为 {5 18}，实际 %+v", got[1])
		}
	})

	t.Run("before 为空时增量即 after", func(t *testing.T) {
		after := []bucketPoint{{Upper: 1, Cum: 7}}
		got := histogramDelta(nil, after)
		if len(got) != 1 || got[0] != after[0] {
			t.Fatalf("before 为空时增量应等于 after，实际 %+v", got)
		}
	})
}

// TestHistogramDeltaThenQuantile 是端到端：增量直方图算出的分位数应显著低于用累计值算出的，
// 这正是「必须做差」的量化理由。
func TestHistogramDeltaThenQuantile(t *testing.T) {
	// before：前一轮低并发已积累大量「快」样本。
	before := []bucketPoint{{Upper: 1, Cum: 10000}, {Upper: 100, Cum: 10000}}
	// after：本轮又加了 100 个「慢」样本。
	after := []bucketPoint{{Upper: 1, Cum: 10000}, {Upper: 100, Cum: 10100}}

	cumulative := histogramQuantile(after, 10100, 0.95)
	delta := histogramDelta(before, after)
	deltaQ := histogramQuantile(delta, 100, 0.95)

	if !(deltaQ > cumulative) {
		t.Fatalf("增量分位数（%.2f）应高于累计分位数（%.2f）——本轮样本比历史慢", deltaQ, cumulative)
	}
	if deltaQ <= 1 {
		t.Fatalf("增量 p95 应落在 ≤100 桶内（>1），实际 %.2f——做差未生效会让慢样本被历史快样本淹没", deltaQ)
	}
}
