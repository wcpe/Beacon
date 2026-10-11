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
// 作用域（用错会拿到错误答案，改调用点前必读）：
//   - 仅用于启动期 AutoMigrate 建立的基础表。**日表**（conn_detail / message / metric_sample_v2 /
//     sched_decision_v2 / mcp_invocation / archive_job_item 等按 UTC 日期后缀或按需建的表）
//     一律保持逐次直查，不得走本函数——那些调用点的语义正是「缺表即跳过」，
//     缓存会把「目前还没有」永远钉死。
//   - 仅用于主库：归档库（FR-151，见 ADR-0066）是第二个 schema，不得把它的表名喂进来。
var baseTableExistsCache sync.Map // map[baseTableKey]bool

// baseTableExists 判基础表是否存在，同一数据库的同名表只真查一次。
//
// 以下情形一律退回逐次直查、不进缓存：
//   - 事务（*sql.Tx）：句柄每笔事务唯一，按它缓存会无界增长，且事务不是本优化要救的热路径
//     （心跳与命令轮询都不在事务里）；
//   - 连接池不可比较或无显式连接池（如新建 session）：无法稳定构造键；
//   - 模型未实现 tableNamer：不自猜命名规则。
func baseTableExists(db *gorm.DB, model any) bool {
	pool := db.ConnPool
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
	baseTableExistsCache.Store(key, exists)
	return exists
}
