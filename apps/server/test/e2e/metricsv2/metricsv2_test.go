//go:build e2e

// FR-144 P4a「v2 指标采样入库」真机端到端测试，纯 Go 原生 go test -tags=e2e（默认 SQLite，无需 docker/MySQL）。
//
// 与 metrics 目录下 v1 看板 e2e 的本质区别：v1 是控制面拉取流、单表 metric_sample；本用例验证 v2 push 流：
// 真 agent 每 1s 采样 → 每 5s 批量 POST /beacon/v2/agent/metrics/report → 控制面异步入库到当日日表
// metric_sample_YYYYMMDD。只借用 harness 的构建/起停/登录能力，链路与断言完全不同。
//
// 编排相位：
//
//	build     构建控制面二进制（SQLite），起临时控制面。
//	namespace 经 admin 建 v2 namespace，取一次性 accessToken（作 agent 的 X-Beacon-Token）。
//	agent     经 gradle :agent-e2e:servePaper 起真 Paper + 真 BeaconAgent（agent-bukkit 壳默认已开启 v2 指标采样）。
//	approve   真 agent v2 注册进 pending → e2e 经 admin approve 使其 active（指标上报要求身份 active，否则 403）。
//	ingest    等 active + online 后，GORM 直读当日 metric_sample_YYYYMMDD，断出现带真实负载值（内存/TPS
//	          真实非零、CPU 为真实使用率或不可用哨兵）的 spec 合规行，且 server_id/kind 与真 agent 一致、
//	          bucket_start_ms 为 5s 对齐、sample_count 标称 1~5。
//
// headless Paper 无真实玩家，online_avg=0 属正常——只断内存/TPS 真值 + 链路通 + 落日表，不引假人。
//
// 铁律：只调既有 admin/agent API + GORM 直读，绝不旁路或弱化任一 FR-144 约束来「让断言通过」。
package metricsv2_e2e

