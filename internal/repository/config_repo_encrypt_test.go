package repository

import (
	"encoding/base64"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"beacon/internal/model"
	"beacon/internal/secret"
)

// newConfigTestDB 打开独立内存 sqlite 并迁移 config_item，供 at-rest 加密边界单测。
func newConfigTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// 每个测试独占一个内存库（dsn 唯一），避免共享内存串扰
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.ConfigItem{}); err != nil {
		t.Fatalf("迁移 config_item 失败: %v", err)
	}
	return db
}

// testCipher 构造一把启用的测试 cipher。
func testCipher(t *testing.T) *secret.Cipher {
	t.Helper()
	raw := make([]byte, secret.KeyBytes)
	for i := range raw {
		raw[i] = byte(i * 7)
	}
	c, err := secret.NewCipher(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatalf("构造 cipher 失败: %v", err)
	}
	return c
}

// 敏感配置项写库即密文，读出自动解密回明文；DB 列里存的是带前缀的密文。
func TestConfigRepo_SensitiveEncryptedAtRest(t *testing.T) {
	db := newConfigTestDB(t)
	repo := NewConfigItemRepository(db, testCipher(t))

	plain := "redis:\n  password: s3cr3t\n"
	item := &model.ConfigItem{
		NamespaceCode: "prod", GroupCode: model.GlobalGroupCode, DataID: "redis.yml",
		ScopeLevel: model.ScopeGlobal, ScopeTarget: "", Format: "yaml",
		Content: plain, ContentMD5: "x", Version: 1, Enabled: true, Sensitive: true,
	}
	if err := repo.Create(item); err != nil {
		t.Fatalf("创建敏感项失败: %v", err)
	}

	// 直查原始列：应为密文，不含明文
	var rawContent string
	if err := db.Raw("SELECT content FROM config_item WHERE id = ?", item.ID).Scan(&rawContent).Error; err != nil {
		t.Fatalf("直查 content 失败: %v", err)
	}
	if !secret.IsEncrypted(rawContent) {
		t.Fatalf("敏感项 DB 列应为密文，实际 %q", rawContent)
	}
	if rawContent == plain {
		t.Fatal("敏感项不应明文落库")
	}

	// 经仓库读出：自动解密回明文
	got, err := repo.FindByID(item.ID)
	if err != nil || got == nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got.Content != plain {
		t.Fatalf("读出应解密回明文，期望 %q 得 %q", plain, got.Content)
	}
	if !got.Sensitive {
		t.Fatal("读出应保留 Sensitive 标记")
	}
}

// 非敏感配置项不加密：DB 列即明文（与现状一致）。
func TestConfigRepo_NonSensitivePlaintext(t *testing.T) {
	db := newConfigTestDB(t)
	repo := NewConfigItemRepository(db, testCipher(t))

	plain := "app:\n  name: demo\n"
	item := &model.ConfigItem{
		NamespaceCode: "prod", GroupCode: model.GlobalGroupCode, DataID: "app.yml",
		ScopeLevel: model.ScopeGlobal, ScopeTarget: "", Format: "yaml",
		Content: plain, ContentMD5: "x", Version: 1, Enabled: true, Sensitive: false,
	}
	if err := repo.Create(item); err != nil {
		t.Fatalf("创建普通项失败: %v", err)
	}
	var rawContent string
	if err := db.Raw("SELECT content FROM config_item WHERE id = ?", item.ID).Scan(&rawContent).Error; err != nil {
		t.Fatalf("直查失败: %v", err)
	}
	if rawContent != plain {
		t.Fatalf("非敏感项应明文落库，期望 %q 得 %q", plain, rawContent)
	}
	if secret.IsEncrypted(rawContent) {
		t.Fatal("非敏感项不应被加密")
	}
}

// 有效配置候选查询也应解密敏感项（保证 agent 下发链路拿到明文）。
func TestConfigRepo_EffectiveCandidatesDecrypted(t *testing.T) {
	db := newConfigTestDB(t)
	repo := NewConfigItemRepository(db, testCipher(t))

	plain := "redis:\n  password: s3cr3t\n"
	if err := repo.Create(&model.ConfigItem{
		NamespaceCode: "prod", GroupCode: model.GlobalGroupCode, DataID: "redis.yml",
		ScopeLevel: model.ScopeGlobal, ScopeTarget: "", Format: "yaml",
		Content: plain, ContentMD5: "x", Version: 1, Enabled: true, Sensitive: true,
	}); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	cands, err := repo.FindEffectiveCandidates("prod", model.GlobalGroupCode, "", "srv-1")
	if err != nil {
		t.Fatalf("候选查询失败: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("应有 1 个候选，实际 %d", len(cands))
	}
	if cands[0].Content != plain {
		t.Fatalf("候选应解密回明文，期望 %q 得 %q", plain, cands[0].Content)
	}
}

// 无密钥写敏感项应 fail（绝不静默明文落库）。
func TestConfigRepo_SensitiveWithoutKeyFails(t *testing.T) {
	db := newConfigTestDB(t)
	disabled, _ := secret.NewCipher("")
	repo := NewConfigItemRepository(db, disabled)

	err := repo.Create(&model.ConfigItem{
		NamespaceCode: "prod", GroupCode: model.GlobalGroupCode, DataID: "redis.yml",
		ScopeLevel: model.ScopeGlobal, ScopeTarget: "", Format: "yaml",
		Content: "redis:\n  password: s3cr3t\n", ContentMD5: "x", Version: 1, Enabled: true, Sensitive: true,
	})
	if err == nil {
		t.Fatal("无密钥写敏感项应失败")
	}

	// 但非敏感项无密钥也能正常落库
	if err := repo.Create(&model.ConfigItem{
		NamespaceCode: "prod", GroupCode: model.GlobalGroupCode, DataID: "app.yml",
		ScopeLevel: model.ScopeGlobal, ScopeTarget: "", Format: "yaml",
		Content: "app:\n  name: demo\n", ContentMD5: "x", Version: 1, Enabled: true, Sensitive: false,
	}); err != nil {
		t.Fatalf("无密钥写非敏感项不应失败: %v", err)
	}
}

// CountSensitive 报告库中是否存在敏感项（供启动 fail-fast 探测）。
func TestConfigRepo_CountSensitive(t *testing.T) {
	db := newConfigTestDB(t)
	repo := NewConfigItemRepository(db, testCipher(t))

	n, err := repo.CountSensitive()
	if err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("初始应为 0，实际 %d", n)
	}
	if err := repo.Create(&model.ConfigItem{
		NamespaceCode: "prod", GroupCode: model.GlobalGroupCode, DataID: "redis.yml",
		ScopeLevel: model.ScopeGlobal, ScopeTarget: "", Format: "yaml",
		Content: "redis:\n  password: s3cr3t\n", ContentMD5: "x", Version: 1, Enabled: true, Sensitive: true,
	}); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	n, _ = repo.CountSensitive()
	if n != 1 {
		t.Fatalf("应为 1，实际 %d", n)
	}
}
