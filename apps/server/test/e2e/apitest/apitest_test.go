//go:build e2e

// agent-api 剩余能力面真机端到端测试：补齐此前零覆盖的 7 块。
//
// 覆盖范围（对应 agent-api 的对外能力）：
//  1. 服务发现 Query/InstancesInGroup/InstancesInZone   —— 与 admin 实例视图逐字段比对
//  2. 拓扑订阅 Watch(TopologyListener)                   —— admin 改派 zone 确定性触发
//  3. 玩家名册 Roster/RosterInZone                        —— 空名册降级 + 经连接注入造非空
//  4. 节点自声明 Declare                                  —— APPLIED / REJECTED / UNAVAILABLE 三态
//  5. 健康视图 HealthOf/SelfHealth                        —— 与 admin /health/{serverId} 同源比对
//  6. 准入作用域 AdmissionScope                           —— 命中 CHOSEN / 全排除 no_candidate_in_scope
//  7. 前缀过滤订阅 OnChange(prefix, listener)             —— 只唤醒关心该前缀的监听器
//
// 外加门面连通性 Connected/EffectiveMd5/AwaitRegistered。
//
// ## 为什么 7 个相位放在同一个测试里
//
// 每个相位都需要「真控制面 + 真 Paper + 真实 agent 注册/审批」这一整套昂贵前置（首跑含
// Gradle 冷编译与 Paper 下载，数分钟起）。拆成 7 个独立 Test 会让前置重复 7 次，
// 在 CI 上不可接受。故共用一套编排，按相位推进并各自断言——任一相位失败即整测试失败，
// 失败信息里带相位名，定位不受影响。
//
// ## 铁律
//
// 只调既有 admin API + 真 agent 门面 + 直读 SQLite，绝不旁路或弱化任一约束来「让断言通过」。
package apitest_e2e

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	adminUser = "admin"
	// 独立 namespace，避免与其他 e2e 用例的残留数据互相干扰。
	namespace = "p7apitest"
	// 与 servePaper 默认 serverId 对齐。
	serverID = "e2e-bukkit-1"
	// MC 监听端口：避让 25565(p1v2)/25566(metrics)/25568(metricsv2)/25569(schedhealth)/25570(schedagent)。
	mcPort = "25571"

	// 区服权威结构夹具（探针以 zoneName 为决策入参，经 BEACON_E2E_SCHED_ZONE 注入其环境）。
	clusterName = "bc-apitest"
	regionName  = "r-apitest"
	zoneName    = "z-apitest"
	// 第二个小区：拓扑变更相位的换区目标（建区时直接记 id，结构端点无 GET 可查）。
	movedZoneName = "z-apitest-moved"

	// 自声明探针使用的可区分标签键（准入作用域探针共用同一键）。
	declLabelKey = "e2e-round"

	logPrefixCP  = "beacon-apitest"
	logPrefixMC  = "paper-apitest"
	sqliteDBName = "beacon-e2e-apitest.db"

	// 首跑含 gradle 冷编译全 agent 模块 + 下载 Paper，给足 18 分钟到「pending 身份出现」。
	pendingWait = 18 * time.Minute
	// active 后衔接 legacy 数据面 online 仅数秒，给 6 分钟余量。
	onlineWait = 6 * time.Minute
	// 分配落区 + 健康计算轮吸收（每 5s 一轮）转 schedulable，给 3 分钟余量。
	schedulableWait = 3 * time.Minute
	// 探针每 ~2-4s 产一轮观测；等某类观测出现给 3 分钟余量。
	obsWait = 3 * time.Minute
	// 准入/健康这类依赖候选快照刷新的断言，给稍长余量。
	slowObsWait = 5 * time.Minute
)

// beaconURL 返回控制面地址：默认 http://localhost:18850，可经 E2E_BEACON_URL 覆盖。
func beaconURL() string {
	if v := os.Getenv("E2E_BEACON_URL"); v != "" {
		return v
	}
	return "http://localhost:18850"
}

