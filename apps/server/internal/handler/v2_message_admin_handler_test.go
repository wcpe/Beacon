package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// messageItemKeys 是 contracts MessageItem 的全部键（逐键契约断言用；**不含 payload**）。
var messageItemKeys = []string{
	"messageId", "namespaceId", "sourceServerId", "msgType", "targetKind", "targetServerId",
	"targetPlayer", "resolvedServerId", "targetNamespaceId", "crossNamespace", "correlationId",
	"status", "failReason", "createdAt", "dispatchedAt", "deliveredAt", "durationMs",
	"hopCount", "payloadSize", "payloadStored",
}

// msgSeed 是一条消息的造数入参。
type msgSeed struct {
	id            string
	src           string
	msgType       string
	targetKind    string
	targetServer  string
	targetPlayer  string
	resolved      string
	status        string
	failReason    string
	correlationID string
	crossNS       bool
	durationMs    *int64
	hops          string
	payload       string // 非空则落 payload 表
}

// newMsgAdminRouter 构造挂 sqlite 真仓库的消息管理面路由（payload 路由前置注入操作者身份）。
func newMsgAdminRouter(t *testing.T, name string) (chi.Router, *repository.MessageRepository, *repository.AuditLogRepository) {
	t.Helper()
	db := openMsgAdminDB(t, name)
	repo := repository.NewMessageRepository(db)
	auditRepo := repository.NewAuditLogRepository(db)
	h := NewV2MessageAdminHandler(service.NewMessageQueryService(repo), service.NewMessagePayloadService(repo, auditRepo))
	r := chi.NewRouter()
	r.Get("/admin/v2/messages", h.List)
	r.Get("/admin/v2/messages/stats", h.Stats)
	r.Get("/admin/v2/messages/{messageId}", h.Detail)
	// payload 路由前置注入操作者（模拟 adminAuthMiddleware），供审计记 operator。
	r.With(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithOperator(req.Context(), "tester")))
		})
	}).Post("/admin/v2/messages/{messageId}/payload", h.Payload)
	return r, repo, auditRepo
}

func openMsgAdminDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: "file:" + name + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	t.Cleanup(func() { store.Close(db) })
	return db
}

// seedMsg 造一条消息元数据（+可选 payload）落日表。
func seedMsg(t *testing.T, repo *repository.MessageRepository, s msgSeed) {
	t.Helper()
	ms, _ := store.TimeMsFromUUIDv7(s.id)
	created := time.UnixMilli(ms).UTC()
	hops := s.hops
	if hops == "" {
		hops = "[]"
	}
	trace := model.MsgTrace{
		MessageID: s.id, NamespaceID: 1, SourceServerID: s.src, MsgType: s.msgType,
		TargetKind: s.targetKind, TargetServerID: s.targetServer, TargetPlayer: s.targetPlayer,
		ResolvedServerID: s.resolved, CrossNamespace: s.crossNS, CorrelationID: s.correlationID,
		Status: s.status, FailReason: s.failReason, CreatedAt: created, DurationMs: s.durationMs,
		HopCount: 1, Hops: hops,
	}
	rec := model.MessageRecord{Trace: trace}
	if s.payload != "" {
		sum := sha256.Sum256([]byte(s.payload))
		trace.PayloadSize = len(s.payload)
		trace.PayloadStored = true
		rec.Trace = trace
		rec.Payload = &model.MsgPayload{
			MessageID: s.id, Payload: s.payload, SHA256: hex.EncodeToString(sum[:]),
			Size: len(s.payload), CreatedAt: created,
		}
	}
	if _, err := repo.FlushDaily([]model.MessageRecord{rec}); err != nil {
		t.Fatalf("造消息失败: %v", err)
	}
}

// postJSON 对路由发一次带 body 的 POST 并解析 json 响应。
func postJSON(t *testing.T, r chi.Router, target string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var out map[string]any
	if len(rec.Body.Bytes()) > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应非 json: %v（%s）", err, rec.Body.String())
		}
	}
	return rec.Code, out
}

// TestMsgAdminListNoPayload 列表：CursorPage{items,nextCursor} + 列表项 20 键对齐 contracts，**响应不含 payload 字段**。
func TestMsgAdminListNoPayload(t *testing.T) {
	r, repo, _ := newMsgAdminRouter(t, "msg_adm_list")
	base := time.Now().UTC().Add(-5 * time.Minute).UnixMilli()
	seedMsg(t, repo, msgSeed{id: uuidV7AtHandler(base, "m1"), src: "game-1", msgType: "chat:cross",
		targetKind: model.MsgTargetKindServer, targetServer: "game-2", resolved: "game-2",
		status: model.MsgStatusDelivered, payload: `{"secret":"top"}`})

	from, to := isoOf(base-time.Hour.Milliseconds()), isoOf(base+time.Hour.Milliseconds())
	code, body := getJSON(t, r, fmt.Sprintf("/admin/v2/messages?serverId=game-1&from=%s&to=%s", from, to))
	if code != http.StatusOK {
		t.Fatalf("应 200，实际 %d：%v", code, body)
	}
	assertKeys(t, body, "items", "nextCursor")
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("应 1 条，实际 %d", len(items))
	}
	item, _ := items[0].(map[string]any)
	assertKeys(t, item, messageItemKeys...) // 键集合精确匹配即已保证无多余 payload 键
	if _, has := item["payload"]; has {
		t.Fatalf("消息列表项绝不应含 payload 字段")
	}
	if item["payloadStored"] != true || item["payloadSize"] != float64(len(`{"secret":"top"}`)) {
		t.Fatalf("payload 元信息不符: %v", item)
	}
}

