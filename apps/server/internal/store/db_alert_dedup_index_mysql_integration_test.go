//go:build integration

package store

import (
	"context"
	"database/sql"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// sqlRecorder 是只记录 SQL 语句的 GORM logger，用于在真 MySQL 上取证「换代实际执行了哪条 DDL」。
type sqlRecorder struct {
	stmts []string
}

// LogMode 恒等返回自身：本记录器只关心语句原文，不关心级别。
func (r *sqlRecorder) LogMode(logger.LogLevel) logger.Interface { return r }

// Info / Warn / Error 不记录日志正文（本轮只需 SQL）。
func (r *sqlRecorder) Info(context.Context, string, ...interface{})  {}
func (r *sqlRecorder) Warn(context.Context, string, ...interface{})  {}
func (r *sqlRecorder) Error(context.Context, string, ...interface{}) {}

// Trace 收集每条被执行的 SQL（含耗时与参数占位，仅取 SQL 部分）。
func (r *sqlRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	r.stmts = append(r.stmts, sql)
}

// findByPrefix 返回记录到的所有以 prefix 开头的语句（大小写不敏感的前缀匹配）。
func (r *sqlRecorder) findByPrefix(prefix string) []string {
	out := make([]string, 0, 2)
	for _, s := range r.stmts {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(s)), strings.ToUpper(prefix)) {
			out = append(out, s)
		}
	}
	return out
}

// dedupITDSN 在真 MySQL 上建独立测试库（beacon_<suffix>），复用 BEACON_TEST_DSN 的账号与实例。
// 未设 BEACON_TEST_DSN 则跳过（与 db_backfill_integration_test.go 同约定）。
func dedupITDSN(t *testing.T, suffix string) string {
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
	return cfg.FormatDSN()
}

// openDedupRaw 用裸 gorm 连接测试库（可挂 SQL 记录器），只做连接不做任何迁移。
func openDedupRaw(t *testing.T, dsn string, rec *sqlRecorder) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger:  rec,
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取连接池失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// mysqlIndexColumns 读 MySQL 上某表的「索引名 → 按序排列的列名」（information_schema.statistics）。
// 与 sqlite 的 sqlite_master 取法等价，但走 MySQL 元数据表，列序即 SEQ_IN_INDEX。
func mysqlIndexColumns(t *testing.T, db *gorm.DB, table string) map[string][]string {
	t.Helper()
	var rows []struct {
		IndexName  string
		ColumnName string
	}
	if err := db.Raw(`SELECT INDEX_NAME AS index_name, COLUMN_NAME AS column_name
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?
		ORDER BY INDEX_NAME, SEQ_IN_INDEX`, table).Scan(&rows).Error; err != nil {
		t.Fatalf("读 MySQL 索引列失败: %v", err)
	}
	out := map[string][]string{}
	for _, r := range rows {
		out[r.IndexName] = append(out[r.IndexName], r.ColumnName)
	}
	return out
}

