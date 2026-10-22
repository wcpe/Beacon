package repository

import (
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// AgentSettingRepository 提供 agent_setting 表的数据访问（FR-277）。
//
// 只做行级读写，不参与作用域语义：把「server 级覆盖 namespace 级」这件事留在 service，
// 是因为它是**下发语义**（决定 agent 看到什么值），而不是一次查询的细节；仓库层若提前
// 合并，调用方就再也拿不到「这个值到底存不存在覆盖」这一事实，来源列也无从给出。
type AgentSettingRepository struct {
	db *gorm.DB
}

// NewAgentSettingRepository 构造仓库。
func NewAgentSettingRepository(db *gorm.DB) *AgentSettingRepository {
	return &AgentSettingRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本（供 service 在外层事务内复用，避免嵌套开事务死锁）。
func (r *AgentSettingRepository) WithTx(tx *gorm.DB) *AgentSettingRepository {
	return &AgentSettingRepository{db: tx}
}

// ListByNamespace 列出某环境**全部**作用域的行（namespace 级 + 所有 server 级）。
// 供管理面一次取全量后自行拼装两级视图，避免「先查 server 级再补 namespace 级」两轮查询。
func (r *AgentSettingRepository) ListByNamespace(namespaceID uint) ([]model.AgentSetting, error) {
	var rows []model.AgentSetting
	if err := r.db.Where("namespace_id = ?", namespaceID).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListEffective 列出某 server 视角下用得到的行：namespace 级默认 + 该 server 的覆盖。
// serverID 为空时只取 namespace 级（即「只看该环境默认值」）。
// 排序稳定（先 namespace 级再 server 级、同级按键），便于上层按顺序覆盖而不依赖实现细节。
func (r *AgentSettingRepository) ListEffective(namespaceID uint, serverID string) ([]model.AgentSetting, error) {
	q := r.db.Where("namespace_id = ? AND server_id = ''", namespaceID)
	if serverID != "" {
		q = r.db.Where("namespace_id = ? AND server_id IN ?", namespaceID, []string{"", serverID})
	}
	var rows []model.AgentSetting
	if err := q.Order("server_id ASC, setting_key ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// Upsert 写入或更新某作用域下某键的值（唯一键 = namespace_id + server_id + setting_key）。
//
// 直接在 r.db 上执行（r.db 可为外层事务 tx，经 WithTx 注入）——**不自开嵌套事务**：
// 嵌套 db.Transaction 在单连接池（MaxOpenConns=1，如测试内存库）下会因抢同一连接而死锁，
// 与 SettingRepository 同一处置。
//
// **注意：普通 Upsert 不是并发安全的 CAS**（先查后写）。审批执行这类「必须确认基线没被别人
// 改过」的场景请用 [UpsertCAS]，否则多 worker 并发时后写者会静默覆盖先写者。
func (r *AgentSettingRepository) Upsert(row *model.AgentSetting) error {
	var existing model.AgentSetting
	err := r.db.Where("namespace_id = ? AND server_id = ? AND setting_key = ?",
		row.NamespaceID, row.ServerID, row.Key).First(&existing).Error
	if err == nil {
		// 已存在：只覆盖值与修改人，updated_at 由 GORM 自动刷新。
		return r.db.Model(&model.AgentSetting{}).Where("id = ?", existing.ID).
			Updates(map[string]any{"value": row.Value, "updated_by": row.UpdatedBy}).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return r.db.Create(row).Error
}

// UpsertCAS 以「该作用域内该键的既有值」为并发基线写入；基线不符时不写。
//
// ## 为什么需要它
//
// 「既有值当版本」只有在比对与写入是**同一个原子谓词**时才成立。普通 Upsert 先 SELECT 再
// UPDATE，两次查询之间别的 worker 可能已改过该行：两个针对同一键的审批各自都在自己快照里
// 看到旧值，双双重校验通过，后提交者静默覆盖先提交者，两张票却都记 succeeded。
// 对 `override.command-whitelist` 这种键，这是安全相关变更的静默丢失。
//
// ## 约定
//
//   - `expected == nil` 表示「调用方认为该层本无覆盖」：只在真的没有行时才插入；
//     若此时已有行（别人抢先写了），返回 (false, nil) 且不覆盖。
//   - `expected != nil` 表示「调用方看到的值是 *expected」：更新时带 `AND value = ?` 谓词，
//     受影响行数为 0 即基线已变；此时若该行已被删除，同样返回 (false, nil)。
//
// 返回 (是否命中, error)。未命中不是错误——调用方据此判「目标已变」。
func (r *AgentSettingRepository) UpsertCAS(
	row *model.AgentSetting,
	expected *string,
) (bool, error) {
	query := r.db.Where("namespace_id = ? AND server_id = ? AND setting_key = ?",
		row.NamespaceID, row.ServerID, row.Key)

	if expected == nil {
		return r.insertIfAbsent(query, row)
	}

	// 带 value 谓词更新：row.Value 可能为空串（合法值域已排除空串，这里只防误用）。
	res := r.db.Model(&model.AgentSetting{}).
		Where("namespace_id = ? AND server_id = ? AND setting_key = ? AND value = ?",
			row.NamespaceID, row.ServerID, row.Key, *expected).
		Updates(map[string]any{"value": row.Value, "updated_by": row.UpdatedBy})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// insertIfAbsent 只在「该作用域该键确实没有行」时插入。
//
// 拆出来是为了把 UpsertCAS 的嵌套压平：三层嵌套（判存在 → 插入 → 判唯一键冲突）
// 会触发 nestif，而这段逻辑本身并不复杂，只是错误分支多。
func (r *AgentSettingRepository) insertIfAbsent(query *gorm.DB, row *model.AgentSetting) (bool, error) {
	var count int64
	if err := query.Model(&model.AgentSetting{}).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return false, nil // 调用方以为是「本无覆盖」，但已有行：基线已变
	}
	// 插入靠唯一键兜底：并发插入时只有一个能成功，另一个拿到唯一键冲突（同样视为基线已变）。
	if err := r.db.Create(row).Error; err != nil {
		if isUniqueViolation(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// DeleteCAS 以「该作用域内该键的既有值」为并发基线删除；基线不符时不删。
//
// `expected` 为调用方看到的当前值。返回 (是否删除, error)。
func (r *AgentSettingRepository) DeleteCAS(namespaceID uint, serverID, key, expected string) (bool, error) {
	res := r.db.Where("namespace_id = ? AND server_id = ? AND setting_key = ? AND value = ?",
		namespaceID, serverID, key, expected).Delete(&model.AgentSetting{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// isUniqueViolation 粗略识别唯一键冲突（sqlite / MySQL 文案不同，两者都覆盖）。
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate")
}

// Delete 删除某作用域下某键的覆盖；不存在时静默成功（幂等，撤销不存在的下发不是错误）。
func (r *AgentSettingRepository) Delete(namespaceID uint, serverID, key string) (bool, error) {
	res := r.db.Where("namespace_id = ? AND server_id = ? AND setting_key = ?",
		namespaceID, serverID, key).Delete(&model.AgentSetting{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
