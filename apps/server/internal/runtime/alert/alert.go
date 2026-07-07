// Package alert 是控制面健康告警通道的可扩展抽象（见 ADR-0019）。
// Alerter 是单一告警出口接口，Dispatcher 扇出到多个通道并逐通道兜错；
// 第一版实现站内信（InboxAlerter）与 webhook（WebhookAlerter），新通道只实现 Alerter 即可接入。
package alert

import (
	"context"
	"log/slog"
	"time"
)

// Alert 是一条健康告警事件（实例状态从 PrevStatus 异常转移到 Status）。
type Alert struct {
	Namespace  string    `json:"namespace"`
	ServerID   string    `json:"serverId"`
	Address    string    `json:"address"`
	PrevStatus string    `json:"prevStatus"`
	Status     string    `json:"status"`
	At         time.Time `json:"at"`
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
	for _, ch := range d.channels {
		if err := ch.Notify(ctx, a); err != nil {
			slog.Warn("告警通道投递失败",
				"通道", ch.Name(), "namespace", a.Namespace, "serverId", a.ServerID, "状态", a.Status, "错误", err)
		}
	}
}
