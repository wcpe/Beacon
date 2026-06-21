package service

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/filetree"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// CreateOverrideSetParams 是新建覆盖集（首次发布）的入参（FR-15）。
type CreateOverrideSetParams struct {
	Namespace     string
	Group         string
	Name          string
	ScopeLevel    string
	ScopeTarget   string
	TargetRoot    string
	ReloadCommand string
	Operator      string
	Comment       string
	ClientIP      string
}

// PublishOverrideSetParams 是发布覆盖集新版本的入参。
type PublishOverrideSetParams struct {
	TargetRoot    string
	ReloadCommand string
	Operator      string
	Comment       string
	ClientIP      string
}

// OverrideSetDryRun 是发布前 dry-run 只读预览结果（不落任何东西，见 ADR-0011 决策 8）：
// 列出将覆盖哪些成员文件 + 将执行什么重载命令。
type OverrideSetDryRun struct {
	TargetRoot    string
	ReloadCommand string
	// 将覆盖的成员文件 path 列表（按字典序）
	MemberPaths []string
	// 命令首 token（供前端展示，白名单由 agent 本地把关，不在控制面）
	CommandFirstToken string
}

// OverrideSetService 编排三方插件文件覆盖兼容（FR-15）的覆盖集 CRUD/发布/回滚/历史 + dry-run 预览。
// 控制面只存"目标根 + 受限重载命令 + 成员清单"这一事实，不执行命令、不编排；命令由 agent 本地受限派发。
// 事务内 set+revision+audit 原子完成（见 ADR-0011 与架构不变量）。
type OverrideSetService struct {
	db        *gorm.DB
	setRepo   *repository.FileOverrideSetRepository
	revRepo   *repository.FileOverrideSetRevisionRepository
	fileRepo  *repository.FileObjectRepository
	auditRepo *repository.AuditLogRepository
	notifier  *ChangeNotifier // 可选，事务提交后唤醒受影响的 override 长轮询（复用 fileHub）
}

// NewOverrideSetService 构造服务。
func NewOverrideSetService(
	db *gorm.DB,
	setRepo *repository.FileOverrideSetRepository,
	revRepo *repository.FileOverrideSetRevisionRepository,
	fileRepo *repository.FileObjectRepository,
	auditRepo *repository.AuditLogRepository,
) *OverrideSetService {
	return &OverrideSetService{db: db, setRepo: setRepo, revRepo: revRepo, fileRepo: fileRepo, auditRepo: auditRepo}
}

// SetNotifier 注入长轮询唤醒器（启动时装配；未注入则不唤醒）。
func (s *OverrideSetService) SetNotifier(n *ChangeNotifier) {
	s.notifier = n
}

// notify 在事务提交成功后按覆盖集 scope 唤醒受影响实例（复用文件长轮询的唤醒集合，见 ADR-0010）。
func (s *OverrideSetService) notify(set *model.FileOverrideSet) {
	if s.notifier != nil {
		s.notifier.NotifyFileChange(set.NamespaceCode, set.ScopeLevel, set.GroupCode, set.ScopeTarget)
	}
}

// List 列出覆盖集。
func (s *OverrideSetService) List(f repository.OverrideSetFilter) ([]model.FileOverrideSet, error) {
	return s.setRepo.List(f)
}

// Get 取单个覆盖集；不存在返回 OVERRIDE_SET_NOT_FOUND。
func (s *OverrideSetService) Get(id uint) (*model.FileOverrideSet, error) {
	set, err := s.setRepo.FindByID(id)
	if err != nil {
		return nil, err
	}
	if set == nil {
		return nil, apperr.ErrOverrideSetNotFound
	}
	return set, nil
}

