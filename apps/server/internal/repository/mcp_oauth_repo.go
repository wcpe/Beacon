package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// MCPOAuthRepository 管理 MCP OAuth 客户端、待应用变更与短期 token 的持久化。
type MCPOAuthRepository struct{ db *gorm.DB }

// NewMCPOAuthRepository 构造 MCP OAuth 仓库。
func NewMCPOAuthRepository(db *gorm.DB) *MCPOAuthRepository { return &MCPOAuthRepository{db: db} }

// WithTx 返回绑定事务的仓库副本。
func (r *MCPOAuthRepository) WithTx(tx *gorm.DB) *MCPOAuthRepository {
	return &MCPOAuthRepository{db: tx}
}

// CreateClient 新建已激活客户端，只能由已批准的领域适配器调用。
func (r *MCPOAuthRepository) CreateClient(client *model.MCPOAuthClient) error {
	return r.db.Create(client).Error
}

// FindClient 按公开 clientId 查找客户端。
func (r *MCPOAuthRepository) FindClient(clientID string) (*model.MCPOAuthClient, error) {
	var client model.MCPOAuthClient
	err := r.db.Where("client_id = ?", clientID).First(&client).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &client, nil
}

// ListClients 按创建倒序读取客户端元数据。
func (r *MCPOAuthRepository) ListClients() ([]model.MCPOAuthClient, error) {
	var clients []model.MCPOAuthClient
	if err := r.db.Order("created_at DESC, id DESC").Find(&clients).Error; err != nil {
		return nil, err
	}
	return clients, nil
}

// CreateChange 落库待审批凭据变更。
func (r *MCPOAuthRepository) CreateChange(change *model.MCPOAuthClientChange) error {
	return r.db.Create(change).Error
}

// FindChangeByApprovalRequest 查询某审批请求冻结的唯一客户端变更。
func (r *MCPOAuthRepository) FindChangeByApprovalRequest(requestID string) (*model.MCPOAuthClientChange, error) {
	var change model.MCPOAuthClientChange
	err := r.db.Where("approval_request_id = ?", requestID).First(&change).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &change, nil
}

// ApplyChangeCAS 将 pending 变更一次性置为 applied。
func (r *MCPOAuthRepository) ApplyChangeCAS(changeID string) (bool, error) {
	result := r.db.Model(&model.MCPOAuthClientChange{}).
		Where("change_id = ? AND status = ?", changeID, model.MCPClientChangePending).
		Update("status", model.MCPClientChangeApplied)
	return result.RowsAffected == 1, result.Error
}

// InvalidateChangeCAS 使未应用变更永久失效。
func (r *MCPOAuthRepository) InvalidateChangeCAS(requestID string) (bool, error) {
	result := r.db.Model(&model.MCPOAuthClientChange{}).
		Where("approval_request_id = ? AND status = ?", requestID, model.MCPClientChangePending).
		Update("status", model.MCPClientChangeInvalidated)
	return result.RowsAffected == 1, result.Error
}

// SaveClient 保存客户端已批准的生命周期变化。
func (r *MCPOAuthRepository) SaveClient(client *model.MCPOAuthClient) error {
	return r.db.Save(client).Error
}

// CreateAccessToken 只写入 access token 摘要。
func (r *MCPOAuthRepository) CreateAccessToken(token *model.MCPAccessToken) error {
	return r.db.Create(token).Error
}

// FindAccessToken 按 token 摘要查找记录。
func (r *MCPOAuthRepository) FindAccessToken(hash string) (*model.MCPAccessToken, error) {
	var token model.MCPAccessToken
	err := r.db.Where("token_hash = ?", hash).First(&token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &token, nil
}

// RevokeClientCAS 直接吊销 active 客户端；调用者同时必须写强审计。
func (r *MCPOAuthRepository) RevokeClientCAS(clientID string, now time.Time) (bool, error) {
	result := r.db.Model(&model.MCPOAuthClient{}).
		Where("client_id = ? AND status = ?", clientID, model.MCPClientStatusActive).
		Updates(map[string]any{"status": model.MCPClientStatusRevoked, "revoked_at": now})
	return result.RowsAffected == 1, result.Error
}

// DeleteExpiredTokens 分批清除已经失效且非当前认证路径所需的短期 token。
func (r *MCPOAuthRepository) DeleteExpiredTokens(now time.Time, limit int) (int64, error) {
	if limit < 1 {
		limit = 100
	}
	var ids []uint
	if err := r.db.Model(&model.MCPAccessToken{}).Where("expires_at < ?", now).Order("id asc").Limit(limit).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result := r.db.Where("id IN ?", ids).Delete(&model.MCPAccessToken{})
	return result.RowsAffected, result.Error
}
