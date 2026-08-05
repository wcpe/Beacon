package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

type sensitiveAccessRequestHookAdapter struct {
	grants *SensitiveAccessGrantService
	fail   error
}

func (sensitiveAccessRequestHookAdapter) Execute(authz.ApprovalRequest, authz.Permit) error {
	return apperr.ErrForbidden
}

func (a sensitiveAccessRequestHookAdapter) PrepareApprovalRequestInTx(tx *gorm.DB, req authz.ApprovalRequest) error {
	_, err := a.grants.WithTx(tx).CreatePending(req.RequestID, req.RequesterType, req.RequesterID, req.Operation.Kind, "message/msg-42", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil {
		return err
	}
	return a.fail
}

// newApprovalTestService 用内存库装配审批服务。
func newApprovalTestService(t *testing.T) (*ApprovalService, *gorm.DB, *int) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}, &model.SensitiveAccessGrant{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移审批表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	for _, tbl := range []string{"approval_request", "approval_execution_receipt", "sensitive_access_grant", "audit_log"} {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	calls := 0
	registry := authz.NewApprovalRegistry()
	registry.Register(authz.OperationDeliveryApprove, authz.TransactionalAdapterFunc(func(_ *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
		calls++
		if permit.RequestID() != req.RequestID {
			t.Fatalf("执行许可应绑定当前申请：permit=%s request=%s", permit.RequestID(), req.RequestID)
		}
		return nil, nil
	}))
	svc := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	return svc, db, &calls
}

// TestApprovalServiceRequiresAuthorization 验证提审必须先通过主体能力授权。
func TestApprovalServiceRequiresAuthorization(t *testing.T) {
	svc, _, _ := newApprovalTestService(t)
	op := approvalOp("auth-denied")
	_, err := svc.Request(op, map[string]any{"id": 42}, auth.Principal{Operator: "viewer"}, "127.0.0.1")
	if !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("缺少能力提审应 403，实际 %v", err)
	}
}

// TestApprovalServiceRequiresReasonAndIdempotencyKey 验证申请原因与幂等键必填。
func TestApprovalServiceRequiresReasonAndIdempotencyKey(t *testing.T) {
	svc, _, _ := newApprovalTestService(t)
	principal := auth.HumanPrincipal("alice")
	op := approvalOp("missing-reason")
	op.Reason = ""
	if _, err := svc.Request(op, map[string]any{"id": 42}, principal, "127.0.0.1"); !errors.Is(err, apperr.ErrApprovalReasonRequired) {
		t.Fatalf("缺少申请原因应拒绝，实际 %v", err)
	}
	op = approvalOp("")
	if _, err := svc.Request(op, map[string]any{"id": 42}, principal, "127.0.0.1"); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("缺少幂等键应拒绝，实际 %v", err)
	}
}

// TestApprovalServiceIdempotentRequest 验证相同主体、操作和幂等键按冻结 hash 幂等。
func TestApprovalServiceIdempotentRequest(t *testing.T) {
	svc, _, _ := newApprovalTestService(t)
	principal := auth.HumanPrincipal("alice")
	op := approvalOp("order-42-approve")

	first, err := svc.Request(op, map[string]any{"id": 42}, principal, "127.0.0.1")
	if err != nil {
		t.Fatalf("首次提审失败: %v", err)
	}
	second, err := svc.Request(op, map[string]any{"id": 42}, principal, "127.0.0.1")
	if err != nil {
		t.Fatalf("重复提审失败: %v", err)
	}
	if first.ID != second.ID || first.RequestID != second.RequestID {
		t.Fatalf("相同冻结内容应复用同一请求，首次 %+v，重复 %+v", first, second)
	}
	if !strings.HasPrefix(first.RequestID, "apr_") || first.FrozenPayloadSHA256 == "" || first.ExpiresAt == nil || first.Version != 1 {
		t.Fatalf("申请公开字段不完整：%+v", first)
	}
	if _, err := svc.Request(op, map[string]any{"id": 43}, principal, "127.0.0.1"); !errors.Is(err, apperr.ErrIdempotencyKeyReused) {
		t.Fatalf("同幂等键不同冻结 hash 应 409，实际 %v", err)
	}
}