// Create 新建覆盖集并首次发布（version=1）。
// 早校验 target_root（限定 plugins/<plugin>/ 内）与 reload_command（单条、无元字符），
// agent 为最终权威（同口径再校验，见 ADR-0011 决策 4）。
func (s *OverrideSetService) Create(p CreateOverrideSetParams) (*model.FileOverrideSet, error) {
	if p.Namespace == "" || p.Operator == "" || p.Name == "" {
		return nil, apperr.ErrInvalidParam
	}
	group, scopeTarget, err := normalizeScope(p.ScopeLevel, p.Group, p.ScopeTarget)
	if err != nil {
		return nil, err
	}
	root, err := filetree.ValidateTargetRoot(p.TargetRoot)
	if err != nil {
		return nil, err
	}
	cmd, err := normalizeReloadCommand(p.ReloadCommand)
	if err != nil {
		return nil, err
	}
	existing, err := s.setRepo.FindByIdentity(p.Namespace, group, p.Name, p.ScopeLevel, scopeTarget)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, apperr.ErrOverrideSetConflict
	}

	set := &model.FileOverrideSet{
		NamespaceCode: p.Namespace, GroupCode: group, Name: p.Name,
		ScopeLevel: p.ScopeLevel, ScopeTarget: scopeTarget,
		TargetRoot: root, ReloadCommand: cmd, Mode: model.OverrideModeFileOverride,
		Version: 1, Enabled: true,
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.setRepo.WithTx(tx).Create(set); err != nil {
			return err
		}
		rev, err := s.appendRevision(tx, set.ID, 1, root, cmd, "", nil, p.Operator, p.Comment)
		if err != nil {
			return err
		}
		set.CurrentRevision = rev.ID
		if err := s.setRepo.WithTx(tx).Save(set); err != nil {
			return err
		}
		return s.writeAudit(tx, set, p.Operator, model.ActionOverrideSetCreate,
			fmt.Sprintf(`{"version":1,"targetRoot":%q,"hasCommand":%t}`, root, cmd != ""), p.ClientIP)
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, apperr.ErrOverrideSetConflict
		}
		return nil, err
	}
	slog.Info("新建覆盖集", "namespace", p.Namespace, "group", group, "name", p.Name, "scope", p.ScopeLevel, "targetRoot", root)
	s.notify(set)
	return set, nil
}

// Publish 发布覆盖集新版本（version+1）：更新目标根 + 重载命令，快照成员清单。
func (s *OverrideSetService) Publish(id uint, p PublishOverrideSetParams) (*model.FileOverrideSet, error) {
	if p.Operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	set, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	root, err := filetree.ValidateTargetRoot(p.TargetRoot)
	if err != nil {
		return nil, err
	}
	cmd, err := normalizeReloadCommand(p.ReloadCommand)
	if err != nil {
		return nil, err
	}
	memberPaths, err := s.memberPathList(set)
	if err != nil {
		return nil, err
	}
	newVersion := set.Version + 1
	err = s.db.Transaction(func(tx *gorm.DB) error {
		rev, err := s.appendRevision(tx, set.ID, newVersion, root, cmd, strings.Join(memberPaths, "\n"), nil, p.Operator, p.Comment)
		if err != nil {
			return err
		}
		set.TargetRoot, set.ReloadCommand, set.Version, set.CurrentRevision = root, cmd, newVersion, rev.ID
		if err := s.setRepo.WithTx(tx).Save(set); err != nil {
			return err
		}
		return s.writeAudit(tx, set, p.Operator, model.ActionOverrideSetPublish,
			fmt.Sprintf(`{"version":%d,"targetRoot":%q,"hasCommand":%t}`, newVersion, root, cmd != ""), p.ClientIP)
	})
	if err != nil {
		return nil, err
	}
	slog.Info("发布覆盖集", "id", id, "version", newVersion, "targetRoot", root)
	s.notify(set)
	return set, nil
}

// Rollback 回滚覆盖集到目标版本（读取该版本目标根 + 命令作为新版本发布，version+1）。
// 注意：回滚只还原"覆盖集事实"，绝不触发重放重载命令——命令重放由 agent 侧明令禁止（见 ADR-0011 决策 5）。
func (s *OverrideSetService) Rollback(id uint, toVersion int64, operator, comment, clientIP string) (*model.FileOverrideSet, error) {
	if operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	set, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	target, err := s.revRepo.FindBySetAndVersion(id, toVersion)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, apperr.ErrRevisionNotFound
	}
	newVersion := set.Version + 1
	src := target.ID
	err = s.db.Transaction(func(tx *gorm.DB) error {
		rev, err := s.appendRevision(tx, set.ID, newVersion, target.TargetRoot, target.ReloadCommand, target.MemberPaths, &src, operator, comment)
		if err != nil {
			return err
		}
		set.TargetRoot, set.ReloadCommand, set.Version, set.CurrentRevision = target.TargetRoot, target.ReloadCommand, newVersion, rev.ID
		if err := s.setRepo.WithTx(tx).Save(set); err != nil {
			return err
		}
		return s.writeAudit(tx, set, operator, model.ActionOverrideSetRollback,
			fmt.Sprintf(`{"version":%d,"fromVersion":%d,"targetRoot":%q}`, newVersion, toVersion, target.TargetRoot), clientIP)
	})
	if err != nil {
		return nil, err
	}
	slog.Info("回滚覆盖集", "id", id, "toVersion", toVersion, "newVersion", newVersion)
	s.notify(set)
	return set, nil
}

