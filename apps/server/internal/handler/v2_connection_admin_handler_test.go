package handler

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// connectionItemKeys 是 contracts ConnectionItem 的全部键（逐键契约断言用）。
var connectionItemKeys = []string{
	"connId", "namespaceId", "proxyServerId", "playerUuid", "playerName", "clientIp",
	"protocolVersion", "openedAt", "closedAt", "durationMs", "status", "closeKind",
	"closeReason", "firstBackendServerId", "lastBackendServerId", "backendSwitchCount",
}

// uuidV7AtHandler 构造高 48 位内嵌指定毫秒的 UUIDv7 文本（handler 测试用；seq 落末段保唯一）。
func uuidV7AtHandler(ms int64, seq string) string {
	const d = "0123456789abcdef"
	h2 := func(b byte) string { return string([]byte{d[b>>4], d[b&0x0f]}) }
	p := h2(byte(ms>>40)) + h2(byte(ms>>32)) + h2(byte(ms>>24)) + h2(byte(ms>>16)) +
		"-" + h2(byte(ms>>8)) + h2(byte(ms))
	tail := (seq + "000000000000")[:12]
	return p + "-7abc-8def-" + tail
}

// isoOf 把毫秒时刻格式化为 URL 查询用的 RFC3339 文本。
func isoOf(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano) }

// newConnAdminRouter 构造挂 sqlite 真仓库的连接管理面路由。
func newConnAdminRouter(t *testing.T, name string) (chi.Router, *repository.ConnDetailRepository) {
	t.Helper()
	db, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: "file:" + name + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	t.Cleanup(func() { store.Close(db) })
	repo := repository.NewConnDetailRepository(db)
	h := NewV2ConnectionAdminHandler(service.NewConnQueryService(repo), newTestSettings(t, db))
	r := chi.NewRouter()
	r.Get("/admin/v2/connections", h.List)
	r.Get("/admin/v2/connections/stats", h.Stats)
	r.Get("/admin/v2/connections/{connId}", h.Detail)
	return r, repo
}

// seedConn 造一条 open/close 会话行（openedMs 决定日表；closed 为 true 时补 close 摘要）。
func seedConn(t *testing.T, repo *repository.ConnDetailRepository, connID, proxy, player string, openedMs int64, closed bool, kind string) {
	t.Helper()
	evs := []model.ConnEvent{{
		Kind: model.ConnEventKindOpen, ConnID: connID, NamespaceID: 1, ProxyServerID: proxy,
		PlayerUUID: player, PlayerName: "Steve", ClientIP: "10.0.0.9", ProtocolVersion: 767, OpenedAtMs: openedMs,
	}}
	if closed {
		evs = append(evs, model.ConnEvent{
			Kind: model.ConnEventKindClose, ConnID: connID, NamespaceID: 1, ProxyServerID: proxy,
			PlayerUUID: player, PlayerName: "Steve", ClientIP: "10.0.0.9", ProtocolVersion: 767,
			OpenedAtMs: openedMs, ClosedAtMs: openedMs + 5000, CloseKind: kind, CloseReason: "bye",
			FirstBackend: "game-1", LastBackend: "game-2", BackendSwitchCount: 1,
		})
	}
	if _, err := repo.FlushDaily(evs); err != nil {
		t.Fatalf("造连接行失败: %v", err)
	}
}

