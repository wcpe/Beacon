package store

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
)

// 连接等待与事务寿命的默认预算（可经 config.DatabaseConfig 覆盖）。
//
// 为什么需要这一层：`database/sql` 的连接池排队**只在调用方给了 ctx 的情况下**才可中断
// （`db.conn(ctx)` 内 `select { case <-ctx.Done(): return nil, ctx.Err() }`）。而本仓主力写法
// `db.Transaction(func(tx *gorm.DB) error {...})` 不传 ctx —— gorm 给它 `context.Background()`，
// 永不取消。于是一旦连接池被占满（例如持锁方在等连接、持连接方在等锁），等待者会**永久挂起**，
// 且 `busy_timeout` 无效（它只管 SQLite 文件锁，不管 Go 层的池排队）。生产 P0（2026-10-10 02:41:05）
// 正是此形态：所有需访问 DB 的 HTTP 端点挂起 25 分钟，只能靠重启恢复。
//
// 本包装层在**不修改任何调用点**的前提下，为每条经 gorm 的 DB 语句与每个事务补上预算：
// 等连接等太久就快速失败并给出可读原因，而不是无声挂起——这是「一处改动覆盖全部调用点」的落点。
//
// 为什么必须两阶段（勿退化为一阶段）：事务的 ctx 会被 `database/sql` 绑到事务寿命上
// （`db.beginDC` 内 `go tx.awaitDone()`，ctx.Done 一到立即回滚事务；`Rows.awaitDone(ctx)` 同理）。
// 若用一个短预算同时管「取连接」与「事务寿命」，任何活过该预算的正常事务都会被中途掐断——
// 本仓最长事务是文件批量导入 2000 文件（实测 8.5s），用 5s 管事务寿命会直接误杀。故拆成两个独立预算：
//   - 取连接：短预算（默认 5s）。此阶段只占 `sql.Conn`，而 `Conn` 拿到后不再看 ctx（见 grabConn），
//     故超时后可安全取消，只需自行归还连接。
//   - 事务寿命：长预算（默认 60s）。作为被 `awaitDone` 监听的那条 ctx，仅在拿到连接之后才生效。
//
// 实测两阶段不损失性能：300 次事务、池上限 4 —— 单阶段 743ms / 两阶段 581ms（反而更快，因为
// `db.Conn(ctx).BeginTx(ctx)` 复用了刚占用的那条连接，少一次池握手）；池上限 1 且并发 8 时
// 无连接流失（InUse=0 / Wait=0）。池被占满时实测裸查询 ~700ms、事务 ~701ms 准时失败。
const (
	// DefaultCallTimeoutMs 是单条语句 / 取连接的默认预算（毫秒）。
	// 取值依据：业务事务实测中位 1.482ms / p95 3.817ms / 最大 11.301ms，5s 有两个数量级余量，
	// 真被耗尽时必是池枯竭而非正常抖动。
	DefaultCallTimeoutMs = 5000
	// DefaultTxTimeoutMs 是事务寿命的默认预算（毫秒）。
	// 取值依据：最长事务（导入 2000 文件）实测 8.5s，60s 留足 7 倍余量；同时它仍是有限值，
	// 保证事务一旦卡死也会在 60s 内**发起**回滚，而不是永久占用。
	//
	// 措辞刻意是「发起」而非「释放」：回滚提交到 `database/sql` 后，归还连接要经
	// `Conn.grabConn` 的 `closemu` 写锁——该写锁与所有在途查询的读锁互斥，而**在途查询本身
	// 不受本预算管辖**（`Conn` 拿到后不再看 ctx）。故若另有一条语句正长跑，这条事务的回滚会被
	// 排在它后面；「60s 内一定归还连接」是不成立的断言，勿在别处据此推导容量。
	DefaultTxTimeoutMs = 60000
)

// dbWaitTimeoutCode 是「等 DB 连接超时」的业务码。
//
// 为什么单独一个码而不是复用 INTERNAL：这条错误是**容量/形态问题**的指纹（池太小、有长事务占着
// 连接，或有人持锁等连接），运维看到它就该去调 `max-open-conns` 或查长事务，而不是去读代码。
// 503 也比 500 更准：服务本身没坏，是依赖资源暂时拿不到。
const dbWaitTimeoutCode = "DB_WAIT_TIMEOUT"

