//go:build e2e

// FR-171 hot_reload 真 Paper 端到端测试：真实控制面、BeaconAgent 与业务插件回调共同完成正推、回滚和失败三相位。
package hotreload_e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	adminUser      = "admin"
	serverID       = "e2e-bukkit-1"
	mcPort         = "25566"
	bootstrapToken = "beacon-bootstrap-2026"
	initialContent = "marker: original\n"
	clusterName    = "hotreload-bc"
	regionName     = "hotreload-region"
	zoneName       = "hotreload-zone"

	successPath  = "plugins/BeaconE2E/delivery-hot-reload.yml"
	failurePath  = "plugins/BeaconE2E/delivery-hot-reload-fail.yml"
	probeLogName = "e2e-delivery-hot-reload.log"

	onlineWait = 12 * time.Minute
	phaseWait  = 90 * time.Second
)

var (
	beaconURL = harness.BeaconURL()
	namespace = fmt.Sprintf("e2e-hotreload-%d", time.Now().UnixNano())
)

type testEnv struct {
	repoRoot    string
	runDir      string
	dbPath      string
	adminToken  string
	namespaceID uint
	db          *gorm.DB
	cp          *harness.ControlPlane
	paper       *harness.GradleProc
}

type orderDetail struct {
	ID      uint   `json:"id"`
	Status  string `json:"status"`
	Batches []struct {
		BatchNo int    `json:"batchNo"`
		Status  string `json:"status"`
	} `json:"batches"`
}

type targetView struct {
	ServerID       string  `json:"serverId"`
	Status         string  `json:"status"`
	BackupPresent  bool    `json:"backupPresent"`
	Error          *string `json:"error"`
	RollbackStatus *string `json:"rollbackStatus"`
}

type observation struct {
	source string
	path   string
	raw    string
}

// TestDeliveryHotReloadE2E 验证 activated、rolled_back、failed 三种真实执行结果，且失败回调不终止 Paper。
func TestDeliveryHotReloadE2E(t *testing.T) {
	env := startEnvironment(t)
	defer env.stop()

	waitProbeReady(t, env)
	runPositiveAndRollback(t, env)
	runFailure(t, env)
}

// startEnvironment 启动隔离 SQLite 控制面与真 Paper，并完成 Agent 身份批准。
func startEnvironment(t *testing.T) *testEnv {
	t.Helper()
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")
	env := prepareEnvironment(t)

	bin, err := harness.BuildBeacon(env.repoRoot)
	if err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}
	env.cp = startControlPlane(t, env, bin, adminPass, authSecret)
	env.adminToken = login(t, adminPass)
	env.namespaceID, env.paper = provisionPaper(t, env)
	env.db = openDB(t, env.dbPath)
	disableApproverSeparation(t, env.adminToken)
	approveAndWaitAgent(t, env)
	setupZoneAndAssign(t, env)
	return env
}

// prepareEnvironment 创建本用例独占的 SQLite 文件并清理 Paper 运行目录。
func prepareEnvironment(t *testing.T) *testEnv {
	t.Helper()
	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	runDir := filepath.Join(repoRoot, "apps", ".tmp", "e2e-run", "bukkit")
	if err := os.RemoveAll(runDir); err != nil {
		t.Fatalf("清理 Paper 运行目录失败：%v", err)
	}
	dbPath := filepath.Join(repoRoot, ".tmp", "beacon-e2e-hotreload.db")
	if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("清理 SQLite 文件失败：%v", err)
	}
	return &testEnv{repoRoot: repoRoot, runDir: runDir, dbPath: dbPath}
}

// startControlPlane 启动使用隔离 SQLite 的控制面。
func startControlPlane(t *testing.T, env *testEnv, bin, adminPass, authSecret string) *harness.ControlPlane {
	t.Helper()
	cp, err := harness.StartControlPlane(harness.ControlPlaneConfig{
		BinPath: bin, RepoRoot: env.repoRoot, BaseURL: beaconURL,
		DBDriver: "sqlite", DBDSN: sqliteDSN(env.dbPath),
		AdminPassword: adminPass, AuthSecret: authSecret, BootstrapToken: bootstrapToken,
		LogPrefix: "beacon-hotreload",
	})
	if err != nil {
		t.Fatalf("启动控制面失败：%v", err)
	}
	return cp
}

