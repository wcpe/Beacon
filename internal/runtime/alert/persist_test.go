package alert

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/internal/model"
)

// fakeEventSink 是 EventSink 测试替身：记录落库的事件，可选注入错误（验兜错）。
type fakeEventSink struct {
	got []*model.AlertEvent
	err error
}

func (f *fakeEventSink) Record(e *model.AlertEvent) error {
	f.got = append(f.got, e)
	return f.err
}

// TestPersistAlerterMapsHealthAlert persist 通道把健康告警映射为 alert_event：类型/级别/字段/detail 正确。
func TestPersistAlerterMapsHealthAlert(t *testing.T) {
	sink := &fakeEventSink{}
	p := NewPersistAlerter(sink)
	if err := p.Notify(context.Background(), sampleAlert()); err != nil {
		t.Fatalf("留痕不应报错: %v", err)
	}
	if len(sink.got) != 1 {
		t.Fatalf("应落库 1 条，实际 %d", len(sink.got))
	}
	e := sink.got[0]
	if e.Type != model.AlertEventTypeHealthTransition {
		t.Fatalf("类型应为 health-transition，实际 %q", e.Type)
	}
	// sampleAlert 状态为 degraded → warning
	if e.Level != model.AlertLevelWarning {
		t.Fatalf("degraded 应映射 warning，实际 %q", e.Level)
	}
	if e.ServerID != "lobby-1" || e.Namespace != "prod" {
		t.Fatalf("serverId/namespace 错误：%+v", e)
	}
	if !strings.Contains(e.Message, "lobby-1") || !strings.Contains(e.Message, "degraded") {
		t.Fatalf("message 应含 serverId 与状态，实际 %q", e.Message)
	}
	// detail 为 json 文本，含前后状态与地址
	if !strings.Contains(e.Detail, "10.0.0.1:25565") || !strings.Contains(e.Detail, "online") {
		t.Fatalf("detail 应含地址与前状态，实际 %q", e.Detail)
	}
	// created_at 不在通道设，交由 GORM 全局 NowFunc 填（保与全表一致）
	if !e.CreatedAt.IsZero() {
		t.Fatalf("created_at 应留零值交 GORM 填，实际 %v", e.CreatedAt)
	}
}

// TestPersistAlerterLevelByStatus 不同状态映射不同级别：lost/offline=critical。
func TestPersistAlerterLevelByStatus(t *testing.T) {
	cases := map[string]string{
		"degraded": model.AlertLevelWarning,
		"lost":     model.AlertLevelCritical,
		"offline":  model.AlertLevelCritical,
		"online":   model.AlertLevelInfo,
	}
	for status, wantLevel := range cases {
		if got := levelForStatus(status); got != wantLevel {
			t.Fatalf("状态 %q 应映射 %q，实际 %q", status, wantLevel, got)
		}
	}
}

// TestPersistAlerterPropagatesError sink 落库失败时 Notify 返回错误，交 Dispatcher 兜错（仅 WARN、不阻断）。
func TestPersistAlerterPropagatesError(t *testing.T) {
	sink := &fakeEventSink{err: errors.New("落库失败")}
	p := NewPersistAlerter(sink)
	if err := p.Notify(context.Background(), sampleAlert()); err == nil {
		t.Fatal("sink 失败时 Notify 应返回错误供 Dispatcher 兜错")
	}
}

// TestDispatcherToleratePersistFailure persist 通道落库失败不阻断其它通道（端到端兜错，FR-89 验收）。
func TestDispatcherToleratePersistFailure(t *testing.T) {
	failing := NewPersistAlerter(&fakeEventSink{err: errors.New("落库失败")})
	good := &recordingAlerter{name: "good"}
	d := NewDispatcher(failing, good)
	d.Dispatch(context.Background(), sampleAlert()) // 不应 panic
	if good.count() != 1 {
		t.Fatalf("留痕失败不应阻断后续通道，good 应收到 1 条，实际 %d", good.count())
	}
}
