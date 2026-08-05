package service

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/secret"
)

func newAPIKeyApprovalTestSuite(t *testing.T) (*APIKeyService, *ApprovalService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.APIKey{}, &model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}, &model.ApprovalCredentialSecret{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移审批密钥表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	for _, table := range []string{"api_key", "approval_request", "approval_execution_receipt", "approval_credential_secret", "audit_log"} {
		if err := db.Exec("DELETE FROM " + table).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", table, err)
		}
	}
	cipher, err := secret.NewCipher(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, secret.KeyBytes)))
	if err != nil {
		t.Fatalf("构造测试加密器失败: %v", err)
	}
	registry := authz.NewApprovalRegistry()
	apiKeys := NewAPIKeyService(db, repository.NewAPIKeyRepository(db), repository.NewAuditLogRepository(db))
	apiKeys.SetCredentialCipher(cipher)
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	apiKeys.SetApprovalService(approval)
	RegisterAPIKeyApprovalAdapters(registry, apiKeys)
	return apiKeys, approval, db
}

func TestAPIKeyApprovalRedeemsEncryptedSecretOnce(t *testing.T) {
	apiKeys, approval, db := newAPIKeyApprovalTestSuite(t)
	requester := auth.HumanPrincipal("alice")
	created, err := apiKeys.RequestCreate("ci", model.RoleFull, nil, "需要自动化访问", "alice", "127.0.0.1", "credential-create", requester)
	if err != nil {
		t.Fatalf("创建审批失败: %v", err)
	}
	var req model.ApprovalRequest
	if err := db.Where("request_id = ?", created.ApprovalRequestID).First(&req).Error; err != nil {
		t.Fatalf("查询审批记录失败: %v", err)
	}
	if !strings.Contains(req.EvidenceSnapshot, "同名有效密钥数") || strings.Contains(req.EvidenceSnapshot, "bk_") {
		t.Fatalf("凭据审批应持久化脱敏证据快照：%s", req.EvidenceSnapshot)
	}
	if _, err := approval.Approve(created.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准审批失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("审批 worker 应执行一次，processed=%d err=%v", processed, err)
	}
	var stored model.ApprovalCredentialSecret
	if err := db.Where("approval_request_id = ?", created.ApprovalRequestID).First(&stored).Error; err != nil {
		t.Fatalf("查询临时密文失败: %v", err)
	}
	if !secret.IsEncrypted(stored.Ciphertext) || strings.Contains(stored.Ciphertext, "bk_") {
		t.Fatalf("审批密钥只应保存密文：%q", stored.Ciphertext)
	}
	req = model.ApprovalRequest{}
	if err := db.Where("request_id = ?", created.ApprovalRequestID).First(&req).Error; err != nil {
		t.Fatalf("查询审批记录失败: %v", err)
	}
	if strings.Contains(req.Payload, "bk_") {
		t.Fatalf("审批载荷不得保存明文：%s", req.Payload)
	}

	plaintext, err := apiKeys.RedeemCredentialSecret(created.ApprovalRequestID, requester)
	if err != nil || !strings.HasPrefix(plaintext, "bk_") {
		t.Fatalf("申请人首次兑换应返回明文，secret=%q err=%v", plaintext, err)
	}
	if _, err := apiKeys.Verify(plaintext); err != nil {
		t.Fatalf("兑换明文应可认证：%v", err)
	}
	for _, principal := range []auth.Principal{requester, auth.HumanPrincipal("mallory"), auth.APIKeyPrincipal("7", "robot", model.RoleFull, "bk_machine")} {
		if _, err := apiKeys.RedeemCredentialSecret(created.ApprovalRequestID, principal); !errors.Is(err, apperr.ErrCredentialSecretLost) {
			t.Fatalf("重复、他人或机器兑换均应 credential_secret_lost，principal=%+v err=%v", principal, err)
		}
	}
}

func TestAPIKeyApprovalRedeemRejectsNonSucceededRequest(t *testing.T) {
	apiKeys, _, _ := newAPIKeyApprovalTestSuite(t)
	created, err := apiKeys.RequestCreate("ci", model.RoleFull, nil, "需要自动化访问", "alice", "127.0.0.1", "credential-pending", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建审批失败: %v", err)
	}
	if _, err := apiKeys.RedeemCredentialSecret(created.ApprovalRequestID, auth.HumanPrincipal("alice")); !errors.Is(err, apperr.ErrCredentialSecretLost) {
		t.Fatalf("非成功审批不得兑换，实际 %v", err)
	}
}

// TestAPIKeyApprovalEvidenceExcludesSecrets 验证凭据审批证据不含明文或哈希。
func TestAPIKeyApprovalEvidenceExcludesSecrets(t *testing.T) {
	apiKeys, approval, _ := newAPIKeyApprovalTestSuite(t)
	created, err := apiKeys.RequestCreate("ci-evidence", model.RoleFull, nil, "需要自动化访问", "alice", "127.0.0.1", "credential-evidence", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建审批失败: %v", err)
	}
	_, evidence, err := approval.DetailEvidence(created.ApprovalRequestID, auth.HumanPrincipal("bob"))
	if err != nil || evidence.EvidenceStatus != "available" || len(evidence.CurrentFactsSummary) == 0 {
		t.Fatalf("凭据实时证据不符：evidence=%+v err=%v", evidence, err)
	}
	for _, line := range evidence.CurrentFactsSummary {
		if strings.Contains(line.Value, "bk_") || len(line.Value) == 64 {
			t.Fatalf("凭据证据泄露敏感值：%+v", line)
		}
	}
}