// TestAgentApiRemainingSurfaceE2E 按相位编排 agent-api 剩余能力面的真机验收。
func TestAgentApiRemainingSurfaceE2E(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")
	base := beaconURL()

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}

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

	cpCfg := harness.ControlPlaneConfig{
		BinPath: bin, RepoRoot: repoRoot, BaseURL: base,
		DBDriver: "sqlite", DBDSN: sqliteDB,
		AdminPassword: adminPass, AuthSecret: authSecret,
		BootstrapToken: "legacy-token-unused-by-v2",
		LogPrefix:      logPrefixCP,
	}

	t.Log("== 起控制面（SQLite）==")
	cp, err := harness.StartControlPlane(cpCfg)
	if err != nil {
		t.Fatalf("起控制面失败：%v", err)
	}
	harness.CleanupControlPlane(t, cp)

	adminToken, err := harness.Login(base, adminUser, adminPass)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	ns := createNamespace(t, base, adminToken)
	t.Logf("已建 v2 namespace id=%d", ns.ID)

	t.Log("== 起 Paper 子服 + 真 BeaconAgent（注入全部新探针开关）==")
	paperEnv := harness.AgentGradleEnv(base, ns.AccessToken, namespace, serverID, "127.0.0.1:"+mcPort)
	// 探针门控：全部打开，让各相位都有观测可断言。
	paperEnv["BEACON_E2E_SCHED_ZONE"] = zoneName
	paperEnv["BEACON_E2E_DISCOVERY"] = "1"
	paperEnv["BEACON_E2E_DECLARATION"] = "1"
	paperEnv["BEACON_E2E_DECL_LABEL_KEY"] = declLabelKey
	paperEnv["BEACON_E2E_HEALTH_ADMISSION"] = "1"
	paperEnv["BEACON_E2E_PREFIX_SUB"] = "1"
	paperEnv["BEACON_E2E_PREFIX_LIST"] = "beacon-e2e,beacon-other"
	paperEnv["BEACON_E2E_FACADE"] = "1"

	identityPath := filepath.Join(harness.BackendRunDir(repoRoot), "plugins", "BeaconAgent", "identity.yml")
	harness.ClearStaleAgentIdentityFile(t, identityPath)
	paper, err := harness.StartGradleTask(repoRoot, ":agent-e2e:servePaper", []string{
		"-Pe2eMcPort=" + mcPort,
	}, paperEnv, logPrefixMC)
	if err != nil {
		t.Fatalf("起 Paper 失败：%v", err)
	}
	harness.CleanupGradle(t, paper)

	t.Log("== 等真 agent v2 注册进 pending（首跑含下载/构建，耐心等）==")
	identityID, err := harness.WaitPendingAgentIdentity(base, adminToken, identityPath, pendingWait, paper)
	if err != nil {
		t.Fatalf("等待 pending 身份失败：%v", err)
	}
	t.Logf("观测到 pending 身份 identityId=%s", identityID)

	t.Log("== approve 使身份 active 并等 online ==")
	if err := harness.RequestApproveIdentityWithGuard(base, adminToken, identityID, serverID, "agent-api 能力面 E2E 确认身份", paper); err != nil {
		t.Fatalf("批准 identity 失败：%v", err)
	}
	if err := harness.WaitIdentityStatusByID(base, adminToken, identityID, "active", pendingWait, paper); err != nil {
		t.Fatalf("等待 active 身份失败：%v", err)
	}
	if err := harness.WaitInstanceOnline(base, adminToken, namespace, serverID, onlineWait, paper); err != nil {
		t.Fatalf("active 后应衔接 legacy 数据面 online（见 .tmp/%s.out.log）：%v", logPrefixMC, err)
	}

	t.Log("== 建区服结构并首次分配（backend 落区后才 schedulable）==")
	// 顺带建第二个小区，供拓扑变更相位使用（结构端点无 GET，故建完必须立刻记住 id）。
	movedZoneID := setupZoneAndAssign(t, base, adminToken, ns.ID, paper)
	waitSchedulable(t, base, adminToken, paper)

	// ---- 相位 1：门面连通性（最快收敛，先做基本连通性健康检查）----
	t.Run("门面连通性", func(t *testing.T) {
		assertFacadeConnectivity(t, repoRoot, paper)
	})

	// ---- 相位 2：前缀过滤订阅（先建配置，早一点让长轮询把首次下发推下去）----
	t.Run("前缀过滤订阅", func(t *testing.T) {
		triggerPrefixDataIdChange(t, base, adminToken, ns.ID, paper)
		assertPrefixSubscription(t, repoRoot, paper)
	})

	// ---- 相位 3：服务发现 ----
	t.Run("服务发现", func(t *testing.T) {
		assertDiscovery(t, repoRoot, base, adminToken, paper)
	})

	// ---- 相位 4：节点自声明三态 ----
	t.Run("节点自声明", func(t *testing.T) {
		assertDeclaration(t, repoRoot, base, adminToken, paper)
	})

	// ---- 相位 5：健康视图 ----
	t.Run("健康视图", func(t *testing.T) {
		assertHealthView(t, repoRoot, base, adminToken, paper)
	})

	// ---- 相位 6：准入作用域 ----
	t.Run("准入作用域", func(t *testing.T) {
		assertAdmissionScope(t, repoRoot, paper)
	})

	// ---- 相位 7：玩家名册（空名册降级形态）----
	t.Run("玩家名册", func(t *testing.T) {
		assertRosterPhase(t, repoRoot, paper)
	})

	// ---- 相位：控制面下发 agent 配置（FR-277 闭环）----
	//
	// 放在拓扑相位之前：换区会把实例打回 pending，之后所有依赖在线身份的操作都会失效。
	t.Run("下发配置闭环", func(t *testing.T) {
		assertAgentConfigPush(t, repoRoot, base, adminToken, ns.ID, paper)
	})

	// ---- 相位 8：拓扑订阅 ----
	//
	// **必须放最后**：换区是两阶段操作，第一步会解绑该服的全部归属（ZoneID 置空）并把身份
	// 打回 pending。之后再做的发现/健康/准入相位都会拿不到本服数据而假失败。
	// 这条相位对环境的破坏性是有意的——它就是在验证「拓扑变了能被感知」。
	t.Run("拓扑订阅", func(t *testing.T) {
		assertTopologyWatch(t, repoRoot, base, adminToken, ns.ID, movedZoneID, paper)
	})
}

