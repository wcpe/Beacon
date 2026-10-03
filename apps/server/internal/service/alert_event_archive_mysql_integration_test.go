//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// 归档域 alert_event 的 MySQL 复验锚点：与单测同语义，但时间函数固定为集成用例时钟 archiveITNow
// （2026-06-20）。保留期默认 180 天 → cutoff = 2025-12-22，故 01-01 的行「到期」、06-15 的行「保留期内」。
var (
	alertMySQLOldAt    = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	alertMySQLRecentAt = time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
)

// TestAlertEventArchiveOnlyResolvedMySQL 在真 MySQL 上复验「只归档已处理行」：extraWhere
// `status = 'resolved'` 必须在 MySQL 上同样只让已处理（resolved）到期行落冷库并从热库删除，
// 未处理（open / acknowledged）行与保留期内的行一律留在热库（FR-89 留痕表纳入归档域的核心语义）。
func TestAlertEventArchiveOnlyResolvedMySQL(t *testing.T) {
	svc, hot, archive := openArchiveITEnv(t)

	// 冷库同名表可能是前次运行的残留：先清掉，保证行数断言只反映本次搬运。
	_ = archive.Migrator().DropTable("alert_event")

	openOld := seedAlertEventRow(t, hot, model.AlertEventStatusOpen, alertMySQLOldAt)
	ackOld := seedAlertEventRow(t, hot, model.AlertEventStatusAcknowledged, alertMySQLOldAt)
	resolvedOldA := seedAlertEventRow(t, hot, model.AlertEventStatusResolved, alertMySQLOldAt)
	resolvedOldB := seedAlertEventRow(t, hot, model.AlertEventStatusResolved, alertMySQLOldAt)
	resolvedRecent := seedAlertEventRow(t, hot, model.AlertEventStatusResolved, alertMySQLRecentAt)
	openRecent := seedAlertEventRow(t, hot, model.AlertEventStatusOpen, alertMySQLRecentAt)
	allIDs := []uint{openOld, ackOld, resolvedOldA, resolvedOldB, resolvedRecent, openRecent}

	// dry-run：MySQL 上的预估计数必须只含「到期 + 已处理」的 2 行，且零副作用。
	dryJob, err := svc.CreateJob(model.ArchiveModeDryRun, []string{"alert_event"}, "admin")
	if err != nil {
		t.Fatalf("创建 dry-run 任务失败: %v", err)
	}
	svc.drainActive(context.Background())
	dryItem := fetchArchiveItem(t, hot, dryJob.ID, "alert_event")
	if dryItem.RowsExpected != 2 || dryItem.RowsCopied != 0 || dryItem.RowsDeleted != 0 {
		t.Fatalf("MySQL dry-run 应只预估 2 行（到期且已处理）、零搬运零删除，实际 %+v", dryItem)
	}
	if cold := alertRowStatusByID(t, archive, allIDs); len(cold) != 0 {
		t.Fatalf("MySQL dry-run 不得向归档库写入数据，实际 %d 行", len(cold))
	}
	if got := alertRowStatusByID(t, hot, allIDs); len(got) != len(allIDs) {
		t.Fatalf("MySQL dry-run 不得删热库行，实际剩 %d / 6", len(got))
	}

	// execute：实际搬运 / 删除量必须与 dry-run 预估一致（同为 2 行），并校验通过。
	execJob, err := svc.CreateJob(model.ArchiveModeExecute, []string{"alert_event"}, "admin")
	if err != nil {
		t.Fatalf("创建 execute 任务失败: %v", err)
	}
	svc.drainActive(context.Background())
	execItem := fetchArchiveItem(t, hot, execJob.ID, "alert_event")
	if execItem.Phase != model.ArchiveItemDone {
		t.Fatalf("MySQL execute 项应为 done，实际 %q（错误 %q）", execItem.Phase, execItem.Error)
	}
	if execItem.RowsCopied != dryItem.RowsExpected || execItem.RowsDeleted != dryItem.RowsExpected {
		t.Fatalf("MySQL execute 实际搬运 / 删除应与预估一致（%d），实际 copied=%d deleted=%d",
			dryItem.RowsExpected, execItem.RowsCopied, execItem.RowsDeleted)
	}
	if execItem.VerifyPassed == nil || !*execItem.VerifyPassed {
		t.Fatalf("MySQL execute 项应校验通过，实际 %v", execItem.VerifyPassed)
	}

	// 冷库：只有两条 resolved 旧行，状态原样。
	cold := alertRowStatusByID(t, archive, allIDs)
	if len(cold) != 2 {
		t.Fatalf("MySQL 归档库应只有 2 行（已处理的到期行），实际 %d：%v", len(cold), cold)
	}
	if cold[resolvedOldA] != model.AlertEventStatusResolved || cold[resolvedOldB] != model.AlertEventStatusResolved {
		t.Fatalf("MySQL 归档库应为已处理行且状态原样，实际 %v", cold)
	}

	// 热库：未处理行与保留期内行必须原样保留（「待办不消失」的关键断言）。
	warm := alertRowStatusByID(t, hot, allIDs)
	if len(warm) != 4 {
		t.Fatalf("MySQL 热库应保留 4 行（2 未处理旧行 + 2 保留期内行），实际 %d：%v", len(warm), warm)
	}
	if warm[openOld] != model.AlertEventStatusOpen || warm[openRecent] != model.AlertEventStatusOpen {
		t.Fatalf("未处理旧告警必须留在热库且状态不变，实际 %v", warm)
	}
	if warm[ackOld] != model.AlertEventStatusAcknowledged {
		t.Fatalf("已确认（acknowledged）旧告警属未完成待办、必须留在热库，实际 %v", warm)
	}
	if warm[resolvedRecent] != model.AlertEventStatusResolved {
		t.Fatalf("保留期内的已处理行不得被搬走（cutoff 生效），实际 %v", warm)
	}
	for _, gone := range []uint{resolvedOldA, resolvedOldB} {
		if _, ok := warm[gone]; ok {
			t.Fatalf("已搬运的到期已处理行应从热库删除，实际仍在：id=%d", gone)
		}
	}

	// 原生 SQL 直证谓词：热库未处理行 3 条、热库「到期且已处理」行 0 条（已全部搬走）。
	var hotUnhandled int64
	if err := hot.Table("alert_event").Where("status <> ?", model.AlertEventStatusResolved).Count(&hotUnhandled).Error; err != nil {
		t.Fatalf("统计热库未处理告警失败: %v", err)
	}
	if hotUnhandled != 3 {
		t.Fatalf("敏感：MySQL 热库未处理告警应保留 3 行（open 2 + acknowledged 1），实际 %d", hotUnhandled)
	}
	var hotExpiredResolved int64
	if err := hot.Table("alert_event").
		Where("status = ?", model.AlertEventStatusResolved).
		Where("created_at < ?", cutoffFor(archiveITNow, archiveDefaultRetentionAlertEvent)).
		Count(&hotExpiredResolved).Error; err != nil {
		t.Fatalf("统计热库到期已处理告警失败: %v", err)
	}
	if hotExpiredResolved != 0 {
		t.Fatalf("MySQL 热库「到期且已处理」行应被搬净，实际剩 %d", hotExpiredResolved)
	}
}
