package service

import (
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"gorm.io/gorm"

	"beacon/internal/apperr"
	"beacon/internal/filetree"
	"beacon/internal/model"
	"beacon/internal/repository"
)

// MaxFileContentBytes 是单个托管文件内容大小上限（1MB）。
const MaxFileContentBytes = 1024 * 1024

// CreateFileParams 是新建文件对象（首次发布）的入参。
type CreateFileParams struct {
	Namespace   string
	Group       string
	Path        string
	ScopeLevel  string
	ScopeTarget string
	Content     string
	Operator    string
	Comment     string
	ClientIP    string
}

// FileService 编排文件树托管（通道B）：CRUD/发布/回滚/历史，事务内 object+revision+audit 原子完成。
// 文件按 path 整文件覆盖，不做格式解析/键级合并（与 ConfigService 的本质区别，见 ADR-0010）。
type FileService struct {
	db        *gorm.DB
	fileRepo  *repository.FileObjectRepository
	revRepo   *repository.FileRevisionRepository
	auditRepo *repository.AuditLogRepository
	notifier  *ChangeNotifier // 可选，事务提交后唤醒受影响的文件长轮询
}

// NewFileService 构造服务。
func NewFileService(db *gorm.DB, fileRepo *repository.FileObjectRepository, revRepo *repository.FileRevisionRepository, auditRepo *repository.AuditLogRepository) *FileService {
	return &FileService{db: db, fileRepo: fileRepo, revRepo: revRepo, auditRepo: auditRepo}
}

// SetNotifier 注入长轮询唤醒器（启动时装配；未注入则不唤醒）。
func (s *FileService) SetNotifier(n *ChangeNotifier) {
	s.notifier = n
}

// notify 在事务提交成功后唤醒该文件对象 scope 下受影响的文件长轮询（独立于配置唤醒集合）。
func (s *FileService) notify(obj *model.FileObject) {
	if s.notifier != nil {
		s.notifier.NotifyFileChange(obj.NamespaceCode, obj.ScopeLevel, obj.GroupCode, obj.ScopeTarget)
	}
}

// List 列出文件对象。
func (s *FileService) List(f repository.FileFilter) ([]model.FileObject, error) {
	return s.fileRepo.List(f)
}

// Get 取单个文件对象；不存在返回 FILE_NOT_FOUND。
func (s *FileService) Get(id uint) (*model.FileObject, error) {
	obj, err := s.fileRepo.FindByID(id)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, apperr.ErrFileNotFound
	}
	return obj, nil
}

// Create 新建文件对象并首次发布（version=1）。
func (s *FileService) Create(p CreateFileParams) (*model.FileObject, error) {
	if p.Namespace == "" || p.Operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	cleanPath, err := normalizePath(p.Path)
	if err != nil {
		return nil, err
	}
	group, scopeTarget, err := normalizeScope(p.ScopeLevel, p.Group, p.ScopeTarget)
	if err != nil {
		return nil, err
	}
	if err := validateFileContent(p.Content); err != nil {
		return nil, err
	}
	existing, err := s.fileRepo.FindByIdentity(p.Namespace, group, cleanPath, p.ScopeLevel, scopeTarget)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, apperr.ErrFileConflict
	}

	md5 := filetree.ContentMD5(p.Content)
	obj := &model.FileObject{
		NamespaceCode: p.Namespace, GroupCode: group, Path: cleanPath,
		ScopeLevel: p.ScopeLevel, ScopeTarget: scopeTarget,
		Content: p.Content, ContentMD5: md5, Version: 1, Enabled: true,
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.fileRepo.WithTx(tx).Create(obj); err != nil {
			return err
		}
		rev, err := s.appendRevision(tx, obj.ID, 1, obj.Content, md5, nil, p.Operator, p.Comment)
		if err != nil {
			return err
		}
		obj.CurrentRevision = rev.ID
		if err := s.fileRepo.WithTx(tx).Save(obj); err != nil {
			return err
		}
		return s.writeAudit(tx, obj, p.Operator, model.ActionFileCreate,
			fmt.Sprintf(`{"version":1,"md5":"%s"}`, md5), p.ClientIP)
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, apperr.ErrFileConflict
		}
		return nil, err
	}
	slog.Info("新建托管文件", "namespace", p.Namespace, "group", group, "path", cleanPath, "scope", p.ScopeLevel)
	s.notify(obj)
	return obj, nil
}

// Publish 发布文件新版本（version+1）。
func (s *FileService) Publish(id uint, content, operator, comment, clientIP string) (*model.FileObject, error) {
	if operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	obj, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if err := validateFileContent(content); err != nil {
		return nil, err
	}
	md5 := filetree.ContentMD5(content)
	newVersion := obj.Version + 1
	err = s.db.Transaction(func(tx *gorm.DB) error {
		rev, err := s.appendRevision(tx, obj.ID, newVersion, content, md5, nil, operator, comment)
		if err != nil {
			return err
		}
		obj.Content, obj.ContentMD5, obj.Version, obj.CurrentRevision = content, md5, newVersion, rev.ID
		if err := s.fileRepo.WithTx(tx).Save(obj); err != nil {
			return err
		}
		return s.writeAudit(tx, obj, operator, model.ActionFilePublish,
			fmt.Sprintf(`{"version":%d,"md5":"%s"}`, newVersion, md5), clientIP)
	})
	if err != nil {
		return nil, err
	}
	slog.Info("发布托管文件", "id", id, "version", newVersion)
	s.notify(obj)
	return obj, nil
}

