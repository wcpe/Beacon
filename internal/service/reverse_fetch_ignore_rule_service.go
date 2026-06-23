package service

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// ReverseFetchIgnoreRuleService 编排反向抓取持久忽略规则（FR-59）：建 / 列 / 删 + 入审计，
// 并提供「按任务作用域匹配活跃规则」给扫描清单标 ignoredByRule（纯展示标记，不改 manifest 存储）。
type ReverseFetchIgnoreRuleService struct {
	db        *gorm.DB
	ruleRepo  *repository.ReverseFetchIgnoreRuleRepository
	auditRepo *repository.AuditLogRepository
}

// NewReverseFetchIgnoreRuleService 构造服务。
func NewReverseFetchIgnoreRuleService(db *gorm.DB, ruleRepo *repository.ReverseFetchIgnoreRuleRepository,
	auditRepo *repository.AuditLogRepository) *ReverseFetchIgnoreRuleService {
	return &ReverseFetchIgnoreRuleService{db: db, ruleRepo: ruleRepo, auditRepo: auditRepo}
}

// CreateRuleParams 是新建忽略规则的入参。
type CreateRuleParams struct {
	Namespace   string
	Scope       string // group / server
	Group       string
	ScopeTarget string // scope=server 时的目标 serverId（group 层留空）
	RuleType    string // exact / prefix
	Pattern     string
	Comment     string
	Operator    string
	ClientIP    string
}

// Create 建一条忽略规则（事务内 Create + 审计）。同标识活跃规则已存在 → ErrConfigConflict（唯一键兜底归一）。
func (s *ReverseFetchIgnoreRuleService) Create(p CreateRuleParams) (*model.ReverseFetchIgnoreRule, error) {
	if p.Namespace == "" || p.Operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	if p.Scope != model.ScopeGroup && p.Scope != model.ScopeServer {
		return nil, apperr.ErrInvalidScope
	}
	if p.Group == "" || (p.Scope == model.ScopeServer && p.ScopeTarget == "") {
		return nil, apperr.ErrInvalidScope
	}
	// group 层规则不挂具体实例：归一掉多余 target，避免污染唯一键。
	scopeTarget := p.ScopeTarget
	if p.Scope == model.ScopeGroup {
		scopeTarget = ""
	}
	if !model.IsValidIgnoreRuleType(p.RuleType) {
		return nil, apperr.ErrInvalidParam
	}
	// 模式按文件相对 path 归一（统一正斜杠、禁穿越）；prefix 允许以 / 结尾表目录前缀，故先剥尾再归一。
	pattern, err := normalizeIgnorePattern(p.RuleType, p.Pattern)
	if err != nil {
		return nil, err
	}

	rule := &model.ReverseFetchIgnoreRule{
		NamespaceCode: p.Namespace, Scope: p.Scope, GroupCode: p.Group, ScopeTarget: scopeTarget,
		RuleType: p.RuleType, Pattern: pattern, Comment: p.Comment, Operator: p.Operator,
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if e := s.ruleRepo.WithTx(tx).Create(rule); e != nil {
			return e
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: p.Namespace, Operator: p.Operator, Action: model.ActionReverseFetchIgnoreRuleAdd,
			TargetType: model.TargetTypeReverseFetchIgnoreRule, TargetRef: fmt.Sprintf("%d", rule.ID),
			Detail: fmt.Sprintf(`{"ruleId":%d,"scope":%q,"group":%q,"target":%q,"ruleType":%q,"pattern":%q}`,
				rule.ID, p.Scope, p.Group, scopeTarget, p.RuleType, pattern),
			Result: model.ResultOK, ClientIP: p.ClientIP,
		})
	})
	if err != nil {
		// 唯一键冲突归一为冲突（同标识活跃规则已存在）。
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, apperr.ErrConfigConflict
		}
		return nil, err
	}
	slog.Info("新增反向抓取忽略规则", "namespace", p.Namespace, "scope", p.Scope, "group", p.Group,
		"target", scopeTarget, "ruleType", p.RuleType, "pattern", pattern, "ruleId", rule.ID, "operator", p.Operator)
	return rule, nil
}

