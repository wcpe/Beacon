package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// SensitiveConfigPlaintextResult 是一次性消费后的敏感配置正文。
type SensitiveConfigPlaintextResult struct {
	Content string
	SHA256  string
	Size    int
}

type sensitiveConfigPlaintextPayload struct {
	ConfigItemID uint   `json:"configItemId"`
	Version      int64  `json:"version"`
	SHA256       string `json:"sha256"`
}

// RegisterSensitiveConfigApprovalAdapter 注册敏感配置正文读取的唯一事务执行入口。
func RegisterSensitiveConfigApprovalAdapter(registry *authz.ApprovalRegistry, configs *ConfigService, grants *SensitiveAccessGrantService) {
	if registry == nil || configs == nil || grants == nil {
		return
	}
	registry.Register(authz.OperationSensitiveConfigPlaintextRead, authz.RequireExecutionReceipt(sensitiveConfigPlaintextAdapter{configs: configs, grants: grants}))
}

type sensitiveConfigPlaintextAdapter struct {
	configs *ConfigService
	grants  *SensitiveAccessGrantService
}

// Execute 拒绝事务外执行，防止绕过审批 worker 签发授权。
func (sensitiveConfigPlaintextAdapter) Execute(authz.ApprovalRequest, authz.Permit) error {
	return apperr.ErrForbidden
}

func (a sensitiveConfigPlaintextAdapter) PrepareApprovalRequestInTx(tx *gorm.DB, req authz.ApprovalRequest) error {
	payload, err := parseSensitiveConfigPlaintextPayload(req.Payload)
	if err != nil {
		return err
	}
	target, err := sensitiveConfigPlaintextTarget(payload)
	if err != nil {
		return err
	}
	_, err = a.grants.WithTx(tx).CreatePending(req.RequestID, req.RequesterType, req.RequesterID, req.Operation.Kind, target.Ref, target.ContentHash)
	return err
}

func (a sensitiveConfigPlaintextAdapter) ExecuteInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
	if permit.Operation() != authz.OperationSensitiveConfigPlaintextRead {
		return nil, apperr.ErrForbidden
	}
	payload, err := parseSensitiveConfigPlaintextPayload(req.Payload)
	if err != nil {
		return nil, err
	}
	item, err := a.configs.GetInTx(tx, payload.ConfigItemID)
	if err != nil {
		return nil, err
	}
	if !item.Sensitive || item.Version != payload.Version || configContentHash(item.Content) != payload.SHA256 {
		return nil, apperr.ErrApprovalTargetChanged
	}
	target, err := sensitiveConfigPlaintextTarget(payload)
	if err != nil {
		return nil, err
	}
	principal := auth.Principal{Kind: req.RequesterType, ID: req.RequesterID}
	if err := a.grants.WithTx(tx).Activate(req.RequestID, principal, req.Operation.Kind, target.Ref, target.ContentHash, time.Now().UTC()); err != nil {
		return nil, err
	}
	return nil, tx.Create(&model.ApprovalExecutionReceipt{
		RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash, ResultRef: target.Ref,
	}).Error
}

// ReadSensitivePlaintext 是旧的直接正文读取入口，永久关闭。
func (s *ConfigService) ReadSensitivePlaintext(uint) (SensitiveConfigPlaintextResult, error) {
	return SensitiveConfigPlaintextResult{}, apperr.ErrOperationRequiresApproval
}

// RequestSensitivePlaintextAccess 冻结当前敏感配置的稳定版本和正文哈希，不保存正文。
func (s *ConfigService) RequestSensitivePlaintextAccess(id uint, reason, idempotencyKey string, principal auth.Principal, clientIP string) (model.ApprovalRequest, error) {
	if s == nil || s.approval == nil || s.grants == nil || strings.TrimSpace(reason) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return model.ApprovalRequest{}, apperr.ErrInvalidParam
	}
	item, err := s.Get(id)
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	if !item.Sensitive {
		return model.ApprovalRequest{}, apperr.ErrForbidden
	}
	payload := sensitiveConfigPlaintextPayload{ConfigItemID: item.ID, Version: item.Version, SHA256: configContentHash(item.Content)}
	return s.approval.Request(authz.Operation{
		Kind: authz.OperationSensitiveConfigPlaintextRead, Resource: "config_item", ResourceID: fmt.Sprintf("%d", item.ID),
		IdempotencyKey: idempotencyKey, Reason: reason,
		PreconditionSummary: fmt.Sprintf("配置项 %d 仍为敏感版本 %d", item.ID, item.Version),
		ImpactSummary:       "一次性读取敏感配置正文",
		EvidenceSnapshot: []authz.ApprovalEvidenceLine{
			{Label: "命名空间", Value: item.NamespaceCode},
			{Label: "配置项", Value: fmt.Sprintf("%d", item.ID)},
			{Label: "版本", Value: fmt.Sprintf("%d", item.Version)},
			{Label: "正文摘要", Value: payload.SHA256[:12]},
		},
	}, map[string]any{"configItemId": payload.ConfigItemID, "version": payload.Version, "sha256": payload.SHA256}, principal, clientIP)
}

// ConsumeSensitivePlaintext 重新核对当前版本后原子消费一次授权，并返回正文。
func (s *ConfigService) ConsumeSensitivePlaintext(grantID string, id uint, principal auth.Principal, clientIP string) (SensitiveConfigPlaintextResult, error) {
	if s == nil || s.grants == nil || grantID == "" || id == 0 {
		return SensitiveConfigPlaintextResult{}, apperr.ErrForbidden
	}
	var result SensitiveConfigPlaintextResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		item, err := s.GetForUpdateInTx(tx, id)
		if err != nil {
			return err
		}
		if !item.Sensitive {
			return apperr.ErrForbidden
		}
		payload := sensitiveConfigPlaintextPayload{ConfigItemID: item.ID, Version: item.Version, SHA256: configContentHash(item.Content)}
		target, err := sensitiveConfigPlaintextTarget(payload)
		if err != nil {
			return err
		}
		if err := s.grants.WithTx(tx).Consume(grantID, principal, authz.OperationSensitiveConfigPlaintextRead, target.Ref, target.ContentHash, timeNowUTC()); err != nil {
			return err
		}
		if err := s.writeAudit(tx, item, principal.AuditRef(), authz.OperationSensitiveConfigPlaintextRead, fmt.Sprintf(`{"version":%d,"sha256":"%s"}`, item.Version, payload.SHA256), clientIP); err != nil {
			return err
		}
		result = SensitiveConfigPlaintextResult{Content: item.Content, SHA256: payload.SHA256, Size: len(item.Content)}
		return nil
	})
	return result, err
}

func parseSensitiveConfigPlaintextPayload(raw []byte) (sensitiveConfigPlaintextPayload, error) {
	var payload sensitiveConfigPlaintextPayload
	if json.Unmarshal(raw, &payload) != nil || payload.ConfigItemID == 0 || payload.Version < 1 || payload.SHA256 == "" {
		return sensitiveConfigPlaintextPayload{}, apperr.ErrInvalidParam
	}
	return payload, nil
}

func sensitiveConfigPlaintextTarget(payload sensitiveConfigPlaintextPayload) (authz.SensitiveAccessTarget, error) {
	return authz.NewSensitiveAccessTarget("config", fmt.Sprintf("%d/%d", payload.ConfigItemID, payload.Version), payload.SHA256)
}
