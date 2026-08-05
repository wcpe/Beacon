package authz

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// 操作类型。
const (
	OperationDeliveryApprove        = "delivery.approve"
	OperationDeliveryResume         = "delivery.resume"
	OperationDeliveryConfirmBatch   = "delivery.confirm_batch"
	OperationDeliveryRollback       = "delivery.rollback"
	OperationDeliveryRollbackFinish = "delivery.rollback_finish"
	OperationDeliveryDraftDelete    = "delivery.draft_delete"

	OperationIdentityApprove         = "identity.approve"
	OperationIdentityUnbind          = "identity.unbind"
	OperationIdentityResolveConflict = "identity.resolve_conflict"
	OperationIdentityEnable          = "identity.enable"
	OperationIdentityAllowReapply    = "identity.allow_reapply"

	OperationCredentialCreate     = "credential.create"
	OperationCredentialRotate     = "credential.rotate"
	OperationMCPOAuthClientCreate = "mcp.client.create"
	OperationMCPOAuthClientRotate = "mcp.client.rotate"
	OperationMCPOAuthClientEnable = "mcp.client.enable"

	OperationNamespaceTrustGrant = "namespace_trust.grant"

	OperationTopologyServerAssign       = "topology.server_assign"
	OperationTopologyServerRezone       = "topology.server_rezone"
	OperationTopologyDefaultEntryChange = "topology.default_entry.change"
	OperationTopologyLobbyMemberMove    = "topology.lobby_member.move"
	OperationTopologyDrainingDisable    = "topology.draining.disable"

	OperationServerArchive            = "server.archive"
	OperationServerRestore            = "server.restore"
	OperationServerPermanentDelete    = "server.permanent_delete"
	OperationNamespaceArchive         = "namespace.archive"
	OperationNamespaceRestore         = "namespace.restore"
	OperationNamespacePermanentDelete = "namespace.permanent_delete"

	OperationSystemUpdateApply    = "system.update.apply"
	OperationSystemUpdateRollback = "system.update.rollback"
	OperationSettingsDangerous    = "settings.update.dangerous"
	OperationConfigPublish        = "config.publish"
	OperationConfigRollback       = "config.rollback"
	OperationConfigGrayPublish    = "config.gray_publish"
	OperationConfigGrayPromote    = "config.gray_promote"
	OperationConfigDelete         = "config.delete"
	OperationConfigBatchDelete    = "config.batch_delete"
	OperationConfigBatchDisable   = "config.batch_disable"
	OperationConfigBatchEnable    = "config.batch_enable"
	OperationFilePublish          = "file.publish"
	OperationFileRollback         = "file.rollback"
	OperationFileDelete           = "file.delete"
	OperationFileCreate           = "file.create"
	OperationFileImport           = "file.import"
	OperationFileBatchDelete      = "file.batch_delete"
	OperationFileBatchDisable     = "file.batch_disable"
	OperationFileBatchEnable      = "file.batch_enable"
	OperationOverrideSetPublish   = "override_set.publish"
	OperationOverrideSetRollback  = "override_set.rollback"
	OperationOverrideSetDelete    = "override_set.delete"

	OperationAgentCommandTailLogs      = "agent.command.tail_logs"
	OperationAgentCommandFSBrowse      = "agent.command.fs_browse"
	OperationAgentCommandResync        = "agent.command.resync"
	OperationAgentCommandReverseScan   = "agent.command.reverse_scan"
	OperationAgentCommandReverseSubmit = "agent.command.reverse_submit"
	OperationAgentCommandImprint       = "agent.command.imprint"
	// OperationAgentCommandDirectoryResync 与普通重同步共用同一审批操作键。
	OperationAgentCommandDirectoryResync = OperationAgentCommandResync

	OperationMessagePayloadRead           = "message.payload.read"
	OperationSensitiveFileContentRead     = "file.sensitive_content_read"
	OperationSensitiveConfigPlaintextRead = "config.sensitive_plaintext_read"
)

