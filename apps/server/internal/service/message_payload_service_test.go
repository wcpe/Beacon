package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
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

type fakePayloadNamespaceReader struct{}

func (fakePayloadNamespaceReader) FindByID(id uint) (*model.Namespace, error) {
	return &model.Namespace{ID: id, Code: "prod"}, nil
}

func newPayloadServiceForTest(repo *repository.MessageRepository, audit messagePayloadAuditWriter) *MessagePayloadService {
	svc := NewMessagePayloadService(repo, audit)
	svc.SetNamespaceRepository(fakePayloadNamespaceReader{})
	return svc
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

// TestPayloadViewReasonValidation 验证直接读取仍要求有效原因且不会把正文带入审计。
func TestPayloadViewReasonValidation(t *testing.T) {
	repo := repository.NewMessageRepository(openSchedQuerySQLite(t, "payload_reason"))
	audit := &fakeAuditWriter{}
	svc := newPayloadServiceForTest(repo, audit)
	id := uuidV7At(time.Now().UTC().UnixMilli(), "r1")
	seedPayloadMsg(t, repo, id, "body", false)

	for _, reason := range []string{"", "   "} {
		_, err := svc.View(ViewPayloadParams{MessageID: id, Reason: reason, Operator: "op"})
		if !errors.Is(err, apperr.ErrApprovalReasonRequired) {
			t.Fatalf("原因 %q 应被拒绝，实际 %v", reason, err)
		}
	}
	_, err := svc.View(ViewPayloadParams{MessageID: id, Reason: strings.Repeat("字", 256), Operator: "op"})
	if !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("过长原因应被拒绝，实际 %v", err)
	}
	if len(audit.entries) != 0 {
		t.Fatalf("原因非法时不应写审计，实际 %d 条", len(audit.entries))
	}
}

// TestPayloadViewNotFound 验证不存在的消息不会返回正文或写成功审计。
func TestPayloadViewNotFound(t *testing.T) {
	repo := repository.NewMessageRepository(openSchedQuerySQLite(t, "payload_404"))
	audit := &fakeAuditWriter{}
	svc := newPayloadServiceForTest(repo, audit)
	_, err := svc.View(ViewPayloadParams{MessageID: uuidV7At(time.Now().UTC().UnixMilli(), "nf"), Reason: "查", Operator: "op"})
	if !errors.Is(err, apperr.ErrMessageNotFound) {
		t.Fatalf("不存在消息应返回 not found，实际 %v", err)
	}
	if len(audit.entries) != 0 {
		t.Fatalf("消息不存在时不应写审计")
	}
}

// TestPayloadViewAuditFailFailsRequest 验证审计落库失败时不返回正文。
func TestPayloadViewAuditFailFailsRequest(t *testing.T) {
	repo := repository.NewMessageRepository(openSchedQuerySQLite(t, "payload_auditfail"))
	audit := &fakeAuditWriter{failErr: errors.New("审计不可用")}
	svc := newPayloadServiceForTest(repo, audit)
	id := uuidV7At(time.Now().UTC().UnixMilli(), "af")
	seedPayloadMsg(t, repo, id, "机密内容", false)

	res, err := svc.View(ViewPayloadParams{MessageID: id, Reason: "排查", Operator: "op"})
	if err == nil {
		t.Fatalf("审计失败应拒绝返回正文")
	}
	if res.Payload != "" {
		t.Fatalf("审计失败时绝不应返回 payload 内容，实际 %q", res.Payload)
	}
}

