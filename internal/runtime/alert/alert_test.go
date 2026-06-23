package alert

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func sampleAlert() Alert {
	return Alert{
		Namespace: "prod", ServerID: "lobby-1", Address: "10.0.0.1:25565",
		PrevStatus: "online", Status: "degraded",
		At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// recordingAlerter 记录收到的告警，可选地按需返回错误，用于断言扇出与兜错。
type recordingAlerter struct {
	name string
	mu   sync.Mutex
	got  []Alert
	err  error
}

func (a *recordingAlerter) Name() string { return a.name }

func (a *recordingAlerter) Notify(_ context.Context, al Alert) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, al)
	return a.err
}

func (a *recordingAlerter) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.got)
}

// TestDispatcherFanOut 分发器扇出到所有通道。
func TestDispatcherFanOut(t *testing.T) {
	c1 := &recordingAlerter{name: "c1"}
	c2 := &recordingAlerter{name: "c2"}
	d := NewDispatcher(c1, c2)
	d.Dispatch(context.Background(), sampleAlert())
	if c1.count() != 1 || c2.count() != 1 {
		t.Fatalf("两个通道都应各收到 1 条，实际 c1=%d c2=%d", c1.count(), c2.count())
	}
}

// TestDispatcherTolerateChannelError 一个通道失败不影响其他通道、不 panic。
func TestDispatcherTolerateChannelError(t *testing.T) {
	bad := &recordingAlerter{name: "bad", err: errors.New("通道故障")}
	good := &recordingAlerter{name: "good"}
	d := NewDispatcher(bad, good)
	d.Dispatch(context.Background(), sampleAlert()) // 不应 panic
	if good.count() != 1 {
		t.Fatalf("故障通道不应阻断后续通道，good 应收到 1 条，实际 %d", good.count())
	}
}

// TestDispatcherEmpty 无通道时分发不报错。
func TestDispatcherEmpty(_ *testing.T) {
	d := NewDispatcher()
	d.Dispatch(context.Background(), sampleAlert()) // 不应 panic
}

// TestInboxAppendAndList 站内信追加后可读回。
func TestInboxAppendAndList(t *testing.T) {
	inbox := NewInboxAlerter(10)
	if err := inbox.Notify(context.Background(), sampleAlert()); err != nil {
		t.Fatalf("站内信投递不应报错: %v", err)
	}
	list := inbox.List()
	if len(list) != 1 || list[0].ServerID != "lobby-1" || list[0].Status != "degraded" {
		t.Fatalf("站内信应有 1 条 lobby-1 degraded，实际 %+v", list)
	}
}

// TestInboxRingCapacity 站内信环形容量上限：超出丢最旧、保留最新 N 条。
func TestInboxRingCapacity(t *testing.T) {
	inbox := NewInboxAlerter(3)
	for i := 0; i < 5; i++ {
		a := sampleAlert()
		a.ServerID = string(rune('A' + i))
		_ = inbox.Notify(context.Background(), a)
	}
	list := inbox.List()
	if len(list) != 3 {
		t.Fatalf("容量 3 应只保留 3 条，实际 %d", len(list))
	}
	// 最新在前：保留 E、D、C
	if list[0].ServerID != "E" || list[2].ServerID != "C" {
		t.Fatalf("应保留最新 3 条（E,D,C），实际 %s..%s", list[0].ServerID, list[2].ServerID)
	}
}

// TestInboxListSnapshotIsolation List 返回快照，外部改动不影响内部。
func TestInboxListSnapshotIsolation(t *testing.T) {
	inbox := NewInboxAlerter(5)
	_ = inbox.Notify(context.Background(), sampleAlert())
	snap := inbox.List()
	snap[0].Status = "TAMPERED"
	if inbox.List()[0].Status != "degraded" {
		t.Fatal("外部改动快照不应影响站内信内部状态")
	}
}

// TestWebhookPostsJSON webhook 通道向目标 URL POST 告警 JSON。
func TestWebhookPostsJSON(t *testing.T) {
	var gotBody []byte
	var gotCT string
	done := make(chan struct{})
	srv := newTestServer(func(ct string, body []byte) {
		gotCT = ct
		gotBody = body
		close(done)
	})
	defer srv.Close()

	wh := NewWebhookAlerter(&fakeWebhookSettings{url: srv.URL, timeoutMs: 2000})
	if err := wh.Notify(context.Background(), sampleAlert()); err != nil {
		t.Fatalf("webhook 投递失败: %v", err)
	}
	<-done
	if gotCT != "application/json" {
		t.Fatalf("Content-Type 应为 application/json，实际 %q", gotCT)
	}
	if len(gotBody) == 0 || !containsAll(string(gotBody), "lobby-1", "degraded", "online") {
		t.Fatalf("告警 JSON 内容不完整: %s", string(gotBody))
	}
}

// TestWebhookSkipsWhenURLEmpty 设置 store 的 webhook url 为空时跳过投递（动态停用，FR-61）：不报错、不发请求。
func TestWebhookSkipsWhenURLEmpty(t *testing.T) {
	hit := false
	srv := newTestServer(func(string, []byte) { hit = true })
	defer srv.Close()

	wh := NewWebhookAlerter(&fakeWebhookSettings{url: "", timeoutMs: 2000})
	if err := wh.Notify(context.Background(), sampleAlert()); err != nil {
		t.Fatalf("url 空应跳过且不报错，实际 %v", err)
	}
	if hit {
		t.Fatal("url 空不应发出任何 HTTP 请求")
	}
}

// fakeWebhookSettings 是 WebhookSettings 的测试替身：以固定 url / timeout 驱动 webhook 通道（FR-61）。
type fakeWebhookSettings struct {
	url       string
	timeoutMs int
}

func (f *fakeWebhookSettings) GetString(key string) string {
	if key == keyAlertWebhookURL {
		return f.url
	}
	return ""
}

func (f *fakeWebhookSettings) GetInt(key string) int {
	if key == keyAlertWebhookTimeoutMs {
		return f.timeoutMs
	}
	return 0
}
