//go:build integration

package server_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime/alert"
	"github.com/wcpe/Beacon/internal/service"
	"github.com/wcpe/Beacon/internal/testsupport"
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
	svc := service.NewAlertEventService(repository.NewAlertEventRepository(db))

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
