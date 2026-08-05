package service

import (
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

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
