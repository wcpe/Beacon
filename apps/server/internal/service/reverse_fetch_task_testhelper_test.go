package service

import (
	"crypto/sha256"
	"fmt"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

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
		return applyErr
	})
	return updated, err
}
