package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/merge"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// encodeCohort 把 serverId 名单规整（去空白 / 去空串 / 去重 / 字典序）后序列化为 JSON 文本。
// 名单为空（全空白 / nil）视为非法（无意义灰度），返回 ErrEmptyCohort。
func encodeCohort(ids []string) (string, error) {
	set := map[string]struct{}{}
	for _, id := range ids {
		s := strings.TrimSpace(id)
		if s == "" {
			continue
		}
		set[s] = struct{}{}
	}
	if len(set) == 0 {
		return "", apperr.ErrEmptyCohort
	}
	uniq := make([]string, 0, len(set))
	for s := range set {
		uniq = append(uniq, s)
	}
	sort.Strings(uniq)
	b, err := json.Marshal(uniq)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeCohort 把落库的 cohort JSON 文本反序列化为 serverId 命中集合。
func decodeCohort(encoded string) (map[string]bool, error) {
	var ids []string
	if err := json.Unmarshal([]byte(encoded), &ids); err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

// DecodeCohortList 把落库 cohort 文本反解析为有序 serverId 名单（供 handler 视图，解析失败返回空）。
func DecodeCohortList(encoded string) []string {
	return decodeMembers(encoded)
}

// cohortMembers 返回 cohort 集合的成员清单（字典序，供按名单逐 serverId 唤醒）。
func cohortMembers(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ServerNotifier 是"按 serverId 名单唤醒配置长轮询"的窄接口（由 ChangeNotifier 实现，可选注入）。
type ServerNotifier interface {
	NotifyServers(ns string, serverIDs []string)
	NotifyConfigChange(ns, scopeLevel, group, scopeTarget string)
}

// ConfigGrayService 编排配置灰度 / Beta（FR-9，见 ADR-0021）：
// 灰度发布 / promote / abort 均事务内写表 + 审计原子完成，提交后按受影响 serverId 唤醒。
// promote 复用 ConfigService 的既有发布路径（appendRevision + 更新 item 指针），不另造发布机制。
type ConfigGrayService struct {
	db         *gorm.DB
	configSvc  *ConfigService
	configRepo *repository.ConfigItemRepository
	grayRepo   *repository.ConfigGrayRepository
	auditRepo  *repository.AuditLogRepository
	notifier   ServerNotifier  // 可选，事务提交后唤醒
	metrics    PublishRecorder // 可选，promote 走发布路径同样计入发布计数（FR-30，见 ADR-0020）
}

// NewConfigGrayService 构造服务。复用 configSvc 的发布路径与 configRepo 完成 promote。
func NewConfigGrayService(db *gorm.DB, configSvc *ConfigService, configRepo *repository.ConfigItemRepository, grayRepo *repository.ConfigGrayRepository, auditRepo *repository.AuditLogRepository) *ConfigGrayService {
	return &ConfigGrayService{db: db, configSvc: configSvc, configRepo: configRepo, grayRepo: grayRepo, auditRepo: auditRepo}
}

// SetNotifier 注入唤醒器（启动时装配；未注入则不唤醒）。
func (s *ConfigGrayService) SetNotifier(n ServerNotifier) {
	s.notifier = n
}

// SetMetrics 注入发布计数器（启动时装配；未注入则不计数）。
func (s *ConfigGrayService) SetMetrics(m PublishRecorder) {
	s.metrics = m
}

// List 列出某环境内当前活跃灰度。
func (s *ConfigGrayService) List(ns string) ([]model.ConfigGray, error) {
	return s.grayRepo.ListActive(ns)
}

// Publish 是已废弃的公开副作用入口；灰度发布只能由审批执行器调用私有事务方法。
func (s *ConfigGrayService) Publish(_ uint, _ string, _ []string, _, _, _ string) (*model.ConfigGray, error) {
	return nil, apperr.ErrForbidden
}

// Promote 是已废弃的公开副作用入口；灰度晋升只能由审批执行器调用私有事务方法。
func (s *ConfigGrayService) Promote(_ uint, _, _, _ string) (*model.ConfigItem, error) {
	return nil, apperr.ErrForbidden
}

func (s *ConfigGrayService) applyPublishInTx(tx *gorm.DB, payload configApprovalPayload, pending configPendingPayload) (*model.ConfigGray, *model.ConfigItem, error) {
	item, err := s.configSvc.GetInTx(tx, payload.ConfigItemID)
	if err != nil {
		return nil, nil, err
	}
	if item.Version != payload.ExpectedVersion || item.GrayVersion != payload.ExpectedGrayVersion || pending.Operator == "" || configContentHash(pending.Content) != payload.ContentSHA256 {
		return nil, nil, apperr.ErrApprovalTargetChanged
	}
	if err := validateContent(item.Format, pending.Content); err != nil {
		return nil, nil, err
	}
	if _, err := decodeCohort(pending.Cohort); err != nil {
		return nil, nil, apperr.ErrApprovalTargetChanged
	}
	ok, err := s.configRepo.WithTx(tx).BumpGrayVersion(item.ID, payload.ExpectedGrayVersion)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, apperr.ErrApprovalTargetChanged
	}
	gray := &model.ConfigGray{ConfigItemID: item.ID, NamespaceCode: item.NamespaceCode, Format: item.Format, Content: pending.Content, ContentMD5: merge.MD5Hex(pending.Content), Cohort: pending.Cohort, Sensitive: item.Sensitive, Comment: pending.Comment, Operator: pending.Operator}
	if _, err := s.grayRepo.WithTx(tx).SoftDelete(item.ID, time.Now().UTC()); err != nil {
		return nil, nil, err
	}
	if err := s.grayRepo.WithTx(tx).Create(gray); err != nil {
		return nil, nil, err
	}
	if err := s.writeGrayAudit(tx, item, pending.Operator, model.ActionConfigGrayPublish, fmt.Sprintf(`{"md5":"%s","cohortSize":%d}`, gray.ContentMD5, len(decodeMembers(pending.Cohort))), pending.ClientIP); err != nil {
		return nil, nil, err
	}
	return gray, item, nil
}

func (s *ConfigGrayService) applyPromoteInTx(tx *gorm.DB, payload configApprovalPayload, pending configPendingPayload) (*model.ConfigItem, *model.ConfigGray, error) {
	item, err := s.configSvc.GetInTx(tx, payload.ConfigItemID)
	if err != nil {
		return nil, nil, err
	}
	if item.Version != payload.ExpectedVersion || item.GrayVersion != payload.ExpectedGrayVersion || pending.Operator == "" || configContentHash(pending.Content) != payload.ContentSHA256 {
		return nil, nil, apperr.ErrApprovalTargetChanged
	}
	gray, err := s.grayRepo.WithTx(tx).FindActiveByItem(item.ID)
	if err != nil {
		return nil, nil, err
	}
	if gray == nil || gray.Content != pending.Content || gray.Cohort != pending.Cohort || gray.ContentMD5 != merge.MD5Hex(pending.Content) {
		return nil, nil, apperr.ErrApprovalTargetChanged
	}
	if err := validateContent(item.Format, pending.Content); err != nil {
		return nil, nil, err
	}
	newVersion := item.Version + 1
	rev, err := s.configSvc.appendRevisionContent(tx, item.ID, item.Format, newVersion, pending.Content, gray.ContentMD5, item.Sensitive, nil, pending.Operator, pending.Comment)
	if err != nil {
		return nil, nil, err
	}
	item.Content, item.ContentMD5, item.Version, item.CurrentRevision = pending.Content, gray.ContentMD5, newVersion, rev.ID
	if err := s.configRepo.WithTx(tx).Save(item); err != nil {
		return nil, nil, err
	}
	if _, err := s.grayRepo.WithTx(tx).SoftDelete(item.ID, time.Now().UTC()); err != nil {
		return nil, nil, err
	}
	if err := s.writeGrayAudit(tx, item, pending.Operator, model.ActionConfigGrayPromote, fmt.Sprintf(`{"version":%d,"md5":"%s"}`, newVersion, gray.ContentMD5), pending.ClientIP); err != nil {
		return nil, nil, err
	}
	return item, gray, nil
}

// Abort 丢弃某 item 的活跃灰度（软删）；cohort 成员回到稳定版本，稳定指针不动。
func (s *ConfigGrayService) Abort(itemID uint, operator, _, clientIP string) error {
	if operator == "" {
		return apperr.ErrInvalidParam
	}
	item, err := s.configSvc.Get(itemID)
	if err != nil {
		return err
	}
	gray, err := s.grayRepo.FindActiveByItem(item.ID)
	if err != nil {
		return err
	}
	if gray == nil {
		return apperr.ErrGrayNotFound
	}
	now := time.Now().UTC()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		deleted, e := s.grayRepo.WithTx(tx).SoftDelete(item.ID, now)
		if e != nil {
			return e
		}
		if !deleted {
			return apperr.ErrGrayNotFound
		}
		return s.writeGrayAudit(tx, item, operator, model.ActionConfigGrayAbort, `{"aborted":true}`, clientIP)
	})
	if err != nil {
		return err
	}
	slog.Info("中止配置灰度", "itemId", item.ID)
	s.notifyServers(item.NamespaceCode, gray.Cohort)
	return nil
}

