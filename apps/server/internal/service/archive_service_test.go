package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// archiveTestNow 是服务测试固定时钟（cutoff 确定）。
var archiveTestNow = time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

// newArchiveTestService 装配一个热库（store.Open 全量迁移）+ 归档库（按需建表）+ 固定时钟的归档服务。
func newArchiveTestService(t *testing.T) (*ArchiveService, *gorm.DB, *gorm.DB) {
	t.Helper()
	hot, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: "file:archsvc_" + t.Name() + "_hot?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开热库失败: %v", err)
	}
	t.Cleanup(func() { store.Close(hot) })
	archive := openArchiveTestDB(t, "arc")

	settings, err := NewSettingsService(hot, repository.NewSettingRepository(hot), repository.NewAuditLogRepository(hot))
	if err != nil {
		t.Fatalf("装配设置服务失败: %v", err)
	}
	// 批间歇置 0，避免测试等待。
	settings.mu.Lock()
	settings.cache[SettingArchiveBatchIntervalMs] = "0"
	settings.mu.Unlock()

	svc := NewArchiveService(hot, archive,
		store.ArchiveInfo{Mode: store.ArchiveModeSameInstance, Database: "beacon_archive", DSNMasked: "sqlite"},
		repository.NewArchiveJobRepository(hot), settings, repository.NewAuditLogRepository(hot))
	svc.now = func() time.Time { return archiveTestNow }
	return svc, hot, archive
}

func archiveAuditCount(t *testing.T, db *gorm.DB, action string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.AuditLog{}).Where("action = ?", action).Count(&n).Error; err != nil {
		t.Fatalf("统计审计 %s 失败: %v", action, err)
	}
	return n
}

// TestServiceExecuteJobArchivesAndDropsHot execute 任务端到端：数据落归档、热库日表被删、任务 succeeded、审计留痕。
func TestServiceExecuteJobArchivesAndDropsHot(t *testing.T) {
	svc, hot, archive := newArchiveTestService(t)
	oldDay := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) // < cutoff(06-06, 保留 14 天)
	name := seedMetricDailyTable(t, hot, oldDay, 3)

	job, err := svc.CreateJob(model.ArchiveModeExecute, []string{"metric_sample"}, "admin")
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	svc.drainActive(context.Background())

	detail, err := svc.GetJob(job.ID)
	if err != nil {
		t.Fatalf("取任务详情失败: %v", err)
	}
	if detail.Status != model.ArchiveJobSucceeded {
		t.Fatalf("任务应 succeeded，实际 %s（error=%v）", detail.Status, detail.Error)
	}
	if len(detail.Items) != 1 || detail.Items[0].Phase != model.ArchiveItemDone {
		t.Fatalf("应 1 个 done item，实际 %+v", detail.Items)
	}
	if tableCount(t, archive, name) != 3 {
		t.Fatalf("归档表应 3 行")
	}
	if hot.Migrator().HasTable(name) {
		t.Fatalf("热库日表应被删")
	}
	if archiveAuditCount(t, hot, model.ActionArchiveJobCreate) != 1 {
		t.Fatalf("应有 1 条创建审计")
	}
	if archiveAuditCount(t, hot, model.ActionArchiveJobComplete) != 1 {
		t.Fatalf("应有 1 条完成审计")
	}
}

// TestServiceSingleFlight 单飞：已有活跃任务时再创建返回 409。
func TestServiceSingleFlight(t *testing.T) {
	svc, hot, _ := newArchiveTestService(t)
	seedMetricDailyTable(t, hot, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), 1)
	if _, err := svc.CreateJob(model.ArchiveModeExecute, []string{"metric_sample"}, "admin"); err != nil {
		t.Fatalf("首个任务创建失败: %v", err)
	}
	_, err := svc.CreateJob(model.ArchiveModeDryRun, []string{"metric_sample"}, "admin")
	if !errors.Is(err, apperr.ErrArchiveJobRunning) {
		t.Fatalf("并发创建应返回 ArchiveJobRunning，实际 %v", err)
	}
}

