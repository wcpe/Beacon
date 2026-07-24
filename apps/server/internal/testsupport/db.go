// Package testsupport 提供集成测试的共享脚手架。
// 为每个测试包分配独立数据库（beacon_<suffix>），避免 go test 并行迁移同库冲突。
package testsupport

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/store"
)

// 集成测试涉及的可清空表（按外键无关顺序）。
// 含反向抓取受管任务 / agent 命令（FR-58/FR-87）：二表带单实例互斥唯一键，跨测试不清会让活跃任务残留、
// 后续同实例建任务误中 409，故必须随每测试清表。
var resetTables = []string{"config_layer_version", "config_file", "config_gray", "config_revision", "config_item", "file_revision", "file_object", "file_override_set_revision", "file_override_set", "file_asset", "file_asset_scan", "reverse_fetch_task", "reverse_fetch_ignore_rule", "agent_command", "file_sync_log", "file_sync_target", "file_sync_batch", "file_sync_task", "change_target", "change_batch", "change_order_item", "change_order", "delivery_blob", "zone_assignment", "server_drain", "server_offline", "audit_log", "alert_event", "metric_sample", "api_key", "setting", "reversible_operation", "agent_identity", "server", "zone", "region", "bc_cluster", "env_namespace", "env", "namespace_trust", "namespace", "health_weights_rev"}

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
	// 集成套件在 CI 上会并行打开多个包库；池宜小、生命周期短，避免 MySQL 151 连接上限被 idle 占满。
	db, err := store.Open(config.DatabaseConfig{
		Driver: "mysql", DSN: cfg.FormatDSN(), MaxOpenConns: 2, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	// 用例结束必须关池：否则并行 package 测试会累积连接直至 Error 1040。
	t.Cleanup(func() { store.Close(db) })
	for _, tbl := range resetTables {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	dropDailyTables(t, db)
	return db
}

// dropDailyTables 清掉全部残留日表（<base>_YYYYMMDD，命名规则见 store.DailyTableName）。
// resetTables 静态白名单管不到动态日表；残留日表会跨运行污染行数敏感断言
// （如查询窗覆盖到历史测试固定日期种子表）。用 GORM Migrator.GetTables 枚举
// （portable，非方言专有 SQL），凡带 8 位日期后缀的表一律 Drop。
func dropDailyTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	all, err := db.Migrator().GetTables()
	if err != nil {
		t.Fatalf("枚举测试库表失败: %v", err)
	}
	for _, tbl := range all {
		if !hasDailySuffix(tbl) {
			continue
		}
		if err := db.Migrator().DropTable(tbl); err != nil {
			t.Fatalf("清残留日表 %s 失败: %v", tbl, err)
		}
	}
}

// hasDailySuffix 判表名是否带 _YYYYMMDD 日期后缀（与 store.DailyTableName 命名规则一致）。
func hasDailySuffix(table string) bool {
	i := strings.LastIndexByte(table, '_')
	if i < 0 || len(table)-i-1 != 8 {
		return false
	}
	_, err := time.ParseInLocation("20060102", table[i+1:], time.UTC)
	return err == nil
}
