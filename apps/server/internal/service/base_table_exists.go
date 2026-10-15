package service

import (
	"database/sql"
	"reflect"
	"sync"

	"gorm.io/gorm"
)

// tableNamer 是「模型可自报表名」的窄约束（与 store.EnsureDailyTable 同一约定）：
// 所有 GORM 实体都实现 TableName()，因此取表名不必再发一次查询。
type tableNamer interface {
	TableName() string
}

// baseTableKey 是基础表存在性缓存的键：(数据库句柄, 表名)。
// 必须带上数据库句柄：只按表名缓存会让「某库还没有该表」的答案污染其它库的判定——
// 运行资格闸一旦被误判为「表不存在」就会**静默放行**归档 namespace，比慢查询危险得多。
// 句柄用接口值本身（持有强引用）而非指针整数：既避免数据库句柄被 GC 回收后
// 地址复用导致的张冠李戴，也不会无限增长（每个库的每个表一条，主库恒定两条）。
type baseTableKey struct {
	pool  any
	table string
}

// baseTableExistsCache 缓存「启动期 AutoMigrate 建立、运行期不再变动」的基础表存在性。
//
// 为什么必须缓存：gorm 的 Migrator().HasTable 并不是读内存，而是真发一条
// `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`——每发一次都要从连接池
// 借一条连接、并占用一条 call-timeout-ms 预算。而 namespace / server 这类基础表只在 store.Open
// 的启动期 AutoMigrate 时可能变化（仓库无运行期 DropTable），运行期是静态事实；逐请求重查等于把
// 一条无意义查询塞进心跳热路径。压测实测：心跳曾以每请求 3 次 HasTable 占到全库连接压力的近一半。
//
// **只缓存 true（缓存里永远只有这一种值）**，这是本缓存的正确性前提：
// HasTable 的签名只有 bool、没有 error 通道，实现内部把 error 丢弃（glebarez 的 HasTable 即
// 忽略 Raw(...).Row().Scan() 的错误）——于是「库不可达 / 连接池枯竭 / 连接已关闭」与「表不存在」
// 在本函数里完全同形，都是 false。而本缓存是**进程级且永不过期**的，若把 false 也写进来，
// 一次瞬时故障就会把「表不存在」钉死到进程退出；偏偏本函数的四个调用点全是 fail-open 的
// 运行资格闸（server_lifecycle.go 中 `!baseTableExists(...) → return nil`），
// 后果是归档 namespace / server 被静默放行，只能靠重启恢复。
// 反过来「存在」是确凿的静态事实（运行期无 DropTable），可以放心长期缓存。
//
// 作用域（用错会拿到错误答案，改调用点前必读）：
//   - 仅用于启动期 AutoMigrate 建立的基础表。**日表**（conn_detail / message / metric_sample_v2 /
//     sched_decision_v2 / mcp_invocation / archive_job_item 等按 UTC 日期后缀或按需建的表）
//     一律保持逐次直查，不得走本函数——那些调用点的语义正是「缺表即跳过」，
//     缓存会把「目前还没有」永远钉死。
//   - 仅用于主库：归档库（FR-151，见 ADR-0066）是第二个 schema，不得把它的表名喂进来。
var baseTableExistsCache sync.Map // map[baseTableKey]bool，当前只会出现 true

// baseTableExists 判基础表是否存在，同一数据库的同名表**一旦确认存在**就不再真查。
//
// 缓存只写入 true，故 Load 命中的必然是「已确认存在」；false（表不存在，或探测失败——两者在此
// 不可区分，原因见缓存变量注释）一律不入缓存，下次调用重新探测。代价是表真不存在时逐次真查，
// 但那种库要么尚未建表（启动期会补齐）、要么本就不该承担运行流量，不构成热路径负担；
// 用一次真查换「故障不被固化」是划算的。
//
// 以下情形一律退回逐次直查、不进缓存：
//   - 事务（*sql.Tx）：事务不是本优化要救的热路径（心跳与命令轮询都不在事务里），
//     且缓存的口径是「长期有效的数据库句柄」，一次性事务句柄不属于该口径；
//   - 连接池不可比较或无显式连接池（如新建 session）：无法稳定构造键；
//   - 模型未实现 tableNamer：不自猜命名规则。
//
// 事务守卫与「只缓存 true」的分工——两者都保留，但防护性质不同：
//   - 「只缓存 true」是**正确性**闸：false 可能是故障假象，固化它会让资格闸永久 fail-open；
//   - 事务守卫是**范围**闸：只保证事务探测完全不碰这个进程级缓存。
//
// 只缓存 true 之后，事务守卫即便失效也不会给出错误答案（false 不入缓存，缓存的 true 又确凿），
// 故它的实际防护价值低于前者；此处修好并保留，是为了让实现与注释一致，
// 并保证将来若有人改动「false 也可缓存」时，事务路径不会是第一个漏口。
func baseTableExists(db *gorm.DB, model any) bool {
	// 读 Statement.ConnPool 而不是 db.ConnPool：gorm 的 Begin 只把事务句柄写进 Statement.ConnPool，
	// 从不改动 DB.ConnPool（它始终指向底层连接池），故读 db.ConnPool 时下面的 *sql.Tx 判定永不成立，
	// 注释承诺的「事务不进缓存」是假的。取池方式与 gorm 自己的 DB() 保持一致：
	// Statement 为空（零值 *gorm.DB）或未绑池时回落到 db.ConnPool。
	pool := db.ConnPool
	if db.Statement != nil && db.Statement.ConnPool != nil {
		pool = db.Statement.ConnPool
	}
	if pool == nil || !reflect.TypeOf(pool).Comparable() {
		return db.Migrator().HasTable(model)
	}
	if _, isTx := pool.(*sql.Tx); isTx {
		return db.Migrator().HasTable(model)
	}
	namer, ok := model.(tableNamer)
	if !ok {
		return db.Migrator().HasTable(model)
	}
	key := baseTableKey{pool: pool, table: namer.TableName()}
	if cached, ok := baseTableExistsCache.Load(key); ok {
		return cached.(bool)
	}
	exists := db.Migrator().HasTable(model)
	// 只登记正答案：见缓存变量注释，false 可能是瞬时故障的假象，固化进永不过期的进程缓存
	// 会让运行资格闸在本进程余下生命周期里静默放行归档对象。
	if exists {
		baseTableExistsCache.Store(key, exists)
	}
	return exists
}

// resetBaseTableExistsCacheForTest 清空基础表存在性缓存，供测试隔离（仿 store.resetDailyTableCacheForTest）。
//
// 本缓存是进程级 sync.Map，用例之间会串味：前一个用例暖过的 true 会让后一个用例在换了库之后
// 仍判到存在（既有用例靠「每例不同 DSN → 不同键」规避，但没法覆盖「同一库先判不存在、建表后再判」
// 这类必须从空缓存起步的场景）。定义在生产文件里与 store 包保持同一范式，仅测试调用。
func resetBaseTableExistsCacheForTest() {
	baseTableExistsCache.Range(func(k, _ any) bool {
		baseTableExistsCache.Delete(k)
		return true
	})
}
