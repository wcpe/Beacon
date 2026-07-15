package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/metricwindow"
)

// healthLevelUnhealthy 是健康域「不健康」等级别名（健康恶化熔断判定用，避免 advance 文件再引 healthview 包）。
const healthLevelUnhealthy = healthview.LevelUnhealthy

// errCASSkip 是事务内 CAS 未命中的哨兵错误（并发已迁移，非真错误）：用于回滚小事务且让调用方不误报为错误。
var errCASSkip = errors.New("cas 未命中（并发已迁移，非错误）")

// errOrSkip 把事务内 CAS 结果归一为错误：真错误原样返回；仅「未命中」返回 errCASSkip（回滚该小事务）。
func errOrSkip(err error, ok bool) error {
	if err != nil {
		return err
	}
	if !ok {
		return errCASSkip
	}
	return nil
}

// 编排推进器节律与观察窗采样粒度（走常量，不硬编码散落；均为控制面内部节律、非运维旋钮）。
const (
	// deliveryTickInterval 推进器周期：驱动超时判定、观察窗计时与采样；回执经 wakeCh 即时唤醒不必等 tick。
	deliveryTickInterval = 2 * time.Second
	// deliveryObserveBucketMs 观察窗采样去重桶（5s 粒度，spec §4.6.3）。
	deliveryObserveBucketMs = 5000
)

// deliveryActiveOrderStatuses 是推进器每轮装载并推进的单状态集（spec §4.1 恢复语义）：
// rolling 全量推进；paused 仅收口在途目标（不下发新目标 / 新批）。rolling_back 归 M5，本版不推进。
var deliveryActiveOrderStatuses = []string{
	model.ChangeOrderStatusRolling, model.ChangeOrderStatusPaused,
}

// DeliveryOrchestrator 是交付编排 M3 灰度推进引擎（FR-166，spec §4.1/§4.4/§4.6）：
// 进程内单 goroutine 驱动 rolling 单的批次推进 → 命令下发 → 回执驱动三层状态机 → 熔断 / 推进门 → 完成。
//
// 单一驱动源：**只有推进器 goroutine 改 change_batch / change_target / change_order 的执行态**；
// agent 回执（DeliveryBlobService.ReceiveResult）只落命令 CAS 并唤醒推进器（WakeOrder），
// 由推进器读命令终态推进目标——避免回执与推进器并发改状态（取舍见报告）。
// 状态与计数全落库，控制面重启后 drainActive 按库内状态恢复推进（仿 archive_service）。
type DeliveryOrchestrator struct {
	db       *gorm.DB
	repo     *repository.ChangeOrderRepository
	blobs    *DeliveryBlobService
	cmdRepo  *repository.AgentCommandRepository
	audit    *repository.AuditLogRepository
	health   *healthview.Store
	metrics  *metricwindow.Store
	notifier CommandNotifier
	events   *deliveryEventHub
	now      func() time.Time
	wakeCh   chan struct{}
	// mu 串行化控制操作（Start / Pause / Resume / Cancel / ConfirmBatch）与每轮推进，使三层状态机迁移不相互竞争。
	mu sync.Mutex
	// observeMu 独立保护观察窗内存缓冲（推进器采样写、Observe/SSE 读），与 mu 有序嵌套（mu→observeMu，不反向）。
	observeMu      sync.RWMutex
	observeByOrder map[uint]*observeState
}

// NewDeliveryOrchestrator 构造编排推进器。
func NewDeliveryOrchestrator(db *gorm.DB, repo *repository.ChangeOrderRepository, blobs *DeliveryBlobService,
	cmdRepo *repository.AgentCommandRepository, audit *repository.AuditLogRepository,
	health *healthview.Store, metrics *metricwindow.Store, notifier CommandNotifier) *DeliveryOrchestrator {
	return &DeliveryOrchestrator{
		db: db, repo: repo, blobs: blobs, cmdRepo: cmdRepo, audit: audit,
		health: health, metrics: metrics, notifier: notifier,
		events:         newDeliveryEventHub(),
		now:            func() time.Time { return time.Now().UTC() },
		wakeCh:         make(chan struct{}, 1),
		observeByOrder: map[uint]*observeState{},
	}
}

