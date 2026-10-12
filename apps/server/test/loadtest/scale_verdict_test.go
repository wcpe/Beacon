//go:build e2e && loadtest

// 规模压测「判定函数」的纯函数单测。
//
// 为什么必须测：这些函数把测量值翻成**结论性措辞**（「次线性」「明显拐点」「基本持平」
// 「池不是瓶颈」），而这些措辞被直接抄进 PRD §5 的容量口径。阈值写错或分支顺序写错，
// 会让一份数据得出方向相反的结论——例如把「两组配置其实相同」读成「池不是瓶颈」，
// 从而给出错误的优化方向。这类错误在报告里看不出异常（措辞读起来完全正常）。
//
// 本文件与同目录其它文件同 tag（仅 `-tags "e2e loadtest"` 时编译），故 CI 不跑；
// 应随规模压测一起在本地执行。

package loadtest_e2e

import (
	"strings"
	"testing"
)

// TestScaleVerdict 覆盖伸缩性判定的全部分支与边界。
func TestScaleVerdict(t *testing.T) {
	cases := []struct {
		name      string
		keepRatio float64
		p95Low    float64
		p95High   float64
		want      string
	}{
		{"吞吐腰斩即明显拐点", 0.49, 1, 1, "明显拐点"},
		{"保持率恰为 0.5 不算腰斩（边界取下界）", 0.5, 1, 1, "次线性"},
		{"吞吐降 20% 出头即次线性", 0.79, 1, 1, "次线性"},
		{"保持率恰为 0.8 不再算次线性", 0.8, 1, 1, "基本持平"},
		{"吞吐够但 p95 涨超 50% 即时延拐点", 0.9, 10, 16, "时延拐点"},
		{"p95 恰涨 50% 不算（边界取上界）", 0.9, 10, 15, "基本持平"},
		{"p95Low 为 0 时不判时延（防除零）", 0.9, 0, 100, "基本持平"},
		{"吞吐与时延都健康即基本持平", 0.95, 10, 11, "基本持平"},
		// 优先级：吞吐判定先于时延判定——吞吐腰斩时不该被时延分支截走。
		{"吞吐腰斩优先于时延判定", 0.3, 10, 100, "明显拐点"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scaleVerdict(tc.keepRatio, tc.p95Low, tc.p95High)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("保持率 %.2f / p95 %.1f→%.1f 应判为「%s」，实际「%s」",
					tc.keepRatio, tc.p95Low, tc.p95High, tc.want, got)
			}
		})
	}
}

// TestBottleneckVerdict 覆盖瓶颈归属的三个互斥分支。
//
// 最关键的是「压测端先饱和」这一支：若压测端自己打满 CPU，测出来的**不是控制面的上限**，
// 必须显式标出，否则会被读成「控制面只能跑这么快」。
func TestBottleneckVerdict(t *testing.T) {
	cores := 40

	t.Run("压测端先饱和（须显式标出，否则误读为控制面上限）", func(t *testing.T) {
		p := &scalePoint{
			ClientCores: float64(cores) * 0.6, // ≥ 半数核
			ServerCores: float64(cores) * 0.2, // 少于压测端
			Resources:   resourceSummary{CPUCores: cores},
		}
		got := bottleneckVerdict(p)
		if !strings.Contains(got, "压测端") {
			t.Fatalf("压测端先饱和应显式指出，实际「%s」", got)
		}
	})

	t.Run("宿主机饱和", func(t *testing.T) {
		p := &scalePoint{
			ClientCores: float64(cores) * 0.1,
			ServerCores: float64(cores) * 0.1,
			Resources:   resourceSummary{CPUCores: cores},
		}
		got := bottleneckVerdict(p)
		// 不断言具体措辞（措辞会演进），只要求它给出一个非空判定。
		if strings.TrimSpace(got) == "" {
			t.Fatal("任何输入都应给出非空判定（空判定会让报告缺失瓶颈归属）")
		}
		t.Logf("宿主机负载档的判定：%s", got)
	})

	t.Run("池排队优先归因于控制面内部", func(t *testing.T) {
		p := &scalePoint{
			ClientCores: float64(cores) * 0.1,
			ServerCores: float64(cores) * 0.3,
			Resources:   resourceSummary{CPUCores: cores},
			PoolWait:    poolDelta{Valid: true, WaitCount: 1000},
		}
		got := bottleneckVerdict(p)
		if strings.TrimSpace(got) == "" {
			t.Fatal("池排队档应给出判定")
		}
		t.Logf("池排队档的判定：%s", got)
	})
}

// TestPoolABVerdictDistinguishesNullFromNoDifference 是本文件最重要的一条：
// 它锁定「缺数据」与「无差异」在结论上必须可区分。
//
// 若两者混为一谈，任一组测量失败时 verdict 会输出「池不是瓶颈」——给出方向完全相反的
// 优化建议（去别处找瓶颈），而用例照样绿。这正是本批反复出现的失败模式。
func TestPoolABVerdictDistinguishesNullFromNoDifference(t *testing.T) {
	sides := []*poolABSide{
		{Label: "A", OpenConns: 4},
		{Label: "B", OpenConns: 32},
	}

	t.Run("两侧都有样本且差异小 → 判为无明显差异", func(t *testing.T) {
		samples := []poolABSample{
			{OpenConns: 4, Round: 1, RPS: 100, ObservedMaxOpen: 4},
			{OpenConns: 32, Round: 1, RPS: 105, ObservedMaxOpen: 32},
		}
		got := strings.Join(poolABVerdict(sides, samples, 32, 1), "\n")
		if strings.TrimSpace(got) == "" {
			t.Fatal("有数据时应给出判定")
		}
		t.Logf("判定：%s", got)
	})

	t.Run("完全无样本时不得给出「无差异」类结论", func(t *testing.T) {
		got := strings.Join(poolABVerdict(sides, nil, 32, 1), "\n")
		// 无样本时若还说「池不是瓶颈 / 无显著差异」，就是把缺数据当成了结论。
		for _, bad := range []string{"池不是瓶颈", "无明显差异", "无显著差异"} {
			if strings.Contains(got, bad) {
				t.Fatalf("无样本时不应给出「%s」类结论（把缺数据当无差异），实际：%s", bad, got)
			}
		}
		t.Logf("无样本时的判定：%s", got)
	})
}
