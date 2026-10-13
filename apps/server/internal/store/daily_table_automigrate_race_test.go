package store

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestEnsureDailyTableAutoMigrateRaceTolerated 确定性覆盖「并发 AutoMigrate 同一张存量日表」的 DDL 竞态。
//
// 为什么必须覆盖：CI 上实际出现过的失败是
//
//	协程 2 调 EnsureDailyTable 失败: 存量日表 metric_sample_20260713 补列失败:
//	  SQL logic error: index idx_metric_sample_20260713_server_bucket already exists (1)
//
// 成因是多个协程同时判到「索引缺失」、随后都去建索引，后者撞 DDL。
//
// 确定性来自两处，缺一不可：
//   - **多连接池**（openMultiConnFileSQLite 而非 openMemSQLite）：后者池上限为 1，
//     多个 goroutine 实际串行执行 DDL，竞态只在极窄的调度窗口偶发；
//   - **宽 DDL 窗口**（灌 10 万行，见 ddlWindowRows）：建索引耗时从约 2ms 拉到约 160ms，
//     4 条连接必然都先判到「索引缺失」。
//
// 双向验证（本机实测）：去掉容错后连跑 10 轮 → **10/10 失败**，且失败信息与 CI 原始报错逐字一致；
// 恢复容错后连跑 15 轮 → **0 失败**。
func TestEnsureDailyTableAutoMigrateRaceTolerated(t *testing.T) {
	resetDailyTableCacheForTest()
	t.Cleanup(resetDailyTableCacheForTest)

	db, _ := openMultiConnFileSQLite(t, "daily_automigrate_race")
	// 前置：证明池确实多连接，否则本用例退化成串行、竞态不成立（假覆盖）。
	assertPoolIsTrulyMultiConn(t, db, multiConnPoolSize)

	day := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	name := DailyTableName("metric_sample", day)
	createLegacyDailyTable(t, db, name)
	seedDailyTableRows(t, db, name, ddlWindowRows)

	if !db.Migrator().HasTable(name) {
		t.Fatal("前置条件：存量表应已存在")
	}
	// 前置：索引确实缺失，AutoMigrate 才会去建它——这正是竞态的着力点。
	if hasDailyTableIndex(t, db, name) {
		t.Skip("前置条件不成立：驱动已为裸表建出同名索引，本用例无法构造竞态")
	}

	// 并发补索引。协程数取池上限：多出的协程只会排队，不会提高并行度。
	const workers = multiConnPoolSize
	var wg sync.WaitGroup
	start := make(chan struct{}) // 同时起跑，最大化交错概率
	errs := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, errs[idx] = EnsureDailyTable(db, &model.MetricSampleV2{}, day)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("协程 %d 因并发 AutoMigrate 报错（这正是 CI 上出现过的失败形态）：%v", i, err)
		}
	}
	// 容错不等于放弃目标状态：索引必须真的建出来，且表仍可写。
	if !hasDailyTableIndex(t, db, name) {
		t.Fatalf("容错后唯一索引应已补齐（容错放行的前提是目标状态达成）；当前索引：%v",
			dailyTableIndexNames(t, db, name))
	}
	row := &model.MetricSampleV2{ServerID: "race-1", Kind: "backend", BucketStartMs: 1}
	if err := db.Table(name).Create(row).Error; err != nil {
		t.Fatalf("容错后写入应成功：%v", err)
	}
}

// TestEnsureDailyTableStillFailsWhenTableGone 锁定容错**没有掩盖真实失败**：
// 若 AutoMigrate 失败且表确实不存在，必须照旧上抛错误（否则写入会静默失败）。
//
// 这条与上条配对：容错的条件是「复判表仍在」，而不是「无条件吞掉错误」。
func TestEnsureDailyTableStillFailsWhenTableGone(t *testing.T) {
	resetDailyTableCacheForTest()
	t.Cleanup(resetDailyTableCacheForTest)

	db := openMemSQLite(t, "daily_fail_still_errors")
	day := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)

	// 正常路径应成功（先确保基线可用）。
	if _, err := EnsureDailyTable(db, &model.MetricSampleV2{}, day); err != nil {
		t.Fatalf("基线建表应成功：%v", err)
	}

	// 关闭底层连接后调用：复判 HasTable 也会失败/返回 false，故必须上抛而非静默成功。
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取连接池失败：%v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("关闭连接池失败：%v", err)
	}
	resetDailyTableCacheForTest() // 清缓存，强制重新走 DDL 路径
	_, err = EnsureDailyTable(db, &model.MetricSampleV2{}, day)
	if err == nil {
		t.Fatal("连接已关闭时应上抛错误，而不是静默成功（否则后续写入会失败且无迹可循）")
	}
	// 错误信息应可诊断（含表名或关闭连接的原因）。
	if !strings.Contains(err.Error(), "metric_sample") && !strings.Contains(err.Error(), "closed") {
		t.Logf("（错误信息：%v——建议含表名或底层原因以便定位）", err)
	}
}
