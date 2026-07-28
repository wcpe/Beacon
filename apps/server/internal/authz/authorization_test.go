package authz

import (
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

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

// TestApprovalAdapterExecutesOnlyExecutingRequest 验证适配器只执行 executing 请求，并收到内核签发的许可。
func TestApprovalAdapterExecutesOnlyExecutingRequest(t *testing.T) {
	called := false
	adapter := AdapterFunc(func(req ApprovalRequest, permit Permit) error {
		called = true
		if permit.RequestID() != "apr_test" || permit.Operation() != OperationDeliveryApprove || permit.PayloadHash() != "hash" {
			t.Fatalf("执行许可绑定信息不符：%+v", permit)
		}
		return nil
	})
	registry := NewApprovalRegistry()
	registry.Register(OperationDeliveryApprove, adapter)

	pending := ApprovalRequest{Operation: Operation{Kind: OperationDeliveryApprove}, Status: model.ApprovalStatusPending}
	if err := registry.Execute(pending, "hash"); err == nil {
		t.Fatal("未进入 executing 的请求不应执行")
	}
	if called {
		t.Fatal("未进入 executing 的请求不应调用适配器")
	}

	executing := ApprovalRequest{RequestID: "apr_test", Operation: Operation{Kind: OperationDeliveryApprove}, Status: model.ApprovalStatusExecuting}
	if err := registry.Execute(executing, "hash"); err != nil {
		t.Fatalf("executing 请求应执行成功，实际 %v", err)
	}
	if !called {
		t.Fatal("executing 请求应调用适配器")
	}
}
