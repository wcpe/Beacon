package store

import (
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"
)

// dailyTableSuffixLayout 是日表名的 UTC 日期后缀格式（如 metric_sample_20260711）。
// 全仓统一用 UTC 日期切表，避免时区导致的跨日归属歧义（基座 §1、可移植约束）。
const dailyTableSuffixLayout = "20060102"

// dailyTableCacheKey 直接持有数据库连接身份，避免把指针格式化成字符串后丢失强引用：连接关闭并被回收时，
// 新连接可能复用旧地址，若只缓存地址字符串会误命中旧库日表。不同热库 / 归档库连接仍按 *gorm.DB 隔离。
type dailyTableCacheKey struct {
	db    *gorm.DB
	table string
}

// ensuredDailyTables 缓存进程内「已确认存在」的日表名，避免每次写入都探测 information_schema。
// 首次写入某日表时判存 / 建表并登记；之后同一数据库的同名表直接命中缓存（sync.Map 并发安全）。
var ensuredDailyTables sync.Map

// tableNamer 是「模型可自报基表名」的窄约束：所有 GORM 实体都实现 TableName()。
type tableNamer interface {
	TableName() string
}

// DailyTableName 由基表名与 UTC 日期拼出日表名（如 base=metric_sample、day=2026-07-11 → metric_sample_20260711）。
// 纯函数，供写入协程按行内时间戳定目标表、跨日批拆分与测试直接断言表名。
func DailyTableName(base string, day time.Time) string {
	return base + "_" + day.UTC().Format(dailyTableSuffixLayout)
}

// EnsureDailyTable 按需建当日日表并返回其表名（全仓日表基建，首个消费者为 P4 指标批表）。
//
// 语义等价可移植的「CREATE TABLE IF NOT EXISTS」：先查进程缓存，未命中再经 GORM Migrator
// 判存（HasTable）、缺失则动态表名建表（db.Table(name).Migrator().CreateTable）。DDL 由 GORM
// 按方言生成，绝不手写方言专有 SQL / 分区语法（守可移植）。
//
// 表已存在时按当前模型补齐缺失列（AutoMigrate 只增列不删列）：旧版二进制建出的当日存量表
// 在升级后首次触达即被加列对齐，模型新增可空列（如 FR-180 广播聚合列）向后兼容、写入不因缺列失败。
// 每表每进程仅补一次（随缓存短路），对齐后为空操作。
//
// model 须实现 TableName() 提供基名；其 TableName 会被 db.Table(name) 覆盖为日表名，
// 故该模型不进全局 AutoMigrate、只经本函数按日建表。并发下多协程可能同时探测同一日表，
// 建表用 IF NOT EXISTS 语义幂等，且建表报错后再判存一次容错（另一协程已建即视为成功）。
//
// 并发容错覆盖**两条**分支（曾只覆盖前一条，CI 上实测漏网）：
//   - CreateTable 分支：报错后再判存一次，另一协程已建即视为成功；
//   - AutoMigrate 分支：两个协程同时补同一张存量表的列/索引时会撞 DDL 竞态
//     （报 `index ... already exists` / `duplicate column`），此处同样复判后放行。
//     AutoMigrate 本身是幂等的（只增列、只增索引），故「撞了但目标状态已达成」等价成功。
func EnsureDailyTable(db *gorm.DB, model any, day time.Time) (string, error) {
	namer, ok := model.(tableNamer)
	if !ok {
		return "", fmt.Errorf("日表模型 %T 未实现 TableName()，无法推导基表名", model)
	}
	name := DailyTableName(namer.TableName(), day)
	// 缓存键直接持有 db 身份：既隔离热库 / 归档库，也防已关闭连接的地址被新连接复用后误命中旧缓存。
	cacheKey := dailyTableCacheKey{db: db, table: name}
	if _, cached := ensuredDailyTables.Load(cacheKey); cached {
		return name, nil
	}
	if err := ensureTableShape(db, name, model); err != nil {
		return "", err
	}
	ensuredDailyTables.Store(cacheKey, struct{}{})
	return name, nil
}

// ensureTableShape 确保日表存在且结构对齐当前模型（建表 / 补列 / 补索引），
// 并对**并发 DDL 竞态**做容错。
//
// 为什么两处都需要容错：本函数语义是「确保目标状态达成」，而非「确保本次 DDL 由我执行」。
// 并发下多个协程会对同一张未建 / 待补的日表同时下手，而判存与 DDL 之间没有原子性：
//   - CreateTable 分支：两协程都判到「表不存在」，后建者报 `table ... already exists`；
//   - AutoMigrate 分支：两协程都判到「列 / 索引缺失」，后行者报
//     `index ... already exists` / `duplicate column name`。
//
// 两者都是「别人已把事做完」，属成功而非失败；而 AutoMigrate 只增不删、本身幂等，
// 故撞竞态后**重跑一次**即可区分「并发成功」与「真实失败」——重跑仍失败才是真失败。
// 这比「报错后判存再放行」严格：后者在 AutoMigrate 分支恒为真（该分支只在表已存在时进入），
// 等于无条件吞错，会把磁盘满 / 只读库这类真实故障误报成成功。
func ensureTableShape(db *gorm.DB, name string, model any) error {
	if !db.Migrator().HasTable(name) {
		if err := db.Table(name).Migrator().CreateTable(model); err != nil && !db.Migrator().HasTable(name) {
			return fmt.Errorf("建日表 %s 失败: %w", name, err)
		}
		return nil
	}
	// 存量表：按当前模型补齐缺失列 / 索引（模型演进只增不删）。
	//
	// 失败后重跑一次而非「判存后放行」：AutoMigrate 是幂等的（只增列、只增索引），故
	//   - 并发撞车（另一协程刚建完同名索引 / 列）：重跑时目标状态已达成，必然成功；
	//   - 真实失败（磁盘满 / 只读库 / 类型冲突）：重跑会再次失败并如实上抛。
	// 若改用「判存放行」，由于本分支只在表**已存在**时进入，判存恒为真，等于无条件吞掉所有错误——
	// 真实失败会被误报成成功，还会把「已确保」写进进程缓存，导致该表在本进程内再不复检。
	if err := db.Table(name).AutoMigrate(model); err != nil {
		if retryErr := db.Table(name).AutoMigrate(model); retryErr != nil {
			return fmt.Errorf("存量日表 %s 补列失败: %w", name, retryErr)
		}
	}
	return nil
}

// resetDailyTableCacheForTest 清空日表缓存，供测试隔离（不同内存库间不串缓存）。
func resetDailyTableCacheForTest() {
	ensuredDailyTables.Range(func(k, _ any) bool {
		ensuredDailyTables.Delete(k)
		return true
	})
}
