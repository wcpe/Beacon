package service

import "testing"

// 本文件是健康计算纯函数核（FR-147，见 health_calc.go 文件头）的微基准：不碰 DB / 内存结构 / 时钟，
// 只压「因子归一化 → 综合分 → 等级」这条健康计算轮每轮都要走一遍的热路径。
// 计时用 `for b.Loop()`：它首次调用时自动重置计时器（构造输入的耗时不计入测量），并保证循环体内
// 函数调用的入参与返回值存活、循环体不会被编译器整体消除，故无需包级 sink 变量与手工 ResetTimer。

// benchHealthCase 是一个基准输入档位：中文档位名 + 该档位的因子原始输入。
type benchHealthCase struct {
	名称 string
	输入 HealthFactorInputs
}

// benchHealthCases 返回两个基准共用的输入档位。基线直接复用 health_calc_test.go 的构造器
// （backendInputs / proxyInputs 及其微调函数），不另造第二份测试数据；另补一档哨兵场景
// （cpu 与 rtt 全不可用 → 两个因子被剔除、权重重归一），它覆盖适用性分支的另一条路径。
func benchHealthCases() []benchHealthCase {
	return []benchHealthCase{
		{"backend全因子适用", backendInputs()},
		{"proxy全因子适用", proxyInputs()},
		{"backend哨兵剔除cpu与latency", withRtt(withCPU(backendInputs(), -1), -1)},
	}
}

// BenchmarkComputeHealthFactors 压因子归一化内核 ComputeHealthFactors（纯函数：不碰 DB / 内存结构 /
// 时钟）。每个健康计算轮对全部实例各调一次，是健康链路里唯一的 O(因子数) 主项；按角色与哨兵档位分档，
// 观察因子适用集对成本的影响 —— 不适用因子仍占位输出（applicable=false、normalized=0），
// 差异只来自分支与归一化，故各档位应落在同一量级。
func BenchmarkComputeHealthFactors(b *testing.B) {
	cfg := DefaultHealthWeightsConfig()
	for _, c := range benchHealthCases() {
		b.Run(c.名称, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = ComputeHealthFactors(c.输入, cfg)
			}
		})
	}
}

// BenchmarkHealthPipeline 压健康计算的真实调用链：ComputeHealthFactors → HealthScore → HealthLevelOf。
//
// 必须串联测、而不是单独压 HealthScore（health_calc.go:165）的理由：HealthScore 只是对 6 个因子做
// 一次加权求和 + math.Round，属纳秒级函数 —— 单独 benchmark 它时，基准框架自身的计时开销与调度抖动
// 会占到同一量级，数字不可信（噪声占比过高）；而生产代码（健康计算轮）永远是「归一化 → 综合分 → 等级」
// 三者串行，串联后的耗时有稳定量级，才具备回归参考价值。HealthLevelOf 同为纳秒级比较，故一并并入链路。
func BenchmarkHealthPipeline(b *testing.B) {
	cfg := DefaultHealthWeightsConfig()
	for _, c := range benchHealthCases() {
		b.Run(c.名称, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				factors := ComputeHealthFactors(c.输入, cfg)
				_ = HealthLevelOf(HealthScore(factors), cfg.Levels)
			}
		})
	}
}