// TestApprovalRequestHookSharesTransaction 验证 pending grant 等领域状态只能随新审批申请同事务创建。
func TestApprovalRequestHookSharesTransaction(t *testing.T) {
	svc, db, _ := newApprovalTestService(t)
	grants := NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	svc.registry.Register(authz.OperationMessagePayloadRead, sensitiveAccessRequestHookAdapter{grants: grants})
	op := approvalOp("message-42-read")
	op.Kind = authz.OperationMessagePayloadRead
	created, err := svc.Request(op, map[string]any{"messageId": "42", "versionHash": "hash"}, auth.HumanPrincipal("alice"), "")
	if err != nil {
		t.Fatalf("首次提审应创建 pending grant：request=%+v err=%v", created, err)
	}
	grant, err := repository.NewSensitiveAccessGrantRepository(db).FindByApprovalRequestID(created.RequestID)
	if err != nil || grant == nil || grant.RequesterID != "alice" {
		t.Fatalf("pending grant 应绑定审批和原申请人：grant=%+v err=%v", grant, err)
	}
	if _, err := svc.Request(op, map[string]any{"messageId": "42", "versionHash": "hash"}, auth.HumanPrincipal("alice"), ""); err != nil {
		t.Fatalf("幂等复用不应重复创建 pending grant：err=%v", err)
	}
	var grantsCount int64
	if err := db.Model(&model.SensitiveAccessGrant{}).Where("approval_request_id = ?", created.RequestID).Count(&grantsCount).Error; err != nil || grantsCount != 1 {
		t.Fatalf("同一审批必须只有一份 pending grant：count=%d err=%v", grantsCount, err)
	}

	svc.registry.Register(authz.OperationSensitiveFileContentRead, sensitiveAccessRequestHookAdapter{grants: grants, fail: errors.New("创建授权失败")})
	failing := approvalOp("file-42-read")
	failing.Kind = authz.OperationSensitiveFileContentRead
	if _, err := svc.Request(failing, map[string]any{"fileId": "42", "versionHash": "hash"}, auth.HumanPrincipal("alice"), ""); err == nil {
		t.Fatal("hook 失败应回滚审批申请")
	}
	var count int64
	if err := db.Model(&model.ApprovalRequest{}).Where("idempotency_key = ?", failing.IdempotencyKey).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("hook 失败不得留下审批申请：count=%d err=%v", count, err)
	}
	if err := db.Model(&model.SensitiveAccessGrant{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("hook 失败不得留下 pending grant：count=%d err=%v", count, err)
	}
}

// TestApprovalServiceApproveWakesWorker 验证批准只推进 executing，不在请求链同步执行。
func TestApprovalServiceApproveWakesWorker(t *testing.T) {
	svc, db, calls := newApprovalTestService(t)
	requester := auth.HumanPrincipal("alice")
	approver := auth.HumanPrincipal("bob")
	created, err := svc.Request(approvalOp("order-42-approve"), map[string]any{"id": 42}, requester, "127.0.0.1")
	if err != nil {
		t.Fatalf("提审失败: %v", err)
	}

	approved, err := svc.Approve(created.RequestID, approver, "127.0.0.2")
	if err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if approved.Status != model.ApprovalStatusExecuting {
		t.Fatalf("批准后应为 executing，实际 %q", approved.Status)
	}
	if *calls != 0 {
		t.Fatalf("批准请求链不应同步调用适配器，实际 %d", *calls)
	}
	if approved.DeciderType != auth.PrincipalKindHuman || approved.DeciderID != "bob" || approved.DecidedAt == nil {
		t.Fatalf("批准人/时间应落库：%+v", approved)
	}

	worker := NewApprovalWorker(svc)
	if processed, err := worker.RunOnce(); err != nil || processed != 1 {
		t.Fatalf("worker 应执行 1 条审批，processed=%d err=%v", processed, err)
	}
	if *calls != 1 {
		t.Fatalf("worker 应调用适配器一次，实际 %d", *calls)
	}
	finished, err := svc.Detail(created.RequestID, requester)
	if err != nil {
		t.Fatalf("查询执行结果失败: %v", err)
	}
	if finished.Status != model.ApprovalStatusSucceeded {
		t.Fatalf("worker 执行成功后应为 succeeded，实际 %q", finished.Status)
	}

	var audits []model.AuditLog
	if err := db.Where("target_type = ?", model.TargetTypeApprovalRequest).Find(&audits).Error; err != nil {
		t.Fatalf("查询审批审计失败: %v", err)
	}
	if len(audits) != 2 {
		t.Fatalf("提审与批准应各写 1 条审计，实际 %d", len(audits))
	}
}

