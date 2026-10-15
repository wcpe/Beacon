package service

import (
	"context"
	"database/sql"
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

// failingConnPool 是可控故障的连接池包装，用来确定性复现「数据库瞬时不可达」。
//
// 为什么需要它：本缓存的真正风险场景不是「表真的不存在」，而是**探测失败**——gorm 的
// Migrator().HasTable 只有 bool 返回、把 error 丢在实现内部，于是「库不可达 / 连接池枯竭 /
// 连接已关闭」与「表不存在」在调用方眼里完全同形（都是 false）。真实网络故障与超时无法稳定复现，
// 故把故障做成开关：
//   - 健康态：转发给真实连接池；
//   - 故障态：转发给一条**已关闭**的连接池（database/sql 对已关闭池的任何查询都直接报错，
//     与平台 / 驱动版本无关），HasTable 内部的 Scan 拿到该错误、count 保持 0，最终返回 false。
//
// 于是可以在同一把 *gorm.DB（也就是同一个缓存键）上先制造故障、再恢复，直接验证
// 「故障期的 false 有没有被固化」——这是「用不存在的表名」验证不了的。
type failingConnPool struct {
	healthy gorm.ConnPool // 健康态转发的真实池
	down    gorm.ConnPool // 故障态转发的已关闭池
	mu      sync.Mutex
	failing bool
}

func (p *failingConnPool) pick() gorm.ConnPool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failing {
		return p.down
	}
	return p.healthy
}

func (p *failingConnPool) setFailing(failing bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failing = failing
}

func (p *failingConnPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return p.pick().PrepareContext(ctx, query)
}

func (p *failingConnPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return p.pick().ExecContext(ctx, query, args...)
}

func (p *failingConnPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return p.pick().QueryContext(ctx, query, args...)
}

func (p *failingConnPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return p.pick().QueryRowContext(ctx, query, args...)
}

// newFailingPoolSuite 建一个「泳道可切换故障」的运行资格测试库（已迁移 namespace / server）。
func newFailingPoolSuite(t *testing.T, dsn string) (*gorm.DB, *failingConnPool) {
	t.Helper()
	healthy, err := sql.Open(sqlite.DriverName, dsn)
	if err != nil {
		t.Fatalf("打开 sqlite 连接池失败: %v", err)
	}
	// 备用池只作为「必然报错」的来源，立即关闭即可：它从不真正拨号（sql.Open 是惰性的），
	// 故不会创建连接、也就不会影响上面的共享内存库。
	down, err := sql.Open(sqlite.DriverName, dsn)
	if err != nil {
		t.Fatalf("打开备用 sqlite 连接池失败: %v", err)
	}
	if err := down.Close(); err != nil {
		t.Fatalf("关闭备用连接池失败: %v", err)
	}

	pool := &failingConnPool{healthy: healthy, down: down}
	db, err := gorm.Open(&sqlite.Dialector{DriverName: sqlite.DriverName, DSN: dsn, Conn: pool}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Namespace{}, &model.Server{}); err != nil {
		t.Fatalf("迁移运行资格测试表失败: %v", err)
	}
	t.Cleanup(func() { _ = healthy.Close() })
	return db, pool
}

// baseTableExistsCacheSize 统计进程缓存的条目数，供测试断言「负答案 / 事务探测没有写缓存」。
// 只看 baseTableExists 的返回值无法区分「每次都真查」与「缓存了 false」，必须直接看缓存。
func baseTableExistsCacheSize() int {
	size := 0
	baseTableExistsCache.Range(func(_, _ any) bool {
		size++
		return true
	})
	return size
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
	resetBaseTableExistsCacheForTest()
	t.Cleanup(resetBaseTableExistsCacheForTest)

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

	// 关键断言：事务内探测必须一条缓存都不写。判定事务靠 db.Statement.ConnPool，
	// 若退回读 db.ConnPool（它始终指向底层池、Begin 不会改它），这里会看到事务句柄被当成
	// 稳定键写进缓存——即注释承诺的「事务不进缓存」是假的。
	if got := baseTableExistsCacheSize(); got != 0 {
		t.Fatalf("事务内探测不得写缓存，实际写入 %d 条；说明事务守卫读错了连接池字段"+
			"（应读 db.Statement.ConnPool，而非始终指向底层池的 db.ConnPool）", got)
	}

	// 直连探测仍应正常缓存，证明上一断言不是因为「整个缓存都不工作」而通过。
	if !baseTableExists(db, &model.Namespace{}) {
		t.Fatal("直连探测应判到 namespace 存在")
	}
	if got := baseTableExistsCacheSize(); got != 1 {
		t.Fatalf("直连探测应写入 1 条正答案缓存，实际 %d 条", got)
	}
}

