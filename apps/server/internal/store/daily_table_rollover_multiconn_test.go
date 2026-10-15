package store

// 本文件为「多连接 sqlite 写锁竞争」补**真实**覆盖，替换 daily_table_rollover_test.go 里那层只有
// 结构性保证的假覆盖。
//
// 为什么必须另开被测池：
//
//	同包既有的跨日并发用例（daily_table_rollover_test.go 的 TestDailyTableRolloverConcurrentCrossDayWrites）
//	走的是 openMemSQLite（daily_table_test.go:14），池是**内存库 + MaxOpenConns=1**。
//	单连接池下多条写语句在 Go 层就被串起来了，`database is locked` 不可能出现——「并发跨日写入无锁竞争」
//	在该配置下是**结构性必然**，不是被测行为。那条断言因此是假保证：把 EnsureDailyTable / 写入路径改成
//	多连接下必然抢锁的样子，它照样全绿。
//	本文件改用**真实文件库 + MaxOpenConns=4**，并且先用 assertPoolIsTrulyMultiConn 证明池确实能同时
//	承载 4 条在途连接，让写锁竞争成为真会发生的事；反向验证（见下方「判别力」）确认拆除前提后必红。
//
// 三个用例全部走 store.Open 的生产装配（自动注入 WAL + busy_timeout(5000) + _txlock=immediate）：
//
//	1. TestDailyTableRolloverMultiConnReadThenWrite
//	   事务内「先读后写」跨两张日表 → 零 SQLITE_BUSY / 零丢写。
//	   这是 WAL 下最危险的写锁升级路径，也是 db.go:428-453 applySQLiteTxLock 存在的唯一理由。
//	2. TestDailyTableRolloverMultiConnConcurrentCrossDayEnsureAndWrite
//	   8 协程分摊到 3 个 UTC 日：同一张未建日表被多协程并发建（DDL 竞态）+ 写入。
//	   覆盖「同一天被多个协程共享」与「不同天并存」两种形态 → 零锁冲突、行数精确、无串表。
//	3. TestDailyTableRolloverMultiConnCrossUTCMidnightBoundary
//	   写入键横跨 D 与 D+1（相邻 1ms 分属两天），多连接下跨日拆分不串表、不丢行。
//
// 判别力（已反向验证，两轴各拆一次，拆完均**恢复原状**；均为 go test -race -count=1 实测）：
//
//	A. 把 _txlock=immediate 注入去掉（newDialector 的 sqlite 分支只调 applySQLitePragmas，
//	   即退回驱动默认 DEFERRED）：
//	   - TestDailyTableRolloverMultiConnReadThenWrite：**失败 17–18 次**（两次采样 17 / 18，共 32 个事务），
//	     指纹全为 `database is locked (5) (SQLITE_BUSY)`，出现位置正是「事务内后写日表」那一步；
//	   - 另两个用例仍绿——它们的事务体内只有纯 INSERT，没有「读锁升级为写锁」，而纯写争用由
//	     busy_timeout(5000) 就能等到写锁释放。故本文件对 _txlock 的判别力**集中在用例 1**，
//	     这与 db.go:436-438 记的形态一致：升级失败才是 immediate 唯一能救的那一类。
//	   恢复注入后：0 次失败，全绿。
//
//	B. 去掉 EnsureDailyTable 的并发建表容错复判（daily_table.go:62-67 的「建表报错后再 HasTable 一次」）：
//	   - TestDailyTableRolloverMultiConnConcurrentCrossDayEnsureAndWrite：**失败 5 次**，
//	     指纹 `table metric_sample_20260711 already exists (1)`——同行共享同一张未建日表的 DDL 竞态被真实踩到；
//	   - TestDailyTableRolloverMultiConnCrossUTCMidnightBoundary：同时转红；
//	   - （同包内存用例 TestDailyTableRolloverConcurrentCrossDayWrites 也一起转红，说明该竞态与池大小无关，
//	     但**能否观察到它**取决于多连接——单连接池下建表被串行化，复判永远不会被走到。）
//	   恢复后：全绿。
//
// 由此确认：用例 1 锁的是 _txlock=immediate，用例 2/3 锁的是并发建表幂等语义，均非结构性必然。
//
// C. 另试了第三轴——去掉 applySQLitePragmas 的 busy_timeout(5000) 注入：**本文件三个用例仍全绿**。
// 原因已实测：glebarez/modernc 驱动在未注入时 `PRAGMA busy_timeout` 本就返回 5000ms（对裸文件名、
// 仅 WAL 两种 DSN 均实测 5000），故该注入在默认路径上是**冗余**的。据此不把 busy_timeout 谎称成
// 本文件的判别点：本文件对锁竞争的真实判别力来自 _txlock=immediate（用例 1）与建表幂等（用例 2/3）。

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// multiConnPoolSize 是本文件各用例的池上限。取 4（= db.go 注释里实测写锁竞争的同一口径）：
// 既足够让多条写语句同时在途，又不会把 sqlite 单写者模型下的等待时间拖长。
const multiConnPoolSize = 4

