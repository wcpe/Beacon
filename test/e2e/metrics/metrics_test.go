//go:build e2e

// FR-32 控制面可观测看板的真机端到端测试（ADR-0023），纯 Go 原生 go test -tags=e2e。
//
// 本测试自起服务端：先构建并起控制面（默认 SQLite、无需 docker/MySQL，且经 ExtraEnv 把采样间隔
// 调小到数秒以便快速积累趋势样本），再起真 Paper + BeaconAgent，等 agent online。随后：
//
//	summary  经 admin REST 建一条 global 层最小配置触发「配置 apply → agent 上报指标」（详见 runReport
//	         注释：agent 仅在配置 apply 时上报指标、心跳不带指标，故用发布配置作触发器）。轮询 summary
//	         端点直到目标子服在线、且上报了真 JVM 内存（avgMemMax>0）。
//	trend    等采样器跑若干轮后，查 trend 端点断时间序列非空、字段为真值。
//	persist  经 GORM 直读 metric_sample 表，断采样器确已落样本（count>0）。
//	boundary 搬集成测试的 assertNoRosterFields 范式，断 summary/trend 响应不含任何玩家名单 / 身份字段
//	         （守 ADR-0023 边界：只指标不名单）。
//
// headless Paper 无真实玩家，playerCount=0 属正常——只断「内存/TPS 真值 + 链路通 + 落样本」，不引假人。
//
// 铁律：本测试只调既有 admin/agent API + GORM 直读，绝不旁路或弱化任一 FR-32 / ADR-0023 约束来「让断言通过」。
package metrics_e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"beacon/internal/merge"
	"beacon/internal/model"
	"beacon/test/e2e/harness"
)

// 服务端编排相关常量（与 runServer gradle 任务的约定一致）。
const (
	beaconURL    = "http://localhost:8848"
	adminUser    = "admin"
	serverID     = "e2e-bukkit-1"
	mcPort       = "25566"
	namespace    = "prod"
	bootstrap    = "beacon-bootstrap-2026"
	onlineWait   = 12 * time.Minute // 首跑含下载 Paper + 构建 jar，给足时间
	logPrefixCP  = "beacon-metrics"
	logPrefixMC  = "paper"
	sqliteDBName = "beacon-e2e-metrics.db"

	// 采样间隔（秒）：调小到 2s，让趋势在测试窗口内快速积累多个样本（默认 30s 太慢）。
	sampleIntervalSec = "2"
)

// 触发上报用的最小配置（global 层，namespace=prod → 覆盖到该环境全部在线实例，含 e2e-bukkit-1）。
const (
	triggerDataID  = "beacon-e2e-metrics"
	triggerFormat  = merge.FormatYAML
	triggerContent = "marker: e2e-metrics-trigger\n"
)

// TestMetricsE2E 按相位顺序编排整套 FR-32 看板真机端到端：构建 → 起控制面(短采样间隔) →
// 起 Paper → 等 agent online → 触发上报 → summary → trend → persist → boundary。defer 收口杀全部进程。
func TestMetricsE2E(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")

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

	t.Log("== 起控制面（SQLite，采样间隔调小到 " + sampleIntervalSec + "s）==")
	cp, err := harness.StartControlPlane(harness.ControlPlaneConfig{
		BinPath: bin, RepoRoot: repoRoot, BaseURL: beaconURL,
		DBDriver: "sqlite", DBDSN: sqliteDB,
		AdminPassword: adminPass, AuthSecret: authSecret, BootstrapToken: bootstrap,
		LogPrefix: logPrefixCP,
		// 调小采样间隔，让趋势样本快速积累（不影响既有两个 e2e 调用：它们不传 ExtraEnv）。
		ExtraEnv: map[string]string{"BEACON_METRIC_SAMPLE_INTERVAL_SEC": sampleIntervalSec},
	})
	if err != nil {
		t.Fatalf("起控制面失败：%v", err)
	}
	defer cp.Stop()

	t.Log("== 起 Paper 子服（" + mcPort + "）==")
	paper, err := harness.StartGradleTask(repoRoot, ":agent-e2e:runServer", []string{"-Pe2eMcPort=" + mcPort}, logPrefixMC)
	if err != nil {
		t.Fatalf("起 Paper 失败：%v", err)
	}
	defer paper.Stop()

	t.Log("== 等 agent online（首跑含下载/构建，耐心等）==")
	token, err := harness.Login(beaconURL, adminUser, adminPass)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	if err := harness.WaitInstanceOnline(beaconURL, token, namespace, serverID, onlineWait); err != nil {
		t.Fatalf("等 %s online 超时（见 .tmp/paper.out.log）：%v", serverID, err)
	}
	t.Log("agent 已 online")

	// 触发一次「配置 apply → 上报指标」，使注册表里 e2e-bukkit-1 带上真 JVM 负载（供 summary 出真值）。
	runReport(t, token)

	t.Run("summary", func(t *testing.T) { runSummary(t, token) })
	t.Run("trend", func(t *testing.T) { runTrend(t, token) })
	t.Run("persist", func(t *testing.T) { runPersist(t, sqliteDB) })
	t.Run("boundary", func(t *testing.T) { runBoundary(t, token) })
}

