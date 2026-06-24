package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/config"
	"github.com/wcpe/Beacon/internal/merge"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime"
	"github.com/wcpe/Beacon/internal/runtime/longpoll"
	"github.com/wcpe/Beacon/internal/service"
	"github.com/wcpe/Beacon/internal/sse"
	"github.com/wcpe/Beacon/internal/store"
)

// streamSqliteStack 用内存 sqlite 装配 SSE 推送编排所需的全套依赖（无需 MySQL，CGO sqlite 即可）。
// 返回配置服务、推送编排器、注册表与拓扑唤醒器（拓扑 watch 测试用 notifier.NotifyTopologyChange 触发）。
func streamSqliteStack(t *testing.T) (*service.ConfigService, *service.StreamService, *runtime.Registry, *service.ChangeNotifier) {
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
	topologyHub := longpoll.NewHub()
	// 命令待办唤醒（FR-39）：流与唤醒器须共用同一 commandHub，notifier.NotifyCommand 才能驱动流发 command-pending。
	commandHub := longpoll.NewHub()

	effSvc := service.NewEffectiveService(configRepo, assignRepo, nil, nil, hub)
	fileEffSvc := service.NewFileEffectiveService(fileRepo, assignRepo, fileHub)
	ovrEffSvc := service.NewOverrideEffectiveService(overrideSetRepo, fileRepo, assignRepo, fileHub)
	// 设置服务（FR-61）：保活间隔取 longpoll.max-hold-ms（默认 30s），测试短时完成不触发心跳、不依赖保活。
	settingsSvc, err := service.NewSettingsService(db, repository.NewSettingRepository(db), auditRepo)
	if err != nil {
		t.Fatalf("装配设置服务失败: %v", err)
	}
	streamSvc := service.NewStreamService(effSvc, fileEffSvc, ovrEffSvc, reg, hub, fileHub, topologyHub, commandHub, settingsSvc)

	notifier := service.NewChangeNotifier(hub, fileHub, topologyHub, commandHub, reg, assignRepo)
	cfgSvc := service.NewConfigService(db, configRepo, repository.NewConfigRevisionRepository(db, noEncryptCipher()), auditRepo)
	cfgSvc.SetNotifier(notifier)
	return cfgSvc, streamSvc, reg, notifier
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
	cfg, stream, _, _ := streamSqliteStack(t)
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
	cfg, stream, _, _ := streamSqliteStack(t)
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

// TestStreamRunCommandPendingEmit 命令待办唤醒（FR-39）：转直播后 NotifyCommand → 流近实时发 command-pending（无 data）。
func TestStreamRunCommandPendingEmit(t *testing.T) {
	_, stream, _, notifier := streamSqliteStack(t)

	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = stream.Run(ctx, "prod", "s1", "", service.ChannelMD5{}, sink) }()
	// 先 ready 进入直播，再触发命令待办唤醒。
	sink.waitFor(t, 2*time.Second, func(e sse.Event) bool { return e.Type == sse.EventReady })

	notifier.NotifyCommand("prod", "s1")
	evt := sink.waitFor(t, 2*time.Second, func(e sse.Event) bool { return e.Type == sse.EventCommandPending })
	if evt.Type != sse.EventCommandPending {
		t.Fatalf("应收到 command-pending 事件，实际 %q", evt.Type)
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
