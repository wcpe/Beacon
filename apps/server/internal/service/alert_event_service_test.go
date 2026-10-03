package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/alert"
)

// newAlertEventService 用私有内存 sqlite 装配告警事件服务（迁移 alert_event + audit_log，不依赖 MySQL）。
func newAlertEventService(t *testing.T) (*AlertEventService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:alertsvc_"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger:  logger.Default.LogMode(logger.Silent),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&model.AlertEvent{}, &model.AuditLog{}, &model.Namespace{}, &model.Server{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	svc := NewAlertEventService(db, repository.NewAlertEventRepository(db), repository.NewAuditLogRepository(db))
	return svc, db
}

// TestRecordDefaultsStatusOpen Record 未指定状态时默认落 open（新告警即待处理，FR-157）。
func TestRecordDefaultsStatusOpen(t *testing.T) {
	svc, db := newAlertEventService(t)
	e := &model.AlertEvent{
		Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelCritical,
		Namespace: "prod", ServerID: "s1", Message: "s1 online → lost",
	}
	if err := svc.Record(e); err != nil {
		t.Fatalf("Record 应成功，实际 %v", err)
	}
	var got model.AlertEvent
	if err := db.First(&got, e.ID).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got.Status != model.AlertEventStatusOpen {
		t.Fatalf("新告警状态应为 open，实际 %q", got.Status)
	}
	if got.HandledAt != nil || got.HandledBy != "" {
		t.Fatalf("新告警不应有处理痕迹，实际 handledBy=%q handledAt=%v", got.HandledBy, got.HandledAt)
	}
}

// TestRecordKeepsExplicitStatus Record 已显式指定状态时不覆盖（如迁移 / 回放场景）。
func TestRecordKeepsExplicitStatus(t *testing.T) {
	svc, db := newAlertEventService(t)
	e := &model.AlertEvent{
		Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelInfo,
		Namespace: "prod", ServerID: "s2", Message: "x", Status: model.AlertEventStatusResolved,
	}
	if err := svc.Record(e); err != nil {
		t.Fatalf("Record 应成功，实际 %v", err)
	}
	var got model.AlertEvent
	_ = db.First(&got, e.ID).Error
	if got.Status != model.AlertEventStatusResolved {
		t.Fatalf("显式状态应保留 resolved，实际 %q", got.Status)
	}
}

// seedOpenEvent 落一条 open 告警并返回其 id。
// seedOpenEvent 直插一条 open 告警（绕过 Record 的 FR-232 收敛，便于构造「同键多行」场景）。
func seedOpenEvent(t *testing.T, svc *AlertEventService, ns, serverID string) uint {
	t.Helper()
	e := &model.AlertEvent{
		Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning,
		Namespace: ns, ServerID: serverID, Message: serverID + " degraded", Status: model.AlertEventStatusOpen,
		OccurrenceCount: 1,
	}
	if err := svc.db.Create(e).Error; err != nil {
		t.Fatalf("落 open 告警失败: %v", err)
	}
	return e.ID
}

// TestHandleStatusTransition 状态转移 open → acknowledged → resolved，处理人 / 时刻 / 说明落库。
func TestHandleStatusTransition(t *testing.T) {
	svc, db := newAlertEventService(t)
	id := seedOpenEvent(t, svc, "prod", "s1")

	// open → acknowledged
	ack, err := svc.Handle(id, "acknowledge", "已知悉，排查中", "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("acknowledge 应成功，实际 %v", err)
	}
	if ack.Status != model.AlertEventStatusAcknowledged {
		t.Fatalf("应转 acknowledged，实际 %q", ack.Status)
	}
	if ack.HandledBy != "alice" || ack.HandledAt == nil || ack.HandleNote != "已知悉，排查中" {
		t.Fatalf("处理痕迹应落库，实际 %+v", ack)
	}

	// acknowledged → resolved
	res, err := svc.Handle(id, "resolve", "已重启恢复", "bob", "10.0.0.2")
	if err != nil {
		t.Fatalf("resolve 应成功，实际 %v", err)
	}
	if res.Status != model.AlertEventStatusResolved || res.HandledBy != "bob" || res.HandleNote != "已重启恢复" {
		t.Fatalf("应转 resolved 并更新处理人 / 说明，实际 %+v", res)
	}

	var got model.AlertEvent
	_ = db.First(&got, id).Error
	if got.Status != model.AlertEventStatusResolved {
		t.Fatalf("库内最终应 resolved，实际 %q", got.Status)
	}
}

// TestHandleAcceptsStatusWording Handle 兼容前端契约措辞（目标状态 acknowledged / resolved）。
func TestHandleAcceptsStatusWording(t *testing.T) {
	svc, _ := newAlertEventService(t)
	id := seedOpenEvent(t, svc, "prod", "s1")
	got, err := svc.Handle(id, model.AlertEventStatusResolved, "note", "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("status 措辞应被接受，实际 %v", err)
	}
	if got.Status != model.AlertEventStatusResolved {
		t.Fatalf("应转 resolved，实际 %q", got.Status)
	}
}

// TestHandleWritesAudit 处理动作在同事务内写专项审计（含操作者 / 事件 id / 动作 / 说明），detail 不泄凭据。
func TestHandleWritesAudit(t *testing.T) {
	svc, db := newAlertEventService(t)
	id := seedOpenEvent(t, svc, "prod", "s1")

	if _, err := svc.Handle(id, "resolve", "已处置", "alice", "203.0.113.9"); err != nil {
		t.Fatalf("resolve 应成功，实际 %v", err)
	}

	var logs []model.AuditLog
	if err := db.Where("action = ?", model.ActionAlertEventResolve).Find(&logs).Error; err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("应有 1 条 alert-event.resolve 审计，实际 %d", len(logs))
	}
	got := logs[0]
	if got.Operator != "alice" || got.ClientIP != "203.0.113.9" {
		t.Fatalf("审计 operator / clientIp 应落库，实际 %q / %q", got.Operator, got.ClientIP)
	}
	if got.TargetType != model.TargetTypeAlertEvent || got.TargetRef != "1" {
		t.Fatalf("审计 target 应为 alert-event/1，实际 %s/%s", got.TargetType, got.TargetRef)
	}
	if got.NamespaceCode != "prod" || got.Result != model.ResultOK {
		t.Fatalf("审计 namespaceCode / result 应为 prod / ok，实际 %q / %q", got.NamespaceCode, got.Result)
	}
	if !strings.Contains(got.Detail, "resolved") || !strings.Contains(got.Detail, "已处置") {
		t.Fatalf("审计 detail 应含状态与处置说明，实际 %q", got.Detail)
	}
}

