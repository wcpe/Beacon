//go:build e2e

// FR-146/147「健康真值 + 调度决策」真机端到端测试，纯 Go 原生 go test -tags=e2e（默认 SQLite，无需 docker/MySQL）。
//
// 与 metricsv2 用例的本质区别：metricsv2 验证「采样 → 5s 批上报 → 日表入库」链路；本用例站在其上，
// 验证控制面把真实指标窗口算成健康真值（score / level / schedulable / factors，FR-147），
// 并在健康视图上完成调度决策闭环（candidates / decide / 决策落库可查 / 降级补报，FR-146）。
//
// 编排相位：
//
//	build     构建控制面二进制（SQLite），起临时控制面。
//	namespace 经 admin 建 v2 namespace，取一次性 accessToken（作 agent 的 X-Beacon-Token）。
//	agent     经 gradle :agent-e2e:servePaper 起真 Paper + 真 BeaconAgent（默认已开启 v2 指标采样）。
//	approve   真 agent v2 注册进 pending → e2e 经 admin approve 使其 active（上报要求 active，否则 403）。
//	health    等健康计算轮消化真实指标批后断管理面真值：/admin/v2/health 条目 score∈[0,100]、level 合法、
//	          未分配阶段 reasons 含 unassigned；详情 factors 非空、weightsRev≥1、cpu 因子合法
//	          （窗口全 -1 容忍 applicable=false，宿主哨兵）；/admin/v2/metrics/summary 实例计数 ≥1。
//	zone      建 bc 集群 / 大区 / 小区并首次分配该 server（zone 归属由控制面权威指派）→ 健康视图转 schedulable=true。
//	sched     以 Go HTTP 客户端模拟 agent 面（X-Beacon-Token + X-Beacon-Identity）：candidates 含该 zone
//	          与候选 → decide 选中该服 → 管理面按 traceId 查到 source=control_plane 决策（详情 / 列表 /
//	          summary）→ report-local 补报 1 条本地决策 → 详情 source=local_fallback。
//
// 铁律：只调既有 admin/agent API，绝不旁路或弱化任一 FR-146/147 约束来「让断言通过」。
package schedhealth_e2e

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

// 服务端编排相关常量（与 :agent-e2e:servePaper 的默认约定一致）。
const (
	adminUser = "admin"
	// v2 namespace 名（同时用作 v1 instances 查询的 namespace code；两者同值，与 metricsv2 一致）。
	namespace = "p4sched"
	// 与 servePaper 默认 serverId 对齐，并通过进程环境显式注入以免默认漂移。
	serverID = "e2e-bukkit-1"
	// MC 监听端口：避让 25565(p1v2)/25566(metrics)/25568(metricsv2)，本用例用 25569。
	mcPort = "25569"

	// 区服权威结构夹具（decide 以 zone 名为决策入参）。
	clusterName = "bc-sched"
	regionName  = "r-sched"
	zoneName    = "z-sched"

	logPrefixCP  = "beacon-schedhealth"
	logPrefixMC  = "paper-schedhealth"
	sqliteDBName = "beacon-e2e-schedhealth.db"

	// 首跑含 gradle 冷编译全 agent 模块 + 下载 Paper + agent 运行期依赖，给足 18 分钟到「pending 身份出现」。
	pendingWait = 18 * time.Minute
	// active 后衔接 legacy 数据面 online 仅数秒，给 6 分钟余量。
	onlineWait = 6 * time.Minute
	// online 后等健康计算轮消化真实指标批（agent 每 5s 批上报、计算轮每 5s 一轮），给 4 分钟余量。
	healthWait = 4 * time.Minute
	// 分配落区 / 候选快照等健康视图重算（每 5s 一轮），给 2 分钟余量。
	viewWait = 2 * time.Minute
	// 决策行异步入库（写入通道 500ms 攒批 flush），给 1 分钟余量。
	persistWait = 1 * time.Minute
)

// 控制面地址：默认 http://localhost:18850（与 metricsv2 同约定、单包独跑不冲突），可经 E2E_BEACON_URL 覆盖。
func beaconURL() string {
	if v := os.Getenv("E2E_BEACON_URL"); v != "" {
		return v
	}
	return "http://localhost:18850"
}

