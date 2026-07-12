//go:build integration

package store

import (
	"database/sql"
	"os"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// openBackfillTestDB 在真 MySQL 上建独立测试库并经 store.Open 迁移（含 backfill），返回连接。
// 未设 BEACON_TEST_DSN 则跳过。
func openBackfillTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	raw := os.Getenv("BEACON_TEST_DSN")
	if raw == "" {
		t.Skip("未设置 BEACON_TEST_DSN，跳过集成测试")
	}
	cfg, err := gomysql.ParseDSN(raw)
	if err != nil {
		t.Fatalf("解析 BEACON_TEST_DSN 失败: %v", err)
	}
	target := cfg.DBName + "_storebackfill"
	admin, err := sql.Open("mysql", raw)
	if err != nil {
		t.Fatalf("打开基础连接失败: %v", err)
	}
	_, err = admin.Exec("CREATE DATABASE IF NOT EXISTS `" + target + "`")
	_ = admin.Close()
	if err != nil {
		t.Fatalf("创建测试库 %s 失败: %v", target, err)
	}
	cfg.DBName = target
	db, err := Open(config.DatabaseConfig{
		Driver: "mysql", DSN: cfg.FormatDSN(), MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetimeSec: 300,
	})
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := db.Exec("DELETE FROM alert_event").Error; err != nil {
		t.Fatalf("清表失败: %v", err)
	}
	return db
}

// TestAlertStatusMigrationAndBackfillMySQL 集成（真 MySQL）：验加列迁移生效 + 存量回填为终态 resolved
// 在真 MySQL 一致（可移植、无方言专有 SQL），且对 open / acknowledged 行无副作用、幂等（FR-157，见 ADR-0064）。
func TestAlertStatusMigrationAndBackfillMySQL(t *testing.T) {
	db := openBackfillTestDB(t)

	// 加列迁移：4 个处理字段在真 MySQL 建成。
	for _, col := range []string{"status", "handled_by", "handled_at", "handle_note"} {
		if !db.Migrator().HasColumn(&model.AlertEvent{}, col) {
			t.Fatalf("alert_event 应含处理列 %q（AutoMigrate 加列未生效）", col)
		}
	}

	now := time.Now().UTC()
	// 模拟加列后的存量历史行：status 空串。
	legacy := &model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning, ServerID: "old", Message: "旧行", Status: "", CreatedAt: now}
	// 新 open 行 + 已确认行：不应被回填。
	fresh := &model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelCritical, ServerID: "new", Message: "新行", Status: model.AlertEventStatusOpen, CreatedAt: now}
	ack := &model.AlertEvent{Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelInfo, ServerID: "ack", Message: "确认行", Status: model.AlertEventStatusAcknowledged, CreatedAt: now}
	for _, e := range []*model.AlertEvent{legacy, fresh, ack} {
		if err := db.Create(e).Error; err != nil {
			t.Fatalf("插入 %s 失败: %v", e.ServerID, err)
		}
	}

	// 幂等：连跑两次回填，结果一致。
	for i := 0; i < 2; i++ {
		if err := backfillLegacyAlertStatus(db); err != nil {
			t.Fatalf("第 %d 次回填失败: %v", i+1, err)
		}
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
}
