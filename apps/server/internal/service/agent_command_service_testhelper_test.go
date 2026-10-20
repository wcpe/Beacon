package service

import (
	"fmt"
	"sync/atomic"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

var imprintConfirmTestSequence atomic.Uint64

// testAgentIdentityOf 构造「回传身份 = 指定归属的那台 agent」，供既有领域行为测试走正常回传路径。
// 归属校验上线后，每个回传入口都必须带身份；真实归属不符的负向用例另行显式构造。
func testAgentIdentityOf(namespace, serverID string) agentauth.Identity {
	return agentauth.Identity{
		Namespace: namespace, ServerID: serverID,
		IdentityID: "test-identity-" + namespace + "/" + serverID,
	}
}

// testAgentIdentity 构造「回传身份 = 该命令归属的那台 agent 本人」。
func testAgentIdentity(cmd *model.AgentCommand) agentauth.Identity {
	if cmd == nil {
		return agentauth.Identity{}
	}
	return testAgentIdentityOf(cmd.NamespaceCode, cmd.ServerID)
}

// applyRequestReverseFetchForTest 仅供既有领域行为测试构造已批准的反向抓取命令，不构成生产旁路。
func applyRequestReverseFetchForTest(s *AgentCommandService, ns, serverID, scope, group, target, operator, clientIP string) (*model.AgentCommand, error) {
	var command *model.AgentCommand
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		command, err = s.applyRequestReverseFetchInTx(tx, ns, serverID, scope, group, target, operator, clientIP)
		return err
	})
	return command, err
}

// applyRequestImprintForTest 仅供既有领域行为测试构造已批准的拓印命令，不构成生产旁路。
func applyRequestImprintForTest(s *AgentCommandService, ns, serverID, path, operator, clientIP string) (*model.AgentCommand, error) {
	var command *model.AgentCommand
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		command, err = s.applyRequestImprintInTx(tx, ns, serverID, path, operator, clientIP)
		return err
	})
	return command, err
}

// applyConfirmImprintForTest 通过审批 worker 验证拓印确认，不提供测试旁路。
func applyConfirmImprintForTest(s *AgentCommandService, commandID uint, scope, group, zone, target, reviewedMD5, operator, clientIP string) (*ImprintConfirmResult, error) {
	command, payload, err := s.requireReadyImprint(commandID)
	if err != nil {
		return nil, err
	}
	registry := authz.NewApprovalRegistry()
	approval := NewApprovalService(s.db, repository.NewApprovalRequestRepository(s.db), repository.NewAuditLogRepository(s.db), registry)
	s.SetApprovalService(approval)
	RegisterAgentCommandApprovalAdapters(registry, s, NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(s.db)))
	ticket, err := s.RequestImprintConfirmApproval(commandID, scope, group, zone, target, reviewedMD5, "测试确认拓印",
		fmt.Sprintf("test-imprint-confirm-%d-%d", commandID, imprintConfirmTestSequence.Add(1)), operator, clientIP, auth.HumanPrincipal(operator))
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
		return nil, apperr.ErrIllegalState
	}
	normGroup, normTarget, err := normalizeImprintScope(scope, group, zone, target, command.ServerID)
	if err != nil {
		return nil, err
	}
	obj, err := s.fileSvc.fileRepo.FindByIdentity(command.NamespaceCode, groupForScope(scope, normGroup), payload.Path, scope, normTarget)
	if err != nil || obj == nil {
		return nil, apperr.ErrIllegalState
	}
	return &ImprintConfirmResult{FileID: obj.ID, ScopeLevel: scope, Group: normGroup, Target: normTarget, Version: obj.Version, MD5: obj.ContentMD5}, nil
}