// TestSchedHealthE2E 按相位编排 FR-146/147 端到端：build → namespace → agent(真 Paper) → approve →
// 健康真值断言 → 建区分配 → 调度决策闭环断言。defer 收口杀全部进程。
func TestSchedHealthE2E(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")
	base := beaconURL()

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}

	// 控制面 SQLite 库文件（每轮删除从干净库开始）。
	sqliteDB := filepath.Join(repoRoot, ".tmp", sqliteDBName)
	if err := os.MkdirAll(filepath.Dir(sqliteDB), 0o755); err != nil {
		t.Fatalf("创建 .tmp 目录失败：%v", err)
	}
	_ = os.Remove(sqliteDB)

	t.Log("== 构建控制面二进制 ==")
	bin, err := harness.BuildBeacon(repoRoot)
	if err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}

	t.Log("== 起控制面（SQLite）==")
	cp, err := harness.StartControlPlane(harness.ControlPlaneConfig{
		BinPath: bin, RepoRoot: repoRoot, BaseURL: base,
		DBDriver: "sqlite", DBDSN: sqliteDB,
		AdminPassword: adminPass, AuthSecret: authSecret,
		// 传统共享令牌本用例不使用（agent 走 v2 namespace 令牌鉴权），置占位值。
		BootstrapToken: "legacy-token-unused-by-v2",
		LogPrefix:      logPrefixCP,
	})
	if err != nil {
		t.Fatalf("起控制面失败：%v", err)
	}
	harness.CleanupControlPlane(t, cp)

	adminToken, err := harness.Login(base, adminUser, adminPass)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}

	// 建 v2 namespace，取一次性 accessToken 作 agent 的 X-Beacon-Token。
	ns := createNamespace(t, base, adminToken)
	t.Logf("已建 v2 namespace id=%d", ns.ID)

	t.Log("== 起 Paper 子服 + 真 BeaconAgent（agent-e2e 壳，默认已开启 v2 指标采样）==")
	paperEnv := harness.AgentGradleEnv(base, ns.AccessToken, namespace, serverID, "127.0.0.1:"+mcPort)
	paper, err := harness.StartGradleTask(repoRoot, ":agent-e2e:servePaper", []string{
		"-Pe2eMcPort=" + mcPort,
	}, paperEnv, logPrefixMC)
	if err != nil {
		t.Fatalf("起 Paper 失败：%v", err)
	}
	harness.CleanupGradle(t, paper)

	t.Log("== 等真 agent v2 注册进 pending（首跑含下载/构建，耐心等）==")
	identityID, err := harness.WaitIdentityStatus(base, adminToken, ns.ID, serverID, "pending", pendingWait, paper)
	if err != nil {
		t.Fatalf("等待 pending 身份失败：%v", err)
	}
	t.Logf("观测到 pending 身份 identityId=%s（serverId=%s）", identityID, serverID)

	t.Log("== approve 使身份 active（上报 / 调度端点要求 active，否则 403）==")
	approveIdentity(t, base, adminToken, identityID, paper)
	if _, err := harness.WaitIdentityStatus(base, adminToken, ns.ID, serverID, "active", pendingWait, paper); err != nil {
		t.Fatalf("等待 active 身份失败：%v", err)
	}
	if err := harness.WaitInstanceOnline(base, adminToken, namespace, serverID, onlineWait, paper); err != nil {
		t.Fatalf("active 后应衔接 legacy 数据面 online（见 .tmp/%s.out.log）：%v", logPrefixMC, err)
	}
	t.Log("agent 已 active + online")

	t.Log("== 健康真值断言（FR-147）==")
	assertHealthTruth(t, base, adminToken, paper)

	t.Log("== 建区服结构并首次分配（backend 落区后才 schedulable，§4.5）==")
	setupZoneAndAssign(t, base, adminToken, ns.ID, paper)
	waitSchedulable(t, base, adminToken, paper)

	t.Log("== 调度决策闭环断言（FR-146）==")
	assertSchedulingChain(t, base, adminToken, ns.AccessToken, identityID, paper)
}

// ---- 健康真值相位（FR-147）----

