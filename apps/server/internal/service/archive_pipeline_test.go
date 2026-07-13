package service

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// openArchiveTestDB 打开一个独立内存 sqlite（单连接、shared-cache），供热 / 冷库双库测试。
func openArchiveTestDB(t *testing.T, tag string) *gorm.DB {
	t.Helper()
	dsn := "file:archpipe_" + t.Name() + "_" + tag + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
		NowFunc:        func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite(%s) 失败: %v", tag, err)
	}
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// mustDomain 取域描述（测试断言用）。
func mustDomain(t *testing.T, name string) archiveDomain {
	t.Helper()
	d, ok := archiveDomainByName(name)
	if !ok {
		t.Fatalf("域 %s 不在注册表", name)
	}
	return d
}

// seedMetricDailyTable 在 db 建 metric_sample 当日表并插入 count 行（id 1..count），返回表名。
func seedMetricDailyTable(t *testing.T, db *gorm.DB, day time.Time, count int) string {
	t.Helper()
	name := store.DailyTableName("metric_sample", day)
	if _, err := store.EnsureDailyTable(db, &model.MetricSampleV2{}, day); err != nil {
		t.Fatalf("建日表失败: %v", err)
	}
	rows := make([]model.MetricSampleV2, 0, count)
	for i := 0; i < count; i++ {
		rows = append(rows, model.MetricSampleV2{
			NamespaceID: 1, ServerID: "s1", Kind: model.ServerKindBackend,
			BucketStartMs: day.Add(time.Duration(i) * 5 * time.Second).UnixMilli(),
			SampleCount:   5, TPSAvg: 20, CPUPctAvg: float64(i),
		})
	}
	if err := db.Table(name).Create(&rows).Error; err != nil {
		t.Fatalf("插入日表数据失败: %v", err)
	}
	return name
}

func newTestRunner(hot, archive *gorm.DB, dom archiveDomain, mode string, batchRows int) *archiveItemRunner {
	return &archiveItemRunner{
		hot: hot, archive: archive, dom: dom, mode: mode,
		batchRows: batchRows, batchInterval: 0, sampleSize: 100,
		saveItem:  func(*model.ArchiveJobItem) error { return nil },
		cancelled: func() bool { return false },
	}
}

func tableCount(t *testing.T, db *gorm.DB, name string) int64 {
	t.Helper()
	var n int64
	if err := db.Table(name).Count(&n).Error; err != nil {
		t.Fatalf("统计 %s 失败: %v", name, err)
	}
	return n
}

// TestPipelineDailyHappyPath 日表整表：copy → verify（通过）→ delete（整表 DropTable）→ done。
func TestPipelineDailyHappyPath(t *testing.T) {
	hot := openArchiveTestDB(t, "hot")
	archive := openArchiveTestDB(t, "arc")
	day := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	name := seedMetricDailyTable(t, hot, day, 3)

	r := newTestRunner(hot, archive, mustDomain(t, "metric_sample"), model.ArchiveModeExecute, 2)
	item := &model.ArchiveJobItem{Domain: "metric_sample", TargetTable: name, Phase: model.ArchiveItemPending}
	if err := r.run(item); err != nil {
		t.Fatalf("流水线执行失败: %v", err)
	}
	if item.Phase != model.ArchiveItemDone {
		t.Fatalf("终态应为 done，实际 %s", item.Phase)
	}
	if item.RowsCopied != 3 {
		t.Fatalf("应搬运 3 行，实际 %d", item.RowsCopied)
	}
	if item.VerifyPassed == nil || !*item.VerifyPassed {
		t.Fatalf("校验应通过，实际 %+v", item.VerifyPassed)
	}
	if item.RowsDeleted != 3 {
		t.Fatalf("应删除 3 行，实际 %d", item.RowsDeleted)
	}
	if tableCount(t, archive, name) != 3 {
		t.Fatalf("归档表应 3 行")
	}
	if hot.Migrator().HasTable(name) {
		t.Fatalf("热库日表应已被 DropTable")
	}
}

// TestPipelineCopyIdempotentReplay 幂等：重放同批搬运（模拟崩溃重跑），OnConflict 去重不产生重复、校验仍通过。
func TestPipelineCopyIdempotentReplay(t *testing.T) {
	hot := openArchiveTestDB(t, "hot")
	archive := openArchiveTestDB(t, "arc")
	day := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	name := seedMetricDailyTable(t, hot, day, 3)

	r := newTestRunner(hot, archive, mustDomain(t, "metric_sample"), model.ArchiveModeExecute, 5)
	item := &model.ArchiveJobItem{Domain: "metric_sample", TargetTable: name, Phase: model.ArchiveItemPending}
	// 首次搬运。
	if err := r.runCopy(item); err != nil {
		t.Fatalf("首次 copy 失败: %v", err)
	}
	// 模拟崩溃重跑：cursor 清零从头重搬，OnConflict DoNothing 全部去重。
	item.Cursor = ""
	if err := r.runCopy(item); err != nil {
		t.Fatalf("重放 copy 失败: %v", err)
	}
	if got := tableCount(t, archive, name); got != 3 {
		t.Fatalf("幂等重放归档表应恰 3 行（无重复），实际 %d", got)
	}
	// 重放后归档 = 热库精确副本，校验应通过。
	if err := r.runVerify(item); err != nil {
		t.Fatalf("重放后校验应通过: %v", err)
	}
	if item.VerifyPassed == nil || !*item.VerifyPassed {
		t.Fatalf("重放后 verify_passed 应为 true")
	}
}

