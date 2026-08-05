package authz

import (
	"errors"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
)

// TestSystemPolicyRegistryFailsClosed 验证后台策略只可执行显式登记的低风险操作。
func TestSystemPolicyRegistryFailsClosed(t *testing.T) {
	registry := NewSystemPolicyRegistry()
	if err := registry.Register(SystemPolicy{Key: "retention.cleanup", Operations: []string{"retention.cleanup"}}); err != nil {
		t.Fatalf("登记低风险策略失败：%v", err)
	}
	if _, err := registry.Evidence("retention.cleanup", "retention.cleanup"); err != nil {
		t.Fatalf("已登记操作应可取得策略证据：%v", err)
	}
	if _, err := registry.Evidence("retention.cleanup", "system.update"); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("未登记操作应失败关闭：%v", err)
	}
	if err := registry.Register(SystemPolicy{Key: "bad", Operations: []string{OperationDeliveryApprove}}); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("危险审批操作不得登记为系统豁免：%v", err)
	}
}