// provisionPaper 创建 namespace 并启动注入固定身份参数的 Paper。
func provisionPaper(t *testing.T, env *testEnv) (uint, *harness.GradleProc) {
	t.Helper()
	nsID, accessToken, err := harness.CreateV2Namespace(beaconURL, env.adminToken, namespace, "FR-171 hot_reload e2e")
	if err != nil {
		t.Fatalf("创建 namespace 失败：%v", err)
	}
	props := []string{
		"-Pe2eMcPort=" + mcPort, harness.BeaconEndpointProp(), "-Pe2eBootstrapToken=" + accessToken,
		"-Pe2eNamespace=" + namespace, "-Pe2eServerId=" + serverID,
	}
	paper, err := harness.StartGradleTask(env.repoRoot, ":agent-e2e:runServer", props, "paper-hotreload")
	if err != nil {
		t.Fatalf("启动 Paper 失败：%v", err)
	}
	return nsID, paper
}

// approveAndWaitAgent 批准首次 pending 身份，并等待真实实例 online。
func approveAndWaitAgent(t *testing.T, env *testEnv) {
	t.Helper()
	identityID, err := harness.WaitIdentityStatus(beaconURL, env.adminToken, env.namespaceID, serverID, "pending", onlineWait)
	if err != nil {
		t.Fatalf("等待 Agent pending 超时：%v", err)
	}
	if err := harness.ApproveIdentity(beaconURL, env.adminToken, identityID); err != nil {
		t.Fatalf("批准 Agent 身份失败：%v", err)
	}
	if _, err := harness.WaitIdentityStatus(beaconURL, env.adminToken, env.namespaceID, serverID, "active", onlineWait); err != nil {
		t.Fatalf("等待 Agent active 超时：%v", err)
	}
	if err := harness.WaitInstanceOnline(beaconURL, env.adminToken, namespace, serverID, onlineWait); err != nil {
		t.Fatalf("等待 Agent online 超时：%v", err)
	}
}

// setupZoneAndAssign 建立最小区服权威结构，并通过管理 API 把目标 backend 分配到小区。
func setupZoneAndAssign(t *testing.T, env *testEnv) {
	t.Helper()
	clusterID := createNode(t, env, "/admin/v2/bc-clusters", map[string]any{
		"namespaceId": env.namespaceID, "name": clusterName,
	})
	regionID := createNode(t, env, "/admin/v2/regions", map[string]any{
		"bcClusterId": clusterID, "name": regionName,
	})
	zoneID := createNode(t, env, "/admin/v2/zones", map[string]any{
		"regionId": regionID, "name": zoneName,
	})
	body := map[string]any{
		"serverIds":      []uint{serverRowID(t, env)},
		"target":         map[string]any{"kind": "zone", "id": zoneID},
		"isDefaultEntry": false, "reason": "FR-171 E2E 首次分配",
	}
	doAdmin(t, http.MethodPost, "/admin/v2/server-assignments", env.adminToken, body, http.StatusOK, nil)
}

// createNode 通过管理 API 创建区服权威节点并返回主键。
func createNode(t *testing.T, env *testEnv, path string, body map[string]any) uint {
	t.Helper()
	var out struct {
		ID uint `json:"id"`
	}
	doAdmin(t, http.MethodPost, path, env.adminToken, body, http.StatusCreated, &out)
	return out.ID
}

// serverRowID 读取目标 server 的数据库行 id，供首次分配端点使用。
func serverRowID(t *testing.T, env *testEnv) uint {
	t.Helper()
	var out struct {
		Items []struct {
			ID       uint   `json:"id"`
			ServerID string `json:"serverId"`
		} `json:"items"`
	}
	path := "/admin/v2/servers?namespaceId=" + strconv.FormatUint(uint64(env.namespaceID), 10)
	doAdmin(t, http.MethodGet, path, env.adminToken, nil, http.StatusOK, &out)
	for _, item := range out.Items {
		if item.ServerID == serverID {
			return item.ID
		}
	}
	t.Fatalf("未找到目标 server 行记录：%s", serverID)
	return 0
}

