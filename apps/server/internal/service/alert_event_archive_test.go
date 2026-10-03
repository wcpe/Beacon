package service

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// alertArchiveOldAt / alertArchiveRecentAt 是告警归档用例的两个时间锚点：
// 保留期默认 180 天、测试固定时钟 2026-06-20 → cutoff = 2025-12-22；
// 旧行取 2025-01-01（远早于 cutoff）、新行取 2026-06-15（保留期内，不得搬运）。
var (
	alertArchiveOldAt    = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	alertArchiveRecentAt = time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
)

// seedAlertEventRow 插入一条告警行（自增 id 由 DB 分配，返回其 id 供逐行断言）。
func seedAlertEventRow(t *testing.T, db *gorm.DB, status string, createdAt time.Time) uint {
	t.Helper()
	row := model.AlertEvent{
		Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelCritical,
		Namespace: "prod", ServerID: "s1", Message: "lobby-1 online → lost",
		Status: status, OccurrenceCount: 1, CreatedAt: createdAt,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("插入告警行失败: %v", err)
	}
	return row.ID
}

// alertRowStatusByID 读指定表（热库 alert_event / 冷库同名表）的主键 → 状态映射（表不存在返回空集）。
func alertRowStatusByID(t *testing.T, db *gorm.DB, ids []uint) map[uint]string {
	t.Helper()
	out := map[uint]string{}
	if !db.Migrator().HasTable("alert_event") {
		return out
	}
	var rows []model.AlertEvent
	if err := db.Table("alert_event").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		t.Fatalf("读 alert_event 行失败: %v", err)
	}
	for _, r := range rows {
		out[r.ID] = r.Status
	}
	return out
}

// TestAlertEventArchiveDomainRegistered 校验归档域登记齐备：单表形态 + 自增主键 + created_at 时间列 +
// 专用保留期键 + **extraWhere 只归档已处理行**（漏了这个条件就会把运维待办搬出热库）。
func TestAlertEventArchiveDomainRegistered(t *testing.T) {
	dom, ok := archiveDomainByName("alert_event")
	if !ok {
		t.Fatalf("alert_event 未登记为归档域（漏登记会让告警表继续只增不减）")
	}
	if dom.baseTable != "alert_event" || dom.form != archiveFormSingle {
		t.Fatalf("归档域形态不符: %+v", dom)
	}
	if dom.pkColumn != "id" || dom.pkKind != archivePKInt {
		t.Fatalf("归档域主键不符（应为自增整数 id）: %+v", dom)
	}
	if dom.timeColumn != "created_at" {
		t.Fatalf("单表形态时间列应为 created_at: %+v", dom)
	}
	if dom.retentionKey != SettingArchiveRetentionAlertEvent {
		t.Fatalf("保留期键未指向 %s: %+v", SettingArchiveRetentionAlertEvent, dom)
	}
	if dom.newModel == nil || dom.newModel().(*model.AlertEvent) == nil {
		t.Fatalf("归档域缺 newModel（无法在归档库建同构表）")
	}
	if dom.extraWhere != "status = 'resolved'" {
		t.Fatalf("alert_event 必须带「只归档已处理行」的附加过滤，实际 %q", dom.extraWhere)
	}
	if !isValidArchiveDomain("alert_event") {
		t.Fatalf("isValidArchiveDomain 未认可 alert_event")
	}
}

// TestArchiveDomainRegistryOrderAndCount 域注册表规模与顺序：9 域（7 张日表 + audit + alert_event），
// alert_event 追加在末尾（既有 8 域的枚举 / 任务展开顺序语义不变）。
func TestArchiveDomainRegistryOrderAndCount(t *testing.T) {
	if len(archiveDomains) != 9 {
		t.Fatalf("归档域应为 9 个，实际 %d", len(archiveDomains))
	}
	last := archiveDomains[len(archiveDomains)-1]
	if last.name != "alert_event" {
		t.Fatalf("alert_event 应追加在域注册表末尾（不动既有顺序），实际末尾为 %s", last.name)
	}
	want := []string{
		"metric_sample", "health_snapshot", "sched_decision", "conn_detail",
		"msg_trace", "msg_payload", "mcp_invocation", "audit", "alert_event",
	}
	for i, name := range want {
		if archiveDomains[i].name != name {
			t.Fatalf("第 %d 个域应为 %s，实际 %s", i, name, archiveDomains[i].name)
		}
	}
	// 仅 alert_event 带附加过滤：其余域的归档语义必须零变化。
	for _, d := range archiveDomains {
		if d.name != "alert_event" && d.extraWhere != "" {
			t.Fatalf("域 %s 不应有附加过滤（会改变既有归档集合）: %q", d.name, d.extraWhere)
		}
	}
}

