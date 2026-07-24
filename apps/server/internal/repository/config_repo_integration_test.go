//go:build integration

package repository

import (
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/secret"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// TestConfigRepoCountSensitiveMySQL 回归 MySQL 8.0 下 sensitive 保留字的方言转义。
func TestConfigRepoCountSensitiveMySQL(t *testing.T) {
	t.Setenv("BEACON_CONFIG_ENCRYPTION_KEY", "")
	db := testsupport.OpenTestDB(t, "config_repo")
	disabledCipher, err := secret.NewCipher("")
	if err != nil {
		t.Fatalf("创建无密钥加密器失败: %v", err)
	}
	repo := NewConfigItemRepository(db, disabledCipher)

	n, err := repo.CountSensitive()
	if err != nil {
		t.Fatalf("空表统计敏感配置失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("空表敏感配置数应为 0，实际 %d", n)
	}

	item := model.ConfigItem{
		NamespaceCode: "prod", GroupCode: model.GlobalGroupCode, DataID: "secret.yml",
		ScopeLevel: model.ScopeGlobal, Format: "yaml", Content: "password: secret",
		ContentMD5: "x", Version: 1, Enabled: true, Sensitive: true,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("创建敏感配置测试数据失败: %v", err)
	}

	n, err = repo.CountSensitive()
	if err != nil {
		t.Fatalf("统计敏感配置失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("敏感配置数应为 1，实际 %d", n)
	}
}
