package service

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"beacon/internal/apperr"
	"beacon/internal/merge"
	"beacon/internal/model"
	"beacon/internal/repository"
)

// MaxContentBytes 是单条配置内容大小上限（256KB）。
const MaxContentBytes = 256 * 1024

// CreateConfigParams 是新建配置项（首次发布）的入参。
type CreateConfigParams struct {
	Namespace   string
	Group       string
	DataID      string
	ScopeLevel  string
	ScopeTarget string
	Format      string
	Content     string
	Operator    string
	Comment     string
}

// ConfigService 编排配置中心：CRUD/发布/回滚/历史/diff，事务内 item+revision+audit 原子完成。
type ConfigService struct {
	db         *gorm.DB
	configRepo *repository.ConfigItemRepository
	revRepo    *repository.ConfigRevisionRepository
	auditRepo  *repository.AuditLogRepository
	notifier   *ChangeNotifier // 可选，事务提交后唤醒受影响长轮询
}

// NewConfigService 构造服务。
func NewConfigService(db *gorm.DB, configRepo *repository.ConfigItemRepository, revRepo *repository.ConfigRevisionRepository, auditRepo *repository.AuditLogRepository) *ConfigService {
	return &ConfigService{db: db, configRepo: configRepo, revRepo: revRepo, auditRepo: auditRepo}
}

// SetNotifier 注入长轮询唤醒器（启动时装配；未注入则不唤醒）。
func (s *ConfigService) SetNotifier(n *ChangeNotifier) {
	s.notifier = n
}

// notify 在事务提交成功后唤醒该配置项 scope 下受影响的长轮询。
func (s *ConfigService) notify(item *model.ConfigItem) {
	if s.notifier != nil {
		s.notifier.NotifyConfigChange(item.NamespaceCode, item.ScopeLevel, item.GroupCode, item.ScopeTarget)
	}
}

// List 列出配置项。
func (s *ConfigService) List(f repository.ConfigFilter) ([]model.ConfigItem, error) {
	return s.configRepo.List(f)
}

// Get 取单个配置项；不存在返回 CONFIG_NOT_FOUND。
func (s *ConfigService) Get(id uint) (*model.ConfigItem, error) {
	item, err := s.configRepo.FindByID(id)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, apperr.ErrConfigNotFound
	}
	return item, nil
}

// Create 新建配置项并首次发布（version=1）。
func (s *ConfigService) Create(p CreateConfigParams) (*model.ConfigItem, error) {
	if p.Namespace == "" || p.DataID == "" || p.Operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	group, scopeTarget, err := normalizeScope(p.ScopeLevel, p.Group, p.ScopeTarget)
	if err != nil {
		return nil, err
	}
	if err := validateContent(p.Format, p.Content); err != nil {
		return nil, err
	}
	if err := s.checkFormatConsistency(p.Namespace, p.DataID, p.Format); err != nil {
		return nil, err
	}
	existing, err := s.configRepo.FindByIdentity(p.Namespace, group, p.DataID, p.ScopeLevel, scopeTarget)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, apperr.ErrConfigConflict
	}

	md5 := merge.MD5Hex(p.Content)
	item := &model.ConfigItem{
		NamespaceCode: p.Namespace, GroupCode: group, DataID: p.DataID,
		ScopeLevel: p.ScopeLevel, ScopeTarget: scopeTarget, Format: p.Format,
		Content: p.Content, ContentMD5: md5, Version: 1, Enabled: true,
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.configRepo.WithTx(tx).Create(item); err != nil {
			return err
		}
		rev, err := s.appendRevision(tx, item, 1, nil, p.Operator, p.Comment)
		if err != nil {
			return err
		}
		item.CurrentRevision = rev.ID
		if err := s.configRepo.WithTx(tx).Save(item); err != nil {
			return err
		}
		return s.writeAudit(tx, item, p.Operator, model.ActionConfigCreate,
			fmt.Sprintf(`{"version":1,"md5":"%s"}`, md5))
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, apperr.ErrConfigConflict
		}
		return nil, err
	}
	slog.Info("新建配置项", "namespace", p.Namespace, "group", group, "dataId", p.DataID, "scope", p.ScopeLevel)
	s.notify(item)
	return item, nil
}

// Publish 发布配置新版本（version+1）。
func (s *ConfigService) Publish(id uint, content, operator, comment string) (*model.ConfigItem, error) {
	if operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	item, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if err := validateContent(item.Format, content); err != nil {
		return nil, err
	}
	md5 := merge.MD5Hex(content)
	newVersion := item.Version + 1
	err = s.db.Transaction(func(tx *gorm.DB) error {
		rev, err := s.appendRevisionContent(tx, item.ID, item.Format, newVersion, content, md5, nil, operator, comment)
		if err != nil {
			return err
		}
		item.Content, item.ContentMD5, item.Version, item.CurrentRevision = content, md5, newVersion, rev.ID
		if err := s.configRepo.WithTx(tx).Save(item); err != nil {
			return err
		}
		return s.writeAudit(tx, item, operator, model.ActionConfigPublish,
			fmt.Sprintf(`{"version":%d,"md5":"%s"}`, newVersion, md5))
	})
	if err != nil {
		return nil, err
	}
	slog.Info("发布配置", "id", id, "version", newVersion)
	s.notify(item)
	return item, nil
}

