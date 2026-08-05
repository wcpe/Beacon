package model

import "testing"

// TestApprovalRequestTableAndStatus 验证统一审批请求模型的表名与规格状态常量。
func TestApprovalRequestTableAndStatus(t *testing.T) {
	if got := (ApprovalRequest{}).TableName(); got != "approval_request" {
		t.Fatalf("审批请求表名应为 approval_request，实际 %q", got)
	}
	if got := (ApprovalExecutionReceipt{}).TableName(); got != "approval_execution_receipt" {
		t.Fatalf("执行回执表名不符，实际 %q", got)
	}
	for _, status := range []string{ApprovalStatusWithdrawn, ApprovalStatusRejected, ApprovalStatusExpired, ApprovalStatusSucceeded, ApprovalStatusFailed} {
		if !IsTerminalApprovalStatus(status) {
			t.Fatalf("%s 应为审批终态", status)
		}
	}
	for _, status := range []string{ApprovalStatusPending, ApprovalStatusExecuting} {
		if IsTerminalApprovalStatus(status) {
			t.Fatalf("%s 不应是审批终态", status)
		}
	}
}