// Run 启动推进循环，直到 ctx 取消（随关停信号优雅退出）。
// 启动先 drainActive 恢复残留 rolling / paused 单（控制面重启续跑，spec §4.1）；此后 ticker + wakeCh 双驱动。
func (s *DeliveryOrchestrator) Run(ctx context.Context) {
	slog.Info("交付编排推进器已启动", "周期", deliveryTickInterval.String())
	s.advanceActiveOrders(ctx)
	ticker := time.NewTicker(deliveryTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("交付编排推进器已停止")
			return
		case <-s.wakeCh:
			s.advanceActiveOrders(ctx)
		case <-ticker.C:
			s.advanceActiveOrders(ctx)
		}
	}
}

// WakeOrder 由 agent 回执落定后调用（DeliveryBlobService 经 progress waker 接口注入），即时唤醒推进器重扫。
// 进程内全局唤醒即可（推进器每轮重扫全部活动单），不需按 orderId 精确定位。
func (s *DeliveryOrchestrator) WakeOrder(uint) { s.wake() }

// wake 非阻塞唤醒推进器（channel 满即已有待处理信号，丢弃本次不阻塞）。
func (s *DeliveryOrchestrator) wake() {
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

// advanceActiveOrders 装载并推进全部活动单（rolling 全量推进、paused 仅收口在途）；持 mu 与控制操作互斥。
func (s *DeliveryOrchestrator) advanceActiveOrders(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	orders, err := s.repo.ListActiveOrders(deliveryActiveOrderStatuses)
	if err != nil {
		slog.Error("交付编排推进器装载活动单失败", "错误", err)
		return
	}
	for i := range orders {
		if ctx.Err() != nil {
			return
		}
		rt, e := s.loadOrderRuntime(&orders[i])
		if e != nil {
			slog.Error("交付编排推进器装载单快照失败", "orderId", orders[i].ID, "错误", e)
			continue
		}
		s.advanceOrder(rt)
	}
}

// orderRuntime 是一次推进所需的单快照（单 + 批次 + 目标 + namespace code + batch_id→batch_no 索引），
// 一轮装载一次、传递复用防重复查库；推进函数就地更新 targets/batches 主状态以保本轮快照一致。
type orderRuntime struct {
	order       *model.ChangeOrder
	batches     []model.ChangeBatch
	targets     []model.ChangeTarget
	nsCode      string
	batchNoByID map[uint]int
}

// loadOrderRuntime 装载单的批次 / 目标快照、namespace code 与批次号索引。
func (s *DeliveryOrchestrator) loadOrderRuntime(order *model.ChangeOrder) (*orderRuntime, error) {
	batches, err := s.repo.ListBatches(order.ID)
	if err != nil {
		return nil, err
	}
	targets, err := s.repo.ListTargetsByOrder(order.ID)
	if err != nil {
		return nil, err
	}
	nsCode, err := changeNamespaceCode(s.db, order.NamespaceID)
	if err != nil {
		return nil, err
	}
	batchNoByID := make(map[uint]int, len(batches))
	for i := range batches {
		batchNoByID[batches[i].ID] = batches[i].BatchNo
	}
	return &orderRuntime{order: order, batches: batches, targets: targets, nsCode: nsCode, batchNoByID: batchNoByID}, nil
}

// advanceOrder 按单状态分派推进：rolling 走完整推进；paused 仅收口在途目标（不下发新目标 / 新批 / 不推进批）。
func (s *DeliveryOrchestrator) advanceOrder(rt *orderRuntime) {
	switch rt.order.Status {
	case model.ChangeOrderStatusRolling:
		s.advanceRolling(rt)
	case model.ChangeOrderStatusPaused:
		// 暂停：已在 pushing / activating 的目标继续走到终态（不制造半截覆盖，spec §4.4.5），但不下发新目标 / 新批。
		s.reconcileInFlightTargets(rt)
		s.refreshAllBatchCounts(rt)
	}
}

// —— 命令下发共享 helper ——

// deliveryUploadPayload 是 delivery_upload 命令载荷（模板源 agent 经 GET upload-manifest 拉清单，此处只带控制信息）。
type deliveryUploadPayload struct {
	OrderID uint `json:"orderId"`
	// 待上传 blob 数（观测用，清单本身经 agent 面拉取）
	MissingCount int `json:"missingCount"`
}

// deliveryPushPayload 是 delivery_push 命令载荷（目标 agent 经 GET manifest 拉完整清单，此处只带摘要，绝不含文件内容）。
type deliveryPushPayload struct {
	OrderID uint `json:"orderId"`
	// 清单文件数
	FileCount int `json:"fileCount"`
	// 清单总字节
	TotalBytes int64 `json:"totalBytes"`
}

// deliveryActivatePayload 是 delivery_activate 命令载荷（生效方式 + restart 超时，spec §4.5.1）。
type deliveryActivatePayload struct {
	OrderID            uint   `json:"orderId"`
	ActivationMethod   string `json:"activationMethod"`
	ActivateTimeoutSec int    `json:"activateTimeoutSec"`
}

// newDeliveryCommand 构造一条待下发交付命令行（Operator=system，命令载荷绝不含文件内容，spec §4.5.1）。
func newDeliveryCommand(nsCode, serverID, cmdType string, payload any) *model.AgentCommand {
	raw, _ := json.Marshal(payload)
	return &model.AgentCommand{
		NamespaceCode: nsCode, ServerID: serverID, Type: cmdType,
		Payload: string(raw), Status: model.CommandStatusPending, Operator: "system",
	}
}

// latestDeliveryCommand 取某目标某类型最新一条命令并校验其 payload orderId 与本单一致（推进器读命令终态用）。
// 冲突守卫保证一台目标同时只属一个活动单，故「最新同类型命令」即本单命令；payload 不符按未下发处理（防御）。
func (s *DeliveryOrchestrator) latestDeliveryCommand(nsCode, serverID, cmdType string, orderID uint) (*model.AgentCommand, error) {
	cmd, err := s.cmdRepo.FindLatestByType(nsCode, serverID, cmdType)
	if err != nil || cmd == nil {
		return nil, err
	}
	var payload deliveryCommandPayload
	_ = json.Unmarshal([]byte(cmd.Payload), &payload) // 解析失败则 OrderID 为零值、与本单不符，按未下发处理
	if payload.OrderID != orderID {
		return nil, nil
	}
	return cmd, nil
}

// deliveryCmdResult 解析命令 result_detail 的回执摘要（推进器读 agent 上报的实际变更 / 备份 / 脱敏原因）。
type deliveryCmdResult struct {
	ChangedFileCount int    `json:"changedFileCount"`
	SkippedFileCount int    `json:"skippedFileCount"`
	BackupPresent    bool   `json:"backupPresent"`
	Error            string `json:"error"`
}

// parseDeliveryCmdResult 解析命令结果摘要（损坏 / 空则返回零值，不阻断推进）。
func parseDeliveryCmdResult(raw string) deliveryCmdResult {
	var r deliveryCmdResult
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &r)
	}
	return r
}

