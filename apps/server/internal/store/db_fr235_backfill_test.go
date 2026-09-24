package store

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestBackfillMachineRegisteredIdentitySources 校验 FR-235 的存量回填：
// 把「控制面机器注册预置、但来源尚未标记」的身份行改标为 machine_registered，
// 使审批能对其自动让位；同时不得误伤真 agent 身份或其它来源的行。
//
// 判据来源（真机实证 2026-09-24）：FR-235 之前机器注册写入的是
// 「binding_source=admin_assigned + boot_id 为空」的组合，而真 agent 身份注册时强制带 bootId，
// 故该组合唯一指向控制面预置的占位空壳。
func TestBackfillMachineRegisteredIdentitySources(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:backfill_"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&model.AgentIdentity{}); err != nil {
		t.Fatalf("迁移 agent_identity 失败: %v", err)
	}

	seed := func(identityID, status, bootID, source string) *model.AgentIdentity {
		t.Helper()
		row := &model.AgentIdentity{
			IdentityID: identityID, NamespaceID: 1, ServerID: model.NullableServerID("srv-" + identityID),
			Kind: model.ServerKindBackend, Status: status, BootID: bootID, BindingSource: source,
		}
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("插入身份行 %s 失败: %v", identityID, err)
		}
		return row
	}

	// 应被回填：FR-235 前机器注册写入的形态（admin_assigned + 空 boot_id）。
	preplacedEmpty := seed("pre-empty", model.AgentIdentityStatusUnbound, "", model.AgentIdentityBindingSourceAdminAssigned)
	// 应被回填：boot_id 为 NULL 的等价形态（旧库可能未写空串）。
	preplacedNull := seed("pre-null", model.AgentIdentityStatusActive, "", model.AgentIdentityBindingSourceAdminAssigned)
	if err := db.Model(preplacedNull).Update("boot_id", nil).Error; err != nil {
		t.Fatalf("置 boot_id 为 NULL 失败: %v", err)
	}
	// 不应回填：真 agent 身份（admin_assigned 但有 boot_id）。
	realAgent := seed("real-agent", model.AgentIdentityStatusActive, "boot-real", model.AgentIdentityBindingSourceAdminAssigned)
	// 不应回填：其它来源（即使 boot_id 为空，来源不是 admin_assigned）。
	otherSource := seed("other-src", model.AgentIdentityStatusActive, "", model.AgentIdentityBindingSourceLegacyLocal)
	// 不应回填：已标记过的行（幂等：重复启动不再命中）。
	alreadyMarked := seed("already", model.AgentIdentityStatusActive, "", model.AgentIdentityBindingSourceMachineRegistered)

	if err := backfillMachineRegisteredIdentitySources(db); err != nil {
		t.Fatalf("回填机器注册预置来源失败: %v", err)
	}

	assertSource := func(row *model.AgentIdentity, want, note string) {
		t.Helper()
		var got model.AgentIdentity
		if err := db.Where("identity_id = ?", row.IdentityID).First(&got).Error; err != nil {
			t.Fatalf("读取 %s 失败: %v", row.IdentityID, err)
		}
		if got.BindingSource != want {
			t.Fatalf("%s：来源应为 %q，实际 %q", note, want, got.BindingSource)
		}
	}
	assertSource(preplacedEmpty, model.AgentIdentityBindingSourceMachineRegistered, "空串 boot_id 的预置壳")
	assertSource(preplacedNull, model.AgentIdentityBindingSourceMachineRegistered, "NULL boot_id 的预置壳")
	assertSource(realAgent, model.AgentIdentityBindingSourceAdminAssigned, "带 boot_id 的真 agent 身份（不得误标）")
	assertSource(otherSource, model.AgentIdentityBindingSourceLegacyLocal, "非 admin_assigned 来源（不得误标）")
	assertSource(alreadyMarked, model.AgentIdentityBindingSourceMachineRegistered, "已标记行")

	// 幂等：再跑一次结果不变（回填后来源已变，不再命中）。
	if err := backfillMachineRegisteredIdentitySources(db); err != nil {
		t.Fatalf("重复回填失败: %v", err)
	}
	var marked int64
	if err := db.Model(&model.AgentIdentity{}).
		Where("binding_source = ?", model.AgentIdentityBindingSourceMachineRegistered).Count(&marked).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if marked != 3 {
		t.Fatalf("幂等重跑后应有 3 行标记为 machine_registered，实际 %d", marked)
	}
	// 关键回归：真 agent 身份必须仍未被标记，否则审批会对它自动让位 → 削弱 FR-141 防线。
	var stillReal model.AgentIdentity
	if err := db.Where("identity_id = ?", realAgent.IdentityID).First(&stillReal).Error; err != nil {
		t.Fatalf("读取真身份失败: %v", err)
	}
	if stillReal.BindingSource == model.AgentIdentityBindingSourceMachineRegistered {
		t.Fatal("真 agent 身份被误标为 machine_registered —— 会导致审批误让位")
	}
}
