package service

import (
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

func newV2NamespaceTrustApprovalTest(t *testing.T) (*gorm.DB, *V2ControlPlaneService, *ApprovalService) {
	t.Helper()
	db, testSvc := newV2ControlPlaneTestService(t)
	if err := db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}); err != nil {
		t.Fatalf("迁移审批表失败：%v", err)
	}
	svc := testSvc.V2ControlPlaneService
	registry := authz.NewApprovalRegistry()
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	svc.SetApprovalService(approval)
	RegisterV2ControlPlaneApprovalAdapters(registry, svc)
	return db, svc, approval
}

func requestNamespaceTrustApproval(t *testing.T, svc *V2ControlPlaneService, approval *ApprovalService) (uint, uint) {
	t.Helper()
	from, _, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "来源", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建来源命名空间失败：%v", err)
	}
	to, _, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "目标", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建目标命名空间失败：%v", err)
	}
	ticket, err := svc.RequestGrantNamespaceTrust(GrantNamespaceTrustParams{
		FromNamespaceID: from.ID,
		ToNamespaceID:   to.ID,
		Capability:      model.NamespaceTrustCapabilitySchedule,
		Note:            "允许跨命名空间调度",
		Reason:          "联调需要跨命名空间调度",
		Operator:        "alice",
	}, auth.HumanPrincipal("alice"), "namespace-trust-request")
	if err != nil {
		t.Fatalf("创建信任审批失败：%v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.1"); err != nil {
		t.Fatalf("批准信任审批失败：%v", err)
	}
	return from.ID, to.ID
}

func TestV2NamespaceTrustApprovalReceiptRollbackDoesNotRefreshSnapshot(t *testing.T) {
	db, svc, approval := newV2NamespaceTrustApprovalTest(t)
	from, to := requestNamespaceTrustApproval(t, svc, approval)
	if err := db.Exec(`CREATE TRIGGER reject_namespace_trust_receipt BEFORE INSERT ON approval_execution_receipt BEGIN SELECT RAISE(ABORT, '拒绝回执'); END`).Error; err != nil {
		t.Fatalf("安装回执拒绝触发器失败：%v", err)
	}

	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("回执失败后的审批处理应收敛为失败：processed=%d err=%v", processed, err)
	}

	var trustCount, receiptCount int64
	if err := db.Model(&model.NamespaceTrust{}).Where("from_namespace_id = ? AND to_namespace_id = ?", from, to).Count(&trustCount).Error; err != nil {
		t.Fatalf("查询信任记录失败：%v", err)
	}
	if err := db.Model(&model.ApprovalExecutionReceipt{}).Count(&receiptCount).Error; err != nil {
		t.Fatalf("查询执行回执失败：%v", err)
	}
	if trustCount != 0 || receiptCount != 0 {
		t.Fatalf("回执导致外层事务回滚后不得持久化信任或回执：trust=%d receipt=%d", trustCount, receiptCount)
	}
	if svc.NamespaceTrustAllowed(from, to, model.NamespaceTrustCapabilitySchedule) {
		t.Fatal("回执导致外层事务回滚后不得刷新进程内信任快照")
	}
}

func TestV2NamespaceTrustApprovalCommitRefreshesSnapshot(t *testing.T) {
	db, svc, approval := newV2NamespaceTrustApprovalTest(t)
	from, to := requestNamespaceTrustApproval(t, svc, approval)

	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("批准后的信任审批应执行成功：processed=%d err=%v", processed, err)
	}

	var trust model.NamespaceTrust
	if err := db.Where("from_namespace_id = ? AND to_namespace_id = ? AND capability = ?", from, to, model.NamespaceTrustCapabilitySchedule).First(&trust).Error; err != nil {
		t.Fatalf("成功提交后应持久化信任：%v", err)
	}
	if trust.Status != model.NamespaceTrustStatusActive {
		t.Fatalf("成功提交后的信任状态应为 active，实际 %q", trust.Status)
	}
	if !svc.NamespaceTrustAllowed(from, to, model.NamespaceTrustCapabilitySchedule) {
		t.Fatal("成功提交后应刷新进程内信任快照")
	}
}

func TestV2NamespaceTrustApprovalRejectsPermitBoundToAnotherLease(t *testing.T) {
	db, svc, approval := newV2NamespaceTrustApprovalTest(t)
	from, to := requestNamespaceTrustApproval(t, svc, approval)
	descriptor, ok := approval.registry.Descriptor(authz.OperationNamespaceTrustGrant)
	if !ok {
		t.Fatal("命名空间信任审批操作未登记")
	}
	approval.registry.RegisterDescriptor(descriptor, authz.RequireExecutionReceipt(authz.TransactionalAdapterFunc(func(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
		req.LeaseOwner = "另一有效执行租约"
		return svc.executeApprovedV2OperationInTx(tx, req, permit)
	})))

	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("租约不匹配应由 worker 收敛为失败：processed=%d err=%v", processed, err)
	}
	var trustCount, receiptCount int64
	if err := db.Model(&model.NamespaceTrust{}).Where("from_namespace_id = ? AND to_namespace_id = ?", from, to).Count(&trustCount).Error; err != nil {
		t.Fatalf("查询信任记录失败：%v", err)
	}
	if err := db.Model(&model.ApprovalExecutionReceipt{}).Count(&receiptCount).Error; err != nil {
		t.Fatalf("查询执行回执失败：%v", err)
	}
	if trustCount != 0 || receiptCount != 0 {
		t.Fatalf("租约不匹配不得写入信任或回执：trust=%d receipt=%d", trustCount, receiptCount)
	}
}
