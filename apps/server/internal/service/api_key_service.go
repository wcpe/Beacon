package service

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apikey"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/secret"
)

// lastUsedThrottle 是"最近使用"落库的节流窗口：同一密钥至多每此间隔写一次，
// 避免外部服务高频读时每请求一次 DB 写。节流状态即库内 last_used_at 本身、无额外内存态。
const lastUsedThrottle = time.Minute

// apiKeyPrincipalPrefix 是 API 密钥认证身份写入审计 operator 的前缀（区别于人类操作者用户名）。
const apiKeyPrincipalPrefix = "apikey:"

// APIKeyService 编排管理面 API 密钥（FR-42，见 ADR-0026）：
// 运行时创建/吊销/重置（事务内写表 + 审计原子完成）+ 认证校验（查库比对哈希，真源在库）。
type APIKeyService struct {
	db               *gorm.DB
	repo             *repository.APIKeyRepository
	auditRepo        *repository.AuditLogRepository
	approval         *ApprovalService
	credentialCipher *secret.Cipher
}

// NewAPIKeyService 构造服务。
func NewAPIKeyService(db *gorm.DB, repo *repository.APIKeyRepository, auditRepo *repository.AuditLogRepository) *APIKeyService {
	return &APIKeyService{db: db, repo: repo, auditRepo: auditRepo}
}

// SetCredentialCipher 注入审批凭据一次性兑换所用的独立密钥。
func (s *APIKeyService) SetCredentialCipher(cipher *secret.Cipher) {
	s.credentialCipher = cipher
}

// Create 已废止直接创建入口，危险凭据只能经审批 worker 执行。
func (s *APIKeyService) Create(name, role string, expiresAt *time.Time, operator, clientIP string) (string, *model.APIKey, error) {
	return "", nil, apperr.ErrForbidden
}

func (s *APIKeyService) applyCreateInTx(tx *gorm.DB, name, role string, expiresAt *time.Time, operator, clientIP string) (string, *model.APIKey, error) {
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
	err = s.repo.WithTx(tx).Create(key)
	if err == nil {
		err = s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			Operator: operator, Action: model.ActionAPIKeyCreate,
			TargetType: model.TargetTypeAPIKey, TargetRef: name,
			Detail: keyAuditDetail(key), Result: model.ResultOK, ClientIP: clientIP,
		})
	}
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

// Reset 已废止直接轮换入口，危险凭据只能经审批 worker 执行。
func (s *APIKeyService) Reset(id uint, operator, clientIP string) (string, *model.APIKey, error) {
	return "", nil, apperr.ErrForbidden
}

func (s *APIKeyService) applyResetInTx(tx *gorm.DB, id uint, operator, clientIP string) (string, *model.APIKey, error) {
	plaintext, hash, prefix, err := apikey.Generate()
	if err != nil {
		return "", nil, err
	}
	var key *model.APIKey
	repo := s.repo.WithTx(tx)
	found, e := repo.FindActiveByID(id)
	if e != nil {
		return "", nil, e
	}
	if found == nil {
		return "", nil, apperr.ErrAPIKeyNotFound
	}
	ok, e := repo.RotateSecret(id, hash, prefix)
	if e != nil {
		return "", nil, e
	}
	if !ok {
		return "", nil, apperr.ErrAPIKeyNotFound
	}
	// 用轮换后的明面字段回填视图（旧最近使用已清空）。
	found.KeyHash = hash
	found.KeyPrefix = prefix
	found.LastUsedAt = nil
	key = found
	slog.Info("重置 API 密钥", "名称", found.Name, "operator", operator)
	err = s.auditRepo.WithTx(tx).Create(&model.AuditLog{
		Operator: operator, Action: model.ActionAPIKeyReset,
		TargetType: model.TargetTypeAPIKey, TargetRef: found.Name,
		Detail: keyAuditDetail(found), Result: model.ResultOK, ClientIP: clientIP,
	})
	if err != nil {
		return "", nil, err
	}
	return plaintext, key, nil
}

