package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// sqlCountingLogger 统计经 gorm 实际发出的语句条数，用于守护热路径的查询次数不再回退。
// 实现 LogMode 返回自身：否则 gorm 可能换掉 logger，计数就悄悄失效（而测试仍是绿的）。
type sqlCountingLogger struct {
	logger.Interface
	mu    sync.Mutex
	count int
}

func (l *sqlCountingLogger) LogMode(logger.LogLevel) logger.Interface { return l }

func (l *sqlCountingLogger) Trace(context.Context, time.Time, func() (string, int64), error) {
	l.mu.Lock()
	l.count++
	l.mu.Unlock()
}

func (l *sqlCountingLogger) statements() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count
}

func (l *sqlCountingLogger) reset() {
	l.mu.Lock()
	l.count = 0
	l.mu.Unlock()
}

// newBaseTableGateSuite 建一个带 SQL 计数的运行资格测试库，并造好一条 namespace 与 server。
// lifecycle 参数让同一套构造既能验「active 放行」也能验「归档拒绝」。
func newBaseTableGateSuite(t *testing.T, dsn string, lifecycle string) (*gorm.DB, *sqlCountingLogger, *model.Namespace) {
	t.Helper()
	counting := &sqlCountingLogger{Interface: logger.Default.LogMode(logger.Silent)}
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: counting})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Namespace{}, &model.Server{}); err != nil {
		t.Fatalf("迁移运行资格测试表失败: %v", err)
	}
	ns := &model.Namespace{Code: "prod", Name: "生产", Lifecycle: lifecycle}
	if err := db.Create(ns).Error; err != nil {
		t.Fatalf("写 namespace 失败: %v", err)
	}
	if err := db.Create(&model.Server{NamespaceID: ns.ID, ServerID: "game-1", Kind: model.ServerKindBackend}).Error; err != nil {
		t.Fatalf("写 server 失败: %v", err)
	}
	return db, counting, ns
}

// TestEnsureServerActiveForNamespaceQueriesNamespaceOnce 守护心跳热路径的取连接次数。
//
// 每请求只允许 2 条语句（namespace 与 server 各一条 SELECT）：按 code 查出 namespace 后必须直接复用，
// 不得再按主键重读同一行；基础表判存也必须命中缓存、不得逐请求真查 sqlite_master。
// 修复前是 6 条（3 次 HasTable + 3 次 First），压测实测该路径曾占全库连接压力的近一半。
func TestEnsureServerActiveForNamespaceQueriesNamespaceOnce(t *testing.T) {
	db, counting, ns := newBaseTableGateSuite(t, "file:base_gate_once?mode=memory&cache=shared", model.NamespaceLifecycleActive)

	// 首次调用把基础表存在性暖进缓存
	if err := ensureServerActiveForNamespace(db, ns.Code, "game-1"); err != nil {
		t.Fatalf("active namespace 的心跳应放行，实际 %v", err)
	}
	counting.reset()

	if err := ensureServerActiveForNamespace(db, ns.Code, "game-1"); err != nil {
		t.Fatalf("active namespace 的心跳应放行，实际 %v", err)
	}
	if got := counting.statements(); got > 2 {
		t.Fatalf("每请求 DB 语句数 = %d，期望 ≤2（namespace 与 server 各一条 SELECT）；"+
			"说明「按主键重读 namespace」或「逐请求 HasTable」回退了", got)
	}

	// server 行缺失时同样只允许 2 条（缺行放行，不是报错）
	counting.reset()
	if err := ensureServerActiveForNamespace(db, ns.Code, "absent-1"); err != nil {
		t.Fatalf("server 行缺失应放行，实际 %v", err)
	}
	if got := counting.statements(); got > 2 {
		t.Fatalf("server 行缺失时每请求 DB 语句数 = %d，期望 ≤2", got)
	}
}

// TestBaseTableExistsCachedPerDatabase 守护缓存按**数据库句柄**隔离，且正反查询顺序结果一致。
//
// 曾经的缺陷：缓存只按表名全局共享。一旦某库缺 namespace 表的答案进了缓存，
// 其它库的 ensureNamespaceRuntimeActiveByCode 就会判成「表不存在」而直接 return nil，
// 把归档 namespace 静默放行——那比慢查询危险得多。故用两个库、两种顺序各验一遍。
func TestBaseTableExistsCachedPerDatabase(t *testing.T) {
	withTable, _, _ := newBaseTableGateSuite(t, "file:base_gate_has?mode=memory&cache=shared", model.NamespaceLifecycleActive)

	bare, err := gorm.Open(sqlite.Open("file:base_gate_bare?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}

	cases := []struct {
		name  string
		probe func() (hasTable bool, hasNotTable bool)
	}{
		{"先查有表库", func() (bool, bool) {
			return baseTableExists(withTable, &model.Namespace{}), baseTableExists(bare, &model.Namespace{})
		}},
		{"先查无表库", func() (bool, bool) {
			// 刻意先查无表库：证明「不存在」的答案不会被带到另一个库上
			hasNotTable := baseTableExists(bare, &model.Namespace{})
			hasTable := baseTableExists(withTable, &model.Namespace{})
			return hasTable, hasNotTable
		}},
	}
	for _, c := range cases {
		hasTable, hasNotTable := c.probe()
		if !hasTable {
			t.Fatalf("%s：已迁移 namespace 的库必须判到存在", c.name)
		}
		if hasNotTable {
			t.Fatalf("%s：未迁移 namespace 的库必须判到不存在（缓存跨库串味）", c.name)
		}
	}
}

// TestArchivedNamespaceStillRejectedAfterCache 守护「缓存不得把运行资格闸静默放行」。
// 这是上面那个缺陷的端到端表现形式：归档 namespace 必须仍被 ErrNamespaceArchived 拒绝，
// 而不是因为「表不存在」的误判而长驱直入。
func TestArchivedNamespaceStillRejectedAfterCache(t *testing.T) {
	archived, _, _ := newBaseTableGateSuite(t, "file:base_gate_archived?mode=memory&cache=shared", model.NamespaceLifecycleArchived)

	if err := ensureNamespaceRuntimeActiveByCode(archived, "prod"); !errors.Is(err, apperr.ErrNamespaceArchived) {
		t.Fatalf("归档 namespace 必须被拒，实际 %v", err)
	}
	if err := ensureServerActiveForNamespace(archived, "prod", "game-1"); !errors.Is(err, apperr.ErrNamespaceArchived) {
		t.Fatalf("归档 namespace 的心跳必须被拒，实际 %v", err)
	}
}

// TestBaseTableExistsInsideTransaction 守护事务路径：事务不得被缓存（句柄每笔唯一，
// 按它缓存会无界增长），且事务内的资格闸语义与直连一致。
func TestBaseTableExistsInsideTransaction(t *testing.T) {
	db, _, ns := newBaseTableGateSuite(t, "file:base_gate_tx?mode=memory&cache=shared", model.NamespaceLifecycleActive)

	err := db.Transaction(func(tx *gorm.DB) error {
		if !baseTableExists(tx, &model.Namespace{}) {
			t.Error("事务内也必须判到 namespace 存在")
		}
		return ensureServerActiveForNamespace(tx, ns.Code, "game-1")
	})
	if err != nil {
		t.Fatalf("事务内的资格闸应正常放行，实际 %v", err)
	}
}