// TestApprovalServiceRejectPreventsApprove 验证驳回是终态，不能再批准执行。
func TestApprovalServiceRejectPreventsApprove(t *testing.T) {
	svc, _, calls := newApprovalTestService(t)
	principal := auth.HumanPrincipal("alice")
	created, err := svc.Request(approvalOp("order-42-reject"), nil, principal, "127.0.0.1")
	if err != nil {
		t.Fatalf("提审失败: %v", err)
	}
	rejected, err := svc.Reject(created.RequestID, principal, "127.0.0.1", "风险过高")
	if err != nil {
		t.Fatalf("驳回失败: %v", err)
	}
	if rejected.Status != model.ApprovalStatusRejected {
		t.Fatalf("驳回后状态应为 rejected，实际 %q", rejected.Status)
	}
	if _, err := svc.Approve(created.RequestID, principal, "127.0.0.1"); !errors.Is(err, apperr.ErrIllegalState) {
		t.Fatalf("驳回后再批准应非法状态，实际 %v", err)
	}
	if *calls != 0 {
		t.Fatalf("驳回不应调用执行适配器，实际 %d", *calls)
	}
}

// TestApprovalServiceMachineCannotDecide 验证机器主体稳定不能批准或驳回。
func TestApprovalServiceMachineCannotDecide(t *testing.T) {
	svc, _, _ := newApprovalTestService(t)
	created, err := svc.Request(approvalOp("machine-request"), nil, auth.HumanPrincipal("alice"), "127.0.0.1")
	if err != nil {
		t.Fatalf("提审失败: %v", err)
	}
	machine := auth.APIKeyPrincipal("apikey:ci", "ci", model.RoleFull, "bk_x")
	if _, err := svc.Approve(created.RequestID, machine, "127.0.0.1"); !errors.Is(err, apperr.ErrMachinePrincipalCannotDecide) {
		t.Fatalf("机器主体批准应稳定 403，实际 %v", err)
	}
	if _, err := svc.Reject(created.RequestID, machine, "127.0.0.1", "不通过"); !errors.Is(err, apperr.ErrMachinePrincipalCannotDecide) {
		t.Fatalf("机器主体驳回应稳定 403，实际 %v", err)
	}
}

// TestApprovalServiceRejectsRelabeledMachine 验证服务层按可信来源拒绝伪装成 human 的机器主体。
func TestApprovalServiceRejectsRelabeledMachine(t *testing.T) {
	svc, _, _ := newApprovalTestService(t)
	created, err := svc.Request(approvalOp("machine-relabeled"), nil, auth.HumanPrincipal("alice"), "127.0.0.1")
	if err != nil {
		t.Fatalf("提审失败：%v", err)
	}
	spoofed := auth.Principal{
		ID: "key-1", Kind: auth.PrincipalKindHuman, Source: auth.SourceAPIKey, Role: "full",
		Capabilities: []string{auth.CapabilityApprovalDecide},
	}
	if _, err := svc.Approve(created.RequestID, spoofed, "127.0.0.1"); !errors.Is(err, apperr.ErrMachinePrincipalCannotDecide) {
		t.Fatalf("伪装的机器主体批准应稳定拒绝，实际 %v", err)
	}
}

// TestApprovalServiceWithdrawAndDetailList 验证申请人可撤回，且列表详情走公开 request_id。
func TestApprovalServiceWithdrawAndDetailList(t *testing.T) {
	svc, _, _ := newApprovalTestService(t)
	principal := auth.HumanPrincipal("alice")
	created, err := svc.Request(approvalOp("withdraw"), nil, principal, "127.0.0.1")
	if err != nil {
		t.Fatalf("提审失败: %v", err)
	}
	items, err := svc.List(ApprovalListFilter{Status: model.ApprovalStatusPending}, principal)
	if err != nil || len(items) != 1 {
		t.Fatalf("列表应返回 1 条 pending，items=%d err=%v", len(items), err)
	}
	detail, err := svc.Detail(created.RequestID, principal)
	if err != nil || detail.RequestID != created.RequestID {
		t.Fatalf("详情应按 request_id 返回申请，detail=%+v err=%v", detail, err)
	}
	withdrawn, err := svc.Withdraw(created.RequestID, principal, "127.0.0.1")
	if err != nil {
		t.Fatalf("撤回失败: %v", err)
	}
	if withdrawn.Status != model.ApprovalStatusWithdrawn {
		t.Fatalf("撤回后状态应为 withdrawn，实际 %q", withdrawn.Status)
	}
	if _, err := svc.Withdraw(created.RequestID, principal, "127.0.0.1"); !errors.Is(err, apperr.ErrApprovalTerminal) {
		t.Fatalf("终态不可重复撤回，实际 %v", err)
	}
}

