//go:build e2e

// FR-148「本机 agent-api 调度接口 + fail-static 降级」真机端到端测试，纯 Go 原生 go test -tags=e2e
// （默认 SQLite，无需 docker/MySQL）。
//
// 与 schedhealth 用例的本质区别：schedhealth 用 Go HTTP 客户端「模拟」agent 面直调控制面端点；本用例驱动
// 真 agent 的 **纯 Java 只读门面** BeaconAgentProvider.get().scheduling().acquireCandidate(zone)（经 BeaconE2E
// 探针周期取候选、把结果落 e2e-scheduling.log），验证 FR-148 的三条时序：
//
//	正常     agent 经门面取候选 → source=CONTROL_PLANE 且选中该服；控制面 decide 决策落库可查（source=control_plane）。
//	fail-static 杀控制面后 agent 仍经门面返回本地快照候选（source=LOCAL_FALLBACK）、不阻断、无未捕获异常（探针持续产观测即活性证明）。
//	恢复     重启控制面后 agent 自动回 CONTROL_PLANE，降级期本地决策经 report-local 补报入库可查（source=local_fallback）。
//
// 编排相位：build → namespace → agent(真 Paper，注入目标小区) → approve → 建区分配 → schedulable →
// 正常观测 + 决策落库 → 杀 CP fail-static 观测 → 重启 CP 恢复观测 + 补报入库。defer 收口杀全部进程。
//
// 铁律：只调既有 admin API + 真门面 + GORM 直读，绝不旁路或弱化任一 FR-148 约束来「让断言通过」。
package schedagent_e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	adminUser = "admin"
	// v2 namespace 名。
	namespace = "p4schedagent"
	// 与 servePaper 默认 serverId 对齐。
	serverID = "e2e-bukkit-1"
	// MC 监听端口：避让 25565(p1v2)/25566(metrics)/25568(metricsv2)/25569(schedhealth)，本用例用 25570。
	mcPort = "25570"

	// 区服权威结构夹具（agent 探针以 zoneName 为决策入参，经 BEACON_E2E_SCHED_ZONE 注入其环境）。
	clusterName = "bc-schedagent"
	regionName  = "r-schedagent"
	zoneName    = "z-schedagent"

	logPrefixCP  = "beacon-schedagent"
	logPrefixMC  = "paper-schedagent"
	sqliteDBName = "beacon-e2e-schedagent.db"

	// 首跑含 gradle 冷编译全 agent 模块 + 下载 Paper + agent 运行期依赖，给足 18 分钟到「pending 身份出现」。
	pendingWait = 18 * time.Minute
	// active 后衔接 legacy 数据面 online 仅数秒，给 6 分钟余量。
	onlineWait = 6 * time.Minute
	// 分配落区 + 健康计算轮吸收（每 5s 一轮）转 schedulable，给 3 分钟余量。
	schedulableWait = 3 * time.Minute
	// 探针每 ~2s 取一次候选；等其观测到「在线决策且候选快照就绪」，给 3 分钟余量。
	cpObsWait = 3 * time.Minute
	// 杀 CP 后探针下一轮即应走本地快照降级，给 2 分钟余量。
	failStaticWait = 2 * time.Minute
	// 重启 CP 后 agent 重注册 + 健康重算 + 候选重刷 + 补报，给 6 分钟余量。
	recoverWait = 6 * time.Minute
	// 决策 / 补报异步入库（写入通道 500ms 攒批 flush），给 1 分钟余量。
	persistWait = 1 * time.Minute
)

// 控制面地址：默认 http://localhost:18850，可经 E2E_BEACON_URL 覆盖。
func beaconURL() string {
	if v := os.Getenv("E2E_BEACON_URL"); v != "" {
		return v
	}
	return "http://localhost:18850"
}

