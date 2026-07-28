package service

import (
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

// newApprovalTestService 用内存库装配审批服务。
func newApprovalTestService(t *testing.T) (*ApprovalService, *gorm.DB, *int) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.ApprovalRequest{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移审批表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	for _, tbl := range []string{"approval_request", "audit_log"} {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	calls := 0
	registry := authz.NewApprovalRegistry()
	registry.Register(authz.OperationDeliveryApprove, authz.AdapterFunc(func(req authz.ApprovalRequest, permit authz.Permit) error {
		calls++
		if permit.RequestID() != req.RequestID {
			t.Fatalf("执行许可应绑定当前申请：permit=%s request=%s", permit.RequestID(), req.RequestID)
		}
		return nil
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

// TestApprovalServiceApproveExecutesAndAudits 验证批准后执行、状态推进并写审计。
func TestApprovalServiceApproveExecutesAndAudits(t *testing.T) {
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
	if approved.Status != model.ApprovalStatusSucceeded {
		t.Fatalf("同步执行成功后应为 succeeded，实际 %q", approved.Status)
	}
	if *calls != 1 {
		t.Fatalf("批准应调用适配器一次，实际 %d", *calls)
	}
	if approved.DeciderType != auth.PrincipalKindHuman || approved.DeciderID != "bob" || approved.DecidedAt == nil {
		t.Fatalf("批准人/时间应落库：%+v", approved)
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

// TestApprovalServiceMarksExpired 验证过期审批不可批准。
func TestApprovalServiceMarksExpired(t *testing.T) {
	svc, db, _ := newApprovalTestService(t)
	expired := time.Now().UTC().Add(-time.Minute)
	req := model.ApprovalRequest{
		RequestID: "apr_expired", OperationKey: authz.OperationDeliveryApprove, OperationKind: authz.OperationDeliveryApprove,
		ResourceType: model.TargetTypeChangeOrder, ResourceID: "42", Payload: "{}", Status: model.ApprovalStatusPending,
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

func approvalOp(key string) authz.Operation {
	return authz.Operation{
		Kind: authz.OperationDeliveryApprove, Resource: model.TargetTypeChangeOrder, ResourceID: "42",
		IdempotencyKey: key, RiskLevel: "high", Reason: "需要执行高风险操作",
	}
}
