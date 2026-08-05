package service

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/update"
)

const (
	operationSystemUpdateApply    = authz.OperationSystemUpdateApply
	operationSystemUpdateRollback = authz.OperationSystemUpdateRollback
	operationSettingsDangerous    = authz.OperationSettingsDangerous
)

// RegisterSystemOperationApprovalAdapters 登记控制面升级、回滚与高影响设置适配器。
func RegisterSystemOperationApprovalAdapters(registry *authz.ApprovalRegistry, updates *UpdateService, settings *SettingsService, db *gorm.DB) {
	if registry == nil || updates == nil || settings == nil || db == nil {
		return
	}
	adapter := authz.RequireExecutionReceipt(systemOperationApprovalAdapter{db: db, updates: updates, settings: settings, executions: repository.NewSystemExecutionRepository(db)})
	for _, key := range []string{operationSystemUpdateApply, operationSystemUpdateRollback, operationSettingsDangerous} {
		registry.RegisterDescriptor(authz.OperationDescriptor{Key: key, SchemaVersion: 1, Capability: auth.CapabilityApprovalRequest, RiskLevel: "high"}, adapter)
	}
}

type systemOperationApprovalAdapter struct {
	db         *gorm.DB
	updates    *UpdateService
	settings   *SettingsService
	executions *repository.SystemExecutionRepository
}

type systemUpdateApprovalPayload struct {
	Frozen   update.FrozenTarget `json:"frozen"`
	Operator string              `json:"operator"`
	ClientIP string              `json:"clientIP"`
}

type systemRollbackApprovalPayload struct {
	Frozen   update.BackupSnapshot `json:"frozen"`
	Operator string                `json:"operator"`
	ClientIP string                `json:"clientIP"`
}

type dangerousSettingApprovalPayload struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Version  int    `json:"version"`
	Operator string `json:"operator"`
	ClientIP string `json:"clientIP"`
}

func (systemOperationApprovalAdapter) Execute(authz.ApprovalRequest, authz.Permit) error {
	return apperr.ErrForbidden
}

func (a systemOperationApprovalAdapter) ExecuteInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
	if tx == nil {
		return nil, apperr.ErrForbidden
	}
	if err := ensureRequestPermit(req, permit); err != nil {
		return nil, err
	}
	switch permit.Operation() {
	case operationSystemUpdateApply:
		var payload systemUpdateApprovalPayload
		if err := json.Unmarshal(req.Payload, &payload); err != nil || payload.Frozen.Version == "" {
			return nil, apperr.ErrInvalidParam
		}
		exec := model.SystemExecution{RequestID: req.RequestID, Operation: permit.Operation(), Nonce: "update-" + req.RequestID,
			Status: model.SystemExecutionStatusPending, TargetVersion: payload.Frozen.Version, AssetName: payload.Frozen.AssetName, AssetSHA256: payload.Frozen.SHA256}
		if err := a.executions.WithTx(tx).Create(&exec); err != nil {
			return nil, err
		}
		if err := tx.Create(&model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash, ResultRef: fmt.Sprintf("system-execution-%d", exec.ID)}).Error; err != nil {
			return nil, err
		}
		return func() {
			err := a.updates.applyFrozen(payload.Frozen, payload.Operator, payload.ClientIP, func(applyErr error) {
				MarkSystemExecutionFailed(a.db, req.RequestID, applyErr)
			})
			MarkSystemExecutionFailed(a.db, req.RequestID, err)
		}, nil
	case operationSettingsDangerous:
		var payload dangerousSettingApprovalPayload
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, apperr.ErrInvalidParam
		}
		after, err := a.settings.applyDangerousInTx(tx, payload.Key, payload.Value, payload.Version, payload.Operator, payload.ClientIP)
		if err != nil {
			return nil, err
		}
		if err := tx.Create(&model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash, ResultRef: "setting-" + payload.Key}).Error; err != nil {
			return nil, err
		}
		return after, nil
	case operationSystemUpdateRollback:
		var payload systemRollbackApprovalPayload
		if err := json.Unmarshal(req.Payload, &payload); err != nil || payload.Frozen.Version == "" || payload.Frozen.SHA256 == "" {
			return nil, apperr.ErrInvalidParam
		}
		exec := model.SystemExecution{RequestID: req.RequestID, Operation: permit.Operation(), Nonce: "rollback-" + req.RequestID,
			Status: model.SystemExecutionStatusPending, TargetVersion: payload.Frozen.Version, AssetSHA256: payload.Frozen.SHA256}
		if err := a.executions.WithTx(tx).Create(&exec); err != nil {
			return nil, err
		}
		if err := tx.Create(&model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash, ResultRef: fmt.Sprintf("system-execution-%d", exec.ID)}).Error; err != nil {
			return nil, err
		}
		return func() {
			MarkSystemExecutionFailed(a.db, req.RequestID, a.updates.rollbackFrozen(payload.Frozen, payload.Operator, payload.ClientIP))
		}, nil
	default:
		return nil, apperr.ErrForbidden
	}
}
