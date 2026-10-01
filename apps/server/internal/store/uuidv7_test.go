package store

import (
	"testing"
	"time"
)

// TestTimeMsFromUUIDv7 校验从 UUIDv7 高 48 位还原毫秒时间戳（大端），并覆盖非法输入。
func TestTimeMsFromUUIDv7(t *testing.T) {
	// 构造一个已知毫秒时间戳对应的 UUIDv7 前缀：2026-07-06T08:00:00Z = 1783324800000ms。
	ms := time.Date(2026, 7, 6, 8, 0, 0, 0, time.UTC).UnixMilli()
	// 前 6 字节（48 位）大端 = ms，写成 12 位十六进制置于 UUID 前两段。
	prefix := []byte{
		byte(ms >> 40), byte(ms >> 32), byte(ms >> 24),
		byte(ms >> 16), byte(ms >> 8), byte(ms),
	}
	// 组装成标准 8-4-4-4-12 文本，后半段随意填充。
	id := hex2(prefix[0]) + hex2(prefix[1]) + hex2(prefix[2]) + hex2(prefix[3]) +
		"-" + hex2(prefix[4]) + hex2(prefix[5]) + "-7abc-8def-0123456789ab"

	got, ok := TimeMsFromUUIDv7(id)
	if !ok {
		t.Fatalf("合法 UUIDv7 解析应成功")
	}
	if got != ms {
		t.Fatalf("解析毫秒不符：期望 %d 实际 %d", ms, got)
	}
}

// TestTimeMsFromUUIDv7Invalid 校验非法输入返回 false，不 panic。
func TestTimeMsFromUUIDv7Invalid(t *testing.T) {
	cases := []string{
		"",                  // 空
		"0190a1b2",          // 位数不足 12 位十六进制
		"----",              // 全连字符
		"zzzzzzzz-zzzz-...", // 含非十六进制字符
	}
	for _, c := range cases {
		if _, ok := TimeMsFromUUIDv7(c); ok {
			t.Fatalf("非法输入 %q 应返回 false", c)
		}
	}
}

// hex2 把一个字节格式化为两位小写十六进制（测试构造用）。
func hex2(b byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[b>>4], digits[b&0x0f]})
}

// TestNewUUIDv7RoundTrip 校验生成 → 解析的往返一致：内嵌毫秒可还原、形态合规、版本与变体位正确。
func TestNewUUIDv7RoundTrip(t *testing.T) {
	ms := time.Date(2026, 7, 11, 4, 5, 6, 789, time.UTC).UnixMilli()
	id := NewUUIDv7(ms)
	if len(id) != 36 {
		t.Fatalf("UUIDv7 文本长度=%d，期望 36: %q", len(id), id)
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				t.Fatalf("第 %d 位应为连字符，实际 %q: %q", i, c, id)
			}
			continue
		}
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("第 %d 位不是小写十六进制字符 %q: %q", i, c, id)
		}
	}
	got, ok := TimeMsFromUUIDv7(id)
	if !ok {
		t.Fatalf("自生成 ID 应可解析: %q", id)
	}
	if got != ms {
		t.Fatalf("内嵌毫秒往返不一致：期望 %d 实际 %d", ms, got)
	}
	// 版本号 7（第 7 字节高 4 位）与变体 10xx（第 9 字节高 2 位）。
	if id[14] != '7' {
		t.Fatalf("版本位应为 7，实际 %q: %q", id[14], id)
	}
	switch id[19] {
	case '8', '9', 'a', 'b':
	default:
		t.Fatalf("变体位（第 9 字节高 2 位）应为 8/9/a/b，实际 %q: %q", id[19], id)
	}
}

// TestNewUUIDv7Unique 校验同一毫秒内多次生成不重复（随机段保证唯一）。
func TestNewUUIDv7Unique(t *testing.T) {
	ms := time.Date(2026, 7, 11, 4, 5, 6, 0, time.UTC).UnixMilli()
	seen := make(map[string]struct{}, 256)
	for i := 0; i < 256; i++ {
		id := NewUUIDv7(ms)
		if _, dup := seen[id]; dup {
			t.Fatalf("同一毫秒内生成了重复 ID: %q", id)
		}
		seen[id] = struct{}{}
	}
}
