package service

import (
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// applyFileCreateForTest 仅供历史行为测试构造已执行的文件写入，不构成生产旁路。
func applyFileCreateForTest(s *FileService, p CreateFileParams) (*model.FileObject, error) {
	return s.applyCreate(p)
}

// ApplyFileCreateForTest 仅供外部测试包构造审批已执行后的文件创建，不构成生产旁路。
func ApplyFileCreateForTest(s *FileService, p CreateFileParams) (*model.FileObject, error) {
	return applyFileCreateForTest(s, p)
}

// applyFilePublishForTest 仅供历史行为测试构造已执行的文件发布，不构成生产旁路。
func applyFilePublishForTest(s *FileService, id uint, content, operator, comment, clientIP string) (*model.FileObject, error) {
	return s.applyPublish(id, content, operator, comment, clientIP)
}

// ApplyFilePublishForTest 仅供外部测试包构造审批已执行后的文件发布，不构成生产旁路。
func ApplyFilePublishForTest(s *FileService, id uint, content, operator, comment, clientIP string) (*model.FileObject, error) {
	return applyFilePublishForTest(s, id, content, operator, comment, clientIP)
}

// applyFileImportForTest 仅供历史行为测试构造已执行的文件导入，不构成生产旁路。
func applyFileImportForTest(s *FileService, p ImportFilesParams) (*ImportResult, error) {
	return s.applyImport(p)
}

// ApplyFileImportForTest 仅供外部测试包构造审批已执行后的文件导入，不构成生产旁路。
func ApplyFileImportForTest(s *FileService, p ImportFilesParams) (*ImportResult, error) {
	return applyFileImportForTest(s, p)
}

// ApplyFileRollbackForTest 仅供历史行为测试构造审批已执行后的文件回滚，不构成生产旁路。
func ApplyFileRollbackForTest(s *FileService, id uint, toVersion int64, operator, comment, clientIP string) (*model.FileObject, error) {
	obj, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	var updated *model.FileObject
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		updated, applyErr = s.RollbackInTx(tx, obj, toVersion, operator, comment)
		if applyErr != nil {
			return applyErr
		}
		return s.writeAudit(tx, updated, operator, model.ActionFileRollback,
			fmt.Sprintf(`{"version":%d,"fromVersion":%d,"md5":"%s"}`, updated.Version, toVersion, updated.ContentMD5), clientIP)
	})
	if err != nil {
		return nil, err
	}
	s.notify(updated)
	s.exportGit(updated, model.ActionFileRollback, operator)
	return updated, nil
}

// ApplyFileDeleteForTest 仅供历史行为测试构造审批已执行后的软删，不构成生产旁路。
func ApplyFileDeleteForTest(s *FileService, id uint, operator, clientIP string) error {
	obj, err := s.Get(id)
	if err != nil {
		return err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.fileRepo.WithTx(tx).SoftDelete(obj.ID, time.Now().UTC()); err != nil {
			return err
		}
		return s.writeAudit(tx, obj, operator, model.ActionFileDelete, `{"deleted":true}`, clientIP)
	})
	if err != nil {
		return err
	}
	s.notify(obj)
	s.exportGit(obj, model.ActionFileDelete, operator)
	return nil
}

// ApplyFileBatchSetEnabledForTest 仅供历史行为测试构造审批已执行后的批量启停，不构成生产旁路。
func ApplyFileBatchSetEnabledForTest(s *FileService, ids []uint, enabled bool, operator, clientIP string) error {
	uniqueIDs := dedupIDs(ids)
	items := make([]model.FileObject, 0, len(uniqueIDs))
	for _, id := range uniqueIDs {
		obj, err := s.Get(id)
		if err != nil {
			return err
		}
		items = append(items, *obj)
	}
	action, detail := model.ActionFileDisable, `{"enabled":false}`
	if enabled {
		action, detail = model.ActionFileEnable, `{"enabled":true}`
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for index := range items {
			item := &items[index]
			if err := s.fileRepo.WithTx(tx).SetEnabled(item.ID, enabled); err != nil {
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
