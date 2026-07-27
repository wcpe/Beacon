package store

import "testing"

// TestApplySQLitePragmas 守护 sqlite DSN 的 WAL 崩溃韧性注入：
// 默认 DSN 追加 journal_mode(WAL)；已显式指定 journal_mode 的 DSN 不覆盖；
// 带 ? 的 DSN 用 & 拼接、不带 ? 的用 ? 拼接。
func TestApplySQLitePragmas(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{"裸文件名", "beacon.db", "beacon.db?_pragma=journal_mode(WAL)"},
		{"已有查询参数", "beacon.db?cache=shared", "beacon.db?cache=shared&_pragma=journal_mode(WAL)"},
		{"file URI 无参数", "file:beacon.db", "file:beacon.db?_pragma=journal_mode(WAL)"},
		{"file URI 有参数", "file:beacon.db?cache=shared", "file:beacon.db?cache=shared&_pragma=journal_mode(WAL)"},
		{"已显式 WAL 不覆盖", "beacon.db?_pragma=journal_mode(WAL)", "beacon.db?_pragma=journal_mode(WAL)"},
		{"已显式 DELETE 不覆盖", "beacon.db?_pragma=journal_mode(DELETE)", "beacon.db?_pragma=journal_mode(DELETE)"},
		{"journal_mode 大小写不敏感不覆盖", "beacon.db?_pragma=JOURNAL_MODE(WAL)", "beacon.db?_pragma=JOURNAL_MODE(WAL)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := applySQLitePragmas(c.dsn); got != c.want {
				t.Fatalf("applySQLitePragmas(%q) = %q，期望 %q", c.dsn, got, c.want)
			}
		})
	}
}
