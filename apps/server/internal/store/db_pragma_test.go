package store

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/config"
)

// TestApplySQLitePragmas 守护 sqlite DSN 的 WAL 崩溃韧性注入与 busy_timeout 并发写保护：
// 默认 DSN 追加 journal_mode(WAL) + busy_timeout(5000)；已显式指定 journal_mode 的 DSN 不覆盖；
// 带 ? 的 DSN 用 & 拼接、不带 ? 的用 ? 拼接。
func TestApplySQLitePragmas(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{"裸文件名", "beacon.db", "beacon.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"},
		{"已有查询参数", "beacon.db?cache=shared", "beacon.db?cache=shared&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"},
		{"file URI 无参数", "file:beacon.db", "file:beacon.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"},
		{"file URI 有参数", "file:beacon.db?cache=shared", "file:beacon.db?cache=shared&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"},
		{"已显式 WAL 不覆盖", "beacon.db?_pragma=journal_mode(WAL)", "beacon.db?_pragma=journal_mode(WAL)"},
		{"已显式 DELETE 不覆盖", "beacon.db?_pragma=journal_mode(DELETE)", "beacon.db?_pragma=journal_mode(DELETE)"},
		{"journal_mode 大小写不敏感不覆盖", "beacon.db?_pragma=JOURNAL_MODE(WAL)", "beacon.db?_pragma=JOURNAL_MODE(WAL)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := applySQLitePragmas(c.dsn); got != c.want {
				t.Fatalf("applySQLitePragmas(%q) = %q，期望 %q", c.dsn, got, c.want)
			}
		})
	}
}

// TestApplySQLiteTxLock 守护 sqlite DSN 的事务锁模式注入：默认追加 _txlock=immediate
// （把「读后写」的写锁竞争前移到 BEGIN 处等待，避免 DEFERRED 下升级失败直接 SQLITE_BUSY），
// 已显式指定 _txlock 的 DSN 一律尊重用户配置不覆盖。
func TestApplySQLiteTxLock(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{"裸文件名", "beacon.db", "beacon.db?_txlock=immediate"},
		{"已有查询参数", "beacon.db?_pragma=journal_mode(WAL)", "beacon.db?_pragma=journal_mode(WAL)&_txlock=immediate"},
		{"file URI", "file:beacon.db?cache=shared", "file:beacon.db?cache=shared&_txlock=immediate"},
		{"已显式 immediate 不覆盖", "beacon.db?_txlock=immediate", "beacon.db?_txlock=immediate"},
		{"已显式 deferred 不覆盖", "beacon.db?_txlock=deferred", "beacon.db?_txlock=deferred"},
		{"已显式 exclusive 不覆盖", "beacon.db?_txlock=exclusive", "beacon.db?_txlock=exclusive"},
		{"_txlock 大小写不敏感不覆盖", "beacon.db?_TXLOCK=deferred", "beacon.db?_TXLOCK=deferred"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := applySQLiteTxLock(c.dsn); got != c.want {
				t.Fatalf("applySQLiteTxLock(%q) = %q，期望 %q", c.dsn, got, c.want)
			}
		})
	}
}

// TestSQLiteImmediateTxLockSerializesReadThenWrite 守护「sqlite 多连接池下『先读后写』事务不再因
// 写锁升级失败」这一前提。它是 MaxOpenConns > 1 能够安全放开的必要条件：
//
// 背景：sqlite 默认 DEFERRED 事务在 BEGIN 时不取锁，第一条写语句才把读锁升级为写锁；WAL 下该升级
// **不可等待**——若另一事务已改过该页，升级立即返回 SQLITE_BUSY/BUSY_SNAPSHOT，busy_timeout 完全不生效。
// 实测（MaxOpenConns=4、8 并发各 15 轮共 120 个事务）：默认 DEFERRED 失败 104/120；加 _txlock=immediate 后 0/120。
//
// 本用例走 store.Open（生产路径，自动注入 WAL + busy_timeout + immediate），并发跑「事务内先读后写」，
// 断言零失败——若有人去掉 _txlock 注入，本用例会立即变红。
func TestSQLiteImmediateTxLockSerializesReadThenWrite(t *testing.T) {
	db, err := Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "read_write_race.db"),
		MaxOpenConns: 4, MaxIdleConns: 2, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开 sqlite 失败: %v", err)
	}
	t.Cleanup(func() { Close(db) })

	// 每行一个计数器，各 goroutine 只改自己那一行（与生产的「先查后改」同形）。
	if err := db.Exec("CREATE TABLE counter (id INTEGER PRIMARY KEY, n INTEGER NOT NULL)").Error; err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	const workers = 4
	for i := 1; i <= workers; i++ {
		if err := db.Exec("INSERT INTO counter (id, n) VALUES (?, 0)", i).Error; err != nil {
			t.Fatalf("铺初始行失败: %v", err)
		}
	}

	var failures int64
	var sample atomic.Value
	var wg sync.WaitGroup
	for w := 1; w <= workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				err := db.Transaction(func(tx *gorm.DB) error {
					// 步骤一：读（拿读锁，与生产的「查当前状态」同形）
					var n int64
					if err := tx.Raw("SELECT n FROM counter WHERE id = ?", w).Scan(&n).Error; err != nil {
						return err
					}
					time.Sleep(time.Millisecond) // 拉长读与写之间的窗口，逼出写锁升级竞争
					// 步骤二：写（须升级为写锁）
					return tx.Exec("UPDATE counter SET n = ? WHERE id = ?", n+1, w).Error
				})
				if err != nil {
					atomic.AddInt64(&failures, 1)
					sample.Store(err.Error())
				}
			}
		}(w)
	}
	wg.Wait()

	if failures > 0 {
		t.Fatalf("多连接池下「先读后写」事务应零失败，实际失败 %d 次（示例：%v）——"+
			"sqlite DSN 的 _txlock=immediate 注入可能被移除或覆盖，此时 MaxOpenConns>1 会频繁 SQLITE_BUSY",
			failures, sample.Load())
	}
}
