package handler

import (
	"testing"
	"time"
)

// TestParseRFC3339 验证审计时间范围解析：空与非法返回零值（不设界），合法按 RFC3339 解析（含时区偏移归一）。
func TestParseRFC3339(t *testing.T) {
	if got := parseRFC3339(""); !got.IsZero() {
		t.Fatalf("空字符串应返回零值，实际 %v", got)
	}
	if got := parseRFC3339("not-a-time"); !got.IsZero() {
		t.Fatalf("非法格式应返回零值，实际 %v", got)
	}
	if got := parseRFC3339("2026-01-02"); !got.IsZero() {
		t.Fatalf("非 RFC3339（缺时间与时区）应返回零值，实际 %v", got)
	}

	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := parseRFC3339("2026-01-02T03:04:05Z"); !got.Equal(want) {
		t.Fatalf("Z 后缀解析错误：got %v want %v", got, want)
	}
	// 含偏移 +08:00 等价于 UTC 的 2026-01-01T19:04:05Z
	if got := parseRFC3339("2026-01-02T03:04:05+08:00"); !got.Equal(want.Add(-8 * time.Hour)) {
		t.Fatalf("含时区偏移解析错误：got %v", got.UTC())
	}
}
