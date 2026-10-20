//go:build e2e

// 节点自声明、健康视图、准入作用域的真机断言。
package apitest_e2e

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

// assertDeclaration 验证 SelfDeclaration 三态。
//
// APPLIED 的判定不能只看「回执说 APPLIED」，还要看 admin 实例视图**真的读到了新值**——
// 否则无法区分「控制面接受了但没落进注册表」与「真的生效了」。
//
// ⚠️ 自声明标签落的是控制面内存注册表的 Instance.Metadata，与 admin 的 server_tag 是两套
// 互不连通的标签系统。故这里只能用 admin 实例视图的 metadata 字段核实，不能用 server_tag 接口。
func assertDeclaration(t *testing.T, repoRoot, base, token string, guard *harness.GradleProc) {
	t.Helper()
	obsPath := filepath.Join(harness.BackendRunDir(repoRoot), "plugins", "BeaconE2E", "e2e-declaration.log")

	// APPLIED：必须有一条成功回执，且带回了目标容量。
	waitObservation(t, obsPath, 0, obsWait, guard, func(o observation) bool {
		return o.source == "DECLARE_APPLIED" && strings.Contains(o.detail, "capacity=120")
	}, "自声明容量 120 被接受并回带")

	// REJECTED：负容量必须被拒绝且带原因（不能静默当成功）。
	waitObservation(t, obsPath, 0, obsWait, guard, func(o observation) bool {
		return o.source == "DECLARE_REJECTED" && strings.Contains(o.detail, "rejectReason=") &&
			!strings.Contains(o.detail, "rejectReason=-")
	}, "负容量自声明被拒绝并带原因")

	// 控制面侧核实：admin 实例视图的 capacity 与 metadata 真的变成了声明值。
	assertDeclarationVisibleInAdmin(t, base, token, guard)

	assertNoErrorSource(t, readObservations(obsPath), "DECLARE_ERROR")
}

// assertDeclarationVisibleInAdmin 用 admin 实例视图核实声明真的落进了注册表。
//
// 轮询是因为声明与视图读取之间可能有短暂时序差；但**不能无限等**——
// 若控制面收了声明却没生效，这里必须失败而不是一直等到超时给出「看起来还行」的结论。
func assertDeclarationVisibleInAdmin(t *testing.T, base, token string, guard *harness.GradleProc) {
	t.Helper()
	var lastCapacity int
	var lastMeta string
	err := waitUntil(obsWait, guard, func(ctx context.Context) bool {
		inst := adminInstanceDetail(t, base, token, guard)
		lastCapacity = inst.Capacity
		lastMeta = inst.Metadata
		return inst.Capacity == 120 && strings.Contains(inst.Metadata, declLabelKey+"=")
	})
	if err != nil {
		t.Fatalf("admin 实例视图未反映自声明：期望 capacity=120 且 metadata 含 %s=…，实际 capacity=%d metadata=%s：%v",
			declLabelKey, lastCapacity, lastMeta, err)
	}
	t.Logf("PASS admin 实例视图确认自声明生效：capacity=%d metadata=%s", lastCapacity, lastMeta)
}

// instanceDetail 是 admin 实例视图里本测试关心的字段。
type instanceDetail struct {
	Capacity int    `json:"capacity"`
	Metadata string `json:"metadata"`
}

// adminInstanceDetail 取 admin 实例视图的容量与 metadata。
//
// metadata 在 JSON 里是对象，但本测试只用「是否包含某键」的弱断言，
// 故按 map 解析后拼成 k=v 串，避免为一次断言引一个强类型结构体。
func adminInstanceDetail(t *testing.T, base, token string, guard *harness.GradleProc) instanceDetail {
	t.Helper()
	var raw struct {
		Capacity int               `json:"capacity"`
		Metadata map[string]string `json:"metadata"`
	}
	path := "/admin/v1/instances/" + serverID + "?namespace=" + namespace
	if err := doAdminJSONAllow(base, "GET", path, token, nil, 200, &raw, guard); err != nil {
		return instanceDetail{}
	}
	parts := make([]string, 0, len(raw.Metadata))
	for k, v := range raw.Metadata {
		parts = append(parts, k+"="+v)
	}
	return instanceDetail{Capacity: raw.Capacity, Metadata: strings.Join(parts, ",")}
}

