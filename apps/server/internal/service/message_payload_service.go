package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

type messagePayloadAuditWriter interface {
	Create(entry *model.AuditLog) error
}

type messageNamespaceReader interface {
	FindByID(id uint) (*model.Namespace, error)
}

// MessagePayloadService 管理 payload 直读审计与旧审批兼容入口。
type MessagePayloadService struct {
	repo        *repository.MessageRepository
	auditWriter messagePayloadAuditWriter
	namespaces  messageNamespaceReader
	approval    *ApprovalService
	grants      *SensitiveAccessGrantService
}

// NewMessagePayloadService 构造服务。
func NewMessagePayloadService(repo *repository.MessageRepository, auditWriters ...messagePayloadAuditWriter) *MessagePayloadService {
	svc := &MessagePayloadService{repo: repo}
	if len(auditWriters) != 0 {
		svc.auditWriter = auditWriters[0]
	}
	return svc
}

// SetNamespaceRepository 装配消息所属 namespace 查询器，用于范围审计与 namespaceCode 归属。
func (s *MessagePayloadService) SetNamespaceRepository(namespaces messageNamespaceReader) {
	if s != nil {
		s.namespaces = namespaces
	}
}

// SetSensitiveAccessApproval 装配 payload 审批申请和一次性授权消费链路。
func (s *MessagePayloadService) SetSensitiveAccessApproval(approval *ApprovalService, grants *SensitiveAccessGrantService) {
	s.approval = approval
	s.grants = grants
}

// ViewPayloadParams 是一次 payload 查看请求（operator/clientIp/traceId 为鉴权链与请求上下文注入）。
type ViewPayloadParams struct {
	MessageID    string
	Reason       string
	Operator     string
	ClientIP     string
	TraceID      string
	NamespaceIDs []uint
	Scoped       bool
}

// PayloadResult 是 payload 查看响应（对齐 contracts MessagePayloadResponse）。
type PayloadResult struct {
	Payload string
	SHA256  string
	Size    int
}

// View 直接读取 payload；先写入仅含消息引用、哈希、大小与原因文本的审计，再返回正文。
// 审计写失败即拒绝返回正文，保证「看了必留痕」。
func (s *MessagePayloadService) View(p ViewPayloadParams) (PayloadResult, error) {
	if s == nil || s.repo == nil || s.auditWriter == nil {
		return PayloadResult{}, apperr.ErrInternal
	}
	if strings.TrimSpace(p.MessageID) == "" || strings.TrimSpace(p.Operator) == "" {
		return PayloadResult{}, apperr.ErrInvalidParam
	}
	reason := strings.TrimSpace(p.Reason)
	if reason == "" {
		return PayloadResult{}, apperr.ErrApprovalReasonRequired
	}
	if len([]rune(reason)) > 255 {
		return PayloadResult{}, apperr.ErrInvalidParam
	}
	trace, err := s.repo.FindByMessageID(p.MessageID)
	if err != nil {
		return PayloadResult{}, err
	}
	if trace == nil || (p.Scoped && !namespaceInScope(trace.NamespaceID, p.NamespaceIDs)) {
		return PayloadResult{}, apperr.ErrMessageNotFound
	}
	if s.namespaces == nil {
		return PayloadResult{}, apperr.ErrInternal
	}
	namespace, err := s.namespaces.FindByID(trace.NamespaceID)
	if err != nil {
		return PayloadResult{}, err
	}
	if namespace == nil {
		return PayloadResult{}, apperr.ErrMessageNotFound
	}
	payload, err := s.repo.FindPayload(p.MessageID)
	if err != nil {
		return PayloadResult{}, err
	}
	if payload == nil || payload.Payload == "" {
		return PayloadResult{}, apperr.ErrMessageNotFound
	}
	hash := payloadSHA256(payload.Payload)
	result := PayloadResult{Payload: payload.Payload, SHA256: hash, Size: len(payload.Payload)}
	// detail 只记消息引用、哈希、大小与原因文本（合规追溯「谁为什么看了正文」），
	// 绝不记 payload 正文；用 json.Marshal 保证 detail 恒为合法 JSON。
	detail, err := json.Marshal(map[string]any{
		"messageId": p.MessageID, "sha256": hash, "size": len(payload.Payload), "reason": auditReasonText(reason),
	})
	if err != nil {
		return PayloadResult{}, err
	}
	if err := s.auditWriter.Create(&model.AuditLog{
		NamespaceCode: namespace.Code, Operator: p.Operator, Action: model.ActionMessagePayloadView, TargetType: model.TargetTypeMessage,
		TargetRef: p.MessageID, Detail: string(detail), Result: model.ResultOK, ClientIP: p.ClientIP,
	}); err != nil {
		return PayloadResult{}, err
	}
	return result, nil
}

// RequestAccess 创建消息 payload 的专用审批申请；冻结消息 ID 与当前正文 SHA-256，绝不冻结正文。
func (s *MessagePayloadService) RequestAccess(messageID, reason, idempotencyKey string, principal auth.Principal, clientIP string) (model.ApprovalRequest, error) {
	if s == nil || s.repo == nil || s.approval == nil {
		// 装配缺失是服务端问题，不把故障甩给调用方（此前与参数错误混用同一 400）。
		return model.ApprovalRequest{}, apperr.ErrInternal
	}
	if strings.TrimSpace(reason) == "" || strings.TrimSpace(idempotencyKey) == "" {
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
