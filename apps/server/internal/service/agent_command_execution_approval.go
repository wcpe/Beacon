package service

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// agentCommandExecutionApprovalAdapter 只允许审批 worker 在同一事务内下发命令并写执行回执。
type agentCommandExecutionApprovalAdapter struct {
	commands *AgentCommandService
	grants   *SensitiveAccessGrantService
}

func (agentCommandExecutionApprovalAdapter) Execute(authz.ApprovalRequest, authz.Permit) error {
	return apperr.ErrForbidden
}

func (a agentCommandExecutionApprovalAdapter) ExecuteInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
	var payload struct {
		Namespace string `json:"namespace"`
		ServerID  string `json:"serverId"`
		Scope     string `json:"scope"`
		Group     string `json:"group"`
		Target    string `json:"target"`
		Path      string `json:"path"`
		Operator  string `json:"operator"`
		ClientIP  string `json:"clientIP"`
	}
	if json.Unmarshal(req.Payload, &payload) != nil || payload.Namespace == "" || payload.ServerID == "" || payload.Operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	var (
		cmd *model.AgentCommand
		err error
	)
	switch permit.Operation() {
	case authz.OperationAgentCommandReverseScan:
		cmd, err = a.commands.applyRequestReverseFetchInTx(tx, payload.Namespace, payload.ServerID, payload.Scope, payload.Group, payload.Target, payload.Operator, payload.ClientIP)
	case authz.OperationAgentCommandImprint:
		cmd, err = a.commands.applyRequestImprintInTx(tx, payload.Namespace, payload.ServerID, payload.Path, payload.Operator, payload.ClientIP)
	default:
		return nil, apperr.ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	if permit.Operation() == authz.OperationAgentCommandImprint {
		if _, err := a.grants.WithTx(tx).CreatePending(req.RequestID, req.RequesterType, req.RequesterID, req.Operation.Kind, fmt.Sprintf("agent-command/%d", cmd.ID), req.PayloadHash); err != nil {
			return nil, err
		}
	}
	if err := tx.Create(&model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash, ResultRef: fmt.Sprintf("agent-command-%d", cmd.ID)}).Error; err != nil {
		return nil, err
	}
	return func() {
		if a.commands.notifier != nil {
			a.commands.notifier.NotifyCommand(payload.Namespace, payload.ServerID)
		}
	}, nil
}
