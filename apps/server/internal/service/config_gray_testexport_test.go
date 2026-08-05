package service

import (
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// ApplyGrayPublishForTest 仅供历史灰度行为测试准备数据，生产路径必须经审批执行器。
func ApplyGrayPublishForTest(s *ConfigGrayService, itemID uint, content string, cohort []string, operator, comment, clientIP string) (*model.ConfigGray, error) {
	encoded, err := encodeCohort(cohort)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 16; attempt++ {
		item, err := s.configSvc.Get(itemID)
		if err != nil {
			return nil, err
		}
		payload := configApprovalPayload{ConfigItemID: itemID, ExpectedVersion: item.Version, ExpectedGrayVersion: item.GrayVersion, ContentSHA256: configContentHash(content)}
		pending := configPendingPayload{Content: content, Cohort: encoded, Operator: operator, Comment: comment, ClientIP: clientIP}
		var gray *model.ConfigGray
		err = s.db.Transaction(func(tx *gorm.DB) error {
			var applyErr error
			gray, _, applyErr = s.applyPublishInTx(tx, payload, pending)
			return applyErr
		})
		if err == nil {
			s.notifyServers(item.NamespaceCode, encoded)
			return gray, nil
		}
		if !errors.Is(err, apperr.ErrApprovalTargetChanged) {
			return nil, err
		}
	}
	return nil, apperr.ErrApprovalTargetChanged
}

// ApplyGrayPromoteForTest 仅供历史灰度行为测试准备数据，生产路径必须经审批执行器。
func ApplyGrayPromoteForTest(s *ConfigGrayService, itemID uint, operator, comment, clientIP string) (*model.ConfigItem, error) {
	item, err := s.configSvc.Get(itemID)
	if err != nil {
		return nil, err
	}
	gray, err := s.grayRepo.FindActiveByItem(itemID)
	if err != nil || gray == nil {
		if err != nil {
			return nil, err
		}
		return nil, apperr.ErrGrayNotFound
	}
	payload := configApprovalPayload{ConfigItemID: itemID, ExpectedVersion: item.Version, ExpectedGrayVersion: item.GrayVersion, ContentSHA256: configContentHash(gray.Content)}
	pending := configPendingPayload{Content: gray.Content, Cohort: gray.Cohort, Operator: operator, Comment: comment, ClientIP: clientIP}
	var promoted *model.ConfigItem
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		promoted, gray, applyErr = s.applyPromoteInTx(tx, payload, pending)
		return applyErr
	})
	if err == nil {
		if s.metrics != nil {
			s.metrics.IncConfigPublish()
		}
		s.notifyPromote(promoted, gray.Cohort)
	}
	return promoted, err
}
