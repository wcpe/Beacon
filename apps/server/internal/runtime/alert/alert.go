// Package alert 是控制面健康告警通道的可扩展抽象（见 ADR-0019）。
// Alerter 是单一告警出口接口，Dispatcher 扇出到多个通道并逐通道兜错；
// 第一版实现站内信（InboxAlerter）与 webhook（WebhookAlerter），新通道只实现 Alerter 即可接入。
package alert

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// 告警事件类型（与 model.AlertEventType* 对齐；本包不 import model 以避免 runtime→model 之外的耦合，
// 由调用方按需传入）。健康流转是唯一有真实触发点的历史类型，交付失败（FR-274）为本轮新增触发点。
const (
	// TypeHealthTransition 实例健康状态异常转移（degraded / lost / offline）。
	TypeHealthTransition = "health-transition"
	// TypePublishFail 交付失败（配置/文件发布失败，FR-274 起有真实触发点）。
	TypePublishFail = "publish-fail"
)

// Alert 是一条告警事件。
//
// 历史上本结构是**健康专用**（只承载 PrevStatus→Status 的状态转移）。FR-274 起需要承载交付失败，
// 故加性扩展 [Type] 与 [Detail]：健康路径的既有字段与消费方式完全不变（PersistAlerter 显式写
// health-transition），新增类型只需填 Type / Detail / Message 语义。
type Alert struct {
	// Type 事件类型（见本包 Type* 常量）；空串按健康流转处理，保持老调用方零改动。
	Type       string    `json:"type,omitempty"`
	Namespace  string    `json:"namespace"`
	ServerID   string    `json:"serverId"`
	Address    string    `json:"address"`
	PrevStatus string    `json:"prevStatus"`
	Status     string    `json:"status"`
	At         time.Time `json:"at"`
	// Message 人读摘要；为空时由各通道按类型自行拼装（健康路径沿用「serverId prev → status」）。
	Message string `json:"message,omitempty"`
	// Detail 结构化上下文（如交付失败的 orderId 与逐插件失败清单）；健康路径不看该字段。
	Detail map[string]any `json:"detail,omitempty"`
}

// EventType 返回实际事件类型（空串归一为健康流转，兼容既有调用方）。
func (a Alert) EventType() string {
	if a.Type == "" {
		return TypeHealthTransition
	}
	return a.Type
}

// Alerter 是一个告警通道：把一条告警投递出去（单一职责）。新增通道只实现本接口。
type Alerter interface {
	// Name 返回通道名（仅用于日志辨识）。
	Name() string
	// Notify 投递一条告警；通道内部失败返回 error，由 Dispatcher 兜错。
	Notify(ctx context.Context, a Alert) error
}

// Dispatcher 持有若干告警通道，扇出分发并逐通道兜错（某通道失败仅告警、不阻断其余通道）。
type Dispatcher struct {
	channels []Alerter

	// 异步扇出的有界队列与固定 worker（见 DispatchAsync）。
	//
	// 惰性启动：只用 Dispatch（同步）的调用方不会凭空多出一组 goroutine。
	startOnce sync.Once
	queue     chan Alert
	workers   sync.WaitGroup
	stopOnce  sync.Once
	stop      chan struct{}
	closed    atomic.Bool
	// enqueueLock 串行化「判关停 + 懒启动 + 入队」，消除与 Shutdown 的交错（见 DispatchAsync）。
	enqueueLock sync.Mutex
	// dropped 统计被丢弃的告警条数（队列满或已关停），用于显式暴露「丢了」而不是静默。
	dropped atomic.Uint64
}

// 异步扇出的容量参数。
//
// 取「队列 256 + worker 4」的理由：告警是旁路，既不能阻塞主循环，也不能在批量失败时
// 无限制地起 goroutine。webhook 通道默认 3s、可配到 60s，4 个 worker 已能覆盖
// 「一轮 tick 内几十个目标同时失败」的常见形态；再多的量本就该被丢弃并计数——
// 宁可显式丢，也不要让进程被自己的告警压垮。
const (
	asyncQueueSize    = 256
	asyncWorkerCount  = 4
	dropLogEveryNDrop = 50 // 丢弃告警的日志节流：首条 + 每满 N 条，避免刷屏
)

// NewDispatcher 构造分发器（无通道时分发为空操作）。
func NewDispatcher(channels ...Alerter) *Dispatcher {
	// stop 在构造期就分配：Shutdown 可能先于任何 DispatchAsync 到达，
	// 若把它放在惰性启动里会与「读 stop」构成竞态。
	return &Dispatcher{channels: channels, stop: make(chan struct{})}
}

// Dispatch 顺序扇出到各通道；任一通道返回 error 仅记 WARN 并继续，绝不 panic、不阻断健康扫描。
func (d *Dispatcher) Dispatch(ctx context.Context, a Alert) {
	d.dispatchSync(ctx, a)
}

