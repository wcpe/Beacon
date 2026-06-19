package handler

import (
	"net/url"
	"testing"
)

// TestParseTagParams 验证发现端点 tag.<key>=<value> 解析：命中、空前缀忽略、空键忽略、同名取最后值、无 tag 返回 nil。
func TestParseTagParams(t *testing.T) {
	q := url.Values{}
	q.Set("namespace", "prod")
	q.Set("role", "bukkit")
	q.Add("tag.region", "cn-east")
	q.Add("tag.tier", "premium")
	q.Set("tag.", "ignored") // 空键应忽略

	tags := parseTagParams(q)
	if len(tags) != 2 {
		t.Fatalf("应解析出 2 个 tag（忽略空键与非 tag 参数），实际 %d：%v", len(tags), tags)
	}
	if tags["region"] != "cn-east" || tags["tier"] != "premium" {
		t.Fatalf("tag 解析值错误：%v", tags)
	}
	if _, ok := tags[""]; ok {
		t.Fatal("空键 tag. 应被忽略")
	}
}

// TestParseTagParamsNoTags 无 tag 参数时返回 nil（不过滤）。
func TestParseTagParamsNoTags(t *testing.T) {
	q := url.Values{}
	q.Set("namespace", "prod")
	q.Set("zone", "zoneA")
	if tags := parseTagParams(q); tags != nil {
		t.Fatalf("无 tag 参数应返回 nil，实际 %v", tags)
	}
}

// TestParseTagParamsLastWins 同名 tag 多值取最后一个（与标准查询参数取值一致）。
func TestParseTagParamsLastWins(t *testing.T) {
	q := url.Values{}
	q.Add("tag.region", "cn-east")
	q.Add("tag.region", "cn-west")
	if tags := parseTagParams(q); tags["region"] != "cn-west" {
		t.Fatalf("同名 tag 应取最后值 cn-west，实际 %q", tags["region"])
	}
}
