//go:build integration

package service

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/store"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// archiveITNow 是集成测试固定时钟。
var archiveITNow = time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

// openArchiveITEnv 打开真实 MySQL 热库（beacon_archiveit）+ 同实例第二库（beacon_archiveit_arc）作归档库。
// 未设 BEACON_TEST_DSN 跳过。归档库经 store.OpenArchive 同实例模式（复用主库参数、替换库名）连接。
func openArchiveITEnv(t *testing.T) (*ArchiveService, *gorm.DB, *gorm.DB) {
	t.Helper()
	raw := os.Getenv("BEACON_TEST_DSN")
	if raw == "" {
		t.Skip("未设置 BEACON_TEST_DSN，跳过集成测试")
	}
	hot := testsupport.OpenTestDB(t, "archiveit")

	cfg, err := gomysql.ParseDSN(raw)
	if err != nil {
		t.Fatalf("解析 BEACON_TEST_DSN 失败: %v", err)
	}
	hotName := cfg.DBName + "_archiveit"
	archiveName := hotName + "_arc"
	admin, err := sql.Open("mysql", raw)
	if err != nil {
		t.Fatalf("打开基础连接失败: %v", err)
	}
	if _, err := admin.Exec("CREATE DATABASE IF NOT EXISTS `" + archiveName + "`"); err != nil {
		_ = admin.Close()
		t.Fatalf("预建归档库 %s 失败: %v", archiveName, err)
	}
	_ = admin.Close()

	cfg.DBName = hotName
	mainCfg := config.DatabaseConfig{
		Driver: "mysql", DSN: cfg.FormatDSN(), MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetimeSec: 300,
	}
	archive, info, err := store.OpenArchive(mainCfg, config.ArchiveConfig{DSN: "", Database: archiveName})
	if err != nil {
		t.Fatalf("连接同实例归档库失败: %v", err)
	}
	t.Cleanup(func() { store.Close(archive) })

	// 清任务表（跨 -count 迭代不残留活跃任务，避免单飞误判）。
	for _, tbl := range []string{"archive_job_item", "archive_job"} {
		if err := hot.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}

	settings, err := NewSettingsService(hot, repository.NewSettingRepository(hot), repository.NewAuditLogRepository(hot))
	if err != nil {
		t.Fatalf("装配设置服务失败: %v", err)
	}
	settings.mu.Lock()
	settings.cache[SettingArchiveBatchIntervalMs] = "0"
	settings.mu.Unlock()

	svc := NewArchiveService(hot, archive, info,
		repository.NewArchiveJobRepository(hot), settings, repository.NewAuditLogRepository(hot))
	svc.now = func() time.Time { return archiveITNow }
	return svc, hot, archive
}

func itTableCount(t *testing.T, db *gorm.DB, name string) int64 {
	t.Helper()
	var n int64
	if err := db.Table(name).Count(&n).Error; err != nil {
		t.Fatalf("统计 %s 失败: %v", name, err)
	}
	return n
}