// TestConnAdminListContract 列表：CursorPage{items,nextCursor} + 列表项 16 键逐键对齐 contracts ConnectionItem。
func TestConnAdminListContract(t *testing.T) {
	r, repo := newConnAdminRouter(t, "conn_adm_list")
	base := time.Now().UTC().Add(-5 * time.Minute).UnixMilli()
	seedConn(t, repo, uuidV7AtHandler(base, "a1"), "proxy-1", "p-1", base, true, model.ConnCloseKindQuit)
	seedConn(t, repo, uuidV7AtHandler(base+1000, "a2"), "proxy-1", "p-2", base+1000, false, "")

	from, to := isoOf(base-time.Hour.Milliseconds()), isoOf(base+time.Hour.Milliseconds())
	code, body := getJSON(t, r, fmt.Sprintf("/admin/v2/connections?serverId=proxy-1&from=%s&to=%s", from, to))
	if code != http.StatusOK {
		t.Fatalf("应 200，实际 %d：%v", code, body)
	}
	assertKeys(t, body, "items", "nextCursor")
	if body["nextCursor"] != nil {
		t.Fatalf("单页应 nextCursor=null，实际 %v", body["nextCursor"])
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("应 2 条，实际 %d", len(items))
	}
	// opened_at 降序：后开的 open 会话（p-2）在前。
	openRow, _ := items[0].(map[string]any)
	closedRow, _ := items[1].(map[string]any)
	assertKeys(t, openRow, connectionItemKeys...)
	assertKeys(t, closedRow, connectionItemKeys...)
	if openRow["status"] != "open" || openRow["closedAt"] != nil || openRow["durationMs"] != nil ||
		openRow["closeKind"] != nil || openRow["firstBackendServerId"] != nil {
		t.Fatalf("open 行 null 语义不符: %v", openRow)
	}
	if closedRow["status"] != "closed" || closedRow["closeKind"] != "quit" ||
		closedRow["durationMs"] != float64(5000) || closedRow["firstBackendServerId"] != "game-1" ||
		closedRow["lastBackendServerId"] != "game-2" || closedRow["backendSwitchCount"] != float64(1) {
		t.Fatalf("closed 行映射不符: %v", closedRow)
	}
}

// TestConnAdminConnIdDirect connId 直查免时间范围；命中返回单条、未命中返回空 items（非 404）。
func TestConnAdminConnIdDirect(t *testing.T) {
	r, repo := newConnAdminRouter(t, "conn_adm_direct")
	base := time.Now().UTC().Add(-3 * time.Minute).UnixMilli()
	cid := uuidV7AtHandler(base, "b1")
	seedConn(t, repo, cid, "proxy-1", "p-1", base, true, model.ConnCloseKindKick)

	code, body := getJSON(t, r, "/admin/v2/connections?connId="+cid)
	if code != http.StatusOK {
		t.Fatalf("connId 直查应 200，实际 %d", code)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("connId 直查应 1 条，实际 %d", len(items))
	}
	// 未命中的 connId 直查返回空 items（不 404）。
	code, body = getJSON(t, r, "/admin/v2/connections?connId="+uuidV7AtHandler(base, "zz"))
	if code != http.StatusOK {
		t.Fatalf("未命中直查应 200，实际 %d", code)
	}
	if items, _ := body["items"].([]any); len(items) != 0 {
		t.Fatalf("未命中直查应空 items，实际 %d", len(items))
	}
}

// TestConnAdminGuard400 无精确 ID 时：缺时间范围、范围超 168h 均 400。
// 有时间窗但无 serverId/playerUuid 允许「全局近期」列表（管理台默认进页），不应 400。
func TestConnAdminGuard400(t *testing.T) {
	r, _ := newConnAdminRouter(t, "conn_adm_guard")
	now := time.Now().UTC().UnixMilli()
	for name, target := range map[string]string{
		"缺过滤缺范围":   "/admin/v2/connections",
		"有过滤缺范围":   "/admin/v2/connections?serverId=proxy-1",
		"范围超 168h": fmt.Sprintf("/admin/v2/connections?serverId=proxy-1&from=%s&to=%s", isoOf(now-200*time.Hour.Milliseconds()), isoOf(now)),
	} {
		if code, body := getJSON(t, r, target); code != http.StatusBadRequest || body["code"] != "query_guard_violation" {
			t.Fatalf("%s 应 400 query_guard_violation，实际 %d %v", name, code, body)
		}
	}
	// 仅时间窗：允许空过滤，返回 200（可能 0 条）
	code, body := getJSON(t, r, fmt.Sprintf("/admin/v2/connections?from=%s&to=%s", isoOf(now-1000), isoOf(now)))
	if code != http.StatusOK {
		t.Fatalf("有范围缺过滤应 200（全局近期），实际 %d %v", code, body)
	}
}

