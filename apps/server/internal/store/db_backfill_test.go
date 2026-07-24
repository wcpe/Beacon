package store

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestBackfillLegacyAlertStatus 校验存量告警状态回填（FR-157，见 ADR-0064）：
// 加列前的历史行（status 空串 / NULL）回填 resolved；已是 open / acknowledged 的行不动；幂等重跑无副作用。
func TestBackfillLegacyAlertStatus(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:backfill_"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if sqlDB, e := db.DB(); e == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&model.AlertEvent{}); err != nil {
		t.Fatalf("迁移 alert_event 失败: %v", err)
	}

	// 模拟加列后的存量历史行：status 为空串（AutoMigrate 加列 DEFAULT '' 的效果）。
	legacy := &model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning, ServerID: "old", Message: "旧行", Status: ""}
	if err := db.Create(legacy).Error; err != nil {
		t.Fatalf("插入存量行失败: %v", err)
	}
	// 新行：显式 open，不应被回填。
	fresh := &model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelCritical, ServerID: "new", Message: "新行", Status: model.AlertEventStatusOpen}
	if err := db.Create(fresh).Error; err != nil {
		t.Fatalf("插入新行失败: %v", err)
	}
	// 已确认行：不应被回填。
	ack := &model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelInfo, ServerID: "ack", Message: "确认行", Status: model.AlertEventStatusAcknowledged}
	if err := db.Create(ack).Error; err != nil {
		t.Fatalf("插入确认行失败: %v", err)
	}

	if err := backfillLegacyAlertStatus(db); err != nil {
		t.Fatalf("回填失败: %v", err)
	}

	assertStatus := func(id uint, want string) {
		t.Helper()
		var got model.AlertEvent
		if err := db.First(&got, id).Error; err != nil {
			t.Fatalf("回读 %d 失败: %v", id, err)
		}
		if got.Status != want {
			t.Fatalf("行 %d 状态应为 %q，实际 %q", id, want, got.Status)
		}
	}
	assertStatus(legacy.ID, model.AlertEventStatusResolved)
	assertStatus(fresh.ID, model.AlertEventStatusOpen)
	assertStatus(ack.ID, model.AlertEventStatusAcknowledged)

	// 幂等：再跑一次，open / acknowledged 不受影响。
	if err := backfillLegacyAlertStatus(db); err != nil {
		t.Fatalf("二次回填失败: %v", err)
	}
	assertStatus(fresh.ID, model.AlertEventStatusOpen)
	assertStatus(ack.ID, model.AlertEventStatusAcknowledged)
}
