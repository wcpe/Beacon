//go:build integration

package repository

import (
	"database/sql"
	"os"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/store"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// openColdITEnv 打开真实 MySQL 热库（beacon_<suffix>）+ 同实例第二库（beacon_archive）作归档库（FR-152 冷查询）。
// 未设 BEACON_TEST_DSN 跳过；归档库经 store.OpenArchive 同实例模式连接（复用主库参数、替换库名）。
func openColdITEnv(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	hot := testsupport.OpenTestDB(t, "coldq")

	raw := os.Getenv("BEACON_TEST_DSN")
	cfg, err := gomysql.ParseDSN(raw)
	if err != nil {
		t.Fatalf("解析 BEACON_TEST_DSN 失败: %v", err)
	}
	archiveName := "beacon_archive"
	admin, err := sql.Open("mysql", raw)
	if err != nil {
		t.Fatalf("打开基础连接失败: %v", err)
	}
	if _, err := admin.Exec("CREATE DATABASE IF NOT EXISTS `" + archiveName + "`"); err != nil {
		_ = admin.Close()
		t.Fatalf("预建归档库 %s 失败: %v", archiveName, err)
	}
	_ = admin.Close()

	cfg.DBName = cfg.DBName + "_coldq" // 与 OpenTestDB 同库
	mainCfg := config.DatabaseConfig{
		Driver: "mysql", DSN: cfg.FormatDSN(), MaxOpenConns: 5, MaxIdleConns: 2, ConnMaxLifetimeSec: 300,
	}
	archive, _, err := store.OpenArchive(mainCfg, config.ArchiveConfig{DSN: "", Database: archiveName})
	if err != nil {
		t.Fatalf("连接同实例归档库失败: %v", err)
	}
	t.Cleanup(func() { store.Close(archive) })

	// 清本测试用当日 conn 日表（两侧），保 -count 多轮幂等、不受上轮残留污染。
	day := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	tbl := store.DailyTableName(model.ConnDetail{}.TableName(), day)
	for _, db := range []*gorm.DB{hot, archive} {
		if db.Migrator().HasTable(tbl) {
			if err := db.Migrator().DropTable(tbl); err != nil {
				t.Fatalf("清 %s 失败: %v", tbl, err)
			}
		}
	}
	return hot, archive
}

// seedConnIT 造一条 open 会话行落指定连接当日表（集成测试）。
func seedConnIT(t *testing.T, db *gorm.DB, connID string, ns uint, openedMs int64) {
	t.Helper()
	if _, err := NewConnDetailRepository(db).FlushDaily(
		[]model.ConnEvent{openEvent(connID, ns, "proxy-1", "p", openedMs)}); err != nil {
		t.Fatalf("造连接数据失败: %v", err)
	}
}

// TestConnColdMergeMySQLIntegration 真 MySQL 双库：冷查询跨热 / 冷有序归并 + 去重保热 + keyset 分页 +
// 默认查询不触归档 + namespace 隔离。验证 keyset / 归并在 MySQL 下同样正确（无跨库 SQL，纯应用层归并）。
func TestConnColdMergeMySQLIntegration(t *testing.T) {
	hot, archive := openColdITEnv(t)
	repo := NewConnDetailRepository(hot)
	repo.SetArchiveDB(archive)

	base := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC).UnixMilli()
	dupID := uuidV7At(base+300, "dup")
	seedConnIT(t, hot, uuidV7At(base+500, "h5"), 1, base+500)
	seedConnIT(t, hot, dupID, 1, base+300)
	seedConnIT(t, hot, uuidV7At(base+100, "h1"), 1, base+100)
	seedConnIT(t, archive, uuidV7At(base+400, "a4"), 1, base+400)
	seedConnIT(t, archive, uuidV7At(base+200, "a2"), 1, base+200)
	seedConnIT(t, archive, dupID, 1, base+300)                     // 两侧同 conn_id：去重保热侧
	seedConnIT(t, hot, uuidV7At(base+150, "n2"), 2, base+150)      // 他 namespace（应被隔离）
	seedConnIT(t, archive, uuidV7At(base+250, "n2a"), 2, base+250) // 他 namespace（应被隔离）

	q := ConnQuery{ServerID: "proxy-1", NamespaceID: 1, FromMs: base - 3600_000, ToMs: base + 3600_000}

	// 全量：去重后 5 条唯一（ns=1），无重复，降序。
	all, _, err := repo.QueryConnectionsCold(q, "", 100)
	if err != nil {
		t.Fatalf("冷查询失败: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("ns=1 去重后应 5 条，实际 %d", len(all))
	}
	seen := map[string]bool{}
	var prev int64 = 1 << 62
	for _, c := range all {
		if c.NamespaceID != 1 {
			t.Fatalf("namespace 隔离被绕过：ns=%d", c.NamespaceID)
		}
		if seen[c.ConnID] {
			t.Fatalf("重复 conn_id: %s", c.ConnID)
		}
		seen[c.ConnID] = true
		if c.OpenedAt.UnixMilli() > prev {
			t.Fatalf("未按 opened_at 降序")
		}
		prev = c.OpenedAt.UnixMilli()
	}

	// keyset 分页：limit=2 逐页拿全 5 条、无重叠无遗漏。
	got := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		rows, next, err := repo.QueryConnectionsCold(q, cursor, 2)
		if err != nil {
			t.Fatalf("分页失败: %v", err)
		}
		for _, r := range rows {
			if got[r.ConnID] {
				t.Fatalf("分页重复返回 %s", r.ConnID)
			}
			got[r.ConnID] = true
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(got) != 5 {
		t.Fatalf("分页应遍历全 5 条，实际 %d", len(got))
	}

	// 默认查询不触归档：归档独有行（a4）不得出现。
	hotRows, _, err := repo.QueryConnections(ConnQuery{
		ServerID: "proxy-1", NamespaceID: 1, FromMs: base - 3600_000, ToMs: base + 3600_000, Limit: 100})
	if err != nil {
		t.Fatalf("默认查询失败: %v", err)
	}
	for _, r := range hotRows {
		if r.ConnID == uuidV7At(base+400, "a4") {
			t.Fatalf("默认查询返回了归档独有行 a4")
		}
	}
	if len(hotRows) != 3 { // 热库 ns=1 三条（h5, dup, h1）
		t.Fatalf("默认查询应仅见热库 ns=1 三条，实际 %d", len(hotRows))
	}
}