// TestSchedAgentFailStaticE2E 按相位编排 FR-148 真门面端到端（含杀 CP fail-static 与恢复补报）。
func TestSchedAgentFailStaticE2E(t *testing.T) {
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

	// 控制面配置复用（杀掉后按同配置 + 同库重启）。
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

	t.Log("== 起 Paper 子服 + 真 BeaconAgent（注入目标小区）==")
	paperEnv := harness.AgentGradleEnv(base, ns.AccessToken, namespace, serverID, "127.0.0.1:"+mcPort)
	paperEnv["BEACON_E2E_SCHED_ZONE"] = zoneName
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
	t.Logf("观测到 pending 身份 identityId=%s", identityID)

	t.Log("== approve 使身份 active 并等 online ==")
	approveIdentity(t, base, adminToken, identityID, paper)
	if _, err := harness.WaitIdentityStatus(base, adminToken, ns.ID, serverID, "active", pendingWait, paper); err != nil {
		t.Fatalf("等待 active 身份失败：%v", err)
	}
	if err := harness.WaitInstanceOnline(base, adminToken, namespace, serverID, onlineWait, paper); err != nil {
		t.Fatalf("active 后应衔接 legacy 数据面 online（见 .tmp/%s.out.log）：%v", logPrefixMC, err)
	}

	t.Log("== 建区服结构并首次分配（backend 落区后才 schedulable）==")
	setupZoneAndAssign(t, base, adminToken, ns.ID, paper)
	waitSchedulable(t, base, adminToken, paper)

	obsPath := obsFile(repoRoot)

	t.Log("== 正常路径：等真门面观测到 source=CONTROL_PLANE 且选中该服、候选快照就绪 ==")
	waitObservation(t, obsPath, 0, cpObsWait, paper, func(o observation) bool {
		return o.source == "CONTROL_PLANE" && o.chosen == serverID && o.candidates >= 1
	}, "CONTROL_PLANE 选中该服且候选快照就绪")
	assertDecisionInDB(t, sqliteDB, model.SchedSourceControlPlane, persistWait, paper)
	t.Log("PASS 正常：真门面 acquireCandidate 走控制面在线决策、选中该服、决策落库 source=control_plane")

	// 记录杀 CP 前的观测行数，之后只认更新的观测（确保是杀 CP 之后的降级证据）。
	beforeKill := len(readObservations(obsPath))

	t.Log("== fail-static：杀控制面，验真门面仍经本地快照返回候选（LOCAL_FALLBACK）、不阻断、无未捕获异常 ==")
	if err := cp.StopE(); err != nil {
		t.Fatalf("fail-static 前停止控制面失败：%v", err)
	}
	failIdx := waitObservation(t, obsPath, beforeKill, failStaticWait, paper, func(o observation) bool {
		return o.source == "LOCAL_FALLBACK" && o.chosen == serverID
	}, "杀 CP 后 LOCAL_FALLBACK 仍选中该服")
	// 杀 CP 后不得出现门面未捕获异常观测（fail-static 契约）。
	assertNoAcquireError(t, readObservations(obsPath))
	t.Logf("PASS fail-static：杀 CP 后第 %d 行观测 source=LOCAL_FALLBACK 选中 %s，探针持续产观测=agent 未崩、玩家链路不阻断", failIdx, serverID)

	// 记录重启 CP 前的观测行数。
	beforeRecover := len(readObservations(obsPath))

	t.Log("== 恢复：重启控制面（同库），验自动回 CONTROL_PLANE 且降级期决策经 report-local 补报入库 ==")
	cp2, err := harness.StartControlPlane(cpCfg)
	if err != nil {
		t.Fatalf("重启控制面失败：%v", err)
	}
	harness.CleanupControlPlane(t, cp2)

	waitObservation(t, obsPath, beforeRecover, recoverWait, paper, func(o observation) bool {
		return o.source == "CONTROL_PLANE" && o.chosen == serverID
	}, "恢复后自动回 CONTROL_PLANE")
	assertDecisionInDB(t, sqliteDB, model.SchedSourceLocalFallback, recoverWait, paper)
	t.Logf("PASS 恢复：agent 自动回 CONTROL_PLANE；降级期本地决策经 report-local 补报入库 source=local_fallback")
}

// ---- 观测文件解析 ----

// observation 是 e2e-scheduling.log 单行观测（时间 | 来源 | 小区 | 选中 serverId | 明细）。
type observation struct {
	source     string
	zone       string
	chosen     string
	candidates int
	raw        string
}

// obsFile 返回 mc-testkit Paper 运行目录中的调度探针观测文件。
func obsFile(repoRoot string) string {
	return filepath.Join(harness.BackendRunDir(repoRoot), "plugins", "BeaconE2E", "e2e-scheduling.log")
}