// mysqlColumns 读 MySQL 上某表的列名集合（用于证明换代零删列）。
func mysqlColumns(t *testing.T, db *gorm.DB, table string) []string {
	t.Helper()
	var cols []string
	if err := db.Raw(`SELECT COLUMN_NAME FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, table).Scan(&cols).Error; err != nil {
		t.Fatalf("读 MySQL 列失败: %v", err)
	}
	sort.Strings(cols)
	return cols
}

// logTableShape 打印 alert_event 的表 DDL、索引列序、行列数（断言之外的可读原始证据，便于人工复核
// 与排障时对照；不影响断言）。
func logTableShape(t *testing.T, db *gorm.DB, label string) {
	t.Helper()
	rows, err := db.Raw("SHOW CREATE TABLE alert_event").Rows()
	if err != nil {
		t.Fatalf("SHOW CREATE TABLE 失败: %v", err)
	}
	var table, ddl string
	for rows.Next() {
		if err := rows.Scan(&table, &ddl); err != nil {
			_ = rows.Close()
			t.Fatalf("读 SHOW CREATE TABLE 失败: %v", err)
		}
	}
	_ = rows.Close()
	t.Logf("[%s] SHOW CREATE TABLE alert_event：%s", label, ddl)

	indexes := mysqlIndexColumns(t, db, "alert_event")
	names := make([]string, 0, len(indexes))
	for name := range indexes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Logf("[%s] 索引 %s → %v", label, name, indexes[name])
	}
	var rowCount int64
	if err := db.Raw("SELECT COUNT(*) FROM alert_event").Scan(&rowCount).Error; err != nil {
		t.Fatalf("统计行数失败: %v", err)
	}
	t.Logf("[%s] 行数=%d 列数=%d", label, rowCount, len(mysqlColumns(t, db, "alert_event")))
}

// dedupITSeed 在既有库形态（旧索引 + 两条历史行）上覆盖迁移前后的断言共用数据。
var dedupITSeed = []struct {
	serverID, toStatus, status, message string
}{
	{"s1", "lost", model.AlertEventStatusOpen, "s1 online → lost"},
	{"s2", "offline", model.AlertEventStatusAcknowledged, "s2 lost → offline"},
}

// seedLegacyDedupTable 在测试库上建「既有库形态」：旧模型建表 + 旧收敛索引 + 两条历史行。
func seedLegacyDedupTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	// 反复运行时先清掉本表，保证从旧形态重新开始（DDL 在 MySQL 触发隐式提交，须在事务外）。
	_ = db.Migrator().DropTable("alert_event")
	if err := db.AutoMigrate(&legacyAlertEventDedup{}); err != nil {
		t.Fatalf("建既有库失败: %v", err)
	}
	if cols := mysqlIndexColumns(t, db, "alert_event"); len(cols["idx_alert_event_dedup"]) == 0 {
		t.Fatalf("既有库应有旧收敛索引，实际索引集合 %+v", cols)
	}
	for _, s := range dedupITSeed {
		if err := db.Exec(`INSERT INTO alert_event (type, level, server_id, namespace, message, detail, status, to_status, occurrence_count, handled_by, handle_note)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			model.AlertEventTypeHealthTransition, model.AlertLevelWarning, s.serverID, "prod",
			s.message, `{"address":"10.0.0.1:1"}`, s.status, s.toStatus, 3, "alice", "已确认").Error; err != nil {
			t.Fatalf("写历史行失败: %v", err)
		}
	}
}

// assertDedupRowsIntact 断言两条历史行在换代后一行不丢、字段值原样。
func assertDedupRowsIntact(t *testing.T, db *gorm.DB) {
	t.Helper()
	var rows []model.AlertEvent
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatalf("回读历史行失败: %v", err)
	}
	if len(rows) != len(dedupITSeed) {
		t.Fatalf("升级不得丢数据：应 %d 行，实际 %d", len(dedupITSeed), len(rows))
	}
	for i, want := range dedupITSeed {
		got := rows[i]
		if got.ServerID != want.serverID || got.ToStatus != want.toStatus || got.Status != want.status ||
			got.Message != want.message || got.OccurrenceCount != 3 || got.HandledBy != "alice" || got.HandleNote != "已确认" {
			t.Fatalf("第 %d 行升级后被改写：%+v", i+1, got)
		}
	}
}

