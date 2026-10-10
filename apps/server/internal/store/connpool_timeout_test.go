package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// —— 连接等待防护（生产 P0 2026-10-10 的「快速失败」半环）——
//
// 生产事故的形态是「持锁等连接 × 持连接等锁」互等；`max-open-conns=1` 下无第三条连接可破环，
// 且 `db.Transaction(func(tx *gorm.DB) error {...})` 不传 ctx（gorm 默认 context.Background()），
// 于是等待**永不结束**，所有需 DB 的端点挂起 25 分钟。
//
// 本组用例锁定「等连接不再无限挂起」这条性质，并逐条防住三类易错实现：
//   - 退化成一阶段预算（用短预算管事务寿命）→ 长事务被误杀，见 TestConnPoolLongTransactionNotKilled；
//   - 覆盖调用方自带期限 → 语义被篡改，见 TestConnPoolDoesNotOverrideCallerDeadline；
//   - 装配点错误（Open 后只赋 db.ConnPool）→ 防护形同虚设，见 TestConnPoolCoversAllGORMPaths；
//   - 忘记归还 db.Conn 抢来的连接 → 池缓慢枯竭，见 TestConnPoolNoConnectionLeak。

// connPoolFixture 是一套「池上限可控」的测试库：用最少的连接数放大争用，
// 从而在小体量下就能观察到「池被占满」的行为。
type connPoolFixture struct {
	db    *gorm.DB
	sqlDB *sql.DB
}

// openConnPoolFixture 按给定池上限与预算打开一个 sqlite 库（走与生产同一套 store.Open 装配）。
func openConnPoolFixture(t *testing.T, name string, maxConns, callMs, txMs int) *connPoolFixture {
	t.Helper()
	db, err := Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: filepath.Join(t.TempDir(), name+".db"),
		MaxOpenConns: maxConns, MaxIdleConns: maxConns, ConnMaxLifetimeSec: 60,
		CallTimeoutMs: callMs, TxTimeoutMs: txMs,
	})
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	db.Logger = db.Logger.LogMode(logger.Silent)
	t.Cleanup(func() { Close(db) })
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取底层池失败: %v", err)
	}
	return &connPoolFixture{db: db, sqlDB: sqlDB}
}

// occupySoleConnection 占住唯一连接，模拟「有长事务/持锁方攥着连接」的生产形态。
// 返回释放函数；调用方须在断言后调用（defer）。
func (f *connPoolFixture) occupySoleConnection(t *testing.T) func() {
	t.Helper()
	conn, err := f.sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("占用连接失败: %v", err)
	}
	if inUse := f.sqlDB.Stats().InUse; inUse < 1 {
		t.Fatalf("占用后应有连接在途，实际 db_in_use=%d", inUse)
	}
	return func() { _ = conn.Close() }
}

// TestConnPoolQueryFailsFastWhenPoolExhausted 是本防护的核心断言：
// 池被占满时，一条**不带 ctx** 的裸查询必须在预算内失败，并给出可诊断原因——
// 修复前它会永久挂起（这正是生产事故中所有 DB 端点挂起的形态）。
//
// 判别力：若把包装层去掉（或装配点写错导致语句绕过它），本用例会在「耗时」上暴露——
// 查询永不返回，用例只能靠 testing 超时收场。
func TestConnPoolQueryFailsFastWhenPoolExhausted(t *testing.T) {
	f := openConnPoolFixture(t, "fastfail", 1, 500, 60000)
	release := f.occupySoleConnection(t)
	defer release()

	var one model.AuditLog
	start := time.Now()
	err := f.db.First(&one).Error
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("池被占满时查询不应成功")
	}
	// 必须「准时」失败：既不能永久挂起（上界），也不该秒回（下界能证明它真的等待过预算）。
	if elapsed > 5*time.Second {
		t.Fatalf("等连接应受预算约束，实际耗时 %v（疑似未套用预算或预算未生效）", elapsed)
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("池被占满时应先等待预算耗尽，实际 %v 失败（疑似误判为其他错误）", elapsed)
	}
	// 错误须可诊断：既是「超时」，也带可读中文原因与独立的业务码。
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误应可被 errors.Is(context.DeadlineExceeded) 识别，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "等 DB 连接超时") {
		t.Fatalf("错误文案应可读并指向连接池，实际 %q", err.Error())
	}
	if !strings.Contains(err.Error(), dbWaitTimeoutCode) {
		t.Fatalf("错误应带业务码 %s 供前端与运维区分，实际 %q", dbWaitTimeoutCode, err.Error())
	}
}

