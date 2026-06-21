package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"beacon/internal/apikey"
	"beacon/internal/apperr"
	"beacon/internal/model"
	"beacon/internal/repository"
)

// newAPIKeyTestService 用内存 sqlite 装配 APIKeyService（不依赖 MySQL/DSN），迁移 api_key + audit_log。
func newAPIKeyTestService(t *testing.T) (*APIKeyService, *repository.APIKeyRepository, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.APIKey{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	// 关闭连接，避免泄漏 cache=shared 共享内存库连接，使其在测试结束后销毁、不串扰同包其它测试
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	// 清表，避免共享内存库残留串扰
	for _, tbl := range []string{"api_key", "audit_log"} {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	repo := repository.NewAPIKeyRepository(db)
	svc := NewAPIKeyService(db, repo, repository.NewAuditLogRepository(db))
	return svc, repo, db
}

// TestAPIKeyCreateVerify 创建返回明文一次，库内只存哈希（非明文），Verify 通过并给出角色身份。
func TestAPIKeyCreateVerify(t *testing.T) {
	svc, repo, _ := newAPIKeyTestService(t)
	plaintext, key, err := svc.Create("ci-backend", model.RoleReadonly, nil, "admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("创建密钥失败: %v", err)
	}
	if !strings.HasPrefix(plaintext, apikey.Prefix) {
		t.Fatalf("应返回带前缀的明文，实际 %q", plaintext)
	}
	// 库内存哈希、不存明文
	stored, _ := repo.FindActiveByHash(apikey.Hash(plaintext))
	if stored == nil {
		t.Fatal("应能按明文哈希查到密钥")
	}
	if stored.KeyHash == plaintext || strings.Contains(stored.KeyHash, plaintext) {
		t.Fatal("库内 key_hash 绝不应等于/含明文")
	}
	if stored.ID != key.ID {
		t.Fatal("返回记录与库内记录应一致")
	}
	// Verify 通过 → 身份为 apikey:<名称>、角色 readonly
	principal, role, err := svc.Verify(plaintext)
	if err != nil {
		t.Fatalf("Verify 合法密钥应通过，实际 %v", err)
	}
	if principal != "apikey:ci-backend" || role != model.RoleReadonly {
		t.Fatalf("身份/角色不符：principal=%q role=%q", principal, role)
	}
	// 最近使用被更新
	stored, _ = repo.FindActiveByHash(apikey.Hash(plaintext))
	if stored.LastUsedAt == nil {
		t.Fatal("Verify 后最近使用应被更新")
	}
}

// TestAPIKeyVerifyRejectsExpired 过期密钥 Verify 失败（→401）。
func TestAPIKeyVerifyRejectsExpired(t *testing.T) {
	svc, repo, _ := newAPIKeyTestService(t)
	// 直接插入一把过期密钥（绕过 Create 的"过期须在未来"校验）
	plaintext, hash, prefix, _ := apikey.Generate()
	past := time.Now().UTC().Add(-time.Hour)
	if err := repo.Create(&model.APIKey{
		Name: "stale", KeyHash: hash, KeyPrefix: prefix, Role: model.RoleFull, ExpiresAt: &past,
	}); err != nil {
		t.Fatalf("插入过期密钥失败: %v", err)
	}
	if _, _, err := svc.Verify(plaintext); !errors.Is(err, apperr.ErrAdminUnauthorized) {
		t.Fatalf("过期密钥应 ErrAdminUnauthorized，实际 %v", err)
	}
}

// TestAPIKeyVerifyRejectsRevoked 吊销后 Verify 失败（→401）。
func TestAPIKeyVerifyRejectsRevoked(t *testing.T) {
	svc, _, _ := newAPIKeyTestService(t)
	plaintext, key, err := svc.Create("ci", model.RoleFull, nil, "admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("创建密钥失败: %v", err)
	}
	if err := svc.Revoke(key.ID, "admin", "127.0.0.1"); err != nil {
		t.Fatalf("吊销失败: %v", err)
	}
	if _, _, err := svc.Verify(plaintext); !errors.Is(err, apperr.ErrAdminUnauthorized) {
		t.Fatalf("吊销密钥应 ErrAdminUnauthorized，实际 %v", err)
	}
	// 二次吊销不存在 → API_KEY_NOT_FOUND
	if err := svc.Revoke(key.ID, "admin", "127.0.0.1"); !errors.Is(err, apperr.ErrAPIKeyNotFound) {
		t.Fatalf("吊销已吊销密钥应 ErrAPIKeyNotFound，实际 %v", err)
	}
}

// TestAPIKeyResetRotates 重置后旧明文失效、新明文生效（密钥只能重置、不能二次读取）。
func TestAPIKeyResetRotates(t *testing.T) {
	svc, _, _ := newAPIKeyTestService(t)
	old, key, err := svc.Create("ci", model.RoleFull, nil, "admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("创建密钥失败: %v", err)
	}
	fresh, _, err := svc.Reset(key.ID, "admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("重置失败: %v", err)
	}
	if fresh == old {
		t.Fatal("重置应换出新明文")
	}
	if _, _, err := svc.Verify(old); !errors.Is(err, apperr.ErrAdminUnauthorized) {
		t.Fatalf("重置后旧明文应失效，实际 %v", err)
	}
	if _, _, err := svc.Verify(fresh); err != nil {
		t.Fatalf("重置后新明文应生效，实际 %v", err)
	}
	// 重置已吊销 / 不存在的密钥 → API_KEY_NOT_FOUND
	_ = svc.Revoke(key.ID, "admin", "127.0.0.1")
	if _, _, err := svc.Reset(key.ID, "admin", "127.0.0.1"); !errors.Is(err, apperr.ErrAPIKeyNotFound) {
		t.Fatalf("重置已吊销密钥应 ErrAPIKeyNotFound，实际 %v", err)
	}
}

// TestAPIKeyCreateRejectsBadInput 名称空 / 角色非法 / 过期时刻已过 一律 INVALID_PARAM。
func TestAPIKeyCreateRejectsBadInput(t *testing.T) {
	svc, _, _ := newAPIKeyTestService(t)
	past := time.Now().UTC().Add(-time.Minute)
	cases := []struct {
		name, role string
		exp        *time.Time
	}{
		{"", model.RoleFull, nil},
		{"x", "superuser", nil},
		{"x", model.RoleFull, &past},
	}
	for _, c := range cases {
		if _, _, err := svc.Create(c.name, c.role, c.exp, "admin", "127.0.0.1"); !errors.Is(err, apperr.ErrInvalidParam) {
			t.Fatalf("name=%q role=%q 应 ErrInvalidParam，实际 %v", c.name, c.role, err)
		}
	}
}

// TestAPIKeyAuditHasNoSecret 创建/吊销/重置审计落库，且 detail 绝不含明文 / 哈希。
func TestAPIKeyAuditHasNoSecret(t *testing.T) {
	svc, _, db := newAPIKeyTestService(t)
	plaintext, key, err := svc.Create("ci", model.RoleFull, nil, "admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("创建密钥失败: %v", err)
	}
	if _, _, err := svc.Reset(key.ID, "admin", "127.0.0.1"); err != nil {
		t.Fatalf("重置失败: %v", err)
	}
	if err := svc.Revoke(key.ID, "admin", "127.0.0.1"); err != nil {
		t.Fatalf("吊销失败: %v", err)
	}
	var audits []model.AuditLog
	if err := db.Where("target_type = ?", model.TargetTypeAPIKey).Find(&audits).Error; err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	if len(audits) != 3 {
		t.Fatalf("应有 3 条密钥审计（建/重置/吊销），实际 %d", len(audits))
	}
	hash := apikey.Hash(plaintext)
	for _, a := range audits {
		if a.Operator != "admin" {
			t.Fatalf("审计 operator 应为认证身份 admin，实际 %q", a.Operator)
		}
		if strings.Contains(a.Detail, plaintext) || strings.Contains(a.Detail, hash) {
			t.Fatalf("审计 detail 绝不应含明文 / 哈希：%s", a.Detail)
		}
	}
}
