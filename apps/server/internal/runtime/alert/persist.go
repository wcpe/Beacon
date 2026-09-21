package alert

import (
	"context"
	"encoding/json"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// EventSink 是告警事件持久化的窄写依赖（由 service.AlertEventService 实现，FR-89）。
// 在 alert 包本地声明、依赖倒置，避免 alert→service 反向依赖（守循环依赖红线，与 WebhookSettings 同范式）。
type EventSink interface {
	// Record 落库一条告警事件；失败返回 error 由 Dispatcher 兜错（仅 WARN、不阻断）。
	Record(e *model.AlertEvent) error
	// ResolveAlertRole 解析实例分级角色（proxy / lobby / backend，FR-231）；未知 / 查不到返回空串（→ 按 backend 规则）。
	ResolveAlertRole(namespace, serverID string) string
}

// PersistAlerter 是告警留痕通道（FR-89，见 ADR-0041）：把一条告警额外持久化为 alert_event，
// 供管理台「事件」页历史信息流回看。与站内信 / webhook 并存——它们往外推 / 进程内即时，本通道留痕跨重启。
// 落库 IO 由调用方（健康扫描循环）在任何注册表锁之外触发（与 webhook 同，守 DB IO 锁外）；
// 落库失败仅由 Dispatcher 记 WARN，绝不阻断健康扫描与其它通道。
type PersistAlerter struct {
	sink EventSink
}

// NewPersistAlerter 构造留痕通道（sink 提供落库能力）。
func NewPersistAlerter(sink EventSink) *PersistAlerter {
	return &PersistAlerter{sink: sink}
}

// Name 返回通道名。
func (p *PersistAlerter) Name() string { return "persist" }

// Notify 把一条健康告警映射为 alert_event 落库。
// created_at 不在此设，交由 GORM 全局 NowFunc 统一填 UTC（保与全表一致）。
// 级别按 FR-231 判定矩阵（健康级别 × 角色）计算；角色经 sink 查控制面权威事实（查不到按 backend 规则降级）。
func (p *PersistAlerter) Notify(_ context.Context, a Alert) error {
	role := ""
	if p.sink != nil {
		role = p.sink.ResolveAlertRole(a.Namespace, a.ServerID)
	}
	return p.sink.Record(&model.AlertEvent{
		Type:      model.AlertEventTypeHealthTransition,
		Level:     GradeAlert(a.Status, role, nil),
		ToStatus:  a.Status,
		ServerID:  a.ServerID,
		Namespace: a.Namespace,
		Message:   a.ServerID + " " + a.PrevStatus + " → " + a.Status,
		Detail:    healthAlertDetail(a),
	})
}

// healthAlertDetail 把告警上下文序列化为 json 文本（落 detail TEXT 列）；序列化失败回退空串（不阻断留痕）。
func healthAlertDetail(a Alert) string {
	b, err := json.Marshal(map[string]string{
		"address":    a.Address,
		"prevStatus": a.PrevStatus,
		"status":     a.Status,
	})
	if err != nil {
		return ""
	}
	return string(b)
}