// TestAlertEventDedupIndexUpgradeMySQL 在真 MySQL 上复验 sqlite 的收敛索引换代结论（FR-232 恶化链合并）：
//  1. AutoMigrate 在 MySQL 上同样「只增不删、同名不比对列」——新 v2 索引建出、旧索引仍在；
//  2. dropLegacyAlertDedupIndex 生成的 DDL 在 MySQL 合法且确实执行（记录 SQL 取证）；
//  3. 换代后一行不丢、一列不删（to_status 保留）；
//  4. 二次调用幂等无错。
func TestAlertEventDedupIndexUpgradeMySQL(t *testing.T) {
	dsn := dedupITDSN(t, "dedupit")
	rec := &sqlRecorder{}
	db := openDedupRaw(t, dsn, rec)
	seedLegacyDedupTable(t, db)
	logTableShape(t, db, "A 既有旧库（旧收敛索引 + 2 行历史数据）")

	// 步骤 1：只跑 AutoMigrate（不带显式清理），观察 MySQL 上旧索引是否被自动移除。
	if err := db.AutoMigrate(&model.AlertEvent{}); err != nil {
		t.Fatalf("新模型迁移失败: %v", err)
	}
	logTableShape(t, db, "B 仅 AutoMigrate 之后（旧新两索引应共存）")
	afterMigrate := mysqlIndexColumns(t, db, "alert_event")
	if _, keep := afterMigrate["idx_alert_event_dedup"]; !keep {
		t.Fatalf("MySQL 前提与 sqlite 不同：AutoMigrate 竟自行移除了旧收敛索引，可去掉显式清理；实际索引 %+v", afterMigrate)
	}
	if !db.Migrator().HasIndex(&model.AlertEvent{}, "idx_alert_event_dedup_v2") {
		t.Fatalf("AutoMigrate 应在 MySQL 上补建 idx_alert_event_dedup_v2，实际索引 %+v", afterMigrate)
	}
	if got := afterMigrate["idx_alert_event_dedup_v2"]; strings.Join(got, ",") != "server_id,namespace,type" {
		t.Fatalf("新收敛索引列序应为 server_id,namespace,type，实际 %v", got)
	}
	colsBefore := mysqlColumns(t, db, "alert_event")
	assertDedupRowsIntact(t, db)

	// 步骤 2：显式清理旧索引，并取证它实际下发的 SQL。
	if err := dropLegacyAlertDedupIndex(db); err != nil {
		t.Fatalf("MySQL 上清理旧收敛索引失败: %v", err)
	}
	dropped := rec.findByPrefix("DROP INDEX")
	if len(dropped) == 0 {
		t.Fatalf("未记录到 DROP INDEX 语句，实际语句尾部 %v", rec.stmts)
	}
	if !strings.Contains(dropped[0], "idx_alert_event_dedup") || !strings.Contains(dropped[0], "alert_event") {
		t.Fatalf("DROP INDEX 语句未指向旧索引与 alert_event：%s", dropped[0])
	}
	t.Logf("MySQL 换代实际执行：%s", dropped[0])

	// 步骤 3：升级后的库形态断言。
	logTableShape(t, db, "C 显式清理旧索引之后（只剩 v2）")
	indexes := mysqlIndexColumns(t, db, "alert_event")
	if got, still := indexes["idx_alert_event_dedup"]; still {
		t.Fatalf("旧收敛索引应被移除，实际仍在（列 %v）", got)
	}
	if got := indexes["idx_alert_event_dedup_v2"]; strings.Join(got, ",") != "server_id,namespace,type" {
		t.Fatalf("新收敛索引应保留且列序不变，实际 %v", got)
	}
	if !db.Migrator().HasColumn(&model.AlertEvent{}, "to_status") {
		t.Fatal("to_status 列必须保留（不删列，历史方向仍可读）")
	}
	colsAfter := mysqlColumns(t, db, "alert_event")
	if strings.Join(colsBefore, "|") != strings.Join(colsAfter, "|") {
		t.Fatalf("换代不得删列：前 %v / 后 %v", colsBefore, colsAfter)
	}
	assertDedupRowsIntact(t, db)

	// 步骤 4：幂等——已升级库再调一次不得报错、不得再下发 DROP。
	rec.stmts = nil
	if err := dropLegacyAlertDedupIndex(db); err != nil {
		t.Fatalf("二次清理应幂等无错，实际 %v", err)
	}
	if again := rec.findByPrefix("DROP INDEX"); len(again) != 0 {
		t.Fatalf("已升级库上不应再下发 DROP INDEX，实际 %v", again)
	}
}