// assertHealthTruth 等健康计算轮基于真实指标批产出健康真值后逐项断言管理面端点：
// 列表条目合法（score∈[0,100]、level 枚举、kind=backend）、未分配阶段 reasons 含 unassigned、
// 详情 factors 非空且 weightsRev≥1、cpu 因子合法（观察项记日志）、metrics/summary 实例计数 ≥1。
func assertHealthTruth(t *testing.T, base, token string, guard *harness.GradleProc) {
	t.Helper()
	// 等详情出现非空 factors：fresh 计算路径产物（lost 视图 factors 为空），即「指标批已被计算轮消化」。
	var detail healthDetailView
	err := waitUntil(healthWait, guard, func(ctx context.Context) bool {
		detail = healthDetailView{}
		if !tryReq(ctx, http.MethodGet, base+"/admin/v2/health/"+url.PathEscape(serverID), adminHeader(token), nil, &detail, guard) {
			return false
		}
		return len(detail.Factors) > 0
	})
	if err != nil {
		t.Fatalf("等待 %s 健康详情出现非空 factors 失败（%s）；最后视图 %+v：%v", serverID, healthWait, detail, err)
	}

	item := findHealthItem(t, base, token, guard)
	if item.Score < 0 || item.Score > 100 {
		t.Fatalf("健康条目 score=%d 应在 [0,100]", item.Score)
	}
	if !validLevel(item.Level) {
		t.Fatalf("健康条目 level=%q 非法（应为 healthy/degraded/unhealthy）", item.Level)
	}
	if item.Kind != model.ServerKindBackend {
		t.Fatalf("健康条目 kind=%q 应为 %q（bukkit → backend）", item.Kind, model.ServerKindBackend)
	}
	// 未分配阶段（尚未建区分配）：应不可调度且 reasons 含 unassigned（§4.5）。
	if item.Schedulable || !containsStr(item.Reasons, healthview.ReasonUnassigned) {
		t.Fatalf("未分配阶段应 schedulable=false 且 reasons 含 %q，实际 %+v", healthview.ReasonUnassigned, item)
	}
	if detail.WeightsRev < 1 {
		t.Fatalf("健康详情 weightsRev=%d 应 ≥1（种子 rev=1）", detail.WeightsRev)
	}
	assertCPUFactor(t, detail.Factors)
	assertMetricsSummary(t, base, token, guard)
	t.Logf("PASS health：score=%d level=%s schedulable=%v reasons=%v weightsRev=%d factors=%d 项",
		item.Score, item.Level, item.Schedulable, item.Reasons, detail.WeightsRev, len(detail.Factors))
}

// assertCPUFactor 校验 cpu 因子合法性并输出观察项：窗口全 -1（宿主 CPU 采集不可用哨兵）容忍
// applicable=false；applicable=true 时 raw 应为真实使用率 ≥0。「cpu 真值非 -1」仅记日志供人工复核。
func assertCPUFactor(t *testing.T, factors []healthFactorView) {
	t.Helper()
	for _, f := range factors {
		if f.Factor != "cpu" {
			continue
		}
		if f.Applicable && f.Raw < 0 {
			t.Fatalf("cpu 因子 applicable=true 时 raw=%.4f 应 ≥0", f.Raw)
		}
		if f.Applicable {
			t.Logf("观察：cpu 因子为真值 raw=%.4f%%（normalized=%.2f）", f.Raw, f.Normalized)
		} else {
			t.Logf("观察：cpu 因子 applicable=false（窗口内全为不可用哨兵 -1，宿主 CPU 采集不可用，agent 已正确归一）")
		}
		return
	}
	t.Fatalf("健康详情 factors 缺 cpu 因子：%+v", factors)
}

// assertMetricsSummary 断集群聚合概览的 backend 实例计数 ≥1 且在线 ≥1（该 agent 指标批持续在报）。
func assertMetricsSummary(t *testing.T, base, token string, guard harness.ProcessGuard) {
	t.Helper()
	var sum struct {
		ByKind struct {
			Backend struct {
				Total  int `json:"total"`
				Online int `json:"online"`
			} `json:"backend"`
		} `json:"byKind"`
	}
	doAdminJSON(t, http.MethodGet, base+"/admin/v2/metrics/summary", token, nil, http.StatusOK, &sum, guard)
	if sum.ByKind.Backend.Total < 1 || sum.ByKind.Backend.Online < 1 {
		t.Fatalf("metrics/summary backend 计数应 total≥1 且 online≥1，实际 %+v", sum.ByKind.Backend)
	}
}

