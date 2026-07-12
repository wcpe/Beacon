package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
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
	if err := db.AutoMigrate(&model.AlertEvent{}, &model.AuditLog{}); err != nil {
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
