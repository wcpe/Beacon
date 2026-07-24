package service

import (
	"math"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
)

// 本文件是健康值模型的纯函数核（FR-147，见 v2-metrics-health-scheduling.md §4.4/§4.5）：
// 因子归一化 → 权重重归一综合分 → 等级阈值 → schedulable 全枚举判定。
// 全部无副作用：不碰 DB / 内存结构 / 时钟，供健康计算轮与单测穷举复用。

// 健康因子名常量（§3.2 factors json 的 factor 字段取值，与 contracts HealthFactor 对齐）。
const (
	healthFactorTPS      = "tps"
	healthFactorCPU      = "cpu"
	healthFactorCapacity = "capacity"
	healthFactorConn     = "conn"
	healthFactorLatency  = "latency"
	healthFactorAlert    = "alert"
)

// HealthWeights 是六因子权重段（json 键与 contracts HealthWeightsConfig.weights 逐字一致）。
type HealthWeights struct {
	TPS      float64 `json:"tps"`
	CPU      float64 `json:"cpu"`
	Capacity float64 `json:"capacity"`
	Conn     float64 `json:"conn"`
	Latency  float64 `json:"latency"`
	Alert    float64 `json:"alert"`
}

// HealthNormalize 是因子归一化参数段（§4.4 公式的 good/bad 边界与惩罚系数）。
type HealthNormalize struct {
	TPSGood       float64 `json:"tpsGood"`
	TPSBad        float64 `json:"tpsBad"`
	CPUGood       float64 `json:"cpuGood"`
	CPUBad        float64 `json:"cpuBad"`
	CapGood       float64 `json:"capGood"`
	CapBad        float64 `json:"capBad"`
	ConnSoftLimit float64 `json:"connSoftLimit"`
	LatGoodMs     float64 `json:"latGoodMs"`
	LatBadMs      float64 `json:"latBadMs"`
	AlertPenalty  float64 `json:"alertPenalty"`
}

// HealthLevelThresholds 是等级阈值段：score ≥ healthyMin → healthy；≥ degradedMin → degraded；否则 unhealthy。
type HealthLevelThresholds struct {
	HealthyMin  int `json:"healthyMin"`
	DegradedMin int `json:"degradedMin"`
}

// HealthWeightsConfig 是完整健康权重配置对象（§4.4，版本化进 health_weights_rev；
// json 形状与 contracts HealthWeightsConfig 逐字一致）。
type HealthWeightsConfig struct {
	Weights   HealthWeights         `json:"weights"`
	Normalize HealthNormalize       `json:"normalize"`
	Levels    HealthLevelThresholds `json:"levels"`
}

// DefaultHealthWeightsConfig 返回 §4.4 括号内全部默认值（种子 rev=1 用）。
func DefaultHealthWeightsConfig() HealthWeightsConfig {
	return HealthWeightsConfig{
		Weights: HealthWeights{TPS: 30, CPU: 20, Capacity: 20, Conn: 10, Latency: 10, Alert: 10},
		Normalize: HealthNormalize{
			TPSGood: 19.5, TPSBad: 10,
			CPUGood: 40, CPUBad: 90,
			CapGood: 0.6, CapBad: 0.95,
			ConnSoftLimit: 2000,
			LatGoodMs:     50, LatBadMs: 500,
			AlertPenalty: 25,
		},
		Levels: HealthLevelThresholds{HealthyMin: 80, DegradedMin: 50},
	}
}

// HealthFactorInputs 是一台实例经 60s 窗口聚合后的因子原始输入（§4.4 因子输入行）。
// 不可用哨兵约定：CPUPct<0 表示窗口内全部批次 CPU 均不可用（JVM 毛刺对称处理）→ cpu 因子不适用；
// RttMs<0 表示延迟不可得 → latency 因子不适用；MaxOnline≤0 无容量上限可依 → capacity 因子不适用。
type HealthFactorInputs struct {
	Kind string // proxy / backend（决定因子适用集）
	// CPUPct 进程 CPU%（窗口内有效批均值；<0 = 全不可用）
	CPUPct float64
	// TPS 仅 backend（窗口内批均值）
	TPS float64
	// OnlineCount / MaxOnline 仅 backend（窗口保守值 / 容量上限）
	OnlineCount int
	MaxOnline   int
	// ConnCount 仅 proxy（窗口保守值）
	ConnCount int
	// RttMs 延迟原始值：backend 取 reportRttMs、proxy 取 backendAvgRttMs；<0 = 不可得
	RttMs float64
	// ActiveAlerts 活跃告警数（P4 期恒 0，P5 由告警事件域供给）
	ActiveAlerts int
}

