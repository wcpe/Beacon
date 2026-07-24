//go:build e2e

// FR-153 P6b「热冷归档管理面 API」真机端到端测试，纯 Go 原生 go test -tags=e2e（默认 SQLite，无需 docker/MySQL）。
//
// 不需真 agent / Paper：本用例只验控制面归档管理面（/admin/v2/archive/*）经真 HTTP + 真后台工作器的整链路。
// 用「双 sqlite 文件」模拟热 / 冷库——同实例模式下 store 自动把归档库派生为同目录第二个 beacon_archive.db。
//
// 编排相位：
//
//	seed     控制面启动前，向热库 sqlite 直写一张到期的 metric_sample 日表（60 天前，保留 14 天 → 到期）。
//	build    构建控制面二进制（SQLite），起临时控制面（后台归档工作器随之启动）。
//	overview GET overview 断目标库同实例、可达、7 域水位。
//	dry_run  建 dry_run 任务 → 轮询到 succeeded → 断 rows_expected=3、热库日表仍在（预览零删）。
//	execute  建 execute 任务 → 轮询到 succeeded → 断热库日表被删、冷库同名表落 3 行（搬运 + 校验门通过后删热库）。
//	list     列表 {items,total} + status/mode/trigger 过滤；详情 job + items 键齐。
//	guards   retry / cancel 状态守卫（succeeded 任务 retry/cancel → 409、不存在 → 404）。
//	rbac     readonly API 密钥对写端点 → 403。
//
// 铁律：只调既有 admin API + GORM 直读旁证，绝不旁路或弱化任一 FR-153 约束来「让断言通过」。
package archive_e2e

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

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	adminUser    = "admin"
	logPrefixCP  = "beacon-archive"
	sqliteDBName = "beacon-e2e-archive.db"
	// 归档库同实例 sqlite 派生名（store.deriveSQLiteArchiveDSN 用默认库名 beacon_archive）。
	archiveDBName = "beacon_archive.db"
	// 归档域：metric_sample 为日期后缀表，整表为归档单元、天然无半表状态，最适合确定性断言。
	archiveDomain = "metric_sample"
	// 造数条数（到期日表内 3 行）。
	seedRows = 3

	// 控制面就绪 + 后台工作器处理任务都极快（无 agent / gradle），给足 90s 到终态即可。
	terminalWait = 90 * time.Second
)

// beaconURL 控制面地址：默认 http://localhost:18850（可经 E2E_BEACON_URL 覆盖以避端口冲突）。
func beaconURL() string {
	if v := os.Getenv("E2E_BEACON_URL"); v != "" {
		return v
	}
	return "http://localhost:18850"
}

// overviewDomainResp 是 overview 域行中本用例关心的字段。
type overviewDomainResp struct {
	Domain        string `json:"domain"`
	RetentionDays int    `json:"retentionDays"`
	HotRows       int64  `json:"hotRows"`
	ArchiveRows   int64  `json:"archiveRows"`
	ExpiredRows   int64  `json:"expiredRows"`
}

// archiveOverviewResp 是 overview 响应中本用例关心的字段。
type archiveOverviewResp struct {
	Target struct {
		Mode      string `json:"mode"`
		Database  string `json:"database"`
		DSNMasked string `json:"dsnMasked"`
		Reachable bool   `json:"reachable"`
	} `json:"target"`
	Domains []overviewDomainResp `json:"domains"`
}

// archiveItemResp 是任务详情 items[] 中本用例关心的字段。
type archiveItemResp struct {
	Domain       string `json:"domain"`
	TableName    string `json:"tableName"`
	Phase        string `json:"phase"`
	RowsExpected int64  `json:"rowsExpected"`
	RowsCopied   int64  `json:"rowsCopied"`
	RowsDeleted  int64  `json:"rowsDeleted"`
	VerifyPassed *bool  `json:"verifyPassed"`
}

// archiveJobResp 是任务详情 / 创建响应。
type archiveJobResp struct {
	ID      uint              `json:"id"`
	Mode    string            `json:"mode"`
	Trigger string            `json:"trigger"`
	Status  string            `json:"status"`
	Items   []archiveItemResp `json:"items"`
}

// archiveListResp 是任务列表 {items,total} 响应。
type archiveListResp struct {
	Items []archiveJobResp `json:"items"`
	Total int64            `json:"total"`
}

