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
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// seedMCPInvocationDailyTable 建一张指定日的 mcp_invocation 日表并插入 count 行（归档域测试用）。
func seedMCPInvocationDailyTable(t *testing.T, db *gorm.DB, day time.Time, count int) string {
	t.Helper()
	name := store.DailyTableName("mcp_invocation", day)
	if _, err := store.EnsureDailyTable(db, &model.MCPInvocation{}, day); err != nil {
		t.Fatalf("建日表失败: %v", err)
	}
	rows := make([]model.MCPInvocation, 0, count)
	for i := 0; i < count; i++ {
		ms := day.Add(time.Duration(i) * time.Second).UnixMilli()
		rows = append(rows, model.MCPInvocation{
			InvocationID: store.NewUUIDv7(ms), ClientID: "c1", Profile: model.MCPClientProfileAutomation,
			ToolName: "beacon.audit.events.list", RiskLevel: model.MCPInvocationRiskLow,
			Result: model.MCPInvocationResultOK, CreatedAt: time.UnixMilli(ms).UTC(),
		})
	}
	if err := db.Table(name).Create(&rows).Error; err != nil {
		t.Fatalf("插入日表数据失败: %v", err)
	}
	return name
}

// TestMCPInvocationArchiveDomainRegistered 校验归档域登记齐备（spec §3.8）：
// 日表形态、UUIDv7 字符串主键（invocation_id）、保留期键指向本 FR 的专用键。
func TestMCPInvocationArchiveDomainRegistered(t *testing.T) {
	dom, ok := archiveDomainByName("mcp_invocation")
	if !ok {
		t.Fatalf("mcp_invocation 未登记为归档域（漏登记会让到期日表永不归档）")
	}
	if dom.baseTable != "mcp_invocation" || dom.form != archiveFormDaily {
		t.Fatalf("归档域形态不符: %+v", dom)
	}
	if dom.pkColumn != "invocation_id" || dom.pkKind != archivePKString {
		t.Fatalf("归档域主键不符（应为 UUIDv7 字符串）: %+v", dom)
	}
	if dom.retentionKey != SettingArchiveRetentionMCPInvocation {
		t.Fatalf("保留期键未指向 %s: %+v", SettingArchiveRetentionMCPInvocation, dom)
	}
	if dom.timeColumn != "" {
		t.Fatalf("daily 形态不应有单表时间列: %q", dom.timeColumn)
	}
	if dom.newModel == nil || dom.newModel().(*model.MCPInvocation) == nil {
		t.Fatalf("归档域缺 newModel（无法在归档库建同构表）")
	}
	if !isValidArchiveDomain("mcp_invocation") {
		t.Fatalf("isValidArchiveDomain 未认可 mcp_invocation")
	}
}

// TestMCPInvocationRetentionSettingRegistered 校验保留期设置键四项同步（spec §3.8；缺一即 fail-open）：
// 键常量、默认值 180、白名单元数据（下限 7 / 上限 3650）、以及**必须**进 dangerousSettingKeys。
func TestMCPInvocationRetentionSettingRegistered(t *testing.T) {
	const key = SettingArchiveRetentionMCPInvocation
	if key != "archive.retention-days.mcp-invocation" {
		t.Fatalf("保留期键字面量不符: %q", key)
	}
	meta, ok := settingMetaFor(key)
	if !ok {
		t.Fatalf("保留期键未登记进 settingsWhitelist（写该键会被拒）")
	}
	if meta.valueType != model.SettingValueTypeInt {
		t.Fatalf("保留期键类型应为 int，实际 %q", meta.valueType)
	}
	if meta.min != archiveMinRetentionDays || meta.max != 3650 {
		t.Fatalf("保留期上下界应为 [%d,3650]，实际 [%d,%d]", archiveMinRetentionDays, meta.min, meta.max)
	}
	if got := meta.defaultFromConfig(config.Default()); got != "180" {
		t.Fatalf("保留期默认值应为 180，实际 %q", got)
	}
	// §5 第 10 条：漏登记 dangerousSettingKeys 会让保留期改动绕过危险设置审批（安全回归）。
	if !SettingDangerous(key) {
		t.Fatalf("%s 未登记进 dangerousSettingKeys —— 保留期改动会绕过危险设置审批流程", key)
	}
}

