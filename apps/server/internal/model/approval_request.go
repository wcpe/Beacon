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
	RiskLevel           string     `gorm:"column:risk_level;size:16;index:idx_approval_risk"`
	ResourceType        string     `gorm:"column:resource_type;size:64;not null;index:idx_approval_resource,priority:1"`
	ResourceID          string     `gorm:"column:resource_id;size:128;not null;index:idx_approval_resource,priority:2"`
	IdempotencyKey      string     `gorm:"column:idempotency_key;size:64;index:idx_approval_idempotency;uniqueIndex:uniq_approval_idempotency,priority:4"`
	RequestReason       string     `gorm:"column:request_reason;size:512"`
	SafeSummary         string     `gorm:"column:safe_summary;type:text"`
	Payload             string     `gorm:"column:payload;type:text;not null"`
	FrozenPayloadSHA256 string     `gorm:"column:frozen_payload_sha256;size:64"`
	Status              string     `gorm:"column:status;size:16;not null;index:idx_approval_status"`
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

// TableName 固定表名为 approval_request。
func (ApprovalRequest) TableName() string { return "approval_request" }