// findHealthItem 从健康列表端点取目标 serverId 条目（缺失即 fatal）。
func findHealthItem(t *testing.T, base, token string, guard harness.ProcessGuard) healthItemView {
	t.Helper()
	var resp struct {
		Items []healthItemView `json:"items"`
		Total int              `json:"total"`
	}
	doAdminJSON(t, http.MethodGet, base+"/admin/v2/health", token, nil, http.StatusOK, &resp, guard)
	for _, it := range resp.Items {
		if it.ServerID == serverID {
			return it
		}
	}
	t.Fatalf("/admin/v2/health（%d 条）应含 %s 条目", resp.Total, serverID)
	return healthItemView{}
}

// validLevel 判断健康等级是否为合法枚举。
func validLevel(level string) bool {
	return level == healthview.LevelHealthy || level == healthview.LevelDegraded || level == healthview.LevelUnhealthy
}

// ---- 建区分配相位 ----

// setupZoneAndAssign 建 bc 集群 → 大区 → 小区，并把该 server 首次分配到小区（zone 归属由控制面权威指派）。
func setupZoneAndAssign(t *testing.T, base, token string, nsID uint, guard harness.ProcessGuard) {
	t.Helper()
	clusterID := createNode(t, base, token, "/bc-clusters", map[string]any{"namespaceId": nsID, "name": clusterName}, guard)
	regionID := createNode(t, base, token, "/regions", map[string]any{"bcClusterId": clusterID, "name": regionName}, guard)
	zoneID := createNode(t, base, token, "/zones", map[string]any{"regionId": regionID, "name": zoneName}, guard)
	rowID := serverRowID(t, base, token, nsID, guard)
	doAdminJSON(t, http.MethodPost, base+"/admin/v2/server-assignments", token, map[string]any{
		"serverIds": []uint{rowID}, "target": map[string]any{"kind": "zone", "id": zoneID},
		"isDefaultEntry": false, "reason": "e2e 首次分配",
	}, http.StatusOK, nil, guard)
	t.Logf("已把 %s 分配到 zone=%s（id=%d）", serverID, zoneName, zoneID)
}

// waitSchedulable 等健康计算轮吸收分配事实：详情 zoneName=目标小区 且 schedulable=true。
func waitSchedulable(t *testing.T, base, token string, guard *harness.GradleProc) {
	t.Helper()
	var detail healthDetailView
	err := waitUntil(viewWait, guard, func(ctx context.Context) bool {
		detail = healthDetailView{}
		if !tryReq(ctx, http.MethodGet, base+"/admin/v2/health/"+url.PathEscape(serverID), adminHeader(token), nil, &detail, guard) {
			return false
		}
		return detail.ZoneName != nil && *detail.ZoneName == zoneName && detail.Schedulable
	})
	if err != nil {
		t.Fatalf("等待 %s 分配后转 schedulable=true 失败（%s）；最后视图 %+v：%v", serverID, viewWait, detail, err)
	}
	t.Logf("分配已生效：zone=%s schedulable=true score=%d level=%s", zoneName, detail.Score, detail.Level)
}

// serverRowID 取该 namespace 下目标 serverId 的 server 行数字 id（分配端点以行 id 定位）。
func serverRowID(t *testing.T, base, token string, nsID uint, guard harness.ProcessGuard) uint {
	t.Helper()
	var out struct {
		Items []struct {
			ID       uint   `json:"id"`
			ServerID string `json:"serverId"`
		} `json:"items"`
	}
	doAdminJSON(t, http.MethodGet, base+"/admin/v2/servers?namespaceId="+utoa(nsID), token, nil, http.StatusOK, &out, guard)
	for _, it := range out.Items {
		if it.ServerID == serverID {
			return it.ID
		}
	}
	t.Fatalf("未找到 server %s 的行记录：%+v", serverID, out.Items)
	return 0
}

// ---- 调度决策闭环相位（FR-146）----