// TestConnPoolTransactionFailsFastWhenPoolExhausted 验证事务路径同样受约束。
// 事务是**最常见**的调用形态，若不覆盖则防护基本无效。
func TestConnPoolTransactionFailsFastWhenPoolExhausted(t *testing.T) {
	f := openConnPoolFixture(t, "fastfail_tx", 1, 500, 60000)
	release := f.occupySoleConnection(t)
	defer release()

	start := time.Now()
	err := f.db.Transaction(func(tx *gorm.DB) error {
		return tx.Create(&model.AuditLog{NamespaceCode: "ns", Operator: "o", Action: model.ActionConfigPublish,
			TargetType: model.TargetTypeConfig, TargetRef: "r", Result: model.ResultOK}).Error
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("池被占满时事务不应成功")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("事务等连接应受预算约束，实际耗时 %v", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("事务超时应保留超时判据，实际 %v", err)
	}
}

// TestConnPoolLongTransactionNotKilled 是「两阶段」的判别性用例。
//
// 事务睡到超过**取连接预算**但仍在事务寿命预算内，必须成功提交。
// 若被退化成一阶段（用同一个短预算管事务寿命），事务会在睡眠中途被 database/sql 的
// awaitDone 回滚而失败——本用例即失败。
//
// 现实对应：本仓最长事务是文件批量导入 2000 文件（实测 8.5s），远超人能接受的等待预算，
// 故「等连接」与「事务寿命」必须分层给值。
func TestConnPoolLongTransactionNotKilled(t *testing.T) {
	// 取连接预算 200ms（很短），事务寿命预算 5s（充裕）。
	f := openConnPoolFixture(t, "longtx", 2, 200, 5000)

	start := time.Now()
	err := f.db.Transaction(func(tx *gorm.DB) error {
		// 模拟长事务：睡足 1.2s，远超 200ms 的取连接预算。
		time.Sleep(1200 * time.Millisecond)
		return tx.Create(&model.AuditLog{NamespaceCode: "ns", Operator: "o", Action: model.ActionConfigPublish,
			TargetType: model.TargetTypeConfig, TargetRef: "r", Result: model.ResultOK}).Error
	})
	if err != nil {
		t.Fatalf("长事务在事务寿命预算内应成功，实际失败（疑似把取连接预算误用到事务寿命上）: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 1200*time.Millisecond {
		t.Fatalf("事务应实际执行到睡眠结束，实际 %v", elapsed)
	}
}

// TestConnPoolDoesNotOverrideCallerDeadline 锁定「不覆盖调用方已有 deadline」这条硬约束。
//
// 调用方带来更紧的期限（如请求级 300ms）时，失败必须归因于**调用方自己的期限**，
// 而不是被报成「连接池耗尽」——否则排障方向会被误导到池容量上。
func TestConnPoolDoesNotOverrideCallerDeadline(t *testing.T) {
	// 本层预算给足 10s，若被误用则会等 10s；调用方期限只有 300ms。
	f := openConnPoolFixture(t, "caller_dl", 1, 10000, 30000)
	release := f.occupySoleConnection(t)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	var one model.AuditLog
	err := f.db.WithContext(ctx).First(&one).Error
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("池被占满时查询不应成功")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("应尊重调用方 300ms 期限，实际耗时 %v（疑似用本层预算覆盖了调用方期限）", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应保留调用方期限的超时判据，实际 %v", err)
	}
	if strings.Contains(err.Error(), dbWaitTimeoutCode) {
		t.Fatalf("调用方自带期限到期不应报成连接池问题（会误导排障），实际 %q", err.Error())
	}
}

// TestConnPoolDoesNotExtendCallerDeadlineForTransaction 验证事务路径也不放宽调用方期限：
// 调用方给 300ms，则事务的整体失败应在 300ms 量级，而不是被本层的长事务预算拖到数十秒。
func TestConnPoolDoesNotExtendCallerDeadlineForTransaction(t *testing.T) {
	f := openConnPoolFixture(t, "caller_dl_tx", 1, 10000, 30000)
	release := f.occupySoleConnection(t)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := f.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Create(&model.AuditLog{NamespaceCode: "ns", Operator: "o", Action: model.ActionConfigPublish,
			TargetType: model.TargetTypeConfig, TargetRef: "r", Result: model.ResultOK}).Error
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("池被占满时事务不应成功")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("事务应尊重调用方 300ms 期限，实际耗时 %v", elapsed)
	}
}

// TestConnPoolNoConnectionLeak 验证两阶段取连接不会漏还连接。
//
// 为什么单独测：两阶段用 `db.Conn(ctx)` 抢连接，该连接与 `*sql.Tx` 的生命周期**不同步**——
// 若只提交/回滚事务而不 Close 那条 Conn，连接会被永久占住，池逐步枯竭直到服务不可用。
// 这里跑满多轮事务（含失败路径）后断言无在途连接。
func TestConnPoolNoConnectionLeak(t *testing.T) {
	f := openConnPoolFixture(t, "noleak", 2, 5000, 30000)

	for i := 0; i < 30; i++ {
		if err := f.db.Transaction(func(tx *gorm.DB) error {
			return tx.Create(&model.AuditLog{NamespaceCode: "ns", Operator: "o", Action: model.ActionConfigPublish,
				TargetType: model.TargetTypeConfig, TargetRef: "r", Result: model.ResultOK}).Error
		}); err != nil {
			t.Fatalf("第 %d 轮事务失败: %v", i, err)
		}
	}
	// 显式失败路径：事务体内返回错误 → gorm 走 Rollback，连接同样必须归还。
	for i := 0; i < 10; i++ {
		boom := errors.New("故意失败以走回滚路径")
		if err := f.db.Transaction(func(*gorm.DB) error { return boom }); !errors.Is(err, boom) {
			t.Fatalf("第 %d 轮回滚事务应透传错误，实际 %v", i, err)
		}
	}

	if st := f.sqlDB.Stats(); st.InUse != 0 {
		t.Fatalf("事务结束后不应有在途连接（连接未归还），实际 InUse=%d", st.InUse)
	}
	if st := f.sqlDB.Stats(); st.OpenConnections > 2 {
		t.Fatalf("连接数不应超过池上限 2，实际 OpenConnections=%d", st.OpenConnections)
	}
	// 收尾证明池仍可用（未被耗干）。
	if err := f.db.Transaction(func(*gorm.DB) error { return nil }); err != nil {
		t.Fatalf("多轮事务后池应仍可用，实际 %v", err)
	}
}

// countingPool 记录被调用的方法次数，用来证明「语句真的经过了包装层」。
type countingPool struct {
	inner gorm.ConnPool
	// begin 计数两阶段入口被走到（= gorm 选中了 ConnPoolBeginner 而非 TxBeginner）。
	begin, stmt atomic.Int64
}

func (c *countingPool) PrepareContext(ctx context.Context, q string) (*sql.Stmt, error) {
	c.stmt.Add(1)
	return c.inner.PrepareContext(ctx, q)
}

func (c *countingPool) ExecContext(ctx context.Context, q string, a ...any) (sql.Result, error) {
	c.stmt.Add(1)
	return c.inner.ExecContext(ctx, q, a...)
}

func (c *countingPool) QueryContext(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	c.stmt.Add(1)
	return c.inner.QueryContext(ctx, q, a...)
}

func (c *countingPool) QueryRowContext(ctx context.Context, q string, a ...any) *sql.Row {
	c.stmt.Add(1)
	return c.inner.QueryRowContext(ctx, q, a...)
}

// BeginTx 返回 gorm.ConnPool，使本类型**只**满足 ConnPoolBeginner（与生产包装层同形）。
func (c *countingPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	c.begin.Add(1)
	beginner, ok := c.inner.(gorm.ConnPoolBeginner)
	if !ok {
		return nil, errors.New("被包装层未实现 gorm.ConnPoolBeginner")
	}
	return beginner.BeginTx(ctx, opts)
}

func (c *countingPool) Ping() error                 { return c.inner.(interface{ Ping() error }).Ping() }
func (c *countingPool) GetDBConn() (*sql.DB, error) { return c.inner.(gorm.GetDBConnector).GetDBConn() }

// TestConnPoolCoversAllGORMPaths 锁定**装配点**，是本组最容易回归的一条。
//
// 为什么要专门锁：把包装层写在 gorm.Open **之后**（`db.ConnPool = wrapper`）看起来很自然，
// 但实测包装层命中 0 次——gorm.Open 末尾会把 `db.ConnPool` 固化进 `db.Statement.ConnPool`，
// 而后续 getInstance() 取的是语句级字段，业务语句仍旧走原池，防护形同虚设且**不留任何迹象**。
// 故必须经 dialector 的 Conn 字段注入，让 gorm 自己写入两个字段。
func TestConnPoolCoversAllGORMPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.db")
	dsn := applySQLiteTxLock(applySQLitePragmas(path))
	raw, err := sql.Open(sqlite.DriverName, dsn)
	if err != nil {
		t.Fatalf("打开原生池失败: %v", err)
	}
	raw.SetMaxOpenConns(2)
	raw.SetMaxIdleConns(2)
	pool := &countingPool{inner: newTimeoutConnPool(raw, 5000, 30000)}

	db, err := gorm.Open(&sqlite.Dialector{DriverName: sqlite.DriverName, DSN: dsn, Conn: pool},
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开 gorm 失败: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	// 每条代表一类调用路径；断言整体命中数而非逐条，避免把测试绑死在 gorm 内部实现细节上。
	if err := db.AutoMigrate(&model.AuditLog{}); err != nil {
		t.Fatalf("AutoMigrate 失败: %v", err)
	}
	row := &model.AuditLog{NamespaceCode: "ns", Operator: "o", Action: model.ActionConfigPublish,
		TargetType: model.TargetTypeConfig, TargetRef: "r", Result: model.ResultOK}
	_ = db.Create(row).Error
	var one model.AuditLog
	_ = db.First(&one, row.ID).Error
	_ = db.Model(&model.AuditLog{}).Where("id = ?", row.ID).Update("operator", "o2").Error
	_ = db.Transaction(func(tx *gorm.DB) error { return tx.Create(&model.AuditLog{NamespaceCode: "ns"}).Error })
	_ = db.Raw("SELECT 1").Scan(&one).Error
	_ = db.Session(&gorm.Session{}).Create(&model.AuditLog{NamespaceCode: "ns"}).Error
	_ = db.WithContext(context.Background()).Create(&model.AuditLog{NamespaceCode: "ns"}).Error
	// Rows 路径（流式读取）：确认包了一层后行集迭代仍可用且错误可控。
	if err := func() error {
		rows, e := db.Model(&model.AuditLog{}).Rows()
		if e != nil {
			return e
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
		}
		return rows.Err()
	}(); err != nil {
		t.Fatalf("Rows 迭代失败: %v", err)
	}
	// 嵌套事务（savepoint）：确认包了一层后嵌套路径仍然可用。
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&model.AuditLog{NamespaceCode: "outer"}).Error; err != nil {
			return err
		}
		return tx.Transaction(func(tx2 *gorm.DB) error { return tx2.Create(&model.AuditLog{NamespaceCode: "inner"}).Error })
	}); err != nil {
		t.Fatalf("嵌套事务失败: %v", err)
	}

	if pool.stmt.Load() == 0 && pool.begin.Load() == 0 {
		t.Fatal("包装层零命中：语句绕过了包装层（装配点未生效，防护形同虚设）")
	}
	// 事务必须真的走 ConnPoolBeginner（两阶段入口）；若为 0 说明 gorm 选中了别的分支，
	// 事务将退回单预算语义、长事务有被误杀风险。
	if pool.begin.Load() == 0 {
		t.Fatal("事务未经过包装层的两阶段入口（BeginTx 零命中）")
	}
	// AutoMigrate 也应被覆盖（它在 gorm.Open 之后、装配完成前就执行）。
	if pool.stmt.Load() < 5 {
		t.Fatalf("包装层命中过少（%d），疑似仅部分路径覆盖", pool.stmt.Load())
	}
}

