//go:build integration

package server_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/alert"
	"github.com/wcpe/Beacon/apps/server/internal/service"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// seedAlertEvent 经真实 DB 仓库直插一条告警事件，供 REST 过滤验证。
func seedAlertEvent(t *testing.T, repo *repository.AlertEventRepository, typ, level, ns, serverID string, at time.Time) {
	t.Helper()
	if err := repo.Create(&model.AlertEvent{
		Type: typ, Level: level, Namespace: ns, ServerID: serverID,
		Message: serverID + " online → " + level, Detail: `{"status":"lost"}`, CreatedAt: at,
	}); err != nil {
		t.Fatalf("写告警事件失败: %v", err)
	}
}

// TestAlertEventListRESTFilter 集成（真 MySQL 验可移植 + 落库 + 过滤）：
// 建表后直插多条，经 HTTP 验类型/级别/环境/时间过滤与分页（时间倒序）。
func TestAlertEventListRESTFilter(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	db := testsupport.OpenTestDB(t, "server")
	repo := repository.NewAlertEventRepository(db)

	base := time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC)
	seedAlertEvent(t, repo, model.AlertEventTypeHealthTransition, model.AlertLevelWarning, "prod", "lobby-1", base)
	seedAlertEvent(t, repo, model.AlertEventTypeHealthTransition, model.AlertLevelCritical, "prod", "lobby-2", base.Add(time.Minute))
	seedAlertEvent(t, repo, model.AlertEventTypeHealthTransition, model.AlertLevelCritical, "dev", "arena-1", base.Add(2*time.Minute))

	// ① 无过滤：3 条，时间倒序（最新 arena-1 在前）
	code, all := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/alert-events", nil)
	if code != http.StatusOK {
		t.Fatalf("查告警事件应 200，实际 %d", code)
	}
	items := asSlice(all["items"])
	if len(items) != 3 {
		t.Fatalf("应有 3 条，实际 %v", all["items"])
	}
	first, _ := items[0].(map[string]any)
	if first["serverId"] != "arena-1" {
		t.Fatalf("时间倒序最新应为 arena-1，实际 %v", first["serverId"])
	}

	// ② 按级别过滤：critical → 2 条
	code, crit := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/alert-events?level=critical", nil)
	if code != http.StatusOK || len(asSlice(crit["items"])) != 2 {
		t.Fatalf("critical 过滤应 2 条，实际 %d %v", code, crit["items"])
	}

	// ③ 按环境过滤：dev → 1 条 arena-1
	code, devNs := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/alert-events?namespace=dev", nil)
	if code != http.StatusOK || len(asSlice(devNs["items"])) != 1 {
		t.Fatalf("namespace=dev 应 1 条，实际 %d %v", code, devNs["items"])
	}

	// ④ 按时间过滤：from 取第二条之后 → 排除 lobby-1（base 那条）
	from := base.Add(30 * time.Second).Format(time.RFC3339)
	code, win := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/alert-events?from="+from, nil)
	if code != http.StatusOK || len(asSlice(win["items"])) != 2 {
		t.Fatalf("from 过滤应 2 条，实际 %d %v", code, win["items"])
	}
}

// TestAlertEventPersistAlerterRecords 集成：persist 通道经真实 DB 把健康告警落 alert_event，
// 模拟健康流转触发持久化（FR-89 验收）；经 HTTP 读回验类型/级别。
func TestAlertEventPersistAlerterRecords(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	db := testsupport.OpenTestDB(t, "server")
	svc := service.NewAlertEventService(db, repository.NewAlertEventRepository(db), repository.NewAuditLogRepository(db))

	// persist 通道 + Dispatcher 扇出，等价健康扫描循环里对一次异常转移的派发
	d := alert.NewDispatcher(alert.NewPersistAlerter(svc))
	d.Dispatch(context.Background(), alert.Alert{
		Namespace: "prod", ServerID: "boss-1", Address: "10.0.0.9:25565",
		PrevStatus: "online", Status: "lost", At: time.Now().UTC(),
	})

	code, got := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/alert-events?type=health-transition&namespace=prod", nil)
	if code != http.StatusOK {
		t.Fatalf("查告警事件应 200，实际 %d", code)
	}
	items := asSlice(got["items"])
	// 注意：测试库可能残留其它用例数据，至少应含本次落的 boss-1 critical 一条
	found := false
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["serverId"] == "boss-1" {
			found = true
			if m["level"] != "critical" {
				t.Fatalf("lost 应映射 critical，实际 %v", m["level"])
			}
		}
	}
	if !found {
		t.Fatalf("应能读回 persist 通道落库的 boss-1 事件，实际 %v", got["items"])
	}
}