// dbWaitTimeoutMessage 是对运维可见的可读原因（含指向的下一步）。
const dbWaitTimeoutMessage = "等 DB 连接超时（可能是连接池耗尽或有长事务占用）"

// ErrDBWaitTimeout 是「等 DB 连接超时」的领域错误：对外由 render 渲染为 503 + 可读中文，
// 供前端与运维直接看出「该去看连接池/长事务」而不是去读代码。
// 单点定义为包级变量，使文案与业务码只有一处真源（日志与响应体不会漂移）。
var ErrDBWaitTimeout = apperr.New(http.StatusServiceUnavailable, dbWaitTimeoutCode, dbWaitTimeoutMessage)

// timeoutConnPool 给 gorm 的底层连接池加上「取连接」与「事务寿命」两层预算。
//
// 它刻意实现 gorm.ConnPoolBeginner（BeginTx 返回 ConnPool）而**不**实现 TxBeginner
// （同名方法但返回 *sql.Tx）。原因：gorm 的 `Begin` 先做 `case TxBeginner` 再做 `case ConnPoolBeginner`
// （finisher_api.go），而 TxBeginner 只把 *sql.Tx 交出来、拿不到那条连接，无法把「等连接」与
// 「事务寿命」拆到两个 ctx 上——两阶段正是靠 ConnPoolBeginner 才能表达。
// 二者方法名相同、签名不同，Go 类型只可能满足其中一个，故此处的方法集天然选中 ConnPoolBeginner。
type timeoutConnPool struct {
	inner *sql.DB
	// call 是单条语句 / 取连接的预算；tx 是事务拿到连接后的寿命预算。
	call, tx time.Duration
}

// newTimeoutConnPool 构造包装层。
//
// 预算取值语义（安全默认，勿改成 0=关闭）：0 = 取内置默认，负值 = 显式关闭该预算。
// 为什么不让 0 表示关闭：本层是 P0 死锁的兜底防护，而 `DatabaseConfig` 是可在代码里手工构造的
// （测试夹具、诊断脚本），一旦「漏填字段」等于「静默失去防护」，防护就会在最需要它的场合消失，
// 且不留下任何迹象。故只有**显式传负值**才关闭，让关闭动作必须是有意为之。
//
// 返回 gorm.ConnPool 以便直接赋给 dialector 的 Conn 字段。
func newTimeoutConnPool(inner *sql.DB, callMs, txMs int) gorm.ConnPool {
	call := resolveBudget(callMs, DefaultCallTimeoutMs)
	tx := resolveBudget(txMs, DefaultTxTimeoutMs)
	if call <= 0 && tx <= 0 {
		return inner
	}
	return &timeoutConnPool{inner: inner, call: call, tx: tx}
}

// BoundedPinger 是**带预算**的数据库连通性探测能力，供运行期健康端点使用。
//
// 为什么必须是可注入的窄接口（而不是让调用方自己拿 *sql.DB 调 PingContext）：
// 预算值住在包装层里（`call-timeout-ms`，可配置、可关闭），而调用方只拿得到原生池
// （store.GetDBConn 的逃生口）。把「预算」与「怎么用」一起封在接口里，调用方就不必知道
// Config 字段，也不会各自拍一个魔法时长——本轮 P1 的成因之一正是「语句有预算、探测没有」。
//
// 用 PingContext 而非 Ping：`(*sql.DB).Ping()` 内部走 `context.Background()`，池被占满时无界等待，
// 恰是事故症状在排障端点上的原样复现（见 timeoutConnPool.PingContext 的说明）。
type BoundedPinger interface {
	// PingContext 在预算内探测连通性；超时由本层预算引起时返回 ErrDBWaitTimeout，
	// 调用方自带期限到期则原样透传（不覆盖、不误报成池问题）。
	PingContext(ctx context.Context) error
}

