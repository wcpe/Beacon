package service

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
)

// 进度事件类型（对齐前端 events-timeline：order_status / batch_status / 其它归 target）。
const (
	deliveryEventOrder  = "order_status"
	deliveryEventBatch  = "batch_status"
	deliveryEventTarget = "target_status"
)

// —— 进度事件 Hub（SSE 实时推送，spec §5.1；断线回退轮询 /events JSON 形态）——

// deliveryEventHub 是按 orderId 索引的进度事件发布订阅（进程内、无中间件）：推进器发布、SSE 订阅。
// 非阻塞投递（缓冲满即丢，慢消费者不拖累推进器）；seq 全局单调递增供前端排序。
type deliveryEventHub struct {
	mu   sync.Mutex
	subs map[uint]map[chan ChangeOrderEventView]struct{}
	seq  int64
}

// newDeliveryEventHub 构造空事件 Hub。
func newDeliveryEventHub() *deliveryEventHub {
	return &deliveryEventHub{subs: map[uint]map[chan ChangeOrderEventView]struct{}{}}
}

// subscribe 登记一个订阅者并返回其只读通道与注销函数（调用方须 defer 注销）。
func (h *deliveryEventHub) subscribe(orderID uint) (<-chan ChangeOrderEventView, func()) {
	ch := make(chan ChangeOrderEventView, 64)
	h.mu.Lock()
	if h.subs[orderID] == nil {
		h.subs[orderID] = map[chan ChangeOrderEventView]struct{}{}
	}
	h.subs[orderID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.subs[orderID], ch)
		if len(h.subs[orderID]) == 0 {
			delete(h.subs, orderID)
		}
		close(ch) // 与 publish 同持 h.mu：已从 map 摘除，publish 不会再向本通道发（无向已关闭通道发送之虞）
	}
}

// publish 向某单全部订阅者非阻塞投递事件（缓冲满即丢弃本条，不阻塞推进器）；赋全局单调 seq。
func (h *deliveryEventHub) publish(orderID uint, evt ChangeOrderEventView) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	evt.Seq = int(h.seq)
	for ch := range h.subs[orderID] {
		select {
		case ch <- evt:
		default:
		}
	}
}

// deliveryEventSink 是 SSE 输出汇（由 handler 用 ResponseWriter+Flusher 实现）：写事件帧与保活注释。
type deliveryEventSink interface {
	Send(evt ChangeOrderEventView) error
	Ping() error
}

// StreamEvents 以 SSE 推送某单实时进度（spec §5.1）：先补发派生快照（单 / 批时间线），再流式推实时事件，
// 直到客户端断连（ctx 取消）。25s 保活注释穿透代理缓冲。校验单存在由调用方在设 SSE 头前完成。
func (s *DeliveryOrchestrator) StreamEvents(ctx context.Context, orderID uint, sink deliveryEventSink) error {
	snapshot, err := s.DerivedEvents(orderID)
	if err != nil {
		return err
	}
	ch, cancel := s.events.subscribe(orderID)
	defer cancel()
	for _, evt := range snapshot.Events {
		if e := sink.Send(evt); e != nil {
			return e
		}
	}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case evt := <-ch:
			if e := sink.Send(evt); e != nil {
				return e
			}
		case <-ping.C:
			if e := sink.Ping(); e != nil {
				return e
			}
		}
	}
}

// DerivedEvents 装配某单的派生事件快照（/events JSON 轮询形态与 SSE 连接初帧共用）：单生命周期 + 批次时间线。
// 逐目标事件仅走 SSE 实时流、不入派生快照（1000+ 目标会撑爆时间线，目标态查 /targets 分页）。
func (s *DeliveryOrchestrator) DerivedEvents(orderID uint) (*ChangeEventsView, error) {
	order, err := requireChangeOrder(s.repo, orderID)
	if err != nil {
		return nil, err
	}
	batches, err := s.repo.ListBatches(orderID)
	if err != nil {
		return nil, err
	}
	return &ChangeEventsView{Events: deriveChangeEventsFull(order, batches)}, nil
}