// waitProbeReady 等业务插件已通过 BeaconAgentProvider 注册真实 onChange 监听。
func waitProbeReady(t *testing.T, env *testEnv) {
	t.Helper()
	if _, ok := waitObservation(env.probeLog(), 30*time.Second, func(o observation) bool {
		return o.source == "LISTENER_READY"
	}); !ok {
		t.Fatalf("业务插件 hot_reload 监听未就绪，观测文件=%s", env.probeLog())
	}
}

// runPositiveAndRollback 验证推送后回调读到新内容、控制面 activated，再整单回滚到原内容和 rolled_back。
func runPositiveAndRollback(t *testing.T, env *testEnv) {
	t.Helper()
	activatedContent := fmt.Sprintf("marker: activated-%d\n", time.Now().UnixNano())
	versionID := seedConfig(t, env.db, env.namespaceID, successPath, activatedContent)
	orderID := createApprovedOrder(t, env, "FR-171 正向与回滚", versionID)

	startOrder(t, env, orderID)
	assertActivated(t, env, orderID, activatedContent)
	completeOrder(t, env, orderID)
	assertRollback(t, env, orderID)
}

// assertActivated 等真实 Agent 回执把目标推进到 activated，并核对磁盘与业务回调观测。
func assertActivated(t *testing.T, env *testEnv, orderID uint, content string) {
	t.Helper()
	target := waitTarget(t, env, orderID, func(v targetView) bool { return v.Status == model.ChangeTargetStatusActivated })
	if !target.BackupPresent {
		t.Fatal("正向目标 activated 时应存在覆盖前备份")
	}
	if _, ok := waitObservation(env.probeLog(), phaseWait, func(o observation) bool {
		return o.source == "ON_CHANGE" && o.path == successPath && strings.Contains(o.raw, strings.TrimSpace(content))
	}); !ok {
		t.Fatal("业务插件未在正向 onChange 中读到新磁盘内容")
	}
	assertFileContains(t, env.target(successPath), strings.TrimSpace(content))
	backup := filepath.Join(env.runDir, "plugins", "BeaconAgent", "delivery-backups", strconv.FormatUint(uint64(orderID), 10), "files", filepath.FromSlash(successPath))
	assertFileEquals(t, backup, initialContent)
}

// completeOrder 等观察窗结束后经真实批次确认 API 把单推进为 completed。
func completeOrder(t *testing.T, env *testEnv, orderID uint) {
	t.Helper()
	waitBatchStatus(t, env, orderID, model.ChangeBatchStatusAwaitingConfirm)
	path := fmt.Sprintf("/admin/v2/change-orders/%d/batches/1/confirm", orderID)
	var detail orderDetail
	doAdmin(t, http.MethodPost, path, env.adminToken, nil, http.StatusOK, &detail)
	if detail.Status != model.ChangeOrderStatusCompleted {
		t.Fatalf("末批确认后单状态应为 completed，实际=%s", detail.Status)
	}
}

// assertRollback 发起真实整单回滚，验证目标与单均 rolled_back，回调和磁盘均恢复原内容。
func assertRollback(t *testing.T, env *testEnv, orderID uint) {
	t.Helper()
	before := countObservation(env.probeLog(), "ON_CHANGE", successPath)
	path := fmt.Sprintf("/admin/v2/change-orders/%d/rollback", orderID)
	doAdmin(t, http.MethodPost, path, env.adminToken, map[string]string{"reason": "FR-171 E2E 验证回滚"}, http.StatusOK, nil)

	waitTarget(t, env, orderID, func(v targetView) bool {
		return v.RollbackStatus != nil && *v.RollbackStatus == model.RollbackStatusRolledBack
	})
	waitOrderStatus(t, env, orderID, model.ChangeOrderStatusRolledBack)
	if !waitUntil(phaseWait, func() bool {
		return countObservation(env.probeLog(), "ON_CHANGE", successPath) > before && fileContent(env.target(successPath)) == initialContent
	}) {
		t.Fatal("回滚后业务回调或磁盘内容未恢复到原始值")
	}
}