// DispatchAsync 把扇出交给**有界**工作队列，立即返回。
//
// 供「调用方处于单 goroutine 驱动的主循环」的场景使用（典型：交付推进器逐目标推进）。
// 同步扇出在那种位置上会出真问题：webhook 通道默认 3000ms、可配到 60000ms，一轮 tick 里
// 多个目标同时失败时超时会累加，整轮推进（含其余目标与全部活动单）被串行阻塞，
// 超时判定被推迟、detectStall 也会饿着。Dispatch 的逐通道兜错只保证**不传播 error**，
// 并不保证**不阻塞**——这是两件事。
//
// ## 为什么是有界队列而不是「每个告警一个 goroutine」
//
// 每个告警一个 goroutine 在 mass failure 时会瞬时起等量 goroutine + 等量 HTTP 连接
// （一整批目标同时失败并不罕见）。有界队列把并发钉在 [asyncWorkerCount]，超出的**显式丢弃**
// 并计数告警——告警是旁路，不得反噬主路径。
//
// 用 context.WithoutCancel 保留调用方的值语义（trace id 等）但摘掉取消：调用方的 ctx 往往
// 随本轮 tick 结束，直接沿用会让异步投递一出生就被取消掉。
//
// 签名保留 ctx 是为了不破坏既有调用方（加性），但队列化之后投递发生在 worker 内、
// 与调用方 ctx 的生命周期无关，故这里不消费它。
func (d *Dispatcher) DispatchAsync(_ context.Context, a Alert) {
	if d == nil {
		return
	}
	// 「判关停 → 懒启动 worker → 入队」必须是一个原子段。
	//
	// 不加锁时存在这个交错：本线程读到 closed=false 后，Shutdown 完整跑完（此时 workers
	// 计数为 0，Wait 立即返回并报成功），随后 worker 才被拉起且立刻看到 stop 已关而退出，
	// 调用方紧接着入队成功 —— 那条告警落进一个**没有消费者的队列**，既投不出去也不计入
	// dropped，正是「丢告警绝不静默」要禁止的形态。
	//
	// 锁只保护入队这一段（几纳秒），不涉及通道投递，不会让旁路拖慢主循环。
	d.enqueueLock.Lock()
	defer d.enqueueLock.Unlock()
	if d.closed.Load() {
		d.reportDrop(a, "分发器已关停")
		return
	}
	d.startWorkers()
	// 非阻塞入队：队列满即丢，绝不让调用方（推进器主循环）等在这里。
	select {
	case d.queue <- a:
	default:
		d.reportDrop(a, "队列已满")
	}
}

// reportDrop 记录并节流上报丢弃的告警。
//
// 只计数不静默：丢告警本身是可接受的降级（旁路），但「丢了却没人知道」不可接受。
func (d *Dispatcher) reportDrop(a Alert, reason string) {
	n := d.dropped.Add(1)
	if n == 1 || n%dropLogEveryNDrop == 0 {
		slog.Warn("告警写入队列失败，已丢弃",
			"原因", reason, "累计丢弃", n, "类型", a.Type,
			"namespace", a.Namespace, "serverId", a.ServerID)
	}
}

// Dropped 返回累计丢弃的告警条数（供指标与自检读取）。
func (d *Dispatcher) Dropped() uint64 {
	if d == nil {
		return 0
	}
	return d.dropped.Load()
}

// startWorkers 惰性启动固定数量的异步 worker（只启动一次）。
func (d *Dispatcher) startWorkers() {
	d.startOnce.Do(func() {
		d.queue = make(chan Alert, asyncQueueSize)
		for i := 0; i < asyncWorkerCount; i++ {
			d.workers.Add(1)
			go d.work()
		}
	})
}

// work 是异步 worker 主循环：取队列 → 扇出；收到停止信号后把队列里**剩余**的告警投完再退出。
//
// 退出前必须把队列 drain 完：那是「已接受但还没发」的告警，丢掉它们等于关停时静默吞掉最后一批。
func (d *Dispatcher) work() {
	defer d.workers.Done()
	for {
		select {
		case a := <-d.queue:
			d.dispatchSafe(a)
		case <-d.stop:
			// 停止信号优先，但仍把已在队列里的投完（非阻塞地取到空为止）。
			for {
				select {
				case a := <-d.queue:
					d.dispatchSafe(a)
				default:
					return
				}
			}
		}
	}
}

// dispatchSafe 在 worker 内扇出，兜住畸形通道实现抛出的 panic（不得打死进程）。
func (d *Dispatcher) dispatchSafe(a Alert) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("告警异步扇出 panic（已兜住）",
				"namespace", a.Namespace, "serverId", a.ServerID, "类型", a.Type, "panic", r)
		}
	}()
	// 异步路径没有调用方的 ctx 可用（它随本轮 tick 结束）；此处不带取消语义。
	d.dispatchSync(context.Background(), a)
}

// Shutdown 停止接收新告警，并等待在途与排队中的告警投递完成。
//
// 控制面优雅退出时若不等这一下，最后一批告警会静默丢失——正是最需要留痕的那一批。
// [ctx] 用于给 drain 设上限：到点仍未投完就返回 ctx 的错误，由调用方决定是否记录。
func (d *Dispatcher) Shutdown(ctx context.Context) error {
	if d == nil {
		return nil
	}
	// 与入队共用同一把锁：要么「先置 closed 再无可入队」，要么「本次入队已完整完成」，
	// 不会出现 worker 尚未启动、告警却已进队列的中间态。
	d.enqueueLock.Lock()
	d.closed.Store(true)
	d.stopOnce.Do(func() { close(d.stop) })
	d.enqueueLock.Unlock()
	done := make(chan struct{})
	go func() {
		d.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// dispatchSync 是同步扇出本体，供 Dispatch 与 DispatchAsync 共用。
func (d *Dispatcher) dispatchSync(ctx context.Context, a Alert) {
	for _, ch := range d.channels {
		if err := ch.Notify(ctx, a); err != nil {
			slog.Warn("告警通道投递失败",
				"通道", ch.Name(), "namespace", a.Namespace, "serverId", a.ServerID, "状态", a.Status, "错误", err)
		}
	}
}