// TestHandleInvalidActionNoChange 非法动作返回 ErrAlertActionInvalid，不改状态、不写审计。
func TestHandleInvalidActionNoChange(t *testing.T) {
	svc, db := newAlertEventService(t)
	id := seedOpenEvent(t, svc, "prod", "s1")

	_, err := svc.Handle(id, "reopen", "", "alice", "10.0.0.1")
	if !errors.Is(err, apperr.ErrAlertActionInvalid) {
		t.Fatalf("非法动作应返回 ErrAlertActionInvalid，实际 %v", err)
	}
	var got model.AlertEvent
	_ = db.First(&got, id).Error
	if got.Status != model.AlertEventStatusOpen {
		t.Fatalf("非法动作不应改状态，实际 %q", got.Status)
	}
	var n int64
	_ = db.Model(&model.AuditLog{}).Count(&n).Error
	if n != 0 {
		t.Fatalf("非法动作不应写审计，实际 %d 条", n)
	}
}

// TestHandleNotFound 处理不存在的事件返回 ErrAlertEventNotFound，不写审计。
func TestHandleNotFound(t *testing.T) {
	svc, db := newAlertEventService(t)
	_, err := svc.Handle(9999, "resolve", "", "alice", "10.0.0.1")
	if !errors.Is(err, apperr.ErrAlertEventNotFound) {
		t.Fatalf("应返回 ErrAlertEventNotFound，实际 %v", err)
	}
	var n int64
	_ = db.Model(&model.AuditLog{}).Count(&n).Error
	if n != 0 {
		t.Fatalf("事件不存在不应写审计，实际 %d 条", n)
	}
}

// TestActiveCountsOnlyOpen ActiveCounts 按 (namespace, serverId) 只统计 open，acknowledged / resolved 不计。
func TestActiveCountsOnlyOpen(t *testing.T) {
	svc, _ := newAlertEventService(t)
	// s1：3 条 open + 1 条已解决 → 计 3
	seedOpenEvent(t, svc, "prod", "s1")
	seedOpenEvent(t, svc, "prod", "s1")
	seedOpenEvent(t, svc, "prod", "s1")
	resolvedID := seedOpenEvent(t, svc, "prod", "s1")
	if _, err := svc.Handle(resolvedID, "resolve", "", "alice", "10.0.0.1"); err != nil {
		t.Fatalf("resolve 失败: %v", err)
	}
	// s2：1 条 open，但先 acknowledge → 不再计入（acknowledged 非 open）
	ackID := seedOpenEvent(t, svc, "prod", "s2")
	if _, err := svc.Handle(ackID, "acknowledge", "", "alice", "10.0.0.1"); err != nil {
		t.Fatalf("acknowledge 失败: %v", err)
	}
	// dev/s3：2 条 open
	seedOpenEvent(t, svc, "dev", "s3")
	seedOpenEvent(t, svc, "dev", "s3")

	counts, err := svc.ActiveCounts()
	if err != nil {
		t.Fatalf("ActiveCounts 失败: %v", err)
	}
	if counts[AlertActiveKey{Namespace: "prod", ServerID: "s1"}] != 3 {
		t.Fatalf("prod/s1 应 3 条 open，实际 %d", counts[AlertActiveKey{Namespace: "prod", ServerID: "s1"}])
	}
	if _, ok := counts[AlertActiveKey{Namespace: "prod", ServerID: "s2"}]; ok {
		t.Fatalf("prod/s2 已 acknowledge，不应出现在活跃计数中")
	}
	if counts[AlertActiveKey{Namespace: "dev", ServerID: "s3"}] != 2 {
		t.Fatalf("dev/s3 应 2 条 open，实际 %d", counts[AlertActiveKey{Namespace: "dev", ServerID: "s3"}])
	}
}