// TestServiceDryRunZeroEffect dry_run 任务：只统计 rows_expected，零写归档、零删热库。
func TestServiceDryRunZeroEffect(t *testing.T) {
	svc, hot, archive := newArchiveTestService(t)
	oldDay := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	name := seedMetricDailyTable(t, hot, oldDay, 3)

	job, err := svc.CreateJob(model.ArchiveModeDryRun, []string{"metric_sample"}, "admin")
	if err != nil {
		t.Fatalf("创建 dry_run 失败: %v", err)
	}
	svc.drainActive(context.Background())
	detail, _ := svc.GetJob(job.ID)
	if detail.Status != model.ArchiveJobSucceeded {
		t.Fatalf("dry_run 应 succeeded，实际 %s", detail.Status)
	}
	if detail.Items[0].RowsExpected != 3 || detail.Items[0].RowsCopied != 0 {
		t.Fatalf("dry_run 应只统计 rows_expected=3、rows_copied=0，实际 %+v", detail.Items[0])
	}
	if archive.Migrator().HasTable(name) {
		t.Fatalf("dry_run 不应写归档")
	}
	if !hot.Migrator().HasTable(name) {
		t.Fatalf("dry_run 不应删热库")
	}
}

// TestServiceCutoffsSnapshotNoDrift cutoffs 创建时快照：创建后改保留期，任务仍按快照 cutoff 执行、不漂移。
func TestServiceCutoffsSnapshotNoDrift(t *testing.T) {
	svc, hot, _ := newArchiveTestService(t)
	oldDay := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) // 保留 14 天 → cutoff 06-06 → 到期
	seedMetricDailyTable(t, hot, oldDay, 3)

	job, err := svc.CreateJob(model.ArchiveModeDryRun, []string{"metric_sample"}, "admin")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	// 创建后把保留期热改为 60 天（cutoff 04-21 → 该表将不再到期）；快照应不受影响。
	svc.settings.mu.Lock()
	svc.settings.cache[SettingArchiveRetentionMetricSample] = "60"
	svc.settings.mu.Unlock()

	svc.drainActive(context.Background())
	detail, _ := svc.GetJob(job.ID)
	// 若用快照(14)→表到期→rows_expected=3；若误用热改后(60)→表未到期→skipped/0。
	if detail.Items[0].RowsExpected != 3 {
		t.Fatalf("应按创建时快照 cutoff 计到期 3 行，实际 phase=%s rows_expected=%d",
			detail.Items[0].Phase, detail.Items[0].RowsExpected)
	}
}

// TestServiceExpandSkippedWhenNoExpired 无到期数据的域生成 skipped item（预览「无事可做」）。
func TestServiceExpandSkippedWhenNoExpired(t *testing.T) {
	svc, hot, _ := newArchiveTestService(t)
	// 仅一张「未到期」的近日表（06-19，保留 14 天 cutoff 06-06，未到期）。
	seedMetricDailyTable(t, hot, time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC), 2)
	job, err := svc.CreateJob(model.ArchiveModeDryRun, []string{"metric_sample"}, "admin")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	svc.drainActive(context.Background())
	detail, _ := svc.GetJob(job.ID)
	if len(detail.Items) != 1 || detail.Items[0].Phase != model.ArchiveItemSkipped {
		t.Fatalf("无到期数据应生成 skipped item，实际 %+v", detail.Items)
	}
}

