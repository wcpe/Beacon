package store

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestFR235LiveShapeMigration 用真机实测数据形态验证回填：
// 11 个 CP 预置空壳（admin_assigned + 空 boot_id）+ 11 个真插件身份（admin_assigned + 有 boot_id），
// 与 2026-09-24 真机库完全同构。回填后应恰好命中 11 个空壳，真身份零误伤。
func TestFR235LiveShapeMigration(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:live_"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&model.AgentIdentity{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	servers := []string{
		"bungee-main", "login-01", "login-02",
		"region1-zone1-lobby", "region1-zone1-game1", "region1-zone2-lobby", "region1-zone2-game1",
		"region2-zone1-lobby", "region2-zone1-game1", "region2-zone2-lobby", "region2-zone2-game1",
	}
	for i, sid := range servers {
		// 空壳：CP 推送造，来源 admin_assigned、boot_id 空、状态 unbound（真机形态）
		if err := db.Create(&model.AgentIdentity{
			IdentityID: "pre-" + sid, NamespaceID: 1, ServerID: model.NullableServerID(sid),
			Kind: model.ServerKindBackend, Status: model.AgentIdentityStatusUnbound,
			BootID: "", BindingSource: model.AgentIdentityBindingSourceAdminAssigned,
		}).Error; err != nil {
			t.Fatalf("落空壳失败: %v", err)
		}
		// 真身份：插件注册，来源 admin_assigned（真机实测值！）、boot_id 非空、active
		if err := db.Create(&model.AgentIdentity{
			IdentityID: "real-" + sid, NamespaceID: 1, ServerID: model.NullableServerID(sid),
			Kind: model.ServerKindBackend, Status: model.AgentIdentityStatusActive,
			BootID: "boot-" + string(rune('a'+i)), BindingSource: model.AgentIdentityBindingSourceAdminAssigned,
		}).Error; err != nil {
			t.Fatalf("落真身份失败: %v", err)
		}
	}

	if err := backfillMachineRegisteredIdentitySources(db); err != nil {
		t.Fatalf("回填失败: %v", err)
	}

	var preplacedCount, realAgentCount int64
	if err := db.Model(&model.AgentIdentity{}).Where("binding_source = ?", model.AgentIdentityBindingSourceMachineRegistered).Count(&preplacedCount).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if err := db.Model(&model.AgentIdentity{}).Where("binding_source = ?", model.AgentIdentityBindingSourceAdminAssigned).Count(&realAgentCount).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if preplacedCount != 11 {
		t.Fatalf("应恰好回填 11 个空壳，实际 %d", preplacedCount)
	}
	if realAgentCount != 11 {
		t.Fatalf("11 个真身份应保持 admin_assigned 不动，实际剩余 %d", realAgentCount)
	}
	// 反向确认：真身份未被误标（这是安全边界，错了会让审批误让位）
	var mislabeled model.AgentIdentity
	if err := db.Where("identity_id = ?", "real-login-01").First(&mislabeled).Error; err != nil {
		t.Fatalf("读取真身份失败: %v", err)
	}
	if mislabeled.BindingSource == model.AgentIdentityBindingSourceMachineRegistered {
		t.Fatal("真身份被误标 —— 真机同构数据下安全边界被破坏")
	}
}
