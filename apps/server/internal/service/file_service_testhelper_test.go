package service

import "github.com/wcpe/Beacon/apps/server/internal/model"

// applyFileCreateForTest 仅供历史行为测试构造已执行的文件写入，不构成生产旁路。
func applyFileCreateForTest(s *FileService, p CreateFileParams) (*model.FileObject, error) {
	return s.applyCreate(p)
}

// applyFilePublishForTest 仅供历史行为测试构造已执行的文件发布，不构成生产旁路。
func applyFilePublishForTest(s *FileService, id uint, content, operator, comment, clientIP string) (*model.FileObject, error) {
	return s.applyPublish(id, content, operator, comment, clientIP)
}

// applyFileImportForTest 仅供历史行为测试构造已执行的文件导入，不构成生产旁路。
func applyFileImportForTest(s *FileService, p ImportFilesParams) (*ImportResult, error) {
	return s.applyImport(p)
}