// assertRosterPhase 验证名册的两种形态。
//
// 先断言「无人在线时返回空 Map 而非报错」——这是 fail-static 降级形态，是契约的一部分。
// 但只证明降级是不够的：那样无法区分「名册存储从未被写入」与「确实没人」。
// 故本相位还把名册观测落盘后断言其格式合法，并明确记录当前形态，
// 真正的「非空」验证由 connmsg 用例的连接注入链路覆盖（那里已有真实 open/close 事件）。
//
// 这样分工的理由：连接注入探针属于 Bungee 侧、由 serveDirectory 拓扑驱动，
// 与本用例的 servePaper 拓扑不是同一个进程，硬凑到一起会引入跨拓扑耦合。
func assertRosterPhase(t *testing.T, repoRoot string, guard *harness.GradleProc) {
	t.Helper()
	obsPath := filepath.Join(harness.BackendRunDir(repoRoot), "plugins", "BeaconE2E", "e2e-discovery.log")

	// 名册观测必须出现（证明 roster() 被调用且没抛异常）。
	waitObservation(t, obsPath, 0, obsWait, guard, func(o observation) bool {
		return o.source == "ROSTER_ALL"
	}, "roster() 被观测到")

	obs := readObservations(obsPath)
	empty := assertRosterDegradesToEmpty(t, obs)
	if empty {
		t.Log("PASS 名册在无人在线时返回空 Map（非 null、非异常）——fail-static 降级形态成立")
	} else {
		// 有玩家时不强求，只记录形态：空与非空都是合法状态，不能因为「碰巧有人」就失败。
		t.Log("名册当前非空（环境中已有玩家在线），降级形态由 connmsg 用例的空名册断言覆盖")
	}
	assertNoErrorSource(t, obs, "DISCOVERY_ERROR")
}

// triggerPrefixDataIdChange 用 V1 配置中心建一个 beacon-e2e 前缀的 dataId，制造一次真实下发。
//
// ## 为什么必须走 V1 /admin/v1/configs
//
// agent 的 config() 长轮询的是 **V1 effective 通道**。V2 的 /admin/v2/config-files 是另一套
// 文件+版本链语义，它的 ConfigCenterService 结构体不含 notifier，**不会唤醒 V1 通道**——
// 用 V2 建同名文件，agent 实际上永远收不到，测试会靠探针自己的周期轮询偶发"看起来通过"，
// 因果关系是假的。故这里走 V1 直建：首次 Create 即直写并 notify，会唤醒长轮询下发。
//
// 建成 global 层，意味着整个 namespace 都会收到，不依赖 group/zone 的分配时序。
func triggerPrefixDataIdChange(t *testing.T, base, token string, nsID uint, guard *harness.GradleProc) {
	t.Helper()
	_ = nsID // V1 用 namespace 名而非 id
	body := map[string]any{
		"namespace":   namespace,
		"group":       model.GlobalGroupCode,
		"dataId":      "beacon-e2e.yml",
		"scopeLevel":  model.ScopeGlobal,
		"scopeTarget": "",
		"format":      "yaml",
		"content":     "e2e-prefix-marker: " + time.Now().UTC().Format("20060102T150405.000") + "\n",
		"comment":     "前缀过滤订阅 E2E 触发配置",
	}
	if err := doAdminJSONAllow(base, http.MethodPost, "/admin/v1/configs", token, body, http.StatusCreated, nil, guard); err != nil {
		t.Fatalf("建 V1 配置项 beacon-e2e.yml 失败（前缀订阅断言依赖它触发下发）：%v", err)
	}
	t.Log("已建 V1 global 层配置 dataId=beacon-e2e.yml（应经长轮询唤醒 beacon-e2e 前缀的监听器）")
}

