package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// newAlertEventTestDB 打开私有内存 sqlite 并迁移 alert_event，供过滤/分页单测（不依赖 MySQL/DSN）。
// 用私有 dsn（非 shared cache）隔离，避免与其它仓库单测共享内存库串扰。
func newAlertEventTestDB(t *testing.T) *AlertEventRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:alertevent_"+t.Name()+"?mode=memory"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AlertEvent{}); err != nil {
		t.Fatalf("迁移 alert_event 失败: %v", err)
	}
	return NewAlertEventRepository(db)
}

func seedAlertEvent(t *testing.T, r *AlertEventRepository, typ, level, ns, serverID string, at time.Time) {
	t.Helper()
	if err := r.Create(&model.AlertEvent{
		Type: typ, Level: level, Namespace: ns, ServerID: serverID,
		Message: serverID + " online → " + level, CreatedAt: at,
	}); err != nil {
		t.Fatalf("写告警事件失败: %v", err)
	}
}

// TestAlertEventListTimeDesc 无过滤按时间倒序返回全部，total 正确。
func TestAlertEventListTimeDesc(t *testing.T) {
	r := newAlertEventTestDB(t)
	base := time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC)
	seedAlertEvent(t, r, model.AlertEventTypeHealthTransition, model.AlertLevelWarning, "prod", "a", base)
	seedAlertEvent(t, r, model.AlertEventTypeHealthTransition, model.AlertLevelCritical, "prod", "b", base.Add(time.Minute))

	items, total, err := r.List(AlertEventFilter{Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("应 2 条，实际 total=%d len=%d", total, len(items))
	}
	if items[0].ServerID != "b" {
		t.Fatalf("时间倒序最新应为 b，实际 %s", items[0].ServerID)
	}
}

// TestAlertEventListFilters 类型/级别/环境/时间过滤各自正确。
func TestAlertEventListFilters(t *testing.T) {
	r := newAlertEventTestDB(t)
	base := time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC)
	seedAlertEvent(t, r, model.AlertEventTypeHealthTransition, model.AlertLevelWarning, "prod", "a", base)
	seedAlertEvent(t, r, model.AlertEventTypeHealthTransition, model.AlertLevelCritical, "prod", "b", base.Add(time.Minute))
	seedAlertEvent(t, r, model.AlertEventTypeHealthTransition, model.AlertLevelCritical, "dev", "c", base.Add(2*time.Minute))

	// 级别过滤
	_, total, _ := r.List(AlertEventFilter{Level: model.AlertLevelCritical, Page: 1, Size: 20})
	if total != 2 {
		t.Fatalf("critical 应 2 条，实际 %d", total)
	}
	// 环境过滤
	_, total, _ = r.List(AlertEventFilter{Namespace: "dev", Page: 1, Size: 20})
	if total != 1 {
		t.Fatalf("dev 应 1 条，实际 %d", total)
	}
	// 类型过滤（不存在的类型）
	_, total, _ = r.List(AlertEventFilter{Type: model.AlertEventTypePublishFail, Page: 1, Size: 20})
	if total != 0 {
		t.Fatalf("publish-fail 应 0 条，实际 %d", total)
	}
	// 时间过滤：from 取第二条起
	from := base.Add(30 * time.Second)
	_, total, _ = r.List(AlertEventFilter{From: from, Page: 1, Size: 20})
	if total != 2 {
		t.Fatalf("from 过滤应 2 条，实际 %d", total)
	}
}

// TestAlertEventActiveCounts ActiveCounts 按 (namespace, serverId) 只聚合 open（GROUP BY 可移植，FR-157）。
func TestAlertEventActiveCounts(t *testing.T) {
	r := newAlertEventTestDB(t)
	mk := func(ns, serverID, status string) {
		if err := r.Create(&model.AlertEvent{
			Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning,
			Namespace: ns, ServerID: serverID, Message: "m", Status: status,
		}); err != nil {
			t.Fatalf("落库失败: %v", err)
		}
	}
	mk("prod", "s1", model.AlertEventStatusOpen)
	mk("prod", "s1", model.AlertEventStatusOpen)
	mk("prod", "s1", model.AlertEventStatusResolved)     // 不计
	mk("prod", "s2", model.AlertEventStatusAcknowledged) // 不计
	mk("dev", "s3", model.AlertEventStatusOpen)

	rows, err := r.ActiveCounts()
	if err != nil {
		t.Fatalf("ActiveCounts 失败: %v", err)
	}
	got := make(map[string]int, len(rows))
	for _, row := range rows {
		got[row.Namespace+"/"+row.ServerID] = row.Count
	}
	if got["prod/s1"] != 2 {
		t.Fatalf("prod/s1 应 2 条 open，实际 %d", got["prod/s1"])
	}
	if _, ok := got["prod/s2"]; ok {
		t.Fatalf("prod/s2 无 open，不应出现")
	}
	if got["dev/s3"] != 1 {
		t.Fatalf("dev/s3 应 1 条 open，实际 %d", got["dev/s3"])
	}
}

