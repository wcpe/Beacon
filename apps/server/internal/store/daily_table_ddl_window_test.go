package store

import (
	"testing"

	"gorm.io/gorm"
)

// 本文件存放「日表 DDL 竞态」系列用例的共享夹具：撑宽 DDL 窗口所需的数据构造。
//
// 原为一次性的耗时探针（测「建索引耗时 vs 行数」），结论沉淀为 ddlWindowRows 的取值依据后，
// 按用户建议以改写方式保留为正式夹具，而非手工删除。

// ddlWindowRows 是「撑宽 DDL 窗口」用例灌入存量表的行数。
//
// 为什么需要灌这么多行：竞态能否命中，取决于「协程 A 开始建索引」到「协程 B 判存」之间的重叠。
// 本机实测（池上限 4，与生产默认一致）：
//
//	1000 行   → 建索引约 2ms   → 重叠靠调度碰运气（实测 10 轮只失败 7 轮）
//	10 万行   → 建索引约 160ms → 4 条连接必然都先判到「索引缺失」，稳定命中
//
// 即：**窗口宽度由数据量决定，而不是由协程数决定**。曾把协程从 8 提到 32 却毫无改善——
// 池上限才是并行度天花板，多出的协程只是排队。
//
// 10 万行也贴近真实：生产日表按 1000 台 × 分钟粒度采集，一天就是百万行量级，
// 「存量表补索引」本就发生在有数据的表上。
const ddlWindowRows = 100000

// createLegacyDailyTable 建出一张「结构完整但缺唯一索引」的存量日表。
//
// 模拟旧版二进制建出的历史日表：当时模型还没声明 server_bucket 唯一索引，
// 升级后首次触达即由 EnsureDailyTable 走 AutoMigrate 分支补索引。
func createLegacyDailyTable(t *testing.T, db *gorm.DB, name string) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE ` + name + ` (
		id integer PRIMARY KEY AUTOINCREMENT,
		namespace_id integer NOT NULL,
		server_id text NOT NULL,
		kind text NOT NULL,
		bucket_start_ms integer NOT NULL,
		sample_count integer NOT NULL DEFAULT 0,
		cpu_pct_avg real NOT NULL DEFAULT 0,
		cpu_pct_max real NOT NULL DEFAULT 0,
		mem_used_mb_avg real NOT NULL DEFAULT 0,
		mem_max_mb integer NOT NULL DEFAULT 0,
		tps_avg real NOT NULL DEFAULT 0,
		tps_min real NOT NULL DEFAULT 0,
		online_avg integer NOT NULL DEFAULT 0,
		online_max integer NOT NULL DEFAULT 0,
		max_online integer NOT NULL DEFAULT 0,
		conn_avg integer NOT NULL DEFAULT 0,
		conn_max integer NOT NULL DEFAULT 0,
		backend_up integer NOT NULL DEFAULT 0,
		backend_total integer NOT NULL DEFAULT 0,
		backend_rtt_ms_avg real NOT NULL DEFAULT -1,
		report_rtt_ms integer NOT NULL DEFAULT -1,
		created_at datetime
	)`).Error; err != nil {
		t.Fatalf("构造存量表失败：%v", err)
	}
}

// seedDailyTableRows 往存量表灌 rows 行，用于撑宽建索引窗口。
//
// 用递归 CTE 而非 generate_series：后者在 sqlite 上不可用，且**静默插 0 行**——
// 窗口不会变宽，用例会悄悄退化成靠调度碰运气。
func seedDailyTableRows(t *testing.T, db *gorm.DB, name string, rows int) {
	t.Helper()
	if err := db.Exec(`WITH RECURSIVE seq(v) AS (SELECT 1 UNION ALL SELECT v+1 FROM seq WHERE v<?)
		INSERT INTO `+name+` (namespace_id, server_id, kind, bucket_start_ms)
		SELECT 1, 'srv-'||(v%2000), 'backend', v FROM seq`, rows).Error; err != nil {
		t.Fatalf("灌入存量数据失败：%v", err)
	}
	var got int64
	if err := db.Table(name).Count(&got).Error; err != nil {
		t.Fatalf("统计存量行数失败：%v", err)
	}
	// 前置：数据必须真的灌进去，否则窗口未撑宽、用例退化成靠运气（假覆盖）。
	if got != int64(rows) {
		t.Fatalf("前置条件不成立：存量表应有 %d 行，实际 %d——窗口未撑宽", rows, got)
	}
}
