package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestApprovalRequestUnknownOperationFailsClosed 验证未知 operation 不能创建审批请求。
func TestApprovalRequestUnknownOperationFailsClosed(t *testing.T) {
	svc, _, _ := newApprovalTestService(t)
	_, err := svc.Request(authz.Operation{
		Kind: "approval.not_registered", Resource: "change-order", ResourceID: "42",
		IdempotencyKey: "unknown-operation", RiskLevel: "high", Reason: "需要执行",
	}, nil, auth.HumanPrincipal("alice"), "127.0.0.1")
	if !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("未知 operation 应 fail-closed，实际 %v", err)
	}
}

// TestApprovalWorkerReceiptConvergesWithoutDuplicateExecution 验证已有 receipt 时不重复调用适配器。
// TestApprovalWorkerRunExecutesAfterApproval 验证常驻 Run 会被批准信号唤醒并自动执行。
func TestApprovalWorkerRunExecutesAfterApproval(t *testing.T) {
	svc, _, calls := newApprovalTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- NewApprovalWorker(svc).Run(ctx)
	}()

	created, err := svc.Request(approvalOp("run-wakeup"), nil, auth.HumanPrincipal("alice"), "127.0.0.1")
	if err != nil {
		t.Fatalf("提审失败: %v", err)
	}
	if _, err := svc.Approve(created.RequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		finished, detailErr := svc.Detail(created.RequestID, auth.HumanPrincipal("alice"))
		if detailErr == nil && finished.Status == model.ApprovalStatusSucceeded {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-workerDone; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("worker 退出异常: %v", err)
	}
	finished, err := svc.Detail(created.RequestID, auth.HumanPrincipal("alice"))
	if err != nil || finished.Status != model.ApprovalStatusSucceeded || *calls != 1 {
		t.Fatalf("批准后 Run 应自动执行一次，request=%+v calls=%d err=%v", finished, *calls, err)
	}
}

func TestApprovalWorkerReceiptConvergesWithoutDuplicateExecution(t *testing.T) {
	svc, db, calls := newApprovalTestService(t)
	created, err := svc.Request(approvalOp("receipt-existing"), nil, auth.HumanPrincipal("alice"), "127.0.0.1")
	if err != nil {
		t.Fatalf("提审失败: %v", err)
	}
	if _, err := svc.Approve(created.RequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	receipt := model.ApprovalExecutionReceipt{
		RequestID: created.RequestID, OperationKey: created.OperationKey,
		PayloadHash: created.FrozenPayloadSHA256, ResultRef: "receipt-result-existing",
	}
	if err := db.Create(&receipt).Error; err != nil {
		t.Fatalf("写入执行 receipt 失败: %v", err)
	}
	if processed, err := NewApprovalWorker(svc).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("worker 应收敛 1 条 receipt，processed=%d err=%v", processed, err)
	}
	if *calls != 0 {
		t.Fatalf("已有 receipt 不应重复执行，实际调用 %d 次", *calls)
	}
	finished, err := svc.Detail(created.RequestID, auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("查询审批结果失败: %v", err)
	}
	if finished.Status != model.ApprovalStatusSucceeded || finished.ResultRef != receipt.ResultRef {
		t.Fatalf("receipt 收敛结果不符：%+v", finished)
	}
}

