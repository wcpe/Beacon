//go:build e2e && loadtest

// 「按行内时间戳定日表」的秒级取证（不等真实午夜即可覆盖白天与隔日的分流）。
//
// 为什么需要它（与 midnight_rollover_test.go 的分工）：
//   - midnight_rollover 真等墙钟走到午夜，代价 19 小时，验证的是「真实时间推进下的集成正确性」；
//   - 本用例不发真实时刻，而是**故意发一个时间戳落在次日的桶**，秒级验证同一条生产写入路径
//     是否按**行内时间戳**（bucketStartMs）选日表，而非按 time.Now()。
//
// 为什么后者抓得到前者抓不到的东西：若生产路径某处误用 time.Now() 决定目标日表，
// 那么「发一个明天的桶」会落进**今天**的表——本用例立刻报红；而真实午夜用例只有在午夜那一刻
// 才会显形（且要等 19 小时）。反过来，本用例证明不了「真实时钟走到午夜时的行为」，
// 两者互补而非互替。
//
// 已确认的前提（读码而非猜测）：
//   - 写路径 `MetricSampleV2Repository.FlushDaily` 按 `groupRowsByDay`（源自 row.BucketStartMs）
//     分组建表，不调 time.Now()；
//   - 上报校验 `validateClockSkew` 只看 `agentTimeMs`（省略即跳过），**不看 bucketStartMs**，
//     故可以合法发送未来桶。
//
// 运行：
//
//	export E2E_ADMIN_PASS='...' E2E_AUTH_SECRET='...'
//	go test -tags "e2e loadtest" -timeout=10m -v -run '^TestDailyTableRoutedByRowTimestamp$' ./apps/server/test/loadtest/

package loadtest_e2e

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/store"
	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	// routeByTsNamespace 独立 namespace，避免与其它用例抢同名空间。
	routeByTsNamespace = "loadtest-routets"
	// routeByTsDBName 独立库文件，避开长跑与其它用例。
	routeByTsDBName = "beacon-routets.db"
	// routeByTsSettle 是收尾等待：异步日表写入通道 500ms 攒批，落地需要一点时间。
	routeByTsSettle = 3 * time.Second
)

