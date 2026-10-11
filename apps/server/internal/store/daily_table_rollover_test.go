package store

// 本文件覆盖日表切换路径（EnsureDailyTable / DailyTableName）在跨 UTC 00:00 边界上的正确性。
//
// 为什么用「注入日期」而不是等真午夜：下一次 UTC 00:00 在 17 小时后，CI 与压测都不可能真的等到午夜。
// EnsureDailyTable 本就接收 day time.Time 参数（真实写入协程 async_daily_writer 按行内时间戳定目标表的现成接缝），
// 因此直接注入「前一天 / 当天 / 后一天」三个日期即可完整驱动同一条路径：建表、缓存登记、按表名分流写入。
//
// 为什么「UTC 归属」这条重要：日表按 UTC 日期后缀切分（daily_table.go 注释明写「避免时区导致的跨日归属歧义」）。
// 若归属错用本地日期，一批本地 23:xx、UTC 已跨日的行会落进前一天的日表——本地视角没有任何报错，
// 但跨日归档 / 清理会误删误留，同批拆分也会写错表。故此处构造本地日期与 UTC 日期不同的时刻锁定归属。

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// findDailySampleRows 读整张日表（经 db.Table(name) 精确落表读取，不依赖基表名），供计数与逐行核对归属。
func findDailySampleRows(t *testing.T, db *gorm.DB, table string) []model.MetricSampleV2 {
	t.Helper()
	var rows []model.MetricSampleV2
	if err := db.Table(table).Find(&rows).Error; err != nil {
		t.Fatalf("读日表 %s 失败: %v", table, err)
	}
	return rows
}

// writeDailySampleRow 向指定日表写一行（metric_sample 是日表基建的首个消费者，P4 指标批表）。
// serverID / bucket 由调用方给足，供断言时区分每行归属哪一天。
func writeDailySampleRow(t *testing.T, db *gorm.DB, table, serverID string, bucket int64) {
	t.Helper()
	row := &model.MetricSampleV2{ServerID: serverID, Kind: "backend", BucketStartMs: bucket}
	if err := db.Table(table).Create(row).Error; err != nil {
		t.Fatalf("写日表 %s 失败: %v", table, err)
	}
}

// assertDailyTableRowsAllPrefixed 断言日表内每一行 server_id 都带指定前缀，证明行没有跨日表串扰。
func assertDailyTableRowsAllPrefixed(t *testing.T, db *gorm.DB, table, prefix string) {
	t.Helper()
	for _, row := range findDailySampleRows(t, db, table) {
		if !strings.HasPrefix(row.ServerID, prefix) {
			t.Fatalf("日表 %s 混入非 %s 前缀的行（server_id=%s，跨表串扰？）", table, prefix, row.ServerID)
		}
	}
}

// assertLocalAndUTCDatesDiffer 断言构造时刻的本地日期确与 UTC 日期不同（本用例真正踩在归属歧义上的前提）。
func assertLocalAndUTCDatesDiffer(t *testing.T, moment time.Time, wantLocalDate string) {
	t.Helper()
	if got := moment.Format("20060102"); got != wantLocalDate {
		t.Fatalf("前置条件：时刻本地日期应为 %s，实际 %s", wantLocalDate, got)
	}
	if moment.Format("20060102") == moment.UTC().Format("20060102") {
		t.Fatalf("前置条件：本地日期 %s 与 UTC 日期应不同", wantLocalDate)
	}
}

// assertUTCDailySuffix 断言给定时刻的日表名按其 UTC 日期拼后缀，而非其本地日期。
func assertUTCDailySuffix(t *testing.T, moment time.Time, wantSuffix string) {
	t.Helper()
	want := "metric_sample_" + wantSuffix
	got := DailyTableName("metric_sample", moment)
	if got != want {
		t.Fatalf("时刻（本地 %s / UTC %s）的日表名应为 %s，实际 %s",
			moment.Format("20060102"), moment.UTC().Format("20060102"), want, got)
	}
}