// assertHealthView 验证 selfHealth / healthOf 与 admin 健康详情同源一致。
//
// 比对 score/level/schedulable 三项。sampledAtMs 刻意不比对：agent 读的是候选快照
// （约 10s 刷新），admin 读实时内存，二者时刻必然不同，比对会稳定假失败。
//
// ## 为什么必须等到「健康已就绪」再比对
//
// agent 刚启动时控制面还没算过健康，快照里的健康是 `score=0 / UNHEALTHY / reasons=lost`
// 这类**启动早期事实**；此刻 admin 侧可能已算出 100/HEALTHY。直接拿第一条观测比对，
// 会得到「agent=0 admin=100」的假失败——两边的真实语义都是对的，只是采样时机不同。
//
// 故这里先等 agent 侧自身健康变为**可调度**（等价健康计算轮已吸收本实例），
// 再与 admin 比对。等不到才失败，且失败信息会明确指出是「始终未就绪」而非「值不符」。
func assertHealthView(t *testing.T, repoRoot, base, token string, guard *harness.GradleProc) {
	t.Helper()
	obsPath := filepath.Join(harness.BackendRunDir(repoRoot), "plugins", "BeaconE2E", "e2e-health-admission.log")

	// 等到 agent 侧自身健康变为可调度（说明健康计算轮已吸收本实例）。
	waitObservation(t, obsPath, 0, slowObsWait, guard, func(o observation) bool {
		return o.source == "HEALTH_SELF" && o.field3 == serverID &&
			extractKV(o.detail, "schedulable") == "true"
	}, "selfHealth() 已就绪（schedulable=true）")

	// healthOf(self) 也必须能取到自身。
	waitObservation(t, obsPath, 0, slowObsWait, guard, func(o observation) bool {
		return o.source == "HEALTH_OF_SELF" && o.detail != "null" && o.field3 == serverID
	}, "healthOf(自身) 取到自身健康")

	assertHealthMatchesAdmin(t, obsPath, base, token, guard)
	assertNoErrorSource(t, readObservations(obsPath), "HEALTH_ERROR")
}

// assertHealthMatchesAdmin 校验 agent 健康视图与 admin 侧**最终一致**（而非瞬时相等）。
//
// ## 为什么不能断言瞬时相等
//
// 健康判定是动态的，两侧采样时刻不同：
//   - agent 读的是**候选快照**（周期性刷新，约 10s 级），刷新间隙里读到的是上一次的判定；
//   - admin 读的是**实时内存**的健康计算轮结果。
//
// 实测同一实例在数秒内经历 HEALTHY(100) → DEGRADED(60) → HEALTHY(81)，两侧几乎必然读到
// 不同瞬间的档位。断言瞬时相等会稳定假失败，而真正的缺陷（健康视图没接上、字段错位）
// 反而被这种噪声掩盖。
//
// ## 断言策略
//
// 轮询等待 **agent 侧最终收敛到与 admin 一致**：只要 agent 快照会持续刷新，
// level 与 schedulable 迟早会追上控制面当前判定。等不到才失败——这才能区分
// 「只是慢了一拍」与「永远追不上（视图没接通）」。
//
// score 不参与比对：它是连续动态值，收敛也未必落在同一整数上；
// 只断言它在合法值域内，用于证明字段映射没串位。
func assertHealthMatchesAdmin(t *testing.T, obsPath, base, token string, guard *harness.GradleProc) {
	t.Helper()
	var lastAgentLevel, lastAdminLevel string
	var lastAgentScore, lastAdminScore int

	// 轮询期间持续重读观测文件与 admin 实时值，直到两者档位一致或超时。
	err := waitUntil(slowObsWait, guard, func(context.Context) bool {
		agent := latestReadyHealth(readObservations(obsPath))
		if agent == nil {
			return false
		}
		admin := adminHealthDetail(t, base, token, guard)
		lastAgentLevel = extractKV(agent.detail, "level")
		lastAgentScore = atoiSafe(agent.field4)
		lastAdminLevel = admin.Level
		lastAdminScore = admin.Score
		schedulableMatch := extractKV(agent.detail, "schedulable") == strconv.FormatBool(admin.Schedulable)
		return strings.EqualFold(lastAgentLevel, admin.Level) && schedulableMatch
	})
	if err != nil {
		t.Fatalf("健康视图在 %s 内始终未追平控制面判定：agent level=%s score=%d / admin level=%s score=%d；"+
			"两侧档位长期不一致说明健康视图未与控制面同源（而非只是刷新滞后）",
			slowObsWait, lastAgentLevel, lastAgentScore, lastAdminLevel, lastAdminScore)
	}

	// score 合法值域校验：越界说明字段映射错位（例如读到了容量或耗时）。
	if lastAgentScore < 0 || lastAgentScore > 100 {
		t.Fatalf("健康 score=%d 越界（合法值域 0..100）：字段映射可能有误", lastAgentScore)
	}
	t.Logf("PASS 健康视图与控制面最终一致：level=%s（agent score=%d / admin=%d，动态值不做精确比对）",
		lastAgentLevel, lastAgentScore, lastAdminScore)
}

