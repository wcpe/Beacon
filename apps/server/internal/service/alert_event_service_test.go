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
func seedOpenEvent(t *testing.T, svc *AlertEventService, ns, serverID string) uint {
	t.Helper()
	e := &model.AlertEvent{
		Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning,
		Namespace: ns, ServerID: serverID, Message: serverID + " degraded",
	}
	if err := svc.Record(e); err != nil {
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
	for i := 0; i < 45; i++ {
		if err := svc.Record(&model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelCritical, Namespace: "prod", ServerID: "s", Message: "m"}); err != nil {
			t.Fatalf("seed critical 失败: %v", err)
		}
	}
	for i := 0; i < 10; i++ {
		if err := svc.Record(&model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning, Namespace: "prod", ServerID: "s", Message: "m"}); err != nil {
			t.Fatalf("seed warning 失败: %v", err)
		}
	}
	for i := 0; i < 5; i++ {
		if err := svc.Record(&model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelCritical, Namespace: "prod", ServerID: "s", Message: "m", Status: model.AlertEventStatusResolved}); err != nil {
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
	anchor := &model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelCritical, Namespace: "prod", ServerID: "s1", Message: "anchor"}
	if err := svc.Record(anchor); err != nil {
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