// TestAlertEventDedupOpenPathMySQL 走**真实生产升级路径** store.Open：从一个旧形态库启动，
// 断言 Open 结束后旧索引被清、新索引就位、数据与列零损失（这条覆盖 Open 里 AutoMigrate → 显式清理的先后顺序）。
func TestAlertEventDedupOpenPathMySQL(t *testing.T) {
	dsn := dedupITDSN(t, "dedupopenit")
	seedDB := openDedupRaw(t, dsn, &sqlRecorder{})
	seedLegacyDedupTable(t, seedDB)
	colsBefore := mysqlColumns(t, seedDB, "alert_event")
	if sqlDB, err := seedDB.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// 真实路径：store.Open 会在 AutoMigrate 之后显式清理旧索引。
	db, err := Open(config.DatabaseConfig{
		Driver: "mysql", DSN: dsn, MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetimeSec: 300,
	})
	if err != nil {
		t.Fatalf("store.Open 在旧形态 MySQL 库上升级失败: %v", err)
	}
	t.Cleanup(func() { Close(db) })

	indexes := mysqlIndexColumns(t, db, "alert_event")
	if _, still := indexes["idx_alert_event_dedup"]; still {
		t.Fatalf("store.Open 之后旧收敛索引应已移除，实际索引 %+v", indexes)
	}
	if got := indexes["idx_alert_event_dedup_v2"]; strings.Join(got, ",") != "server_id,namespace,type" {
		t.Fatalf("store.Open 之后新收敛索引列序应为 server_id,namespace,type，实际 %v", got)
	}
	if colsAfter := mysqlColumns(t, db, "alert_event"); strings.Join(colsBefore, "|") != strings.Join(colsAfter, "|") {
		t.Fatalf("store.Open 换代不得删列：前 %v / 后 %v", colsBefore, colsAfter)
	}
	assertDedupRowsIntact(t, db)

	// 再次 Open：已升级库 / 全新库上清理应为无操作（幂等，且不得把 v2 误删）。
	db2, err := Open(config.DatabaseConfig{
		Driver: "mysql", DSN: dsn, MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetimeSec: 300,
	})
	if err != nil {
		t.Fatalf("二次 store.Open 失败（清理应幂等）: %v", err)
	}
	t.Cleanup(func() { Close(db2) })
	if !db2.Migrator().HasIndex(&model.AlertEvent{}, "idx_alert_event_dedup_v2") {
		t.Fatal("二次 Open 后新收敛索引应保留")
	}
	assertDedupRowsIntact(t, db2)
}

// TestAlertEventDedupIndexFreshInstallMySQL 全新 MySQL 库走 store.Open：只应有 v2 索引，清理函数静默跳过。
func TestAlertEventDedupIndexFreshInstallMySQL(t *testing.T) {
	dsn := dedupITDSN(t, "dedupfreshit")
	db, err := Open(config.DatabaseConfig{
		Driver: "mysql", DSN: dsn, MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetimeSec: 300,
	})
	if err != nil {
		t.Fatalf("全新 MySQL 库 store.Open 失败: %v", err)
	}
	t.Cleanup(func() { Close(db) })
	indexes := mysqlIndexColumns(t, db, "alert_event")
	if _, bad := indexes["idx_alert_event_dedup"]; bad {
		t.Fatalf("全新 MySQL 库不应存在旧收敛索引，实际 %+v", indexes)
	}
	if got := indexes["idx_alert_event_dedup_v2"]; strings.Join(got, ",") != "server_id,namespace,type" {
		t.Fatalf("全新 MySQL 库应建出新收敛索引（列序 server_id,namespace,type），实际 %v", got)
	}
	if err := dropLegacyAlertDedupIndex(db); err != nil {
		t.Fatalf("全新库上清理应为无操作，实际 %v", err)
	}
}