// runFailure 让业务插件在专用路径的真实 onChange 中抛错，验证 failed 回执且 Paper 继续在线。
func runFailure(t *testing.T, env *testEnv) {
	t.Helper()
	failedContent := fmt.Sprintf("marker: rejected-%d\n", time.Now().UnixNano())
	versionID := seedConfig(t, env.db, env.namespaceID, failurePath, failedContent)
	orderID := createApprovedOrder(t, env, "FR-171 回调失败", versionID)
	startOrder(t, env, orderID)

	target := waitTarget(t, env, orderID, func(v targetView) bool { return v.Status == model.ChangeTargetStatusFailed })
	if target.Error == nil || !strings.Contains(*target.Error, "配置变更通知失败") {
		t.Fatalf("失败目标应携带业务回调失败摘要，实际=%v", target.Error)
	}
	failedIndex, ok := waitObservation(env.probeLog(), phaseWait, func(o observation) bool {
		return o.source == "CALLBACK_FAILED" && o.path == failurePath
	})
	if !ok {
		t.Fatal("未观测到业务插件 onChange 主动失败记录")
	}
	assertFileContains(t, env.target(failurePath), strings.TrimSpace(failedContent))
	assertPaperAliveAfter(t, env, failedIndex)
}

// assertPaperAliveAfter 以失败记录之后的新存活观测、Minecraft 端口和控制面在线视图三重证明 Paper 未退出。
func assertPaperAliveAfter(t *testing.T, env *testEnv, failedIndex int) {
	t.Helper()
	if !waitUntil(30*time.Second, func() bool {
		records := readObservations(env.probeLog())
		for i := failedIndex + 1; i < len(records); i++ {
			if records[i].source == "PROBE_ALIVE" {
				return true
			}
		}
		return false
	}) {
		t.Fatal("回调失败后未出现新的业务插件存活观测")
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+mcPort, 3*time.Second)
	if err != nil {
		t.Fatalf("回调失败后 Paper 端口不可达：%v", err)
	}
	_ = conn.Close()
	if err := harness.WaitInstanceOnline(beaconURL, env.adminToken, namespace, serverID, 15*time.Second); err != nil {
		t.Fatalf("回调失败后 Agent 不再 online：%v", err)
	}
}

// seedConfig 在隔离 SQLite 中建立配置文件、基线版本与待发布版本；不写执行态或回执。
func seedConfig(t *testing.T, db *gorm.DB, namespaceID uint, path, targetContent string) uint {
	t.Helper()
	file := model.ConfigFile{NamespaceID: namespaceID, Name: path, Format: "yaml", CreatedBy: adminUser}
	mustCreate(t, db, &file)
	base := model.ConfigLayerVersion{
		ConfigFileID: file.ID, ScopeLevel: model.ConfigScopeNamespace, ScopeRefID: namespaceID,
		VersionNo: 1, Content: initialContent, ContentHash: sha256Hex(initialContent), CreatedBy: adminUser,
	}
	mustCreate(t, db, &base)
	target := model.ConfigLayerVersion{
		ConfigFileID: file.ID, ScopeLevel: model.ConfigScopeNamespace, ScopeRefID: namespaceID,
		VersionNo: 2, Content: targetContent, ContentHash: sha256Hex(targetContent),
		BasedOnVersionID: &base.ID, CreatedBy: adminUser,
	}
	mustCreate(t, db, &target)
	return target.ID
}

// createApprovedOrder 通过管理 API 完成建单、挂配置、提交与审批。
func createApprovedOrder(t *testing.T, env *testEnv, title string, versionID uint) uint {
	t.Helper()
	body := map[string]any{
		"namespaceId": env.namespaceID, "title": title,
		"selector":  map[string]any{"all": false, "regions": []uint{}, "zones": []uint{}, "servers": []string{serverID}, "excludes": []string{}},
		"batchMode": "count", "batchSizes": []int{1}, "activationMethod": model.ActivationMethodHotReload,
		"observeWindowSec": 1, "activateTimeoutSec": 60,
		"failureRateThresholdPercent": 0, "unhealthyRateThresholdPercent": 0,
	}
	var detail orderDetail
	doAdmin(t, http.MethodPost, "/admin/v2/change-orders", env.adminToken, body, http.StatusCreated, &detail)
	patchOrderConfig(t, env, detail.ID, versionID)
	transitionOrder(t, env, detail.ID, "submit")
	transitionOrder(t, env, detail.ID, "approve")
	return detail.ID
}