// assertSchedulingChain 以 Go HTTP 客户端模拟 agent 面完成调度闭环：candidates → decide →
// 管理面按 traceId 查决策（详情 / 列表 / summary）→ report-local 补报降级决策再查详情。
func assertSchedulingChain(t *testing.T, base, adminToken, nsToken, identityID string, guard *harness.GradleProc) {
	t.Helper()
	fromMs := time.Now().UTC().Add(-time.Hour).UnixMilli()

	waitCandidates(t, base, nsToken, identityID, guard)
	traceID := decideAndAssert(t, base, nsToken, identityID, guard)

	detail := waitDecisionDetail(t, base, adminToken, traceID, guard)
	if detail.Source != model.SchedSourceControlPlane {
		t.Fatalf("决策 %s source=%q 应为 %q", traceID, detail.Source, model.SchedSourceControlPlane)
	}
	if detail.ChosenServerID == nil || *detail.ChosenServerID != serverID {
		t.Fatalf("决策 %s chosenServerId 应为 %s，实际 %+v", traceID, serverID, detail.ChosenServerID)
	}
	if detail.ZoneName != zoneName || detail.Strategy != model.SchedStrategyHighestScore {
		t.Fatalf("决策 %s zone/strategy 应为 %s/%s，实际 %+v", traceID, zoneName, model.SchedStrategyHighestScore, detail)
	}
	if detail.RequesterServerID != serverID {
		t.Fatalf("决策 %s requesterServerId 应为权威身份 %s，实际 %q", traceID, serverID, detail.RequesterServerID)
	}
	assertDecisionList(t, base, adminToken, traceID, fromMs, guard)
	assertDecisionSummary(t, base, adminToken, guard)

	localTrace := reportLocalAndAssert(t, base, nsToken, identityID, guard)
	localDetail := waitDecisionDetail(t, base, adminToken, localTrace, guard)
	if localDetail.Source != model.SchedSourceLocalFallback {
		t.Fatalf("补报决策 %s source=%q 应为 %q", localTrace, localDetail.Source, model.SchedSourceLocalFallback)
	}
	if localDetail.ChosenServerID == nil || *localDetail.ChosenServerID != serverID {
		t.Fatalf("补报决策 %s chosenServerId 应为 %s，实际 %+v", localTrace, serverID, localDetail.ChosenServerID)
	}
	t.Logf("PASS sched：decide traceId=%s 选中 %s 已入库 source=control_plane；补报 localTraceId=%s 入库 source=local_fallback",
		traceID, serverID, localTrace)
}

// waitCandidates 轮询 agent 面候选快照，直到目标 zone 出现且候选含该 server；再断快照时间戳合法。
func waitCandidates(t *testing.T, base, nsToken, identityID string, guard *harness.GradleProc) {
	t.Helper()
	var resp candidatesView
	err := waitUntil(viewWait, guard, func(ctx context.Context) bool {
		resp = candidatesView{}
		if !tryReq(ctx, http.MethodGet, base+"/beacon/v2/agent/schedule/candidates", agentHeader(nsToken, identityID), nil, &resp, guard) {
			return false
		}
		return candidateHit(resp)
	})
	if err != nil {
		t.Fatalf("等待候选快照出现 zone=%s 且含 %s 失败（%s）；最后响应 %+v：%v", zoneName, serverID, viewWait, resp, err)
	}
	if resp.GeneratedAtMs <= 0 {
		t.Fatalf("候选快照 generatedAtMs=%d 应为正", resp.GeneratedAtMs)
	}
}

// candidateHit 判断候选快照是否已含目标 zone 与目标 server。
func candidateHit(resp candidatesView) bool {
	for _, z := range resp.Zones {
		if z.Zone != zoneName {
			continue
		}
		for _, c := range z.Candidates {
			if c.ServerID == serverID {
				return true
			}
		}
	}
	return false
}

