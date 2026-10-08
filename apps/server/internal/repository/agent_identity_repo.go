package repository

import (
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// AgentIdentityRepository 提供 v2 agent 身份绑定事实的窄读数据访问（FR-264）：
// 交付能力版本守卫按 serverId 批量取 agent 自报版本，避免下发路径逐台查库。
// 身份的写入 / 状态机归 V2ControlPlaneService，本仓库只读、不承载状态迁移。
type AgentIdentityRepository struct {
	db *gorm.DB
}

// NewAgentIdentityRepository 构造仓库。
func NewAgentIdentityRepository(db *gorm.DB) *AgentIdentityRepository {
	return &AgentIdentityRepository{db: db}
}

// WithTx 返回绑定到事务的仓库副本（供事务内的能力守卫复用同一连接，防另开连接互等死锁）。
func (r *AgentIdentityRepository) WithTx(tx *gorm.DB) *AgentIdentityRepository {
	return &AgentIdentityRepository{db: tx}
}

// FindVersionsByServerIDs 批量取某 namespace 内各 server 当前绑定的 agent 上报版本（FR-264）。
//
// 同键多行（历史换绑 / 永久墓碑保留原 serverId）按 status_changed_at 升序扫描后覆盖写，
// 留下最新一行——不用窗口函数 / 相关子查询，守 MySQL 5.7 与 Postgres 双兼容（架构不变量 §4）。
// **无身份行的服不回键**：由调用方按「无版本 = 旧 agent 未上报 = 不支持」fail-closed 处理。
// 入参为空返回空集。
func (r *AgentIdentityRepository) FindVersionsByServerIDs(namespaceID uint, serverIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(serverIDs))
	if len(serverIDs) == 0 {
		return out, nil
	}
	var rows []model.AgentIdentity
	if err := r.db.Select("server_id", "agent_version", "status_changed_at").
		Where("namespace_id = ? AND server_id IN ?", namespaceID, serverIDs).
		Order("status_changed_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		if !rows[i].ServerID.Assigned() {
			continue
		}
		out[string(rows[i].ServerID)] = rows[i].AgentVersion
	}
	return out, nil
}