// notifyAgent 事务提交后唤醒目标 agent 拉取命令（未注入 notifier 则留待 agent 重连拉取或超时）。
func (s *DeliveryOrchestrator) notifyAgent(nsCode, serverID string) {
	if s.notifier != nil {
		s.notifier.NotifyCommand(nsCode, serverID)
	}
}

// writeOrchestratorAudit 在事务内写一条编排审计（detail 必含 orderId；绝不含文件内容 / 配置明文，spec §4.8.2）。
func (s *DeliveryOrchestrator) writeOrchestratorAudit(tx *gorm.DB, nsCode, operator, clientIP, action string,
	orderID uint, detail map[string]any) error {
	return writeChangeOrderAudit(tx, s.audit, nsCode, operator, clientIP, action, orderID, detail)
}

// detailView 组装单详情视图（复用 M1 装配口径：单 + items + 批次 + 计数）。
func (s *DeliveryOrchestrator) detailView(orderID uint) (*ChangeOrderDetailView, error) {
	order, err := requireChangeOrder(s.repo, orderID)
	if err != nil {
		return nil, err
	}
	items, err := s.repo.ListItems(order.ID)
	if err != nil {
		return nil, err
	}
	batches, err := s.repo.ListBatches(order.ID)
	if err != nil {
		return nil, err
	}
	targetCounts, err := s.repo.CountTargetsByStatus(order.ID)
	if err != nil {
		return nil, err
	}
	rollbackCounts, err := s.repo.CountTargetsByRollbackStatus(order.ID)
	if err != nil {
		return nil, err
	}
	return &ChangeOrderDetailView{
		ChangeOrderSummaryView: changeOrderSummaryView(order),
		Selector:               decodeSelector(order.Selector),
		Items:                  changeOrderItemViews(items),
		Batches:                changeBatchViews(batches),
		TargetCounts:           targetCounts,
		RollbackCounts:         rollbackCounts,
	}, nil
}

