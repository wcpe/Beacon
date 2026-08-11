package service

import (
	"time"

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
	return ApplyConfigPublishWithExpectedVersionForTest(s, id, content, operator, comment, clientIP, item.Version)
}

// ApplyConfigPublishWithExpectedVersionForTest 仅供并发历史测试固定审批冻结时的目标版本，不构成生产旁路。
func ApplyConfigPublishWithExpectedVersionForTest(s *ConfigService, id uint, content, operator, comment, clientIP string, expectedVersion int64) (*model.ConfigItem, error) {
	var updated *model.ConfigItem
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		updated, applyErr = s.applyPublishInTx(tx, id, content, operator, comment, clientIP, expectedVersion)
		return applyErr
	})
	if err != nil {
		return nil, mapDuplicateKey(err)
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

// ApplyConfigDeleteForTest 仅供历史行为测试构造审批已执行后的软删，不构成生产旁路。
func ApplyConfigDeleteForTest(s *ConfigService, id uint, operator, clientIP string) error {
	item, err := s.Get(id)
	if err != nil {
		return err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.configRepo.WithTx(tx).SoftDelete(item.ID, time.Now().UTC()); err != nil {
			return err
		}
		return s.writeAudit(tx, item, operator, model.ActionConfigDelete, `{"deleted":true}`, clientIP)
	})
	if err != nil {
		return err
	}
	s.notify(item)
	s.exportGit(item, model.ActionConfigDelete, operator)
	return nil
}

// ApplyConfigBatchSetEnabledForTest 仅供历史行为测试构造审批已执行后的批量启停，不构成生产旁路。
func ApplyConfigBatchSetEnabledForTest(s *ConfigService, ids []uint, enabled bool, operator, clientIP string) error {
	uniqueIDs := dedupIDs(ids)
	items, err := s.configRepo.FindByIDs(uniqueIDs)
	if err != nil {
		return err
	}
	if len(items) != len(uniqueIDs) {
		return apperr.ErrConfigNotFound
	}
	action, detail := model.ActionConfigDisable, `{"enabled":false}`
	if enabled {
		action, detail = model.ActionConfigEnable, `{"enabled":true}`
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		for index := range items {
			item := &items[index]
			if err := s.configRepo.WithTx(tx).SetEnabled(item.ID, enabled); err != nil {
				return err
			}
			if err := s.writeAudit(tx, item, operator, action, detail, clientIP); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for index := range items {
		s.notify(&items[index])
		s.exportGit(&items[index], action, operator)
	}
	return nil
}