// TestHandleBatchByFilterCrossPage 按筛选跨页批量处理：一条 UPDATE 作用于全部命中 open 行（非仅当前页）。
func TestHandleBatchByFilterCrossPage(t *testing.T) {
	svc, db := newAlertEventService(t)
	// 45 条 critical/open（> 默认页 20，验证「跨页」在服务端一次性生效）+ 10 条 warning + 5 条已 resolved
	// 直插绕过 FR-232 收敛（否则同键会合并为 1 行，测不出跨页）。
	for i := 0; i < 45; i++ {
		if err := db.Create(&model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelCritical, Namespace: "prod", ServerID: "s", Message: "m", Status: model.AlertEventStatusOpen, OccurrenceCount: 1}).Error; err != nil {
			t.Fatalf("seed critical 失败: %v", err)
		}
	}
	for i := 0; i < 10; i++ {
		if err := db.Create(&model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning, Namespace: "prod", ServerID: "s", Message: "m", Status: model.AlertEventStatusOpen, OccurrenceCount: 1}).Error; err != nil {
			t.Fatalf("seed warning 失败: %v", err)
		}
	}
	for i := 0; i < 5; i++ {
		if err := db.Create(&model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelCritical, Namespace: "prod", ServerID: "s", Message: "m", Status: model.AlertEventStatusResolved, OccurrenceCount: 1}).Error; err != nil {
			t.Fatalf("seed resolved 失败: %v", err)
		}
	}

	affected, err := svc.HandleBatch(repository.AlertEventFilter{Level: model.AlertLevelCritical}, "resolve", "批量处置", "admin", "203.0.113.7")
	if err != nil {
		t.Fatalf("批量处理失败: %v", err)
	}
	if affected != 45 {
		t.Fatalf("应命中全部 45 条 open critical（跨页），实际 %d", affected)
	}
	// 15 条（10 warning + 5 已 resolved）不受影响
	var stillOpenOrResolved int64
	db.Model(&model.AlertEvent{}).Where("level = ? AND status = ?", model.AlertLevelWarning, model.AlertEventStatusOpen).Count(&stillOpenOrResolved)
	if stillOpenOrResolved != 10 {
		t.Fatalf("warning 应保持 open（10 条），实际 %d", stillOpenOrResolved)
	}
	var resolvedCount int64
	db.Model(&model.AlertEvent{}).Where("status = ?", model.AlertEventStatusResolved).Count(&resolvedCount)
	if resolvedCount != 50 {
		t.Fatalf("resolved 应为 50（原 5 + 新 45），实际 %d", resolvedCount)
	}

	// 幂等：重复执行 0 命中
	again, err := svc.HandleBatch(repository.AlertEventFilter{Level: model.AlertLevelCritical}, "resolve", "", "admin", "")
	if err != nil {
		t.Fatalf("重复批量失败: %v", err)
	}
	if again != 0 {
		t.Fatalf("重复执行应 0 命中（幂等），实际 %d", again)
	}

	// 审计：一条批量审计（含条件 + 命中数 + 操作者）
	var audit model.AuditLog
	if err := db.Where("action = ?", model.ActionAlertEventBatchHandled).Order("id ASC").First(&audit).Error; err != nil {
		t.Fatalf("批量处理未落审计: %v", err)
	}
	if audit.Operator != "admin" || !strings.Contains(audit.Detail, `"affected":45`) || !strings.Contains(audit.Detail, `"level":"critical"`) {
		t.Fatalf("批量审计明细不符：operator=%q detail=%s", audit.Operator, audit.Detail)
	}
}

// TestHandleBatchAcknowledgeWording 批量同样接受动词 / 目标状态两种措辞（acknowledge → acknowledged）。
func TestHandleBatchAcknowledgeWording(t *testing.T) {
	svc, db := newAlertEventService(t)
	if err := svc.Record(&model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelInfo, Namespace: "prod", ServerID: "s", Message: "m"}); err != nil {
		t.Fatalf("seed 失败: %v", err)
	}
	affected, err := svc.HandleBatch(repository.AlertEventFilter{}, "acknowledge", "", "admin", "")
	if err != nil {
		t.Fatalf("批量确认失败: %v", err)
	}
	if affected != 1 {
		t.Fatalf("应命中 1 条，实际 %d", affected)
	}
	var got model.AlertEvent
	db.First(&got)
	if got.Status != model.AlertEventStatusAcknowledged {
		t.Fatalf("应落 acknowledged，实际 %q", got.Status)
	}
	if _, err := svc.HandleBatch(repository.AlertEventFilter{}, "bogus", "", "admin", ""); !errors.Is(err, apperr.ErrAlertActionInvalid) {
		t.Fatalf("非法动作应 ErrAlertActionInvalid，实际 %v", err)
	}
}

// TestAlertContextAggregatesServerAndTimeline 校验 FR-230：详情聚合返回该服告警时间线（查 alert_event，
// 24h 内、倒序、上限 20）；无健康真源（已归档 / 无 serverId）时 server 为 nil 但时间线仍返回，不报错。
func TestAlertContextAggregatesServerAndTimeline(t *testing.T) {
	svc, db := newAlertEventService(t)
	base := time.Now().UTC()
	for i := 0; i < 25; i++ {
		at := base.Add(-time.Duration(i) * time.Minute)
		if err := db.Create(&model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning, Namespace: "prod", ServerID: "s1", Message: "m", CreatedAt: at}).Error; err != nil {
			t.Fatalf("seed s1 失败: %v", err)
		}
	}
	if err := db.Create(&model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning, Namespace: "prod", ServerID: "s1", Message: "old", CreatedAt: base.Add(-48 * time.Hour)}).Error; err != nil {
		t.Fatalf("seed old 失败: %v", err)
	}
	if err := db.Create(&model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning, Namespace: "prod", ServerID: "s2", Message: "other", CreatedAt: base}).Error; err != nil {
		t.Fatalf("seed s2 失败: %v", err)
	}
	anchor := &model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelCritical, Namespace: "prod", ServerID: "s1", Message: "anchor", Status: model.AlertEventStatusOpen, OccurrenceCount: 1}
	if err := db.Create(anchor).Error; err != nil {
		t.Fatalf("record anchor 失败: %v", err)
	}

	res, err := svc.Context(anchor.ID, ObservationScope{All: true})
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	if len(res.Timeline) != 20 {
		t.Fatalf("时间线应上限 20，实际 %d", len(res.Timeline))
	}
	for _, e := range res.Timeline {
		if e.ServerID != "s1" {
			t.Fatalf("时间线串入了其它服：%q", e.ServerID)
		}
		if e.CreatedAt.Before(base.Add(-24 * time.Hour)) {
			t.Fatalf("时间线越出 24h 窗口：%v", e.CreatedAt)
		}
	}
	if res.TimelineLimit != 20 || res.TimelineWindowHours != 24 {
		t.Fatalf("窗口常量应为 20/24，实际 %d/%d", res.TimelineLimit, res.TimelineWindowHours)
	}
	if res.Server != nil {
		t.Fatalf("未装配健康真源时 server 应为 nil")
	}

	if _, err := svc.Context(999999, ObservationScope{All: true}); !errors.Is(err, apperr.ErrAlertEventNotFound) {
		t.Fatalf("未知告警应 ErrAlertEventNotFound，实际 %v", err)
	}

	nsEvent := &model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelInfo, Namespace: "prod", ServerID: "", Message: "cluster"}
	if err := svc.Record(nsEvent); err != nil {
		t.Fatalf("record ns 事件失败: %v", err)
	}
	res2, err := svc.Context(nsEvent.ID, ObservationScope{All: true})
	if err != nil {
		t.Fatalf("集群级聚合失败: %v", err)
	}
	if res2.Server != nil {
		t.Fatalf("无 serverId 时 server 应为 nil")
	}
	if len(res2.Timeline) == 0 {
		t.Fatalf("无 serverId 时应按 namespace 退化返回时间线")
	}
}

