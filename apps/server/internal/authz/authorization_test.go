package authz

import (
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

type terminalAdapterFunc func(tx *gorm.DB, req ApprovalRequest, status string) error

func (terminalAdapterFunc) Execute(ApprovalRequest, Permit) error { return apperr.ErrForbidden }

func (f terminalAdapterFunc) CompleteTerminalInTx(tx *gorm.DB, req ApprovalRequest, status string) error {
	return f(tx, req, status)
}

// TestAuthorizeOperationRequiresCapability 验证写操作按能力授权，缺能力一律拒绝。
func TestAuthorizeOperationRequiresCapability(t *testing.T) {
	op := Operation{Kind: OperationDeliveryApprove, Resource: "change-order", ResourceID: "42"}
	allowed := auth.Principal{Operator: "bob", Capabilities: []string{auth.CapabilityApprovalRequest}}
	if err := Authorize(allowed, op); err != nil {
		t.Fatalf("具备能力应允许提审操作，实际 %v", err)
	}

	denied := auth.Principal{Operator: "readonly"}
	if err := Authorize(denied, op); err == nil {
		t.Fatal("缺少提审能力应拒绝")
	}
}

// TestApprovalAdapterExecutesOnlyExecutingRequest 验证许可仅从事务中的权威审批行签发。
func TestApprovalAdapterExecutesOnlyExecutingRequest(t *testing.T) {
	called := false
	adapter := TransactionalAdapterFunc(func(_ *gorm.DB, _ ApprovalRequest, permit Permit) (func(), error) {
		called = true
		if permit.RequestID() != "apr_test" || permit.Operation() != OperationDeliveryApprove || permit.SchemaVersion() != 1 ||
			permit.PayloadHash() != frozenPayloadHash([]byte(`{"orderId":42}`)) || permit.LeaseToken() != "lease_test" || permit.Version() != 7 {
			t.Fatalf("执行许可绑定信息不符：%+v", permit)
		}
		return nil, nil
	})
	registry := NewApprovalRegistry()
	registry.Register(OperationDeliveryApprove, adapter)

	pending := ApprovalRequest{Operation: Operation{Kind: OperationDeliveryApprove}, Status: model.ApprovalStatusPending}
	if err := registry.Execute(pending, "hash"); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("非事务执行入口应 fail-closed，实际 %v", err)
	}
	if called {
		t.Fatal("非事务执行不应调用适配器")
	}

	db, err := gorm.Open(sqlite.Open("file:approval_authz?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败：%v", err)
	}
	if err := db.AutoMigrate(&model.ApprovalRequest{}); err != nil {
		t.Fatalf("迁移审批表失败：%v", err)
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(time.Minute)
	payload := []byte(`{"orderId":42}`)
	executing := model.ApprovalRequest{
		RequestID:     "apr_test",
		OperationKind: OperationDeliveryApprove, OperationKey: OperationDeliveryApprove,
		SchemaVersion:      1,
		RequiredCapability: auth.CapabilityApprovalRequest,
		Payload:            string(payload), FrozenPayloadSHA256: frozenPayloadHash(payload),
		Status:      model.ApprovalStatusExecuting,
		LeaseOwner:  "lease_test",
		LeaseUntil:  &leaseUntil,
		DeciderType: auth.PrincipalKindHuman,
		DeciderID:   "alice",
		ApprovedAt:  &now,
		Version:     7,
	}
	if err := db.Create(&executing).Error; err != nil {
		t.Fatalf("写入审批行失败：%v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := registry.ExecuteInTx(tx, executing.RequestID, executing.LeaseOwner)
		return err
	}); err != nil {
		t.Fatalf("executing 请求应执行成功，实际 %v", err)
	}
	if !called {
		t.Fatal("executing 请求应调用适配器")
	}
	called = false
	if err := db.Model(&model.ApprovalRequest{}).Where("request_id = ?", executing.RequestID).
		Update("payload", `{"orderId":99}`).Error; err != nil {
		t.Fatalf("篡改冻结载荷失败：%v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := registry.ExecuteInTx(tx, executing.RequestID, executing.LeaseOwner)
		return err
	}); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("载荷哈希失配应拒绝签发许可，实际 %v", err)
	}
	if called {
		t.Fatal("载荷哈希失配不应调用适配器")
	}
}

// TestAuthorizeUnknownOperationsFailClosed 验证未登记命名空间不能借助前缀绕过授权。
func TestAuthorizeUnknownOperationsFailClosed(t *testing.T) {
	principal := auth.Principal{Operator: "alice", Capabilities: []string{"approval.unknown"}}
	for _, kind := range []string{"approval.unknown", "management.unknown"} {
		if err := Authorize(principal, Operation{Kind: kind}); !errors.Is(err, apperr.ErrForbidden) {
			t.Fatalf("未知操作 %q 应 fail-closed，实际 %v", kind, err)
		}
	}
}

// TestRegistryUnknownOperationFailsClosed 验证注册表不会执行未登记适配器。
func TestRegistryUnknownOperationFailsClosed(t *testing.T) {
	registry := NewApprovalRegistry()
	err := registry.Execute(ApprovalRequest{
		RequestID: "apr_unknown",
		Operation: Operation{Kind: "approval.unknown"},
		Status:    model.ApprovalStatusExecuting,
	}, "hash")
	if !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("未知执行操作应 fail-closed，实际 %v", err)
	}
}

// TestTerminalCallbackRejectsTamperedPayload 验证终态回调同样拒绝被篡改的冻结载荷。
func TestTerminalCallbackRejectsTamperedPayload(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:approval_terminal_authz?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存库失败：%v", err)
	}
	if err := db.AutoMigrate(&model.ApprovalRequest{}); err != nil {
		t.Fatalf("迁移审批表失败：%v", err)
	}
	payload := []byte(`{"orderId":42}`)
	req := model.ApprovalRequest{
		RequestID: "apr_terminal", OperationKind: OperationDeliveryApprove, OperationKey: OperationDeliveryApprove,
		SchemaVersion: 1, RequiredCapability: auth.CapabilityApprovalRequest,
		Payload: string(payload), FrozenPayloadSHA256: frozenPayloadHash(payload),
		Status: model.ApprovalStatusRejected, Version: 3,
	}
	if err := db.Create(&req).Error; err != nil {
		t.Fatalf("写入审批行失败：%v", err)
	}
	if err := db.Model(&model.ApprovalRequest{}).Where("request_id = ?", req.RequestID).Update("payload", `{"orderId":99}`).Error; err != nil {
		t.Fatalf("篡改冻结载荷失败：%v", err)
	}
	called := false
	registry := NewApprovalRegistry()
	registry.RegisterDescriptor(OperationDescriptor{
		Key: OperationDeliveryApprove, SchemaVersion: 1, Capability: auth.CapabilityApprovalRequest,
		RiskLevel: "high", RequiresTerminalCallback: true,
	}, terminalAdapterFunc(func(*gorm.DB, ApprovalRequest, string) error {
		called = true
		return nil
	}))
	err = db.Transaction(func(tx *gorm.DB) error {
		return registry.ExecuteTerminalInTx(tx, req.RequestID, req.Version, model.ApprovalStatusRejected, "human:alice")
	})
	if !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("篡改载荷应拒绝终态回调，实际 %v", err)
	}
	if called {
		t.Fatal("篡改载荷不应触发终态回调")
	}
}