// TestArchiveAdminAPIE2E 按相位编排 FR-153 归档管理面真机端到端。defer 收口杀控制面。
func TestArchiveAdminAPIE2E(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")
	base := beaconURL()

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}

	tmpDir := filepath.Join(repoRoot, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatalf("创建 .tmp 目录失败：%v", err)
	}
	sqliteDB := filepath.Join(tmpDir, sqliteDBName)
	coldDB := filepath.Join(tmpDir, archiveDBName)
	// 每轮从干净的热 / 冷两库开始（含 sqlite 边车文件）。
	for _, p := range []string{sqliteDB, coldDB} {
		removeSQLiteFiles(p)
	}

	// == 造数（控制面启动前直写热库到期日表）==
	metricTable := seedExpiredMetricTable(t, sqliteDB)
	t.Logf("已造到期热库日表 %s（%d 行）", metricTable, seedRows)

	t.Log("== 构建控制面二进制 ==")
	bin, err := harness.BuildBeacon(repoRoot)
	if err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}

	t.Log("== 起控制面（SQLite，同实例双库；后台归档工作器随启）==")
	cp, err := harness.StartControlPlane(harness.ControlPlaneConfig{
		BinPath: bin, RepoRoot: repoRoot, BaseURL: base,
		DBDriver: "sqlite", DBDSN: sqliteDB,
		AdminPassword: adminPass, AuthSecret: authSecret,
		BootstrapToken: "legacy-token-unused-by-archive",
		LogPrefix:      logPrefixCP,
	})
	if err != nil {
		t.Fatalf("起控制面失败：%v", err)
	}
	defer cp.Stop()

	adminToken, err := harness.Login(base, adminUser, adminPass)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}

	// 关每日自动归档，使本用例完全 hermetic（不与手动任务抢单飞）。
	doAdminJSON(t, base, http.MethodPut, "/admin/v1/settings/archive.auto-enabled", adminToken,
		map[string]any{"value": "false"}, http.StatusOK, nil)

	// == overview ==
	var ov archiveOverviewResp
	doAdminJSON(t, base, http.MethodGet, "/admin/v2/archive/overview", adminToken, nil, http.StatusOK, &ov)
	if ov.Target.Mode != store.ArchiveModeSameInstance || !ov.Target.Reachable {
		t.Fatalf("overview target 应同实例且可达，实际 %+v", ov.Target)
	}
	if len(ov.Domains) != 7 {
		t.Fatalf("overview 应 7 个归档域，实际 %d", len(ov.Domains))
	}
	if dm := findDomain(ov, archiveDomain); dm.ExpiredRows != seedRows || dm.HotRows < seedRows {
		t.Fatalf("overview %s 到期水位应 %d、热库行 ≥%d，实际 %+v", archiveDomain, seedRows, seedRows, dm)
	}
	t.Log("overview 断言通过（同实例可达 + 7 域 + 到期水位）")

	// == dry_run：预览零删 ==
	dryID := createJob(t, base, adminToken, "dry_run", []string{archiveDomain})
	dry := pollJobTerminal(t, base, adminToken, dryID)
	if dry.Status != model.ArchiveJobSucceeded {
		t.Fatalf("dry_run 应 succeeded，实际 %s", dry.Status)
	}
	dryItem := findItem(t, dry, metricTable)
	if dryItem.RowsExpected != seedRows || dryItem.RowsDeleted != 0 {
		t.Fatalf("dry_run item 应 rows_expected=%d 且零删，实际 %+v", seedRows, dryItem)
	}
	if !sqliteHasTable(t, sqliteDB, metricTable) {
		t.Fatalf("dry_run 后热库日表 %s 应仍在（预览零删）", metricTable)
	}
	t.Logf("dry_run 断言通过（rows_expected=%d、零删、热库表仍在）", seedRows)

	// == execute：搬运 + 校验门通过后删热库 ==
	execID := createJob(t, base, adminToken, "execute", []string{archiveDomain})
	exec := pollJobTerminal(t, base, adminToken, execID)
	if exec.Status != model.ArchiveJobSucceeded {
		t.Fatalf("execute 应 succeeded，实际 %s（item=%+v）", exec.Status, exec.Items)
	}
	execItem := findItem(t, exec, metricTable)
	if execItem.Phase != model.ArchiveItemDone {
		t.Fatalf("execute item 应 done，实际 %+v", execItem)
	}
	if sqliteHasTable(t, sqliteDB, metricTable) {
		t.Fatalf("execute 后热库日表 %s 应被删", metricTable)
	}
	if got := sqliteTableCount(t, coldDB, metricTable); got != seedRows {
		t.Fatalf("execute 后冷库 %s 应 %d 行，实际 %d", metricTable, seedRows, got)
	}
	t.Logf("execute 断言通过（热库表已删、冷库落 %d 行、item done）", seedRows)

	// == 列表 + 过滤 + 详情 ==
	var all archiveListResp
	doAdminJSON(t, base, http.MethodGet, "/admin/v2/archive/jobs", adminToken, nil, http.StatusOK, &all)
	if all.Total < 2 {
		t.Fatalf("列表应 ≥2 条（dry_run + execute），实际 %d", all.Total)
	}
	var succ archiveListResp
	doAdminJSON(t, base, http.MethodGet, "/admin/v2/archive/jobs?status=succeeded&mode=execute&trigger=manual",
		adminToken, nil, http.StatusOK, &succ)
	if succ.Total != 1 || succ.Items[0].ID != execID {
		t.Fatalf("过滤 succeeded+execute+manual 应仅命中 execute 任务，实际 %+v", succ)
	}
	t.Log("列表 + 过滤断言通过")

	// == retry / cancel 状态守卫 ==
	assertStatus(t, base, adminToken, http.MethodPost, "/admin/v2/archive/jobs/"+utoa(execID)+"/retry", http.StatusConflict)
	assertStatus(t, base, adminToken, http.MethodPost, "/admin/v2/archive/jobs/"+utoa(execID)+"/cancel", http.StatusConflict)
	assertStatus(t, base, adminToken, http.MethodPost, "/admin/v2/archive/jobs/999999/retry", http.StatusNotFound)
	assertStatus(t, base, adminToken, http.MethodPost, "/admin/v2/archive/jobs/999999/cancel", http.StatusNotFound)
	t.Log("retry / cancel 状态守卫断言通过（终态 409、不存在 404）")

	// == readonly 写端点 403 ==
	roToken := createReadonlyKey(t, base, adminToken)
	assertStatus(t, base, roToken, http.MethodPost, "/admin/v2/archive/jobs", http.StatusForbidden)
	t.Log("readonly 写端点 403 断言通过")
}

