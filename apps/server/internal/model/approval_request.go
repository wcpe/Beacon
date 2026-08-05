package model

import "time"

// 审批状态。
const (
	ApprovalStatusPending   = "pending"
	ApprovalStatusWithdrawn = "withdrawn"
	ApprovalStatusRejected  = "rejected"
	ApprovalStatusExpired   = "expired"
	ApprovalStatusExecuting = "executing"
	ApprovalStatusSucceeded = "succeeded"
	ApprovalStatusFailed    = "failed"
)

// IsTerminalApprovalStatus 判断审批请求是否已终结。
func IsTerminalApprovalStatus(status string) bool {
	switch status {
	case ApprovalStatusWithdrawn, ApprovalStatusRejected, ApprovalStatusExpired, ApprovalStatusSucceeded, ApprovalStatusFailed:
		return true
	default:
		return false
	}
}

// ApprovalRequest 是跨域高风险动作的统一审批请求。
type ApprovalRequest struct {
	ID                  uint       `gorm:"primaryKey;autoIncrement"`
	RequestID           string     `gorm:"column:request_id;size:48;uniqueIndex"`
	OperationKey        string     `gorm:"column:operation_key;size:96;index:idx_approval_operation,priority:1;uniqueIndex:uniq_approval_idempotency,priority:3"`
	OperationKind       string     `gorm:"column:operation_kind;size:64;not null;index:idx_approval_operation_kind,priority:1"`
	SchemaVersion       int        `gorm:"column:schema_version;not null;default:1"`
	RequiredCapability  string     `gorm:"column:required_capability;size:64;not null;default:''"`
	RiskLevel           string     `gorm:"column:risk_level;size:16;index:idx_approval_risk"`
	NamespaceID         *uint      `gorm:"column:namespace_id;index:idx_approval_namespace_status,priority:1"`
	ResourceType        string     `gorm:"column:resource_type;size:64;not null;index:idx_approval_resource,priority:1"`
	ResourceID          string     `gorm:"column:resource_id;size:128;not null;index:idx_approval_resource,priority:2"`
	IdempotencyKey      string     `gorm:"column:idempotency_key;size:64;index:idx_approval_idempotency;uniqueIndex:uniq_approval_idempotency,priority:4"`
	RequestReason       string     `gorm:"column:request_reason;size:512"`
	SafeSummary         string     `gorm:"column:safe_summary;type:text"`
	PreconditionSummary string     `gorm:"column:precondition_summary;type:text"`
	ImpactSummary       string     `gorm:"column:impact_summary;type:text"`
	EvidenceSnapshot    string     `gorm:"column:evidence_snapshot;type:text"`
	Payload             string     `gorm:"column:payload;type:text;not null"`
	FrozenPayloadSHA256 string     `gorm:"column:frozen_payload_sha256;size:64"`
	Status              string     `gorm:"column:status;size:16;not null;index:idx_approval_status"`
	Attempt             int        `gorm:"column:attempt;not null;default:0"`
	LeaseOwner          string     `gorm:"column:lease_owner;size:128;index:idx_approval_lease"`
	LeaseUntil          *time.Time `gorm:"column:lease_until;index:idx_approval_lease"`
	ResultRef           string     `gorm:"column:result_ref;size:256"`
	FailureSummary      string     `gorm:"column:failure_summary;type:text"`
	RequesterType       string     `gorm:"column:requester_type;size:32;index:idx_approval_requester,priority:1;uniqueIndex:uniq_approval_idempotency,priority:1"`
	RequesterID         string     `gorm:"column:requester_id;size:128;index:idx_approval_requester,priority:2;uniqueIndex:uniq_approval_idempotency,priority:2"`
	RequestedBy         string     `gorm:"column:requested_by;size:128;not null"`
	DeciderType         string     `gorm:"column:decider_type;size:32"`
	DeciderID           string     `gorm:"column:decider_id;size:128"`
	ApprovedBy          *string    `gorm:"column:approved_by;size:128"`
	RejectReason        string     `gorm:"column:reject_reason;size:512"`
	DecisionReason      string     `gorm:"column:decision_reason;size:512"`
	FailureReason       string     `gorm:"column:failure_reason;size:512"`
	ExpiresAt           *time.Time `gorm:"column:expires_at;index"`
	ApprovedAt          *time.Time `gorm:"column:approved_at"`
	DecidedAt           *time.Time `gorm:"column:decided_at"`
	ExecutedAt          *time.Time `gorm:"column:executed_at"`
	FinishedAt          *time.Time `gorm:"column:finished_at"`
	Version             uint       `gorm:"column:version;not null;default:1"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ApprovalExecutionReceipt 是最小执行回执，用于已完成副作用的收敛判断。
type ApprovalExecutionReceipt struct {
	ID           uint   `gorm:"primaryKey;autoIncrement"`
	RequestID    string `gorm:"column:request_id;size:48;not null;uniqueIndex"`
	OperationKey string `gorm:"column:operation_key;size:96;not null"`
	PayloadHash  string `gorm:"column:payload_hash;size:64;not null"`
	ResultRef    string `gorm:"column:result_ref;size:256;not null"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TableName 固定执行回执表名。
func (ApprovalExecutionReceipt) TableName() string { return "approval_execution_receipt" }

// ApprovalCredentialSecret 保存审批成功后待一次性兑换的凭据密文。
type ApprovalCredentialSecret struct {
	ID                uint       `gorm:"primaryKey;autoIncrement"`
	ApprovalRequestID string     `gorm:"column:approval_request_id;size:48;not null;uniqueIndex"`
	Ciphertext        string     `gorm:"column:ciphertext;type:text;not null"`
	ConsumedAt        *time.Time `gorm:"column:consumed_at"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// TableName 固定一次性凭据密文表名。
func (ApprovalCredentialSecret) TableName() string { return "approval_credential_secret" }

// TableName 固定表名为 approval_request。
func (ApprovalRequest) TableName() string { return "approval_request" }