// TestPipelineResumeFromCursor 断点续跑：cursor 已到 1，仅搬运 2、3。
func TestPipelineResumeFromCursor(t *testing.T) {
	hot := openArchiveTestDB(t, "hot")
	archive := openArchiveTestDB(t, "arc")
	day := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	name := seedMetricDailyTable(t, hot, day, 3)

	// 归档已含 id=1（上次搬运的第一批）。
	if err := ensureArchiveTable(archive, name, &model.MetricSampleV2{}); err != nil {
		t.Fatalf("建归档表失败: %v", err)
	}
	pre := model.MetricSampleV2{ID: 1, NamespaceID: 1, ServerID: "s1", Kind: model.ServerKindBackend,
		BucketStartMs: day.UnixMilli(), SampleCount: 5, TPSAvg: 20}
	if err := archive.Table(name).Create(&pre).Error; err != nil {
		t.Fatalf("预置归档行失败: %v", err)
	}

	r := newTestRunner(hot, archive, mustDomain(t, "metric_sample"), model.ArchiveModeExecute, 5)
	// 从 copying 阶段、cursor=1 续起。
	item := &model.ArchiveJobItem{Domain: "metric_sample", TargetTable: name, Phase: model.ArchiveItemCopying, Cursor: "1", RowsCopied: 1}
	if err := r.runCopy(item); err != nil {
		t.Fatalf("续跑 copy 失败: %v", err)
	}
	if item.RowsCopied != 3 { // 1（已有）+ 2、3
		t.Fatalf("续跑后累计搬运应为 3，实际 %d", item.RowsCopied)
	}
	if got := tableCount(t, archive, name); got != 3 {
		t.Fatalf("归档表应 3 行，实际 %d", got)
	}
	if item.Cursor != "3" {
		t.Fatalf("cursor 应推进到 3，实际 %s", item.Cursor)
	}
}

// TestPipelineVerifyFailRowCountKeepsHot 校验门：归档行数少于热库时判 failed，绝不删热库。
func TestPipelineVerifyFailRowCountKeepsHot(t *testing.T) {
	hot := openArchiveTestDB(t, "hot")
	archive := openArchiveTestDB(t, "arc")
	day := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	name := seedMetricDailyTable(t, hot, day, 3)
	dom := mustDomain(t, "metric_sample")
	r := newTestRunner(hot, archive, dom, model.ArchiveModeExecute, 5)
	item := &model.ArchiveJobItem{Domain: "metric_sample", TargetTable: name, Phase: model.ArchiveItemPending}

	if err := r.runCopy(item); err != nil {
		t.Fatalf("copy 失败: %v", err)
	}
	// 篡改：删归档一行，制造行数不一致。
	if err := archive.Exec("DELETE FROM " + name + " WHERE id = 2").Error; err != nil {
		t.Fatalf("篡改归档失败: %v", err)
	}
	err := r.runVerify(item)
	if err == nil {
		t.Fatalf("行数不一致应校验失败")
	}
	if item.VerifyPassed == nil || *item.VerifyPassed {
		t.Fatalf("verify_passed 应为 false")
	}
	// 绝不删热库。
	if !hot.Migrator().HasTable(name) || tableCount(t, hot, name) != 3 {
		t.Fatalf("校验失败时热库数据必须保留完整")
	}
}

// TestPipelineVerifyFailHashKeepsHot 校验门：行数相等但抽样内容被篡改时哈希不一致 → failed，不删热库。
func TestPipelineVerifyFailHashKeepsHot(t *testing.T) {
	hot := openArchiveTestDB(t, "hot")
	archive := openArchiveTestDB(t, "arc")
	day := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	name := seedMetricDailyTable(t, hot, day, 3)
	dom := mustDomain(t, "metric_sample")
	r := newTestRunner(hot, archive, dom, model.ArchiveModeExecute, 5)
	item := &model.ArchiveJobItem{Domain: "metric_sample", TargetTable: name, Phase: model.ArchiveItemPending}
	if err := r.runCopy(item); err != nil {
		t.Fatalf("copy 失败: %v", err)
	}
	// 篡改归档某行内容（行数不变、内容变），使抽样哈希不一致。
	if err := archive.Exec("UPDATE " + name + " SET tps_avg = 999 WHERE id = 2").Error; err != nil {
		t.Fatalf("篡改归档失败: %v", err)
	}
	if err := r.runVerify(item); err == nil {
		t.Fatalf("哈希不一致应校验失败")
	}
	if item.VerifyPassed == nil || *item.VerifyPassed {
		t.Fatalf("verify_passed 应为 false")
	}
	if !hot.Migrator().HasTable(name) {
		t.Fatalf("校验失败时热库日表必须保留")
	}
}