// notifyServers 按 cohort 名单逐 serverId 唤醒配置长轮询（发布 / abort 仅影响 cohort 成员）。
func (s *ConfigGrayService) notifyServers(ns, encodedCohort string) {
	if s.notifier == nil {
		return
	}
	s.notifier.NotifyServers(ns, decodeMembers(encodedCohort))
}

// notifyPromote 晋升后按 item scope（稳定版变更波及全 scope）+ 原 cohort 名单并集唤醒。
func (s *ConfigGrayService) notifyPromote(item *model.ConfigItem, encodedCohort string) {
	if s.notifier == nil {
		return
	}
	s.notifier.NotifyConfigChange(item.NamespaceCode, item.ScopeLevel, item.GroupCode, item.ScopeTarget)
	s.notifier.NotifyServers(item.NamespaceCode, decodeMembers(encodedCohort))
}

// decodeMembers 把落库 cohort 文本反解析为成员清单；解析失败返回空（不阻断主流程）。
func decodeMembers(encoded string) []string {
	set, err := decodeCohort(encoded)
	if err != nil {
		slog.Warn("解析灰度 cohort 失败，跳过唤醒", "cohort", encoded, "错误", err)
		return nil
	}
	return cohortMembers(set)
}

// writeGrayAudit 在事务内写一条灰度审计。
func (s *ConfigGrayService) writeGrayAudit(tx *gorm.DB, item *model.ConfigItem, operator, action, detail, clientIP string) error {
	return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
		NamespaceCode: item.NamespaceCode,
		Operator:      operator,
		Action:        action,
		TargetType:    model.TargetTypeConfig,
		TargetRef:     fmt.Sprintf("%s/%s/%s@%s:%s", item.NamespaceCode, item.GroupCode, item.DataID, item.ScopeLevel, item.ScopeTarget),
		Detail:        detail,
		Result:        model.ResultOK,
		ClientIP:      clientIP,
	})
}