// TestBaseTableExistsFailureNotCached 锁定 M1：探测失败（false）不得写进进程缓存。
//
// 为什么这是本缓存最危险的缺陷：HasTable 只有 bool、无 error 通道，实现内部丢弃错误，
// 于是「库不可达 / 连接池枯竭 / 连接已关闭」与「表不存在」同形。而本缓存是进程级且不过期的，
// 一旦把故障期的 false 固化，四条 fail-open 的运行资格闸
// （server_lifecycle.go 中 `!baseTableExists(...) → return nil`）会在本进程余下生命周期里
// 静默放行归档 namespace / server，只能靠重启恢复。
//
// 手法：同一把 *gorm.DB（同一个缓存键）上先制造「库不可达」——连接池被包装成可切换泳道，
// 故障态全部转发给一条已关闭的连接池，HasTable 由此返回 false；随后恢复健康，再次探测
// 必须重新真查并得到 true。若故障期的 false 被误缓存，第二次会直接命中缓存返回 false，
// 断言立刻失败。用「表不存在」构造不出这个场景：那两种 false 必须可区分，才谈得上「不缓存失败」。
func TestBaseTableExistsFailureNotCached(t *testing.T) {
	resetBaseTableExistsCacheForTest()
	t.Cleanup(resetBaseTableExistsCacheForTest)

	db, pool := newFailingPoolSuite(t, "file:base_gate_failure?mode=memory&cache=shared")

	// 故障期：库不可达，探测只能拿到 false
	pool.setFailing(true)
	if baseTableExists(db, &model.Namespace{}) {
		t.Fatal("库不可达时探测应返回 false（HasTable 无 error 通道，故障与缺表同形）")
	}
	if got := baseTableExistsCacheSize(); got != 0 {
		t.Fatalf("探测失败的负答案不得写缓存，实际写入 %d 条", got)
	}

	// 恢复期：同一个缓存键（同一把 DB 句柄）上必须重新真查，而不是沿用故障期的 false
	pool.setFailing(false)
	if !baseTableExists(db, &model.Namespace{}) {
		t.Fatal("库恢复后应重新真查并判到 namespace 存在——" +
			"判成不存在说明故障期的 false 被固化进了永不过期的进程缓存")
	}
	if got := baseTableExistsCacheSize(); got != 1 {
		t.Fatalf("恢复后确认存在应写入 1 条正答案缓存，实际 %d 条", got)
	}

	// 正答案仍须缓存：热路径优化不能被本次修复削弱（否则每次心跳都真查 sqlite_master）。
	pool.setFailing(true)
	if !baseTableExists(db, &model.Namespace{}) {
		t.Fatal("已确认存在的表即使库暂时不可达也应命中缓存放行（存在是静态事实）")
	}
}

// TestBaseTableExistsAbsentTableRecheckedThenConverges 锁定 M1 的另一半：表确实不存在时逐次复检，
// 建表后立刻收敛到 true。
//
// 这是「只缓存 true」换来的一条正向价值：表存在性从「不存在」变成「存在」也能被及时看到
// （启动期 AutoMigrate 补齐之前若有请求先探到不存在，补表之后无需重启即可恢复运行资格）。
func TestBaseTableExistsAbsentTableRecheckedThenConverges(t *testing.T) {
	resetBaseTableExistsCacheForTest()
	t.Cleanup(resetBaseTableExistsCacheForTest)

	db, err := gorm.Open(sqlite.Open("file:base_gate_absent?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}

	// 表尚未迁移：连续两次都必须如实判到不存在，且不留下任何缓存条目
	for i := 1; i <= 2; i++ {
		if baseTableExists(db, &model.Namespace{}) {
			t.Fatalf("第 %d 次探测：表不存在时必须返回 false", i)
		}
	}
	if got := baseTableExistsCacheSize(); got != 0 {
		t.Fatalf("表不存在不得留下缓存条目，实际 %d 条", got)
	}

	// 建表后必须立刻可见（若「不存在」被缓存，此处会继续判到不存在、资格闸被永久 fail-open）
	if err := db.AutoMigrate(&model.Namespace{}); err != nil {
		t.Fatalf("迁移 namespace 表失败: %v", err)
	}
	if !baseTableExists(db, &model.Namespace{}) {
		t.Fatal("建表后应判到存在——判成不存在说明「表不存在」被缓存钉死了")
	}
}