// TestPipelineDryRunZeroWriteDelete dry_run 只统计 rows_expected，零写归档、零删热库。
func TestPipelineDryRunZeroWriteDelete(t *testing.T) {
	hot := openArchiveTestDB(t, "hot")
	archive := openArchiveTestDB(t, "arc")
	day := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	name := seedMetricDailyTable(t, hot, day, 3)
	r := newTestRunner(hot, archive, mustDomain(t, "metric_sample"), model.ArchiveModeDryRun, 5)
	item := &model.ArchiveJobItem{Domain: "metric_sample", TargetTable: name, Phase: model.ArchiveItemPending}
	if err := r.run(item); err != nil {
		t.Fatalf("dry_run 失败: %v", err)
	}
	if item.RowsExpected != 3 {
		t.Fatalf("rows_expected 应为 3，实际 %d", item.RowsExpected)
	}
	if item.RowsCopied != 0 || item.RowsDeleted != 0 {
		t.Fatalf("dry_run 不应搬运 / 删除")
	}
	if archive.Migrator().HasTable(name) {
		t.Fatalf("dry_run 不应建 / 写归档表")
	}
	if !hot.Migrator().HasTable(name) {
		t.Fatalf("dry_run 不应删热库")
	}
}

// TestPipelineSingleTableArchiveWithAccumulationIsolation 单表 audit：区间归档 + 归档库累积历史行时的下界隔离。
// 归档库已含更早一次归档的旧行（id 小、created_at 更早、已从热库删）；本次校验须以热侧最小主键为下界，
// 排除该旧行，行数才匹配、校验通过并删除本次区间。
func TestPipelineSingleTableArchiveWithAccumulationIsolation(t *testing.T) {
	hot := openArchiveTestDB(t, "hot")
	archive := openArchiveTestDB(t, "arc")
	if err := hot.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatalf("热库 audit_log 迁移失败: %v", err)
	}
	if err := ensureArchiveTable(archive, "audit_log", &model.AuditLog{}); err != nil {
		t.Fatalf("归档 audit_log 建表失败: %v", err)
	}
	cutoff := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// 归档库预置一条更早已归档的旧行（id=1，早于 cutoff 且早于本次热侧行；已不在热库）。
	old := model.AuditLog{ID: 1, Operator: "sys", Action: "x", TargetType: "t", TargetRef: "r",
		Result: "ok", CreatedAt: cutoff.AddDate(0, 0, -100)}
	if err := archive.Table("audit_log").Create(&old).Error; err != nil {
		t.Fatalf("预置归档旧行失败: %v", err)
	}

	// 热库：到期行 id=10/11/12（created_at < cutoff）+ 未到期行 id=13/14（>= cutoff）。
	hotRows := []model.AuditLog{
		{ID: 10, Operator: "a", Action: "x", TargetType: "t", TargetRef: "r", Result: "ok", CreatedAt: cutoff.AddDate(0, 0, -3)},
		{ID: 11, Operator: "b", Action: "x", TargetType: "t", TargetRef: "r", Result: "ok", CreatedAt: cutoff.AddDate(0, 0, -2)},
		{ID: 12, Operator: "c", Action: "x", TargetType: "t", TargetRef: "r", Result: "ok", CreatedAt: cutoff.AddDate(0, 0, -1)},
		{ID: 13, Operator: "d", Action: "x", TargetType: "t", TargetRef: "r", Result: "ok", CreatedAt: cutoff.Add(time.Hour)},
		{ID: 14, Operator: "e", Action: "x", TargetType: "t", TargetRef: "r", Result: "ok", CreatedAt: cutoff.Add(2 * time.Hour)},
	}
	if err := hot.Table("audit_log").Create(&hotRows).Error; err != nil {
		t.Fatalf("插入热库审计行失败: %v", err)
	}

	r := newTestRunner(hot, archive, mustDomain(t, "audit"), model.ArchiveModeExecute, 2)
	rangeTo := cutoff
	item := &model.ArchiveJobItem{Domain: "audit", TargetTable: "audit_log", RangeTo: &rangeTo, Phase: model.ArchiveItemPending}
	if err := r.run(item); err != nil {
		t.Fatalf("单表归档流水线失败: %v", err)
	}
	if item.Phase != model.ArchiveItemDone {
		t.Fatalf("终态应 done，实际 %s（error=%s）", item.Phase, item.Error)
	}
	if item.VerifyPassed == nil || !*item.VerifyPassed {
		t.Fatalf("下界隔离后校验应通过")
	}
	if item.RowsDeleted != 3 {
		t.Fatalf("应删除 3 条到期行，实际 %d", item.RowsDeleted)
	}
	// 归档库 = 旧行 + 本次 3 行 = 4；热库剩未到期 2 行。
	if got := tableCount(t, archive, "audit_log"); got != 4 {
		t.Fatalf("归档 audit_log 应 4 行（1 旧 + 3 新），实际 %d", got)
	}
	if got := tableCount(t, hot, "audit_log"); got != 2 {
		t.Fatalf("热库 audit_log 应剩 2 未到期行，实际 %d", got)
	}
}