// patchOrderConfig 给草稿单挂 namespace 作用域配置版本。
func patchOrderConfig(t *testing.T, env *testEnv, orderID, versionID uint) {
	t.Helper()
	body := map[string]any{"configChanges": []map[string]any{{
		"configScopeKind": model.ConfigScopeNamespace,
		"configScopeId":   env.namespaceID, "configToVersionId": versionID,
	}}}
	path := fmt.Sprintf("/admin/v2/change-orders/%d", orderID)
	doAdmin(t, http.MethodPatch, path, env.adminToken, body, http.StatusOK, nil)
}

// transitionOrder 调用无额外参数的变更单生命周期端点。
func transitionOrder(t *testing.T, env *testEnv, orderID uint, action string) {
	t.Helper()
	path := fmt.Sprintf("/admin/v2/change-orders/%d/%s", orderID, action)
	doAdmin(t, http.MethodPost, path, env.adminToken, nil, http.StatusOK, nil)
}

// startOrder 仅经生产 start 端点启动执行状态机。
func startOrder(t *testing.T, env *testEnv, orderID uint) {
	t.Helper()
	path := fmt.Sprintf("/admin/v2/change-orders/%d/start", orderID)
	doAdmin(t, http.MethodPost, path, env.adminToken, map[string]string{"reason": "FR-171 E2E"}, http.StatusOK, nil)
}

// waitTarget 等目标满足谓词，超时输出最后事实。
func waitTarget(t *testing.T, env *testEnv, orderID uint, predicate func(targetView) bool) targetView {
	t.Helper()
	var last targetView
	if waitUntil(phaseWait, func() bool {
		for _, item := range getTargets(t, env, orderID) {
			if item.ServerID != serverID {
				continue
			}
			last = item
			return predicate(last)
		}
		return false
	}) {
		return last
	}
	t.Fatalf("等待目标状态超时，最后事实=%+v", last)
	return targetView{}
}

// getTargets 读取生产 targets 端点。
func getTargets(t *testing.T, env *testEnv, orderID uint) []targetView {
	t.Helper()
	var page struct {
		Items []targetView `json:"items"`
	}
	path := fmt.Sprintf("/admin/v2/change-orders/%d/targets?pageSize=10", orderID)
	doAdmin(t, http.MethodGet, path, env.adminToken, nil, http.StatusOK, &page)
	return page.Items
}

// waitBatchStatus 等第一批进入指定状态。
func waitBatchStatus(t *testing.T, env *testEnv, orderID uint, status string) {
	t.Helper()
	if !waitUntil(phaseWait, func() bool {
		detail := getOrder(t, env, orderID)
		return len(detail.Batches) == 1 && detail.Batches[0].Status == status
	}) {
		t.Fatalf("等待批次状态 %s 超时", status)
	}
}

// waitOrderStatus 等变更单进入指定状态。
func waitOrderStatus(t *testing.T, env *testEnv, orderID uint, status string) {
	t.Helper()
	if !waitUntil(phaseWait, func() bool { return getOrder(t, env, orderID).Status == status }) {
		t.Fatalf("等待变更单状态 %s 超时", status)
	}
}

// getOrder 读取生产变更单详情端点。
func getOrder(t *testing.T, env *testEnv, orderID uint) orderDetail {
	t.Helper()
	var detail orderDetail
	path := fmt.Sprintf("/admin/v2/change-orders/%d", orderID)
	doAdmin(t, http.MethodGet, path, env.adminToken, nil, http.StatusOK, &detail)
	return detail
}

