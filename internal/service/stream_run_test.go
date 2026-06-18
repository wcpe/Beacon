package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"beacon/internal/config"
	"beacon/internal/merge"
	"beacon/internal/model"
	"beacon/internal/repository"
	"beacon/internal/runtime"
	"beacon/internal/runtime/longpoll"
	"beacon/internal/service"
	"beacon/internal/sse"
	"beacon/internal/store"
)

// streamSqliteStack 用内存 sqlite 装配 SSE 推送编排所需的全套依赖（无需 MySQL，CGO sqlite 即可）。
func streamSqliteStack(t *testing.T) (*service.ConfigService, *service.StreamService, *runtime.Registry) {
	t.Helper()
	// 每个测试一个独立内存库（cache=shared 让多连接共享同一内存库）。
	db, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: "file:" + t.Name() + "?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 300,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	t.Cleanup(func() { store.Close(db) })

	configRepo := repository.NewConfigItemRepository(db, noEncryptCipher())
	fileRepo := repository.NewFileObjectRepository(db)
	overrideSetRepo := repository.NewFileOverrideSetRepository(db)
	assignRepo := repository.NewZoneAssignmentRepository(db)
	auditRepo := repository.NewAuditLogRepository(db)
	reg := runtime.NewRegistry()
	hub := longpoll.NewHub()
	fileHub := longpoll.NewHub()

	effSvc := service.NewEffectiveService(configRepo, assignRepo, hub)
	fileEffSvc := service.NewFileEffectiveService(fileRepo, assignRepo, fileHub)
	ovrEffSvc := service.NewOverrideEffectiveService(overrideSetRepo, fileRepo, assignRepo, fileHub)
	streamSvc := service.NewStreamService(effSvc, fileEffSvc, ovrEffSvc, hub, fileHub, 0) // 关保活，测试不依赖心跳

	cfgSvc := service.NewConfigService(db, configRepo, repository.NewConfigRevisionRepository(db, noEncryptCipher()), auditRepo)
	cfgSvc.SetNotifier(service.NewChangeNotifier(hub, fileHub, reg, assignRepo))
	return cfgSvc, streamSvc, reg
}

// recordingSink 记录收到的事件，供测试断言（线程安全）。
type recordingSink struct {
	mu     sync.Mutex
	events []sse.Event
	ch     chan sse.Event
}

func newRecordingSink() *recordingSink {
	return &recordingSink{ch: make(chan sse.Event, 32)}
}

func (s *recordingSink) Send(e sse.Event) error {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
	s.ch <- e
	return nil
}

// waitFor 在期限内等待一条满足条件的事件。
func (s *recordingSink) waitFor(t *testing.T, timeout time.Duration, pred func(sse.Event) bool) sse.Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case e := <-s.ch:
			if pred(e) {
				return e
			}
		case <-deadline:
			t.Fatal("等待目标事件超时")
			return sse.Event{}
		}
	}
}

func createGlobalConfig(t *testing.T, cfg *service.ConfigService, content string) uint {
	t.Helper()
	it, err := cfg.Create(service.CreateConfigParams{
		Namespace: "prod", Group: model.GlobalGroupCode, DataID: "app.yml",
		ScopeLevel: model.ScopeGlobal, Format: merge.FormatYAML, Content: content, Operator: "admin",
	})
	if err != nil {
		t.Fatalf("建全局配置失败: %v", err)
	}
	return it.ID
}

// TestStreamRunReconcileThenReady 连接即对账：上报空 md5、库里有配置 → 先补发 config-changed，再发 ready。
func TestStreamRunReconcileThenReady(t *testing.T) {
	cfg, stream, _ := streamSqliteStack(t)
	createGlobalConfig(t, cfg, "k: 1\n")

	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = stream.Run(ctx, "prod", "s1", "", service.ChannelMD5{}, sink) }()

	// 应先补发 config-changed（携带新 md5），随后 ready。
	cfgEvt := sink.waitFor(t, 2*time.Second, func(e sse.Event) bool { return e.Type == sse.EventConfigChanged })
	if cfgEvt.MD5 == "" {
		t.Fatal("config-changed 应携带新 md5")
	}
	sink.waitFor(t, 2*time.Second, func(e sse.Event) bool { return e.Type == sse.EventReady })
}

// TestStreamRunLivePushOnPublish 转直播后发布 → 经唤醒重算推 config-changed（不丢更新）。
func TestStreamRunLivePushOnPublish(t *testing.T) {
	cfg, stream, _ := streamSqliteStack(t)
	id := createGlobalConfig(t, cfg, "k: 1\n")

	// 先用空 md5 的探针流取当前配置 md5（首个 config-changed 即携带它），再据此模拟"agent 已对齐"。
	cur := probeCurrentConfigMD5(t, stream)

	// agent 已对齐：开流上报当前 md5 → 对账无补发，直接 ready，随后转直播。
	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = stream.Run(ctx, "prod", "s1", "", service.ChannelMD5{Config: cur}, sink) }()
	sink.waitFor(t, 2*time.Second, func(e sse.Event) bool { return e.Type == sse.EventReady })

	// 直播阶段发布 → 应近实时收到 config-changed，且携带新 md5（≠ 旧）。
	if _, err := cfg.Publish(id, "k: 2\n", "admin", "改", ""); err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	evt := sink.waitFor(t, 2*time.Second, func(e sse.Event) bool { return e.Type == sse.EventConfigChanged })
	if evt.MD5 == cur {
		t.Fatalf("直播事件应携带变更后的新 md5，实际仍为旧值 %q", cur)
	}
}

// probeCurrentConfigMD5 用空 md5 开一条临时流取当前配置 md5（首个 config-changed 携带），取到即取消。
func probeCurrentConfigMD5(t *testing.T, stream *service.StreamService) string {
	t.Helper()
	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = stream.Run(ctx, "prod", "s1", "", service.ChannelMD5{}, sink) }()
	evt := sink.waitFor(t, 2*time.Second, func(e sse.Event) bool { return e.Type == sse.EventConfigChanged })
	if evt.MD5 == "" {
		t.Fatal("探针流应取到当前配置 md5")
	}
	return evt.MD5
}