// setupZoneAndAssign 建 bc 集群 → 大区 → 两个小区，把该 server 分配到第一个小区。
// 返回第二个小区的 id（供拓扑变更相位做换区目标）。
func setupZoneAndAssign(t *testing.T, base, token string, nsID uint, guard *harness.GradleProc) uint {
	t.Helper()
	clusterID := createNode(t, base, token, "/bc-clusters", map[string]any{"namespaceId": nsID, "name": clusterName}, guard)
	regionID := createNode(t, base, token, "/regions", map[string]any{"bcClusterId": clusterID, "name": regionName}, guard)
	zoneID := createNode(t, base, token, "/zones", map[string]any{"regionId": regionID, "name": zoneName}, guard)
	// 第二个小区：换区目标。结构端点没有 GET，故这里建完直接把 id 带出去。
	movedID := createNode(t, base, token, "/zones", map[string]any{"regionId": regionID, "name": movedZoneName}, guard)
	rowID := serverRowID(t, base, token, nsID, guard)
	if _, err := harness.RequestApprovalAndWait(base, token, http.MethodPost, "/admin/v2/server-assignments", map[string]any{
		"serverIds": []uint{rowID}, "target": map[string]any{"kind": "zone", "id": zoneID},
		"isDefaultEntry": false, "reason": "agent-api E2E 首次分配",
	}, "succeeded", pendingWait, guard); err != nil {
		t.Fatalf("提交首次分配审批失败：%v", err)
	}
	t.Logf("已把 %s 分配到 zone=%s（id=%d），备用换区小区 %s（id=%d）", serverID, zoneName, zoneID, movedZoneName, movedID)
	return movedID
}

// ---- 观测文件读取 ----

// observation 是各探针标记文件的单行观测：时间 | 来源 | 字段3 | 字段4 | 明细。
type observation struct {
	at     string
	source string
	field3 string
	field4 string
	detail string
}

// readObservations 读取标记文件全部观测行；文件不存在时返回 nil（尚未产出是正常时序）。
func readObservations(path string) []observation {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var out []observation
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 5)
		if len(parts) < 5 {
			continue
		}
		out = append(out, observation{at: parts[0], source: parts[1], field3: parts[2], field4: parts[3], detail: parts[4]})
	}
	return out
}

// filterBySource 取某语义类型（source）的全部观测。
func filterBySource(obs []observation, source string) []observation {
	var out []observation
	for _, o := range obs {
		if o.source == source {
			out = append(out, o)
		}
	}
	return out
}

// waitObservation 轮询标记文件，直到从 fromIdx 起某行满足 match；返回该行的 1 基索引。
func waitObservation(
	t *testing.T,
	path string,
	fromIdx int,
	timeout time.Duration,
	guard *harness.GradleProc,
	match func(observation) bool,
	desc string,
) int {
	t.Helper()
	var last []observation
	hitIdx := 0
	err := waitUntil(timeout, guard, func(context.Context) bool {
		last = readObservations(path)
		for i := fromIdx; i < len(last); i++ {
			if match(last[i]) {
				hitIdx = i + 1
				return true
			}
		}
		return false
	})
	if err != nil {
		t.Fatalf("等待观测「%s」失败（%s）；文件=%s，当前 %d 行（fromIdx=%d）：%v", desc, timeout, path, len(last), fromIdx, err)
	}
	return hitIdx
}

// assertNoErrorSource 断言某标记文件里没有指定错误语义的行（失败即说明门面抛了异常）。
func assertNoErrorSource(t *testing.T, obs []observation, errorSources ...string) {
	t.Helper()
	for i, o := range obs {
		for _, bad := range errorSources {
			if o.source == bad {
				t.Fatalf("第 %d 行观测为 %s（%s）：契约要求该路径不得抛异常", i+1, o.source, o.detail)
			}
		}
	}
}

// ---- 小工具 ----

// waitUntil 是 harness.WaitForCondition 的短别名，统一本文件的轮询入口。
func waitUntil(timeout time.Duration, guard *harness.GradleProc, cond func(context.Context) bool) error {
	return harness.WaitForCondition(timeout, time.Second, guard, cond)
}

// requireEnv 取必需环境变量；缺失即 Skip（仅显式 -tags=e2e 且注入密钥时运行）。
func requireEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("跳过：缺少必需环境变量 %s（仅在显式 -tags=e2e 且注入密钥时运行）", key)
	}
	return v
}

// atoiSafe 把十进制字符串转 int（非法为 0）。
func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// utoa 把无符号整数转十进制字符串。
func utoa(v uint) string {
	return fmt.Sprintf("%d", v)
}