// OperationDescriptor 描述 operation 的授权分类与冻结参数版本。
type OperationDescriptor struct {
	Key                      string
	SchemaVersion            int
	Capability               string
	RiskLevel                string
	RequiresTerminalCallback bool
}

// Operation 描述一次待授权 / 待审批的业务动作。
type Operation struct {
	Kind                string
	NamespaceID         *uint
	Resource            string
	ResourceID          string
	IdempotencyKey      string
	RiskLevel           string
	Reason              string
	PreconditionSummary string
	ImpactSummary       string
	EvidenceSnapshot    []ApprovalEvidenceLine
}

// Authorize 校验主体是否可发起指定操作。
func Authorize(principal auth.Principal, op Operation) error {
	return authorizeCapability(principal, op, capabilityFor(op.Kind))
}

// AuthorizeDescriptor 按注册表登记的 descriptor 校验审批申请权限。
func AuthorizeDescriptor(principal auth.Principal, op Operation, descriptor OperationDescriptor) error {
	return authorizeCapability(principal, op, descriptor.Capability)
}

func authorizeCapability(principal auth.Principal, _ Operation, capability string) error {
	principal = auth.NormalizePrincipal(principal)
	if capability == "" || !principal.HasCapability(capability) {
		return apperr.ErrForbidden
	}
	return nil
}

func capabilityFor(kind string) string {
	switch kind {
	case OperationDeliveryApprove,
		OperationDeliveryResume,
		OperationDeliveryConfirmBatch,
		OperationDeliveryRollback,
		OperationDeliveryRollbackFinish,
		OperationDeliveryDraftDelete,
		OperationIdentityApprove,
		OperationIdentityUnbind,
		OperationIdentityResolveConflict,
		OperationIdentityEnable,
		OperationIdentityAllowReapply,
		OperationCredentialCreate,
		OperationCredentialRotate,
		OperationMCPOAuthClientCreate,
		OperationMCPOAuthClientRotate,
		OperationMCPOAuthClientEnable,
		OperationNamespaceTrustGrant,
		OperationTopologyServerAssign,
		OperationTopologyServerRezone,
		OperationTopologyDefaultEntryChange,
		OperationTopologyLobbyMemberMove,
		OperationTopologyDrainingDisable,
		OperationServerArchive,
		OperationServerRestore,
		OperationServerPermanentDelete,
		OperationNamespaceArchive,
		OperationNamespaceRestore,
		OperationNamespacePermanentDelete,
		OperationSystemUpdateApply,
		OperationSystemUpdateRollback,
		OperationSettingsDangerous,
		OperationConfigPublish,
		OperationConfigRollback,
		OperationConfigGrayPublish,
		OperationConfigGrayPromote,
		OperationConfigDelete,
		OperationConfigBatchDelete,
		OperationConfigBatchDisable,
		OperationConfigBatchEnable,
		OperationFilePublish,
		OperationFileRollback,
		OperationFileDelete,
		OperationFileCreate,
		OperationFileImport,
		OperationFileBatchDelete,
		OperationFileBatchDisable,
		OperationFileBatchEnable,
		OperationOverrideSetPublish,
		OperationOverrideSetRollback,
		OperationOverrideSetDelete:
		return auth.CapabilityApprovalRequest
	case OperationAgentCommandTailLogs,
		OperationAgentCommandFSBrowse,
		OperationAgentCommandResync,
		OperationAgentCommandReverseScan,
		OperationAgentCommandReverseSubmit,
		OperationAgentCommandImprint,
		OperationMessagePayloadRead,
		OperationSensitiveFileContentRead,
		OperationSensitiveConfigPlaintextRead:
		return auth.CapabilityApprovalRequest
	default:
		return ""
	}
}