// ---- 造数 / 断言旁证（GORM 直读热 / 冷 sqlite）----

// seedExpiredMetricTable 控制面启动前向热库直写一张到期的 metric_sample 日表并返回表名。
func seedExpiredMetricTable(t *testing.T, sqliteDB string) string {
	t.Helper()
	hot, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: sqliteDB, MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开热库造数失败：%v", err)
	}
	defer store.Close(hot)

	// 60 天前（metric_sample 保留 14 天 → 远早于 cutoff，稳定到期）。
	day := time.Now().UTC().AddDate(0, 0, -60)
	table := store.DailyTableName(archiveDomain, day)
	if _, err := store.EnsureDailyTable(hot, &model.MetricSampleV2{}, day); err != nil {
		t.Fatalf("建热库日表失败：%v", err)
	}
	rows := []model.MetricSampleV2{
		{NamespaceID: 1, ServerID: "arc-s1", Kind: model.ServerKindBackend, BucketStartMs: day.UnixMilli(), SampleCount: 5, TPSAvg: 20},
		{NamespaceID: 1, ServerID: "arc-s1", Kind: model.ServerKindBackend, BucketStartMs: day.Add(5 * time.Second).UnixMilli(), SampleCount: 5, TPSAvg: 19},
		{NamespaceID: 1, ServerID: "arc-s2", Kind: model.ServerKindBackend, BucketStartMs: day.UnixMilli(), SampleCount: 5, TPSAvg: 18},
	}
	if err := hot.Table(table).Create(&rows).Error; err != nil {
		t.Fatalf("插入热库日表失败：%v", err)
	}
	return table
}

// sqliteHasTable 以独立只读句柄探测某 sqlite 文件是否含某表。
func sqliteHasTable(t *testing.T, dbFile, table string) bool {
	t.Helper()
	db := openReadDB(t, dbFile)
	sqlDB, _ := db.DB()
	defer func() { _ = sqlDB.Close() }()
	return db.Migrator().HasTable(table)
}

