package service

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// RegisterReverseFetchTaskApprovalAdapters 注册受管反向抓取两阶段审批适配器。
func RegisterReverseFetchTaskApprovalAdapters(registry *authz.ApprovalRegistry, tasks *ReverseFetchTaskService) {
	if registry == nil || tasks == nil {
		return
	}
	adapter := reverseFetchTaskApprovalAdapter{tasks: tasks}
	registry.Register(authz.OperationAgentCommandReverseScan, authz.RequireExecutionReceipt(adapter))
	registry.Register(authz.OperationAgentCommandReverseSubmit, authz.RequireExecutionReceipt(adapter))
}

type reverseFetchTaskApprovalAdapter struct{ tasks *ReverseFetchTaskService }

func (reverseFetchTaskApprovalAdapter) Execute(authz.ApprovalRequest, authz.Permit) error { return apperr.ErrForbidden }

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
		task *model.ReverseFetchTask
		err  error
	)
	switch permit.Operation() {
	case authz.OperationAgentCommandReverseScan:
		task, err = a.tasks.applyCreateScanTaskInTx(tx, payload.Namespace, payload.ServerID, payload.Scope, payload.Group, payload.Target, payload.Operator, payload.ClientIP)
	case authz.OperationAgentCommandReverseSubmit:
		task, err = a.tasks.applySubmitInTx(tx, payload.TaskID, payload.ManifestHash, payload.SelectedPaths, payload.ConfirmOverThreshold, payload.Operator, payload.ClientIP)
	default:
		return nil, apperr.ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Create(&model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash, ResultRef: fmt.Sprintf("reverse-fetch-task-%d", task.ID)}).Error; err != nil {
		return nil, err
	}
	return func() {
		if a.tasks.notifier != nil {
			a.tasks.notifier.NotifyCommand(task.NamespaceCode, task.ServerID)
		}
	}, nil
}