// ApprovalRequest 是执行适配器所需的冻结审批请求。
type ApprovalRequest struct {
	ID                 uint
	RequestID          string
	Operation          Operation
	OperationKey       string
	SchemaVersion      int
	RequiredCapability string
	Payload            []byte
	PayloadHash        string
	Status             string
	LeaseOwner         string
	LeaseUntil         *time.Time
	DeciderType        string
	DeciderID          string
	ApprovedAt         *time.Time
	DecisionReason     string
	Actor              string
	RequesterType      string
	RequesterID        string
	Version            uint
}

// Permit 是审批内核生成的执行许可，外部包无法字面量伪造。
type Permit struct {
	requestID     string
	operation     string
	schemaVersion int
	payloadHash   string
	leaseToken    string
	version       uint
}

// RequestID 返回许可绑定的公开审批 ID。
func (p Permit) RequestID() string { return p.requestID }

// Operation 返回许可绑定的操作。
func (p Permit) Operation() string { return p.operation }

// SchemaVersion 返回许可绑定的载荷 schema 版本。
func (p Permit) SchemaVersion() int { return p.schemaVersion }

// PayloadHash 返回许可绑定的冻结载荷哈希。
func (p Permit) PayloadHash() string { return p.payloadHash }

// LeaseToken 返回许可绑定的执行租约令牌。
func (p Permit) LeaseToken() string { return p.leaseToken }

// Version 返回许可绑定的审批行版本。
func (p Permit) Version() uint { return p.version }

// Adapter 执行已审批通过的业务动作。
type Adapter interface {
	Execute(req ApprovalRequest, permit Permit) error
}

// AdapterFunc 让普通函数满足 Adapter。
type AdapterFunc func(req ApprovalRequest, permit Permit) error

func (f AdapterFunc) Execute(req ApprovalRequest, permit Permit) error {
	return f(req, permit)
}

// TransactionalAdapter 在 worker 提供的数据库事务中执行领域写入，并返回提交后的内存动作。
type TransactionalAdapter interface {
	Adapter
	ExecuteInTx(tx *gorm.DB, req ApprovalRequest, permit Permit) (func(), error)
}

// ApprovalRequestTransactionalAdapter 在审批申请创建事务内准备领域状态。
// 敏感读取适配器必须在这里创建 pending grant，禁止由 handler 或事务外代码补写。
type ApprovalRequestTransactionalAdapter interface {
	Adapter
	PrepareApprovalRequestInTx(tx *gorm.DB, req ApprovalRequest) error
}

// TerminalAdapter 在审批请求进入拒绝、撤回或过期终态时，同一事务内收敛领域状态。
type TerminalAdapter interface {
	Adapter
	CompleteTerminalInTx(tx *gorm.DB, req ApprovalRequest, status string) error
}

// ApprovalEvidence 是领域适配器返回的脱敏实时审批证据。
type ApprovalEvidence struct {
	EvidenceStatus      string
	DriftStatus         string
	CurrentFactsSummary []ApprovalEvidenceLine
	CurrentDiff         []ApprovalEvidenceDiffLine
}

// ApprovalEvidenceLine 是审批详情中的一条脱敏事实。
type ApprovalEvidenceLine struct {
	Label string
	Value string
}

// ApprovalEvidenceDiffLine 是审批快照与当前事实的脱敏差异。
type ApprovalEvidenceDiffLine struct {
	Label    string
	Snapshot string
	Current  string
	Changed  bool
}

// ApprovalEvidenceReader 由领域适配器读取当前事实，不得从冻结载荷推断。
type ApprovalEvidenceReader interface {
	ReadApprovalEvidence(req ApprovalRequest) (ApprovalEvidence, error)
}

// TransactionalAdapterFunc 让事务函数满足 TransactionalAdapter。
type TransactionalAdapterFunc func(tx *gorm.DB, req ApprovalRequest, permit Permit) (func(), error)

// Execute 防止事务适配器被误用在非事务执行路径。
func (TransactionalAdapterFunc) Execute(ApprovalRequest, Permit) error {
	return apperr.ErrForbidden
}