// openMultiConnFileSQLite 打开「真实文件 + 多连接」sqlite 库，返回连接与文件路径。
//
// 与 openMemSQLite 的两点差异正是本文件存在的理由：
//   - 真实文件（而非 mode=memory）：WAL 与文件写锁在真实文件上才成立；
//   - MaxOpenConns=4（而非 1）：并发写语句才能同时在途，写锁竞争才可能真的发生。
//
// 走 store.Open 生产装配，故 DSN 会经 applySQLitePragmas + applySQLiteTxLock 注入
// WAL / busy_timeout(5000) / _txlock=immediate——被测前提与实际部署一致。
func openMultiConnFileSQLite(t *testing.T, name string) (*gorm.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".db")
	db, err := Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: path,
		MaxOpenConns: multiConnPoolSize, MaxIdleConns: multiConnPoolSize, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开多连接文件 sqlite 失败: %v", err)
	}
	t.Cleanup(func() { Close(db) })
	return db, path
}

// assertPoolIsTrulyMultiConn 断言池真的能同时承载 want 条在途连接。
//
// 为什么每个用例都要先跑这道前置：本文件存在的理由就是「池只有一条连接时，无写锁竞争是结构性保证、
// 用例是假覆盖」。若新用例的池因任何原因（MaxOpenConns 被改回 1、驱动忽略池参数、包装层把连接数
// 压成 1）退化为单连接，并发竞争就又一次变成不可能发生的事，用例会**重新**变成假覆盖——而且照样全绿。
// 故这里直接证明：并发取到 want 条连接并让它们同时在途，而非只看配置字段。
func assertPoolIsTrulyMultiConn(t *testing.T, db *gorm.DB, want int) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取底层连接池失败: %v", err)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != want {
		t.Fatalf("池上限应为 %d，实际 %d——池不是多连接池，本用例的并发写锁竞争不成立（假覆盖）", want, got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	got := make(chan *sql.Conn, want)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < want; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, connErr := sqlDB.Conn(ctx)
			if connErr != nil {
				return // 取不到即不计入，下面的计数会暴露不足
			}
			got <- conn
			<-release // 持住连接，保证「同时在途」而不是取完即还
			_ = conn.Close()
		}()
	}
	inFlight := 0
	for inFlight < want {
		select {
		case <-got:
			inFlight++
		case <-ctx.Done():
			close(release)
			wg.Wait()
			t.Fatalf("20s 内只并发取到 %d/%d 条连接——池实际退化为单连接，"+
				"此时「无 database is locked」是结构性必然而非被测行为（假覆盖）", inFlight, want)
		}
	}
	close(release)
	wg.Wait()
}

// sqliteWriteConflict 判定错误是否属于 sqlite 写锁竞争（本文件最关心的失败形态）。
// 覆盖两种指纹：裸写锁失败（database is locked）与升级快照失败（SQLITE_BUSY / SQLITE_BUSY_SNAPSHOT）。
func sqliteWriteConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "sqlite_busy") ||
		strings.Contains(msg, "table is locked")
}

// failOnWriteErrors 汇总并发协程的错误；写锁竞争单列并说明它证明什么，其余错误原样报出。
func failOnWriteErrors(t *testing.T, errs []error, scenario string) {
	t.Helper()
	if len(errs) == 0 {
		return
	}
	conflicts := 0
	for _, err := range errs {
		if sqliteWriteConflict(err) {
			conflicts++
		}
	}
	if conflicts > 0 {
		t.Fatalf("%s：出现 %d 次 sqlite 写锁竞争失败（共 %d 个错误，示例：%v）。\n"+
			"这证明多连接池下的写锁竞争未被消化：若 _txlock=immediate 注入失效，DEFERRED 事务「读后写」\n"+
			"在 WAL 下会立即返回 SQLITE_BUSY/BUSY_SNAPSHOT 且**完全不理会** busy_timeout；若 busy_timeout 注入\n"+
			"失效，连「等写锁释放」的能力也一并丢掉。两条路都会让 UTC 00:00 切表窗口随机丢写。",
			scenario, conflicts, len(errs), errs[0])
	}
	t.Fatalf("%s：并发写失败 %d 次（非写锁竞争，示例：%v）", scenario, len(errs), errs[0])
}