// runReport 触发一次 agent 指标上报：经 admin REST 建一条 global 层最小配置。
//
// 为何用「发布配置」做触发器：agent 仅在配置 apply（pollEffective 返回 Changed）时调 report 携带指标，
// 心跳不带指标（见 AgentLifecycle.reportApplied 仅由 pollTick 的 Changed 分支调用）。建 global 层配置
// 会经长轮询唤醒下发到该 namespace 全部在线实例 → agent apply → 上报真 JVM 负载到注册表，供 summary 取真值。
func runReport(t *testing.T, token string) {
	body := map[string]any{
		"namespace": namespace, "group": model.GlobalGroupCode, "dataId": triggerDataID,
		"scopeLevel": model.ScopeGlobal, "scopeTarget": "",
		"format": triggerFormat, "content": triggerContent, "comment": "e2e 指标上报触发器",
	}
	doAdmin(t, http.MethodPost, "/admin/v1/configs", token, body, http.StatusCreated, nil)
	t.Logf("已建 global 层触发配置 dataId=%s（应经长轮询下发触发 agent apply→report）", triggerDataID)
}

// runSummary 轮询 summary 端点，断在线服含目标子服且上报了真 JVM 内存（avgMemMax>0）。
func runSummary(t *testing.T, token string) {
	var last summaryView
	if !waitUntil(40*time.Second, func() bool {
		last = getSummary(t, token)
		if last.OnlineServers < 1 || last.AvgMemMax <= 0 {
			return false
		}
		for _, s := range last.Servers {
			if s.ServerID == serverID {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("summary：超时未观测到目标子服 %s 在线且上报真 JVM 内存（avgMemMax>0）；当前=%+v", serverID, last)
	}
	t.Logf("PASS summary：在线服=%d，含 %s，avgMemMax=%d 字节（真 JVM 最大堆），totalPlayers=%d（headless 无真人属正常）",
		last.OnlineServers, serverID, last.AvgMemMax, last.TotalPlayers)
}

// runTrend 等采样器跑若干轮后，断趋势端点返回非空时间序列、字段为真值（avgMemMax>0）。
func runTrend(t *testing.T, token string) {
	var points []trendPointView
	// 采样间隔 2s，等 ~8 轮足够积累多个样本（给足余量到 25s）。
	if !waitUntil(25*time.Second, func() bool {
		points = getTrend(t, token)
		for _, p := range points {
			if p.AvgMemMax > 0 {
				return true // 至少一个点带真 JVM 内存，证明采样器把真值落进了趋势
			}
		}
		return false
	}) {
		t.Fatalf("trend：超时未观测到带真值（avgMemMax>0）的趋势点；当前点数=%d", len(points))
	}
	t.Logf("PASS trend：趋势序列 %d 点，含真 JVM 内存样本（采样器已把负载真值落进历史趋势）", len(points))
}

// runPersist 经 GORM 直读 metric_sample 表，断采样器确已落样本（count>0）。
func runPersist(t *testing.T, sqliteDB string) {
	db := openE2EDB(t, sqliteDB)
	var count int64
	if err := db.Model(&model.MetricSample{}).
		Where("namespace = ? AND server_id = ?", namespace, serverID).
		Count(&count).Error; err != nil {
		t.Fatalf("persist：查 metric_sample 失败：%v", err)
	}
	if count <= 0 {
		t.Fatalf("persist：采样器应已为 %s 落样本到 metric_sample，实际 count=%d", serverID, count)
	}
	t.Logf("PASS persist：metric_sample 已落 %s 的样本 %d 条（采样落库链路成立）", serverID, count)
}

// runBoundary 断 summary/trend 原始响应均不含玩家名单 / 身份字段（守 ADR-0023 边界：只指标不名单）。
func runBoundary(t *testing.T, token string) {
	assertNoRosterFields(t, getRaw(t, http.MethodGet, "/admin/v1/metrics/summary?namespace="+namespace, token))
	assertNoRosterFields(t, getRaw(t, http.MethodGet, "/admin/v1/metrics/trend?namespace="+namespace+"&window=1h", token))
	t.Log("PASS boundary：summary/trend 响应均不含玩家名单 / 身份字段（只指标不名单成立）")
}

// ---- summary / trend 端点视图（仅取断言所需字段）----

// serverPlayersView 是 summary 里每服明细（仅计数，无名单）。
type serverPlayersView struct {
	ServerID    string `json:"serverId"`
	PlayerCount int    `json:"playerCount"`
}

// summaryView 是 summary 端点对外视图（取断言所需字段）。
type summaryView struct {
	TotalPlayers  int                 `json:"totalPlayers"`
	OnlineServers int                 `json:"onlineServers"`
	Servers       []serverPlayersView `json:"servers"`
	AvgMemMax     int64               `json:"avgMemMax"`
}

// trendPointView 是 trend 端点的时间序列点（取断言所需字段）。
type trendPointView struct {
	AvgMemMax int64 `json:"avgMemMax"`
}

// getSummary 取 summary 端点并解析为视图。
func getSummary(t *testing.T, token string) summaryView {
	var out summaryView
	doAdmin(t, http.MethodGet, "/admin/v1/metrics/summary?namespace="+namespace, token, nil, http.StatusOK, &out)
	return out
}

// getTrend 取 trend 端点（近 1h 窗）并解析出时间序列点。
func getTrend(t *testing.T, token string) []trendPointView {
	var out struct {
		Points []trendPointView `json:"points"`
	}
	doAdmin(t, http.MethodGet, "/admin/v1/metrics/trend?namespace="+namespace+"&window=1h", token, nil, http.StatusOK, &out)
	return out.Points
}

// ---- 边界守护 ----

// assertNoRosterFields 断响应体不含任何玩家名单 / 身份字段（与集成测试 metric_integration_test.go 同范式）。
func assertNoRosterFields(t *testing.T, body map[string]any) {
	t.Helper()
	banned := []string{"players", "roster", "playerNames", "names", "uuids", "playerList"}
	for _, k := range banned {
		if _, ok := body[k]; ok {
			t.Fatalf("响应体不得含玩家名单 / 身份字段 %q（越界），实际 %v", k, body)
		}
	}
}

// ---- 数据库直读 ----

// openE2EDB 按 sqlite 打开与控制面同一份数据（加 _busy_timeout 缓解共享文件写锁竞争）。
func openE2EDB(t *testing.T, sqliteDB string) *gorm.DB {
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

// ---- HTTP 工具 ----

// doAdmin 发一个带 Bearer 的 admin 请求，校验期望状态码，并（若 out 非 nil）解析响应体。失败即 fatal。
func doAdmin(t *testing.T, method, path, token string, body any, wantStatus int, out any) {
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, beaconURL+path, reader)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求 %s %s 失败：%v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s 期望 HTTP %d，得 %d：%s", method, path, wantStatus, resp.StatusCode, string(raw))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("解析 %s 响应失败：%v（%s）", path, err, string(raw))
		}
	}
}

// getRaw 取一个 admin GET 的原始 JSON 对象（供边界守护逐键检查名单字段）。
func getRaw(t *testing.T, method, path, token string) map[string]any {
	var out map[string]any
	doAdmin(t, method, path, token, nil, http.StatusOK, &out)
	return out
}

// ---- 小工具 ----

func waitUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(1 * time.Second)
	}
	return cond()
}

// requireEnv 取必填 env，缺失即 t.Skip（让普通 go test ./... 不因缺密钥失败）。
func requireEnv(t *testing.T, key string) string {
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("跳过：缺少必需环境变量 %s（仅在显式 -tags=e2e 且注入密钥时运行）", key)
	}
	return v
}