// seedOpenAlertEvent 直插一条 open 告警并返回其 id（真 DB，供处理工作流验证）。
func seedOpenAlertEvent(t *testing.T, repo *repository.AlertEventRepository, ns, serverID string) uint {
	t.Helper()
	e := &model.AlertEvent{
		Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning,
		Namespace: ns, ServerID: serverID, Message: serverID + " degraded",
		Status: model.AlertEventStatusOpen, CreatedAt: time.Now().UTC(),
	}
	if err := repo.Create(e); err != nil {
		t.Fatalf("落 open 告警失败: %v", err)
	}
	return e.ID
}

// TestAlertEventHandleWorkflowMySQL 集成（真 MySQL）：加列迁移生效 + /handle 端到端更新状态并写审计，
// List 响应含 status 族字段。验 schema 演进与处理工作流在真 MySQL 一致（FR-157，见 ADR-0064）。
func TestAlertEventHandleWorkflowMySQL(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()
	db := testsupport.OpenTestDB(t, "server")
	repo := repository.NewAlertEventRepository(db)

	// 加列迁移：4 个处理字段在真 MySQL 建成。
	for _, col := range []string{"status", "handled_by", "handled_at", "handle_note"} {
		if !db.Migrator().HasColumn(&model.AlertEvent{}, col) {
			t.Fatalf("alert_event 应含处理列 %q（AutoMigrate 加列未生效）", col)
		}
	}

	id := seedOpenAlertEvent(t, repo, "prod", "lobby-9")

	// POST /handle（前端契约措辞 {status, note}）→ 200，返回 resolved + handledBy。
	code, resp := doJSON(t, http.MethodPost, ts.URL+"/admin/v1/alert-events/"+strconv.FormatUint(uint64(id), 10)+"/handle",
		map[string]any{"status": "resolved", "note": "已重启恢复"})
	if code != http.StatusOK {
		t.Fatalf("handle 应 200，实际 %d %v", code, resp)
	}
	if resp["status"] != model.AlertEventStatusResolved {
		t.Fatalf("应返回 resolved，实际 %v", resp["status"])
	}
	if resp["handledBy"] == nil {
		t.Fatalf("handledBy 应非 null，实际 %v", resp["handledBy"])
	}

	// GET 列表：该事件状态已为 resolved，且含处理说明。
	code, list := doJSON(t, http.MethodGet, ts.URL+"/admin/v1/alert-events?namespace=prod", nil)
	if code != http.StatusOK {
		t.Fatalf("列表应 200，实际 %d", code)
	}
	var target map[string]any
	for _, it := range asSlice(list["items"]) {
		m, _ := it.(map[string]any)
		if m["serverId"] == "lobby-9" {
			target = m
		}
	}
	if target == nil || target["status"] != model.AlertEventStatusResolved || target["handleNote"] != "已重启恢复" {
		t.Fatalf("列表中处理后的事件应 resolved 且含说明，实际 %v", target)
	}

	// 专项审计落库（真 MySQL）：action=alert-event.resolve，target=alert-event/<id>。
	var n int64
	if err := db.Model(&model.AuditLog{}).
		Where("action = ? AND target_ref = ?", model.ActionAlertEventResolve, strconv.FormatUint(uint64(id), 10)).
		Count(&n).Error; err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("应有 1 条 alert-event.resolve 审计，实际 %d", n)
	}
}

// TestAlertEventActiveCountsMySQL 集成（真 MySQL）：ActiveCounts 的 GROUP BY 在真 MySQL 正确按实例聚合 open，
// acknowledged / resolved 不计——即 activeAlerts 真值来源在真 MySQL 一致（可移植，无方言函数）。
func TestAlertEventActiveCountsMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "server")
	repo := repository.NewAlertEventRepository(db)
	svc := service.NewAlertEventService(db, repo, repository.NewAuditLogRepository(db))

	seedOpenAlertEvent(t, repo, "prod", "s1")
	seedOpenAlertEvent(t, repo, "prod", "s1")
	acked := seedOpenAlertEvent(t, repo, "prod", "s2")
	if _, err := svc.Handle(acked, "acknowledge", "", testAuthUser, "10.0.0.1"); err != nil {
		t.Fatalf("acknowledge 失败: %v", err)
	}
	resolved := seedOpenAlertEvent(t, repo, "prod", "s1")
	if _, err := svc.Handle(resolved, "resolve", "", testAuthUser, "10.0.0.1"); err != nil {
		t.Fatalf("resolve 失败: %v", err)
	}

	counts, err := svc.ActiveCounts()
	if err != nil {
		t.Fatalf("ActiveCounts 失败: %v", err)
	}
	if counts[service.AlertActiveKey{Namespace: "prod", ServerID: "s1"}] != 2 {
		t.Fatalf("prod/s1 应 2 条 open（1 条已 resolve 不计），实际 %d", counts[service.AlertActiveKey{Namespace: "prod", ServerID: "s1"}])
	}
	if _, ok := counts[service.AlertActiveKey{Namespace: "prod", ServerID: "s2"}]; ok {
		t.Fatalf("prod/s2 已 acknowledge，不应计入活跃")
	}
}