// readObservations 读取观测文件全部行，文件不存在时返回空。
func readObservations(path string) []observation {
	return parseObservations(path)
}

// parseObservations 按行解析观测文件（每行 5 段以 | 分隔）。
func parseObservations(path string) []observation {
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
		out = append(out, observation{
			source:     parts[1],
			zone:       parts[2],
			chosen:     parts[3],
			candidates: extractCandidates(parts[4]),
			raw:        parts[4],
		})
	}
	return out
}

// extractCandidates 从明细段 raw 里取 candidates=N（缺失为 0）。
func extractCandidates(raw string) int {
	for _, kv := range strings.Split(raw, ";") {
		if strings.HasPrefix(kv, "candidates=") {
			return atoiSafe(strings.TrimPrefix(kv, "candidates="))
		}
	}
	return 0
}

// waitObservation 轮询观测文件，直到「索引 ≥ fromIdx」的某行满足 match，返回其行索引（1 基）。
func waitObservation(t *testing.T, path string, fromIdx int, timeout time.Duration, guard *harness.GradleProc, match func(observation) bool, desc string) int {
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
		t.Fatalf("等待观测「%s」失败（%s）；观测文件=%s，当前 %d 行（fromIdx=%d）：%v", desc, timeout, path, len(last), fromIdx, err)
	}
	return hitIdx
}

// assertNoAcquireError 断言观测里没有门面未捕获异常行（fail-static 契约：future 绝不异常完成）。
func assertNoAcquireError(t *testing.T, obs []observation) {
	t.Helper()
	for i, o := range obs {
		if o.source == "ACQUIRE_ERROR" {
			t.Fatalf("第 %d 行观测为 ACQUIRE_ERROR（%s）：fail-static 契约要求门面绝不抛未捕获异常", i+1, o.raw)
		}
	}
}

// ---- DB 直读断言 ----

// assertDecisionInDB 轮询当日 sched_decision 日表，直到出现指定 source 且选中该服的决策行。
func assertDecisionInDB(t *testing.T, sqliteDB, source string, timeout time.Duration, guard *harness.GradleProc) {
	t.Helper()
	db := openE2EDB(t, sqliteDB)
	dailyName := store.DailyTableName("sched_decision", time.Now().UTC())
	err := waitUntil(timeout, guard, func(context.Context) bool {
		if !db.Migrator().HasTable(dailyName) {
			return false
		}
		var count int64
		if err := db.Table(dailyName).
			Where("source = ? AND chosen_server_id = ?", source, serverID).
			Count(&count).Error; err != nil {
			return false
		}
		return count > 0
	})
	if err != nil {
		t.Fatalf("日表 %s 未在 %s 内出现 source=%q 且 chosen=%s 的决策行：%v", dailyName, timeout, source, serverID, err)
	}
}

// ---- admin 编排助手 ----

type namespaceView struct {
	ID          uint   `json:"id"`
	AccessToken string `json:"accessToken"`
}