// NewBoundedPinger 取一个带预算的探测器供运行期健康探测注入。
//
// db 为 nil 时返回 nil（调用方须自行判空：这表示「没有可用的探测器」，不是「连通」）。
//
// 为什么经类型断言取包装层而不是直接暴露包装类型：包装层经 dialector 注入后由 gorm 固化进
// 语句级字段（见 newTimeoutConnPool 的装配说明），类型断言是唯一不依赖 gorm 内部字段布局的
// 取用方式；导出包装类型则会把「预算怎么实现」变成跨包契约，锁死后续调整空间。
//
// 两道回退，都不报错（探测能力缺失不该阻断启动，两者的语义差别见下）：
//   - 顶层 `db.ConnPool` 已是包装层 → 直接用（带预算，正常路径）；
//   - 否则经 `db.DB()` 拿原生池 → **不带本层预算**，只在驱动未启用包装层（如两侧预算都被
//     显式关闭）时出现。此时它仍比 `Ping()` 强：调用方传 ctx 即可中断（原生 PingContext 可用），
//     只是没有兜底期限——由调用方自己的 ctx 负责。返回 nil 会让调用方彻底失去探测能力，
//     比「无兜底期限但可被 ctx 中断」更差，故取后者。
func NewBoundedPinger(db *gorm.DB) BoundedPinger {
	if db == nil {
		return nil
	}
	if p, ok := db.ConnPool.(BoundedPinger); ok {
		return p
	}
	sqlDB, err := db.DB()
	if err != nil || sqlDB == nil {
		return nil
	}
	return sqlDB
}

// resolveBudget 把配置值解析为实际预算：0 取默认、正值原样、负值表示关闭（返回 0）。
func resolveBudget(configuredMs, defaultMs int) time.Duration {
	switch {
	case configuredMs > 0:
		return time.Duration(configuredMs) * time.Millisecond
	case configuredMs < 0:
		return 0
	default:
		return time.Duration(defaultMs) * time.Millisecond
	}
}

// budget 表示一次带预算的调用：ctx 用于下传，ours 报告「本次超时是否由本层预算引起」。
//
// 区分归属是必要的：调用方自带的期限（如请求级 timeout）到期属于它自己的语义，
// 报成「连接池耗尽」会误导排障方向。故只在本层新建的预算真的到点时，才翻译成池相关的可读错误。
type budget struct {
	ctx context.Context
	// ours 判定本次超时是否由本层预算引起。
	ours func() bool
}

// startBudget 在 ctx 没有 deadline 时补一个，并回传归属判据。
//
// **不覆盖调用方已有期限**是硬要求：审批、导出等路径可能带着比本预算更紧的 ctx，
// 若在此统一收紧或放宽，会改变那些路径既有的失败时机语义。
func startBudget(ctx context.Context, d time.Duration) budget {
	if d <= 0 {
		return budget{ctx: ctx, ours: func() bool { return false }}
	}
	if _, ok := ctx.Deadline(); ok {
		return budget{ctx: ctx, ours: func() bool { return false }}
	}
	derived, cancel := context.WithTimeout(ctx, d)
	// 故意不调用 cancel：gorm 会把该 ctx 传给 `database/sql`，而后者把它绑到事务寿命与行集上
	// （beginDC 内 `go tx.awaitDone()` 监听 ctx.Done，一到就回滚事务）。在此 cancel 会让事务当场被回滚。
	// 这是本文件最容易踩的坑：标准写法是 defer cancel()，此处**刻意违反**。
	// 代价只是让 timer 活到预算耗尽后由 GC 回收；正确性不可让。
	_ = cancel
	return budget{ctx: derived, ours: func() bool { return errors.Is(derived.Err(), context.DeadlineExceeded) }}
}

// waitTimeoutError 是「等 DB 连接超时」错误：对外暴露可读业务文案，对内保留超时判据。
//
// Unwrap 同时给出两个因：
//   - *apperr.Error → render.WriteError 的 errors.As 命中，对外回 503 + 中文可读文案；
//   - context.DeadlineExceeded → 调用侧 `errors.Is(err, context.DeadlineExceeded)` 依旧成立，
//     便于上层把「拿不到连接」与「SQL 真的报错」区分开。
type waitTimeoutError struct {
	// op 是发生超时的阶段（取连接 / 语句 / 开事务），用于日志定位。
	op string
}

// Error 实现 error；沿用 apperr 的「码: 文案」形态，使日志里既能一眼看出类别、也保留人话原因。
func (e *waitTimeoutError) Error() string { return ErrDBWaitTimeout.Error() }