// TestMsgAdminDetailContract 详情：22 键（元数据 + hops + correlated），payload 仅元信息、无 payload 字段；未命中 404。
func TestMsgAdminDetailContract(t *testing.T) {
	r, repo, _ := newMsgAdminRouter(t, "msg_adm_detail")
	base := time.Now().UTC().Add(-4 * time.Minute).UnixMilli()
	reqID := uuidV7AtHandler(base, "rq")
	respID := uuidV7AtHandler(base+50, "rp")
	hops := `[{"seq":0,"node":"game-1","event":"sent","at":"2026-07-12T08:00:00.000Z"},{"seq":1,"node":"beacon","event":"received","at":"2026-07-12T08:00:00.003Z","costMs":3}]`
	// 请求消息（correlationId 自引用），响应消息（correlationId 指回请求）。
	seedMsg(t, repo, msgSeed{id: reqID, src: "game-1", msgType: "rpc:call", targetKind: model.MsgTargetKindServer,
		targetServer: "game-2", resolved: "game-2", status: model.MsgStatusDelivered, correlationID: reqID,
		hops: hops, payload: "req-body"})
	seedMsg(t, repo, msgSeed{id: respID, src: "game-2", msgType: "rpc:reply", targetKind: model.MsgTargetKindServer,
		targetServer: "game-1", resolved: "game-1", status: model.MsgStatusDelivered, correlationID: reqID})

	code, body := getJSON(t, r, "/admin/v2/messages/"+reqID)
	if code != http.StatusOK {
		t.Fatalf("详情应 200，实际 %d：%v", code, body)
	}
	assertKeys(t, body, append([]string{"hops", "correlated"}, messageItemKeys...)...)
	if _, has := body["payload"]; has {
		t.Fatalf("消息详情绝不应含 payload 字段")
	}
	hopsArr, _ := body["hops"].([]any)
	if len(hopsArr) != 2 {
		t.Fatalf("hops 应 2 条，实际 %v", body["hops"])
	}
	hop0, _ := hopsArr[0].(map[string]any)
	assertKeys(t, hop0, "seq", "node", "event", "at") // seq0 无 costMs（omitempty）
	corr, _ := body["correlated"].(map[string]any)
	if corr == nil {
		t.Fatalf("请求消息应关联到响应消息，实际 correlated=null")
	}
	assertKeys(t, corr, "messageId", "msgType", "status")
	if corr["messageId"] != respID {
		t.Fatalf("correlated 应指向响应消息 %s，实际 %v", respID, corr["messageId"])
	}

	if code, b := getJSON(t, r, "/admin/v2/messages/"+uuidV7AtHandler(base, "no")); code != http.StatusNotFound || b["code"] != "message_not_found" {
		t.Fatalf("未命中应 404 message_not_found，实际 %d %v", code, b)
	}
}

// TestMsgAdminCorrelationDirect correlationId 直查：返回往返两条（请求 + 响应），免时间范围。
func TestMsgAdminCorrelationDirect(t *testing.T) {
	r, repo, _ := newMsgAdminRouter(t, "msg_adm_corr")
	base := time.Now().UTC().Add(-6 * time.Minute).UnixMilli()
	reqID := uuidV7AtHandler(base, "cq")
	respID := uuidV7AtHandler(base+40, "cp")
	seedMsg(t, repo, msgSeed{id: reqID, src: "game-1", msgType: "rpc:call", targetKind: model.MsgTargetKindServer,
		targetServer: "game-2", resolved: "game-2", status: model.MsgStatusDelivered, correlationID: reqID})
	seedMsg(t, repo, msgSeed{id: respID, src: "game-2", msgType: "rpc:reply", targetKind: model.MsgTargetKindServer,
		targetServer: "game-1", resolved: "game-1", status: model.MsgStatusDelivered, correlationID: reqID})

	code, body := getJSON(t, r, "/admin/v2/messages?correlationId="+reqID)
	if code != http.StatusOK {
		t.Fatalf("correlationId 直查应 200，实际 %d", code)
	}
	if items, _ := body["items"].([]any); len(items) != 2 {
		t.Fatalf("correlationId 直查应得往返两条，实际 %d", len(items))
	}
}