// TestApprovalWorkerRetriesOnlyExplicitRetryableErrors 验证仅明确可重试错误最多执行三次。
func TestApprovalWorkerRetriesOnlyExplicitRetryableErrors(t *testing.T) {
	svc, _, _ := newApprovalTestService(t)
	attempts := 0
	svc.registry.Register(authz.OperationDeliveryApprove, authz.TransactionalAdapterFunc(func(_ *gorm.DB, _ authz.ApprovalRequest, _ authz.Permit) (func(), error) {
		attempts++
		if attempts < 3 {
			return nil, authz.Retryable(errors.New("临时依赖不可用"))
		}
		return nil, nil
	}))
	created, err := svc.Request(approvalOp("retryable"), nil, auth.HumanPrincipal("alice"), "127.0.0.1")
	if err != nil {
		t.Fatalf("提审失败: %v", err)
	}
	if _, err := svc.Approve(created.RequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if _, err := NewApprovalWorker(svc).RunOnce(); err != nil {
		t.Fatalf("可重试错误最终成功不应返回错误：%v", err)
	}
	finished, err := svc.Detail(created.RequestID, auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("查询审批结果失败: %v", err)
	}
	if attempts != 3 || finished.Status != model.ApprovalStatusSucceeded || finished.Attempt != 3 {
		t.Fatalf("重试收口不符：attempts=%d request=%+v", attempts, finished)
	}
}

// TestApprovalWorkerDomainErrorIsTerminal 验证非明确可重试错误立即进入 failed 终态。
func TestApprovalWorkerDomainErrorIsTerminal(t *testing.T) {
	svc, _, _ := newApprovalTestService(t)
	calls := 0
	svc.registry.Register(authz.OperationDeliveryApprove, authz.TransactionalAdapterFunc(func(_ *gorm.DB, _ authz.ApprovalRequest, _ authz.Permit) (func(), error) {
		calls++
		return nil, apperr.ErrApprovalTargetChanged
	}))
	created, err := svc.Request(approvalOp("domain-failure"), nil, auth.HumanPrincipal("alice"), "127.0.0.1")
	if err != nil {
		t.Fatalf("提审失败: %v", err)
	}
	if _, err := svc.Approve(created.RequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if _, err := NewApprovalWorker(svc).RunOnce(); err != nil {
		t.Fatalf("领域错误已落 failed，不应作为 worker 基础设施错误返回：%v", err)
	}
	finished, err := svc.Detail(created.RequestID, auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("查询审批结果失败: %v", err)
	}
	if calls != 1 || finished.Status != model.ApprovalStatusFailed || finished.Attempt != 1 || finished.FailureSummary == "" {
		t.Fatalf("领域错误终态不符：calls=%d request=%+v", calls, finished)
	}
}

// TestApprovalWorkerReclaimsExpiredLease 验证崩溃遗留的过期 lease 可被重新认领。
func TestApprovalWorkerReclaimsExpiredLease(t *testing.T) {
	svc, db, calls := newApprovalTestService(t)
	created, err := svc.Request(approvalOp("lease-recovery"), nil, auth.HumanPrincipal("alice"), "127.0.0.1")
	if err != nil {
		t.Fatalf("提审失败: %v", err)
	}
	if _, err := svc.Approve(created.RequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	expired := time.Now().UTC().Add(-time.Minute)
	if err := db.Model(&model.ApprovalRequest{}).Where("id = ?", created.ID).Updates(map[string]any{
		"lease_owner": "crashed-worker", "lease_until": expired,
	}).Error; err != nil {
		t.Fatalf("制造过期 lease 失败: %v", err)
	}
	if _, err := NewApprovalWorker(svc).RunOnce(); err != nil {
		t.Fatalf("恢复扫描失败: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("过期 lease 应恢复执行一次，实际 %d", *calls)
	}
}

// TestApprovalServiceListPageReturnsTotal 验证服务层分页返回当页数据与 total。
func TestApprovalServiceListPageReturnsTotal(t *testing.T) {
	svc, _, _ := newApprovalTestService(t)
	principal := auth.HumanPrincipal("alice")
	for _, key := range []string{"page-1", "page-2", "page-3"} {
		if _, err := svc.Request(approvalOp(key), nil, principal, "127.0.0.1"); err != nil {
			t.Fatalf("提审 %s 失败: %v", key, err)
		}
	}
	items, total, err := svc.ListPage(ApprovalListFilter{Page: 2, PageSize: 1}, principal)
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if len(items) != 1 || total != 3 {
		t.Fatalf("分页结果不符：items=%d total=%d", len(items), total)
	}
}

// TestApprovalServiceListPageFiltersSafeFields 验证审批列表只按已持久化的安全字段筛选。
func TestApprovalServiceListPageFiltersSafeFields(t *testing.T) {
	svc, db, _ := newApprovalTestService(t)
	principal := auth.HumanPrincipal("alice")
	from := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	namespaceID := uint(7)
	req := model.ApprovalRequest{
		RequestID: "apr_safe_filter", OperationKey: authz.OperationDeliveryApprove, OperationKind: authz.OperationDeliveryApprove,
		ResourceType: model.TargetTypeChangeOrder, ResourceID: "42", RequestReason: "归档旧变更", SafeSummary: "归档变更单",
		Payload: "{}", Status: model.ApprovalStatusPending, RequesterType: auth.PrincipalKindHuman, RequesterID: "alice",
		RequestedBy: "human:alice", NamespaceID: &namespaceID, Version: 1, CreatedAt: from,
	}
	if err := db.Create(&req).Error; err != nil {
		t.Fatalf("写入筛选审批失败：%v", err)
	}
	items, total, err := svc.ListPage(ApprovalListFilter{
		NamespaceID: &namespaceID, Keyword: "归档", CreatedFrom: &from, CreatedTo: &to, Page: 1, PageSize: 10,
	}, principal)
	if err != nil || total != 1 || len(items) != 1 || items[0].RequestID != req.RequestID {
		t.Fatalf("安全筛选结果不符：items=%+v total=%d err=%v", items, total, err)
	}
}

// TestApprovalServiceRejectsSensitiveFrozenPayload 验证冻结载荷拒绝敏感字段。
func TestApprovalServiceRejectsSensitiveFrozenPayload(t *testing.T) {
	svc, _, _ := newApprovalTestService(t)
	_, err := svc.Request(approvalOp("sensitive-payload"), map[string]any{"accessToken": "不要冻结"}, auth.HumanPrincipal("alice"), "127.0.0.1")
	if !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("冻结载荷含 token 应拒绝，实际 %v", err)
	}
}
