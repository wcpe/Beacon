package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/config"
)

// TestBusyLockNotMisattributedToPoolExhaustion 锁定 P2-5 的归因判据：
// **文件锁竞争（SQLITE_BUSY）不得被报成「等 DB 连接超时（池耗尽）」**。
//
// 为什么这条要单独锁：两者对运维指向完全不同的处置——池耗紧要调 `max-open-conns` / 查长事务，
// 而写锁竞争要看谁在长写、是否该缩短写事务。报错文案若把后者说成前者，排障就会被带偏。
//
// 判别力（本用例在修复前必红，实测）：预算取 300ms、DSN 走生产默认 busy_timeout(5000)，
// 一条写语句撞上未提交的写事务时，等待发生在 SQLite 的 busy 处理里并**等满 5s**——
// 此刻派生 ctx 早已过期，修复前的 `wrapTimeout` 只看「我建的预算到点了」，
// 于是把这条裸 SQLITE_BUSY 翻译成了 DB_WAIT_TIMEOUT（实测耗时 5.01s、带可读中文）。
// 修复后要求：错误里能识别出 SQLITE_BUSY，且**不得**是 ErrDBWaitTimeout。
func TestBusyLockNotMisattributedToPoolExhaustion(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "busy.db")

	// A：长事务持有写锁，且刻意不提交。
	dbA, err := Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: dsn, MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
		CallTimeoutMs: 20000, TxTimeoutMs: 30000,
	})
	if err != nil {
		t.Fatalf("打开 A 失败: %v", err)
	}
	t.Cleanup(func() { Close(dbA) })
	if err := dbA.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)").Error; err != nil {
		t.Fatalf("建表失败: %v", err)
	}

	// B：预算 300ms，远小于 busy_timeout(5000)，故等待由 SQLite 主导而不是由预算主导。
	dbB, err := Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: dsn, MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
		CallTimeoutMs: 300, TxTimeoutMs: 30000,
	})
	if err != nil {
		t.Fatalf("打开 B 失败: %v", err)
	}
	t.Cleanup(func() { Close(dbB) })

	tx := dbA.Begin()
	if err := tx.Exec("INSERT INTO t (v) VALUES ('a')").Error; err != nil {
		t.Fatalf("A 写失败: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	err = dbB.Exec("INSERT INTO t (v) VALUES ('b')").Error
	if err == nil {
		t.Fatal("写冲突应失败，实际成功——A 的写锁未生效，本用例失去判别力")
	}
	if errors.Is(err, ErrDBWaitTimeout) {
		t.Fatalf("文件锁竞争被误报成连接池耗尽（运维会被引向调 max-open-conns，而真因是写锁竞争）: %v", err)
	}
	// 反向断言：错误必须仍可诊断（不能被吞成 nil 或裸空串），且形态可识别为 sqlite 忙。
	if err.Error() == "" {
		t.Fatal("错误文案为空，运维无法据此定位")
	}
	if msg := err.Error(); !strings.Contains(msg, "SQLITE_BUSY") && !strings.Contains(msg, "locked") {
		t.Fatalf("错误形态不可识别为写锁竞争: %v", err)
	}
}