// utcDayOfBucket 把桶起点毫秒时间戳转成用于拼日表名的时刻（UTC）。
//
// 与生产侧 groupRowsByDay 的 utcDayStart 同义：DailyTableName 只取 day.UTC() 的年月日，
// 故 time.UnixMilli(ms).UTC() 与「归到当日零点」拼出的表名完全一致。
func utcDayOfBucket(bucketStartMs int64) time.Time {
	return time.UnixMilli(bucketStartMs).UTC()
}

// assertRowsBelongToTheirTable 断言日表内每行都落在「其 bucket_start_ms 所属 UTC 日」对应的日表里。
// 直接锁定跨日拆分语义（groupRowsByDay → DailyTableName）：错落一天不报错、也不丢行，只有本断言能抓。
func assertRowsBelongToTheirTable(t *testing.T, db *gorm.DB, table, scenario string) {
	t.Helper()
	for _, row := range findDailySampleRows(t, db, table) {
		want := DailyTableName("metric_sample", utcDayOfBucket(row.BucketStartMs))
		if table != want {
			t.Fatalf("%s：行 server_id=%s bucket_start_ms=%d 按 UTC 日应落 %s，实际落在 %s——跨日拆分串表",
				scenario, row.ServerID, row.BucketStartMs, want, table)
		}
	}
}

// assertDailyRowsMatchPrefixes 断言日表内每行 server_id 都命中给定前缀之一（抓跨日拆分串表 / 丢行后错表补齐）。
func assertDailyRowsMatchPrefixes(t *testing.T, db *gorm.DB, table string, prefixes []string, scenario string) {
	t.Helper()
	for _, row := range findDailySampleRows(t, db, table) {
		matched := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(row.ServerID, prefix) {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("%s：日表 %s 混入不属于该天的行（server_id=%s，本表只应有前缀 %v 的行）——跨日拆分串表",
				scenario, table, row.ServerID, prefixes)
		}
	}
}

