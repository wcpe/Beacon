package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// fakeAuditWriter 是审计落库的内存假实现：可注入失败，记录成功写入的条目。
type fakeAuditWriter struct {
	entries []*model.AuditLog
	failErr error
}

func (f *fakeAuditWriter) Create(e *model.AuditLog) error {
	if f.failErr != nil {
		return f.failErr
	}
	f.entries = append(f.entries, e)
	return nil
}

// uuidV7At 构造高 48 位内嵌毫秒的 UUIDv7 文本（service 测试用）。
func uuidV7At(ms int64, seq string) string {
	const d = "0123456789abcdef"
	h2 := func(b byte) string { return string([]byte{d[b>>4], d[b&0x0f]}) }
	p := h2(byte(ms>>40)) + h2(byte(ms>>32)) + h2(byte(ms>>24)) + h2(byte(ms>>16)) +
		"-" + h2(byte(ms>>8)) + h2(byte(ms))
	return p + "-7abc-8def-" + (seq + "000000000000")[:12]
}

// seedPayloadMsg 造一条带 / 不带 payload 的消息落日表。
func seedPayloadMsg(t *testing.T, repo *repository.MessageRepository, id, payload string, crossNS bool) {
	t.Helper()
	ms, _ := store.TimeMsFromUUIDv7(id)
	created := time.UnixMilli(ms).UTC()
	trace := model.MsgTrace{
		MessageID: id, NamespaceID: 7, SourceServerID: "game-1", MsgType: "economy:sync",
		TargetKind: model.MsgTargetKindServer, TargetServerID: "game-2", ResolvedServerID: "game-2",
		CrossNamespace: crossNS, Status: model.MsgStatusDelivered, CreatedAt: created, HopCount: 1, Hops: "[]",
	}
	rec := model.MessageRecord{Trace: trace}
	if payload != "" {
		sum := sha256.Sum256([]byte(payload))
		trace.PayloadSize = len(payload)
		trace.PayloadStored = true
		rec.Trace = trace
		rec.Payload = &model.MsgPayload{
			MessageID: id, Payload: payload, SHA256: hex.EncodeToString(sum[:]), Size: len(payload), CreatedAt: created,
		}
	}
	if _, err := repo.FlushDaily([]model.MessageRecord{rec}); err != nil {
		t.Fatalf("造消息失败: %v", err)
	}
}

// TestPayloadViewReasonValidation 原因必填 ≤255 字：空 / 纯空白 / 超长均 400 missing_reason，且不写审计。
func TestPayloadViewReasonValidation(t *testing.T) {
	repo := repository.NewMessageRepository(openSchedQuerySQLite(t, "payload_reason"))
	audit := &fakeAuditWriter{}
	svc := NewMessagePayloadService(repo, audit)
	id := uuidV7At(time.Now().UTC().UnixMilli(), "r1")
	seedPayloadMsg(t, repo, id, "body", false)

	for _, reason := range []string{"", "   ", strings.Repeat("字", 256)} {
		_, err := svc.View(ViewPayloadParams{MessageID: id, Reason: reason, Operator: "op"})
		if !errors.Is(err, apperr.ErrPayloadReasonRequired) {
			t.Fatalf("原因 %q 应 ErrPayloadReasonRequired，实际 %v", reason, err)
		}
	}
	if len(audit.entries) != 0 {
		t.Fatalf("原因非法时不应写审计，实际 %d 条", len(audit.entries))
	}
}

// TestPayloadViewNotFound 消息不存在 → ErrMessageNotFound，不写审计。
func TestPayloadViewNotFound(t *testing.T) {
	repo := repository.NewMessageRepository(openSchedQuerySQLite(t, "payload_404"))
	audit := &fakeAuditWriter{}
	svc := NewMessagePayloadService(repo, audit)
	_, err := svc.View(ViewPayloadParams{MessageID: uuidV7At(time.Now().UTC().UnixMilli(), "nf"), Reason: "查", Operator: "op"})
	if !errors.Is(err, apperr.ErrMessageNotFound) {
		t.Fatalf("应 ErrMessageNotFound，实际 %v", err)
	}
	if len(audit.entries) != 0 {
		t.Fatalf("消息不存在时不应写审计")
	}
}

