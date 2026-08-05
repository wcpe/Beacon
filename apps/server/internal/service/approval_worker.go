package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

const (
	approvalWorkerMaxAttempts = 3
	approvalWorkerLease       = 30 * time.Second
	approvalWorkerPoll        = 5 * time.Second
)

// ApprovalReceiptStore 是 worker 查询执行回执所需的最小接口。
type ApprovalReceiptStore interface {
	FindByRequestID(requestID string) (*model.ApprovalExecutionReceipt, error)
}

// SetApprovalReceiptStore 注入执行回执查询端口，供测试或后续事务接线使用。
func (s *ApprovalService) SetApprovalReceiptStore(store ApprovalReceiptStore) {
	if store != nil {
		s.receipts = store
	}
}

// ApprovalWorker 是单进程、DB 驱动的审批执行器。
type ApprovalWorker struct {
	svc           *ApprovalService
	workerID      string
	leaseDuration time.Duration
	now           func() time.Time
}

// NewApprovalWorker 构造审批 worker。
func NewApprovalWorker(svc *ApprovalService) *ApprovalWorker {
	return &ApprovalWorker{
		svc: svc, workerID: newApprovalWorkerID(),
		leaseDuration: approvalWorkerLease, now: time.Now,
	}
}

// RunOnce 扫描并执行当前可认领的审批请求，适合测试与显式调度。
// 可选上下文用于在批量扫描间取消执行，省略时使用后台上下文。
func (w *ApprovalWorker) RunOnce(ctxs ...context.Context) (int, error) {
	ctx := context.Background()
	if len(ctxs) > 0 && ctxs[0] != nil {
		ctx = ctxs[0]
	}
	processed := 0
	for {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		req, found, err := w.claimNext()
		if err != nil {
			return processed, err
		}
		if !found {
			return processed, nil
		}
		if err := w.execute(req); err != nil {
			return processed, err
		}
		processed++
	}
}

// Run 持续响应批准唤醒并周期扫描过期 lease。
func (w *ApprovalWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(approvalWorkerPoll)
	defer ticker.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.svc.WorkerWake():
		case <-ticker.C:
		}
	}
}

