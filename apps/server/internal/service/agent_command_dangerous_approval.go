package service

import (
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// RegisterAgentCommandApprovalAdapters 注册命令危险操作审批适配器。
func RegisterAgentCommandApprovalAdapters(registry *authz.ApprovalRegistry, commands *AgentCommandService, grants *SensitiveAccessGrantService) {
	if registry == nil || commands == nil || grants == nil {
		return
	}
	registry.Register(authz.OperationAgentCommandResync, authz.RequireExecutionReceipt(agentCommandApprovalAdapter{commands: commands, grants: grants}))
	registry.Register(authz.OperationAgentCommandReverseScan, authz.RequireExecutionReceipt(agentCommandExecutionApprovalAdapter{commands: commands, grants: grants}))
	registry.Register(authz.OperationAgentCommandImprint, authz.RequireExecutionReceipt(agentCommandExecutionApprovalAdapter{commands: commands, grants: grants}))
}

// RegisterAgentLogApprovalAdapter 注册实时日志命令审批适配器。
func RegisterAgentLogApprovalAdapter(registry *authz.ApprovalRegistry, logs *AgentLogService, grants *SensitiveAccessGrantService) {
	if registry == nil || logs == nil || grants == nil {
		return
	}
	registry.Register(authz.OperationAgentCommandTailLogs, authz.RequireExecutionReceipt(agentLogApprovalAdapter{logs: logs, grants: grants}))
}

type agentLogApprovalAdapter struct {
	logs   *AgentLogService
	grants *SensitiveAccessGrantService
}

func (agentLogApprovalAdapter) Execute(authz.ApprovalRequest, authz.Permit) error { return apperr.ErrForbidden }

func (a agentLogApprovalAdapter) ExecuteInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
	if permit.Operation() != authz.OperationAgentCommandTailLogs {
		return nil, apperr.ErrForbidden
	}
	var payload struct {
		Namespace string `json:"namespace"`
		ServerID  string `json:"serverId"`
		Operator  string `json:"operator"`
		ClientIP  string `json:"clientIP"`
	}
	if json.Unmarshal(req.Payload, &payload) != nil {
		return nil, apperr.ErrInvalidParam
	}
	cmd, err := a.logs.applyRequestTailLogsInTx(tx, payload.Namespace, payload.ServerID, payload.Operator, payload.ClientIP)
	if err != nil {
		return nil, err
	}
	if _, err := a.grants.WithTx(tx).CreatePending(req.RequestID, req.RequesterType, req.RequesterID, req.Operation.Kind, fmt.Sprintf("agent-command/%d", cmd.ID), req.PayloadHash); err != nil {
		return nil, err
	}
	if err := tx.Create(&model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash, ResultRef: "agent-command-" + fmt.Sprint(cmd.ID)}).Error; err != nil {
		return nil, err
	}
	return func() {
		if a.logs.notifier != nil {
			a.logs.notifier.NotifyCommand(payload.Namespace, payload.ServerID)
		}
	}, nil
}

type agentCommandApprovalAdapter struct {
	commands *AgentCommandService
	grants   *SensitiveAccessGrantService
}

func (agentCommandApprovalAdapter) Execute(authz.ApprovalRequest, authz.Permit) error {
	return apperr.ErrForbidden
}

func (a agentCommandApprovalAdapter) PrepareApprovalRequestInTx(tx *gorm.DB, req authz.ApprovalRequest) error {
	var payload struct {
		Namespace string `json:"namespace"`
		ServerID  string `json:"serverId"`
	}
	if json.Unmarshal(req.Payload, &payload) != nil || payload.Namespace == "" || payload.ServerID == "" {
		return apperr.ErrInvalidParam
	}
	target, err := authz.NewSensitiveAccessTarget("agent-command", payload.Namespace+"/"+payload.ServerID+"/"+req.RequestID, req.PayloadHash)
	if err != nil {
		return err
	}
	_, err = a.grants.WithTx(tx).CreatePending(req.RequestID, req.RequesterType, req.RequesterID, req.Operation.Kind, target.Ref, target.ContentHash)
	return err
}

func (a agentCommandApprovalAdapter) ExecuteInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
	if permit.Operation() != authz.OperationAgentCommandResync {
		return nil, apperr.ErrForbidden
	}
	var payload struct {
		Namespace string `json:"namespace"`
		ServerID  string `json:"serverId"`
		Operator  string `json:"operator"`
		ClientIP  string `json:"clientIP"`
	}
	if json.Unmarshal(req.Payload, &payload) != nil {
		return nil, apperr.ErrInvalidParam
	}
	cmd, err := a.commands.applyRequestResyncInTx(tx, payload.Namespace, payload.ServerID, payload.Operator, payload.ClientIP)
	if err != nil {
		return nil, err
	}
	target, err := authz.NewSensitiveAccessTarget("agent-command", payload.Namespace+"/"+payload.ServerID+"/"+req.RequestID, req.PayloadHash)
	if err != nil {
		return nil, err
	}
	if err := a.grants.WithTx(tx).Activate(req.RequestID, auth.Principal{Kind: req.RequesterType, ID: req.RequesterID}, req.Operation.Kind, target.Ref, target.ContentHash, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := tx.Create(&model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash, ResultRef: "agent-command-" + fmt.Sprint(cmd.ID)}).Error; err != nil {
		return nil, err
	}
	return func() {
		if a.commands.notifier != nil {
			a.commands.notifier.NotifyCommand(payload.Namespace, payload.ServerID)
		}
	}, nil
}