// TestAlertEventListPagination 分页：每页 1 条、共 3 条 → 第 2 页取中间一条。
func TestAlertEventListPagination(t *testing.T) {
	r := newAlertEventTestDB(t)
	base := time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC)
	seedAlertEvent(t, r, model.AlertEventTypeHealthTransition, model.AlertLevelWarning, "prod", "a", base)
	seedAlertEvent(t, r, model.AlertEventTypeHealthTransition, model.AlertLevelWarning, "prod", "b", base.Add(time.Minute))
	seedAlertEvent(t, r, model.AlertEventTypeHealthTransition, model.AlertLevelWarning, "prod", "c", base.Add(2*time.Minute))

	items, total, err := r.List(AlertEventFilter{Page: 2, Size: 1})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if total != 3 || len(items) != 1 {
		t.Fatalf("应 total=3 当页 1 条，实际 total=%d len=%d", total, len(items))
	}
	// 时间倒序 c,b,a → 第 2 页（size=1）为 b
	if items[0].ServerID != "b" {
		t.Fatalf("第 2 页应为 b，实际 %s", items[0].ServerID)
	}
}

// TestAlertEventFindUnresolvedByDedupKey 收敛查询按 (namespace, server_id, type) 命中最近一条未恢复行，
// **不看 to_status**（恶化链合并：degraded → lost → offline 是同一行的三个跳，不是三行）；
// 只有 resolved 行时视为未命中（ErrRecordNotFound），由调用方插新行。
func TestAlertEventFindUnresolvedByDedupKey(t *testing.T) {
	r := newAlertEventTestDB(t)
	mk := func(typ, ns, serverID, toStatus, status string) *model.AlertEvent {
		t.Helper()
		e := &model.AlertEvent{
			Type: typ, Level: model.AlertLevelWarning, Namespace: ns, ServerID: serverID,
			ToStatus: toStatus, Status: status, Message: "m", OccurrenceCount: 1,
		}
		if err := r.Create(e); err != nil {
			t.Fatalf("落库失败: %v", err)
		}
		return e
	}
	mk(model.AlertEventTypeHealthTransition, "prod", "s1", "lost", model.AlertEventStatusOpen)
	latest := mk(model.AlertEventTypeHealthTransition, "prod", "s1", "offline", model.AlertEventStatusAcknowledged)
	otherServer := mk(model.AlertEventTypeHealthTransition, "prod", "s2", "lost", model.AlertEventStatusOpen)
	otherType := mk(model.AlertEventTypeIdentityConflict, "prod", "s1", "", model.AlertEventStatusOpen)

	got, err := r.FindUnresolvedByDedupKey("prod", "s1", model.AlertEventTypeHealthTransition)
	if err != nil {
		t.Fatalf("收敛查询失败: %v", err)
	}
	if got.ID != latest.ID {
		t.Fatalf("应命中该键最近一条未恢复行 id=%d，实际 id=%d（方向不同的更早行 %d 也在同键内）",
			latest.ID, got.ID, latest.ID-1)
	}

	// 边界：环境 / 实例 / 类型任一不同都不命中
	for _, tc := range []struct{ ns, serverID, typ string }{
		{"dev", "s1", model.AlertEventTypeHealthTransition},
		{"prod", "s3", model.AlertEventTypeHealthTransition},
		{"prod", "s1", model.AlertEventTypePublishFail},
	} {
		if _, err := r.FindUnresolvedByDedupKey(tc.ns, tc.serverID, tc.typ); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("(%s,%s,%s) 应未命中，实际 %v", tc.ns, tc.serverID, tc.typ, err)
		}
	}
	// 其它实例 / 类型的行仍在（未被误合并或误消解）
	var n int64
	r.db.Model(&model.AlertEvent{}).Where("id IN ?", []uint{otherServer.ID, otherType.ID}).Count(&n)
	if n != 2 {
		t.Fatalf("其它实例 / 类型的行应保留，实际 %d 行", n)
	}

	// 该键全部 resolved 后 → 视为未命中，供调用方插新行
	r.db.Model(&model.AlertEvent{}).Where("namespace = ? AND server_id = ?", "prod", "s1").
		Update("status", model.AlertEventStatusResolved)
	if _, err := r.FindUnresolvedByDedupKey("prod", "s1", model.AlertEventTypeHealthTransition); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("已 resolved 后应未命中，实际 %v", err)
	}
}