// Unwrap 返回被包装的两个因，供 errors.Is / errors.As 双向穿透。
func (e *waitTimeoutError) Unwrap() []error {
	return []error{ErrDBWaitTimeout, context.DeadlineExceeded}
}

// Timeout 报告这是一次超时，满足 net.Error 与常见超时判定的接口约定。
func (e *waitTimeoutError) Timeout() bool { return true }

// Op 返回发生超时的阶段，供日志标注。
func (e *waitTimeoutError) Op() string { return e.op }

// wrapTimeout 在「超时由本层预算引起」时换成可读的 waitTimeoutError；否则原样透传
// （调用方自己的期限到期、以及真实的 SQL 错误都保持原样，不被误报成池问题）。
//
// 为什么除归属判据外还要看**底层错误本身**（勿删这个条件，它是 P2-5 的修复）：
// `ours()` 只能回答「我建的预算到点了」，回答不了「本次失败是它造成的」。两者不等价——
// 实测：预算 300ms、sqlite busy_timeout 5000ms 时，一次写冲突在 SQLite 的 busy 处理里
// 等满 5s 才返回 SQLITE_BUSY；此刻派生 ctx 早已过期，`ours()` 为真，于是这条纯粹的
// **文件锁竞争**被贴上了「等 DB 连接超时（可能是连接池耗尽或有长事务占用）」。
// 对运维这是方向性误导：该去看谁在长写、而不是去调 max-open-conns。
// 加上 `errors.Is(err, context.DeadlineExceeded)` 后，只有真正由 ctx 期限驱动的失败才被认领；
// 池排队那一路（`db.conn(ctx)` 返回 `ctx.Err()`）仍是 DeadlineExceeded，故覆盖不受损。
func wrapTimeout(op string, err error, ours bool) error {
	if err == nil {
		return nil
	}
	if ours && errors.Is(err, context.DeadlineExceeded) {
		return &waitTimeoutError{op: op}
	}
	return err
}

// PrepareContext 带预算执行预处理。
func (p *timeoutConnPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	b := startBudget(ctx, p.call)
	stmt, err := p.inner.PrepareContext(b.ctx, query)
	return stmt, wrapTimeout("prepare", err, b.ours())
}

// ExecContext 带预算执行写语句。
func (p *timeoutConnPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	b := startBudget(ctx, p.call)
	res, err := p.inner.ExecContext(b.ctx, query, args...)
	return res, wrapTimeout("exec", err, b.ours())
}

// QueryContext 带预算执行查询。
func (p *timeoutConnPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	b := startBudget(ctx, p.call)
	rows, err := p.inner.QueryContext(b.ctx, query, args...)
	return rows, wrapTimeout("query", err, b.ours())
}

// QueryRowContext 带预算执行单行查询。
// 注意 *sql.Row 的错误延迟到 Scan 才浮现，此处无法翻译；预算本身照常生效。
func (p *timeoutConnPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return p.inner.QueryRowContext(startBudget(ctx, p.call).ctx, query, args...)
}

// BeginTx 实现 gorm.ConnPoolBeginner：两阶段取连接——先用短预算抢一条连接，
// 再以长预算开事务，从而让「等连接」受限而「事务寿命」不受限。
//
// 为什么不用 db.BeginTx：它只有一个 ctx，无法同时表达两个预算——这正是 gorm 内建旋钮
// （DefaultTransactionTimeout / DefaultContextTimeout）覆盖不全的根因。
func (p *timeoutConnPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	cb := startBudget(ctx, p.call)
	conn, err := p.inner.Conn(cb.ctx)
	if err != nil {
		// 池被占满时这里就是快速失败的落点（而不是无限等待）。
		return nil, wrapTimeout("取连接", err, cb.ours())
	}
	tb := startBudget(ctx, p.tx)
	tx, err := conn.BeginTx(tb.ctx, opts)
	if err != nil {
		// 开事务失败必须立刻归还连接，否则每次失败都泄一条（池很快枯竭、再也拿不到连接）。
		_ = conn.Close()
		return nil, wrapTimeout("begin", err, tb.ours())
	}
	return &boundedTx{Tx: tx, conn: conn}, nil
}