// TestDailyTableRolloverMultiConnReadThenWrite 校验多连接池下「事务内先读后写」跨两张日表零失败、零丢写。
//
// 这是把 db.go:436-438 那句「MaxOpenConns=4、120 个事务：默认 DEFERRED 失败约 100–107/120，
// 加 _txlock=immediate 后 0/120」从注释变成**机器可验证判据**的用例。
//
// 为什么形状选「先读后写」而不是纯写：纯写在 sqlite 下由 busy_timeout 就能等到写锁释放，不构成陷阱；
// 真正的陷阱是 DEFERRED 事务先读到快照、再想把读锁升级为写锁——WAL 模式下该升级**不可等待**，
// 一旦另一事务已改过该页就立刻 SQLITE_BUSY/BUSY_SNAPSHOT。生产里 UTC 00:00 切表窗口正是这个形状：
// 先查当日表状态 / 已落批，再插下一批。故这里刻意在读写之间插 1ms，把窗口撑开。
func TestDailyTableRolloverMultiConnReadThenWrite(t *testing.T) {
	resetDailyTableCacheForTest()
	t.Cleanup(resetDailyTableCacheForTest)
	db, _ := openMultiConnFileSQLite(t, "daily_multiconn_rw")
	assertPoolIsTrulyMultiConn(t, db, multiConnPoolSize)

	// 三个 UTC 日各一张日表，复刻午夜窗口里「多个批次同时写不同日表」的形态。
	baseDay := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	tables := make([]string, 0, 3)
	for d := 0; d < 3; d++ {
		name, err := EnsureDailyTable(db, &model.MetricSampleV2{}, baseDay.AddDate(0, 0, d))
		if err != nil {
			t.Fatalf("建第 %d 张日表失败: %v", d, err)
		}
		tables = append(tables, name)
	}

	const roundsPerWriter = 8
	// 每个协程在事务内访问的日表由 (worker+k) % 3 决定（k=0,1 两张），即「一个跨日批打两张日表」。
	// 该分摊是期望值的唯一真源：预置行与末尾断言都按它推导，避免两者各写一套取模规则后悄悄错配。
	workerTables := make([][]string, multiConnPoolSize)
	for w := 0; w < multiConnPoolSize; w++ {
		for k := 0; k < 2; k++ {
			workerTables[w] = append(workerTables[w], tables[(w+k)%len(tables)])
		}
	}
	// 初始行：每个协程在其会访问的每张日表各一行。
	for w := 0; w < multiConnPoolSize; w++ {
		for _, table := range workerTables[w] {
			writeDailySampleRow(t, db, table, fmt.Sprintf("rw-w%d", w), 1000)
		}
	}

	var (
		mu      sync.Mutex
		errs    []error
		startWG sync.WaitGroup
	)
	startWG.Add(multiConnPoolSize)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < multiConnPoolSize; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			serverID := fmt.Sprintf("rw-w%d", worker)
			startWG.Done()
			<-start // 同时起跑，避免串行执行把竞争蒸发掉
			for round := 0; round < roundsPerWriter; round++ {
				err := db.Transaction(func(tx *gorm.DB) error {
					// 一个事务内跨两张日表「先读后写」，与 FlushDaily「单事务内逐日批插」同形。
					for _, table := range workerTables[worker] {
						var row model.MetricSampleV2
						if readErr := tx.Table(table).Where("server_id = ?", serverID).Take(&row).Error; readErr != nil {
							return fmt.Errorf("事务内先读日表 %s 失败: %w", table, readErr)
						}
						time.Sleep(time.Millisecond) // 撑开读与写之间的窗口，逼出写锁升级竞争
						if writeErr := tx.Table(table).
							Where("server_id = ?", serverID).
							Update("sample_count", row.SampleCount+1).Error; writeErr != nil {
							return fmt.Errorf("事务内后写日表 %s 失败: %w", table, writeErr)
						}
					}
					return nil
				})
				if err != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("协程 %d 第 %d 轮: %w", worker, round, err))
					mu.Unlock()
				}
			}
		}(w)
	}
	startWG.Wait()
	close(start)
	wg.Wait()

	failOnWriteErrors(t, errs, "多连接池下并发「先读后写」事务")

	// 零丢写：每个 (协程, 其访问过的日表) 的行都被精确自增 roundsPerWriter 次。
	for w := 0; w < multiConnPoolSize; w++ {
		for _, table := range workerTables[w] {
			var row model.MetricSampleV2
			if err := db.Table(table).Where("server_id = ?", fmt.Sprintf("rw-w%d", w)).Take(&row).Error; err != nil {
				t.Fatalf("回读 %s 的 server_id=rw-w%d 失败: %v", table, w, err)
			}
			if row.SampleCount != roundsPerWriter {
				t.Fatalf("%s 的 server_id=rw-w%d 自增次数应恰为 %d，实际 %d——有「先读后写」事务未落地（丢写）",
					table, w, roundsPerWriter, row.SampleCount)
			}
		}
	}
}