// TestPayloadViewRejectsOutsideObservationScope 域外 messageId 不得读取正文或写成功审计。
func TestPayloadViewRejectsOutsideObservationScope(t *testing.T) {
	repo := repository.NewMessageRepository(openSchedQuerySQLite(t, "payload_out_of_scope"))
	audit := &fakeAuditWriter{}
	svc := newPayloadServiceForTest(repo, audit)
	id := uuidV7At(time.Now().UTC().UnixMilli(), "scope")
	seedPayloadMsg(t, repo, id, "域外正文", false)

	result, err := svc.View(ViewPayloadParams{
		MessageID: id, Reason: "排查", Operator: "alice", Scoped: true, NamespaceIDs: []uint{8},
	})
	if !errors.Is(err, apperr.ErrMessageNotFound) || result.Payload != "" {
		t.Fatalf("域外 messageId 应按未命中拒绝，result=%+v err=%v", result, err)
	}
	if len(audit.entries) != 0 {
		t.Fatalf("域外 messageId 不得写成功审计，实际 %d 条", len(audit.entries))
	}
}

// TestPayloadViewSuccessAuditsFirst 验证返回 payload 前已有只含元数据与原因、归属正确 namespace 的审计。
func TestPayloadViewSuccessAuditsFirst(t *testing.T) {
	repo := repository.NewMessageRepository(openSchedQuerySQLite(t, "payload_ok"))
	audit := &fakeAuditWriter{}
	svc := newPayloadServiceForTest(repo, audit)
	id := uuidV7At(time.Now().UTC().UnixMilli(), "ok")
	secret := `{"pw":"top-secret-xyz"}`
	seedPayloadMsg(t, repo, id, secret, true)

	res, err := svc.View(ViewPayloadParams{MessageID: id, Reason: "跨域排查", Operator: "alice"})
	if err != nil || res.Payload != secret || len(audit.entries) != 1 {
		t.Fatalf("直读应返回正文并先写审计：res=%+v audits=%d err=%v", res, len(audit.entries), err)
	}
	if strings.Contains(audit.entries[0].Detail, secret) || !strings.Contains(audit.entries[0].Detail, res.SHA256) {
		t.Fatalf("审计只能含哈希等元数据，detail=%q", audit.entries[0].Detail)
	}
	// reason 必须落进审计 detail：强制填 reason 却只校验不入库，事后无法追溯谁为什么读了正文。
	if !strings.Contains(audit.entries[0].Detail, "跨域排查") {
		t.Fatalf("审计 detail 必须记录原因文本，detail=%q", audit.entries[0].Detail)
	}
	if !json.Valid([]byte(audit.entries[0].Detail)) {
		t.Fatalf("审计 detail 必须是合法 JSON，detail=%q", audit.entries[0].Detail)
	}
	if audit.entries[0].NamespaceCode != "prod" {
		t.Fatalf("payload 读取审计应归属消息 namespace prod，实际 %q", audit.entries[0].NamespaceCode)
	}
}

// TestPayloadViewDirectReadWithAudit 验证消息正文直读不再依赖审批 grant，但仍先写审计。
// 直读是产品意图（不恢复旧审批语义）；本用例只锁「直读 + 必留痕」这条契约。
func TestPayloadViewDirectReadWithAudit(t *testing.T) {
	repo := repository.NewMessageRepository(openSchedQuerySQLite(t, "payload_grant_required"))
	audit := &fakeAuditWriter{}
	svc := newPayloadServiceForTest(repo, audit)
	id := uuidV7At(time.Now().UTC().UnixMilli(), "grant")
	seedPayloadMsg(t, repo, id, "允许直读", false)

	res, err := svc.View(ViewPayloadParams{MessageID: id, Reason: "排查", Operator: "alice"})
	if err != nil || res.Payload != "允许直读" || len(audit.entries) != 1 {
		t.Fatalf("正文读取应无需 grant 且写审计：res=%+v audits=%d err=%v", res, len(audit.entries), err)
	}
	if audit.entries[0].Action != model.ActionMessagePayloadView {
		t.Fatalf("直读必须写 message.payload.view 审计，实际 %q", audit.entries[0].Action)
	}
}