// TestResolveAlertRoleAndOverrideLevel 校验 FR-231：角色解析（proxy / lobby / backend）与人工改级（落列 + 审计）。
func TestResolveAlertRoleAndOverrideLevel(t *testing.T) {
	svc, db := newAlertEventService(t)
	ns := model.Namespace{Code: "prod", Name: "prod", Lifecycle: model.NamespaceLifecycleActive}
	if err := db.Create(&ns).Error; err != nil {
		t.Fatalf("建 namespace 失败: %v", err)
	}
	lobby := uint(9)
	seed := []model.Server{
		{NamespaceID: ns.ID, ServerID: "bk1", Kind: model.ServerKindBackend},
		{NamespaceID: ns.ID, ServerID: "px1", Kind: model.ServerKindProxy},
		{NamespaceID: ns.ID, ServerID: "lb1", Kind: model.ServerKindBackend, LobbyClusterID: &lobby},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("建 server 失败: %v", err)
		}
	}
	cases := map[string]string{"bk1": alert.RoleBackend, "px1": alert.RoleProxy, "lb1": alert.RoleLobby, "nope": ""}
	for serverID, want := range cases {
		if got := svc.ResolveAlertRole("prod", serverID); got != want {
			t.Fatalf("%s 角色应为 %q，实际 %q", serverID, want, got)
		}
	}
	if got := svc.ResolveAlertRole("nosuchns", "bk1"); got != "" {
		t.Fatalf("未知 namespace 应空串，实际 %q", got)
	}

	// 人工改级：写 override 列 + 审计
	e := &model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelInfo, Namespace: "prod", ServerID: "bk1", Message: "m"}
	if err := svc.Record(e); err != nil {
		t.Fatalf("record 失败: %v", err)
	}
	updated, err := svc.OverrideAlertLevel(e.ID, model.AlertLevelCritical, "admin", "203.0.113.9")
	if err != nil {
		t.Fatalf("改级失败: %v", err)
	}
	if updated.SeverityOverride != model.AlertLevelCritical || updated.OverriddenBy != "admin" || updated.OverriddenAt == nil {
		t.Fatalf("覆盖列未正确落库：%+v", updated)
	}
	var audit model.AuditLog
	if err := db.Where("action = ?", model.ActionAlertEventLevelOverridden).First(&audit).Error; err != nil {
		t.Fatalf("改级未落审计: %v", err)
	}
	if !strings.Contains(audit.Detail, `"newLevel":"critical"`) {
		t.Fatalf("审计明细应含新级别，实际 %s", audit.Detail)
	}
	if _, err := svc.OverrideAlertLevel(e.ID, "bogus", "admin", ""); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("非法级别应 ErrInvalidParam，实际 %v", err)
	}
	if _, err := svc.OverrideAlertLevel(999999, model.AlertLevelInfo, "admin", ""); !errors.Is(err, apperr.ErrAlertEventNotFound) {
		t.Fatalf("未知告警应 ErrAlertEventNotFound，实际 %v", err)
	}
}

// TestFR232ConvergenceAndAutoResolve 校验 FR-232：同键收敛计数、已处理不回退、取最高级、恶化链合并不分行、恢复自动消解。
func TestFR232ConvergenceAndAutoResolve(t *testing.T) {
	svc, db := newAlertEventService(t)
	trigger := func(level, toStatus string) {
		t.Helper()
		if err := svc.Record(&model.AlertEvent{
			Type: model.AlertEventTypeHealthTransition, Level: level,
			ToStatus: toStatus, Namespace: "prod", ServerID: "s1", Message: "m",
		}); err != nil {
			t.Fatalf("触发失败: %v", err)
		}
	}
	// 同键连续触发 5 次 → 只有 1 行、计数 5
	for i := 0; i < 5; i++ {
		trigger(model.AlertLevelWarning, "lost")
	}
	var rows []model.AlertEvent
	db.Where("namespace = ? AND server_id = ?", "prod", "s1").Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("同键应收敛为 1 行，实际 %d", len(rows))
	}
	if rows[0].OccurrenceCount != 5 {
		t.Fatalf("计数应为 5，实际 %d", rows[0].OccurrenceCount)
	}
	if rows[0].LastAt == nil {
		t.Fatalf("last_at 应被刷新")
	}

	// 已 acknowledged 再触发：不回退 open，仅计数 / 取最高级
	if _, err := svc.Handle(rows[0].ID, "acknowledge", "", "alice", ""); err != nil {
		t.Fatalf("acknowledge 失败: %v", err)
	}
	trigger(model.AlertLevelCritical, "lost")
	var one model.AlertEvent
	db.First(&one, rows[0].ID)
	if one.Status != model.AlertEventStatusAcknowledged {
		t.Fatalf("已处理行不应回退 open，实际 %q", one.Status)
	}
	if one.OccurrenceCount != 6 {
		t.Fatalf("计数应递增为 6，实际 %d", one.OccurrenceCount)
	}
	if one.Level != model.AlertLevelCritical {
		t.Fatalf("合并应取最高级 critical，实际 %q", one.Level)
	}

	// 方向更严重（lost → offline）不新开行：仍在同一行内升到最严重态，计数继续递增。
	// 旧实现把 to_status 计入收敛键，这里会另开一行（恶化链被拆成多行刷屏）。
	trigger(model.AlertLevelCritical, "offline")
	db.Where("namespace = ? AND server_id = ?", "prod", "s1").Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("方向恶化应并入同一行，实际 %d 行", len(rows))
	}
	if rows[0].ID != one.ID || rows[0].ToStatus != runtime.StatusOffline || rows[0].OccurrenceCount != 7 {
		t.Fatalf("恶化应同行升向且计数递增，实际 %+v", rows[0])
	}

	// 恢复自动消解：该实例只剩这 1 行未恢复告警
	n, err := svc.AutoResolveAlerts("prod", "s1")
	if err != nil {
		t.Fatalf("自动消解失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("应消解 1 行，实际 %d", n)
	}
	db.Where("namespace = ? AND server_id = ?", "prod", "s1").Find(&rows)
	for _, r := range rows {
		if r.Status != model.AlertEventStatusResolved || r.HandledBy != model.AutoResolveOperator {
			t.Fatalf("恢复后应全 resolved 且 handled_by=system，实际 %+v", r)
		}
	}

	// 消解后再触发同键 → 视为新事件，另起一行
	trigger(model.AlertLevelWarning, "lost")
	db.Where("namespace = ? AND server_id = ?", "prod", "s1").Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("已 resolved 后同键再触发应新起一行，实际 %d", len(rows))
	}
}