// emitOrderEvent / emitBatchEvent / emitTargetEvent 在状态迁移成功后向 Hub 发实时事件（就地快照的当前状态）。
func (s *DeliveryOrchestrator) emitOrderEvent(rt *orderRuntime) {
	s.events.publish(rt.order.ID, ChangeOrderEventView{
		At: s.now(), Type: deliveryEventOrder, OrderID: rt.order.ID, Status: rt.order.Status,
	})
}

func (s *DeliveryOrchestrator) emitBatchEvent(rt *orderRuntime, batch *model.ChangeBatch) {
	no := batch.BatchNo
	s.events.publish(rt.order.ID, ChangeOrderEventView{
		At: s.now(), Type: deliveryEventBatch, OrderID: rt.order.ID, BatchNo: &no, Status: batch.Status,
	})
}

func (s *DeliveryOrchestrator) emitTargetEvent(rt *orderRuntime, t *model.ChangeTarget) {
	no := rt.batchNoByID[t.BatchID]
	sid := t.ServerID
	s.events.publish(rt.order.ID, ChangeOrderEventView{
		At: s.now(), Type: deliveryEventTarget, OrderID: rt.order.ID, BatchNo: &no, ServerID: &sid, Status: t.Status,
	})
}

// deriveChangeEventsFull 派生单生命周期 + 批次时间线事件（按发生时间稳定排序、seq 从 1 重排）。
// 单事件复用 deriveChangeOrderEvents（保持既有派生口径），批事件由批次时间戳派生（running / observing / 终态）。
func deriveChangeEventsFull(order *model.ChangeOrder, batches []model.ChangeBatch) []ChangeOrderEventView {
	events := deriveChangeOrderEvents(order)
	for i := range batches {
		events = append(events, deriveBatchEvents(order.ID, &batches[i])...)
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
	for i := range events {
		events[i].Seq = i + 1
	}
	return events
}

// deriveBatchEvents 由批次时间戳派生批事件：started_at→running、observe_started_at→observing、finished_at→终态状态。
// awaiting_confirm 无独立时间戳、不派生（其到达由观察窗结束确定，实时事件走 SSE）。
func deriveBatchEvents(orderID uint, batch *model.ChangeBatch) []ChangeOrderEventView {
	no := batch.BatchNo
	events := make([]ChangeOrderEventView, 0, 3)
	add := func(status string, at time.Time) {
		events = append(events, ChangeOrderEventView{
			At: at, Type: deliveryEventBatch, OrderID: orderID, BatchNo: &no, Status: status,
		})
	}
	if batch.StartedAt != nil {
		add(model.ChangeBatchStatusRunning, *batch.StartedAt)
	}
	if batch.ObserveStartedAt != nil {
		add(model.ChangeBatchStatusObserving, *batch.ObserveStartedAt)
	}
	if batch.FinishedAt != nil && isBatchTerminalStatus(batch.Status) {
		add(batch.Status, *batch.FinishedAt)
	}
	return events
}

// isBatchTerminalStatus 判批状态是否为携带 finished_at 的终态（completed / failed / skipped）。
func isBatchTerminalStatus(status string) bool {
	switch status {
	case model.ChangeBatchStatusCompleted, model.ChangeBatchStatusFailed, model.ChangeBatchStatusSkipped:
		return true
	}
	return false
}

// —— 观察窗内存缓冲（spec §4.6.3，当前批全窗序列供 /observe 展示与展示同源熔断判定）——

// observeState 是某单当前批的观察窗内存序列（推进器采样写、Observe 读；observeMu 保护）。
type observeState struct {
	batchNo          int
	observeStartedAt *time.Time // nil 表示批仍 running（观察窗未正式开始）
	series           map[string][]ChangeObserveSeriesPoint
	lastBucket       map[string]int64
	order            []string // serverId 首次出现顺序，稳定输出
}

// sampleObserve 为当前批 activated 目标采样一点（健康分 / 等级 / TPS / 告警，读内存真源，5s 去重）。
// batchNo 变更即重置缓冲（新批新窗）；仅 activated 目标计入（含 running 期已 activated，spec §4.4.4/§4.6.3）。
func (s *DeliveryOrchestrator) sampleObserve(orderID uint, batchNo int, nsID uint, members []*model.ChangeTarget) {
	nowMs := s.now().UnixMilli()
	bucket := nowMs / deliveryObserveBucketMs * deliveryObserveBucketMs
	s.observeMu.Lock()
	defer s.observeMu.Unlock()
	st := s.observeByOrder[orderID]
	if st == nil || st.batchNo != batchNo {
		st = &observeState{batchNo: batchNo, series: map[string][]ChangeObserveSeriesPoint{}, lastBucket: map[string]int64{}}
		s.observeByOrder[orderID] = st
	}
	for _, t := range members {
		if t.Status != model.ChangeTargetStatusActivated {
			continue
		}
		if st.lastBucket[t.ServerID] == bucket && len(st.series[t.ServerID]) > 0 {
			continue // 本 5s 桶已采过，去重
		}
		if _, seen := st.series[t.ServerID]; !seen {
			st.order = append(st.order, t.ServerID)
		}
		st.series[t.ServerID] = append(st.series[t.ServerID], s.observePoint(nsID, t.ServerID, bucket))
		st.lastBucket[t.ServerID] = bucket
	}
}

// observePoint 组一采样点：健康分 / 等级读健康视图内存真源，TPS 读指标窗口，告警取 alert 因子原值。
func (s *DeliveryOrchestrator) observePoint(nsID uint, serverID string, tsMs int64) ChangeObserveSeriesPoint {
	point := ChangeObserveSeriesPoint{TsMs: tsMs, Level: "unknown"}
	if view, ok := s.health.Get(nsID, serverID); ok {
		point.Score = view.Score
		point.Level = view.Level
		point.Alerts = alertFactorCount(view.Factors)
	}
	if sample, ok := s.metrics.Latest(nsID, serverID); ok {
		point.TPS = sample.TPSAvg
	}
	return point
}

// markObserveStarted 批进入 observing 时登记观察窗开始时刻（供 /observe 展示窗口起点）。
func (s *DeliveryOrchestrator) markObserveStarted(orderID uint, batchNo int, startedAt time.Time) {
	s.observeMu.Lock()
	defer s.observeMu.Unlock()
	if st := s.observeByOrder[orderID]; st != nil && st.batchNo == batchNo {
		ts := startedAt
		st.observeStartedAt = &ts
	}
}

// clearObserve 清空某单观察窗缓冲（推进门确认切批 / 紧急终止后）。
func (s *DeliveryOrchestrator) clearObserve(orderID uint) {
	s.observeMu.Lock()
	delete(s.observeByOrder, orderID)
	s.observeMu.Unlock()
}

// ObserveSeries 读某单当前批观察窗序列（/observe 接真，spec §4.6.3）；无缓冲返回空形态（数组非 null）。
func (s *DeliveryOrchestrator) ObserveSeries(orderID uint) *ChangeObserveView {
	s.observeMu.RLock()
	defer s.observeMu.RUnlock()
	st := s.observeByOrder[orderID]
	if st == nil {
		return &ChangeObserveView{Targets: []ChangeObserveTargetSeries{}}
	}
	targets := make([]ChangeObserveTargetSeries, 0, len(st.order))
	for _, serverID := range st.order {
		points := make([]ChangeObserveSeriesPoint, len(st.series[serverID]))
		copy(points, st.series[serverID])
		targets = append(targets, ChangeObserveTargetSeries{ServerID: serverID, Series: points})
	}
	batchNo := st.batchNo
	return &ChangeObserveView{BatchNo: &batchNo, ObserveStartedAt: st.observeStartedAt, Targets: targets}
}

// alertFactorCount 取健康因子中 alert 因子的原始告警数（无则 0）。
func alertFactorCount(factors []healthview.Factor) int {
	for i := range factors {
		if factors[i].Factor == "alert" {
			return int(factors[i].Raw)
		}
	}
	return 0
}
