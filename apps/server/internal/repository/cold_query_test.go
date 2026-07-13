package repository

import (
	"testing"
)

// coldRow 是归并测试的最小行（时间 + 字符串主键）。
type coldRow struct {
	ts int64
	id string
}

func coldRowKey(r coldRow) coldCursor { return coldCursor{TimeMs: r.ts, ID: r.id} }

// TestColdCursorRoundTrip 游标编码解码往返；空 / 非法令牌回零值（首页）。
func TestColdCursorRoundTrip(t *testing.T) {
	c := coldCursor{TimeMs: 1_700_000_000_123, ID: "abc-123"}
	got := decodeColdCursor(c.encode())
	if got != c {
		t.Fatalf("往返不一致: got=%v want=%v", got, c)
	}
	if !decodeColdCursor("").isZero() {
		t.Fatalf("空令牌应回零值")
	}
	if !decodeColdCursor("!!!not-base64").isZero() {
		t.Fatalf("非法 base64 应回零值")
	}
	if !decodeColdCursor("bm9waXBl").isZero() { // "nopipe" base64，无分隔符
		t.Fatalf("无分隔符令牌应回零值")
	}
	if (coldCursor{}).encode() != "" {
		t.Fatalf("零游标应编码为空串")
	}
}

// TestMergeColdPageOrderAndDedup 有序归并 + 主键去重保热侧 + 取前 limit + 下一游标。
func TestMergeColdPageOrderAndDedup(t *testing.T) {
	// 热侧（各已按时间降序）：id=h3(300) h1(100)；冷侧：a2(200) a1(100dup-of-h1? 不同 id) shared(150)
	hot := []coldRow{{300, "h3"}, {150, "shared"}, {100, "h1"}}
	// 冷侧含一条与热侧同主键 "shared"（归档进行中两侧同存），应去重保热侧。
	archive := []coldRow{{250, "a25"}, {150, "shared"}, {120, "a12"}}
	page, next, hasMore := mergeColdPage(hot, archive, 3, coldRowKey, coldLessStringDesc)
	if len(page) != 3 || !hasMore {
		t.Fatalf("应取前 3 且有下一页，实际 len=%d hasMore=%v", len(page), hasMore)
	}
	// 归并去重后按时间降序：h3(300) a25(250) shared(150,热) a12(120) h1(100)；前 3 = h3,a25,shared
	wantIDs := []string{"h3", "a25", "shared"}
	for i, id := range wantIDs {
		if page[i].id != id {
			t.Fatalf("第 %d 位应为 %s，实际 %s", i, id, page[i].id)
		}
	}
	// 下一游标 = 本页末行 shared(150)
	if next.TimeMs != 150 || next.ID != "shared" {
		t.Fatalf("下一游标应为 (150,shared)，实际 %v", next)
	}
	// 校验去重确实保热侧：shared 行应来自热侧（本例值相同无法从字段区分，改验总去重数）。
	// 合并全集去重后应为 5 条唯一 id（h3,a25,shared,a12,h1）。
	full, _, more := mergeColdPage(hot, archive, 10, coldRowKey, coldLessStringDesc)
	if len(full) != 5 || more {
		t.Fatalf("去重后应 5 条且无下一页，实际 len=%d more=%v", len(full), more)
	}
}

// TestMergeColdPageDedupKeepsHot 造两侧同主键、字段可区分的行，验证保留热侧那份。
func TestMergeColdPageDedupKeepsHot(t *testing.T) {
	type tagged struct {
		ts   int64
		id   string
		side string
	}
	keyOf := func(r tagged) coldCursor { return coldCursor{TimeMs: r.ts, ID: r.id} }
	hot := []tagged{{100, "dup", "hot"}}
	archive := []tagged{{100, "dup", "archive"}}
	page, _, _ := mergeColdPage(hot, archive, 5, keyOf, coldLessStringDesc)
	if len(page) != 1 || page[0].side != "hot" {
		t.Fatalf("同主键应保留热侧，实际 %+v", page)
	}
}

// TestMergeColdPageNumericTiebreak audit 数值主键同刻并列时按数值（非字典序）降序。
func TestMergeColdPageNumericTiebreak(t *testing.T) {
	// 同一时刻 t=100，id 9 与 10：数值降序应 10 在前（字典序会误判 "9">"10"）。
	hot := []coldRow{{100, "10"}}
	archive := []coldRow{{100, "9"}}
	page, _, _ := mergeColdPage(hot, archive, 5, coldRowKey, coldLessNumericDesc)
	if len(page) != 2 || page[0].id != "10" || page[1].id != "9" {
		t.Fatalf("数值主键同刻应 10,9 序，实际 %+v", page)
	}
}

// TestMergeColdPageOneSideEmpty 一侧为空时退化为另一侧有序取前 limit。
func TestMergeColdPageOneSideEmpty(t *testing.T) {
	hot := []coldRow{{300, "a"}, {200, "b"}, {100, "c"}}
	page, next, hasMore := mergeColdPage(hot, nil, 2, coldRowKey, coldLessStringDesc)
	if len(page) != 2 || !hasMore || page[0].id != "a" || page[1].id != "b" {
		t.Fatalf("热侧独有取前 2，实际 len=%d hasMore=%v page=%+v", len(page), hasMore, page)
	}
	if next.TimeMs != 200 {
		t.Fatalf("下一游标应为 b(200)，实际 %v", next)
	}
	page2, _, hasMore2 := mergeColdPage(nil, hot, 5, coldRowKey, coldLessStringDesc)
	if len(page2) != 3 || hasMore2 {
		t.Fatalf("冷侧独有全取，实际 len=%d hasMore=%v", len(page2), hasMore2)
	}
}

// TestUnionColdRowsDedup 区间聚合并表去重保热侧。
func TestUnionColdRowsDedup(t *testing.T) {
	type row struct {
		key  string
		side string
	}
	dedup := func(r row) string { return r.key }
	hot := []row{{"k1", "hot"}, {"k2", "hot"}}
	archive := []row{{"k2", "archive"}, {"k3", "archive"}}
	out := unionColdRows(hot, archive, dedup)
	if len(out) != 3 {
		t.Fatalf("并表去重后应 3 条，实际 %d", len(out))
	}
	// k2 应保热侧
	for _, r := range out {
		if r.key == "k2" && r.side != "hot" {
			t.Fatalf("k2 应保热侧，实际 %s", r.side)
		}
	}
}