// TestFR232HealthDeteriorationChainConvergesToOneRow 校验 FR-232 恶化链合并：同一实例的一次健康恶化
// （degraded → lost → offline）只留 1 行——收敛键 (namespace, serverId, type) **不含方向**，中间阶段
// 不再各开一行（prod 实测 10 台实例下线由此从 30 条降到 10 条）。合并取最严重：to_status 只升不降、
// level 取最高、message 取最新，created_at 保持首发时间，occurrence_count 累计跳数。
func TestFR232HealthDeteriorationChainConvergesToOneRow(t *testing.T) {
	svc, db := newAlertEventService(t)
	chain := []struct{ toStatus, level, message string }{
		{runtime.StatusDegraded, model.AlertLevelInfo, "s1 online → degraded"},
		{runtime.StatusLost, model.AlertLevelWarning, "s1 degraded → lost"},
		{runtime.StatusOffline, model.AlertLevelWarning, "s1 lost → offline"},
	}
	readRows := func() []model.AlertEvent {
		t.Helper()
		var rows []model.AlertEvent
		db.Where("namespace = ? AND server_id = ?", "prod", "s1").Order("id").Find(&rows)
		return rows
	}

	// 第一跳：degraded 起一条新行，记下 id 与首发时间作后续基准
	if err := svc.Record(&model.AlertEvent{
		Type: model.AlertEventTypeHealthTransition, Level: chain[0].level,
		ToStatus: chain[0].toStatus, Namespace: "prod", ServerID: "s1", Message: chain[0].message,
	}); err != nil {
		t.Fatalf("触发 %s 失败: %v", chain[0].toStatus, err)
	}
	first := readRows()
	if len(first) != 1 {
		t.Fatalf("首发应 1 行，实际 %d", len(first))
	}
	if first[0].ToStatus != runtime.StatusDegraded || first[0].OccurrenceCount != 1 {
		t.Fatalf("首发行应 to_status=degraded、计数 1，实际 %+v", first[0])
	}

	// 后续两跳：每跳后都断言仍只有 1 行——中间阶段各开一行正是本次要修掉的噪音来源
	for i, stage := range chain[1:] {
		if err := svc.Record(&model.AlertEvent{
			Type: model.AlertEventTypeHealthTransition, Level: stage.level,
			ToStatus: stage.toStatus, Namespace: "prod", ServerID: "s1", Message: stage.message,
		}); err != nil {
			t.Fatalf("触发 %s 失败: %v", stage.toStatus, err)
		}
		if rows := readRows(); len(rows) != 1 {
			t.Fatalf("恶化链第 %d 跳后应收敛为 1 行，实际 %d 行", i+2, len(rows))
		}
	}

	got := readRows()[0]
	if got.ID != first[0].ID {
		t.Fatalf("恶化链必须更新同一行：首发 id=%d，现在 id=%d", first[0].ID, got.ID)
	}
	if !got.CreatedAt.Equal(first[0].CreatedAt) {
		t.Fatalf("created_at 应保持首发时间 %v，实际 %v", first[0].CreatedAt, got.CreatedAt)
	}
	if got.ToStatus != runtime.StatusOffline {
		t.Fatalf("方向应取最严重态 offline，实际 %q", got.ToStatus)
	}
	if got.OccurrenceCount != 3 {
		t.Fatalf("计数应累计 3 跳，实际 %d", got.OccurrenceCount)
	}
	if got.Level != model.AlertLevelWarning {
		t.Fatalf("级别应取最高（warning > info），实际 %q", got.Level)
	}
	if got.Message != chain[2].message {
		t.Fatalf("message 应取最新一跳，实际 %q", got.Message)
	}
	if got.Status != model.AlertEventStatusOpen {
		t.Fatalf("未被处理时状态应保持 open，实际 %q", got.Status)
	}
}

// TestFR232HealthStatusNeverDowngrades 方向只升不降：同一收敛行先到 offline，之后再来更轻的 lost
// 不得把方向降回轻态（否则「该实例已下线」这一信号会被一次回光心跳抹掉）；级别与计数照常取最高 / 递增。
func TestFR232HealthStatusNeverDowngrades(t *testing.T) {
	svc, db := newAlertEventService(t)
	trigger := func(level, toStatus, message string) {
		t.Helper()
		if err := svc.Record(&model.AlertEvent{
			Type: model.AlertEventTypeHealthTransition, Level: level,
			ToStatus: toStatus, Namespace: "prod", ServerID: "s1", Message: message,
		}); err != nil {
			t.Fatalf("触发 %s 失败: %v", toStatus, err)
		}
	}
	trigger(model.AlertLevelWarning, runtime.StatusOffline, "s1 lost → offline")
	trigger(model.AlertLevelCritical, runtime.StatusLost, "s1 offline → lost")

	var rows []model.AlertEvent
	db.Where("namespace = ? AND server_id = ?", "prod", "s1").Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("同键应仍为 1 行，实际 %d", len(rows))
	}
	got := rows[0]
	if got.ToStatus != runtime.StatusOffline {
		t.Fatalf("方向不得回落：应保持 offline，实际 %q", got.ToStatus)
	}
	if got.Level != model.AlertLevelCritical {
		t.Fatalf("级别与方向独立：应取最高 critical，实际 %q", got.Level)
	}
	if got.OccurrenceCount != 2 {
		t.Fatalf("计数应递增为 2，实际 %d", got.OccurrenceCount)
	}
	// message 取最新一次触发（反映「此刻发生了什么」），故在方向不回落时它会与 to_status 不同步——
	// 这是刻意取舍：行内 to_status 记最严重态供筛选 / 判级，message 记最近一跳供人读。
	if got.Message != "s1 offline → lost" {
		t.Fatalf("message 应取最新一跳，实际 %q", got.Message)
	}
}