// ComputeHealthFactors 按 §4.4 把原始输入归一化为六因子明细（固定顺序 tps/cpu/capacity/conn/latency/alert）。
// 不适用因子仍占位输出（applicable=false、normalized=0），供前端因子分解完整展示。
func ComputeHealthFactors(in HealthFactorInputs, cfg HealthWeightsConfig) []healthview.Factor {
	backend := in.Kind == model.ServerKindBackend
	n := cfg.Normalize
	w := cfg.Weights
	capacityRatio, capacityOK := occupancyRatio(float64(in.OnlineCount), float64(in.MaxOnline))
	connRatio, connOK := occupancyRatio(float64(in.ConnCount), n.ConnSoftLimit)
	return []healthview.Factor{
		buildFactor(healthFactorTPS, in.TPS, w.TPS, backend,
			normalizeLinear(in.TPS-n.TPSBad, n.TPSGood-n.TPSBad)),
		buildFactor(healthFactorCPU, in.CPUPct, w.CPU, in.CPUPct >= 0,
			normalizeLinear(n.CPUBad-in.CPUPct, n.CPUBad-n.CPUGood)),
		buildFactor(healthFactorCapacity, capacityRatio, w.Capacity, backend && capacityOK,
			normalizeLinear(n.CapBad-capacityRatio, n.CapBad-n.CapGood)),
		buildFactor(healthFactorConn, float64(in.ConnCount), w.Conn, in.Kind == model.ServerKindProxy && connOK,
			normalizeLinear(n.CapBad-connRatio, n.CapBad-n.CapGood)),
		buildFactor(healthFactorLatency, in.RttMs, w.Latency, in.RttMs >= 0,
			normalizeLinear(n.LatBadMs-in.RttMs, n.LatBadMs-n.LatGoodMs)),
		buildFactor(healthFactorAlert, float64(in.ActiveAlerts), w.Alert, true,
			alertScore(in.ActiveAlerts, n.AlertPenalty)),
	}
}

// buildFactor 组装单因子明细：不适用因子归一化分强制 0（不计分，仅占位展示）。
func buildFactor(name string, raw, weight float64, applicable bool, normalized float64) healthview.Factor {
	if !applicable {
		normalized = 0
	}
	return healthview.Factor{Factor: name, Raw: raw, Normalized: normalized, Weight: weight, Applicable: applicable}
}

// normalizeLinear 是 §4.4 线性归一公式的公共形态：clamp(分子/分母, 0, 1) × 100。
// 分母 ≤0（配置未过校验的防御性兜底）时返回 0，不产生 NaN/Inf。
func normalizeLinear(numerator, denominator float64) float64 {
	if denominator <= 0 {
		return 0
	}
	return clamp01(numerator/denominator) * 100
}

// alertScore 告警因子：100 − activeAlerts × alertPenalty，下限 0、上限 100。
func alertScore(activeAlerts int, penalty float64) float64 {
	return clamp01((100-float64(activeAlerts)*penalty)/100) * 100
}

// occupancyRatio 计算占用率 r=used/limit；limit ≤0 时该占用类因子不适用（无除零）。
func occupancyRatio(used, limit float64) (float64, bool) {
	if limit <= 0 {
		return 0, false
	}
	return used / limit, true
}

// clamp01 把 x 收敛到 [0,1]。
func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// HealthScore 综合分（§4.4）：score = round(Σ(wᵢ×fᵢ) / Σwᵢ)，仅对适用因子求和（权重自动重归一）。
// 适用权重和 ≤0（无任何适用因子或权重全 0）时返回 0。
func HealthScore(factors []healthview.Factor) int {
	var weightSum, weighted float64
	for _, f := range factors {
		if !f.Applicable || f.Weight <= 0 {
			continue
		}
		weightSum += f.Weight
		weighted += f.Weight * f.Normalized
	}
	if weightSum <= 0 {
		return 0
	}
	return int(math.Round(weighted / weightSum))
}

// HealthLevelOf 按阈值定级：score ≥ healthyMin → healthy；≥ degradedMin → degraded；否则 unhealthy。
func HealthLevelOf(score int, levels HealthLevelThresholds) string {
	if score >= levels.HealthyMin {
		return healthview.LevelHealthy
	}
	if score >= levels.DegradedMin {
		return healthview.LevelDegraded
	}
	return healthview.LevelUnhealthy
}

// HealthScheduleFacts 是 schedulable 判定的事实输入（§4.5 全枚举的事实来源集合）。
type HealthScheduleFacts struct {
	Kind           string // server 实体 kind
	IdentityStatus string // agent_identity.status（pending 未确认 / disabled 禁用）
	Unassigned     bool   // backend 未分配到小区（v2 server.zone_id 为空）
	Draining       bool   // server.draining 排空中
	Lost           bool   // 超过 30s 无指标批（内存活性判定）
	Level          string // 健康等级（§4.4 计算结果）
}

// SchedulableReasons 按 §4.5 全枚举返回不可调度原因码（可叠加，按 spec 表序）；
// 空切片即 schedulable=true。degraded 不进排除表（仅决策排序劣势）。
func SchedulableReasons(f HealthScheduleFacts) []string {
	reasons := make([]string, 0, 4)
	if f.Kind != model.ServerKindBackend {
		reasons = append(reasons, healthview.ReasonKindNotSchedulable)
	}
	if f.IdentityStatus == model.AgentIdentityStatusPending {
		reasons = append(reasons, healthview.ReasonPendingConfirm)
	}
	// unassigned 只对 backend 有意义（proxy 按 bc_cluster 分配、不落 zone，且已被 kind 原因排除）。
	if f.Kind == model.ServerKindBackend && f.Unassigned {
		reasons = append(reasons, healthview.ReasonUnassigned)
	}
	if f.IdentityStatus == model.AgentIdentityStatusDisabled {
		reasons = append(reasons, healthview.ReasonDisabled)
	}
	if f.Draining {
		reasons = append(reasons, healthview.ReasonDraining)
	}
	if f.Lost {
		reasons = append(reasons, healthview.ReasonLost)
	}
	if f.Level == healthview.LevelUnhealthy {
		reasons = append(reasons, healthview.ReasonUnhealthy)
	}
	return reasons
}
