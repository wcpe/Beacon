package service

import (
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// ApplyConfigPublishForTest 仅供历史时间线测试构造已执行的配置版本，不构成生产旁路。
func ApplyConfigPublishForTest(s *ConfigService, id uint, content, operator, comment, clientIP string) (*model.ConfigItem, error) {
	item, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	var updated *model.ConfigItem
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		updated, applyErr = s.applyPublishInTx(tx, id, content, operator, comment, clientIP, item.Version)
		return applyErr
	})
	if err != nil {
		return nil, err
	}
	s.recordPublish()
	s.notify(updated)
	s.exportGit(updated, model.ActionConfigPublish, operator)
	return updated, nil
}

// ApplyConfigRollbackForTest 仅供历史时间线测试构造已执行的配置回滚，不构成生产旁路。
func ApplyConfigRollbackForTest(s *ConfigService, id uint, toVersion int64, operator, comment, clientIP string) (*model.ConfigItem, error) {
	item, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	target, err := s.revRepo.FindByItemAndVersion(id, toVersion)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, apperr.ErrRevisionNotFound
	}
	pending := configPendingPayload{RollbackVersion: toVersion, Content: target.Content, Operator: operator, Comment: comment, ClientIP: clientIP}
	var updated *model.ConfigItem
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		updated, applyErr = s.applyRollbackInTx(tx, id, pending, item.Version)
		return applyErr
	})
	if err != nil {
		return nil, err
	}
	s.recordPublish()
	s.notify(updated)
	s.exportGit(updated, model.ActionConfigRollback, operator)
	return updated, nil
}