// Ping 是**无 ctx** 的透传，只服务 gorm.Open 的启动期自动 Ping 这一条路径。
//
// 为什么这里刻意不加预算（勿顺手「补上」）：`database/sql` 的 Ping 只有 PingContext 一种实现，
// 而无 ctx 的重载内部走 `context.Background()`。若要在此加预算，只能在下面自行 WithTimeout 包一层——
// 单看是安全的（Ping 不把 ctx 传给任何更长寿命的对象，不像事务/行集绑定寿命）。但更关键的是
// **运行期健康探测根本不该走这里**：调用方拿到的往往是 GetDBConn() 暴露的原生 *sql.DB（见下），
// 它压根不经过本方法。故本方法的真实身份是「启动期连通性检查」，而启动期恰恰**不能**套用秒级预算
// （慢启动的 MySQL 首连、冷启进程的 DNS 解析都会超 5s，套上去就把「能起来」变成「起不来」，
// 与 bootstrapTimeout 给建表放宽 30 分钟同一口径）。故启动期保持无界，运行期走 PingContext。
func (p *timeoutConnPool) Ping() error { return p.inner.Ping() }

// PingContext 是**带预算**的健康探测，供运行期端点（/api/system/status、归档可达性）使用。
//
// 为什么必须单独开一个方法而不是复用 Ping：事故里「所有需 DB 的端点挂起」的同一症状会在
// 排障期原样复现在这两条端点上——而它们恰恰是排障时最需要的。原生 `(*sql.DB).Ping()` 内部用
// `context.Background()`，池被占满时**无界等待**；包装层又不经过 Ping（调用方拿的是 GetDBConn()
// 返回的原生池），故「语句与事务已有预算」这一层防护对健康探测**完全不生效**。
//
// 预算取 call 而非 tx：探测只借一条连接做一次往返，语义与「单条语句」同类（等连接 + 一次执行），
// 不与任何事务寿命绑定。调用方自带期限时由 startBudget 原样尊重（见其注释），不覆盖。
func (p *timeoutConnPool) PingContext(ctx context.Context) error {
	b := startBudget(ctx, p.call)
	err := p.inner.PingContext(b.ctx)
	// 归属翻译：预算到点报可读的「等连接超时」，调用方自己的期限到期则原样透传。
	return wrapTimeout("ping", err, b.ours())
}

// GetDBConn 暴露原生 *sql.DB：gorm 的 db.DB()、store.Close 与归档可达性探测都依赖它。
//
// 注意它**绕过**本层的全部预算：语句走 ConnPool 接口故仍受管辖，但调用方拿着原生池直接
// `Ping()` / `Conn()` 时不受限。这是刻意保留的逃生口（gorm 内部与关停路径需要真实池语义），
// 代价是「预算覆盖不全」——真正需要受限的调用点应改用带 ctx 的对应方法（如 PingContext）。
func (p *timeoutConnPool) GetDBConn() (*sql.DB, error) { return p.inner, nil }

// boundedTx 是被包装层接管生命周期的原生事务。
//
// 必须一起做两件事，缺一即是连接泄漏：
//   - `db.Conn()` 占住的那条连接：事务结束后显式 Close 归还给池（boundedTx 与 *sql.Tx 不同，
//     *sql.Tx 自己只管 releaseConn，不会归还被 Conn 独占的连接）；
//   - 事务寿命 ctx：由 startBudget 派生后**不 cancel**，随事务对象一起被 GC 回收。
type boundedTx struct {
	*sql.Tx
	// conn 是 BeginTx 抢到的那条连接，事务结束时归还。
	conn *sql.Conn
	// once 保证归还只发生一次（Commit 与 Rollback 可能被上层先后调用）。
	once sync.Once
}

// finish 归还连接（幂等）。
func (t *boundedTx) finish() {
	t.once.Do(func() { _ = t.conn.Close() })
}

// Commit 提交并归还连接；即使提交失败也归还，避免失败路径泄连接。
func (t *boundedTx) Commit() error {
	err := t.Tx.Commit()
	t.finish()
	return err
}

// Rollback 回滚并归还连接。
func (t *boundedTx) Rollback() error {
	err := t.Tx.Rollback()
	t.finish()
	return err
}
