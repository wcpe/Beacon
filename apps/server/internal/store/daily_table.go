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

// ensuredDailyTables 缓存进程内「已确认存在」的日表名，避免每次写入都探测 information_schema。
// 首次写入某日表时判存 / 建表并登记；之后同名直接命中缓存跳过 DDL 探测（sync.Map 并发安全）。
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
// model 须实现 TableName() 提供基名；其 TableName 会被 db.Table(name) 覆盖为日表名，
// 故该模型不进 AutoMigrate、只经本函数按日建表。并发下多协程可能同时探测同一日表，
// 建表用 IF NOT EXISTS 语义幂等，且建表报错后再判存一次容错（另一协程已建即视为成功）。
func EnsureDailyTable(db *gorm.DB, model any, day time.Time) (string, error) {
	namer, ok := model.(tableNamer)
	if !ok {
		return "", fmt.Errorf("日表模型 %T 未实现 TableName()，无法推导基表名", model)
	}
	name := DailyTableName(namer.TableName(), day)
	// 缓存键带 db 身份：同名日表在不同库（如多套测试库）需各自建；只按表名缓存会让 A 库建过后
	// B 库误判已存在而跳过建表、随后写入失败。生产仅一个 db、键即稳定。
	cacheKey := fmt.Sprintf("%p|%s", db, name)
	if _, cached := ensuredDailyTables.Load(cacheKey); cached {
		return name, nil
	}
	if !db.Migrator().HasTable(name) {
		if err := db.Table(name).Migrator().CreateTable(model); err != nil {
			// 并发建表竞态：另一协程可能已抢先建好，再判存一次容错，仍无才是真失败。
			if !db.Migrator().HasTable(name) {
				return "", fmt.Errorf("建日表 %s 失败: %w", name, err)
			}
		}
	}
	ensuredDailyTables.Store(cacheKey, struct{}{})
	return name, nil
}

// resetDailyTableCacheForTest 清空日表缓存，供测试隔离（不同内存库间不串缓存）。
func resetDailyTableCacheForTest() {
	ensuredDailyTables.Range(func(k, _ any) bool {
		ensuredDailyTables.Delete(k)
		return true
	})
}