// TestServiceRetrySkipsDoneItem 断点续跑：done item 跳过、failed item 从头续，任务转 succeeded。
func TestServiceRetrySkipsDoneItem(t *testing.T) {
	svc, hot, archive := newArchiveTestService(t)
	dayB := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	tableB := seedMetricDailyTable(t, hot, dayB, 3) // failed item 待续跑的表

	// 直接构造一个 failed 任务：item A 已 done（无表）、item B failed（待续）。
	now := archiveTestNow
	job := &model.ArchiveJob{Mode: model.ArchiveModeExecute, Trigger: model.ArchiveTriggerManual,
		Status: model.ArchiveJobFailed, Domains: `["metric_sample"]`, Operator: "admin",
		CreatedAt: now, StartedAt: &now, FinishedAt: &now}
	if err := svc.repo.CreateJob(job); err != nil {
		t.Fatalf("造 failed 任务失败: %v", err)
	}
	items := []model.ArchiveJobItem{
		{JobID: job.ID, Domain: "metric_sample", TargetTable: "metric_sample_20260601", Phase: model.ArchiveItemDone, RowsCopied: 5, RowsDeleted: 5},
		{JobID: job.ID, Domain: "metric_sample", TargetTable: tableB, Phase: model.ArchiveItemFailed},
	}
	if err := svc.repo.CreateItems(items); err != nil {
		t.Fatalf("造 items 失败: %v", err)
	}

	if _, err := svc.RetryJob(job.ID, "admin"); err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	svc.drainActive(context.Background())
	detail, _ := svc.GetJob(job.ID)
	if detail.Status != model.ArchiveJobSucceeded {
		t.Fatalf("重试后应 succeeded，实际 %s（error=%v）", detail.Status, detail.Error)
	}
	var doneA, doneB ArchiveJobItemView
	for _, it := range detail.Items {
		if it.TableName == "metric_sample_20260601" {
			doneA = it
		} else {
			doneB = it
		}
	}
	if doneA.RowsCopied != 5 { // 未被重新处理（保持原值）
		t.Fatalf("done item 不应被重处理，rows_copied 变为 %d", doneA.RowsCopied)
	}
	if doneB.Phase != model.ArchiveItemDone || doneB.RowsCopied != 3 {
		t.Fatalf("failed item 应续跑到 done、搬运 3 行，实际 %+v", doneB)
	}
	if tableCount(t, archive, tableB) != 3 {
		t.Fatalf("续跑后归档表 B 应 3 行")
	}
	if archiveAuditCount(t, hot, model.ActionArchiveJobRetry) != 1 {
		t.Fatalf("应有 1 条重试审计")
	}
}

// TestServiceCancelPending 取消 pending 任务：直接置 cancelled。
func TestServiceCancelPending(t *testing.T) {
	svc, hot, _ := newArchiveTestService(t)
	seedMetricDailyTable(t, hot, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), 1)
	job, err := svc.CreateJob(model.ArchiveModeExecute, []string{"metric_sample"}, "admin")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	detail, err := svc.CancelJob(job.ID, "admin")
	if err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	if detail.Status != model.ArchiveJobCancelled {
		t.Fatalf("pending 取消应直接 cancelled，实际 %s", detail.Status)
	}
	if archiveAuditCount(t, hot, model.ActionArchiveJobCancel) != 1 {
		t.Fatalf("应有 1 条取消审计")
	}
}

// TestServiceCancelRunningBatchBoundary 取消 running 任务：worker 在批次边界侦测 cancelling → 收尾 cancelled、不处理 item。
func TestServiceCancelRunningBatchBoundary(t *testing.T) {
	svc, hot, _ := newArchiveTestService(t)
	tableName := seedMetricDailyTable(t, hot, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), 3)
	// 造一个已 running 且被请求取消（cancelling）的任务 + 一个 pending item。
	now := archiveTestNow
	job := &model.ArchiveJob{Mode: model.ArchiveModeExecute, Trigger: model.ArchiveTriggerManual,
		Status: model.ArchiveJobCancelling, Domains: `["metric_sample"]`, Operator: "admin",
		CreatedAt: now, StartedAt: &now}
	if err := svc.repo.CreateJob(job); err != nil {
		t.Fatalf("造 cancelling 任务失败: %v", err)
	}
	item := model.ArchiveJobItem{JobID: job.ID, Domain: "metric_sample", TargetTable: tableName, Phase: model.ArchiveItemPending}
	if err := svc.repo.CreateItems([]model.ArchiveJobItem{item}); err != nil {
		t.Fatalf("造 item 失败: %v", err)
	}
	svc.drainActive(context.Background())
	detail, _ := svc.GetJob(job.ID)
	if detail.Status != model.ArchiveJobCancelled {
		t.Fatalf("批次边界应收尾为 cancelled，实际 %s", detail.Status)
	}
	if detail.Items[0].Phase != model.ArchiveItemPending {
		t.Fatalf("取消时未处理的 item 应保持 pending，实际 %s", detail.Items[0].Phase)
	}
	// 热库数据两侧保留（未删）。
	if !hot.Migrator().HasTable(tableName) {
		t.Fatalf("取消不应删热库")
	}
}