// decideAndAssert 发一次 decide 并断言选中该服（唯一候选），返回 traceId。
func decideAndAssert(t *testing.T, base, nsToken, identityID string, guard harness.ProcessGuard) string {
	t.Helper()
	var resp decideView
	doAgentJSON(t, http.MethodPost, base+"/beacon/v2/agent/schedule/decide", nsToken, identityID,
		map[string]any{"zone": zoneName}, http.StatusOK, &resp, guard)
	if resp.TraceID == "" {
		t.Fatalf("decide 响应 traceId 应非空：%+v", resp)
	}
	if resp.FailReason != nil {
		t.Fatalf("decide 应成功，实际 failReason=%s", *resp.FailReason)
	}
	if resp.Chosen == nil || resp.Chosen.ServerID != serverID {
		t.Fatalf("decide 应选中 %s，实际 chosen=%+v", serverID, resp.Chosen)
	}
	if resp.CandidateCount < 1 {
		t.Fatalf("decide candidateCount=%d 应 ≥1", resp.CandidateCount)
	}
	t.Logf("decide 成功：traceId=%s chosen=%s score=%d", resp.TraceID, resp.Chosen.ServerID, resp.Chosen.Score)
	return resp.TraceID
}

// waitDecisionDetail 轮询管理面决策详情直到该 traceId 入库（异步写入通道 500ms 攒批 flush）。
func waitDecisionDetail(t *testing.T, base, token, traceID string, guard *harness.GradleProc) decisionDetailView {
	t.Helper()
	var detail decisionDetailView
	err := waitUntil(persistWait, guard, func(ctx context.Context) bool {
		detail = decisionDetailView{}
		return tryReq(ctx, http.MethodGet, base+"/admin/v2/sched-decisions/"+url.PathEscape(traceID),
			adminHeader(token), nil, &detail, guard)
	})
	if err != nil {
		t.Fatalf("等待决策 %s 异步入库失败（%s）：%v", traceID, persistWait, err)
	}
	return detail
}

// assertDecisionList 断列表查询（from/to 必填毫秒时间戳）含该 traceId。
func assertDecisionList(t *testing.T, base, token, traceID string, fromMs int64, guard harness.ProcessGuard) {
	t.Helper()
	toMs := time.Now().UTC().Add(time.Minute).UnixMilli()
	var resp struct {
		Items []decisionDetailView `json:"items"`
		Total int64                `json:"total"`
	}
	u := fmt.Sprintf("%s/admin/v2/sched-decisions?from=%d&to=%d", base, fromMs, toMs)
	doAdminJSON(t, http.MethodGet, u, token, nil, http.StatusOK, &resp, guard)
	for _, it := range resp.Items {
		if it.TraceID == traceID {
			return
		}
	}
	t.Fatalf("决策列表（%d 条 / total=%d）应含 traceId=%s", len(resp.Items), resp.Total, traceID)
}

// assertDecisionSummary 断概览聚合 total ≥1（1h 窗口内刚产生过决策）。
func assertDecisionSummary(t *testing.T, base, token string, guard harness.ProcessGuard) {
	t.Helper()
	var sum struct {
		Window string `json:"window"`
		Total  int64  `json:"total"`
	}
	doAdminJSON(t, http.MethodGet, base+"/admin/v2/sched-decisions/summary?window=1h", token, nil, http.StatusOK, &sum, guard)
	if sum.Total < 1 {
		t.Fatalf("决策概览（window=%s）total=%d 应 ≥1", sum.Window, sum.Total)
	}
}

// reportLocalAndAssert 补报 1 条降级期本地决策（localTraceId 自造 UUID）并断 202 accepted=1，返回 localTraceId。
func reportLocalAndAssert(t *testing.T, base, nsToken, identityID string, guard harness.ProcessGuard) string {
	t.Helper()
	localTrace := newUUIDv4()
	var resp struct {
		Accepted     int `json:"accepted"`
		Deduplicated int `json:"deduplicated"`
	}
	doAgentJSON(t, http.MethodPost, base+"/beacon/v2/agent/schedule/report-local", nsToken, identityID,
		map[string]any{"decisions": []map[string]any{{
			"localTraceId": localTrace, "tsMs": time.Now().UTC().UnixMilli(),
			"zone": zoneName, "plugin": "e2e", "purpose": "降级补报演练",
			"candidateCount": 1, "excluded": []any{},
			"chosenServerId": serverID, "failReason": "",
		}}}, http.StatusAccepted, &resp, guard)
	if resp.Accepted != 1 || resp.Deduplicated != 0 {
		t.Fatalf("补报应 accepted=1 / deduplicated=0，实际 %+v", resp)
	}
	return localTrace
}