import (
	"context"
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

// 服务端编排相关常量（与 :agent-e2e:servePaper 的默认约定一致）。
const (
	adminUser = "admin"
	// v2 namespace 名（同时用作 v1 instances 查询的 namespace code；两者同值，与 p1v2 一致）。
	namespace = "p4metrics"
	// 与 servePaper 默认 serverId 对齐，并通过进程环境显式注入以免默认漂移。
	serverID = "e2e-bukkit-1"
	// MC 监听端口：避让 25565(p1v2)/25566(metrics)，本用例用 25568。
	mcPort = "25568"

	logPrefixCP  = "beacon-metricsv2"
	logPrefixMC  = "paper-metricsv2"
	sqliteDBName = "beacon-e2e-metricsv2.db"

	// 首跑含 gradle 冷编译全 agent 模块 + 下载 Paper + agent 运行期依赖（TabooLib/okhttp/kotlinx）
	// 首次下载，耗时可观，给足 18 分钟到「pending 身份出现」（其后 active/online 仅数秒）。
	pendingWait = 18 * time.Minute
	// active 后衔接 legacy 数据面 online 仅数秒，给 6 分钟余量即可。
	onlineWait = 6 * time.Minute
	// approve+online 后等日表出现带真值的采样行：agent 每 5s 批上报、桶闭合后才上报，给足 4 分钟余量。
	ingestWait = 4 * time.Minute
)

// 控制面地址：默认 http://localhost:18850（避让 8848 生产 / 18848 前端 real / 18848 p1v2），可经 E2E_BEACON_URL 覆盖。
func beaconURL() string {
	if v := os.Getenv("E2E_BEACON_URL"); v != "" {
		return v
	}
	return "http://localhost:18850"
}

// namespaceView 是建 namespace 响应中本用例关心的字段（ID + 一次性明文 accessToken）。
type namespaceView struct {
	ID          uint   `json:"id"`
	AccessToken string `json:"accessToken"`
}

// TestMetricsV2IngestE2E 按相位编排 FR-144 采样入库真机端到端：
// build → namespace → agent(真 Paper) → approve → GORM 直读当日日表断真实采样行。defer 收口杀全部进程。
func TestMetricsV2IngestE2E(t *testing.T) {
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

	t.Log("== approve 使身份 active（指标上报要求 active，否则 403）==")
	approveIdentity(t, base, adminToken, identityID, paper)
	if _, err := harness.WaitIdentityStatus(base, adminToken, ns.ID, serverID, "active", pendingWait, paper); err != nil {
		t.Fatalf("等待 active 身份失败：%v", err)
	}
	if err := harness.WaitInstanceOnline(base, adminToken, namespace, serverID, onlineWait, paper); err != nil {
		t.Fatalf("active 后应衔接 legacy 数据面 online（见 .tmp/%s.out.log）：%v", logPrefixMC, err)
	}
	t.Log("agent 已 active + online")

	// 核心断言：等当日日表出现带真实采样值的行并逐字段校验。
	assertDailyIngest(t, sqliteDB, ingestWait, paper)
}

// assertDailyIngest 轮询当日 metric_sample_YYYYMMDD 直到出现「带真实负载值且 spec 合规」的代表行，
// 再对该服全部行做硬不变量校验，并把环境相关观测（CPU 不可用、启动期样本数抖动）记为日志、不误判失败。
//
// 「真实负载值」判据取内存(JVM 已用堆) + TPS(Paper)：二者为真实采样、非零、非占位，直接证明
// 采样 → 5s 批上报 → 异步入库整条链路把真值落进了日表。CPU 不入必过判据——某些宿主（如本机）
// getProcessCpuLoad 因 OS 性能计数器不可用恒返 -1（哨兵，agent 已按 fail-static 正确归一），
// 属环境限制而非链路缺陷；故对 CPU 只断「为真实使用率(>0) 或不可用哨兵(-1)」这一合法性。
func assertDailyIngest(t *testing.T, sqliteDB string, timeout time.Duration, guard *harness.GradleProc) {
	t.Helper()
	db := openE2EDB(t, sqliteDB)
	// 日表名按 bucket_start_ms 的 UTC 当日切分；agent 实时采样，桶落当日，故取「今日 UTC」表。
	dailyName := store.DailyTableName("metric_sample", time.Now().UTC())

	var rows []model.MetricSampleV2
	var chosen model.MetricSampleV2
	err := waitUntil(timeout, guard, func(context.Context) bool {
		// 首次写入前日表尚不存在，视作 0 行继续等（避免查不存在表报错）。
		if !db.Migrator().HasTable(dailyName) {
			return false
		}
		rows = nil
		if err := db.Table(dailyName).Where("server_id = ?", serverID).
			Order("bucket_start_ms").Find(&rows).Error; err != nil {
			t.Logf("轮询读日表 %s 失败（重试）：%v", dailyName, err)
			return false
		}
		for i := range rows {
			if isRepresentativeRow(rows[i]) {
				chosen = rows[i]
				return true
			}
		}
		return false
	})
	if err != nil {
		t.Fatalf("日表 %s 未在 %s 内出现带真实负载值(mem>0 且 tps>0)的 spec 合规采样行；当前该服行数=%d：%v",
			dailyName, timeout, len(rows), err)
	}

	// 代表行强断言：归属一致、真实负载、桶 5s 对齐、样本数标称 1~5、CPU 合法值。
	assertRowSound(t, chosen)

	// 全量行硬不变量：归属一致、桶 5s 对齐、样本数 1~5（spec §3.1，agent 侧已幂等启动 + 聚合封顶）、CPU 合法。
	cpuUnavail := 0
	for i := range rows {
		r := rows[i]
		if r.ServerID != serverID {
			t.Fatalf("行 server_id=%q 应为 %q", r.ServerID, serverID)
		}
		if r.Kind != model.ServerKindBackend {
			t.Fatalf("行 kind=%q 应为 %q（bukkit → backend）", r.Kind, model.ServerKindBackend)
		}
		if r.BucketStartMs <= 0 || r.BucketStartMs%5000 != 0 {
			t.Fatalf("行 bucket_start_ms=%d 应为正且 5s 对齐", r.BucketStartMs)
		}
		if r.SampleCount < 1 || r.SampleCount > 5 {
			t.Fatalf("行 sample_count=%d 应在 1~5（spec §3.1）", r.SampleCount)
		}
		if r.CPUPctAvg != -1 && r.CPUPctAvg <= 0 {
			t.Fatalf("行 cpu_pct_avg=%.4f 非法：应为真实使用率(>0) 或不可用哨兵(-1)", r.CPUPctAvg)
		}
		if r.CPUPctAvg == -1 {
			cpuUnavail++
		}
	}
	if cpuUnavail > 0 {
		// 本机 getProcessCpuLoad 因 OS 性能计数器不可用恒返 -1；agent 已正确归一为哨兵，链路无损。
		t.Logf("观测：%d/%d 行 cpu_pct_avg=-1（宿主 CPU 采集不可用，agent 已正确归一为哨兵）", cpuUnavail, len(rows))
	}

	t.Logf("PASS ingest：日表 %s 落 %s 的样本 %d 行；代表行 bucket=%d sampleCount=%d memAvg=%.2fMB memMax=%dMB tpsAvg=%.3f tpsMin=%.3f onlineAvg=%d cpuAvg=%.2f（headless 无真人 online=0 属正常）",
		dailyName, serverID, len(rows), chosen.BucketStartMs, chosen.SampleCount,
		chosen.MemUsedMbAvg, chosen.MemMaxMb, chosen.TPSAvg, chosen.TPSMin, chosen.OnlineAvg, chosen.CPUPctAvg)
}

// isRepresentativeRow 判定一行是否为「带真实负载值且 spec 合规」的代表行：
// 内存(JVM 已用堆) 与 TPS(Paper) 为真实非零值、桶 5s 对齐、样本数标称 1~5。
func isRepresentativeRow(r model.MetricSampleV2) bool {
	return r.MemUsedMbAvg > 0 && r.TPSAvg > 0 &&
		r.SampleCount >= 1 && r.SampleCount <= 5 &&
		r.BucketStartMs > 0 && r.BucketStartMs%5000 == 0
}

// assertRowSound 对代表行做强断言（归属 + 真实负载 + 桶对齐 + 样本数 + CPU 合法值）。
func assertRowSound(t *testing.T, r model.MetricSampleV2) {
	t.Helper()
	if r.ServerID != serverID {
		t.Fatalf("代表行 server_id=%q 应为 %q", r.ServerID, serverID)
	}
	if r.Kind != model.ServerKindBackend {
		t.Fatalf("代表行 kind=%q 应为 %q（bukkit → backend）", r.Kind, model.ServerKindBackend)
	}
	if r.MemUsedMbAvg <= 0 {
		t.Fatalf("代表行 mem_used_mb_avg=%.4f 应为真实非零（JVM 已用堆）", r.MemUsedMbAvg)
	}
	if r.TPSAvg <= 0 {
		t.Fatalf("代表行 tps_avg=%.4f 应为真实非零（Paper TPS）", r.TPSAvg)
	}
	if r.BucketStartMs <= 0 || r.BucketStartMs%5000 != 0 {
		t.Fatalf("代表行 bucket_start_ms=%d 应为正且 5s 对齐", r.BucketStartMs)
	}
	if r.SampleCount < 1 || r.SampleCount > 5 {
		t.Fatalf("代表行 sample_count=%d 应在标称 1~5", r.SampleCount)
	}
	if !(r.CPUPctAvg > 0 || r.CPUPctAvg == -1) {
		t.Fatalf("代表行 cpu_pct_avg=%.4f 非法：应为真实使用率(>0) 或不可用哨兵(-1)", r.CPUPctAvg)
	}
}

// ---- admin 编排助手 ----

// createNamespace 经 admin 建 v2 namespace 并取一次性明文 accessToken。
func createNamespace(t *testing.T, base, token string) namespaceView {
	t.Helper()
	id, accessToken, err := harness.CreateV2Namespace(base, token, namespace, "P4a v2 指标采样入库 e2e")
	if err != nil {
		t.Fatalf("建 namespace 失败：%v", err)
	}
	return namespaceView{ID: id, AccessToken: accessToken}
}

// approveIdentity 首次确认身份（无 target：确认但暂不分配区服），断响应状态 active。
func approveIdentity(t *testing.T, base, token, identityID string, guard *harness.GradleProc) {
	t.Helper()
	if err := harness.ApproveIdentityWithGuard(base, token, identityID, guard); err != nil {
		t.Fatalf("批准 identity 失败：%v", err)
	}
}

// ---- DB / 小工具 ----

// openE2EDB 按 sqlite 打开与控制面同一份数据（加 _busy_timeout 缓解共享文件写锁竞争）。
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

// itoa 把无符号整数转十进制字符串（避免为一处 uint 拼接引入 strconv 导入噪声）。
func itoa(v uint) string {
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