// doAdmin 发管理请求并严格校验状态码与响应 JSON。
func doAdmin(t *testing.T, method, path, token string, body any, wantStatus int, out any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败：%v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, strings.TrimRight(beaconURL, "/")+path, reader)
	if err != nil {
		t.Fatalf("构造管理请求失败：%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("管理请求 %s %s 失败：%v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("管理请求 %s %s 期望 HTTP %d，实际 %d：%s", method, path, wantStatus, resp.StatusCode, string(raw))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("解析管理响应失败：%v，响应=%s", err, string(raw))
		}
	}
}

// disableApproverSeparation 仅设置测试前置，使单管理员也能走真实 submit→approve API。
func disableApproverSeparation(t *testing.T, token string) {
	t.Helper()
	body := map[string]any{"value": "false"}
	doAdmin(t, http.MethodPut, "/admin/v1/settings/delivery.approver-separation-enabled", token, body, http.StatusOK, nil)
}

// readObservations 解析业务插件单行观测文件；忽略并发写入中的不完整末行。
func readObservations(path string) []observation {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	out := make([]observation, 0, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, "|", 5)
		if len(parts) == 5 {
			out = append(out, observation{source: parts[1], path: parts[2], raw: parts[4]})
		}
	}
	return out
}

// waitObservation 等第一条命中观测并返回其行索引。
func waitObservation(path string, timeout time.Duration, predicate func(observation) bool) (int, bool) {
	index := -1
	ok := waitUntil(timeout, func() bool {
		for i, item := range readObservations(path) {
			if predicate(item) {
				index = i
				return true
			}
		}
		return false
	})
	return index, ok
}

// countObservation 统计指定来源与路径的观测数。
func countObservation(path, source, targetPath string) int {
	count := 0
	for _, item := range readObservations(path) {
		if item.source == source && item.path == targetPath {
			count++
		}
	}
	return count
}

// waitUntil 以固定短周期等待异步系统事实收敛。
func waitUntil(timeout time.Duration, predicate func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return predicate()
}

// assertFileContains 断言文件存在且包含目标标记。
func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	got := fileContent(path)
	if !strings.Contains(got, want) {
		t.Fatalf("文件 %s 未包含 %q，实际=%q", path, want, got)
	}
}

// assertFileEquals 断言文件字节文本与期望完全一致。
func assertFileEquals(t *testing.T, path, want string) {
	t.Helper()
	if got := fileContent(path); got != want {
		t.Fatalf("文件 %s 内容不符，期望=%q，实际=%q", path, want, got)
	}
}

// fileContent 读取文本文件，失败返回空串供轮询继续等待。
func fileContent(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

// mustCreate 插入测试前置模型，禁止用于任何执行态或回执表。
func mustCreate(t *testing.T, db *gorm.DB, value any) {
	t.Helper()
	if err := db.Create(value).Error; err != nil {
		t.Fatalf("写入测试前置数据失败：%v", err)
	}
}

// openDB 打开与控制面共享的隔离 SQLite。
func openDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(sqliteDSN(path)), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开 SQLite 失败：%v", err)
	}
	return db
}

// sqliteDSN 为并发控制面与测试连接启用忙等待。
func sqliteDSN(path string) string {
	return path + "?_busy_timeout=5000"
}

// sha256Hex 计算配置版本内容摘要。
func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// login 使用真实管理员登录端点。
func login(t *testing.T, password string) string {
	t.Helper()
	token, err := harness.Login(beaconURL, adminUser, password)
	if err != nil {
		t.Fatalf("管理员登录失败：%v", err)
	}
	return token
}

// requireEnv 读取真机 E2E 凭据；缺失时明确跳过而非伪造默认值。
func requireEnv(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Skipf("缺少真机 E2E 环境变量 %s", key)
	}
	return value
}

// probeLog 返回业务插件观测文件路径。
func (e *testEnv) probeLog() string {
	return filepath.Join(e.runDir, "plugins", "BeaconE2E", probeLogName)
}

// target 把固定服务器根相对路径映射到 Paper 运行目录。
func (e *testEnv) target(path string) string {
	return filepath.Join(e.runDir, filepath.FromSlash(path))
}

// stop 逆序停止 Paper 与控制面。
func (e *testEnv) stop() {
	if e.paper != nil {
		e.paper.Stop()
	}
	if e.cp != nil {
		e.cp.Stop()
	}
}