// TestPayloadViewAuditFailFailsRequest 审计写失败 → 整请求失败，绝不返回 payload 内容（先审计后返回）。
func TestPayloadViewAuditFailFailsRequest(t *testing.T) {
	repo := repository.NewMessageRepository(openSchedQuerySQLite(t, "payload_auditfail"))
	audit := &fakeAuditWriter{failErr: errors.New("模拟审计落库失败")}
	svc := NewMessagePayloadService(repo, audit)
	id := uuidV7At(time.Now().UTC().UnixMilli(), "af")
	seedPayloadMsg(t, repo, id, "机密内容", false)

	res, err := svc.View(ViewPayloadParams{MessageID: id, Reason: "排查", Operator: "op"})
	if err == nil {
		t.Fatalf("审计写失败应使整请求失败，实际 err=nil")
	}
	if res.Payload != "" {
		t.Fatalf("审计失败时绝不应返回 payload 内容，实际 %q", res.Payload)
	}
}

// TestPayloadViewSuccessAuditsFirst 成功查看：先写审计（含原因原文 / 跨域标记 / 不含 payload）后返回 payload。
func TestPayloadViewSuccessAuditsFirst(t *testing.T) {
	repo := repository.NewMessageRepository(openSchedQuerySQLite(t, "payload_ok"))
	audit := &fakeAuditWriter{}
	svc := NewMessagePayloadService(repo, audit)
	id := uuidV7At(time.Now().UTC().UnixMilli(), "ok")
	secret := `{"pw":"top-secret-xyz"}`
	seedPayloadMsg(t, repo, id, secret, true)

	res, err := svc.View(ViewPayloadParams{MessageID: id, Reason: "跨域排查", Operator: "alice", ClientIP: "10.0.0.1", TraceID: "tid-1"})
	if err != nil {
		t.Fatalf("查看应成功，实际 %v", err)
	}
	if res.Payload != secret || res.Size != len(secret) || res.SHA256 == "" {
		t.Fatalf("payload 返回不符: %+v", res)
	}
	if len(audit.entries) != 1 {
		t.Fatalf("应恰写 1 条审计，实际 %d", len(audit.entries))
	}
	e := audit.entries[0]
	if e.Action != model.ActionMessagePayloadView || e.TargetType != model.TargetTypeMessage || e.TargetRef != id || e.Operator != "alice" {
		t.Fatalf("审计字段不符: %+v", e)
	}
	if !strings.Contains(e.Detail, "跨域排查") || !strings.Contains(e.Detail, `"crossNamespace":true`) {
		t.Fatalf("审计 detail 应含原因与跨域标记，实际 %s", e.Detail)
	}
	if strings.Contains(e.Detail, "top-secret") {
		t.Fatalf("审计 detail 绝不应含 payload 内容，实际 %s", e.Detail)
	}
}

// TestPayloadViewStoredFalseStillAudits payload 未落库（归档 / 未存）时仍先写审计、返回空内容。
func TestPayloadViewStoredFalseStillAudits(t *testing.T) {
	repo := repository.NewMessageRepository(openSchedQuerySQLite(t, "payload_none"))
	audit := &fakeAuditWriter{}
	svc := NewMessagePayloadService(repo, audit)
	id := uuidV7At(time.Now().UTC().UnixMilli(), "no")
	seedPayloadMsg(t, repo, id, "", false) // 无 payload

	res, err := svc.View(ViewPayloadParams{MessageID: id, Reason: "查", Operator: "op"})
	if err != nil {
		t.Fatalf("应成功，实际 %v", err)
	}
	if res.Payload != "" || res.Size != 0 {
		t.Fatalf("payload 未落库应返回空内容，实际 %+v", res)
	}
	if len(audit.entries) != 1 {
		t.Fatalf("payload 未落库仍应写审计，实际 %d", len(audit.entries))
	}
}