// TestDailyTableRolloverCrossDayWrites 校验同一连接跨日写入不丢失、不串表，切回旧日仍正确追加。
//
// 这是 UTC 00:00 切换窗口的核心不变量：同库先写 D、再写 D+1，两表须各自独立；
// 缓存已登记 D 不应导致 D+1 漏建，也不应把切回 D 的写入错导到别处。
func TestDailyTableRolloverCrossDayWrites(t *testing.T) {
	resetDailyTableCacheForTest()
	t.Cleanup(resetDailyTableCacheForTest)
	db := openMemSQLite(t, "daily_rollover_write")

	dayD := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	dayDNext := dayD.AddDate(0, 0, 1)

	nameD, err := EnsureDailyTable(db, &model.MetricSampleV2{}, dayD)
	if err != nil {
		t.Fatalf("建 D 日表失败: %v", err)
	}
	if want := DailyTableName("metric_sample", dayD); nameD != want {
		t.Fatalf("D 日表名应为 %s，实际 %s", want, nameD)
	}
	nameDNext, err := EnsureDailyTable(db, &model.MetricSampleV2{}, dayDNext)
	if err != nil {
		t.Fatalf("建 D+1 日表失败: %v", err)
	}
	if want := DailyTableName("metric_sample", dayDNext); nameDNext != want {
		t.Fatalf("D+1 日表名应为 %s，实际 %s", want, nameDNext)
	}
	if nameD == nameDNext {
		t.Fatalf("跨日应建出两张不同表名，实际同为 %s", nameD)
	}
	if !db.Migrator().HasTable(nameD) {
		t.Fatalf("D 日表 %s 应已存在", nameD)
	}
	if !db.Migrator().HasTable(nameDNext) {
		t.Fatalf("D+1 日表 %s 应已存在", nameDNext)
	}

	// 先写 D：3 行；再写 D+1：2 行（bucket 故意与 D 复用同一组值，证明两表互不影响）。
	for i := 0; i < 3; i++ {
		writeDailySampleRow(t, db, nameD, fmt.Sprintf("day-D-%d", i), int64(100+i))
	}
	for i := 0; i < 2; i++ {
		writeDailySampleRow(t, db, nameDNext, fmt.Sprintf("day-next-%d", i), int64(100+i))
	}

	if got := len(findDailySampleRows(t, db, nameD)); got != 3 {
		t.Fatalf("D 表应有 3 行，实际 %d", got)
	}
	if got := len(findDailySampleRows(t, db, nameDNext)); got != 2 {
		t.Fatalf("D+1 表应有 2 行，实际 %d", got)
	}
	assertDailyTableRowsAllPrefixed(t, db, nameD, "day-D")
	assertDailyTableRowsAllPrefixed(t, db, nameDNext, "day-next")

	// 切回 D 再写一行：缓存命中应仍精确返回 D 表名，行数正确递增（不因登记过 D / D+1 而漏建或误建）。
	nameDAgain, err := EnsureDailyTable(db, &model.MetricSampleV2{}, dayD)
	if err != nil {
		t.Fatalf("切回 D 调 EnsureDailyTable 失败: %v", err)
	}
	if nameDAgain != nameD {
		t.Fatalf("切回 D 应返回 D 表名 %s，实际 %s", nameD, nameDAgain)
	}
	writeDailySampleRow(t, db, nameD, "day-D-back", 999)
	if got := len(findDailySampleRows(t, db, nameD)); got != 4 {
		t.Fatalf("切回 D 后 D 表应递增到 4 行，实际 %d", got)
	}
	if got := len(findDailySampleRows(t, db, nameDNext)); got != 2 {
		t.Fatalf("切回 D 不应影响 D+1 表，仍应为 2 行，实际 %d", got)
	}
}