// Rollback 回滚到目标版本（= 读取该版本内容作为新版本发布，version+1）。
func (s *FileService) Rollback(id uint, toVersion int64, operator, comment, clientIP string) (*model.FileObject, error) {
	if operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	obj, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	target, err := s.revRepo.FindByObjectAndVersion(id, toVersion)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, apperr.ErrRevisionNotFound
	}
	newVersion := obj.Version + 1
	src := target.ID
	err = s.db.Transaction(func(tx *gorm.DB) error {
		rev, err := s.appendRevision(tx, obj.ID, newVersion, target.Content, target.ContentMD5, &src, operator, comment)
		if err != nil {
			return err
		}
		obj.Content, obj.ContentMD5, obj.Version, obj.CurrentRevision = target.Content, target.ContentMD5, newVersion, rev.ID
		if err := s.fileRepo.WithTx(tx).Save(obj); err != nil {
			return err
		}
		return s.writeAudit(tx, obj, operator, model.ActionFileRollback,
			fmt.Sprintf(`{"version":%d,"fromVersion":%d,"md5":"%s"}`, newVersion, toVersion, target.ContentMD5), clientIP)
	})
	if err != nil {
		return nil, err
	}
	slog.Info("回滚托管文件", "id", id, "toVersion", toVersion, "newVersion", newVersion)
	s.notify(obj)
	return obj, nil
}

// Delete 软删文件对象（该层从覆盖链脱落，下游 agent 据 manifest 比对会删该 path 的镜像）。
func (s *FileService) Delete(id uint, operator, comment, clientIP string) error {
	if operator == "" {
		return apperr.ErrInvalidParam
	}
	obj, err := s.Get(id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.fileRepo.WithTx(tx).SoftDelete(id, now); err != nil {
			return err
		}
		return s.writeAudit(tx, obj, operator, model.ActionFileDelete, `{"deleted":true}`, clientIP)
	})
	if err != nil {
		return err
	}
	slog.Info("软删托管文件", "id", id)
	s.notify(obj)
	return nil
}

// ListRevisions 列出某文件对象的历史版本。
func (s *FileService) ListRevisions(id uint) ([]model.FileRevision, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	return s.revRepo.ListByObject(id)
}

// GetRevision 取某文件对象的指定历史版本。
func (s *FileService) GetRevision(id uint, version int64) (*model.FileRevision, error) {
	rev, err := s.revRepo.FindByObjectAndVersion(id, version)
	if err != nil {
		return nil, err
	}
	if rev == nil {
		return nil, apperr.ErrRevisionNotFound
	}
	return rev, nil
}

// appendRevision 追加一条指定内容的版本快照。
func (s *FileService) appendRevision(tx *gorm.DB, objectID uint, version int64, content, md5 string, source *uint, operator, comment string) (*model.FileRevision, error) {
	rev := &model.FileRevision{
		FileObjectID: objectID, Version: version,
		Content: content, ContentMD5: md5, SourceRevision: source,
		Operator: operator, Comment: comment,
	}
	if err := s.revRepo.WithTx(tx).Create(rev); err != nil {
		return nil, err
	}
	return rev, nil
}

// writeAudit 在事务内写一条文件审计。
func (s *FileService) writeAudit(tx *gorm.DB, obj *model.FileObject, operator, action, detail, clientIP string) error {
	return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
		NamespaceCode: obj.NamespaceCode,
		Operator:      operator,
		Action:        action,
		TargetType:    model.TargetTypeFile,
		TargetRef:     fmt.Sprintf("%s/%s/%s@%s:%s", obj.NamespaceCode, obj.GroupCode, obj.Path, obj.ScopeLevel, obj.ScopeTarget),
		Detail:        detail,
		Result:        model.ResultOK,
		ClientIP:      clientIP,
	})
}

// validateFileContent 校验文件内容不超限（整文件 blob 不做格式解析）。
func validateFileContent(content string) error {
	if len(content) > MaxFileContentBytes {
		return apperr.ErrContentTooLarge
	}
	return nil
}

// normalizePath 规整并校验文件相对 path：非空、清理冗余、禁止绝对路径与向上穿越（防 agent 落盘逃逸到 dataFolder 之外）。
func normalizePath(p string) (string, error) {
	if p == "" {
		return "", apperr.ErrInvalidPath
	}
	if strings.ContainsAny(p, "\\") {
		return "", apperr.ErrInvalidPath // 统一用正斜杠，反斜杠拒绝
	}
	clean := path.Clean(p)
	if clean == "." || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", apperr.ErrInvalidPath
	}
	return clean, nil
}