// TestMessagePayloadApprovalGrantFlow 验证申请、批准 worker 激活与回执同事务完成，且正文只能原申请人消费一次。
func TestMessagePayloadApprovalGrantFlow(t *testing.T) {
	db := openSchedQuerySQLite(t, "payload_approval_grant")
	if err := db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}, &model.SensitiveAccessGrant{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移审批表失败：%v", err)
	}
	repo := repository.NewMessageRepository(db)
	grants := NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	registry := authz.NewApprovalRegistry()
	RegisterMessagePayloadApprovalAdapter(registry, repo, grants)
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	svc := NewMessagePayloadService(repo)
	svc.SetSensitiveAccessApproval(approval, grants)
	id := uuidV7At(time.Now().UTC().UnixMilli(), "flow")
	secret := `{"accessToken":"only-once"}`
	seedPayloadMsg(t, repo, id, secret, false)

	requested, err := svc.RequestAccess(id, "排查消息异常", "payload-flow-1", auth.HumanPrincipal("alice"), "")
	if err != nil || requested.Status != model.ApprovalStatusPending {
		t.Fatalf("创建 payload 审批申请失败：request=%+v err=%v", requested, err)
	}
	grantRepo := repository.NewSensitiveAccessGrantRepository(db)
	grant, err := grantRepo.FindByApprovalRequestID(requested.RequestID)
	if err != nil || grant == nil || grant.Status != model.SensitiveAccessGrantStatusPending {
		t.Fatalf("申请后应有 pending grant：grant=%+v err=%v", grant, err)
	}
	if _, err = approval.Approve(requested.RequestID, auth.HumanPrincipal("reviewer"), ""); err != nil {
		t.Fatalf("批准申请失败：%v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("批准 worker 应处理一次：processed=%d err=%v", processed, err)
	}
	grant, err = grantRepo.FindByApprovalRequestID(requested.RequestID)
	if err != nil || grant == nil || grant.Status != model.SensitiveAccessGrantStatusActive {
		t.Fatalf("批准后 grant 应激活：grant=%+v err=%v", grant, err)
	}
	var receipt model.ApprovalExecutionReceipt
	if err := db.Where("request_id = ?", requested.RequestID).First(&receipt).Error; err != nil || receipt.ResultRef != "message/"+id {
		t.Fatalf("批准事务应同时写回执：receipt=%+v err=%v", receipt, err)
	}
	if _, err = svc.Consume(grant.GrantID, id, auth.HumanPrincipal("bob")); !errors.Is(err, apperr.ErrSensitiveAccessWrongPrincipal) {
		t.Fatalf("非原申请主体不得消费：%v", err)
	}
	result, err := svc.Consume(grant.GrantID, id, auth.HumanPrincipal("alice"))
	if err != nil || result.Payload != secret || result.SHA256 != payloadSHA256(secret) {
		t.Fatalf("原申请主体首次消费失败：result=%+v err=%v", result, err)
	}
	if _, err = svc.Consume(grant.GrantID, id, auth.HumanPrincipal("alice")); !errors.Is(err, apperr.ErrSensitiveAccessConsumed) {
		t.Fatalf("重复消费必须拒绝：%v", err)
	}
}

// TestPayloadViewStoredFalseStillAudits 未存储 payload 时不得写成功审计或返回正文。
func TestPayloadViewStoredFalseStillAudits(t *testing.T) {
	repo := repository.NewMessageRepository(openSchedQuerySQLite(t, "payload_none"))
	audit := &fakeAuditWriter{}
	svc := newPayloadServiceForTest(repo, audit)
	id := uuidV7At(time.Now().UTC().UnixMilli(), "no")
	seedPayloadMsg(t, repo, id, "", false) // 无 payload

	res, err := svc.View(ViewPayloadParams{MessageID: id, Reason: "查", Operator: "op"})
	if !errors.Is(err, apperr.ErrMessageNotFound) || res.Payload != "" || len(audit.entries) != 0 {
		t.Fatalf("payload 不存在时应失败且零正文零审计：res=%+v audits=%d err=%v", res, len(audit.entries), err)
	}
}
