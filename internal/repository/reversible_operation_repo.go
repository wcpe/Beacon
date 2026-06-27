package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/model"
)

// ReversibleOperationFilter 是可逆操作列表查询的可选过滤条件（FR-116）。
type ReversibleOperationFilter struct {
	Namespace string
	// OpType 操作类型过滤（push/publish/fetch），空则不过滤
	OpType string
	// Status 状态过滤（reversible/reversed/...），空则不过滤
	Status string
	// Limit 返回上限（<=0 时由调用方给默认）
	Limit int
}

// ReversibleOperationRepository 提供 reversible_operation 表的数据访问（FR-116，见 ADR-0051）。
// 账目真源在库：建为 reversible，撤回经状态 CAS 翻转 reversed；过期 / 被覆盖经 CAS 置 expired/superseded。
// 所有状态写均经 WHERE status=expect 的 CAS + RowsAffected 判命中——串行化并发撤回 / 覆盖，杜绝双撤回（ADR-0051 决策 6/7）。
type ReversibleOperationRepository struct {
	db *gorm.DB
}

// NewReversibleOperationRepository 构造仓库。
func NewReversibleOperationRepository(db *gorm.DB) *ReversibleOperationRepository {
	return &ReversibleOperationRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本（供 service 在事务内复用）。
func (r *ReversibleOperationRepository) WithTx(tx *gorm.DB) *ReversibleOperationRepository {
	return &ReversibleOperationRepository{db: tx}
}

// Create 追加一条可逆账目（状态由调用方置 reversible）。
func (r *ReversibleOperationRepository) Create(op *model.ReversibleOperation) error {
	return r.db.Create(op).Error
}

// FindByID 按主键查账目；不存在返回 (nil, nil)。
func (r *ReversibleOperationRepository) FindByID(id uint) (*model.ReversibleOperation, error) {
	var op model.ReversibleOperation
	err := r.db.Where("id = ?", id).First(&op).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &op, nil
}

// MarkReversed 把账目从 reversible CAS 翻转为 reversed 并回填撤回人 / 时间 + 清空反向快照瞬态。
// 返回是否命中（前态非 reversible / 不存在则 false）——撤回幂等闸：抢不到 CAS 即说明已被撤回 / 过期 / 覆盖。
func (r *ReversibleOperationRepository) MarkReversed(id uint, reversedBy string, now time.Time) (bool, error) {
	res := r.db.Model(&model.ReversibleOperation{}).
		Where("id = ? AND status = ?", id, model.ReversibleStatusReversible).
		Updates(map[string]any{
			"status":          model.ReversibleStatusReversed,
			"reversed_by":     reversedBy,
			"reversed_at":     now,
			"inverse_payload": "",
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// SupersedeActiveByForwardRef 把同一被操作对象（forward_ref 标识具体 config_item / file_object）上仍 reversible
// 的旧账目 CAS 置 superseded 并清空其反向快照瞬态（同对象发生后续同类操作时调用，防撤回旧操作把后续操作抹掉的
// 脏撤回，ADR-0051 决策 8）。以 forward_ref 而非 (scope,scopeTarget) 匹配——后者太粗会把同层不同对象误判为覆盖。
// exceptID 排除刚新建的当前账目（其自身仍可撤回）。空 forwardRef 不匹配任何行（防误伤）。返回受影响条数。
func (r *ReversibleOperationRepository) SupersedeActiveByForwardRef(ns, opType, forwardRef string, exceptID uint, now time.Time) (int64, error) {
	if forwardRef == "" {
		return 0, nil
	}
	res := r.db.Model(&model.ReversibleOperation{}).
		Where("namespace_code = ? AND op_type = ? AND forward_ref = ? AND status = ? AND id <> ?",
			ns, opType, forwardRef, model.ReversibleStatusReversible, exceptID).
		Updates(map[string]any{
			"status":          model.ReversibleStatusSuperseded,
			"inverse_payload": "",
			"updated_at":      now,
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// ExpireStale 把创建早于 before、仍 reversible 的账目 CAS 置 expired 并清空反向快照瞬态（清理器调用）。
// 返回受影响条数。
func (r *ReversibleOperationRepository) ExpireStale(before, now time.Time) (int64, error) {
	res := r.db.Model(&model.ReversibleOperation{}).
		Where("status = ? AND created_at < ?", model.ReversibleStatusReversible, before).
		Updates(map[string]any{
			"status":          model.ReversibleStatusExpired,
			"inverse_payload": "",
			"updated_at":      now,
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// List 按过滤条件列出账目（最新在前，供工作台操作日志）。
func (r *ReversibleOperationRepository) List(f ReversibleOperationFilter) ([]model.ReversibleOperation, error) {
	q := r.db.Model(&model.ReversibleOperation{})
	if f.Namespace != "" {
		q = q.Where("namespace_code = ?", f.Namespace)
	}
	if f.OpType != "" {
		q = q.Where("op_type = ?", f.OpType)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	var list []model.ReversibleOperation
	if err := q.Order("id desc").Limit(limit).Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}
