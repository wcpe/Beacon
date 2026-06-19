package service_test

import (
	"context"
	"testing"
	"time"

	"beacon/internal/runtime"
	"beacon/internal/service"
	"beacon/internal/sse"
)

// TestStreamRunReconcileTopologyOnConnect 连接即对账：上报空拓扑摘要、注册表已有可用实例 → 先补发 topology-changed。
func TestStreamRunReconcileTopologyOnConnect(t *testing.T) {
	_, stream, reg, _ := streamSqliteStack(t)
	// 注册表预置一个可用实例（在线）。
	mustRegister(t, reg, "prod", "lobby-1", "10.0.0.1:25565")

	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 上报空拓扑摘要 → 当前非空 → 应补发 topology-changed，再 ready。
	go func() { _ = stream.Run(ctx, "prod", "watcher", "", service.ChannelMD5{}, sink) }()

	evt := sink.waitFor(t, 2*time.Second, func(e sse.Event) bool { return e.Type == sse.EventTopologyChanged })
	if evt.MD5 == "" {
		t.Fatal("topology-changed 应携带新拓扑摘要")
	}
	sink.waitFor(t, 2*time.Second, func(e sse.Event) bool { return e.Type == sse.EventReady })
}

// TestStreamRunLivePushOnInstanceUp 转直播后新实例上线 → 经拓扑唤醒推 topology-changed。
func TestStreamRunLivePushOnInstanceUp(t *testing.T) {
	_, stream, reg, notifier := streamSqliteStack(t)
	mustRegister(t, reg, "prod", "lobby-1", "10.0.0.1:25565")

	// 探针流取当前拓扑摘要，模拟 watcher 已对齐。
	cur := probeCurrentTopologyDigest(t, stream)

	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = stream.Run(ctx, "prod", "watcher", "", service.ChannelMD5{Topology: cur}, sink) }()
	sink.waitFor(t, 2*time.Second, func(e sse.Event) bool { return e.Type == sse.EventReady })

	// 新实例上线 + 唤醒拓扑 → 应近实时收到 topology-changed，且摘要变化。
	mustRegister(t, reg, "prod", "lobby-2", "10.0.0.2:25565")
	notifier.NotifyTopologyChange("prod")
	evt := sink.waitFor(t, 2*time.Second, func(e sse.Event) bool { return e.Type == sse.EventTopologyChanged })
	if evt.MD5 == cur {
		t.Fatalf("上线后拓扑摘要应变化，实际仍为 %q", cur)
	}
}

// TestStreamRunLivePushOnReassign 改派 zone → 推 topology-changed。
func TestStreamRunLivePushOnReassign(t *testing.T) {
	_, stream, reg, notifier := streamSqliteStack(t)
	mustRegister(t, reg, "prod", "lobby-1", "10.0.0.1:25565")
	cur := probeCurrentTopologyDigest(t, stream)

	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = stream.Run(ctx, "prod", "watcher", "", service.ChannelMD5{Topology: cur}, sink) }()
	sink.waitFor(t, 2*time.Second, func(e sse.Event) bool { return e.Type == sse.EventReady })

	reg.UpdateAssignment("prod", "lobby-1", "area2", "zoneZ")
	notifier.NotifyTopologyChange("prod")
	evt := sink.waitFor(t, 2*time.Second, func(e sse.Event) bool { return e.Type == sse.EventTopologyChanged })
	if evt.MD5 == cur {
		t.Fatalf("改派后拓扑摘要应变化，实际仍为 %q", cur)
	}
}

// TestStreamRunNoPushWhenTopologyUnchanged 唤醒但拓扑实际未变（仅运行指标变化）→ 不推 topology-changed。
func TestStreamRunNoPushWhenTopologyUnchanged(t *testing.T) {
	_, stream, reg, notifier := streamSqliteStack(t)
	mustRegister(t, reg, "prod", "lobby-1", "10.0.0.1:25565")
	cur := probeCurrentTopologyDigest(t, stream)

	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = stream.Run(ctx, "prod", "watcher", "", service.ChannelMD5{Topology: cur}, sink) }()
	sink.waitFor(t, 2*time.Second, func(e sse.Event) bool { return e.Type == sse.EventReady })

	// 仅改运行指标（不入拓扑摘要）后唤醒拓扑 → 重算摘要不变 → 不应再推 topology-changed。
	reg.Report("prod", "lobby-1", "md5x", 42, 19.9, 128<<20, 512<<20, 0.35)
	notifier.NotifyTopologyChange("prod")
	// 给直播循环重算的时间窗口；窗口内不应出现 topology-changed。
	if gotUnexpected(sink, 300*time.Millisecond, func(e sse.Event) bool { return e.Type == sse.EventTopologyChanged }) {
		t.Fatal("拓扑未变（仅运行指标变化）不应推 topology-changed")
	}
}

// mustRegister 往注册表注册一个在线实例（失败即 fatal）。
func mustRegister(t *testing.T, reg *runtime.Registry, ns, serverID, addr string) {
	t.Helper()
	inst := &runtime.Instance{Namespace: ns, ServerID: serverID, Role: "bukkit", Address: addr}
	if _, err := reg.Register(inst, 30*time.Second, time.Now().UTC()); err != nil {
		t.Fatalf("注册实例 %s 失败: %v", serverID, err)
	}
}

// probeCurrentTopologyDigest 用空拓扑摘要开一条临时流取当前拓扑摘要（首个 topology-changed 携带）。
func probeCurrentTopologyDigest(t *testing.T, stream *service.StreamService) string {
	t.Helper()
	sink := newRecordingSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = stream.Run(ctx, "prod", "probe", "", service.ChannelMD5{}, sink) }()
	evt := sink.waitFor(t, 2*time.Second, func(e sse.Event) bool { return e.Type == sse.EventTopologyChanged })
	if evt.MD5 == "" {
		t.Fatal("探针流应取到当前拓扑摘要")
	}
	return evt.MD5
}

// gotUnexpected 在期限内若收到满足条件的事件返回 true（用于断言"不应推"）。
func gotUnexpected(s *recordingSink, within time.Duration, pred func(sse.Event) bool) bool {
	deadline := time.After(within)
	for {
		select {
		case e := <-s.ch:
			if pred(e) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}