// TestNewTimeoutConnPoolBudgetSemantics 锁定预算的取值语义（安全默认）：
// 0 = 取内置默认、正值 = 原样采用、负值 = 显式关闭该预算。
//
// 为什么 0 必须表示「取默认」而不是「关闭」：本层是 P0 死锁的兜底防护，而 DatabaseConfig 可在代码里
// 手工构造（测试夹具、诊断脚本、旧的调用点），一旦「漏填字段」等于「静默失去防护」，防护就会在最需要
// 它的场合消失且不留迹象。故关闭动作必须显式写负值，是有意为之。
func TestNewTimeoutConnPoolBudgetSemantics(t *testing.T) {
	raw, err := sql.Open(sqlite.DriverName, filepath.Join(t.TempDir(), "budget.db"))
	if err != nil {
		t.Fatalf("打开原生池失败: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	t.Run("零值取内置默认（不关闭）", func(t *testing.T) {
		got := newTimeoutConnPool(raw, 0, 0)
		pool, ok := got.(*timeoutConnPool)
		if !ok {
			t.Fatalf("零值应取默认预算而非关闭防护，实际类型 %T", got)
		}
		if pool.call != time.Duration(DefaultCallTimeoutMs)*time.Millisecond {
			t.Fatalf("call 预算应为默认 %dms，实际 %v", DefaultCallTimeoutMs, pool.call)
		}
		if pool.tx != time.Duration(DefaultTxTimeoutMs)*time.Millisecond {
			t.Fatalf("tx 预算应为默认 %dms，实际 %v", DefaultTxTimeoutMs, pool.tx)
		}
	})

	t.Run("正值原样采用", func(t *testing.T) {
		got := newTimeoutConnPool(raw, 1234, 5678)
		pool, ok := got.(*timeoutConnPool)
		if !ok {
			t.Fatalf("正值应启用防护，实际类型 %T", got)
		}
		if pool.call != 1234*time.Millisecond || pool.tx != 5678*time.Millisecond {
			t.Fatalf("预算应原样采用，实际 call=%v tx=%v", pool.call, pool.tx)
		}
	})

	t.Run("负值显式关闭", func(t *testing.T) {
		got := newTimeoutConnPool(raw, -1, -1)
		if _, ok := got.(*timeoutConnPool); ok {
			t.Fatal("两个预算都显式关闭时应退回原生池")
		}
		if got != raw {
			t.Fatalf("应原样返回传入的池，实际 %T", got)
		}
	})

	t.Run("单侧关闭仅关闭该侧", func(t *testing.T) {
		got := newTimeoutConnPool(raw, -1, 0)
		pool, ok := got.(*timeoutConnPool)
		if !ok {
			t.Fatalf("只有一侧关闭时应仍启用防护，实际类型 %T", got)
		}
		if pool.call != 0 {
			t.Fatalf("call 预算应已关闭（0），实际 %v", pool.call)
		}
		if pool.tx != time.Duration(DefaultTxTimeoutMs)*time.Millisecond {
			t.Fatalf("tx 预算应仍取默认，实际 %v", pool.tx)
		}
	})
}