func (f TransactionalAdapterFunc) ExecuteInTx(tx *gorm.DB, req ApprovalRequest, permit Permit) (func(), error) {
	return f(tx, req, permit)
}

type receiptRequiredAdapter struct{ Adapter }

func (receiptRequiredAdapter) RequiresExecutionReceipt() bool { return true }

func (a receiptRequiredAdapter) ExecuteInTx(tx *gorm.DB, req ApprovalRequest, permit Permit) (func(), error) {
	adapter, ok := a.Adapter.(TransactionalAdapter)
	if !ok {
		return nil, apperr.ErrForbidden
	}
	return adapter.ExecuteInTx(tx, req, permit)
}

func (a receiptRequiredAdapter) ReadApprovalEvidence(req ApprovalRequest) (ApprovalEvidence, error) {
	reader, ok := a.Adapter.(ApprovalEvidenceReader)
	if !ok {
		return ApprovalEvidence{}, apperr.ErrForbidden
	}
	return reader.ReadApprovalEvidence(req)
}

func (a receiptRequiredAdapter) PrepareApprovalRequestInTx(tx *gorm.DB, req ApprovalRequest) error {
	hook, ok := a.Adapter.(ApprovalRequestTransactionalAdapter)
	if !ok {
		return nil
	}
	return hook.PrepareApprovalRequestInTx(tx, req)
}

// CompleteTerminalInTx 将终态回调透传给被回执包装的领域适配器。
func (a receiptRequiredAdapter) CompleteTerminalInTx(tx *gorm.DB, req ApprovalRequest, status string) error {
	terminal, ok := a.Adapter.(TerminalAdapter)
	if !ok {
		return apperr.ErrForbidden
	}
	return terminal.CompleteTerminalInTx(tx, req, status)
}

// RequireExecutionReceipt 标记适配器必须在领域写入同一事务中提交执行回执。
func RequireExecutionReceipt(adapter Adapter) Adapter {
	if adapter == nil {
		return nil
	}
	return receiptRequiredAdapter{Adapter: adapter}
}

// ApprovalRegistry 保存已明确登记的危险操作与执行适配器。
type ApprovalRegistry struct {
	mu          sync.RWMutex
	adapters    map[string]Adapter
	descriptors map[string]OperationDescriptor
}

// NewApprovalRegistry 构造审批适配器注册表。
func NewApprovalRegistry() *ApprovalRegistry {
	return &ApprovalRegistry{adapters: map[string]Adapter{}, descriptors: map[string]OperationDescriptor{}}
}

// Register 注册一种已知危险操作的执行适配器。
func (r *ApprovalRegistry) Register(kind string, adapter Adapter) {
	descriptor := OperationDescriptor{
		Key: kind, SchemaVersion: 1, Capability: capabilityFor(kind), RiskLevel: "high",
	}
	if descriptor.Capability == auth.CapabilityApprovalRequest {
		r.RegisterDescriptor(descriptor, adapter)
	}
}

// RegisterDescriptor 显式登记操作描述与执行适配器。
func (r *ApprovalRegistry) RegisterDescriptor(descriptor OperationDescriptor, adapter Adapter) {
	if descriptor.Key == "" || descriptor.SchemaVersion < 1 || descriptor.Capability == "" || adapter == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[descriptor.Key] = adapter
	r.descriptors[descriptor.Key] = descriptor
}

// Descriptor 返回已登记操作描述。
func (r *ApprovalRegistry) Descriptor(kind string) (OperationDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	descriptor, ok := r.descriptors[kind]
	if !ok || r.adapters[kind] == nil {
		return OperationDescriptor{}, false
	}
	return descriptor, true
}

// ValidateOperation 校验操作是否同时登记了描述与适配器。
func (r *ApprovalRegistry) ValidateOperation(kind string) (OperationDescriptor, error) {
	descriptor, ok := r.Descriptor(kind)
	if !ok {
		return OperationDescriptor{}, apperr.ErrForbidden
	}
	return descriptor, nil
}

