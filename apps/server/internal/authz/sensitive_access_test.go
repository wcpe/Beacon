package authz

import "testing"

// TestSensitiveAccessTargetRejectsUnstableReference 验证敏感内容授权只接受稳定引用和内容哈希。
func TestSensitiveAccessTargetRejectsUnstableReference(t *testing.T) {
	target, err := NewSensitiveAccessTarget("message", "msg-42", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err != nil || target.Ref != "message/msg-42" {
		t.Fatalf("稳定目标应可冻结：target=%+v err=%v", target, err)
	}
	if _, err := NewSensitiveAccessTarget("message", "msg 42", target.ContentHash); err == nil {
		t.Fatal("含空白的目标引用应拒绝")
	}
	if target, err := NewSensitiveAccessTarget("agent-command", "prod/server-a/apr_42", target.ContentHash); err != nil || target.Ref != "agent-command/prod/server-a/apr_42" {
		t.Fatalf("命令授权应支持命名空间/实例/申请ID：target=%+v err=%v", target, err)
	}
	if _, err := NewSensitiveAccessTarget("message", "msg-42", "short"); err == nil {
		t.Fatal("非 SHA-256 内容哈希应拒绝")
	}
}

// TestSensitiveAccessOperationsRequireApproval 验证静态敏感读取均无法绕过审批发起权限。
func TestSensitiveAccessOperationsRequireApproval(t *testing.T) {
	operations := []string{
		OperationMessagePayloadRead,
		OperationSensitiveFileContentRead,
		OperationSensitiveConfigPlaintextRead,
	}
	for _, operation := range operations {
		if got := capabilityFor(operation); got != "approval.request" {
			t.Fatalf("操作 %s 的审批能力错误：%s", operation, got)
		}
	}
}
