package service

import (
	"encoding/json"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"beacon/internal/apikey"
	"beacon/internal/apperr"
	"beacon/internal/model"
	"beacon/internal/repository"
)

// lastUsedThrottle 是"最近使用"落库的节流窗口：同一密钥至多每此间隔写一次，
// 避免外部服务高频读时每请求一次 DB 写。节流状态即库内 last_used_at 本身、无额外内存态。
const lastUsedThrottle = time.Minute

// apiKeyPrincipalPrefix 是 API 密钥认证身份写入审计 operator 的前缀（区别于人类操作者用户名）。
const apiKeyPrincipalPrefix = "apikey:"

// APIKeyService 编排管理面 API 密钥（FR-42，见 ADR-0026）：
// 运行时创建/吊销/重置（事务内写表 + 审计原子完成）+ 认证校验（查库比对哈希，真源在库）。
type APIKeyService struct {
	db        *gorm.DB
	repo      *repository.APIKeyRepository
	auditRepo *repository.AuditLogRepository
}

// NewAPIKeyService 构造服务。
func NewAPIKeyService(db *gorm.DB, repo *repository.APIKeyRepository, auditRepo *repository.AuditLogRepository) *APIKeyService {
	return &APIKeyService{db: db, repo: repo, auditRepo: auditRepo}
}

// Create 创建一把新密钥：校验入参 → 生成明文/哈希 → 事务内写 api_key + 审计。
// 返回**明文**（仅此一次可得，调用方一次性回给用户后丢弃）与落库记录。
func (s *APIKeyService) Create(name, role string, expiresAt *time.Time, operator, clientIP string) (string, *model.APIKey, error) {
	if name == "" || !model.IsValidRole(role) {
		return "", nil, apperr.ErrInvalidParam
	}
	// 过期时刻若给定须在未来（否则建出来即失效，无意义）
	if expiresAt != nil && !expiresAt.After(time.Now().UTC()) {
		return "", nil, apperr.ErrInvalidParam
	}
	plaintext, hash, prefix, err := apikey.Generate()
	if err != nil {
		return "", nil, err
	}
	key := &model.APIKey{Name: name, KeyHash: hash, KeyPrefix: prefix, Role: role, ExpiresAt: expiresAt}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if e := s.repo.WithTx(tx).Create(key); e != nil {
			return e
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			Operator: operator, Action: model.ActionAPIKeyCreate,
			TargetType: model.TargetTypeAPIKey, TargetRef: name,
			Detail: keyAuditDetail(key), Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return "", nil, err
	}
	slog.Info("创建 API 密钥", "名称", name, "角色", role, "operator", operator)
	return plaintext, key, nil
}

// Revoke 吊销某密钥（软删）：事务内软删 + 审计；不存在 / 已吊销返回 API_KEY_NOT_FOUND。
func (s *APIKeyService) Revoke(id uint, operator, clientIP string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		repo := s.repo.WithTx(tx)
		key, err := repo.FindActiveByID(id)
		if err != nil {
			return err
		}
		if key == nil {
			return apperr.ErrAPIKeyNotFound
		}
		ok, err := repo.Revoke(id, time.Now().UTC())
		if err != nil {
			return err
		}
		if !ok {
			return apperr.ErrAPIKeyNotFound
		}
		slog.Info("吊销 API 密钥", "名称", key.Name, "operator", operator)
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			Operator: operator, Action: model.ActionAPIKeyRevoke,
			TargetType: model.TargetTypeAPIKey, TargetRef: key.Name,
			Detail: keyAuditDetail(key), Result: model.ResultOK, ClientIP: clientIP,
		})
	})
}

// Reset 重置某密钥的明文（轮换：换新哈希/前缀、清空最近使用，旧明文立即失效）：
// 事务内轮换 + 审计。**密钥不可二次读取，丢失只能重置。** 不存在 / 已吊销返回 API_KEY_NOT_FOUND。
func (s *APIKeyService) Reset(id uint, operator, clientIP string) (string, *model.APIKey, error) {
	plaintext, hash, prefix, err := apikey.Generate()
	if err != nil {
		return "", nil, err
	}
	var key *model.APIKey
	err = s.db.Transaction(func(tx *gorm.DB) error {
		repo := s.repo.WithTx(tx)
		found, e := repo.FindActiveByID(id)
		if e != nil {
			return e
		}
		if found == nil {
			return apperr.ErrAPIKeyNotFound
		}
		ok, e := repo.RotateSecret(id, hash, prefix)
		if e != nil {
			return e
		}
		if !ok {
			return apperr.ErrAPIKeyNotFound
		}
		// 用轮换后的明面字段回填视图（旧最近使用已清空）
		found.KeyHash = hash
		found.KeyPrefix = prefix
		found.LastUsedAt = nil
		key = found
		slog.Info("重置 API 密钥", "名称", found.Name, "operator", operator)
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			Operator: operator, Action: model.ActionAPIKeyReset,
			TargetType: model.TargetTypeAPIKey, TargetRef: found.Name,
			Detail: keyAuditDetail(found), Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return "", nil, err
	}
	return plaintext, key, nil
}

// List 列出全部密钥（含已吊销，供展示状态），按创建时间倒序；不含任何明文 / 哈希。
func (s *APIKeyService) List() ([]model.APIKey, error) {
	return s.repo.List()
}

// Verify 校验明文密钥并返回认证身份与角色（实现 server.APIKeyVerifier）。
// 查库未软删行比对哈希（真源在库、吊销即时生效）→ 校验未过期 → 节流更新最近使用。
// 失败返回 ErrAdminUnauthorized（缺失 / 错误 / 过期 / 吊销一律 401）；DB 故障返回原始错误（→500）。
func (s *APIKeyService) Verify(rawKey string) (string, string, error) {
	key, err := s.repo.FindActiveByHash(apikey.Hash(rawKey))
	if err != nil {
		return "", "", err
	}
	if key == nil {
		return "", "", apperr.ErrAdminUnauthorized
	}
	now := time.Now().UTC()
	if key.ExpiresAt != nil && now.After(*key.ExpiresAt) {
		return "", "", apperr.ErrAdminUnauthorized
	}
	// 节流更新最近使用：best-effort，失败仅告警、不阻断认证
	if key.LastUsedAt == nil || now.Sub(*key.LastUsedAt) >= lastUsedThrottle {
		if e := s.repo.TouchLastUsed(key.ID, now); e != nil {
			slog.Warn("更新 API 密钥最近使用失败", "名称", key.Name, "原因", e)
		}
	}
	return apiKeyPrincipalPrefix + key.Name, key.Role, nil
}

// keyAuditDetail 组装审计 detail（json 文本）：仅元数据，**绝不含明文 / 哈希**。
func keyAuditDetail(key *model.APIKey) string {
	d := map[string]any{"id": key.ID, "name": key.Name, "role": key.Role}
	if key.ExpiresAt != nil {
		d["expiresAt"] = key.ExpiresAt.UTC().Format(time.RFC3339)
	}
	raw, _ := json.Marshal(d)
	return string(raw)
}