// RequiresExecutionReceipt 返回适配器是否必须自行提交事务回执。
func (r *ApprovalRegistry) RequiresExecutionReceipt(kind string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	adapter := r.adapters[kind]
	required, ok := adapter.(interface{ RequiresExecutionReceipt() bool })
	return ok && required.RequiresExecutionReceipt()
}

// ReadEvidence 读取已登记领域适配器的实时审批证据；未支持或读取失败时返回不可用。
func (r *ApprovalRegistry) ReadEvidence(req ApprovalRequest) ApprovalEvidence {
	unavailable := ApprovalEvidence{EvidenceStatus: "unavailable", DriftStatus: "none"}
	r.mu.RLock()
	adapter := r.adapters[req.Operation.Kind]
	r.mu.RUnlock()
	reader, ok := adapter.(ApprovalEvidenceReader)
	if !ok || reader == nil {
		return unavailable
	}
	evidence, err := reader.ReadApprovalEvidence(req)
	if err != nil || evidence.EvidenceStatus != "available" {
		return unavailable
	}
	if evidence.DriftStatus == "" {
		evidence.DriftStatus = "none"
	}
	return evidence
}

// ExecuteTerminalInTx 在领域声明需要终态回调时执行回调；审批事实只能从当前事务重读。
func (r *ApprovalRegistry) ExecuteTerminalInTx(tx *gorm.DB, requestID string, version uint, status, actor string) error {
	if tx == nil {
		return apperr.ErrForbidden
	}
	req, err := loadApprovalRequest(tx, requestID)
	if err != nil || !terminalApprovalStatus(status) || req.Status != status || req.Version != version {
		return apperr.ErrForbidden
	}
	if req.PayloadHash == "" || frozenPayloadHash(req.Payload) != req.PayloadHash {
		return apperr.ErrForbidden
	}
	r.mu.RLock()
	adapter := r.adapters[req.Operation.Kind]
	descriptor, described := r.descriptors[req.Operation.Kind]
	r.mu.RUnlock()
	if !described || adapter == nil || descriptor.Key != req.Operation.Kind {
		return apperr.ErrForbidden
	}
	if !descriptor.RequiresTerminalCallback {
		return nil
	}
	terminal, ok := adapter.(TerminalAdapter)
	if !ok {
		return apperr.ErrForbidden
	}
	req.Actor = actor
	return terminal.CompleteTerminalInTx(tx, req, status)
}

func terminalApprovalStatus(status string) bool {
	return status == model.ApprovalStatusRejected || status == model.ApprovalStatusWithdrawn || status == model.ApprovalStatusExpired
}

// Execute 已废弃：危险操作必须在事务中由 worker 基于数据库事实执行。
func (r *ApprovalRegistry) Execute(ApprovalRequest, string) error { return apperr.ErrForbidden }

// ExecuteInTx 在调用方提供的事务中执行已登记的事务适配器。
func (r *ApprovalRegistry) ExecuteInTx(tx *gorm.DB, requestID, leaseToken string) (func(), error) {
	if tx == nil {
		return nil, apperr.ErrForbidden
	}
	req, err := loadApprovalRequest(tx, requestID)
	if err != nil {
		return nil, err
	}
	adapter, permit, err := r.executionAdapter(req, leaseToken)
	if err != nil {
		return nil, err
	}
	transactional, ok := adapter.(TransactionalAdapter)
	if !ok {
		return nil, apperr.ErrForbidden
	}
	return transactional.ExecuteInTx(tx, req, permit)
}

// PrepareApprovalRequestInTx 让已登记适配器在审批申请落库后、同一事务内创建其冻结领域状态。
func (r *ApprovalRegistry) PrepareApprovalRequestInTx(tx *gorm.DB, req ApprovalRequest) error {
	if tx == nil || req.RequestID == "" {
		return apperr.ErrForbidden
	}
	r.mu.RLock()
	adapter := r.adapters[req.Operation.Kind]
	r.mu.RUnlock()
	hook, ok := adapter.(ApprovalRequestTransactionalAdapter)
	if !ok {
		return nil
	}
	return hook.PrepareApprovalRequestInTx(tx, req)
}