// TestMsgAdminStatsEdge stats(groupBy=edge)：{edges:[MessageEdgeStat]}，失败率 / 样本 / top 原因正确。
func TestMsgAdminStatsEdge(t *testing.T) {
	r, repo, _ := newMsgAdminRouter(t, "msg_adm_edge")
	base := time.Now().UTC().Add(-3 * time.Minute).UnixMilli()
	// 同一 game-1→game-2 边：1 delivered + 1 failed(ack_timeout)。
	dur := int64(120)
	seedMsg(t, repo, msgSeed{id: uuidV7AtHandler(base, "e1"), src: "game-1", msgType: "chat", targetKind: model.MsgTargetKindServer,
		targetServer: "game-2", resolved: "game-2", status: model.MsgStatusDelivered, durationMs: &dur})
	seedMsg(t, repo, msgSeed{id: uuidV7AtHandler(base+10, "e2"), src: "game-1", msgType: "chat", targetKind: model.MsgTargetKindServer,
		targetServer: "game-2", resolved: "game-2", status: model.MsgStatusFailed, failReason: model.MsgFailAckTimeout})

	code, body := getJSON(t, r, fmt.Sprintf("/admin/v2/messages/stats?groupBy=edge&from=%s&to=%s", isoOf(base-time.Hour.Milliseconds()), isoOf(base+time.Hour.Milliseconds())))
	if code != http.StatusOK {
		t.Fatalf("stats 应 200，实际 %d：%v", code, body)
	}
	assertKeys(t, body, "edges")
	edges, _ := body["edges"].([]any)
	if len(edges) != 1 {
		t.Fatalf("应 1 条边，实际 %d", len(edges))
	}
	edge, _ := edges[0].(map[string]any)
	assertKeys(t, edge, "sourceServerId", "resolvedServerId", "total", "failed", "expired",
		"failRatePercent", "p95DurationMs", "topFailReasons", "sampleMessageIds")
	if edge["total"] != float64(2) || edge["failed"] != float64(1) || edge["failRatePercent"] != float64(50) {
		t.Fatalf("边聚合计数不符: %v", edge)
	}
	if samples, _ := edge["sampleMessageIds"].([]any); len(samples) != 1 {
		t.Fatalf("应 1 条失败样本，实际 %v", edge["sampleMessageIds"])
	}
	reasons, _ := edge["topFailReasons"].([]any)
	if len(reasons) != 1 {
		t.Fatalf("应 1 条 top 原因，实际 %v", edge["topFailReasons"])
	}
	rc, _ := reasons[0].(map[string]any)
	assertKeys(t, rc, "reason", "count")
	if rc["reason"] != model.MsgFailAckTimeout {
		t.Fatalf("top 原因不符: %v", rc)
	}
}

// TestMsgAdminPayloadViewAudits payload 查看：先写审计后返回内容；审计记 message.payload.view、含原因、**不含 payload**。
func TestMsgAdminPayloadViewAudits(t *testing.T) {
	r, repo, auditRepo := newMsgAdminRouter(t, "msg_adm_payload")
	base := time.Now().UTC().Add(-2 * time.Minute).UnixMilli()
	mid := uuidV7AtHandler(base, "pv")
	secret := `{"token":"s3cr3t-abc"}`
	seedMsg(t, repo, msgSeed{id: mid, src: "game-1", msgType: "economy:sync", targetKind: model.MsgTargetKindServer,
		targetServer: "game-2", resolved: "game-2", status: model.MsgStatusDelivered, payload: secret})

	// 缺原因 400 missing_reason，且不写审计。
	code, body := postJSON(t, r, "/admin/v2/messages/"+mid+"/payload", map[string]any{})
	if code != http.StatusBadRequest || body["code"] != "missing_reason" {
		t.Fatalf("缺原因应 400 missing_reason，实际 %d %v", code, body)
	}

	// 有原因 200，返回 payload/sha256/size。
	code, body = postJSON(t, r, "/admin/v2/messages/"+mid+"/payload", map[string]any{"reason": "排查跨服经济异常"})
	if code != http.StatusOK {
		t.Fatalf("查看应 200，实际 %d：%v", code, body)
	}
	assertKeys(t, body, "payload", "sha256", "size")
	if body["payload"] != secret || body["size"] != float64(len(secret)) {
		t.Fatalf("payload 返回不符: %v", body)
	}

	// 审计：恰有一条 message.payload.view，含原因原文、messageId，detail 绝不含 payload 内容。
	audits, _, err := auditRepo.List(repository.AuditFilter{Action: model.ActionMessagePayloadView, Page: 1, Size: 50})
	if err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	if len(audits) != 1 {
		t.Fatalf("应恰有 1 条 payload 查看审计，实际 %d", len(audits))
	}
	a := audits[0]
	if a.Operator != "tester" || a.TargetRef != mid || a.Action != model.ActionMessagePayloadView {
		t.Fatalf("审计字段不符: %+v", a)
	}
	if !strings.Contains(a.Detail, "排查跨服经济异常") {
		t.Fatalf("审计 detail 应含原因原文，实际 %s", a.Detail)
	}
	if strings.Contains(a.Detail, "s3cr3t") || strings.Contains(a.Detail, secret) {
		t.Fatalf("审计 detail 绝不应含 payload 内容，实际 %s", a.Detail)
	}
}