// TestConnAdminCursorPaging 游标分页：limit 截断 + nextCursor 递进 + 尾页 null。
func TestConnAdminCursorPaging(t *testing.T) {
	r, repo := newConnAdminRouter(t, "conn_adm_cursor")
	base := time.Now().UTC().Add(-10 * time.Minute).UnixMilli()
	for i := 0; i < 3; i++ {
		ms := base + int64(i)*1000
		seedConn(t, repo, uuidV7AtHandler(ms, fmt.Sprintf("c%d", i)), "proxy-1", "p", ms, true, model.ConnCloseKindQuit)
	}
	from, to := isoOf(base-time.Hour.Milliseconds()), isoOf(base+time.Hour.Milliseconds())
	code, body := getJSON(t, r, fmt.Sprintf("/admin/v2/connections?serverId=proxy-1&limit=2&from=%s&to=%s", from, to))
	if code != http.StatusOK {
		t.Fatalf("应 200，实际 %d", code)
	}
	if items, _ := body["items"].([]any); len(items) != 2 {
		t.Fatalf("首页 limit=2 应 2 条，实际 %d", len(items))
	}
	if body["nextCursor"] != "2" {
		t.Fatalf("首页 nextCursor 应 \"2\"，实际 %v", body["nextCursor"])
	}
	code, body = getJSON(t, r, fmt.Sprintf("/admin/v2/connections?serverId=proxy-1&limit=2&cursor=2&from=%s&to=%s", from, to))
	if items, _ := body["items"].([]any); code != http.StatusOK || len(items) != 1 {
		t.Fatalf("次页应 1 条，实际 %d（%d）", len(items), code)
	}
	if body["nextCursor"] != nil {
		t.Fatalf("尾页 nextCursor 应 null，实际 %v", body["nextCursor"])
	}
}

// TestConnAdminDetail404 单条详情：未命中 404 connection_not_found。
func TestConnAdminDetail404(t *testing.T) {
	r, repo := newConnAdminRouter(t, "conn_adm_detail")
	base := time.Now().UTC().Add(-2 * time.Minute).UnixMilli()
	cid := uuidV7AtHandler(base, "d1")
	seedConn(t, repo, cid, "proxy-1", "p-1", base, false, "")

	code, body := getJSON(t, r, "/admin/v2/connections/"+cid)
	if code != http.StatusOK {
		t.Fatalf("详情应 200，实际 %d：%v", code, body)
	}
	assertKeys(t, body, connectionItemKeys...)

	code, body = getJSON(t, r, "/admin/v2/connections/"+uuidV7AtHandler(base, "no"))
	if code != http.StatusNotFound || body["code"] != "connection_not_found" {
		t.Fatalf("未命中应 404 connection_not_found，实际 %d %v", code, body)
	}
}

// TestConnAdminStatsContract stats：{buckets:[{startAt,opens,closes,abnormalCloses,estimatedOpen}]}。
func TestConnAdminStatsContract(t *testing.T) {
	r, repo := newConnAdminRouter(t, "conn_adm_stats")
	// 造若干近时会话：一个异常断开（timeout）落入桶。
	toMs := time.Now().UTC().UnixMilli()
	fromMs := toMs - 10*60_000
	mid := fromMs + 3*60_000
	seedConn(t, repo, uuidV7AtHandler(mid, "s1"), "proxy-1", "p-1", mid, true, model.ConnCloseKindTimeout)
	seedConn(t, repo, uuidV7AtHandler(mid+1000, "s2"), "proxy-1", "p-2", mid+1000, false, "")

	code, body := getJSON(t, r, fmt.Sprintf("/admin/v2/connections/stats?serverId=proxy-1&bucket=1m&from=%s&to=%s", isoOf(fromMs), isoOf(toMs)))
	if code != http.StatusOK {
		t.Fatalf("stats 应 200，实际 %d：%v", code, body)
	}
	assertKeys(t, body, "buckets")
	buckets, _ := body["buckets"].([]any)
	if len(buckets) == 0 {
		t.Fatalf("应有时间桶，实际空")
	}
	first, _ := buckets[0].(map[string]any)
	assertKeys(t, first, "startAt", "opens", "closes", "abnormalCloses", "estimatedOpen")
	var totalOpens, totalAbnormal float64
	for _, b := range buckets {
		m, _ := b.(map[string]any)
		totalOpens += m["opens"].(float64)
		totalAbnormal += m["abnormalCloses"].(float64)
	}
	if totalOpens != 2 {
		t.Fatalf("两条会话应各计一次 open，实际 opens 合计 %v", totalOpens)
	}
	if totalAbnormal != 1 {
		t.Fatalf("一条 timeout 应计异常断开，实际合计 %v", totalAbnormal)
	}
}