// Delete 软删覆盖集。
func (s *OverrideSetService) Delete(id uint, operator, _, clientIP string) error {
	if operator == "" {
		return apperr.ErrInvalidParam
	}
	set, err := s.Get(id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.setRepo.WithTx(tx).SoftDelete(id, now); err != nil {
			return err
		}
		return s.writeAudit(tx, set, operator, model.ActionOverrideSetDelete, `{"deleted":true}`, clientIP)
	})
	if err != nil {
		return err
	}
	slog.Info("软删覆盖集", "id", id)
	s.notify(set)
	return nil
}

// ListRevisions 列出某覆盖集的历史版本。
func (s *OverrideSetService) ListRevisions(id uint) ([]model.FileOverrideSetRevision, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	return s.revRepo.ListBySet(id)
}

// DryRun 发布前只读预览（不落任何东西，见 ADR-0011 决策 8）：返回将覆盖的成员文件清单 + 将执行的命令。
func (s *OverrideSetService) DryRun(id uint) (*OverrideSetDryRun, error) {
	set, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	memberPaths, err := s.memberPathList(set)
	if err != nil {
		return nil, err
	}
	return &OverrideSetDryRun{
		TargetRoot:        set.TargetRoot,
		ReloadCommand:     set.ReloadCommand,
		MemberPaths:       memberPaths,
		CommandFirstToken: filetree.FirstToken(set.ReloadCommand),
	}, nil
}

// memberPathList 取覆盖集成员文件的 path 清单（按字典序），并对每条做目标根内的早校验（agent 为最终权威）。
func (s *OverrideSetService) memberPathList(set *model.FileOverrideSet) ([]string, error) {
	members, err := s.fileRepo.ListByOverrideSet(set.ID)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(members))
	for _, m := range members {
		if err := filetree.ValidateMemberPath(set.TargetRoot, m.Path); err != nil {
			return nil, err // 成员路径不合法即拒绝整次操作（防越界落盘）
		}
		paths = append(paths, m.Path)
	}
	return paths, nil
}

// appendRevision 追加一条覆盖集版本快照。
func (s *OverrideSetService) appendRevision(tx *gorm.DB, setID uint, version int64, root, cmd, memberPaths string, source *uint, operator, comment string) (*model.FileOverrideSetRevision, error) {
	rev := &model.FileOverrideSetRevision{
		OverrideSetID: setID, Version: version,
		TargetRoot: root, ReloadCommand: cmd, MemberPaths: memberPaths,
		SourceRevision: source, Operator: operator, Comment: comment,
	}
	if err := s.revRepo.WithTx(tx).Create(rev); err != nil {
		return nil, err
	}
	return rev, nil
}

// writeAudit 在事务内写一条覆盖集审计。
func (s *OverrideSetService) writeAudit(tx *gorm.DB, set *model.FileOverrideSet, operator, action, detail, clientIP string) error {
	return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
		NamespaceCode: set.NamespaceCode,
		Operator:      operator,
		Action:        action,
		TargetType:    model.TargetTypeOverrideSet,
		TargetRef:     fmt.Sprintf("%s/%s/%s@%s:%s", set.NamespaceCode, set.GroupCode, set.Name, set.ScopeLevel, set.ScopeTarget),
		Detail:        detail,
		Result:        model.ResultOK,
		ClientIP:      clientIP,
	})
}

// normalizeReloadCommand 归一化并校验一条受限重载命令；空命令视为"不下发命令"放行。
func normalizeReloadCommand(cmd string) (string, error) {
	if strings.TrimSpace(cmd) == "" {
		return "", nil // 空命令合法：只覆盖文件、不下发命令
	}
	return filetree.ValidateReloadCommand(cmd)
}
