package sse

import (
	"strings"
	"testing"
)

// TestEncodeChangedEvent *-changed 事件应含 event 行与携带新 md5 的 data 行，并以空行结尾。
func TestEncodeChangedEvent(t *testing.T) {
	out := Encode(Event{Type: EventConfigChanged, MD5: "abc123"})
	if !strings.HasPrefix(out, "event: config-changed\n") {
		t.Fatalf("应以 event 行起头，实际 %q", out)
	}
	if !strings.Contains(out, `data: {"md5":"abc123"}`) {
		t.Fatalf("data 行应携带新 md5，实际 %q", out)
	}
	if !strings.HasSuffix(out, "\n\n") {
		t.Fatalf("单条事件应以空行结尾，实际 %q", out)
	}
}

// TestEncodeReadyEvent ready 事件 data 为最小占位对象，不携带 md5。
func TestEncodeReadyEvent(t *testing.T) {
	out := Encode(Event{Type: EventReady})
	if !strings.Contains(out, "event: ready\n") {
		t.Fatalf("应为 ready 事件，实际 %q", out)
	}
	if !strings.Contains(out, "data: {}\n") {
		t.Fatalf("ready 事件 data 应为空对象，实际 %q", out)
	}
	if strings.Contains(out, "md5") {
		t.Fatalf("ready 事件不应携带 md5，实际 %q", out)
	}
}

// TestEncodeAllChannels 三条通道事件类型均按约定编码（防类型常量笔误）。
func TestEncodeAllChannels(t *testing.T) {
	cases := map[string]string{
		EventConfigChanged:   "config-changed",
		EventFileChanged:     "file-changed",
		EventOverrideChanged: "override-changed",
	}
	for typ, want := range cases {
		out := Encode(Event{Type: typ, MD5: "m"})
		if !strings.Contains(out, "event: "+want+"\n") {
			t.Fatalf("通道 %s 应编码为 event: %s，实际 %q", typ, want, out)
		}
	}
}