type identityView struct {
	IdentityID string `json:"identityId"`
	ServerID   string `json:"serverId"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
}

func createNamespace(t *testing.T, base, token string) namespaceView {
	t.Helper()
	id, accessToken, err := harness.CreateV2Namespace(base, token, namespace, "FR-148 真门面 fail-static e2e")
	if err != nil {
		t.Fatalf("建 namespace 失败：%v", err)
	}
	return namespaceView{ID: id, AccessToken: accessToken}
}

func createNode(t *testing.T, base, token, path string, body map[string]any, guard harness.ProcessGuard) uint {
	t.Helper()
	var out struct {
		ID uint `json:"id"`
	}
	doAdminJSON(t, base, http.MethodPost, "/admin/v2"+path, token, body, http.StatusCreated, &out, guard)
	if out.ID == 0 {
		t.Fatalf("建 %s 应返回数字 id，实际 %+v", path, out)
	}
	return out.ID
}

// setupZoneAndAssign 建 bc 集群 → 大区 → 小区，并把该 server 首次分配到小区。
func setupZoneAndAssign(t *testing.T, base, token string, nsID uint, guard harness.ProcessGuard) {
	t.Helper()
	clusterID := createNode(t, base, token, "/bc-clusters", map[string]any{"namespaceId": nsID, "name": clusterName}, guard)
	regionID := createNode(t, base, token, "/regions", map[string]any{"bcClusterId": clusterID, "name": regionName}, guard)
	zoneID := createNode(t, base, token, "/zones", map[string]any{"regionId": regionID, "name": zoneName}, guard)
	rowID := serverRowID(t, base, token, nsID, guard)
	doAdminJSON(t, base, http.MethodPost, "/admin/v2/server-assignments", token, map[string]any{
		"serverIds": []uint{rowID}, "target": map[string]any{"kind": "zone", "id": zoneID},
		"isDefaultEntry": false, "reason": "e2e 首次分配",
	}, http.StatusOK, nil, guard)
	t.Logf("已把 %s 分配到 zone=%s（id=%d）", serverID, zoneName, zoneID)
}

// waitSchedulable 等健康计算轮吸收分配事实：/admin/v2/health/{serverId} 转 schedulable=true。
func waitSchedulable(t *testing.T, base, token string, guard *harness.GradleProc) {
	t.Helper()
	var detail struct {
		Schedulable bool `json:"schedulable"`
	}
	err := waitUntil(schedulableWait, guard, func(ctx context.Context) bool {
		detail.Schedulable = false
		if !tryGet(ctx, base+"/admin/v2/health/"+url.PathEscape(serverID), token, &detail, guard) {
			return false
		}
		return detail.Schedulable
	})
	if err != nil {
		t.Fatalf("等待 %s 分配后转 schedulable=true 失败（%s）：%v", serverID, schedulableWait, err)
	}
}

func serverRowID(t *testing.T, base, token string, nsID uint, guard harness.ProcessGuard) uint {
	t.Helper()
	var out struct {
		Items []struct {
			ID       uint   `json:"id"`
			ServerID string `json:"serverId"`
		} `json:"items"`
	}
	doAdminJSON(t, base, http.MethodGet, "/admin/v2/servers?namespaceId="+utoa(nsID), token, nil, http.StatusOK, &out, guard)
	for _, it := range out.Items {
		if it.ServerID == serverID {
			return it.ID
		}
	}
	t.Fatalf("未找到 server %s 的行记录", serverID)
	return 0
}

func approveIdentity(t *testing.T, base, token, identityID string, guard *harness.GradleProc) {
	t.Helper()
	if err := harness.ApproveIdentityWithGuard(base, token, identityID, guard); err != nil {
		t.Fatalf("批准 identity 失败：%v", err)
	}
}

// ---- HTTP / DB / 小工具 ----

func doAdminJSON(t *testing.T, base, method, path, token string, body any, wantStatus int, out any, guard harness.ProcessGuard) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败：%v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, strings.TrimRight(base, "/")+path, reader)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := harness.DoRequestWithGuard(req, 0, guard)
	if err != nil {
		t.Fatalf("%s %s 请求失败：%v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s 期望 HTTP %d，得 %d", method, path, wantStatus, resp.StatusCode)
	}
	if out != nil {
		raw, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			t.Fatalf("读取 %s 响应失败：%v", path, readErr)
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				t.Fatalf("解析 %s 响应失败：%v", path, err)
			}
		}
	}
}

// tryGet 发一个带 Bearer 的 admin GET，仅在 200 且能解析时返回 true（轮询用，不报错）。
func tryGet(ctx context.Context, u, token string, out any, guard harness.ProcessGuard) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := harness.DoRequestWithGuard(req, 0, guard)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	raw, _ := io.ReadAll(resp.Body)
	return json.Unmarshal(raw, out) == nil
}

func openE2EDB(t *testing.T, sqliteDB string) *gorm.DB {
	t.Helper()
	dsn := sqliteDB
	if !strings.Contains(dsn, "?") {
		dsn += "?_busy_timeout=5000"
	}
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("连接数据库失败：%v", err)
	}
	return db
}

func waitUntil(timeout time.Duration, guard *harness.GradleProc, cond func(context.Context) bool) error {
	return harness.WaitForCondition(timeout, time.Second, guard, cond)
}

func requireEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("跳过：缺少必需环境变量 %s（仅在显式 -tags=e2e 且注入密钥时运行）", key)
	}
	return v
}

// atoiSafe 把十进制字符串转 int（非法为 0，避免为一处引入 strconv 噪声）。
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