// TestAlertEventRetentionSettingRegistered 校验保留期设置键四处同步（缺一即 fail-open）：
// 键常量、默认值 180、白名单元数据（下限 7 / 上限 3650）、以及**必须**进 dangerousSettingKeys。
func TestAlertEventRetentionSettingRegistered(t *testing.T) {
	const key = SettingArchiveRetentionAlertEvent
	if key != "archive.retention-days.alert-event" {
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
	if !SettingDangerous(key) {
		t.Fatalf("%s 未登记进 dangerousSettingKeys —— 保留期改动会绕过危险设置审批流程", key)
	}
}

// TestAlertEventArchiveOnlyResolved 是本次改动的核心用例：执行归档后
// **只有已处理（resolved）行**落冷库并从热库删除，未处理（open / acknowledged）行
// 与保留期内的行一律原样留在热库。
func TestAlertEventArchiveOnlyResolved(t *testing.T) {
	svc, hot, archive := newArchiveTestService(t)

	openOld := seedAlertEventRow(t, hot, model.AlertEventStatusOpen, alertArchiveOldAt)
	ackOld := seedAlertEventRow(t, hot, model.AlertEventStatusAcknowledged, alertArchiveOldAt)
	resolvedOldA := seedAlertEventRow(t, hot, model.AlertEventStatusResolved, alertArchiveOldAt)
	resolvedOldB := seedAlertEventRow(t, hot, model.AlertEventStatusResolved, alertArchiveOldAt)
	resolvedRecent := seedAlertEventRow(t, hot, model.AlertEventStatusResolved, alertArchiveRecentAt)
	openRecent := seedAlertEventRow(t, hot, model.AlertEventStatusOpen, alertArchiveRecentAt)
	allIDs := []uint{openOld, ackOld, resolvedOldA, resolvedOldB, resolvedRecent, openRecent}

	// dry-run：预估量必须只含「到期 + 已处理」的 2 行，且零副作用。
	dryJob, err := svc.CreateJob(model.ArchiveModeDryRun, []string{"alert_event"}, "admin")
	if err != nil {
		t.Fatalf("创建 dry-run 任务失败: %v", err)
	}
	svc.drainActive(context.Background())
	dryItem := fetchArchiveItem(t, hot, dryJob.ID, "alert_event")
	if dryItem.RowsExpected != 2 || dryItem.RowsCopied != 0 || dryItem.RowsDeleted != 0 {
		t.Fatalf("dry-run 应只预估 2 行（到期且已处理）、零搬运零删除，实际 %+v", dryItem)
	}
	if cold := alertRowStatusByID(t, archive, allIDs); len(cold) != 0 {
		t.Fatalf("dry-run 不得向归档库写入数据，实际 %d 行", len(cold))
	}
	if got := alertRowStatusByID(t, hot, allIDs); len(got) != len(allIDs) {
		t.Fatalf("dry-run 不得删热库行，实际剩 %d / 6", len(got))
	}

	// execute：实际搬运量必须与 dry-run 预估一致（同为 2 行）。
	execJob, err := svc.CreateJob(model.ArchiveModeExecute, []string{"alert_event"}, "admin")
	if err != nil {
		t.Fatalf("创建 execute 任务失败: %v", err)
	}
	svc.drainActive(context.Background())
	execItem := fetchArchiveItem(t, hot, execJob.ID, "alert_event")
	if execItem.Phase != model.ArchiveItemDone {
		t.Fatalf("execute 项应为 done，实际 %q（错误 %q）", execItem.Phase, execItem.Error)
	}
	if execItem.RowsCopied != dryItem.RowsExpected || execItem.RowsDeleted != dryItem.RowsExpected {
		t.Fatalf("execute 实际搬运 / 删除应与 dry-run 预估一致（%d），实际 copied=%d deleted=%d",
			dryItem.RowsExpected, execItem.RowsCopied, execItem.RowsDeleted)
	}
	if execItem.VerifyPassed == nil || !*execItem.VerifyPassed {
		t.Fatalf("execute 项应校验通过，实际 %v", execItem.VerifyPassed)
	}

	// 冷库：只有两条 resolved 旧行，数据原样（状态仍是 resolved）。
	cold := alertRowStatusByID(t, archive, allIDs)
	if len(cold) != 2 {
		t.Fatalf("归档库应只有 2 行（已处理的到期行），实际 %d：%v", len(cold), cold)
	}
	if cold[resolvedOldA] != model.AlertEventStatusResolved || cold[resolvedOldB] != model.AlertEventStatusResolved {
		t.Fatalf("归档库应为已处理行且状态原样，实际 %v", cold)
	}

	// 热库：未处理行与保留期内行必须原样保留（本条是「待办不消失」的关键断言）。
	warm := alertRowStatusByID(t, hot, allIDs)
	if len(warm) != 4 {
		t.Fatalf("热库应保留 4 行（2 未处理旧行 + 2 保留期内行），实际 %d：%v", len(warm), warm)
	}
	if warm[openOld] != model.AlertEventStatusOpen {
		t.Fatalf("未处理旧告警必须留在热库且状态不变，实际 %v", warm)
	}
	if warm[ackOld] != model.AlertEventStatusAcknowledged {
		t.Fatalf("已确认（acknowledged）旧告警属未完成待办、必须留在热库，实际 %v", warm)
	}
	if warm[resolvedRecent] != model.AlertEventStatusResolved {
		t.Fatalf("保留期内的已处理行不得被搬走（cutoff 生效），实际 %v", warm)
	}
	if warm[openRecent] != model.AlertEventStatusOpen {
		t.Fatalf("保留期内的未处理行不得被动，实际 %v", warm)
	}
	for _, gone := range []uint{resolvedOldA, resolvedOldB} {
		if _, ok := warm[gone]; ok {
			t.Fatalf("已搬运的到期已处理行应从热库删除，实际仍在：id=%d", gone)
		}
	}
	// 「open 行仍在热库」的最直白断言：按状态计数。
	var hotOpen int64
	if err := hot.Model(&model.AlertEvent{}).Where("status <> ?", model.AlertEventStatusResolved).Count(&hotOpen).Error; err != nil {
		t.Fatalf("统计热库未处理告警失败: %v", err)
	}
	if hotOpen != 3 {
		t.Fatalf("热库未处理告警应保留 3 行（open 2 + acknowledged 1），实际 %d", hotOpen)
	}
}

// TestAlertEventArchiveOverviewCountsResolvedOnly 总览口径：热库体量与到期量都必须与
// 「实际可搬运集合」同口径（只数已处理行），否则运维会看到「到期 5 行、实际只搬 2 行」的自相矛盾。
func TestAlertEventArchiveOverviewCountsResolvedOnly(t *testing.T) {
	svc, hot, _ := newArchiveTestService(t)
	seedAlertEventRow(t, hot, model.AlertEventStatusOpen, alertArchiveOldAt)
	seedAlertEventRow(t, hot, model.AlertEventStatusOpen, alertArchiveRecentAt)
	seedAlertEventRow(t, hot, model.AlertEventStatusResolved, alertArchiveOldAt)
	seedAlertEventRow(t, hot, model.AlertEventStatusResolved, alertArchiveRecentAt)

	ov, err := svc.Overview()
	if err != nil {
		t.Fatalf("总览失败: %v", err)
	}
	var dom *ArchiveDomainOverviewView
	for i := range ov.Domains {
		if ov.Domains[i].Domain == "alert_event" {
			dom = &ov.Domains[i]
		}
	}
	if dom == nil {
		t.Fatalf("总览缺 alert_event 域")
	}
	if dom.RetentionDays != archiveDefaultRetentionAlertEvent {
		t.Fatalf("保留期应 %d，实际 %d", archiveDefaultRetentionAlertEvent, dom.RetentionDays)
	}
	if dom.HotRows != 2 {
		t.Fatalf("热库体量应只数已处理行（2），实际 %d", dom.HotRows)
	}
	if dom.ExpiredRows != 1 {
		t.Fatalf("到期量应只数「到期且已处理」的行（1），实际 %d", dom.ExpiredRows)
	}
}

// TestAlertEventDomainCountsMatchArchiveScope 域内计数（domainRowCount，overview 的水位来源）与
// 实际搬运集合同口径：cutoff 非空只数到期行、nil 数全部可归档行。
func TestAlertEventDomainCountsMatchArchiveScope(t *testing.T) {
	_, hot, _ := newArchiveTestService(t)
	seedAlertEventRow(t, hot, model.AlertEventStatusOpen, alertArchiveOldAt)
	seedAlertEventRow(t, hot, model.AlertEventStatusOpen, alertArchiveRecentAt)
	seedAlertEventRow(t, hot, model.AlertEventStatusResolved, alertArchiveOldAt)
	seedAlertEventRow(t, hot, model.AlertEventStatusResolved, alertArchiveRecentAt)

	dom := mustDomain(t, "alert_event")
	cutoff := cutoffFor(archiveTestNow, archiveDefaultRetentionAlertEvent)
	expired, err := domainRowCount(hot, dom, &cutoff)
	if err != nil {
		t.Fatalf("统计到期行失败: %v", err)
	}
	if expired != 1 {
		t.Fatalf("到期行应只数已处理（1），实际 %d", expired)
	}
	all, err := domainRowCount(hot, dom, nil)
	if err != nil {
		t.Fatalf("统计全量行失败: %v", err)
	}
	if all != 2 {
		t.Fatalf("全量行应只数已处理（2），实际 %d", all)
	}
}

// TestAlertEventDeleteSkipsUncopiedRows 是「搬运集合 ≡ 删除集合」的加固用例：
// 过滤谓词可变（status 会被人工处置 / 自动消解改），若某行在 copy 之后才变为 resolved，
// 它**从未搬到归档库**——删除阶段绝不能删它（删了就是热库没有、冷库也没有的静默丢失）。
func TestAlertEventDeleteSkipsUncopiedRows(t *testing.T) {
	_, hot, archive := newArchiveTestService(t)
	dom := mustDomain(t, "alert_event")

	// 热库两行「到期且已处理」，但归档库一行都没有（模拟：copy 阶段它们还不是 resolved）。
	idA := seedAlertEventRow(t, hot, model.AlertEventStatusResolved, alertArchiveOldAt)
	idB := seedAlertEventRow(t, hot, model.AlertEventStatusResolved, alertArchiveOldAt)

	rangeTo := cutoffFor(archiveTestNow, archiveDefaultRetentionAlertEvent)
	runner := &archiveItemRunner{
		hot: hot, archive: archive, dom: dom, mode: model.ArchiveModeExecute,
		batchRows: 100, batchInterval: 0, sampleSize: 100,
		saveItem:  func(*model.ArchiveJobItem) error { return nil },
		cancelled: func() bool { return false },
	}
	item := &model.ArchiveJobItem{
		Domain: "alert_event", TargetTable: "alert_event",
		RangeTo: &rangeTo, Phase: model.ArchiveItemDeleting,
	}
	// 归档表尚未建（等价于「一行都没搬」）：整轮删除必须零删除、且不空转。
	if err := runner.runDelete(item); err != nil {
		t.Fatalf("删除阶段不应报错（未搬运的行只是跳过）: %v", err)
	}
	if item.RowsDeleted != 0 {
		t.Fatalf("归档库没有的行绝不能被删除，实际删除 %d 行", item.RowsDeleted)
	}
	if got := len(alertRowStatusByID(t, hot, []uint{idA, idB})); got != 2 {
		t.Fatalf("热库两行必须原样保留，实际剩 %d 行", got)
	}

	// 反向验证：先把 B 搬进归档库，则只有 B 被删、A 仍留热库（证明上面的「零删除」不是没用例覆盖的死路径）。
	if err := runner.runCopy(&model.ArchiveJobItem{
		Domain: "alert_event", TargetTable: "alert_event", RangeTo: &rangeTo, Phase: model.ArchiveItemPending,
	}); err != nil {
		t.Fatalf("copy 阶段失败: %v", err)
	}
	item2 := &model.ArchiveJobItem{
		Domain: "alert_event", TargetTable: "alert_event",
		RangeTo: &rangeTo, Phase: model.ArchiveItemDeleting,
	}
	if err := runner.runDelete(item2); err != nil {
		t.Fatalf("补搬后删除失败: %v", err)
	}
	if item2.RowsDeleted != 2 {
		t.Fatalf("两行都已搬进归档库，删除应得 2 行，实际 %d", item2.RowsDeleted)
	}
	if got := len(alertRowStatusByID(t, hot, []uint{idA, idB})); got != 0 {
		t.Fatalf("已搬运的行应从热库删除，实际剩 %d 行", got)
	}
	if got := len(alertRowStatusByID(t, archive, []uint{idA, idB})); got != 2 {
		t.Fatalf("归档库应有 2 行，实际 %d 行", got)
	}
}

// TestArchiveAllDomainsExecuteNoRegression 全域名 execute 任务（domains 省略=全部）：
// 既有日表域与 audit 单表域照常搬运 + 删热库，alert_event 域按 resolved 过滤，
// 各域互不影响（域数从 8 变 9 后既有行为不回归）。
func TestArchiveAllDomainsExecuteNoRegression(t *testing.T) {
	svc, hot, archive := newArchiveTestService(t)

	// 日表域：到期日表（metric_sample，保留 14 天 → 06-01 早于 cutoff 06-06）。
	metricDay := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	metricTable := seedMetricDailyTable(t, hot, metricDay, 3)

	// audit 单表域：1 条到期行 + 1 条保留期内行（后者必须留在热库）。
	oldAudit := model.AuditLog{Operator: "a", Action: "x", TargetType: "t", TargetRef: "r", Result: "ok",
		CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	recentAudit := model.AuditLog{Operator: "b", Action: "x", TargetType: "t", TargetRef: "r", Result: "ok",
		CreatedAt: time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)}
	if err := hot.Create(&oldAudit).Error; err != nil {
		t.Fatalf("插入审计行失败: %v", err)
	}
	if err := hot.Create(&recentAudit).Error; err != nil {
		t.Fatalf("插入审计行失败: %v", err)
	}

	// alert_event 域：到期未处理行（必须留热库）+ 到期已处理行（应归档）。
	openAlert := seedAlertEventRow(t, hot, model.AlertEventStatusOpen, alertArchiveOldAt)
	resolvedAlert := seedAlertEventRow(t, hot, model.AlertEventStatusResolved, alertArchiveOldAt)

	job, err := svc.CreateJob(model.ArchiveModeExecute, nil, "admin") // nil = 全部域
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	svc.drainActive(context.Background())
	detail, err := svc.GetJob(job.ID)
	if err != nil {
		t.Fatalf("取任务详情失败: %v", err)
	}
	if detail.Status != model.ArchiveJobSucceeded {
		t.Fatalf("全域名任务应 succeeded，实际 %s（error=%v）", detail.Status, detail.Error)
	}
	// 每个实际搬运过的 item 都必须校验通过。
	for _, it := range detail.Items {
		if it.RowsCopied > 0 && (it.VerifyPassed == nil || !*it.VerifyPassed) {
			t.Fatalf("item %s（域 %s）校验未通过", it.TableName, it.Domain)
		}
	}

	// 日表域：整表搬运 + 删热库（既有行为不变）。
	if got := tableCount(t, archive, metricTable); got != 3 {
		t.Fatalf("归档 metric 日表应 3 行，实际 %d", got)
	}
	if hot.Migrator().HasTable(metricTable) {
		t.Fatalf("热库 metric 日表应被删")
	}
	// audit 域：到期行搬走、保留期内行留下（注意任务自身会写创建 / 完成两条审计，也落在保留期内）。
	var expiredAudit int64
	if err := hot.Model(&model.AuditLog{}).Where("created_at < ?", cutoffFor(archiveTestNow, archiveDefaultRetentionAudit)).
		Count(&expiredAudit).Error; err != nil {
		t.Fatalf("统计热库到期审计失败: %v", err)
	}
	if expiredAudit != 0 {
		t.Fatalf("热库到期审计应清零，实际 %d", expiredAudit)
	}
	var keptAudit int64
	if err := hot.Model(&model.AuditLog{}).Where("id = ?", recentAudit.ID).Count(&keptAudit).Error; err != nil {
		t.Fatalf("统计保留期内审计失败: %v", err)
	}
	if keptAudit != 1 {
		t.Fatalf("保留期内的审计行必须留在热库，实际 %d", keptAudit)
	}
	// alert_event 域：只搬已处理行，未处理行留在热库。
	var hotAlerts int64
	if err := hot.Model(&model.AlertEvent{}).Count(&hotAlerts).Error; err != nil {
		t.Fatalf("统计热库告警失败: %v", err)
	}
	if hotAlerts != 1 {
		t.Fatalf("热库应只剩未处理告警 1 条，实际 %d", hotAlerts)
	}
	warm := alertRowStatusByID(t, hot, []uint{openAlert, resolvedAlert})
	if warm[openAlert] != model.AlertEventStatusOpen {
		t.Fatalf("未处理告警必须留在热库，实际 %v", warm)
	}
	cold := alertRowStatusByID(t, archive, []uint{openAlert, resolvedAlert})
	if len(cold) != 1 || cold[resolvedAlert] != model.AlertEventStatusResolved {
		t.Fatalf("归档库应只有已处理的那条告警，实际 %v", cold)
	}
}