// TestDailyTableRolloverMultiConnConcurrentCrossDayEnsureAndWrite 校验多连接池下并发跨日建表 + 写入。
//
// 覆盖两种形态：**同一天被多个协程共享**（对同一张未建日表并发建 → DDL 竞态，要靠 EnsureDailyTable
// 的 IF NOT EXISTS 语义 + 建表后复判兜住）与**不同天并存**。同时起跑，保证建表竞态真的同时发生，
// 而不是被串行执行掩掉。
func TestDailyTableRolloverMultiConnConcurrentCrossDayEnsureAndWrite(t *testing.T) {
	resetDailyTableCacheForTest()
	t.Cleanup(resetDailyTableCacheForTest)
	db, _ := openMultiConnFileSQLite(t, "daily_multiconn_crossday")
	assertPoolIsTrulyMultiConn(t, db, multiConnPoolSize)

	const (
		writers       = 8
		rowsPerWriter = 6
	)
	baseDay := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	daySpan := []time.Time{baseDay, baseDay.AddDate(0, 0, 1), baseDay.AddDate(0, 0, 2)}

	// 前置：确认分摊形态确实同时具备两种形态，否则本用例的覆盖面会静默退化。
	sharedDays := 0
	for idx := range daySpan {
		onDay := 0
		for w := 0; w < writers; w++ {
			if w%len(daySpan) == idx {
				onDay++
			}
		}
		if onDay >= 2 {
			sharedDays++
		}
	}
	if len(daySpan) < 2 || sharedDays == 0 {
		t.Fatalf("前置条件不成立：需要「不同天并存」+「某天被多个协程共享」，实际天数 %d、共享天数 %d",
			len(daySpan), sharedDays)
	}

	var (
		mu      sync.Mutex
		errs    []error
		startWG sync.WaitGroup
	)
	startWG.Add(writers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			day := daySpan[worker%len(daySpan)]
			startWG.Done()
			<-start // 同时起跑：让同一张未建日表的 DDL 竞态真的同时发生
			table, err := EnsureDailyTable(db, &model.MetricSampleV2{}, day)
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("协程 %d 并发建日表失败: %w", worker, err))
				mu.Unlock()
				return
			}
			if want := DailyTableName("metric_sample", day); table != want {
				mu.Lock()
				errs = append(errs, fmt.Errorf("协程 %d 得到日表名 %s，应为 %s", worker, table, want))
				mu.Unlock()
				return
			}
			for r := 0; r < rowsPerWriter; r++ {
				// serverID 与 bucket 按协程唯一：绕开 (server_id, bucket_start_ms) 唯一键，聚焦切表路径。
				row := &model.MetricSampleV2{
					ServerID:      fmt.Sprintf("w%d-r%d", worker, r),
					Kind:          "backend",
					BucketStartMs: int64(worker)*1000 + int64(r),
				}
				if err := db.Table(table).Create(row).Error; err != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("协程 %d 写日表 %s 失败: %w", worker, table, err))
					mu.Unlock()
					return
				}
			}
		}(w)
	}
	startWG.Wait()
	close(start)
	wg.Wait()

	failOnWriteErrors(t, errs, "多连接池下并发跨日建表 + 写入")

	// 行数精确 + 无串表：每张日表 = 落在该天的协程数 × 每协程行数，且每行都属于该天的协程。
	for idx, day := range daySpan {
		onDay := []int{}
		prefixes := []string{}
		for w := 0; w < writers; w++ {
			if w%len(daySpan) == idx {
				onDay = append(onDay, w)
				prefixes = append(prefixes, fmt.Sprintf("w%d-", w))
			}
		}
		table := DailyTableName("metric_sample", day)
		want := len(onDay) * rowsPerWriter
		if got := len(findDailySampleRows(t, db, table)); got != want {
			t.Fatalf("%s 应有 %d 行（协程 %v 各 %d 行），实际 %d——有行丢失或落错表",
				table, want, onDay, rowsPerWriter, got)
		}
		assertDailyRowsMatchPrefixes(t, db, table, prefixes, "并发跨日建表 + 写入")
	}
}