// isTargetTerminal 判目标主状态是否终态（activated / failed / skipped）。
func isTargetTerminal(status string) bool {
	switch status {
	case model.ChangeTargetStatusActivated, model.ChangeTargetStatusFailed, model.ChangeTargetStatusSkipped:
		return true
	}
	return false
}

// activeBatch 取单当前活动批（running / observing / awaiting_confirm 三态之一，批次序号最小者）；无则 nil。
// 顺序灰度下同时至多一个活动批；pending 批等待、终态批（completed/skipped/failed）跳过。
func activeBatch(batches []model.ChangeBatch) *model.ChangeBatch {
	for i := range batches {
		switch batches[i].Status {
		case model.ChangeBatchStatusRunning, model.ChangeBatchStatusObserving, model.ChangeBatchStatusAwaitingConfirm:
			return &batches[i]
		}
	}
	return nil
}

// targetsInBatch 取某批内的目标（按加载顺序，server_id 升序）。
func targetsInBatch(targets []model.ChangeTarget, batchID uint) []*model.ChangeTarget {
	out := make([]*model.ChangeTarget, 0, len(targets))
	for i := range targets {
		if targets[i].BatchID == batchID {
			out = append(out, &targets[i])
		}
	}
	return out
}

// countByStatus 统计一组目标各主状态的条数。
func countByStatus(targets []*model.ChangeTarget) map[string]int {
	counts := map[string]int{}
	for _, t := range targets {
		counts[t.Status]++
	}
	return counts
}

// allTargetsTerminal 判一批目标是否全部进入终态。
func allTargetsTerminal(targets []*model.ChangeTarget) bool {
	for _, t := range targets {
		if !isTargetTerminal(t.Status) {
			return false
		}
	}
	return len(targets) > 0
}

// breakReason 构造带阈值与实测值的熔断原因文案（spec §4.4.4 要求记触发阈值与实测值）。
func breakReason(kind string, actual, threshold, denom int) string {
	return fmt.Sprintf("%s熔断：实测 %d/%d≈%d%% ≥ 阈值 %d%%", kind, actual, denom, pct(actual, denom), threshold)
}

// pct 计算百分比（denom=0 返回 0，避免除零）。
func pct(num, denom int) int {
	if denom == 0 {
		return 0
	}
	return num * 100 / denom
}
