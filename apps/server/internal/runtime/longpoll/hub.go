// Package longpoll 实现配置长轮询的 waiter 注册与唤醒（纯进程内通知，无 Redis/MQ）。
// Hub 只发信号，不碰 DB；被唤醒方重跑解析比对 md5（"唤醒即重算"在调用方）。
package longpoll

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Waiter 是一个挂起的长轮询登记项。
type Waiter struct {
	namespace string
	serverID  string
	// 唤醒信号；缓冲为 1，非阻塞 send 保证发布方不被慢 waiter 阻塞，重复唤醒幂等
	notify chan struct{}
}

// NotifyChan 返回只读的唤醒信号通道，供 SSE 推送在 select 中直接多路等待
// （长轮询用 Wait 单次等待，SSE 直播用本通道在循环里持续监听，二者共用同一缓冲信号）。
func (w *Waiter) NotifyChan() <-chan struct{} {
	return w.notify
}

// Wait 在「被唤醒」「超时」「客户端断连」之间多路等待。
// 返回 true 表示被唤醒（调用方应重算比对），false 表示超时或断连。
func (w *Waiter) Wait(ctx context.Context, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.notify:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// Hub 是 waiter 注册表，按 (namespace, serverId) 索引。
type Hub struct {
	mu sync.Mutex
	// key = namespace + "\x00" + serverId；一个实例可有多个并发 waiter（重连叠加）
	waiters map[string]map[*Waiter]struct{}
}

// NewHub 构造空 Hub。
func NewHub() *Hub {
	return &Hub{waiters: map[string]map[*Waiter]struct{}{}}
}

// Register 登记并返回一个 waiter（调用方须 defer Deregister）。
func (h *Hub) Register(ns, serverID string) *Waiter {
	w := &Waiter{namespace: ns, serverID: serverID, notify: make(chan struct{}, 1)}
	k := key(ns, serverID)
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.waiters[k]
	if set == nil {
		set = map[*Waiter]struct{}{}
		h.waiters[k] = set
	}
	set[w] = struct{}{}
	return w
}

// Deregister 摘除 waiter（返回即清理，防泄漏）。
func (h *Hub) Deregister(w *Waiter) {
	k := key(w.namespace, w.serverID)
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.waiters[k]
	if set == nil {
		return
	}
	delete(set, w)
	if len(set) == 0 {
		delete(h.waiters, k)
	}
}

// Notify 唤醒指定 serverId 集合下的所有 waiter（非阻塞、幂等）。
func (h *Hub) Notify(ns string, serverIDs []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, sid := range serverIDs {
		for w := range h.waiters[key(ns, sid)] {
			signal(w)
		}
	}
}

// NotifyNamespace 唤醒某 namespace 下的所有 waiter（用于 global 层变更）。
func (h *Hub) NotifyNamespace(ns string) {
	prefix := ns + "\x00"
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, set := range h.waiters {
		if strings.HasPrefix(k, prefix) {
			for w := range set {
				signal(w)
			}
		}
	}
}

// WaiterCount 返回当前挂起的 waiter 总数（测试/观测用）。
func (h *Hub) WaiterCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, set := range h.waiters {
		n += len(set)
	}
	return n
}

// signal 向 waiter 发非阻塞唤醒信号（缓冲已满即视为已通知）。
func signal(w *Waiter) {
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func key(ns, serverID string) string {
	return ns + "\x00" + serverID
}
