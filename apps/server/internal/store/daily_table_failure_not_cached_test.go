package store

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestEnsureDailyTableFailureNotCached 锁定「补表失败时不得写进进程缓存」。
//
// 为什么单独锁这条：EnsureDailyTable 在成功后会把表名记进 ensuredDailyTables，
// 之后同一库同一表直接短路。这要求「失败路径绝不能登记」——否则一次瞬时故障
// （磁盘满、库短暂只读）会让该表在本进程内**永久**被当作已确保，
// 后续写入全部失败且不再重试、也不再有任何建表尝试的痕迹。
//
// 与 TestEnsureDailyTableSurfacesRealDDLFailure 配对：那条锁「必须上抛」，这条锁「上抛后不留缓存」。
// 两者合起来才完整覆盖失败语义——只锁其一，都可能留下「报错但仍被缓存」的漏洞。
//
// 手法：sqlite 的 query_only 模式确定性制造「表在、写入被拒」，调用一次失败后关掉只读，
// 再调用一次应当重新走 DDL 并成功（若失败被误缓存，第二次会直接短路返回成功，
// 表结构仍是残缺的——故断言索引是否真的补齐，而不只看返回值）。
func TestEnsureDailyTableFailureNotCached(t *testing.T) {
	resetDailyTableCacheForTest()
	t.Cleanup(resetDailyTableCacheForTest)

	// 池上限为 1，故 PRAGMA 能稳定作用于唯一那条连接。
	db := openMemSQLite(t, "daily_failure_not_cached")
	day := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	name := DailyTableName("metric_sample", day)

	// 存量表缺唯一索引：AutoMigrate 必然尝试写入。
	createLegacyDailyTable(t, db, name)

	if err := db.Exec(`PRAGMA query_only=ON`).Error; err != nil {
		t.Fatalf("开启 query_only 失败：%v", err)
	}
	if _, err := EnsureDailyTable(db, &model.MetricSampleV2{}, day); err == nil {
		t.Fatal("只读库上应上抛错误")
	}

	// 恢复可写，再次调用：必须重新执行 DDL（而非命中失败时误写的缓存）。
	if err := db.Exec(`PRAGMA query_only=OFF`).Error; err != nil {
		t.Fatalf("关闭 query_only 失败：%v", err)
	}
	if _, err := EnsureDailyTable(db, &model.MetricSampleV2{}, day); err != nil {
		t.Fatalf("故障恢复后重试应成功：%v", err)
	}
	// 关键断言：看真实 DDL 状态，而非返回值——失败若被误缓存，此处索引仍然缺失。
	if !hasDailyTableIndex(t, db, name) {
		t.Fatalf("故障恢复后唯一索引应已补齐——索引缺失说明失败时把表误登记进了缓存，"+
			"第二次调用直接短路、根本没重试 DDL；当前索引：%v",
			dailyTableIndexNames(t, db, name))
	}
}