// TestAlertEventAutoResolveByNamespace 环境级自动消解（FR-232 环境归档 / 永久删除触发点）：
// 一条 UPDATE 关闭该环境**全部**未恢复行（含无实例的集群级行），且不越出 namespace 边界。
func TestAlertEventAutoResolveByNamespace(t *testing.T) {
	r := newAlertEventTestDB(t)
	now := time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC)
	mk := func(ns, serverID, status string) *model.AlertEvent {
		t.Helper()
		e := &model.AlertEvent{
			Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning,
			Namespace: ns, ServerID: serverID, Message: "m", Status: status,
		}
		if err := r.Create(e); err != nil {
			t.Fatalf("落库失败: %v", err)
		}
		return e
	}
	open := mk("prod", "s1", model.AlertEventStatusOpen)
	acknowledged := mk("prod", "s2", model.AlertEventStatusAcknowledged)
	clusterLevel := mk("prod", "", model.AlertEventStatusOpen)
	otherNamespace := mk("dev", "s1", model.AlertEventStatusOpen)
	alreadyResolved := mk("prod", "s3", model.AlertEventStatusResolved)

	const note = "环境已归档，自动消解"
	n, err := r.AutoResolveByNamespace("prod", now, note)
	if err != nil {
		t.Fatalf("AutoResolveByNamespace 失败: %v", err)
	}
	if n != 3 {
		t.Fatalf("应消解该环境 3 行未恢复告警（含集群级），实际 %d", n)
	}
	for _, want := range []*model.AlertEvent{open, acknowledged, clusterLevel} {
		var got model.AlertEvent
		if err := r.db.First(&got, want.ID).Error; err != nil {
			t.Fatalf("回读告警 %d 失败: %v", want.ID, err)
		}
		if got.Status != model.AlertEventStatusResolved || got.HandledBy != model.AutoResolveOperator || got.HandledAt == nil || got.HandleNote != note {
			t.Fatalf("告警 %d 应被自动消解（resolved / handled_by=system / note=%q），实际 %+v", want.ID, note, got)
		}
	}

	// 防误伤：另一个环境的告警不得被关闭。
	var untouched model.AlertEvent
	if err := r.db.First(&untouched, otherNamespace.ID).Error; err != nil {
		t.Fatalf("回读告警失败: %v", err)
	}
	if untouched.Status != model.AlertEventStatusOpen || untouched.HandledBy != "" || untouched.HandledAt != nil {
		t.Fatalf("另一环境告警不应被触碰，实际 %+v", untouched)
	}
	// 已 resolved 行不参与 UPDATE（不得被重写处理痕迹）。
	var kept model.AlertEvent
	if err := r.db.First(&kept, alreadyResolved.ID).Error; err != nil {
		t.Fatalf("回读告警失败: %v", err)
	}
	if kept.Status != model.AlertEventStatusResolved || kept.HandledBy != "" || kept.HandledAt != nil {
		t.Fatalf("已 resolved 行不应被改写，实际 %+v", kept)
	}

	// 幂等：无未恢复行时 0 行受影响且不报错。
	if again, err := r.AutoResolveByNamespace("prod", now.Add(time.Minute), note); err != nil || again != 0 {
		t.Fatalf("重复执行应 0 行受影响且不报错，n=%d err=%v", again, err)
	}
}