// TestDailyTableRolloverMultiConnCrossUTCMidnightBoundary 校验多连接池下跨真实 UTC 日界的写入不串表、不丢行。
//
// 日界用**相邻 1ms** 构造：D 日最后一毫秒与 D+1 日第一毫秒分属两天、却几乎同一时刻，
// 是「按 bucket 归日再拆表」最容易错落一天的形态。每个协程在一个事务内跨两张日表写入
// （与 FlushDaily 单事务逐日批插一致），可同时压到跨日拆分与写锁竞争两条路径。
func TestDailyTableRolloverMultiConnCrossUTCMidnightBoundary(t *testing.T) {
	resetDailyTableCacheForTest()
	t.Cleanup(resetDailyTableCacheForTest)
	db, _ := openMultiConnFileSQLite(t, "daily_multiconn_midnight")
	assertPoolIsTrulyMultiConn(t, db, multiConnPoolSize)

	dayD := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	dayDNext := dayD.AddDate(0, 0, 1)
	bucketLastOfDayD := dayDNext.UnixMilli() - 1 // D 日 23:59:59.999
	bucketFirstOfDayNext := dayDNext.UnixMilli() // D+1 日 00:00:00.000

	// 前置：锁定「相邻 1ms 却分属两天」这一构造，避免日后改坏日期后用例静默失去覆盖。
	if bucketFirstOfDayNext-bucketLastOfDayD != 1 {
		t.Fatalf("前置条件不成立：两个桶应仅相差 1ms，实际相差 %dms", bucketFirstOfDayNext-bucketLastOfDayD)
	}
	if got := utcDayOfBucket(bucketLastOfDayD).Format(dailyTableSuffixLayout); got != "20260711" {
		t.Fatalf("前置条件不成立：日界前 1ms 应归 UTC 日 20260711，实际 %s", got)
	}
	if got := utcDayOfBucket(bucketFirstOfDayNext).Format(dailyTableSuffixLayout); got != "20260712" {
		t.Fatalf("前置条件不成立：日界首毫秒应归 UTC 日 20260712，实际 %s", got)
	}

	// 每个协程写 2 行：一行贴日界前（D），一行贴日界后（D+1）；表名由桶的 UTC 日推出，不走 time.Now。
	type rowPlan struct {
		serverID string
		bucket   int64
		table    string
	}
	plans := make([][]rowPlan, multiConnPoolSize)
	for w := 0; w < multiConnPoolSize; w++ {
		plans[w] = []rowPlan{
			{serverID: fmt.Sprintf("mid-w%d-d0", w), bucket: bucketLastOfDayD - int64(w)*1000},
			{serverID: fmt.Sprintf("mid-w%d-d1", w), bucket: bucketFirstOfDayNext + int64(w)*1000},
		}
		for i := range plans[w] {
			plans[w][i].table = DailyTableName("metric_sample", utcDayOfBucket(plans[w][i].bucket))
		}
	}
	tableD, tableNext := plans[0][0].table, plans[0][1].table
	if tableD == tableNext {
		t.Fatalf("日界两侧应落两张不同日表，实际同为 %s", tableD)
	}

	var (
		mu      sync.Mutex
		errs    []error
		startWG sync.WaitGroup
	)
	startWG.Add(multiConnPoolSize)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < multiConnPoolSize; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			startWG.Done()
			<-start // 同时起跑：让两张日表的并发建表与跨日事务真的重叠
			for _, plan := range plans[worker] {
				// 步骤一：事务外按需建表（DDL 隐式提交，必须离开事务，与 FlushDaily 一致）。
				name, err := EnsureDailyTable(db, &model.MetricSampleV2{}, utcDayOfBucket(plan.bucket))
				if err != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("协程 %d 建日表（bucket=%d）失败: %w", worker, plan.bucket, err))
					mu.Unlock()
					return
				}
				if name != plan.table {
					mu.Lock()
					errs = append(errs, fmt.Errorf("协程 %d 建出的日表名 %s 与按 UTC 日推出的 %s 不符", worker, name, plan.table))
					mu.Unlock()
					return
				}
			}
			// 步骤二：一个事务内跨日写入两行（跨日拆分与写锁竞争同时压到）。
			err := db.Transaction(func(tx *gorm.DB) error {
				for _, plan := range plans[worker] {
					row := &model.MetricSampleV2{ServerID: plan.serverID, Kind: "backend", BucketStartMs: plan.bucket}
					if createErr := tx.Table(plan.table).Create(row).Error; createErr != nil {
						return fmt.Errorf("写入日表 %s 失败: %w", plan.table, createErr)
					}
				}
				return nil
			})
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("协程 %d 跨日事务失败: %w", worker, err))
				mu.Unlock()
			}
		}(w)
	}
	startWG.Wait()
	close(start)
	wg.Wait()

	failOnWriteErrors(t, errs, "多连接池下跨真实 UTC 日界的并发写")

	// 行数精确 + 归属精确：每张日表恰好收到各协程 1 行，且每行的 bucket 都归属本表那一天。
	for _, tc := range []struct {
		table  string
		suffix string
	}{
		{tableD, "-d0"},
		{tableNext, "-d1"},
	} {
		prefixes := make([]string, 0, multiConnPoolSize)
		for w := 0; w < multiConnPoolSize; w++ {
			prefixes = append(prefixes, fmt.Sprintf("mid-w%d%s", w, tc.suffix))
		}
		if got := len(findDailySampleRows(t, db, tc.table)); got != multiConnPoolSize {
			t.Fatalf("%s 应收到 %d 行（每协程 1 行），实际 %d——跨日拆分丢行或落错表",
				tc.table, multiConnPoolSize, got)
		}
		assertDailyRowsMatchPrefixes(t, db, tc.table, prefixes, "跨真实 UTC 日界的并发写")
		assertRowsBelongToTheirTable(t, db, tc.table, "跨真实 UTC 日界的并发写")
	}
}
