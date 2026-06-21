package repository

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"beacon/internal/model"
)

// newAPIKeyTestDB 打开内存 sqlite 并迁移 api_key，供仓库单测（不依赖 MySQL/DSN）。
func newAPIKeyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.APIKey{}); err != nil {
		t.Fatalf("迁移 api_key 失败: %v", err)
	}
	if err := db.Exec("DELETE FROM api_key").Error; err != nil {
		t.Fatalf("清表失败: %v", err)
	}
	return db
}

// TestAPIKeyCreateAndFindByHash 建后可按摘要查到，且填了软删哨兵。
func TestAPIKeyCreateAndFindByHash(t *testing.T) {
	repo := NewAPIKeyRepository(newAPIKeyTestDB(t))
	if err := repo.Create(&model.APIKey{Name: "ci", KeyHash: "h1", KeyPrefix: "bk_aaa", Role: model.RoleReadonly}); err != nil {
		t.Fatalf("建密钥失败: %v", err)
	}
	got, err := repo.FindActiveByHash("h1")
	if err != nil {
		t.Fatalf("查密钥失败: %v", err)
	}
	if got == nil || got.Name != "ci" || got.Role != model.RoleReadonly {
		t.Fatalf("应查到 readonly 密钥 ci，实际 %+v", got)
	}
	if !got.DeletedAt.Equal(model.SoftDeleteSentinel) {
		t.Fatalf("未删记录的 deleted_at 应为哨兵，实际 %v", got.DeletedAt)
	}
	// 不存在的摘要返回 (nil, nil)
	if miss, err := repo.FindActiveByHash("nope"); err != nil || miss != nil {
		t.Fatalf("未知摘要应返回 (nil,nil)，实际 (%v,%v)", miss, err)
	}
}

// TestAPIKeyRevokeHidesFromHashLookup 吊销（软删）后按摘要查不到，但仍在 List 中可见。
func TestAPIKeyRevokeHidesFromHashLookup(t *testing.T) {
	repo := NewAPIKeyRepository(newAPIKeyTestDB(t))
	k := &model.APIKey{Name: "ci", KeyHash: "h1", KeyPrefix: "bk_aaa", Role: model.RoleFull}
	if err := repo.Create(k); err != nil {
		t.Fatalf("建密钥失败: %v", err)
	}
	ok, err := repo.Revoke(k.ID, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("吊销应命中，实际 ok=%v err=%v", ok, err)
	}
	// 吊销后认证查不到
	if got, err := repo.FindActiveByHash("h1"); err != nil || got != nil {
		t.Fatalf("吊销后按摘要应查不到，实际 (%v,%v)", got, err)
	}
	// 二次吊销不命中（幂等）
	if ok, _ := repo.Revoke(k.ID, time.Now().UTC()); ok {
		t.Fatal("二次吊销不应命中")
	}
	// 列表仍含已吊销行（供展示状态）
	list, err := repo.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("列表应含 1 条（含已吊销），实际 %d err=%v", len(list), err)
	}
	if !model.IsDeleted(list[0].DeletedAt) {
		t.Fatal("列表中该行应为已吊销")
	}
}

// TestAPIKeyRotateSecret 重置换新摘要、清空最近使用，旧摘要立即失效。
func TestAPIKeyRotateSecret(t *testing.T) {
	repo := NewAPIKeyRepository(newAPIKeyTestDB(t))
	now := time.Now().UTC()
	k := &model.APIKey{Name: "ci", KeyHash: "old", KeyPrefix: "bk_old", Role: model.RoleFull, LastUsedAt: &now}
	if err := repo.Create(k); err != nil {
		t.Fatalf("建密钥失败: %v", err)
	}
	ok, err := repo.RotateSecret(k.ID, "new", "bk_new")
	if err != nil || !ok {
		t.Fatalf("重置应命中，实际 ok=%v err=%v", ok, err)
	}
	// 旧摘要失效
	if got, _ := repo.FindActiveByHash("old"); got != nil {
		t.Fatal("重置后旧摘要应失效")
	}
	// 新摘要生效、最近使用被清空
	got, err := repo.FindActiveByHash("new")
	if err != nil || got == nil {
		t.Fatalf("重置后新摘要应生效，实际 (%v,%v)", got, err)
	}
	if got.KeyPrefix != "bk_new" || got.LastUsedAt != nil {
		t.Fatalf("重置应换前缀并清空最近使用，实际 prefix=%q lastUsed=%v", got.KeyPrefix, got.LastUsedAt)
	}
}

// TestAPIKeyTouchLastUsed 更新最近使用时刻。
func TestAPIKeyTouchLastUsed(t *testing.T) {
	repo := NewAPIKeyRepository(newAPIKeyTestDB(t))
	k := &model.APIKey{Name: "ci", KeyHash: "h1", KeyPrefix: "bk_aaa", Role: model.RoleReadonly}
	if err := repo.Create(k); err != nil {
		t.Fatalf("建密钥失败: %v", err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	if err := repo.TouchLastUsed(k.ID, at); err != nil {
		t.Fatalf("更新最近使用失败: %v", err)
	}
	got, _ := repo.FindActiveByHash("h1")
	if got == nil || got.LastUsedAt == nil || !got.LastUsedAt.Equal(at) {
		t.Fatalf("最近使用应被更新为 %v，实际 %v", at, got.LastUsedAt)
	}
}

// TestAPIKeyFindActiveByID 按主键查未吊销密钥；吊销后查不到。
func TestAPIKeyFindActiveByID(t *testing.T) {
	repo := NewAPIKeyRepository(newAPIKeyTestDB(t))
	k := &model.APIKey{Name: "ci", KeyHash: "h1", KeyPrefix: "bk_aaa", Role: model.RoleFull}
	if err := repo.Create(k); err != nil {
		t.Fatalf("建密钥失败: %v", err)
	}
	if got, err := repo.FindActiveByID(k.ID); err != nil || got == nil {
		t.Fatalf("应按 id 查到，实际 (%v,%v)", got, err)
	}
	_, _ = repo.Revoke(k.ID, time.Now().UTC())
	if got, err := repo.FindActiveByID(k.ID); err != nil || got != nil {
		t.Fatalf("吊销后按 id 应查不到，实际 (%v,%v)", got, err)
	}
}