// TestWithBudgetKeepsExistingDeadline 直接锁定 withBudget 的取舍：已有期限一律不动。
func TestWithBudgetKeepsExistingDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	got := startBudget(ctx, time.Hour).ctx
	deadline, ok := got.Deadline()
	if !ok {
		t.Fatal("应保留调用方期限")
	}
	// 若被本层覆盖成 1h，这里的余量会远超 50ms。
	if remaining := time.Until(deadline); remaining > time.Second {
		t.Fatalf("调用方期限被覆盖（余量 %v，疑似换成了本层预算）", remaining)
	}
}

// TestStartBudgetFlagsOwnTimeout 验证「超时归属」判定：只有本层预算真的到点才算我们的超时。
// 归属判错会直接导致误导性排障（把调用方期限到期报成连接池耗尽，或反之漏报池问题）。
func TestStartBudgetFlagsOwnTimeout(t *testing.T) {
	b := startBudget(context.Background(), 30*time.Millisecond)
	<-b.ctx.Done()
	if !b.ours() {
		t.Fatal("本层预算到点应判定为自身的超时")
	}

	caller, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	cb := startBudget(caller, time.Hour)
	<-cb.ctx.Done()
	if cb.ours() {
		t.Fatal("调用方期限到点不应被记为本层的超时")
	}
}

// TestWaitTimeoutErrorUnwrapsToAppErrorAndDeadline 锁定错误双向穿透：
// 对外要能取到 *apperr.Error（渲染 503 + 可读中文），对内要能 errors.Is 到超时。
// 只满足一侧都会造成实际损失——前者丢了运维可读性，后者让上层的超时分支失效。
func TestWaitTimeoutErrorUnwrapsToAppErrorAndDeadline(t *testing.T) {
	err := error(&waitTimeoutError{op: "取连接"})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("应可 errors.Is 到 context.DeadlineExceeded")
	}
	if !strings.Contains(err.Error(), dbWaitTimeoutMessage) {
		t.Fatalf("文案应为可读中文原因，实际 %q", err.Error())
	}
}