// ---- admin 编排助手 ----

// namespaceView 是建 namespace 响应中本用例关心的字段（ID + 一次性明文 accessToken）。
type namespaceView struct {
	ID          uint   `json:"id"`
	AccessToken string `json:"accessToken"`
}

// identityView 是 agent 身份列表项中本用例关心的字段。
type identityView struct {
	IdentityID string `json:"identityId"`
	ServerID   string `json:"serverId"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
}

// createNamespace 经 admin 建 v2 namespace 并取一次性明文 accessToken。
func createNamespace(t *testing.T, base, token string) namespaceView {
	t.Helper()
	id, accessToken, err := harness.CreateV2Namespace(base, token, namespace, "健康值与调度决策 e2e")
	if err != nil {
		t.Fatalf("建 namespace 失败：%v", err)
	}
	return namespaceView{ID: id, AccessToken: accessToken}
}

// createNode 建区服权威节点（bc-cluster / region / zone），返回其数字 id。
func createNode(t *testing.T, base, token, path string, body map[string]any, guard harness.ProcessGuard) uint {
	t.Helper()
	var out struct {
		ID uint `json:"id"`
	}
	doAdminJSON(t, http.MethodPost, base+"/admin/v2"+path, token, body, http.StatusCreated, &out, guard)
	if out.ID == 0 {
		t.Fatalf("建 %s 应返回数字 id，实际 %+v", path, out)
	}
	return out.ID
}

// approveIdentity 首次确认身份（无 target：确认但暂不分配区服），断响应状态 active。
func approveIdentity(t *testing.T, base, token, identityID string, guard *harness.GradleProc) {
	t.Helper()
	if err := harness.ApproveIdentityWithGuard(base, token, identityID, guard); err != nil {
		t.Fatalf("批准 identity 失败：%v", err)
	}
}

// ---- 响应视图类型 ----

// healthItemView 是 /admin/v2/health 列表项中本用例关心的字段。
type healthItemView struct {
	ServerID    string   `json:"serverId"`
	Kind        string   `json:"kind"`
	ZoneName    *string  `json:"zoneName"`
	Score       int      `json:"score"`
	Level       string   `json:"level"`
	Schedulable bool     `json:"schedulable"`
	Reasons     []string `json:"reasons"`
}

// healthFactorView 是健康详情中的单因子分解项。
type healthFactorView struct {
	Factor     string  `json:"factor"`
	Raw        float64 `json:"raw"`
	Normalized float64 `json:"normalized"`
	Weight     float64 `json:"weight"`
	Applicable bool    `json:"applicable"`
}

// healthDetailView 是 /admin/v2/health/{serverId} 详情（列表项 + 因子分解 + 权重版本）。
type healthDetailView struct {
	healthItemView
	Factors    []healthFactorView `json:"factors"`
	WeightsRev int                `json:"weightsRev"`
}

// candidatesView 对齐 §5.1 candidates 响应中本用例关心的字段。
type candidatesView struct {
	GeneratedAtMs int64 `json:"generatedAtMs"`
	Zones         []struct {
		Zone       string `json:"zone"`
		Candidates []struct {
			ServerID string `json:"serverId"`
			Score    int    `json:"score"`
			Level    string `json:"level"`
		} `json:"candidates"`
	} `json:"zones"`
}

// decideView 对齐 §5.1 decide 的 200 响应。
type decideView struct {
	TraceID string `json:"traceId"`
	Chosen  *struct {
		ServerID string `json:"serverId"`
		Score    int    `json:"score"`
	} `json:"chosen"`
	CandidateCount int     `json:"candidateCount"`
	ExcludedCount  int     `json:"excludedCount"`
	FailReason     *string `json:"failReason"`
}

// decisionDetailView 是管理面决策详情 / 列表项中本用例关心的字段。
type decisionDetailView struct {
	TraceID           string  `json:"traceId"`
	RequesterServerID string  `json:"requesterServerId"`
	ZoneName          string  `json:"zoneName"`
	Strategy          string  `json:"strategy"`
	Source            string  `json:"source"`
	ChosenServerID    *string `json:"chosenServerId"`
	FailReason        *string `json:"failReason"`
}

// ---- HTTP 与小工具 ----

// doAdminJSON 发一个带管理面 Bearer 的请求，校验期望状态码并解析响应体。失败即 fatal。
func doAdminJSON(
	t *testing.T,
	method, u, token string,
	body any,
	wantStatus int,
	out any,
	guard harness.ProcessGuard,
) {
	t.Helper()
	doReq(t, method, u, adminHeader(token), body, wantStatus, out, guard)
}

// doAgentJSON 发一个带 agent 面鉴权头（X-Beacon-Token + X-Beacon-Identity）的请求，
// 校验期望状态码并解析响应体。失败即 fatal。
func doAgentJSON(
	t *testing.T,
	method, u, nsToken, identityID string,
	body any,
	wantStatus int,
	out any,
	guard harness.ProcessGuard,
) {
	t.Helper()
	doReq(t, method, u, agentHeader(nsToken, identityID), body, wantStatus, out, guard)
}

// doReq 发一个 JSON 请求：设置给定请求头、校验期望状态码，并（若 out 非 nil）解析响应体。失败即 fatal。
func doReq(
	t *testing.T,
	method, u string,
	headers map[string]string,
	body any,
	wantStatus int,
	out any,
	guard harness.ProcessGuard,
) {
	t.Helper()
	status, raw, err := rawReq(context.Background(), method, u, headers, body, guard)
	if err != nil {
		t.Fatalf("%s %s 请求失败：%v", method, u, err)
	}
	if status != wantStatus {
		t.Fatalf("%s %s 期望 HTTP %d，得 %d", method, u, wantStatus, status)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("解析 %s 响应失败：%v", u, err)
		}
	}
}

// tryReq 轮询版请求：仅在 200 且能解析时返回 true，任何失败静默返回 false（供 waitUntil 重试）。
func tryReq(
	ctx context.Context,
	method, u string,
	headers map[string]string,
	body, out any,
	guard harness.ProcessGuard,
) bool {
	status, raw, err := rawReq(ctx, method, u, headers, body, guard)
	if err != nil || status != http.StatusOK {
		return false
	}
	return json.Unmarshal(raw, out) == nil
}

// rawReq 构造并发送一次带 deadline 与进程 guard 的 JSON 请求，返回状态码与已读完的响应体。
func rawReq(
	ctx context.Context,
	method, u string,
	headers map[string]string,
	body any,
	guard harness.ProcessGuard,
) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("编码请求体失败：%w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return 0, nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := harness.DoRequestWithGuard(req, 0, guard)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("读取响应失败：%w", err)
	}
	return resp.StatusCode, raw, nil
}

// adminHeader 组装管理面 Bearer 头。
func adminHeader(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// agentHeader 组装 agent 面鉴权头（token↔namespace + identity 绑定，spec §5.1）。
func agentHeader(nsToken, identityID string) map[string]string {
	return map[string]string{"X-Beacon-Token": nsToken, "X-Beacon-Identity": identityID}
}

// waitUntil 在超时内每秒重试条件，并在全程检查 Gradle 生命周期。
func waitUntil(timeout time.Duration, guard *harness.GradleProc, cond func(context.Context) bool) error {
	return harness.WaitForCondition(timeout, time.Second, guard, cond)
}

// requireEnv 取必填 env，缺失即 t.Skip（让普通 go test ./... 不因缺密钥失败）。
func requireEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("跳过：缺少必需环境变量 %s（仅在显式 -tags=e2e 且注入密钥时运行）", key)
	}
	return v
}

// containsStr 判断字符串是否在列表中。
func containsStr(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}

// newUUIDv4 用 crypto/rand 生成 UUID v4 文本（36 字符），作补报 localTraceId（spec §3.4）。
// 不引第三方 uuid 依赖（依赖管理纪律）；读随机失败沿用零字节（概率可忽略）。
func newUUIDv4() string {
	var b [16]byte
	_, _ = crand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// utoa 把无符号整数转十进制字符串（避免为一处 uint 拼接引入 strconv 导入噪声）。
func utoa(v uint) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