// TestServiceOverview 总览：目标可达 + 各域保留期 / 热库体量 / 到期量。
func TestServiceOverview(t *testing.T) {
	svc, hot, _ := newArchiveTestService(t)
	seedMetricDailyTable(t, hot, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), 3) // 到期
	ov, err := svc.Overview()
	if err != nil {
		t.Fatalf("总览失败: %v", err)
	}
	if !ov.Target.Reachable || ov.Target.Mode != store.ArchiveModeSameInstance {
		t.Fatalf("目标应可达且 same-instance，实际 %+v", ov.Target)
	}
	if len(ov.Domains) != len(archiveDomains) {
		t.Fatalf("应含全部 %d 域，实际 %d", len(archiveDomains), len(ov.Domains))
	}
	var metric *ArchiveDomainOverviewView
	for i := range ov.Domains {
		if ov.Domains[i].Domain == "metric_sample" {
			metric = &ov.Domains[i]
		}
	}
	if metric == nil {
		t.Fatalf("总览缺 metric_sample 域")
	}
	if metric.RetentionDays != archiveDefaultRetentionMetricSample {
		t.Fatalf("metric_sample 保留期应 %d，实际 %d", archiveDefaultRetentionMetricSample, metric.RetentionDays)
	}
	if metric.HotRows != 3 || metric.ExpiredRows != 3 {
		t.Fatalf("metric_sample 热库 / 到期应各 3 行，实际 hot=%d expired=%d", metric.HotRows, metric.ExpiredRows)
	}
}

// TestServiceUnreachableDegrades 归档库不可达：archiveDB=nil 时 overview 标不可用、创建被拒、Run 立即返回。
func TestServiceUnreachableDegrades(t *testing.T) {
	hot, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: "file:archsvc_" + t.Name() + "_hot?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开热库失败: %v", err)
	}
	t.Cleanup(func() { store.Close(hot) })
	settings, _ := NewSettingsService(hot, repository.NewSettingRepository(hot), repository.NewAuditLogRepository(hot))
	svc := NewArchiveService(hot, nil,
		store.ArchiveInfo{Mode: store.ArchiveModeSameInstance, Database: "beacon_archive", DSNMasked: "sqlite"},
		repository.NewArchiveJobRepository(hot), settings, repository.NewAuditLogRepository(hot))

	if _, err := svc.CreateJob(model.ArchiveModeExecute, nil, "admin"); !errors.Is(err, apperr.ErrArchiveUnavailable) {
		t.Fatalf("不可达应拒绝创建，实际 %v", err)
	}
	ov, err := svc.Overview()
	if err != nil {
		t.Fatalf("总览失败: %v", err)
	}
	if ov.Target.Reachable {
		t.Fatalf("不可达时 reachable 应为 false")
	}
	// Run 应立即返回（不启动搬运循环）。
	done := make(chan struct{})
	go func() { svc.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("不可达时 Run 应立即返回")
	}
}

// TestArchiveRetentionGuardBelow7 保留期下限守卫：<7 被设置校验拒绝、=7 通过。
func TestArchiveRetentionGuardBelow7(t *testing.T) {
	svc, _ := newTestSettingsService(t)
	if err := svc.Update(SettingArchiveRetentionMetricSample, "6", "admin", "127.0.0.1"); !errors.Is(err, apperr.ErrSettingValueInvalid) {
		t.Fatalf("保留期 6(<7) 应被拒，实际 %v", err)
	}
	if err := svc.Update(SettingArchiveRetentionMetricSample, "7", "admin", "127.0.0.1"); err != nil {
		t.Fatalf("保留期 7 应通过，实际 %v", err)
	}
}
