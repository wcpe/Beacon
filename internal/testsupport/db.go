// Package testsupport 提供集成测试的共享脚手架。
// 为每个测试包分配独立数据库（beacon_<suffix>），避免 go test 并行迁移同库冲突。
package testsupport

import (
	"database/sql"
	"os"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/config"
	"github.com/wcpe/Beacon/internal/store"
)

// 集成测试涉及的可清空表（按外键无关顺序）。
// 含反向抓取受管任务 / agent 命令（FR-58/FR-87）：二表带单实例互斥唯一键，跨测试不清会让活跃任务残留、
// 后续同实例建任务误中 409，故必须随每测试清表。
var resetTables = []string{"config_gray", "config_revision", "config_item", "file_revision", "file_object", "file_override_set_revision", "file_override_set", "reverse_fetch_task", "agent_command", "zone_assignment", "server_drain", "server_offline", "audit_log", "metric_sample", "api_key", "setting", "namespace"}

// OpenTestDB 为某测试包打开独立数据库（beacon_<suffix>），迁移并清表。
// 未设 BEACON_TEST_DSN 则跳过该测试。
func OpenTestDB(t *testing.T, suffix string) *gorm.DB {
	t.Helper()
	raw := os.Getenv("BEACON_TEST_DSN")
	if raw == "" {
		t.Skip("未设置 BEACON_TEST_DSN，跳过集成测试")
	}
	cfg, err := gomysql.ParseDSN(raw)
	if err != nil {
		t.Fatalf("解析 BEACON_TEST_DSN 失败: %v", err)
	}
	target := cfg.DBName + "_" + suffix

	// 先连到基础库创建独立测试库（IF NOT EXISTS 并发安全）
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
	db, err := store.Open(config.DatabaseConfig{
		Driver: "mysql", DSN: cfg.FormatDSN(), MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetimeSec: 300,
	})
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	for _, tbl := range resetTables {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	return db
}