func (w *ApprovalWorker) claimNext() (*model.ApprovalRequest, bool, error) {
	if w.svc == nil || w.svc.db == nil {
		return nil, false, apperr.ErrInternal
	}
	var claimed *model.ApprovalRequest
	err := w.svc.db.Transaction(func(tx *gorm.DB) error {
		now := w.now().UTC()
		var candidate model.ApprovalRequest
		err := tx.Where("status = ? AND (lease_until IS NULL OR lease_until <= ?)", model.ApprovalStatusExecuting, now).
			Order("created_at asc, id asc").First(&candidate).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		oldVersion := candidate.Version
		leaseUntil := now.Add(w.leaseDuration)
		leaseToken := newApprovalLeaseToken(w.workerID)
		result := tx.Model(&model.ApprovalRequest{}).
			Where("id = ? AND status = ? AND version = ? AND (lease_until IS NULL OR lease_until <= ?)", candidate.ID, model.ApprovalStatusExecuting, oldVersion, now).
			Updates(map[string]any{
				"attempt": gorm.Expr("attempt + 1"), "lease_owner": leaseToken,
				"lease_until": leaseUntil, "version": oldVersion + 1,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		candidate.Attempt++
		candidate.LeaseOwner = leaseToken
		candidate.LeaseUntil = &leaseUntil
		candidate.Version = oldVersion + 1
		claimed = &candidate
		return nil
	})
	return claimed, claimed != nil, err
}

func (w *ApprovalWorker) execute(req *model.ApprovalRequest) error {
	receipt, err := w.svc.receipts.FindByRequestID(req.RequestID)
	if err != nil {
		return err
	}
	if receipt != nil {
		if !sameReceipt(req, receipt) {
			return w.finishFailure(req, "执行回执绑定不一致")
		}
		return w.finishSuccess(req, receipt)
	}
	if w.svc.registry == nil {
		return w.finishFailure(req, "审批执行注册表未装配")
	}
	var afterCommit func()
	execErr := w.svc.db.Transaction(func(tx *gorm.DB) error {
		// 先校验认领快照，再仅按 requestID 与 lease 由注册表重读权威事实并签发许可。
		if _, err := w.loadExecutionRequest(tx, req); err != nil {
			return err
		}
		var err error
		afterCommit, err = w.svc.registry.ExecuteInTx(tx, req.RequestID, req.LeaseOwner)
		return err
	})
	if execErr == nil && afterCommit != nil {
		afterCommit()
	}
	receipt, err = w.svc.receipts.FindByRequestID(req.RequestID)
	if err != nil {
		return err
	}
	if receipt != nil {
		if !sameReceipt(req, receipt) {
			return w.finishFailure(req, "执行回执绑定不一致")
		}
		return w.finishSuccess(req, receipt)
	}
	if execErr != nil {
		return w.handleExecutionError(req, execErr)
	}
	if w.svc.registry.RequiresExecutionReceipt(req.OperationKind) {
		return w.finishFailure(req, "审批适配器未提交执行回执")
	}
	return w.finishSuccess(req, nil)
}

func (w *ApprovalWorker) loadExecutionRequest(tx *gorm.DB, claimed *model.ApprovalRequest) (*model.ApprovalRequest, error) {
	var current model.ApprovalRequest
	if err := tx.Where("request_id = ?", claimed.RequestID).First(&current).Error; err != nil {
		return nil, err
	}
	if current.Status != model.ApprovalStatusExecuting || current.LeaseOwner != claimed.LeaseOwner ||
		current.Version != claimed.Version || current.FrozenPayloadSHA256 != claimed.FrozenPayloadSHA256 {
		return nil, apperr.ErrForbidden
	}
	return &current, nil
}

func sameReceipt(req *model.ApprovalRequest, receipt *model.ApprovalExecutionReceipt) bool {
	return receipt.RequestID == req.RequestID && receipt.OperationKey == req.OperationKey && receipt.PayloadHash == req.FrozenPayloadSHA256
}

func (w *ApprovalWorker) handleExecutionError(req *model.ApprovalRequest, err error) error {
	summary := safeFailureSummary(err)
	if authz.IsRetryable(err) && req.Attempt < approvalWorkerMaxAttempts {
		return w.releaseForRetry(req, summary)
	}
	return w.finishFailure(req, summary)
}

func (w *ApprovalWorker) releaseForRetry(req *model.ApprovalRequest, summary string) error {
	result := w.svc.db.Model(&model.ApprovalRequest{}).
		Where("id = ? AND status = ? AND lease_owner = ? AND version = ?", req.ID, model.ApprovalStatusExecuting, req.LeaseOwner, req.Version).
		Updates(map[string]any{
			"lease_owner": nil, "lease_until": nil, "failure_summary": summary,
			"version": req.Version + 1,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return apperr.ErrIllegalState
	}
	req.LeaseOwner, req.LeaseUntil, req.FailureSummary, req.Version = "", nil, summary, req.Version+1
	return nil
}

func (w *ApprovalWorker) finishSuccess(req *model.ApprovalRequest, receipt *model.ApprovalExecutionReceipt) error {
	now := w.now().UTC()
	if receipt == nil {
		receipt = newApprovalReceipt(req)
	}
	err := w.svc.db.Transaction(func(tx *gorm.DB) error {
		if receipt.ID == 0 {
			if err := tx.Create(receipt).Error; err != nil {
				return err
			}
		}
		result := tx.Model(&model.ApprovalRequest{}).
			Where("id = ? AND status = ? AND lease_owner = ? AND version = ?", req.ID, model.ApprovalStatusExecuting, req.LeaseOwner, req.Version).
			Updates(map[string]any{
				"status": model.ApprovalStatusSucceeded, "result_ref": receipt.ResultRef,
				"failure_reason": "", "failure_summary": "", "lease_owner": nil,
				"lease_until": nil, "executed_at": now, "finished_at": now,
				"version": req.Version + 1,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return apperr.ErrIllegalState
		}
		return nil
	})
	if err != nil {
		return err
	}
	req.Status, req.ResultRef, req.FailureReason, req.FailureSummary = model.ApprovalStatusSucceeded, receipt.ResultRef, "", ""
	req.LeaseOwner, req.LeaseUntil, req.ExecutedAt, req.FinishedAt = "", nil, &now, &now
	req.Version++
	return nil
}

func (w *ApprovalWorker) finishFailure(req *model.ApprovalRequest, summary string) error {
	now := w.now().UTC()
	result := w.svc.db.Model(&model.ApprovalRequest{}).
		Where("id = ? AND status = ? AND lease_owner = ? AND version = ?", req.ID, model.ApprovalStatusExecuting, req.LeaseOwner, req.Version).
		Updates(map[string]any{
			"status": model.ApprovalStatusFailed, "failure_reason": summary,
			"failure_summary": summary, "lease_owner": nil, "lease_until": nil,
			"finished_at": now, "version": req.Version + 1,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return apperr.ErrIllegalState
	}
	req.Status, req.FailureReason, req.FailureSummary = model.ApprovalStatusFailed, summary, summary
	req.LeaseOwner, req.LeaseUntil, req.FinishedAt, req.Version = "", nil, &now, req.Version+1
	return nil
}

func newApprovalReceipt(req *model.ApprovalRequest) *model.ApprovalExecutionReceipt {
	return &model.ApprovalExecutionReceipt{
		RequestID: req.RequestID, OperationKey: req.OperationKey,
		PayloadHash: req.FrozenPayloadSHA256, ResultRef: "apr-result-" + req.RequestID,
	}
}

func safeFailureSummary(err error) string {
	if err == nil {
		return "执行失败"
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "执行失败"
	}
	lower := strings.ToLower(message)
	for _, term := range []string{"token", "secret", "password", "authorization", "bearer", "body", "content"} {
		if strings.Contains(lower, term) {
			return "执行失败（错误摘要含敏感字段）"
		}
	}
	if len(message) > 512 {
		return message[:512]
	}
	return message
}

func newApprovalWorkerID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "approval-worker-" + hex.EncodeToString(raw[:])
	}
	return "approval-worker"
}

func newApprovalLeaseToken(workerID string) string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return workerID + "-lease-" + hex.EncodeToString(raw[:])
	}
	return workerID + "-lease"
}
