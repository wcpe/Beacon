package authz

import (
	"sync"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
)

// SystemPolicy 描述低风险后台维护策略允许执行的操作。
type SystemPolicy struct {
	Key        string
	Operations []string
}

// SystemPolicyEvidence 是只由策略注册表签发的内部维护证据。
type SystemPolicyEvidence struct {
	policyKey string
	operation string
}

// PolicyKey 返回已登记策略键。
func (e SystemPolicyEvidence) PolicyKey() string { return e.policyKey }

// Operation 返回证据绑定的低风险操作键。
func (e SystemPolicyEvidence) Operation() string { return e.operation }

// SystemPolicyRegistry 保存后台低风险维护操作白名单。
type SystemPolicyRegistry struct {
	mu      sync.RWMutex
	entries map[string]map[string]struct{}
}

// NewSystemPolicyRegistry 构造空的后台策略注册表。
func NewSystemPolicyRegistry() *SystemPolicyRegistry {
	return &SystemPolicyRegistry{entries: map[string]map[string]struct{}{}}
}

// Register 登记一个仅含低风险维护操作的系统策略。
func (r *SystemPolicyRegistry) Register(policy SystemPolicy) error {
	if r == nil || policy.Key == "" || len(policy.Operations) == 0 {
		return apperr.ErrForbidden
	}
	operations := make(map[string]struct{}, len(policy.Operations))
	for _, operation := range policy.Operations {
		if operation == "" || capabilityFor(operation) == auth.CapabilityApprovalRequest || !lowRiskSystemOperation(operation) {
			return apperr.ErrForbidden
		}
		operations[operation] = struct{}{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[policy.Key]; exists {
		return apperr.ErrForbidden
	}
	r.entries[policy.Key] = operations
	return nil
}

func lowRiskSystemOperation(operation string) bool {
	_, ok := lowRiskSystemOperations[operation]
	return ok
}

var lowRiskSystemOperations = map[string]struct{}{
	"retention.cleanup":       {},
	"retention.expire":        {},
	"metrics.flush":           {},
	"metrics.aggregate":       {},
	"runtime.health_scan":     {},
	"runtime.message_sweep":   {},
	"runtime.command_sweep":   {},
	"runtime.archive_cleanup": {},
}

// Evidence 为已登记策略操作签发进程内维护证据。
func (r *SystemPolicyRegistry) Evidence(policyKey, operation string) (SystemPolicyEvidence, error) {
	if r == nil || policyKey == "" || operation == "" {
		return SystemPolicyEvidence{}, apperr.ErrForbidden
	}
	r.mu.RLock()
	operations := r.entries[policyKey]
	_, allowed := operations[operation]
	r.mu.RUnlock()
	if !allowed {
		return SystemPolicyEvidence{}, apperr.ErrForbidden
	}
	return SystemPolicyEvidence{policyKey: policyKey, operation: operation}, nil
}
