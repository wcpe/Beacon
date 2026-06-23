//go:build integration

package service_test

import (
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/config"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/service"
	"github.com/wcpe/Beacon/internal/testsupport"
)

// newSettingsStack 用真实测试库装配设置服务（迁移 setting + audit_log 由 store.Open 完成）。
func newSettingsStack(t *testing.T) (*service.SettingsService, *gorm.DB) {
	db := testsupport.OpenTestDB(t, "service")
	svc, err := service.NewSettingsService(db, repository.NewSettingRepository(db), repository.NewAuditLogRepository(db))
	if err != nil {
		t.Fatalf("装配设置服务失败: %v", err)
	}
	return svc, db
}

// TestSettingsSeedUpdateConsume 集成验证（真实 MySQL）：首启种子 → 改设置 → 消费侧读到新值（热生效）。
func TestSettingsSeedUpdateConsume(t *testing.T) {
	svc, _ := newSettingsStack(t)

	// 首启种子：用 config 默认填充缺失的热改项。
	cfg := config.Default()
	cfg.Health.TTLSec = 30
	if err := svc.SeedFromConfig(cfg); err != nil {
		t.Fatalf("种子失败: %v", err)
	}
	if got := svc.GetInt(service.SettingHealthTTLSec); got != 30 {
		t.Fatalf("首启种子后 ttl 应为 30，实际 %d", got)
	}

	// 改设置（模拟运维调参）→ 消费侧（GetInt）即读新值。
	if err := svc.Update(service.SettingHealthTTLSec, "45", "alice", "127.0.0.1"); err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if got := svc.GetInt(service.SettingHealthTTLSec); got != 45 {
		t.Fatalf("改设置后消费侧应读 45，实际 %d", got)
	}
}

// TestSettingsReloadFromStore 集成验证：改设置落库后，新建服务从库载入缓存读到改后值（store 为热改项真源）。
func TestSettingsReloadFromStore(t *testing.T) {
	svc, db := newSettingsStack(t)
	if err := svc.SeedFromConfig(config.Default()); err != nil {
		t.Fatalf("种子失败: %v", err)
	}
	if err := svc.Update(service.SettingLogLevel, "DEBUG", "alice", ""); err != nil {
		t.Fatalf("更新 log.level 失败: %v", err)
	}

	// 用同一库新建服务（重启语义）：缓存从库载入，读到改后的 DEBUG。
	svc2, err := service.NewSettingsService(db, repository.NewSettingRepository(db), repository.NewAuditLogRepository(db))
	if err != nil {
		t.Fatalf("二次装配失败: %v", err)
	}
	if got := svc2.GetString(service.SettingLogLevel); got != "DEBUG" {
		t.Fatalf("重启后应从 store 读 DEBUG，实际 %q", got)
	}
}
