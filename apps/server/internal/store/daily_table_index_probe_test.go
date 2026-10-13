package store

import (
	"testing"

	"gorm.io/gorm"
)

// dailyTableIndexNames 返回某张表上实际存在的索引名（直接查 sqlite_master）。
//
// 为什么不用 db.Migrator().HasIndex(&model.MetricSampleV2{}, idx)：
// Migrator 会用**模型自报的基表名**（metric_sample）去查，而不是日表名
// （metric_sample_20260713），于是无论日表上有没有索引都返回 false。
//
// 用它会得到双重假信号：前置检查「索引缺失」恒成立（跳过条件永不触发），
// 后置断言「索引已补齐」恒失败。本用例要断言的恰恰是真实 DDL 状态，
// 故直接读 sqlite_master，不经任何 GORM 语义。
//
// 这不是假设——本文件诞生于一次真实的自我误导：曾据此以为宽窗口用例
// 「10/10 失败」是竞态检出，实际是断言写错了。故把观测方式单列出来并写明理由。
func dailyTableIndexNames(t *testing.T, db *gorm.DB, table string) []string {
	t.Helper()
	var names []string
	if err := db.Raw(`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name=? ORDER BY name`, table).
		Scan(&names).Error; err != nil {
		t.Fatalf("读取 %s 的索引列表失败：%v", table, err)
	}
	return names
}

// hasDailyTableIndex 报告日表上是否存在模型声明的唯一索引（名为 idx_<日表名>_server_bucket）。
func hasDailyTableIndex(t *testing.T, db *gorm.DB, table string) bool {
	t.Helper()
	want := "idx_" + table + "_server_bucket"
	for _, n := range dailyTableIndexNames(t, db, table) {
		if n == want {
			return true
		}
	}
	return false
}
