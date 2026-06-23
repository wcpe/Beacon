package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
)

// ReverseFetchIgnoreRuleRepository 提供 reverse_fetch_ignore_rule 表的数据访问（FR-59）。
// 规则真源在库：未删为软删哨兵、删除即软删（同标识可再建）；按 ns / scope / group / scopeTarget 维度查活跃规则。
type ReverseFetchIgnoreRuleRepository struct {
	db *gorm.DB
}

// NewReverseFetchIgnoreRuleRepository 构造仓库。
func NewReverseFetchIgnoreRuleRepository(db *gorm.DB) *ReverseFetchIgnoreRuleRepository {
	return &ReverseFetchIgnoreRuleRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本。
func (r *ReverseFetchIgnoreRuleRepository) WithTx(tx *gorm.DB) *ReverseFetchIgnoreRuleRepository {
	return &ReverseFetchIgnoreRuleRepository{db: tx}
}

// active 限定未软删（deleted_at = 哨兵值）的查询基。
func (r *ReverseFetchIgnoreRuleRepository) active() *gorm.DB {
	return r.db.Where("deleted_at = ?", model.SoftDeleteSentinel)
}

// Create 追加一条忽略规则。
func (r *ReverseFetchIgnoreRuleRepository) Create(rule *model.ReverseFetchIgnoreRule) error {
	return r.db.Create(rule).Error
}

// FindByID 按主键查未软删规则；不存在返回 (nil, nil)。
func (r *ReverseFetchIgnoreRuleRepository) FindByID(id uint) (*model.ReverseFetchIgnoreRule, error) {
	var rule model.ReverseFetchIgnoreRule
	err := r.active().Where("id = ?", id).First(&rule).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rule, nil
}

// List 列出活跃规则（ns / scope / group / scopeTarget 任一为空则不过滤），最新在前。
func (r *ReverseFetchIgnoreRuleRepository) List(ns, scope, group, scopeTarget string) ([]model.ReverseFetchIgnoreRule, error) {
	q := r.active()
	if ns != "" {
		q = q.Where("namespace_code = ?", ns)
	}
	if scope != "" {
		q = q.Where("scope = ?", scope)
	}
	if group != "" {
		q = q.Where("group_code = ?", group)
	}
	if scopeTarget != "" {
		q = q.Where("scope_target = ?", scopeTarget)
	}
	var list []model.ReverseFetchIgnoreRule
	if err := q.Order("id desc").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// SoftDelete 软删一条规则（置 deleted_at 为真实时间，解除唯一键占位允许同标识再建）。
// 返回是否命中（不存在 / 已删则 false）。
func (r *ReverseFetchIgnoreRuleRepository) SoftDelete(id uint, now time.Time) (bool, error) {
	res := r.db.Model(&model.ReverseFetchIgnoreRule{}).
		Where("id = ? AND deleted_at = ?", id, model.SoftDeleteSentinel).
		Update("deleted_at", now)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