// RedeemCredentialSecret 让原申请人工主体原子领取一次审批成功后的 API 密钥明文。
func (s *APIKeyService) RedeemCredentialSecret(requestID string, principal auth.Principal) (string, error) {
	principal = auth.NormalizePrincipal(principal)
	if !principal.IsHuman() {
		return "", apperr.ErrCredentialSecretLost
	}
	ciphertext, err := s.consumeCredentialSecret(requestID, principal)
	if err != nil {
		return "", err
	}
	if s.credentialCipher == nil {
		return "", apperr.ErrCredentialSecretLost
	}
	plaintext, err := s.credentialCipher.Decrypt(ciphertext)
	if err != nil {
		return "", apperr.ErrCredentialSecretLost
	}
	return plaintext, nil
}

func (s *APIKeyService) consumeCredentialSecret(requestID string, principal auth.Principal) (string, error) {
	var ciphertext string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		request, err := repository.NewApprovalRequestRepository(tx).FindByPublicID(requestID)
		if err != nil || !canRedeemCredential(request, principal) {
			return apperr.ErrCredentialSecretLost
		}
		secrets := repository.NewApprovalCredentialSecretRepository(tx)
		stored, err := secrets.FindByApprovalRequestID(requestID)
		if err != nil || stored == nil {
			return apperr.ErrCredentialSecretLost
		}
		consumed, err := secrets.Consume(requestID, time.Now().UTC())
		if err != nil || !consumed {
			return apperr.ErrCredentialSecretLost
		}
		ciphertext = stored.Ciphertext
		return nil
	})
	return ciphertext, err
}

func canRedeemCredential(request *model.ApprovalRequest, principal auth.Principal) bool {
	return request != nil && request.Status == model.ApprovalStatusSucceeded &&
		request.RequesterType == auth.PrincipalKindHuman && request.RequesterID == principal.StableID()
}

func (s *APIKeyService) storeCredentialSecretInTx(tx *gorm.DB, requestID, plaintext string) error {
	if s.credentialCipher == nil || !s.credentialCipher.IsEnabled() {
		return apperr.ErrInternal
	}
	ciphertext, err := s.credentialCipher.Encrypt(plaintext)
	if err != nil {
		return err
	}
	return repository.NewApprovalCredentialSecretRepository(tx).Create(&model.ApprovalCredentialSecret{
		ApprovalRequestID: requestID, Ciphertext: ciphertext,
	})
}

// List 列出全部密钥（含已吊销，供展示状态），按创建时间倒序；不含任何明文 / 哈希。
func (s *APIKeyService) List() ([]model.APIKey, error) {
	return s.repo.List()
}

// Verify 校验明文密钥并返回认证主体（实现 server.APIKeyVerifier）。
// 查库未软删行比对哈希（真源在库、吊销即时生效）→ 校验未过期 → 节流更新最近使用。
// 失败返回 ErrAdminUnauthorized（缺失 / 错误 / 过期 / 吊销一律 401）；DB 故障返回原始错误（→500）。
func (s *APIKeyService) Verify(rawKey string) (auth.Principal, error) {
	key, err := s.repo.FindActiveByHash(apikey.Hash(rawKey))
	if err != nil {
		return auth.Principal{}, err
	}
	if key == nil {
		return auth.Principal{}, apperr.ErrAdminUnauthorized
	}
	now := time.Now().UTC()
	if key.ExpiresAt != nil && now.After(*key.ExpiresAt) {
		return auth.Principal{}, apperr.ErrAdminUnauthorized
	}
	// 节流更新最近使用：best-effort，失败仅告警、不阻断认证
	if key.LastUsedAt == nil || now.Sub(*key.LastUsedAt) >= lastUsedThrottle {
		if e := s.repo.TouchLastUsed(key.ID, now); e != nil {
			slog.Warn("更新 API 密钥最近使用失败", "名称", key.Name, "原因", e)
		}
	}
	principal := auth.APIKeyPrincipal(strconv.FormatUint(uint64(key.ID), 10), key.Name, key.Role, key.KeyPrefix)
	principal.Operator = apiKeyPrincipalPrefix + key.Name
	return principal, nil
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
