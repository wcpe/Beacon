package authz

import (
	"fmt"
	"strings"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// 操作类型。
const (
	OperationDeliveryApprove  = "delivery.approve"
	OperationDeliveryRollback = "delivery.rollback"

	OperationIdentityApprove         = "identity.approve"
	OperationIdentityUnbind          = "identity.unbind"
	OperationIdentityResolveConflict = "identity.resolve_conflict"
	OperationIdentityEnable          = "identity.enable"

	OperationCredentialCreate = "credential.create"
	OperationCredentialRotate = "credential.rotate"

	OperationNamespaceTrustGrant = "namespace_trust.grant"

	OperationTopologyServerAssign       = "topology.server_assign"
	OperationTopologyServerRezone       = "topology.server_rezone"
	OperationTopologyDefaultEntryChange = "topology.default_entry.change"
	OperationTopologyLobbyMemberMove    = "topology.lobby_member.move"
	OperationTopologyDrainingDisable    = "topology.draining.disable"

	OperationServerArchive = "server.archive"
	OperationServerRestore = "server.restore"
)

// OperationDescriptor 描述 operation 的授权分类与冻结参数版本。
type OperationDescriptor struct {
	Key           string
	SchemaVersion int
	Capability    string
	RiskLevel     string
}

// Operation 描述一次待授权 / 待审批的业务动作。
type Operation struct {
	Kind           string
	Resource       string
	ResourceID     string
	IdempotencyKey string
	RiskLevel      string
	Reason         string
}

// Authorize 校验主体是否可发起指定操作。
func Authorize(principal auth.Principal, op Operation) error {
	principal = auth.NormalizePrincipal(principal)
	capability := capabilityFor(op.Kind)
	if capability == "" || !principal.HasCapability(capability) {
		return apperr.ErrForbidden
	}
	return nil
}

func capabilityFor(kind string) string {
	switch kind {
	case OperationDeliveryApprove,
		OperationIdentityApprove,
		OperationIdentityUnbind,
		OperationIdentityResolveConflict,
		OperationIdentityEnable,
		OperationCredentialCreate,
		OperationCredentialRotate,
		OperationNamespaceTrustGrant,
		OperationTopologyServerAssign,
		OperationTopologyServerRezone,
		OperationTopologyDefaultEntryChange,
		OperationTopologyLobbyMemberMove,
		OperationTopologyDrainingDisable,
		OperationServerArchive,
		OperationServerRestore:
		return auth.CapabilityApprovalRequest
	case OperationDeliveryRollback:
		return auth.CapabilityManagementDirect
	default:
		if strings.HasPrefix(kind, "approval.") || strings.HasPrefix(kind, "management.") {
			return kind
		}
		return ""
	}
}

// ApprovalRequest 是执行适配器所需的冻结审批请求。
type ApprovalRequest struct {
	ID        uint
	RequestID string
	Operation Operation
	Payload   []byte
	Status    string
	Actor     string
}

// Permit 是审批内核生成的执行许可，外部包无法字面量伪造。
type Permit struct {
	requestID   string
	operation   string
	payloadHash string
}

// RequestID 返回许可绑定的公开审批 ID。
func (p Permit) RequestID() string { return p.requestID }

// Operation 返回许可绑定的操作。
func (p Permit) Operation() string { return p.operation }

// PayloadHash 返回许可绑定的冻结载荷哈希。
func (p Permit) PayloadHash() string { return p.payloadHash }

// Adapter 执行已审批通过的业务动作。
type Adapter interface {
	Execute(req ApprovalRequest, permit Permit) error
}

// AdapterFunc 让普通函数满足 Adapter。
type AdapterFunc func(req ApprovalRequest, permit Permit) error

func (f AdapterFunc) Execute(req ApprovalRequest, permit Permit) error {
	return f(req, permit)
}

// ApprovalRegistry 保存操作到执行适配器的映射。
type ApprovalRegistry struct {
	adapters map[string]Adapter
}

// NewApprovalRegistry 构造审批适配器注册表。
func NewApprovalRegistry() *ApprovalRegistry {
	return &ApprovalRegistry{adapters: map[string]Adapter{}}
}

// Register 注册一种操作的执行适配器。
func (r *ApprovalRegistry) Register(kind string, adapter Adapter) {
	r.adapters[kind] = adapter
}

// Execute 执行已进入 executing 状态的审批请求，并在内部签发许可。
func (r *ApprovalRegistry) Execute(req ApprovalRequest, payloadHash string) error {
	if req.Status != model.ApprovalStatusExecuting {
		return apperr.ErrIllegalState
	}
	adapter := r.adapters[req.Operation.Kind]
	if adapter == nil {
		return fmt.Errorf("审批操作未注册执行适配器: %s", req.Operation.Kind)
	}
	permit := Permit{requestID: req.RequestID, operation: req.Operation.Kind, payloadHash: payloadHash}
	return adapter.Execute(req, permit)
}
