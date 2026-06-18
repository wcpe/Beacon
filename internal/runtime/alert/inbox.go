package alert

import (
	"context"
	"sync"
)

// InboxAlerter 是站内信告警通道：进程内固定容量环形缓存，管理台可只读（GET /admin/v1/alerts）。
// 持独立 RWMutex，与三大运行态锁（Registry/Hub/Health）不嵌套；不持久化，控制面重启清零（见 ADR-0019）。
type InboxAlerter struct {
	mu       sync.RWMutex
	capacity int
	buf      []Alert // 环形缓存底层数组（cap 上限 capacity）
	next     int     // 下一个写入位置
	full     bool    // 是否已写满一圈
}

// NewInboxAlerter 构造站内信通道；capacity<=0 兜底为 1，避免无效容量。
func NewInboxAlerter(capacity int) *InboxAlerter {
	if capacity <= 0 {
		capacity = 1
	}
	return &InboxAlerter{capacity: capacity, buf: make([]Alert, 0, capacity)}
}

// Name 返回通道名。
func (i *InboxAlerter) Name() string { return "inbox" }

// Notify 把告警写入环形缓存（超容量覆盖最旧）。
func (i *InboxAlerter) Notify(_ context.Context, a Alert) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.buf) < i.capacity {
		i.buf = append(i.buf, a)
		return nil
	}
	// 已满：在 next 位置覆盖最旧条目
	i.buf[i.next] = a
	i.next = (i.next + 1) % i.capacity
	i.full = true
	return nil
}

// List 返回站内信快照，最新在前；快照与内部数组隔离，外部改动不影响内部。
func (i *InboxAlerter) List() []Alert {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make([]Alert, 0, len(i.buf))
	if !i.full {
		// 未写满：buf[0..len) 即按写入顺序，反转为最新在前
		for k := len(i.buf) - 1; k >= 0; k-- {
			out = append(out, i.buf[k])
		}
		return out
	}
	// 已写满：从最新（next-1）往回读一圈
	for k := 0; k < i.capacity; k++ {
		idx := (i.next - 1 - k + i.capacity) % i.capacity
		out = append(out, i.buf[idx])
	}
	return out
}