// TestMCPInvocationRetentionGuard 保留期下限守卫：<7 被设置校验拒绝、=7 通过（复用既有守卫）。
func TestMCPInvocationRetentionGuard(t *testing.T) {
	svc, db := newTestSettingsService(t)
	row := model.Setting{Key: SettingArchiveRetentionMCPInvocation, Value: "180", ValueType: model.SettingValueTypeInt, Version: 1}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("写入初始保留期失败: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := svc.applyDangerousInTx(tx, SettingArchiveRetentionMCPInvocation, "6", row.Version, "admin", "127.0.0.1")
		return err
	}); !errors.Is(err, apperr.ErrSettingValueInvalid) {
		t.Fatalf("保留期 6(<7) 应被拒，实际 %v", err)
	}
	var after func()
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		after, err = svc.applyDangerousInTx(tx, SettingArchiveRetentionMCPInvocation, "7", row.Version, "admin", "127.0.0.1")
		return err
	}); err != nil {
		t.Fatalf("保留期 7 应通过，实际 %v", err)
	}
	if after != nil {
		after()
	}
}

// TestMCPInvocationArchiveDryRunAndExecute 端到端验证归档闭环（spec §3.8 / §5 第 10 条）：
// 构造到期 mcp_invocation 日表 → dry-run 能枚举到该表且不留痕 → execute 后校验通过才删热库、数据落归档库。
func TestMCPInvocationArchiveDryRunAndExecute(t *testing.T) {
	svc, hot, archive := newArchiveTestService(t)
	// 保留期默认 180 天（spec §3.8），故造一张 200 天前的日表保证落在 cutoff 之前。
	oldDay := archiveTestNow.AddDate(0, 0, -200)
	table := seedMCPInvocationDailyTable(t, hot, oldDay, 3)

	dryJob, err := svc.CreateJob(model.ArchiveModeDryRun, []string{"mcp_invocation"}, "admin")
	if err != nil {
		t.Fatalf("创建 dry-run 任务失败: %v", err)
	}
	svc.drainActive(context.Background())
	// dry-run 只填 rows_expected、不搬运不删除。
	dryItem := fetchArchiveItem(t, hot, dryJob.ID, table)
	if dryItem.RowsExpected != 3 {
		t.Fatalf("dry-run 应枚举到 3 行到期数据，实际 %d", dryItem.RowsExpected)
	}
	if dryItem.RowsCopied != 0 || dryItem.RowsDeleted != 0 {
		t.Fatalf("dry-run 不得搬运 / 删除，实际 copied=%d deleted=%d", dryItem.RowsCopied, dryItem.RowsDeleted)
	}
	if !hot.Migrator().HasTable(table) {
		t.Fatalf("dry-run 不得删除热库日表: %s", table)
	}
	if countInvocationRows(t, archive, table) != 0 {
		t.Fatalf("dry-run 不得向归档库写入数据")
	}

	execJob, err := svc.CreateJob(model.ArchiveModeExecute, []string{"mcp_invocation"}, "admin")
	if err != nil {
		t.Fatalf("创建 execute 任务失败: %v", err)
	}
	svc.drainActive(context.Background())
	execItem := fetchArchiveItem(t, hot, execJob.ID, table)
	if execItem.Phase != model.ArchiveItemDone {
		t.Fatalf("execute 项应为 done，实际 %q（错误 %q）", execItem.Phase, execItem.Error)
	}
	if execItem.RowsCopied != 3 || execItem.RowsDeleted != 3 {
		t.Fatalf("execute 应搬运并删除 3 行，实际 copied=%d deleted=%d", execItem.RowsCopied, execItem.RowsDeleted)
	}
	if hot.Migrator().HasTable(table) {
		t.Fatalf("校验通过后热库日表应被删除: %s", table)
	}
	if got := countInvocationRows(t, archive, table); got != 3 {
		t.Fatalf("归档库行数=%d，期望 3", got)
	}
}

// fetchArchiveItem 按任务 + 目标表取归档工作项。
func fetchArchiveItem(t *testing.T, db *gorm.DB, jobID uint, table string) model.ArchiveJobItem {
	t.Helper()
	var item model.ArchiveJobItem
	if err := db.Where("job_id = ? AND table_name = ?", jobID, table).First(&item).Error; err != nil {
		t.Fatalf("取归档工作项（任务 %d 表 %s）失败: %v", jobID, table, err)
	}
	return item
}

// countInvocationRows 统计指定表行数（表不存在视为 0）。
func countInvocationRows(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	if !db.Migrator().HasTable(table) {
		return 0
	}
	var n int64
	if err := db.Table(table).Count(&n).Error; err != nil {
		t.Fatalf("统计 %s 行数失败: %v", table, err)
	}
	return n
}