// TestArchiveIntegrationExecuteJobMySQL 真 MySQL 双库端到端：metric 日表整表 + audit 单表区间归档、
// sha256 校验门通过后删热库；audit 归档库跨迭代累积、下界隔离仍正确（关键用例 -count=2）。
func TestArchiveIntegrationExecuteJobMySQL(t *testing.T) {
	svc, hot, archive := openArchiveITEnv(t)

	// metric 日表：清热 / 冷两侧旧表后播 3 行（30 天前，保留 14 天 → 到期）。
	metricDay := archiveITNow.AddDate(0, 0, -30)
	metricTable := store.DailyTableName("metric_sample", metricDay)
	_ = hot.Migrator().DropTable(metricTable)
	_ = archive.Migrator().DropTable(metricTable)
	if _, err := store.EnsureDailyTable(hot, &model.MetricSampleV2{}, metricDay); err != nil {
		t.Fatalf("建热库日表失败: %v", err)
	}
	metricRows := []model.MetricSampleV2{
		{NamespaceID: 1, ServerID: "s1", Kind: model.ServerKindBackend, BucketStartMs: metricDay.UnixMilli(), SampleCount: 5, TPSAvg: 20},
		{NamespaceID: 1, ServerID: "s1", Kind: model.ServerKindBackend, BucketStartMs: metricDay.Add(5 * time.Second).UnixMilli(), SampleCount: 5, TPSAvg: 19},
		{NamespaceID: 1, ServerID: "s2", Kind: model.ServerKindBackend, BucketStartMs: metricDay.UnixMilli(), SampleCount: 5, TPSAvg: 18},
	}
	if err := hot.Table(metricTable).Create(&metricRows).Error; err != nil {
		t.Fatalf("插入热库日表失败: %v", err)
	}

	// audit 单表：播 3 条到期行（200 天前，保留 180 天 → 到期），autoincrement id（跨迭代自增、天然隔离）。
	auditRows := []model.AuditLog{
		{Operator: "a", Action: "x", TargetType: "t", TargetRef: "r", Result: "ok", CreatedAt: archiveITNow.AddDate(0, 0, -200)},
		{Operator: "b", Action: "x", TargetType: "t", TargetRef: "r", Result: "ok", CreatedAt: archiveITNow.AddDate(0, 0, -199)},
		{Operator: "c", Action: "x", TargetType: "t", TargetRef: "r", Result: "ok", CreatedAt: archiveITNow.AddDate(0, 0, -198)},
	}
	if err := hot.Table("audit_log").Create(&auditRows).Error; err != nil {
		t.Fatalf("插入热库审计行失败: %v", err)
	}

	job, err := svc.CreateJob(model.ArchiveModeExecute, []string{"metric_sample", "audit"}, "admin")
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	svc.drainActive(context.Background())

	detail, err := svc.GetJob(job.ID)
	if err != nil {
		t.Fatalf("取详情失败: %v", err)
	}
	if detail.Status != model.ArchiveJobSucceeded {
		t.Fatalf("任务应 succeeded，实际 %s（error=%v）", detail.Status, detail.Error)
	}
	// metric：归档得 3 行、热库日表被删。
	if itTableCount(t, archive, metricTable) != 3 {
		t.Fatalf("归档 metric 日表应 3 行")
	}
	if hot.Migrator().HasTable(metricTable) {
		t.Fatalf("热库 metric 日表应被删")
	}
	// audit：热库到期行被删。
	var hotExpired int64
	if err := hot.Table("audit_log").Where("created_at < ?", archiveITNow.AddDate(0, 0, -180)).Count(&hotExpired).Error; err != nil {
		t.Fatalf("统计热库到期审计失败: %v", err)
	}
	if hotExpired != 0 {
		t.Fatalf("热库到期审计应清零，实际 %d", hotExpired)
	}
	// 每个非 skipped item 校验通过。
	for _, it := range detail.Items {
		if it.Phase == model.ArchiveItemDone && it.VerifyPassed != nil && !*it.VerifyPassed {
			t.Fatalf("item %s 校验未通过", it.TableName)
		}
	}
}

// TestArchiveIntegrationIdempotentReplayMySQL 真 MySQL 幂等：重放同批搬运 OnConflict 去重收敛、校验通过（-count=2）。
func TestArchiveIntegrationIdempotentReplayMySQL(t *testing.T) {
	_, hot, archive := openArchiveITEnv(t)
	day := archiveITNow.AddDate(0, 0, -40)
	name := store.DailyTableName("metric_sample", day)
	_ = hot.Migrator().DropTable(name)
	_ = archive.Migrator().DropTable(name)
	if _, err := store.EnsureDailyTable(hot, &model.MetricSampleV2{}, day); err != nil {
		t.Fatalf("建热库日表失败: %v", err)
	}
	rows := []model.MetricSampleV2{
		{NamespaceID: 1, ServerID: "s1", Kind: model.ServerKindBackend, BucketStartMs: day.UnixMilli(), SampleCount: 5, TPSAvg: 20},
		{NamespaceID: 1, ServerID: "s1", Kind: model.ServerKindBackend, BucketStartMs: day.Add(5 * time.Second).UnixMilli(), SampleCount: 5, TPSAvg: 19},
	}
	if err := hot.Table(name).Create(&rows).Error; err != nil {
		t.Fatalf("插入失败: %v", err)
	}

	r := &archiveItemRunner{
		hot: hot, archive: archive, dom: mustDomain(t, "metric_sample"), mode: model.ArchiveModeExecute,
		batchRows: 5, batchInterval: 0, sampleSize: 100,
		saveItem:  func(*model.ArchiveJobItem) error { return nil },
		cancelled: func() bool { return false },
	}
	item := &model.ArchiveJobItem{Domain: "metric_sample", TargetTable: name, Phase: model.ArchiveItemPending}
	if err := r.runCopy(item); err != nil {
		t.Fatalf("首次 copy 失败: %v", err)
	}
	item.Cursor = ""
	if err := r.runCopy(item); err != nil {
		t.Fatalf("重放 copy 失败: %v", err)
	}
	if got := itTableCount(t, archive, name); got != 2 {
		t.Fatalf("幂等重放归档应 2 行，实际 %d", got)
	}
	if err := r.runVerify(item); err != nil {
		t.Fatalf("重放后校验应通过: %v", err)
	}
	if item.VerifyPassed == nil || !*item.VerifyPassed {
		t.Fatalf("verify_passed 应为 true")
	}
}