// TestDailyTableRolloverConcurrentCrossDayWrites 校验并发跨日建表 + 写入：无 database is locked、无行丢失、无 panic。
//
// 这是真实写入协程（async_daily_writer）在午夜窗口的形态：多协程同时触达 EnsureDailyTable 与写入，
// 有的落在 UTC 当天、有的跨到次日，也有协程共享同一天（对同一张未建日表的并发建表竞态）。
// 建表须 IF NOT EXISTS 语义幂等，任一协程失败都不允许静默吞行。
func TestDailyTableRolloverConcurrentCrossDayWrites(t *testing.T) {
	resetDailyTableCacheForTest()
	t.Cleanup(resetDailyTableCacheForTest)
	db := openMemSQLite(t, "daily_rollover_concurrent")

	const (
		goroutines    = 8
		rowsPerWriter = 5
	)
	baseDay := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	// 三个日期：8 个协程按取模分摊——同一天由多个协程共享（并发建同一张表），也有的落在不同天。
	daySpan := []time.Time{baseDay, baseDay.AddDate(0, 0, 1), baseDay.AddDate(0, 0, 2)}

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			day := daySpan[worker%len(daySpan)]
			table, err := EnsureDailyTable(db, &model.MetricSampleV2{}, day)
			if err != nil {
				errCh <- fmt.Errorf("协程 %d 调 EnsureDailyTable 失败: %w", worker, err)
				return
			}
			if want := DailyTableName("metric_sample", day); table != want {
				errCh <- fmt.Errorf("协程 %d 得到的日表名 %s 应为 %s", worker, table, want)
				return
			}
			for r := 0; r < rowsPerWriter; r++ {
				// serverID / bucket 按协程唯一，只绕开 (server_id, bucket_start_ms) 唯一键，聚焦切表路径。
				serverID := fmt.Sprintf("w%d-r%d", worker, r)
				row := &model.MetricSampleV2{ServerID: serverID, Kind: "backend", BucketStartMs: int64(100000*worker + r)}
				if err := db.Table(table).Create(row).Error; err != nil {
					errCh <- fmt.Errorf("协程 %d 写 %s 失败: %w", worker, table, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if strings.Contains(err.Error(), "database is locked") {
			t.Fatalf("并发跨日写入触到写锁竞争 database is locked: %v", err)
		}
		t.Fatalf("并发跨日写入失败: %v", err)
	}

	// 无行丢失：每张日表行数 = 落在该天的协程数 × 每协程行数。
	for dayIdx, day := range daySpan {
		writers := 0
		for g := 0; g < goroutines; g++ {
			if g%len(daySpan) == dayIdx {
				writers++
			}
		}
		table := DailyTableName("metric_sample", day)
		want := writers * rowsPerWriter
		if got := len(findDailySampleRows(t, db, table)); got != want {
			t.Fatalf("并发跨日写入后 %s 应有 %d 行（%d 协程 × %d 行），实际 %d", table, want, writers, rowsPerWriter, got)
		}
	}
}

// TestDailyTableRolloverUTCAttribution 校验日表名 / 建表按 UTC 日期归属，而非本地日期。
//
// 守护 daily_table.go「全仓统一用 UTC 日期切表，避免时区导致的跨日归属歧义」的承诺：
// 构造本地日期与 UTC 日期不同的时刻，行必须落进 UTC 当天那张表。
func TestDailyTableRolloverUTCAttribution(t *testing.T) {
	resetDailyTableCacheForTest()
	t.Cleanup(resetDailyTableCacheForTest)

	// 负偏移（西五区）：本地 2026-07-11 23:30，UTC 已是 2026-07-12。
	west := time.Date(2026, 7, 11, 23, 30, 0, 0, time.FixedZone("UTC-5", -5*60*60))
	// 正偏移（东八区）：本地 2026-07-12 00:30，UTC 仍是 2026-07-11。
	east := time.Date(2026, 7, 12, 0, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))

	// 两个方向都锁死：本地仍是前一天 → 归 UTC 次日；本地已是次日 → 归 UTC 前一天。
	assertLocalAndUTCDatesDiffer(t, west, "20260711")
	assertLocalAndUTCDatesDiffer(t, east, "20260712")
	assertUTCDailySuffix(t, west, "20260712")
	assertUTCDailySuffix(t, east, "20260711")

	// 功能面：用西五区午夜前时刻直接驱动建表 + 写入，行必须落进 UTC 日 20260712 的表，
	// 绝不能出现按本地日期建的 20260711 日表（本地落错日不报错，但跨日归档 / 清理会误删误留）。
	db := openMemSQLite(t, "daily_rollover_utc")
	name, err := EnsureDailyTable(db, &model.MetricSampleV2{}, west)
	if err != nil {
		t.Fatalf("按 UTC 归属建日表失败: %v", err)
	}
	if want := "metric_sample_20260712"; name != want {
		t.Fatalf("UTC 已跨日的行应归到 %s，实际 %s", want, name)
	}
	if !db.Migrator().HasTable("metric_sample_20260712") {
		t.Fatal("UTC 日 20260712 的日表应已建出")
	}
	if db.Migrator().HasTable("metric_sample_20260711") {
		t.Fatal("不应存在按本地日期（20260711）建的日表，归属必须按 UTC")
	}
	writeDailySampleRow(t, db, name, "utc-row", 1)
	if got := len(findDailySampleRows(t, db, name)); got != 1 {
		t.Fatalf("UTC 日表应含刚写入的 1 行，实际 %d", got)
	}
}

// TestDailyTableRolloverCacheKeyIsolatedByDB 校验日表缓存键按 (db, table) 隔离：不同库的同名同日表各自真建。
//
// 直接触达包级 ensuredDailyTables（键为 dailyTableCacheKey{db, table}）：db1 登记过某日表后，
// db2 用同一基表名与同一天调 EnsureDailyTable，不能命中 db1 的缓存而跳过建表，否则 db2 一写即「表不存在」。
func TestDailyTableRolloverCacheKeyIsolatedByDB(t *testing.T) {
	resetDailyTableCacheForTest()
	t.Cleanup(resetDailyTableCacheForTest)
	db1 := openMemSQLite(t, "daily_rollover_iso_1")
	db2 := openMemSQLite(t, "daily_rollover_iso_2")
	day := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)

	name, err := EnsureDailyTable(db1, &model.MetricSampleV2{}, day)
	if err != nil {
		t.Fatalf("db1 建日表失败: %v", err)
	}
	// 前置：新库 db2 上该表尚未存在，后续查出它真被建出，才证明是 EnsureDailyTable 新做的功。
	if db2.Migrator().HasTable(name) {
		t.Fatalf("前置条件不成立：新建库 db2 不应已有日表 %s", name)
	}

	if _, err := EnsureDailyTable(db2, &model.MetricSampleV2{}, day); err != nil {
		t.Fatalf("db2 建日表失败（缓存未按 db 隔离会误命中 db1 缓存而漏建）: %v", err)
	}
	if !db2.Migrator().HasTable(name) {
		t.Fatalf("db2 应各自真建日表 %s", name)
	}

	// 直查缓存：同名日表在两个库应各有一条键（键持有不同 db 身份是隔离的关键）。
	keyCount := 0
	ensuredDailyTables.Range(func(key, _ any) bool {
		if ck, ok := key.(dailyTableCacheKey); ok && ck.table == name && (ck.db == db1 || ck.db == db2) {
			keyCount++
		}
		return true
	})
	if keyCount != 2 {
		t.Fatalf("同名日表 %s 应在两个库各有一条缓存键，实际 %d 条", name, keyCount)
	}

	// 两库的日表功能独立：各写一行只进各自库，互不穿透。
	writeDailySampleRow(t, db1, name, "iso-db1", 1)
	writeDailySampleRow(t, db2, name, "iso-db2", 2)
	if got := len(findDailySampleRows(t, db1, name)); got != 1 {
		t.Fatalf("db1 日表应只有自己写的 1 行，实际 %d", got)
	}
	if got := len(findDailySampleRows(t, db2, name)); got != 1 {
		t.Fatalf("db2 日表应只有自己写的 1 行，实际 %d", got)
	}
}