func (r *ApprovalRegistry) executionAdapter(req ApprovalRequest, leaseToken string) (Adapter, Permit, error) {
	if req.Status != model.ApprovalStatusExecuting || req.RequestID == "" || req.LeaseOwner == "" || req.LeaseUntil == nil {
		return nil, Permit{}, apperr.ErrForbidden
	}
	if !time.Now().UTC().Before(req.LeaseUntil.UTC()) || req.DeciderType != auth.PrincipalKindHuman || req.DeciderID == "" || req.ApprovedAt == nil {
		return nil, Permit{}, apperr.ErrForbidden
	}
	r.mu.RLock()
	adapter, registered := r.adapters[req.Operation.Kind]
	descriptor, described := r.descriptors[req.Operation.Kind]
	r.mu.RUnlock()
	if !registered || !described || adapter == nil || descriptor.SchemaVersion != req.SchemaVersion || descriptor.SchemaVersion < 1 {
		return nil, Permit{}, apperr.ErrForbidden
	}
	if req.OperationKey != descriptor.Key || req.RequiredCapability != descriptor.Capability || req.PayloadHash == "" || req.LeaseOwner != leaseToken {
		return nil, Permit{}, apperr.ErrForbidden
	}
	if frozenPayloadHash(req.Payload) != req.PayloadHash {
		return nil, Permit{}, apperr.ErrForbidden
	}
	permit := Permit{
		requestID: req.RequestID, operation: req.Operation.Kind, schemaVersion: req.SchemaVersion,
		payloadHash: req.PayloadHash, leaseToken: req.LeaseOwner, version: req.Version,
	}
	return adapter, permit, nil
}

func loadApprovalRequest(tx *gorm.DB, requestID string) (ApprovalRequest, error) {
	if requestID == "" {
		return ApprovalRequest{}, apperr.ErrForbidden
	}
	var stored model.ApprovalRequest
	if err := tx.Where("request_id = ?", requestID).First(&stored).Error; err != nil {
		return ApprovalRequest{}, err
	}
	return ApprovalRequest{
		ID: stored.ID, RequestID: stored.RequestID,
		Operation: Operation{Kind: stored.OperationKind, NamespaceID: stored.NamespaceID, Resource: stored.ResourceType,
			ResourceID: stored.ResourceID, IdempotencyKey: stored.IdempotencyKey, RiskLevel: stored.RiskLevel, Reason: stored.RequestReason},
		OperationKey: stored.OperationKey, SchemaVersion: stored.SchemaVersion, RequiredCapability: stored.RequiredCapability,
		Payload: []byte(stored.Payload), PayloadHash: stored.FrozenPayloadSHA256, Status: stored.Status,
		LeaseOwner: stored.LeaseOwner, LeaseUntil: stored.LeaseUntil, DeciderType: stored.DeciderType,
		DeciderID: stored.DeciderID, ApprovedAt: stored.ApprovedAt, DecisionReason: stored.DecisionReason,
		Actor: auditActor(stored.ApprovedBy), RequesterType: stored.RequesterType, RequesterID: stored.RequesterID, Version: stored.Version,
	}, nil
}

func frozenPayloadHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func auditActor(actor *string) string {
	if actor == nil {
		return ""
	}
	return *actor
}

// RetryableError 标记仅允许 worker 重试的明确错误。
type RetryableError struct{ err error }

func (e *RetryableError) Error() string { return e.err.Error() }
func (e *RetryableError) Unwrap() error { return e.err }

// Retryable 将明确的临时错误标记为可重试。
func Retryable(err error) error {
	if err == nil {
		return nil
	}
	return &RetryableError{err: err}
}

// IsRetryable 判断错误是否明确标记为可重试。
func IsRetryable(err error) bool {
	var target *RetryableError
	return errors.As(err, &target)
}
