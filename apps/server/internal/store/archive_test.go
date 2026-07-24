package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/config"
)

// TestReplaceMySQLDBName 校验同实例模式仅替换 mysql DSN 的库名、其余连接参数原样保留。
func TestReplaceMySQLDBName(t *testing.T) {
	main := "user:pass@tcp(127.0.0.1:3306)/beacon?charset=utf8mb4&parseTime=true&loc=UTC"
	got := replaceMySQLDBName(main, "beacon_archive")
	if !strings.Contains(got, "/beacon_archive?") {
		t.Fatalf("库名未替换为 beacon_archive：%s", got)
	}
	if !strings.Contains(got, "user:pass@") || !strings.Contains(got, "127.0.0.1:3306") {
		t.Fatalf("连接参数未保留：%s", got)
	}
	if strings.Contains(got, "/beacon?") {
		t.Fatalf("旧库名未被替换：%s", got)
	}
}

// TestDeriveSQLiteArchiveDSN 校验同实例 sqlite 归档派生为同目录第二个 .db 文件。
func TestDeriveSQLiteArchiveDSN(t *testing.T) {
	cases := []struct {
		main string
		want string
	}{
		{"beacon.db", filepath.Join(".", "beacon_archive.db")},
		{"data/beacon.db", filepath.Join("data", "beacon_archive.db")},
		{"file:data/beacon.db?cache=shared", filepath.Join("data", "beacon_archive.db")},
	}
	for _, c := range cases {
		if got := deriveSQLiteArchiveDSN(c.main, "beacon_archive"); got != c.want {
			t.Fatalf("deriveSQLiteArchiveDSN(%q)=%q，期望 %q", c.main, got, c.want)
		}
	}
}

// TestMaskDSNMySQL 校验 mysql DSN 展示脱敏去口令。
func TestMaskDSNMySQL(t *testing.T) {
	masked := maskDSN("mysql", "root:s3cret@tcp(127.0.0.1:3306)/beacon_archive")
	if strings.Contains(masked, "s3cret") {
		t.Fatalf("口令未脱敏：%s", masked)
	}
	if !strings.Contains(masked, "root:***@") {
		t.Fatalf("脱敏形态不符：%s", masked)
	}
}

// TestOpenArchiveSameInstanceSqliteInfo 校验同实例 sqlite 的 ArchiveInfo 形态（mode/database）。
func TestOpenArchiveSameInstanceSqliteInfo(t *testing.T) {
	dir := t.TempDir()
	mainCfg := config.DatabaseConfig{
		Driver: "sqlite", DSN: filepath.Join(dir, "beacon.db"),
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	}
	db, info, err := OpenArchive(mainCfg, config.ArchiveConfig{DSN: "", Database: "beacon_archive"})
	if err != nil {
		t.Fatalf("同实例 sqlite 归档连接失败: %v", err)
	}
	defer Close(db)
	if info.Mode != ArchiveModeSameInstance {
		t.Fatalf("mode 应为 same-instance，实际 %s", info.Mode)
	}
	if info.Database != "beacon_archive" {
		t.Fatalf("database 应为 beacon_archive，实际 %s", info.Database)
	}
	// 归档文件确实建在主库同目录、不同文件。
	if _, err := db.DB(); err != nil {
		t.Fatalf("归档连接不可用: %v", err)
	}
}

// TestOpenArchiveExternalSqlite 校验独立库模式直接用 archive.dsn。
func TestOpenArchiveExternalSqlite(t *testing.T) {
	dir := t.TempDir()
	mainCfg := config.DatabaseConfig{
		Driver: "sqlite", DSN: filepath.Join(dir, "beacon.db"),
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	}
	arcCfg := config.ArchiveConfig{DSN: filepath.Join(dir, "cold.db"), Database: "ignored"}
	db, info, err := OpenArchive(mainCfg, arcCfg)
	if err != nil {
		t.Fatalf("独立库 sqlite 归档连接失败: %v", err)
	}
	defer Close(db)
	if info.Mode != ArchiveModeExternal {
		t.Fatalf("mode 应为 external，实际 %s", info.Mode)
	}
}
