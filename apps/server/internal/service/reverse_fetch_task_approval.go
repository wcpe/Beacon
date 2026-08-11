package service

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// RegisterReverseFetchTaskApprovalAdapters 注册受管反向抓取审批适配器。
func RegisterReverseFetchTaskApprovalAdapters(registry *authz.ApprovalRegistry, tasks *ReverseFetchTaskService) {
	if registry == nil || tasks == nil {
		return
	}
	adapter := reverseFetchTaskApprovalAdapter{tasks: tasks}
	registry.Register(authz.OperationAgentCommandReverseScan, authz.RequireExecutionReceipt(adapter))
	registry.Register(authz.OperationAgentCommandReverseSubmit, authz.RequireExecutionReceipt(adapter))
	registry.Register(authz.OperationAgentCommandReverseResolve, authz.RequireExecutionReceipt(adapter))
}

type reverseFetchTaskApprovalAdapter struct{ tasks *ReverseFetchTaskService }

type reverseFetchResolveApprovalPayload struct {
	TaskID         uint                                  `json:"taskId"`
	ManifestSHA256 string                                `json:"manifestSha256"`
	OutputSHA256   string                                `json:"outputSha256"`
	Conflicts      []reverseFetchResolveConflictSnapshot `json:"conflicts"`
	Decisions      []ResolveDecision                     `json:"decisions"`
	Operator       string                                `json:"operator"`
	ClientIP       string                                `json:"clientIP"`
}

func (reverseFetchTaskApprovalAdapter) Execute(authz.ApprovalRequest, authz.Permit) error {
	return apperr.ErrForbidden
}

func (a reverseFetchTaskApprovalAdapter) ExecuteInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
	var payload struct {
		Namespace            string   `json:"namespace"`
		ServerID             string   `json:"serverId"`
		Scope                string   `json:"scope"`
		Group                string   `json:"group"`
		Target               string   `json:"target"`
		TaskID               uint     `json:"taskId"`
		ManifestHash         string   `json:"manifestHash"`
		SelectedPaths        []string `json:"selectedPaths"`
		ConfirmOverThreshold bool     `json:"confirmOverThreshold"`
		Operator             string   `json:"operator"`
		ClientIP             string   `json:"clientIP"`
	}
	if json.Unmarshal(req.Payload, &payload) != nil {
		return nil, apperr.ErrInvalidParam
	}
	var (
		task          *model.ReverseFetchTask
		resolveResult *ImportResult
		resultRef     string
		err           error
	)
	switch permit.Operation() {
	case authz.OperationAgentCommandReverseScan:
		task, err = a.tasks.applyCreateScanTaskInTx(tx, payload.Namespace, payload.ServerID, payload.Scope, payload.Group, payload.Target, payload.Operator, payload.ClientIP)
	case authz.OperationAgentCommandReverseSubmit:
		task, err = a.tasks.applySubmitInTx(tx, payload.TaskID, payload.ManifestHash, payload.SelectedPaths, payload.ConfirmOverThreshold, payload.Operator, payload.ClientIP)
		if err == nil {
			if a.tasks.grants == nil {
				return nil, apperr.ErrForbidden
			}
			grant, grantErr := a.tasks.grants.WithTx(tx).CreatePending(req.RequestID, req.RequesterType, req.RequesterID,
				req.Operation.Kind, fmt.Sprintf("agent-command/%d", task.SubmitCommandID), req.PayloadHash)
			if grantErr != nil {
				return nil, grantErr
			}
			resultRef = fmt.Sprintf("agent-sensitive-operation/%d/%s", task.SubmitCommandID, grant.GrantID)
		}
	case authz.OperationAgentCommandReverseResolve:
		var resolvePayload reverseFetchResolveApprovalPayload
		if json.Unmarshal(req.Payload, &resolvePayload) != nil {
			return nil, apperr.ErrInvalidParam
		}
		task, resolveResult, err = a.tasks.applyResolveInTx(tx, req, permit, resolvePayload)
	default:
		return nil, apperr.ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	if resultRef == "" {
		resultRef = fmt.Sprintf("reverse-fetch-task-%d", task.ID)
	}
	if err := tx.Create(&model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash, ResultRef: resultRef}).Error; err != nil {
		return nil, err
	}
	return func() {
		if resolveResult != nil {
			a.tasks.afterResolveCommit(task, resolveResult, payload.Operator)
			return
		}
		if a.tasks.notifier != nil {
			a.tasks.notifier.NotifyCommand(task.NamespaceCode, task.ServerID)
		}
	}, nil
}