// TestFR232ConvergenceIsScopedPerServerAndType 收敛键的两个边界：不同实例、不同类型都不得互相合并
// （合并逻辑一旦打宽，会把无关告警吞进同一行，比不收敛更危险）。
func TestFR232ConvergenceIsScopedPerServerAndType(t *testing.T) {
	svc, db := newAlertEventService(t)
	record := func(typ, serverID, toStatus, message string) {
		t.Helper()
		if err := svc.Record(&model.AlertEvent{
			Type: typ, Level: model.AlertLevelWarning,
			ToStatus: toStatus, Namespace: "prod", ServerID: serverID, Message: message,
		}); err != nil {
			t.Fatalf("触发失败: %v", err)
		}
	}
	// 先落 s2 一行并快照，供后面断言「另一实例的行未被触碰」
	record(model.AlertEventTypeHealthTransition, "s2", runtime.StatusLost, "s2 online → lost")
	var s2Before model.AlertEvent
	if err := db.Where("namespace = ? AND server_id = ?", "prod", "s2").First(&s2Before).Error; err != nil {
		t.Fatalf("回读 s2 失败: %v", err)
	}

	// s1 连跳两跳 → 自己收敛成 1 行
	record(model.AlertEventTypeHealthTransition, "s1", runtime.StatusDegraded, "s1 online → degraded")
	record(model.AlertEventTypeHealthTransition, "s1", runtime.StatusOffline, "s1 degraded → offline")

	// 另一实例的行一个字段都不该变（计数 / 方向 / 文案 / 状态）
	var s2After model.AlertEvent
	if err := db.First(&s2After, s2Before.ID).Error; err != nil {
		t.Fatalf("回读 s2 失败: %v", err)
	}
	if s2After.OccurrenceCount != 1 || s2After.ToStatus != s2Before.ToStatus ||
		s2After.Message != s2Before.Message || s2After.Status != s2Before.Status {
		t.Fatalf("不同实例不得互相合并：s2 触发前 %+v，触发后 %+v", s2Before, s2After)
	}
	var total int64
	db.Model(&model.AlertEvent{}).Count(&total)
	if total != 2 {
		t.Fatalf("应 s1 / s2 各 1 行，实际 %d 行", total)
	}

	// 不同类型不合并：s1 再来 identity-conflict → 与 health-transition 各成一行，且互不覆盖
	record(model.AlertEventTypeIdentityConflict, "s1", "", "并发身份冲突：s1")
	var s1Rows []model.AlertEvent
	db.Where("namespace = ? AND server_id = ?", "prod", "s1").Order("id").Find(&s1Rows)
	if len(s1Rows) != 2 {
		t.Fatalf("不同类型应各成一行，实际 %d 行", len(s1Rows))
	}
	health := s1Rows[0]
	if health.Type != model.AlertEventTypeHealthTransition || health.ToStatus != runtime.StatusOffline || health.OccurrenceCount != 2 {
		t.Fatalf("health 行不得被其它类型事件触碰，实际 %+v", health)
	}
	conflict := s1Rows[1]
	if conflict.Type != model.AlertEventTypeIdentityConflict || conflict.OccurrenceCount != 1 || conflict.ToStatus != "" {
		t.Fatalf("identity-conflict 应自成一独立行，实际 %+v", conflict)
	}
	// 类型隔离的另一半：identity-conflict 自身也收敛（见 TestFR232IdentityConflictConvergesToOneRow），
	// 但**只并进自己那一行**——收敛键含 type 就是为了这个；合并一旦跨类型打宽，健康恶化行的方向 / 文案
	// 会被身份冲突覆盖成另一件事。
	record(model.AlertEventTypeIdentityConflict, "s1", "", "并发身份冲突：s1（再次检出）")
	db.Where("namespace = ? AND server_id = ?", "prod", "s1").Order("id").Find(&s1Rows)
	if len(s1Rows) != 2 {
		t.Fatalf("同键 identity-conflict 应并入既有行、不新开，实际 %d 行", len(s1Rows))
	}
	if s1Rows[0].ID != health.ID || s1Rows[0].OccurrenceCount != 2 ||
		s1Rows[0].ToStatus != runtime.StatusOffline || s1Rows[0].Message != health.Message {
		t.Fatalf("health 行不得被 identity-conflict 的合并触碰，实际 %+v", s1Rows[0])
	}
	if s1Rows[1].ID != conflict.ID || s1Rows[1].OccurrenceCount != 2 || s1Rows[1].Message != "并发身份冲突：s1（再次检出）" {
		t.Fatalf("identity-conflict 应并入自己那 1 行（计数 2、文案取最新），实际 %+v", s1Rows[1])
	}
}

