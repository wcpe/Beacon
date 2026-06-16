package longpoll

import (
	"context"
	"testing"
	"time"
)

// TestNotifyWakesWaiter 注册后被 Notify 唤醒，Wait 返回 true。
func TestNotifyWakesWaiter(t *testing.T) {
	h := NewHub()
	w := h.Register("prod", "lobby-1")
	defer h.Deregister(w)
	h.Notify("prod", []string{"lobby-1"})
	if !w.Wait(context.Background(), time.Second) {
		t.Fatal("被 Notify 后 Wait 应返回 true")
	}
}

// TestBufferedSignalNotLost 注册后、Wait 前到达的 Notify 不丢（缓冲为 1）。
func TestBufferedSignalNotLost(t *testing.T) {
	h := NewHub()
	w := h.Register("prod", "lobby-1")
	defer h.Deregister(w)
	// 先 Notify（waiter 尚未进入 Wait），信号应被缓冲
	h.Notify("prod", []string{"lobby-1"})
	// 随后 Wait 应立即返回 true（拿到缓冲信号）
	if !w.Wait(context.Background(), 50*time.Millisecond) {
		t.Fatal("注册后 Wait 前的 Notify 不应丢失")
	}
}

// TestWaitTimeout 无唤醒到超时返回 false。
func TestWaitTimeout(t *testing.T) {
	h := NewHub()
	w := h.Register("prod", "lobby-1")
	defer h.Deregister(w)
	if w.Wait(context.Background(), 30*time.Millisecond) {
		t.Fatal("无唤醒应超时返回 false")
	}
}

// TestWaitCtxCancel 客户端断连（ctx 取消）返回 false。
func TestWaitCtxCancel(t *testing.T) {
	h := NewHub()
	w := h.Register("prod", "lobby-1")
	defer h.Deregister(w)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if w.Wait(ctx, time.Second) {
		t.Fatal("ctx 取消应返回 false")
	}
}

// TestNotifyOnlyTargeted 只唤醒目标 serverId，其余不动。
func TestNotifyOnlyTargeted(t *testing.T) {
	h := NewHub()
	wA := h.Register("prod", "a")
	wB := h.Register("prod", "b")
	defer h.Deregister(wA)
	defer h.Deregister(wB)
	h.Notify("prod", []string{"a"})
	if !wA.Wait(context.Background(), 50*time.Millisecond) {
		t.Fatal("a 应被唤醒")
	}
	if wB.Wait(context.Background(), 30*time.Millisecond) {
		t.Fatal("b 不应被唤醒")
	}
}

// TestNotifyNamespace 唤醒整个 namespace 下的 waiter。
func TestNotifyNamespace(t *testing.T) {
	h := NewHub()
	wA := h.Register("prod", "a")
	wB := h.Register("prod", "b")
	wOther := h.Register("test", "c")
	defer h.Deregister(wA)
	defer h.Deregister(wB)
	defer h.Deregister(wOther)
	h.NotifyNamespace("prod")
	if !wA.Wait(context.Background(), 50*time.Millisecond) || !wB.Wait(context.Background(), 50*time.Millisecond) {
		t.Fatal("prod 下 a/b 都应被唤醒")
	}
	if wOther.Wait(context.Background(), 30*time.Millisecond) {
		t.Fatal("test 命名空间的 waiter 不应被唤醒")
	}
}

// TestDeregisterCleansUp 摘除后计数归零。
func TestDeregisterCleansUp(t *testing.T) {
	h := NewHub()
	w := h.Register("prod", "lobby-1")
	if h.WaiterCount() != 1 {
		t.Fatalf("注册后应有 1 个 waiter，实际 %d", h.WaiterCount())
	}
	h.Deregister(w)
	if h.WaiterCount() != 0 {
		t.Fatalf("摘除后应为 0，实际 %d", h.WaiterCount())
	}
}