// sqliteTableCount 以独立只读句柄统计某 sqlite 文件某表行数。
func sqliteTableCount(t *testing.T, dbFile, table string) int64 {
	t.Helper()
	db := openReadDB(t, dbFile)
	sqlDB, _ := db.DB()
	defer func() { _ = sqlDB.Close() }()
	var n int64
	if err := db.Table(table).Count(&n).Error; err != nil {
		t.Fatalf("统计 %s.%s 失败：%v", dbFile, table, err)
	}
	return n
}

// openReadDB 打开一份独立 sqlite 句柄（带 busy_timeout 缓解与控制面同文件的读写锁竞争）。
func openReadDB(t *testing.T, dbFile string) *gorm.DB {
	t.Helper()
	db, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: dbFile + "?_busy_timeout=5000", MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开 sqlite %s 失败：%v", dbFile, err)
	}
	return db
}

// ---- admin API 编排助手 ----

// createJob 建归档任务，断 201，返回任务 id。
func createJob(t *testing.T, base, token, mode string, domains []string) uint {
	t.Helper()
	var job archiveJobResp
	doAdminJSON(t, base, http.MethodPost, "/admin/v2/archive/jobs", token,
		map[string]any{"mode": mode, "domains": domains}, http.StatusCreated, &job)
	if job.ID == 0 || job.Mode != mode {
		t.Fatalf("建 %s 任务响应异常：%+v", mode, job)
	}
	return job.ID
}

// pollJobTerminal 轮询任务详情直到终态（succeeded/failed/cancelled）。
func pollJobTerminal(t *testing.T, base, token string, id uint) archiveJobResp {
	t.Helper()
	deadline := time.Now().Add(terminalWait)
	var job archiveJobResp
	for time.Now().Before(deadline) {
		job = archiveJobResp{}
		doAdminJSON(t, base, http.MethodGet, "/admin/v2/archive/jobs/"+utoa(id), token, nil, http.StatusOK, &job)
		switch job.Status {
		case model.ArchiveJobSucceeded, model.ArchiveJobFailed, model.ArchiveJobCancelled:
			return job
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("任务 %d 未在 %s 内抵终态，最后状态 %s（见 .tmp/%s.out.log）", id, terminalWait, job.Status, logPrefixCP)
	return job
}

// createReadonlyKey 建一枚 readonly 管理密钥，返回其明文（作 Bearer）。
func createReadonlyKey(t *testing.T, base, token string) string {
	t.Helper()
	var resp struct {
		Key  string `json:"key"`
		Role string `json:"role"`
	}
	doAdminJSON(t, base, http.MethodPost, "/admin/v1/api-keys", token,
		map[string]any{"name": "archive-e2e-ro", "role": model.RoleReadonly}, http.StatusCreated, &resp)
	if resp.Key == "" || resp.Role != model.RoleReadonly {
		t.Fatalf("建 readonly 密钥响应异常：%+v", resp)
	}
	return resp.Key
}

// findDomain 从 overview 取某域行。
func findDomain(ov archiveOverviewResp, name string) overviewDomainResp {
	for _, d := range ov.Domains {
		if d.Domain == name {
			return d
		}
	}
	return ov.Domains[0]
}

// findItem 从任务详情取某表名的工作项。
func findItem(t *testing.T, job archiveJobResp, table string) archiveItemResp {
	t.Helper()
	for _, it := range job.Items {
		if it.TableName == table {
			return it
		}
	}
	t.Fatalf("任务 %d 未含表 %s 的工作项：%+v", job.ID, table, job.Items)
	return archiveItemResp{}
}

// assertStatus 发一次不带体的 admin 请求，只断状态码。
func assertStatus(t *testing.T, base, token, method, path string, want int) {
	t.Helper()
	req, err := http.NewRequest(method, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s 请求失败：%v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != want {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s 期望 HTTP %d，得 %d：%s", method, path, want, resp.StatusCode, string(raw))
	}
}

// ---- HTTP / 小工具 ----

// doAdminJSON 发一个带 Bearer 的 admin 请求，校验期望状态码，并（若 out 非 nil）解析响应体。失败即 fatal。
func doAdminJSON(t *testing.T, base, method, path, token string, body any, wantStatus int, out any) {
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s 请求失败：%v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s 期望 HTTP %d，得 %d：%s", method, path, wantStatus, resp.StatusCode, string(raw))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("解析 %s 响应失败：%v（%s）", path, err, string(raw))
		}
	}
}

// removeSQLiteFiles 删除 sqlite 主文件及其 -wal / -shm 边车文件（忽略不存在）。
func removeSQLiteFiles(path string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(path + suffix)
	}
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
