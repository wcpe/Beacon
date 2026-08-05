package service

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// RegisterMessagePayloadApprovalAdapter 注册消息正文审批的事务适配器。
func RegisterMessagePayloadApprovalAdapter(registry *authz.ApprovalRegistry, messages *repository.MessageRepository, grants *SensitiveAccessGrantService) {
	if registry == nil || messages == nil || grants == nil {
		return
	}
	registry.Register(authz.OperationMessagePayloadRead, authz.RequireExecutionReceipt(messagePayloadApprovalAdapter{messages: messages, grants: grants}))
}

type messagePayloadApprovalAdapter struct {
	messages *repository.MessageRepository
	grants   *SensitiveAccessGrantService
}

type messagePayloadApprovalPayload struct {
	MessageID string `json:"messageId"`
	SHA256    string `json:"sha256"`
}

func (messagePayloadApprovalAdapter) Execute(authz.ApprovalRequest, authz.Permit) error {
	return apperr.ErrForbidden
}

func (a messagePayloadApprovalAdapter) PrepareApprovalRequestInTx(tx *gorm.DB, req authz.ApprovalRequest) error {
	payload, err := parseMessagePayloadApproval(req.Payload)
	if err != nil {
		return err
	}
	target, err := authz.NewSensitiveAccessTarget("message", payload.MessageID, payload.SHA256)
	if err != nil {
		return err
	}
	_, err = a.grants.WithTx(tx).CreatePending(req.RequestID, req.RequesterType, req.RequesterID, req.Operation.Kind, target.Ref, target.ContentHash)
	return err
}

func (a messagePayloadApprovalAdapter) ExecuteInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
	if permit.Operation() != authz.OperationMessagePayloadRead {
		return nil, apperr.ErrForbidden
	}
	payload, err := parseMessagePayloadApproval(req.Payload)
	if err != nil {
		return nil, err
	}
	stored, err := a.messages.WithTx(tx).FindPayload(payload.MessageID)
	if err != nil {
		return nil, err
	}
	if stored == nil || payloadSHA256(stored.Payload) != payload.SHA256 {
		return nil, apperr.ErrForbidden
	}
	target, err := authz.NewSensitiveAccessTarget("message", payload.MessageID, payload.SHA256)
	if err != nil {
		return nil, err
	}
	principal := auth.Principal{Kind: req.RequesterType, ID: req.RequesterID}
	if err := a.grants.WithTx(tx).Activate(req.RequestID, principal, req.Operation.Kind, target.Ref, target.ContentHash, time.Now().UTC()); err != nil {
		return nil, err
	}
	receipt := &model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash, ResultRef: target.Ref}
	if err := tx.Create(receipt).Error; err != nil {
		return nil, err
	}
	return nil, nil
}

func parseMessagePayloadApproval(raw []byte) (messagePayloadApprovalPayload, error) {
	var payload messagePayloadApprovalPayload
	if json.Unmarshal(raw, &payload) != nil || payload.MessageID == "" || payload.SHA256 == "" {
		return messagePayloadApprovalPayload{}, apperr.ErrInvalidParam
	}
	return payload, nil
}