func TestUpdatePendingApprovalRejectsStaleVersion(t *testing.T) {
	_, db, _ := newApprovalTestService(t)
	req := model.ApprovalRequest{
		RequestID: "apr_stale_pending", OperationKey: authz.OperationDeliveryApprove, OperationKind: authz.OperationDeliveryApprove,
		ResourceType: model.TargetTypeChangeOrder, ResourceID: "42", Payload: "{}", Status: model.ApprovalStatusPending,
		RequestedBy: "human:alice", RequesterType: auth.PrincipalKindHuman, RequesterID: "alice", Version: 1,
	}
	if err := db.Create(&req).Error; err != nil {
		t.Fatalf("创建审批请求失败: %v", err)
	}
	stale := req
	if err := db.Model(&model.ApprovalRequest{}).Where("id = ?", req.ID).Update("version", req.Version+1).Error; err != nil {
		t.Fatalf("构造陈旧版本失败: %v", err)
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		return updatePendingApproval(tx, &stale, map[string]any{"status": model.ApprovalStatusWithdrawn})
	})
	if !errors.Is(err, apperr.ErrIllegalState) {
		t.Fatalf("陈旧 pending 版本应拒绝覆盖，实际 %v", err)
	}
	var current model.ApprovalRequest
	if err := db.First(&current, req.ID).Error; err != nil {
		t.Fatalf("查询审批请求失败: %v", err)
	}
	if current.Status != model.ApprovalStatusPending || current.Version != req.Version+1 {
		t.Fatalf("CAS 失败不得改变审批状态，实际 status=%q version=%d", current.Status, current.Version)
	}
}

// TestApprovalServiceMarksExpired 验证过期审批不可批准。
func TestApprovalServiceMarksExpired(t *testing.T) {
	svc, db, _ := newApprovalTestService(t)
	expired := time.Now().UTC().Add(-time.Minute)
	payload := []byte("{}")
	sum := sha256.Sum256(payload)
	req := model.ApprovalRequest{
		RequestID: "apr_expired", OperationKey: authz.OperationDeliveryApprove, OperationKind: authz.OperationDeliveryApprove,
		SchemaVersion: 1, RequiredCapability: auth.CapabilityApprovalRequest,
		ResourceType: model.TargetTypeChangeOrder, ResourceID: "42", Payload: string(payload), FrozenPayloadSHA256: hex.EncodeToString(sum[:]), Status: model.ApprovalStatusPending,
		RequestedBy: "human:alice", RequesterType: auth.PrincipalKindHuman, RequesterID: "alice", ExpiresAt: &expired, Version: 1,
	}
	if err := db.Create(&req).Error; err != nil {
		t.Fatalf("插入过期审批失败: %v", err)
	}
	principal := auth.HumanPrincipal("bob")
	if _, err := svc.Approve(strconv.FormatUint(uint64(req.ID), 10), principal, "127.0.0.1"); !errors.Is(err, apperr.ErrApprovalExpired) {
		t.Fatalf("过期审批应拒绝批准，实际 %v", err)
	}
}

// TestApprovalServiceRejectRequiresDeclaredTerminalCallback 防止需要领域收敛的审批静默进入终态。
func TestApprovalServiceRejectRequiresDeclaredTerminalCallback(t *testing.T) {
	svc, db, _ := newApprovalTestService(t)
	registry := authz.NewApprovalRegistry()
	registry.RegisterDescriptor(authz.OperationDescriptor{
		Key: authz.OperationDeliveryApprove, SchemaVersion: 1,
		Capability: auth.CapabilityApprovalRequest, RiskLevel: "high", RequiresTerminalCallback: true,
	}, authz.AdapterFunc(func(authz.ApprovalRequest, authz.Permit) error { return nil }))
	svc.registry = registry
	created, err := svc.Request(approvalOp("terminal-callback"), map[string]any{"id": 42}, auth.HumanPrincipal("alice"), "")
	if err != nil {
		t.Fatalf("创建审批申请失败: %v", err)
	}
	if _, err := svc.Reject(created.RequestID, auth.HumanPrincipal("admin"), "", "需要回调"); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("缺失终态回调应失败关闭，实际 %v", err)
	}
	var current model.ApprovalRequest
	if err := db.Where("request_id = ?", created.RequestID).First(&current).Error; err != nil {
		t.Fatalf("查询审批申请失败: %v", err)
	}
	if current.Status != model.ApprovalStatusPending {
		t.Fatalf("终态回调失败不得改变审批状态，实际 %q", current.Status)
	}
}

func approvalOp(key string) authz.Operation {
	return authz.Operation{
		Kind: authz.OperationDeliveryApprove, Resource: model.TargetTypeChangeOrder, ResourceID: "42",
		IdempotencyKey: key, RiskLevel: "high", Reason: "需要执行高风险操作",
	}
}
