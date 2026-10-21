// Package alert 是控制面健康告警通道的可扩展抽象（见 ADR-0019）。
// Alerter 是单一告警出口接口，Dispatcher 扇出到多个通道并逐通道兜错；
// 第一版实现站内信（InboxAlerter）与 webhook（WebhookAlerter），新通道只实现 Alerter 即可接入。
package alert

import (
	"context"
	"log/slog"
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
}

// NewDispatcher 构造分发器（无通道时分发为空操作）。
func NewDispatcher(channels ...Alerter) *Dispatcher {
	return &Dispatcher{channels: channels}
}

// Dispatch 顺序扇出到各通道；任一通道返回 error 仅记 WARN 并继续，绝不 panic、不阻断健康扫描。
func (d *Dispatcher) Dispatch(ctx context.Context, a Alert) {
	d.dispatchSync(ctx, a)
}

// DispatchAsync 把扇出交给独立 goroutine，**立即返回**。
//
// 供「调用方处于单 goroutine 驱动的主循环」的场景使用（典型：交付推进器逐目标推进）。
// 同步扇出在那种位置上会出真问题：webhook 通道默认 3000ms、可配到 60000ms，一轮 tick 里
// 多个目标同时失败时超时会累加，整轮推进（含其余目标与全部活动单）被串行阻塞，
// 超时判定被推迟、detectStall 也会饿着。Dispatch 的逐通道兜错只保证**不传播 error**，
// 并不保证**不阻塞**——这是两件事。
//
// 用 context.WithoutCancel 保留调用方的值语义（trace id 等）但摘掉取消：调用方的 ctx 往往
// 随本轮 tick 结束，直接沿用会让异步投递一出生就被取消掉。
func (d *Dispatcher) DispatchAsync(ctx context.Context, a Alert) {
	go func() {
		defer func() {
			// 告警是旁路：任何畸形通道实现（如 Notify 内 panic）都不得打死进程。
			if r := recover(); r != nil {
				slog.Error("告警异步扇出 panic（已兜住）",
					"namespace", a.Namespace, "serverId", a.ServerID, "类型", a.Type, "panic", r)
			}
		}()
		d.dispatchSync(context.WithoutCancel(ctx), a)
	}()
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