// latestReadyHealth 取最新一条已就绪（schedulable=true）的自身健康观测。
//
// 倒序是必需的：早期行是启动瞬间的事实（reasons=lost），不能代表当前状态。
func latestReadyHealth(obs []observation) *observation {
	for i := len(obs) - 1; i >= 0; i-- {
		if obs[i].source == "HEALTH_SELF" && obs[i].detail != "null" &&
			extractKV(obs[i].detail, "schedulable") == "true" {
			return &obs[i]
		}
	}
	return nil
}

// healthDetail 是 admin 健康详情里本测试关心的字段。
type healthDetail struct {
	Score       int    `json:"score"`
	Level       string `json:"level"`
	Schedulable bool   `json:"schedulable"`
}

// adminHealthDetail 取 admin 健康详情作为期望值。
func adminHealthDetail(t *testing.T, base, token string, guard *harness.GradleProc) healthDetail {
	t.Helper()
	var out healthDetail
	if err := doAdminJSONAllow(base, "GET", "/admin/v2/health/"+serverID, token, nil, 200, &out, guard); err != nil {
		t.Fatalf("读取 admin 健康详情失败：%v", err)
	}
	return out
}

// assertAdmissionScope 验证 AdmissionScope 的三种形态。
//
// 三条断言缺一不可：
//   - 空 scope：等价老语义，应命中（证明收窄逻辑不会误伤「不按标签过滤」的调用）；
//   - 命中 scope：标签匹配时应 CHOSEN（证明收窄确实按声明的标签工作）；
//   - 全排除 scope：应 NO_CANDIDATE 且 failReason 指明在作用域内无候选，
//     且 admissionExcludedCount > 0（证明是「被作用域排除」而非「本来就没有候选」）。
func assertAdmissionScope(t *testing.T, repoRoot string, guard *harness.GradleProc) {
	t.Helper()
	obsPath := filepath.Join(harness.BackendRunDir(repoRoot), "plugins", "BeaconE2E", "e2e-health-admission.log")

	// 空 scope：应选中本服。
	waitObservation(t, obsPath, 0, slowObsWait, guard, func(o observation) bool {
		return o.source == "ADMIT_EMPTY_SCOPE" && strings.Contains(o.detail, "state=CHOSEN")
	}, "空 scope 应 CHOSEN")

	// 命中 scope：标签匹配应 CHOSEN。
	waitObservation(t, obsPath, 0, slowObsWait, guard, func(o observation) bool {
		return o.source == "ADMIT_HIT" && strings.Contains(o.detail, "state=CHOSEN")
	}, "标签命中的 scope 应 CHOSEN")

	// 全排除 scope：应 NO_CANDIDATE + no_candidate_in_scope + 排除数 > 0。
	idx := waitObservation(t, obsPath, 0, slowObsWait, guard, func(o observation) bool {
		return o.source == "ADMIT_NONE" && strings.Contains(o.detail, "no_candidate_in_scope")
	}, "全排除 scope 应报告作用域内无候选")
	assertAdmissionExcludedCount(t, readObservations(obsPath)[idx-1])

	assertNoErrorSource(t, readObservations(obsPath), "ADMIT_ERROR")
}

// assertAdmissionExcludedCount 断言被作用域排除的台数确实大于 0。
//
// 这条断言是「区分两种无候选」的关键：没有它，no_candidate 也可能只是「本来就没有候选」，
// 无法证明作用域收窄真的生效过。
func assertAdmissionExcludedCount(t *testing.T, line observation) {
	t.Helper()
	raw := extractKV(line.detail, "admissionExcludedCount")
	n := atoiSafe(raw)
	if n <= 0 {
		t.Fatalf("全排除 scope 的 admissionExcludedCount=%q 应 > 0：否则无法证明是「被作用域排除」而非「本来没有候选」（行=%s）",
			raw, line.detail)
	}
	t.Logf("PASS 全排除 scope 报告作用域内无候选，且被作用域排除 %d 台（证明收窄生效）", n)
}

// extractKV 从 detail 串里取 key=value 的值（分隔符 ; 或 ,）；缺失返回空串。
func extractKV(detail, key string) string {
	for _, sep := range []string{";", ","} {
		for _, kv := range strings.Split(detail, sep) {
			kv = strings.TrimSpace(kv)
			if strings.HasPrefix(kv, key+"=") {
				return strings.TrimPrefix(kv, key+"=")
			}
		}
	}
	return ""
}