// Rollback 回滚到目标版本（= 读取该版本内容作为新版本发布，version+1）。
func (s *ConfigService) Rollback(id uint, toVersion int64, operator, comment string) (*model.ConfigItem, error) {
	if operator == "" {
		return nil, apperr.ErrInvalidParam
	}
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
	newVersion := item.Version + 1
	src := target.ID
	err = s.db.Transaction(func(tx *gorm.DB) error {
		rev, err := s.appendRevisionContent(tx, item.ID, item.Format, newVersion, target.Content, target.ContentMD5, &src, operator, comment)
		if err != nil {
			return err
		}
		item.Content, item.ContentMD5, item.Version, item.CurrentRevision = target.Content, target.ContentMD5, newVersion, rev.ID
		if err := s.configRepo.WithTx(tx).Save(item); err != nil {
			return err
		}
		return s.writeAudit(tx, item, operator, model.ActionConfigRollback,
			fmt.Sprintf(`{"version":%d,"fromVersion":%d,"md5":"%s"}`, newVersion, toVersion, target.ContentMD5))
	})
	if err != nil {
		return nil, err
	}
	slog.Info("回滚配置", "id", id, "toVersion", toVersion, "newVersion", newVersion)
	s.notify(item)
	return item, nil
}

// Delete 软删配置项（该层从合并链脱落）。
func (s *ConfigService) Delete(id uint, operator, comment string) error {
	if operator == "" {
		return apperr.ErrInvalidParam
	}
	item, err := s.Get(id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.configRepo.WithTx(tx).SoftDelete(id, now); err != nil {
			return err
		}
		return s.writeAudit(tx, item, operator, model.ActionConfigDelete, `{"deleted":true}`)
	})
	if err != nil {
		return err
	}
	slog.Info("软删配置项", "id", id)
	s.notify(item)
	return nil
}

// ListRevisions 列出某配置项的历史版本。
func (s *ConfigService) ListRevisions(id uint) ([]model.ConfigRevision, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	return s.revRepo.ListByItem(id)
}

// GetRevision 取某配置项的指定历史版本。
func (s *ConfigService) GetRevision(id uint, version int64) (*model.ConfigRevision, error) {
	rev, err := s.revRepo.FindByItemAndVersion(id, version)
	if err != nil {
		return nil, err
	}
	if rev == nil {
		return nil, apperr.ErrRevisionNotFound
	}
	return rev, nil
}

// Diff 返回两版本内容文本（供前端渲染差异）。
func (s *ConfigService) Diff(id uint, from, to int64) (string, string, error) {
	fromRev, err := s.GetRevision(id, from)
	if err != nil {
		return "", "", err
	}
	toRev, err := s.GetRevision(id, to)
	if err != nil {
		return "", "", err
	}
	return fromRev.Content, toRev.Content, nil
}

// appendRevision 以 item 当前内容追加一条版本快照。
func (s *ConfigService) appendRevision(tx *gorm.DB, item *model.ConfigItem, version int64, source *uint, operator, comment string) (*model.ConfigRevision, error) {
	return s.appendRevisionContent(tx, item.ID, item.Format, version, item.Content, item.ContentMD5, source, operator, comment)
}

// appendRevisionContent 追加一条指定内容的版本快照。
func (s *ConfigService) appendRevisionContent(tx *gorm.DB, itemID uint, format string, version int64, content, md5 string, source *uint, operator, comment string) (*model.ConfigRevision, error) {
	rev := &model.ConfigRevision{
		ConfigItemID: itemID, Version: version, Format: format,
		Content: content, ContentMD5: md5, SourceRevision: source,
		Operator: operator, Comment: comment,
	}
	if err := s.revRepo.WithTx(tx).Create(rev); err != nil {
		return nil, err
	}
	return rev, nil
}

// writeAudit 在事务内写一条配置审计。
func (s *ConfigService) writeAudit(tx *gorm.DB, item *model.ConfigItem, operator, action, detail string) error {
	return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
		NamespaceCode: item.NamespaceCode,
		Operator:      operator,
		Action:        action,
		TargetType:    model.TargetTypeConfig,
		TargetRef:     fmt.Sprintf("%s/%s/%s@%s:%s", item.NamespaceCode, item.GroupCode, item.DataID, item.ScopeLevel, item.ScopeTarget),
		Detail:        detail,
		Result:        model.ResultOK,
	})
}

// checkFormatConsistency 校验同一 dataId 跨层格式一致。
func (s *ConfigService) checkFormatConsistency(ns, dataID, format string) error {
	existing, err := s.configRepo.FindFormatByDataID(ns, dataID)
	if err != nil {
		return err
	}
	if existing != "" && existing != format {
		return apperr.ErrFormatInconsistent
	}
	return nil
}

// validateContent 校验格式合法、大小不超限、内容可解析。
func validateContent(format, content string) error {
	if !merge.IsValidFormat(format) {
		return apperr.ErrInvalidParam
	}
	if len(content) > MaxContentBytes {
		return apperr.ErrContentTooLarge
	}
	if _, err := merge.Parse(format, content); err != nil {
		return apperr.ErrContentInvalid
	}
	return nil
}

// normalizeScope 按覆盖层规整并校验 (group, scopeTarget)。
// global → group=__GLOBAL__、target=”；group → target=”；zone/server → target 非空。
func normalizeScope(level, group, scopeTarget string) (string, string, error) {
	if !model.IsValidScopeLevel(level) {
		return "", "", apperr.ErrInvalidScope
	}
	switch level {
	case model.ScopeGlobal:
		return model.GlobalGroupCode, "", nil
	case model.ScopeGroup:
		if group == "" || group == model.GlobalGroupCode {
			return "", "", apperr.ErrInvalidScope
		}
		return group, "", nil
	default: // zone / server
		if group == "" || group == model.GlobalGroupCode || scopeTarget == "" {
			return "", "", apperr.ErrInvalidScope
		}
		return group, scopeTarget, nil
	}
}
