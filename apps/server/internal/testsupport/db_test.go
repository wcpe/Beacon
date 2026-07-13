package testsupport

import "testing"

// TestHasDailySuffix 穷举日表后缀判定：合法 _YYYYMMDD 命中，长度 / 非日期 / 无下划线不命中。
func TestHasDailySuffix(t *testing.T) {
	cases := []struct {
		table string
		want  bool
	}{
		{"sched_decision_20260712", true},
		{"metric_sample_20251231", true},
		{"msg_payload_20260101", true},
		{"sched_decision", false},          // 无日期后缀
		{"config_item", false},             // 普通表
		{"metric_sample_2026071", false},   // 7 位
		{"metric_sample_202607125", false}, // 9 位
		{"sched_decision_20261340", false}, // 非法月日
		{"sched_decision_abcdefgh", false}, // 非数字
		{"20260712", false},                // 无下划线
	}
	for _, c := range cases {
		if got := hasDailySuffix(c.table); got != c.want {
			t.Errorf("hasDailySuffix(%q) = %v, 期望 %v", c.table, got, c.want)
		}
	}
}
