package service

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// newTestSettingsDB 打开内存 sqlite 并迁移 setting + audit_log（不依赖 MySQL/DSN，单测快路）。
// 用 t.Name() 作每测试**独立**内存库（cache=shared 让本测试内多连接共享同一私有库）——
// 不接入全局 file::memory: 共享缓存，避免跨测试共用一个内存库导致事务在 shared-cache 写锁上死锁。
func newTestSettingsDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:settings_" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	// 单连接：sqlite shared-cache 下避免并发写 "table is locked"。
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&model.Setting{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	for _, tbl := range []string{"setting", "audit_log"} {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	return db
}

// newTestSettingsService 装配一个空 store 的 SettingsService（缓存空、读走白名单默认）。
func newTestSettingsService(t *testing.T) (*SettingsService, *gorm.DB) {
	t.Helper()
	db := newTestSettingsDB(t)
	svc, err := NewSettingsService(db, repository.NewSettingRepository(db), repository.NewAuditLogRepository(db))
	if err != nil {
		t.Fatalf("装配设置服务失败: %v", err)
	}
	return svc, db
}

// settingsWith 装配设置服务并把若干 key 直接置入缓存 + store（供消费者测试预设值，绕过校验便于构造）。
func settingsWith(t *testing.T, kv map[string]string) *SettingsService {
	t.Helper()
	svc, _ := newTestSettingsService(t)
	for k, v := range kv {
		meta, ok := settingMetaFor(k)
		if !ok {
			t.Fatalf("预设了白名单外 key %q", k)
		}
		if _, err := svc.repo.Upsert(k, v, meta.valueType); err != nil {
			t.Fatalf("预设 %s 失败: %v", k, err)
		}
		svc.mu.Lock()
		svc.cache[k] = v
		svc.mu.Unlock()
	}
	return svc
}
