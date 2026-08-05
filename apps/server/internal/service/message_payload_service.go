package service

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// MessagePayloadService 管理 payload 的审批申请与一次性受控读取。
type MessagePayloadService struct {
	repo     *repository.MessageRepository
	approval *ApprovalService
	grants   *SensitiveAccessGrantService
}

// NewMessagePayloadService 构造服务。
func NewMessagePayloadService(repo *repository.MessageRepository) *MessagePayloadService {
	return &MessagePayloadService{repo: repo}
}

// SetSensitiveAccessApproval 装配 payload 审批申请和一次性授权消费链路。
func (s *MessagePayloadService) SetSensitiveAccessApproval(approval *ApprovalService, grants *SensitiveAccessGrantService) {
	s.approval = approval
	s.grants = grants
}

// ViewPayloadParams 是一次 payload 查看请求（operator/clientIp/traceId 为鉴权链与请求上下文注入）。
type ViewPayloadParams struct {
	MessageID string
	Reason    string
	Operator  string
	ClientIP  string
	TraceID   string
}

// PayloadResult 是 payload 查看响应（对齐 contracts MessagePayloadResponse）。
type PayloadResult struct {
	Payload string
	SHA256  string
	Size    int
}

// View 是旧正文直出入口，现已永久关闭。
func (s *MessagePayloadService) View(p ViewPayloadParams) (PayloadResult, error) {
	return PayloadResult{}, apperr.ErrOperationRequiresApproval
}

// RequestAccess 创建消息 payload 的专用审批申请；冻结消息 ID 与当前正文 SHA-256，绝不冻结正文。
func (s *MessagePayloadService) RequestAccess(messageID, reason, idempotencyKey string, principal auth.Principal, clientIP string) (model.ApprovalRequest, error) {
	if s == nil || s.repo == nil || s.approval == nil || strings.TrimSpace(reason) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return model.ApprovalRequest{}, apperr.ErrInvalidParam
	}
	payload, err := s.repo.FindPayload(messageID)
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	if payload == nil {
		return model.ApprovalRequest{}, apperr.ErrMessageNotFound
	}
	contentHash := payloadSHA256(payload.Payload)
	return s.approval.Request(authz.Operation{
		Kind: authz.OperationMessagePayloadRead, Resource: "message", ResourceID: messageID,
		IdempotencyKey: idempotencyKey, Reason: reason,
	}, map[string]any{"messageId": messageID, "sha256": contentHash}, principal, clientIP)
}

// Consume 复核当前正文 SHA-256 后，原子消费原申请主体的一次性授权并返回正文。
func (s *MessagePayloadService) Consume(grantID, messageID string, principal auth.Principal) (PayloadResult, error) {
	if s == nil || s.repo == nil || s.grants == nil {
		return PayloadResult{}, apperr.ErrForbidden
	}
	payload, err := s.repo.FindPayload(messageID)
	if err != nil {
		return PayloadResult{}, err
	}
	if payload == nil {
		return PayloadResult{}, apperr.ErrMessageNotFound
	}
	contentHash := payloadSHA256(payload.Payload)
	target, err := authz.NewSensitiveAccessTarget("message", messageID, contentHash)
	if err != nil {
		return PayloadResult{}, err
	}
	if err := s.grants.Consume(grantID, principal, authz.OperationMessagePayloadRead, target.Ref, contentHash, timeNowUTC()); err != nil {
		return PayloadResult{}, err
	}
	return PayloadResult{Payload: payload.Payload, SHA256: contentHash, Size: len(payload.Payload)}, nil
}

func payloadSHA256(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", sum)
}

func timeNowUTC() time.Time { return time.Now().UTC() }