// TestFR232IdentityConflictConvergesToOneRow 身份冲突纳入收敛（FR-232）：同一实例在未处置期间反复检出
// 并发双实例（同 identityId 交替 bootId）**只保留 1 行**——旧行为「一次冲突一行」让同一台机器互相顶替刷屏，
// 待办数随检测往复无界增长。合并语义与 health-transition 一致：计数累计、last_at 刷新、level 取最高、
// message / detail 取**最新一次**、created_at 保持首发；to_status 仍为空串（该类型不写方向）。
// 取舍：合并后单条 boot 冲突明细只留最近一次，逐次历史由 identity.conflict_detected 审计追溯
// （见 TestConflictAuditPerOccurrenceAndAlertConverges）。
func TestFR232IdentityConflictConvergesToOneRow(t *testing.T) {
	svc, db := newAlertEventService(t)
	readRows := func() []model.AlertEvent {
		t.Helper()
		var rows []model.AlertEvent
		db.Where("namespace = ? AND server_id = ?", "prod", "s1").Order("id").Find(&rows)
		return rows
	}
	repeats := []struct{ level, message, detail string }{
		{model.AlertLevelWarning, "并发身份冲突：s1 检出交替 bootId（identityId id-1，boot-a/boot-b）", `{"identityId":"id-1","peers":[{"bootId":"boot-a"},{"bootId":"boot-b"}]}`},
		{model.AlertLevelCritical, "并发身份冲突：s1 检出交替 bootId（identityId id-1，boot-a/boot-c）", `{"identityId":"id-1","peers":[{"bootId":"boot-a"},{"bootId":"boot-c"}]}`},
		{model.AlertLevelCritical, "并发身份冲突：s1 检出交替 bootId（identityId id-1，boot-b/boot-d）", `{"identityId":"id-1","peers":[{"bootId":"boot-b"},{"bootId":"boot-d"}]}`},
	}
	for i, r := range repeats {
		if err := svc.Record(&model.AlertEvent{
			Type: model.AlertEventTypeIdentityConflict, Level: r.level,
			Namespace: "prod", ServerID: "s1", Message: r.message, Detail: r.detail,
		}); err != nil {
			t.Fatalf("第 %d 次冲突触发失败: %v", i+1, err)
		}
	}
	rows := readRows()
	if len(rows) != 1 {
		t.Fatalf("同键身份冲突应收敛为 1 行，实际 %d 行", len(rows))
	}
	got := rows[0]
	if got.OccurrenceCount != 3 {
		t.Fatalf("计数应累计 3 次检出，实际 %d", got.OccurrenceCount)
	}
	if got.Detail != repeats[2].detail {
		t.Fatalf("detail 应取最后一次冲突明细，实际 %q", got.Detail)
	}
	if got.Message != repeats[2].message {
		t.Fatalf("message 应取最后一次冲突，实际 %q", got.Message)
	}
	if got.Level != model.AlertLevelCritical {
		t.Fatalf("级别应取最高（critical > warning），实际 %q", got.Level)
	}
	if got.ToStatus != "" {
		t.Fatalf("身份冲突不写方向，to_status 应恒为空串，实际 %q", got.ToStatus)
	}
	if got.Status != model.AlertEventStatusOpen {
		t.Fatalf("未被处理时状态应保持 open，实际 %q", got.Status)
	}

	// created_at 保持首发时间、last_at 刷新为最近一次：先记下首发行，再触发一次比对。
	var first model.AlertEvent
	if err := db.First(&first, got.ID).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if got.LastAt == nil {
		t.Fatalf("last_at 应被刷新")
	}
	if err := svc.Record(&model.AlertEvent{
		Type: model.AlertEventTypeIdentityConflict, Level: model.AlertLevelCritical,
		Namespace: "prod", ServerID: "s1", Message: "并发身份冲突：s1（第 4 次）",
	}); err != nil {
		t.Fatalf("第 4 次冲突触发失败: %v", err)
	}
	last := readRows()
	if len(last) != 1 {
		t.Fatalf("仍应收敛为 1 行，实际 %d 行", len(last))
	}
	if last[0].ID != first.ID || !last[0].CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("created_at 应保持首发时间 %v（id=%d），实际 id=%d created_at=%v",
			first.CreatedAt, first.ID, last[0].ID, last[0].CreatedAt)
	}
	if last[0].LastAt == nil || first.LastAt == nil || last[0].LastAt.Before(*first.LastAt) {
		t.Fatalf("last_at 应随最近一次触发前进：上一次 %v，实际 %v", first.LastAt, last[0].LastAt)
	}
	if last[0].OccurrenceCount != 4 {
		t.Fatalf("计数应继续递增为 4，实际 %d", last[0].OccurrenceCount)
	}
}

// TestFR232IdentityConflictConvergesScopedPerServerAndNamespace 收敛键的边界：不同实例、不同 namespace
// 的身份冲突**不得互相合并**——一台机器的冲突合并进另一台的行，会让运维只看到其中一台的 boot 明细而
// 误以为另一台已处置，比不收敛更危险。断言对照行在目标触发前后逐字段不变。
func TestFR232IdentityConflictConvergesScopedPerServerAndNamespace(t *testing.T) {
	svc, db := newAlertEventService(t)
	trigger := func(ns, serverID, message string) {
		t.Helper()
		if err := svc.Record(&model.AlertEvent{
			Type: model.AlertEventTypeIdentityConflict, Level: model.AlertLevelCritical,
			Namespace: ns, ServerID: serverID, Message: message,
			Detail: `{"identityId":"id-` + ns + `-` + serverID + `"}`,
		}); err != nil {
			t.Fatalf("触发 %s/%s 失败: %v", ns, serverID, err)
		}
	}
	// 对照组：同 namespace 的另一台 + 同 serverId 的另一 namespace，各一行
	trigger("prod", "s2", "并发身份冲突：prod/s2")
	trigger("dev", "s1", "并发身份冲突：dev/s1")
	const scoped = "(namespace = ? AND server_id = ?) OR (namespace = ? AND server_id = ?)"
	var before []model.AlertEvent
	db.Where(scoped, "prod", "s2", "dev", "s1").Order("id").Find(&before)
	if len(before) != 2 {
		t.Fatalf("对照组应 2 行，实际 %d", len(before))
	}

	// prod/s1 连检出 3 次 → 只影响自己这一行
	for i := 0; i < 3; i++ {
		trigger("prod", "s1", "并发身份冲突：prod/s1")
	}

	var after []model.AlertEvent
	db.Where(scoped, "prod", "s2", "dev", "s1").Order("id").Find(&after)
	if len(after) != 2 {
		t.Fatalf("对照组行数不应变化，实际 %d 行", len(after))
	}
	for i := range after {
		if after[i].ID != before[i].ID || after[i].OccurrenceCount != before[i].OccurrenceCount ||
			after[i].Message != before[i].Message || after[i].Detail != before[i].Detail ||
			after[i].Status != before[i].Status {
			t.Fatalf("不同实例 / 不同 namespace 不得互相合并：触发前 %+v，触发后 %+v", before[i], after[i])
		}
	}
	var target model.AlertEvent
	if err := db.Where("namespace = ? AND server_id = ?", "prod", "s1").First(&target).Error; err != nil {
		t.Fatalf("回读 prod/s1 失败: %v", err)
	}
	if target.OccurrenceCount != 3 || target.ID == before[0].ID || target.ID == before[1].ID {
		t.Fatalf("prod/s1 应自成一收敛行（计数 3），实际 %+v", target)
	}
}

