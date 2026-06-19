package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"beacon/internal/apperr"
	"beacon/internal/merge"
	"beacon/internal/model"
	"beacon/internal/repository"
)

// errGrayVersionConflict 是灰度发布乐观锁 CAS 未命中的内部哨兵：触发事务回滚 + 重读重试，不外泄。
var errGrayVersionConflict = errors.New("灰度发布乐观锁版本冲突，需重试")

// maxGrayPublishRetries 是灰度发布乐观锁 CAS 的最大重试次数。
// 并发 N 路时每轮至少一路 CAS 命中并提交，故 N-1 次内必成功；取 16 足够覆盖现实管理员竞态。
const maxGrayPublishRetries = 16

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

// Publish 对某 config_item 发布一条灰度（指定灰度内容 + cohort 名单）。
// 内容过既有发布前校验（格式 / 大小 / 可解析 / FR-27 schema）；sensitive 与所属 item 镜像。
// 同一 item 已有活跃灰度则先软删旧的再建新的（保持至多一个活跃灰度的唯一约束）。
//
// 重发即覆盖语义：并发对同一 item 发布灰度时，以 config_item.gray_version 为基准做乐观锁 CAS——
// 抢到的那路才进「先软删后建」段，未抢到的重读版本重试。CAS 在 item 行上串行化（单行锁、无环），
// 从源头消除「先软删后建」在 uk_gray_item 上的死锁；各路重试后最终都成功、恰留一条活跃灰度。
func (s *ConfigGrayService) Publish(itemID uint, content string, cohort []string, operator, comment, clientIP string) (*model.ConfigGray, error) {
	if operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	item, err := s.configSvc.Get(itemID)
	if err != nil {
		return nil, err
	}
	if err := validateContent(item.Format, content); err != nil {
		return nil, err
	}
	encodedCohort, err := encodeCohort(cohort)
	if err != nil {
		return nil, err
	}
	md5 := merge.MD5Hex(content)

	var gray *model.ConfigGray
	for attempt := 0; attempt <= maxGrayPublishRetries; attempt++ {
		// 每次尝试重读最新 gray_version 作为 CAS 基准
		cur, e := s.configRepo.FindByID(item.ID)
		if e != nil {
			return nil, e
		}
		if cur == nil {
			return nil, apperr.ErrConfigNotFound
		}
		expected := cur.GrayVersion

		e = s.db.Transaction(func(tx *gorm.DB) error {
			ok, te := s.configRepo.WithTx(tx).BumpGrayVersion(item.ID, expected)
			if te != nil {
				return te
			}
			if !ok {
				// 版本被并发灰度发布改动，回滚后重读重试
				return errGrayVersionConflict
			}
			// CAS 已串行化本段：先软删同 item 旧活跃灰度，再建新的（重发即覆盖）
			now := time.Now().UTC()
			g := &model.ConfigGray{
				ConfigItemID: item.ID, NamespaceCode: item.NamespaceCode, Format: item.Format,
				Content: content, ContentMD5: md5, Cohort: encodedCohort, Sensitive: item.Sensitive,
				Comment: comment, Operator: operator,
			}
			if _, te := s.grayRepo.WithTx(tx).SoftDelete(item.ID, now); te != nil {
				return te
			}
			if te := s.grayRepo.WithTx(tx).Create(g); te != nil {
				return te
			}
			if te := s.writeGrayAudit(tx, item, operator, model.ActionConfigGrayPublish,
				fmt.Sprintf(`{"md5":"%s","cohortSize":%d}`, md5, len(decodeMembers(encodedCohort))), clientIP); te != nil {
				return te
			}
			gray = g
			return nil
		})
		if e == nil {
			slog.Info("发布配置灰度", "itemId", item.ID, "dataId", item.DataID, "cohortSize", len(decodeMembers(encodedCohort)))
			s.notifyServers(item.NamespaceCode, encodedCohort)
			return gray, nil
		}
		if !errors.Is(e, errGrayVersionConflict) {
			return nil, e
		}
		// CAS 未命中：item 行锁已让重试天然错峰，无需额外退避，直接重读重试
		slog.Debug("灰度发布乐观锁版本冲突，重读重试", "itemId", item.ID, "第几次重试", attempt+1)
	}
	slog.Warn("灰度发布乐观锁重试耗尽，放弃", "itemId", item.ID, "重试上限", maxGrayPublishRetries)
	return nil, errGrayVersionConflict
}

// Promote 把某 item 的活跃灰度晋升为全量稳定版（version+1）并软删灰度。
// 走既有发布路径：灰度内容作为新稳定版本发布、过校验、敏感加密、记审计、唤醒。
func (s *ConfigGrayService) Promote(itemID uint, operator, comment, clientIP string) (*model.ConfigItem, error) {
	if operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	item, err := s.configSvc.Get(itemID)
	if err != nil {
		return nil, err
	}
	gray, err := s.grayRepo.FindActiveByItem(item.ID)
	if err != nil {
		return nil, err
	}
	if gray == nil {
		return nil, apperr.ErrGrayNotFound
	}
	// 晋升等同把灰度内容作为新版本发布，需同样过发布前校验（兜底防御历史脏灰度）
	if err := validateContent(item.Format, gray.Content); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	newVersion := item.Version + 1
	err = s.db.Transaction(func(tx *gorm.DB) error {
		rev, e := s.configSvc.appendRevisionContent(tx, item.ID, item.Format, newVersion,
			gray.Content, gray.ContentMD5, item.Sensitive, nil, operator, comment)
		if e != nil {
			return e
		}
		item.Content, item.ContentMD5, item.Version, item.CurrentRevision = gray.Content, gray.ContentMD5, newVersion, rev.ID
		if e := s.configRepo.WithTx(tx).Save(item); e != nil {
			return e
		}
		if _, e := s.grayRepo.WithTx(tx).SoftDelete(item.ID, now); e != nil {
			return e
		}
		return s.writeGrayAudit(tx, item, operator, model.ActionConfigGrayPromote,
			fmt.Sprintf(`{"version":%d,"md5":"%s"}`, newVersion, gray.ContentMD5), clientIP)
	})
	if err != nil {
		return nil, err
	}
	slog.Info("晋升配置灰度为稳定版", "itemId", item.ID, "version", newVersion)
	// 晋升走发布路径（生成新稳定版本），与普通发布同样计入发布计数（FR-30）
	if s.metrics != nil {
		s.metrics.IncConfigPublish()
	}
	// 晋升影响 item 整 scope（稳定版变了）+ 原 cohort 成员（灰度撤销）；按 scope + cohort 名单并集唤醒
	s.notifyPromote(item, gray.Cohort)
	return item, nil
}

// Abort 丢弃某 item 的活跃灰度（软删）；cohort 成员回到稳定版本，稳定指针不动。
func (s *ConfigGrayService) Abort(itemID uint, operator, comment, clientIP string) error {
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
