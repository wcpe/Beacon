package service

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

var reverseResolveTestSequence atomic.Uint64
var reverseResolveSchemaMu sync.Mutex

// applyCreateScanTaskForTest 仅供既有领域行为测试构造已批准的扫描任务，不构成生产旁路。
func applyCreateScanTaskForTest(s *ReverseFetchTaskService, ns, serverID, scope, group, target, operator, clientIP string) (*model.ReverseFetchTask, error) {
	var task *model.ReverseFetchTask
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		task, err = s.applyCreateScanTaskInTx(tx, ns, serverID, scope, group, target, operator, clientIP)
		return err
	})
	return task, err
}

// applySubmitForTest 仅供既有领域行为测试构造已批准的选定文件提交，不构成生产旁路。
func applySubmitForTest(s *ReverseFetchTaskService, taskID uint, selectedPaths []string, confirmOverThreshold bool, operator, clientIP string) (*model.ReverseFetchTask, error) {
	task, err := s.requireTask(taskID)
	if err != nil {
		return nil, err
	}
	manifestHash := fmt.Sprintf("%x", sha256.Sum256([]byte(task.Manifest)))
	var updated *model.ReverseFetchTask
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		updated, applyErr = s.applySubmitInTx(tx, taskID, manifestHash, selectedPaths, confirmOverThreshold, operator, clientIP)
		if applyErr != nil {
			return applyErr
		}
		if s.grants == nil {
			return apperr.ErrForbidden
		}
		_, applyErr = s.grants.WithTx(tx).CreatePending(fmt.Sprintf("test-reverse-submit-%d", reverseResolveTestSequence.Add(1)),
			auth.PrincipalKindHuman, operator, authz.OperationAgentCommandReverseSubmit,
			fmt.Sprintf("agent-command/%d", updated.SubmitCommandID), manifestHash)
		return applyErr
	})
	return updated, err
}

// applyResolveForTest 通过审批 worker 验证冲突落库，不提供测试旁路。
func applyResolveForTest(s *ReverseFetchTaskService, taskID uint, decisions []ResolveDecision, operator, clientIP string) (*ImportResult, error) {
	reverseResolveSchemaMu.Lock()
	if err := s.db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}, &model.SensitiveAccessGrant{}); err != nil {
		reverseResolveSchemaMu.Unlock()
		return nil, err
	}
	reverseResolveSchemaMu.Unlock()
	task, env, err := s.requireConflictReview(taskID)
	if err != nil {
		return nil, err
	}
	if err := prepareConsumedSubmitGrantForTest(s, task, operator); err != nil {
		return nil, err
	}
	overwrite, err := s.validateDecisions(env, decisions)
	if err != nil {
		return nil, err
	}
	result := &ImportResult{}
	conflicts := make(map[string]struct{}, len(env.Conflicts))
	for _, path := range env.Conflicts {
		conflicts[path] = struct{}{}
	}
	paths := make([]string, 0, len(env.Files))
	for path := range env.Files {
		if _, conflict := conflicts[path]; !conflict {
			paths = append(paths, path)
		}
	}
	for path := range overwrite {
		paths = append(paths, path)
	}
	group, scopeTarget, err := normalizeScope(task.Scope, task.GroupCode, task.ScopeTarget)
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		existing, err := s.fileSvc.fileRepo.FindByIdentity(task.NamespaceCode, group, path, task.Scope, scopeTarget)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			result.Created++
		} else {
			result.Updated++
		}
	}
	registry := authz.NewApprovalRegistry()
	approval := NewApprovalService(s.db, repository.NewApprovalRequestRepository(s.db), repository.NewAuditLogRepository(s.db), registry)
	s.SetApprovalService(approval)
	RegisterReverseFetchTaskApprovalAdapters(registry, s)
	ticket, err := s.RequestResolveApproval(taskID, decisions, "测试冲突审核", fmt.Sprintf("test-reverse-resolve-%d-%d", taskID, reverseResolveTestSequence.Add(1)), operator, clientIP, auth.HumanPrincipal(operator))
	if err != nil {
		return nil, err
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("reviewer"), clientIP); err != nil {
		return nil, err
	}
	if _, err := NewApprovalWorker(approval).RunOnce(); err != nil {
		return nil, err
	}
	var request model.ApprovalRequest
	if err := s.db.Where("request_id = ?", ticket.ApprovalRequestID).First(&request).Error; err != nil {
		return nil, err
	}
	if request.Status != model.ApprovalStatusSucceeded {
		return nil, apperr.ErrReverseFetchTaskState
	}
	return result, err
}

// prepareConsumedSubmitGrantForTest 为既有冲突落库测试补齐“已由申请人读取正文”的前置事实。
func prepareConsumedSubmitGrantForTest(s *ReverseFetchTaskService, task *model.ReverseFetchTask, operator string) error {
	if s.grants == nil {
		s.SetSensitiveAccessGrants(NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(s.db)))
	}
	targetRef := fmt.Sprintf("agent-command/%d", task.SubmitCommandID)
	grant, err := s.grants.repo.FindByTargetRef(targetRef)
	if err != nil {
		return err
	}
	if grant == nil {
		grant, err = s.grants.CreatePending(fmt.Sprintf("test-submit-body-%d", task.ID), auth.PrincipalKindHuman, operator,
			authz.OperationAgentCommandReverseSubmit, targetRef, "test-pending")
		if err != nil {
			return err
		}
	}
	if grant.Status == model.SensitiveAccessGrantStatusPending {
		if err := s.grants.BindAndActivatePendingCommandFromAgent(task.SubmitCommandID, authz.OperationAgentCommandReverseSubmit,
			reverseFetchSHA256(task.SubmitContent), time.Now().UTC()); err != nil {
			return err
		}
		grant, err = s.grants.repo.FindByID(grant.GrantID)
		if err != nil {
			return err
		}
	}
	if grant.Status == model.SensitiveAccessGrantStatusConsumed {
		return nil
	}
	return s.grants.Consume(grant.GrantID, auth.HumanPrincipal(operator), authz.OperationAgentCommandReverseSubmit,
		targetRef, reverseFetchSHA256(task.SubmitContent), time.Now().UTC())
}