// TestFR232IdentityConflictStatusNotReopenedAndResolvedRestartsRow 身份冲突合并行的状态机边界
// （与 health-transition 同口径）：已 `acknowledged` 的行再检出冲突**不回退 `open`**、只累计计数并取最新文案；
// 已 `resolved` 的行不参与合并，同键再检出**另起一行**（旧行是一个已闭事件，新行是新事件）。
func TestFR232IdentityConflictStatusNotReopenedAndResolvedRestartsRow(t *testing.T) {
	svc, db := newAlertEventService(t)
	trigger := func(message string) {
		t.Helper()
		if err := svc.Record(&model.AlertEvent{
			Type: model.AlertEventTypeIdentityConflict, Level: model.AlertLevelCritical,
			Namespace: "prod", ServerID: "s1", Message: message,
		}); err != nil {
			t.Fatalf("触发失败: %v", err)
		}
	}
	trigger("并发身份冲突：s1（首发）")
	var first model.AlertEvent
	if err := db.Where("namespace = ? AND server_id = ?", "prod", "s1").First(&first).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if _, err := svc.Handle(first.ID, "acknowledge", "", "alice", ""); err != nil {
		t.Fatalf("acknowledge 失败: %v", err)
	}
	trigger("并发身份冲突：s1（再次检出）")
	var acknowledged model.AlertEvent
	db.First(&acknowledged, first.ID)
	if acknowledged.Status != model.AlertEventStatusAcknowledged {
		t.Fatalf("已确认行不应回退 open，实际 %q", acknowledged.Status)
	}
	if acknowledged.OccurrenceCount != 2 || acknowledged.Message != "并发身份冲突：s1（再次检出）" {
		t.Fatalf("已确认行仍应合并计数并取最新文案，实际 %+v", acknowledged)
	}

	if _, err := svc.Handle(first.ID, "resolve", "", "alice", ""); err != nil {
		t.Fatalf("resolve 失败: %v", err)
	}
	trigger("并发身份冲突：s1（消解后再检出）")
	var rows []model.AlertEvent
	db.Where("namespace = ? AND server_id = ?", "prod", "s1").Order("id").Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("已 resolved 后同键再检出应另起一行，实际 %d 行", len(rows))
	}
	if rows[0].Status != model.AlertEventStatusResolved || rows[0].OccurrenceCount != 2 {
		t.Fatalf("旧行应保持 resolved 且计数不再变动，实际 %+v", rows[0])
	}
	if rows[1].Status != model.AlertEventStatusOpen || rows[1].OccurrenceCount != 1 ||
		rows[1].Message != "并发身份冲突：s1（消解后再检出）" {
		t.Fatalf("新行应为 open、计数 1、记新文案，实际 %+v", rows[1])
	}
}

// TestFR230ContextRespectsObservationScope 校验 FR-230：受限观测范围下，详情时间线锁定该告警所属
// namespace 且不越出范围——serverId 跨 namespace 重名时按 namespace 收敛，集群级告警按 namespace 退化。
func TestFR230ContextRespectsObservationScope(t *testing.T) {
	svc, db := newAlertEventService(t)
	base := time.Now().UTC()
	mk := func(ns, serverID, msg string) uint {
		t.Helper()
		e := &model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning, Namespace: ns, ServerID: serverID, Message: msg, Status: model.AlertEventStatusOpen, OccurrenceCount: 1, CreatedAt: base}
		if err := db.Create(e).Error; err != nil {
			t.Fatalf("seed 失败: %v", err)
		}
		return e.ID
	}
	prodServer := mk("prod", "same-id", "prod-server")
	mk("dev", "same-id", "dev-server")      // 同 serverId 但不同 namespace
	prodCluster := mk("prod", "", "prod-c") // 集群级
	mk("dev", "", "dev-c")

	scopedProd := ObservationScope{NamespaceCodes: []string{"prod"}}

	// serverId 分支：仅 prod 的 same-id，dev 同名不串入
	res, err := svc.Context(prodServer, scopedProd)
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	if len(res.Timeline) != 1 || res.Timeline[0].Namespace != "prod" {
		t.Fatalf("受限下应只含 prod 的 same-id，实际 %+v", res.Timeline)
	}

	// 集群级分支：退化为该 namespace 近期告警——只含 prod（可含同 namespace 的实例级），不含 dev。
	res2, err := svc.Context(prodCluster, scopedProd)
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	if len(res2.Timeline) == 0 {
		t.Fatalf("集群级应退化返回该 namespace 告警")
	}
	for _, e := range res2.Timeline {
		if e.Namespace != "prod" {
			t.Fatalf("受限下集群级不应越出 prod，实际含 %q", e.Namespace)
		}
	}
}
