package service

import (
	"testing"

	"beacon/internal/sse"
)

// TestDiffEventsAllMatch 三通道 md5 全一致 → 无补发事件（无变更）。
func TestDiffEventsAllMatch(t *testing.T) {
	same := ChannelMD5{Config: "c", File: "f", Override: "o"}
	if got := DiffEvents(same, same); len(got) != 0 {
		t.Fatalf("全一致应无补发事件，实际 %v", got)
	}
}

// TestDiffEventsConfigBehind 仅配置落后 → 只补发 config-changed，携带当前 md5。
func TestDiffEventsConfigBehind(t *testing.T) {
	reported := ChannelMD5{Config: "old", File: "f", Override: "o"}
	current := ChannelMD5{Config: "new", File: "f", Override: "o"}
	got := DiffEvents(reported, current)
	if len(got) != 1 || got[0].Type != sse.EventConfigChanged || got[0].MD5 != "new" {
		t.Fatalf("仅配置落后应只补发 config-changed(new)，实际 %v", got)
	}
}

// TestDiffEventsAllBehind 三通道都落后（断线期间全变）→ 三条 *-changed 全补发，不丢更新。
func TestDiffEventsAllBehind(t *testing.T) {
	reported := ChannelMD5{Config: "c0", File: "f0", Override: "o0"}
	current := ChannelMD5{Config: "c1", File: "f1", Override: "o1"}
	got := DiffEvents(reported, current)
	if len(got) != 3 {
		t.Fatalf("三通道落后应补发 3 条事件，实际 %d 条：%v", len(got), got)
	}
	byType := map[string]string{}
	for _, e := range got {
		byType[e.Type] = e.MD5
	}
	if byType[sse.EventConfigChanged] != "c1" || byType[sse.EventFileChanged] != "f1" || byType[sse.EventOverrideChanged] != "o1" {
		t.Fatalf("各通道应补发到当前 md5，实际 %v", byType)
	}
}

// TestDiffEventsEmptyReportedFirstConnect 首连无任何 md5（全空）但服务端有内容 → 三条全补发（首次接入即对齐）。
func TestDiffEventsEmptyReportedFirstConnect(t *testing.T) {
	reported := ChannelMD5{}
	current := ChannelMD5{Config: "c", File: "f", Override: "o"}
	if got := DiffEvents(reported, current); len(got) != 3 {
		t.Fatalf("首连空 md5 且服务端有内容应补发 3 条，实际 %v", got)
	}
}
