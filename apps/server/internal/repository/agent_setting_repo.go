package repository

import (
	"errors"

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

// Delete 删除某作用域下某键的覆盖；不存在时静默成功（幂等，撤销不存在的下发不是错误）。
func (r *AgentSettingRepository) Delete(namespaceID uint, serverID, key string) (bool, error) {
	res := r.db.Where("namespace_id = ? AND server_id = ? AND setting_key = ?",
		namespaceID, serverID, key).Delete(&model.AgentSetting{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}