// TestDailyTableRoutedByRowTimestamp 秒级验证「日表由行内时间戳决定」。
//
// 手法：同一实例连发两个桶——一个落在**今天**、一个落在**次日**（两者都相对当前时刻构造），
// 然后核对两行各自落进与**自己时间戳**相符的日表。若生产路径误用 time.Now() 选表，
// 两个桶会全落进今天的表，用例报红。
func TestDailyTableRoutedByRowTimestamp(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	workDir := filepath.Join(repoRoot, ".tmp", "loadtest")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("创建压测工作目录失败：%v", err)
	}
	dbPath := filepath.Join(workDir, routeByTsDBName)
	for _, suffix := range []string{"", "-shm", "-wal"} {
		if err := os.Remove(dbPath + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("清理遗留库 %s%s 失败：%v", dbPath, suffix, err)
		}
	}

	binPath := filepath.Join(repoRoot, ".tmp", "beacon-routets")
	if err := buildBeaconTo(repoRoot, binPath); err != nil {
		t.Fatalf("%v", err)
	}

	cp, baseURL := startControlPlane(t, harness.ControlPlaneConfig{
		BinPath: binPath, RepoRoot: repoRoot,
		DBDriver: "sqlite", DBDSN: dbPath,
		AdminPassword: adminPass, AuthSecret: authSecret, BootstrapToken: bootstrapToken,
		LogPrefix: "beacon-routets",
		ExtraEnv:  map[string]string{"BEACON_LOG_LEVEL": envOr("LOADTEST_CP_LOG_LEVEL", defaultCPLogLevel)},
	}, controlPlaneStartAttempts)
	harness.CleanupControlPlane(t, cp)

	token, err := harness.Login(baseURL, adminUser, adminPass, cp)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	v2 := bootstrapV2Identity(t, baseURL, token, routeByTsNamespace, 1, cp)

	client := newLoadClient(4)
	defer client.CloseIdleConnections()
	probeDB := openMidnightProbeDB(t, dbPath)

	// 构造两个桶：一个属「今天」，一个属「次日」。
	// 用「下一个 UTC 午夜」为基准，避免依赖当前钟点（任何时刻运行都成立）。
	now := time.Now().UTC()
	nextMidnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	todayBucket := nextMidnight.Add(-2 * time.Hour).UnixMilli()  // 今天 22:00（若当前已过 22:00 仍是「今天」的表）
	nextDayBucket := nextMidnight.Add(1 * time.Hour).UnixMilli() // 次日 01:00
	tableToday := store.DailyTableName(metricSampleBaseTable, time.UnixMilli(todayBucket).UTC())
	tableNext := store.DailyTableName(metricSampleBaseTable, time.UnixMilli(nextDayBucket).UTC())

	if tableToday == tableNext {
		t.Fatalf("前置条件：两个桶应分属不同日表，实际同为 %s", tableToday)
	}
	t.Logf("现在 %s UTC", now.Format(time.RFC3339))
	t.Logf("构造两个桶：%s → 期望表 %s；%s → 期望表 %s",
		time.UnixMilli(todayBucket).UTC().Format(time.RFC3339), tableToday,
		time.UnixMilli(nextDayBucket).UTC().Format(time.RFC3339), tableNext)

	// 连发两个桶（同一实例、同一次会话）。
	for _, bucket := range []int64{todayBucket, nextDayBucket} {
		status, snippet, err := reportMetricBucket(context.Background(), client, baseURL, v2, bucket, cp)
		if err != nil {
			t.Fatalf("上报桶 %d 失败：%v", bucket, err)
		}
		if status != http.StatusAccepted {
			t.Fatalf("上报桶 %d 期望 202，实际 %d；片段：%s", bucket, status, snippet)
		}
	}
	time.Sleep(routeByTsSettle)

	// 核对：两个桶必须各自落进与自己时间戳相符的日表。
	assertBucketInTable(t, probeDB, tableToday, todayBucket)
	assertBucketInTable(t, probeDB, tableNext, nextDayBucket)

	// 交叉核对：今天的表不得含次日桶，反之亦然（防「两行都写进同一张表」的误判）。
	assertBucketNotInTable(t, probeDB, tableToday, nextDayBucket)
	assertBucketNotInTable(t, probeDB, tableNext, todayBucket)

	t.Logf("✅ 两个桶各自落进与自身时间戳相符的日表——日表路由由行内时间戳决定，未误用 time.Now()")
}

// assertBucketInTable 断言该 bucket 出现在指定日表中。
func assertBucketInTable(t *testing.T, db *gorm.DB, table string, bucket int64) {
	t.Helper()
	if !db.Migrator().HasTable(table) {
		t.Fatalf("日表 %s 不存在——时间戳 %d（%s）的行未按行内时间戳落表",
			table, bucket, time.UnixMilli(bucket).UTC().Format(time.RFC3339))
	}
	var n int64
	if err := db.Table(table).Where("bucket_start_ms = ?", bucket).Count(&n).Error; err != nil {
		t.Fatalf("查日表 %s 失败：%v", table, err)
	}
	if n != 1 {
		t.Fatalf("日表 %s 应有时间戳 %d（%s）的 1 行，实际 %d 行——"+
			"若为 0 说明该行被路由到了别的表（很可能按 time.Now() 选了今天）",
			table, bucket, time.UnixMilli(bucket).UTC().Format(time.RFC3339), n)
	}
}

// assertBucketNotInTable 断言该 bucket 不出现在指定日表中（防两行落同一表）。
func assertBucketNotInTable(t *testing.T, db *gorm.DB, table string, bucket int64) {
	t.Helper()
	if !db.Migrator().HasTable(table) {
		return // 表不存在即该行不在此表，符合预期
	}
	var n int64
	if err := db.Table(table).Where("bucket_start_ms = ?", bucket).Count(&n).Error; err != nil {
		t.Fatalf("查日表 %s 失败：%v", table, err)
	}
	if n != 0 {
		t.Fatalf("日表 %s 不应含时间戳 %d（%s）的行，实际 %d 行——串表",
			table, bucket, time.UnixMilli(bucket).UTC().Format(time.RFC3339), n)
	}
}