// List 列出活跃忽略规则（按 ns / scope / group / scopeTarget 过滤）。
func (s *ReverseFetchIgnoreRuleService) List(ns, scope, group, scopeTarget string) ([]model.ReverseFetchIgnoreRule, error) {
	return s.ruleRepo.List(ns, scope, group, scopeTarget)
}

// Delete 软删一条忽略规则（事务内 SoftDelete + 审计）；规则不存在 / 已删 → ErrConfigNotFound。
func (s *ReverseFetchIgnoreRuleService) Delete(id uint, operator, clientIP string) error {
	if operator == "" {
		return apperr.ErrInvalidParam
	}
	rule, err := s.ruleRepo.FindByID(id)
	if err != nil {
		return err
	}
	if rule == nil {
		return apperr.ErrConfigNotFound
	}
	now := time.Now().UTC()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		ok, e := s.ruleRepo.WithTx(tx).SoftDelete(id, now)
		if e != nil {
			return e
		}
		if !ok {
			return apperr.ErrConfigNotFound // 并发已删
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: rule.NamespaceCode, Operator: operator, Action: model.ActionReverseFetchIgnoreRuleRemove,
			TargetType: model.TargetTypeReverseFetchIgnoreRule, TargetRef: fmt.Sprintf("%d", id),
			Detail: fmt.Sprintf(`{"ruleId":%d,"scope":%q,"group":%q,"target":%q,"pattern":%q}`,
				id, rule.Scope, rule.GroupCode, rule.ScopeTarget, rule.Pattern),
			Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return err
	}
	slog.Info("删除反向抓取忽略规则", "ruleId", id, "operator", operator)
	return nil
}

// MatchActive 取作用于某任务 (ns, scope, group, scopeTarget) 的活跃规则集（供扫描清单标 ignoredByRule）。
// 大区层规则（scope=group）对该大区下任意实例生效；实例层规则（scope=server）仅对该 serverId 生效——
// 故 server 作用域任务同时受 group 层与本实例 server 层规则约束。
func (s *ReverseFetchIgnoreRuleService) MatchActive(ns, taskScope, group, scopeTarget string) ([]model.ReverseFetchIgnoreRule, error) {
	rules := make([]model.ReverseFetchIgnoreRule, 0)
	groupRules, err := s.ruleRepo.List(ns, model.ScopeGroup, group, "")
	if err != nil {
		return nil, err
	}
	rules = append(rules, groupRules...)
	// server 作用域任务额外叠加本实例的 server 层规则。
	if taskScope == model.ScopeServer && scopeTarget != "" {
		serverRules, err := s.ruleRepo.List(ns, model.ScopeServer, group, scopeTarget)
		if err != nil {
			return nil, err
		}
		rules = append(rules, serverRules...)
	}
	return rules, nil
}

// IgnoredByRules 判定某文件 path 是否命中规则集中任一规则（exact: path==pattern；prefix: HasPrefix）。
// 纯函数：供扫描清单视图即时标 ignoredByRule，不改 manifest 存储。
func IgnoredByRules(path string, rules []model.ReverseFetchIgnoreRule) bool {
	for i := range rules {
		switch rules[i].RuleType {
		case model.IgnoreRuleExact:
			if path == rules[i].Pattern {
				return true
			}
		case model.IgnoreRulePrefix:
			if strings.HasPrefix(path, rules[i].Pattern) {
				return true
			}
		}
	}
	return false
}

// normalizeIgnorePattern 归一忽略规则模式：exact 按文件相对 path 归一（统一正斜杠、禁绝对 / 穿越）；
// prefix 容许以 / 结尾的目录前缀，剥尾归一后补回 /（保前缀语义，使 ServerProbe/ 只匹配该目录下）。
func normalizeIgnorePattern(ruleType, pattern string) (string, error) {
	if strings.TrimSpace(pattern) == "" {
		return "", apperr.ErrInvalidParam
	}
	if ruleType == model.IgnoreRulePrefix {
		trimmed := strings.TrimSuffix(pattern, "/")
		clean, err := normalizePath(trimmed)
		if err != nil {
			return "", err
		}
		return clean + "/", nil
	}
	return normalizePath(pattern)
}
