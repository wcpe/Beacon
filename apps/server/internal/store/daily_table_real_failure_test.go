package store

import (
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestEnsureDailyTableSurfacesRealDDLFailure 锁定「容错只放行并发撞车，绝不吞掉真实失败」。
//
// 为什么这条必不可少：并发撞车用例（TestEnsureDailyTableAutoMigrateRaceTolerated）无法区分两种修法——
//
//	A. 报错后判存（`if err != nil && !HasTable(name)`）——能过并发用例；
//	B. 报错后重跑一次——也能过并发用例。
//
// 但两者语义天差地别：A 在该分支恒为「放行」——因为 AutoMigrate 分支**只在表已存在时进入**，
// 判存恒为真，等于无条件吞掉所有错误。于是磁盘满 / 只读库 / 类型冲突这类真实故障被误报成成功，
// 还会把「已确保」写进进程缓存，导致该表在本进程内再不复检、后续写入持续失败且无迹可循。
//
// 本用例用 sqlite 的 query_only 模式确定性复现「表在、但写入被拒」：
// 这正是 A 会误判、而 B 会如实上抛的情形，从而把 A 钉死。
func TestEnsureDailyTableSurfacesRealDDLFailure(t *testing.T) {
	resetDailyTableCacheForTest()
	t.Cleanup(resetDailyTableCacheForTest)

	// 池上限为 1，故 PRAGMA query_only 能稳定作用于唯一那条连接。
	db := openMemSQLite(t, "daily_readonly")
	day := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	name := DailyTableName("metric_sample", day)

	// 存量表缺唯一索引：AutoMigrate 必然尝试写入（建索引）。
	createLegacyDailyTable(t, db, name)

	if err := db.Exec(`PRAGMA query_only=ON`).Error; err != nil {
		t.Fatalf("设置 query_only 失败：%v", err)
	}
	// 前置：证明此刻确实是「表在但写不了」——A 修法会在此误判为成功。
	if !db.Migrator().HasTable(name) {
		t.Fatal("前置条件不成立：存量表应存在（A 修法正是靠这一点误判）")
	}
	if err := db.Exec(`CREATE INDEX probe_should_fail ON ` + name + `(server_id)`).Error; err != nil {
		if !strings.Contains(err.Error(), "readonly") {
			t.Fatalf("前置条件不成立：写入应因只读被拒，实际：%v", err)
		}
	} else {
		t.Fatal("前置条件不成立：只读库上写入竟然成功了，本用例无法验证吞错")
	}

	_, err := EnsureDailyTable(db, &model.MetricSampleV2{}, day)
	if err == nil {
		t.Fatal("只读库上补列必须上抛错误——静默成功会让后续写入持续失败且无迹可循" +
			"（注意：此处若用「报错后判存再放行」，因表已存在，判存恒为真，等于无条件吞错）")
	}
	if !strings.Contains(err.Error(), "readonly") && !strings.Contains(err.Error(), "readonly database") {
		t.Logf("（错误信息：%v——应含底层只读原因以便定位）", err)
	}
}
