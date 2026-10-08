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
	// restartHealthWarmup 是 restart 生效目标的重启预热宽限：activated（心跳回归）后此段时间内，
	// 服务器仍在冷启动、健康评分尚未从关服断供（lost，score=0/unhealthy）或样本不足的低分恢复，
	// 该目标被排除出健康恶化熔断评估——避免把「重启固有的短暂不健康」误判为「生效导致的健康恶化」。
	// 取 90s 覆盖健康计算轮恢复节律（5s 一轮 + 60s 聚合窗 + 余量），且短于默认观察窗 120s，
	// 预热后仍留窗口评估真实健康恶化。仅 restart 适用——push_only/hot_reload 不重启服务器，无冷启动预热期。
	restartHealthWarmup = 90 * time.Second
)

// deliveryActiveOrderStatuses 是推进器每轮装载并推进的单状态集（spec §4.1 恢复语义）：
// rolling 全量推进；paused 仅收口在途目标（不下发新目标 / 新批）；rolling_back 一次性全量推进回滚（FR-167）。
var deliveryActiveOrderStatuses = []string{
	model.ChangeOrderStatusRolling, model.ChangeOrderStatusPaused, model.ChangeOrderStatusRollingBack,
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
	mu *sync.Mutex
	// observeMu 独立保护观察窗内存缓冲（推进器采样写、Observe/SSE 读），与 mu 有序嵌套（mu→observeMu，不反向）。
	observeMu      *sync.RWMutex
	observeByOrder map[uint]*observeState
	// stallMu 独立保护停滞观测表（FR-262 / P0 回归）。**刻意不复用 mu**：
	// clearObserve 有两条调用方——Cancel / applyConfirmBatch 已持 mu 同步调用，
	// 而审批执行适配器的 afterCommit 闭包由审批 worker 在事务提交后执行、**全程不持 mu**；
	// Go 互斥锁不可重入，若这里复用 mu，前一条路径会直接死锁。
	// 独立锁下两条路径都安全；锁序为 mu → stallMu（与 mu → observeMu 同向，不反向嵌套）。
	stallMu *sync.Mutex
	// stallByOrder 记录各单「推进停滞」的观测状态（推进器每轮检查，stallMu 保护，FR-262）。
	// 停滞 = 单在装载集里、但推进器已无事可做且无人来推：确认门等人确认、或根本没有活动批。
	// 这两类都不会自行恢复（推进器只会重复空转），此前完全静默——运维只能靠「感觉单卡住了」去翻库。
	stallByOrder map[uint]*deliveryStallState
	approval     *ApprovalService
	// config 配置版本回退能力（整单回滚记账用，ConfigCenterService 实现；未装配则跳过 config 回退，测试兼容）
	config configRollbacker
	// cfgVers 配置版本仓库（回滚 from==nil 项撤销贡献时反查 configFileID）
	cfgVers *repository.ConfigLayerVersionRepository
}

// SetApprovalService 注入统一审批申请服务；未装配时危险继续操作失败关闭。
func (s *DeliveryOrchestrator) SetApprovalService(approval *ApprovalService) { s.approval = approval }

// configRollbacker 交付域对配置版本回退的窄依赖（整单回滚记账用，由 ConfigCenterService 实现）：
// from!=nil 项回退到 from 版本、from==nil 项撤销该作用域贡献，使 config-center head 与磁盘还原对齐（ADR-0071 决策6）。
type configRollbacker interface {
	RollbackVersion(versionID uint, remark, operator, clientIP string) (*ConfigSaveResultView, error)
	RemoveScopeContribution(fileID uint, scopeLevel string, scopeRefID uint, reason, operator, clientIP string) (*ConfigRevokeResultView, error)
}

// SetConfigRollbacker 注入配置版本回退能力与版本仓库（整单回滚装配时调用，FR-167）：未注入则回滚跳过 config 记账。
func (s *DeliveryOrchestrator) SetConfigRollbacker(config configRollbacker, cfgVers *repository.ConfigLayerVersionRepository) {
	s.config = config
	s.cfgVers = cfgVers
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
		mu:             &sync.Mutex{},
		observeMu:      &sync.RWMutex{},
		observeByOrder: map[uint]*observeState{},
		stallMu:        &sync.Mutex{},
		stallByOrder:   map[uint]*deliveryStallState{},
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
	// FR-261：活动单引用的 blob 每轮刷新一次引用时间——保留期清理以 last_referenced_at 为准，
	// 只在「模板源上传回执成功」那一刻刷新的话，长跑单（观察窗久、暂停后继续）超期即被误删，
	// 目标侧表现为下载 404 且无告警。刷新是廉价的按 sha 批量 UPDATE，重复调用无副作用。
	orderIDs := make([]uint, 0, len(orders))
	for i := range orders {
		orderIDs = append(orderIDs, orders[i].ID)
	}
	if e := s.blobs.TouchReferencesForOrders(orderIDs); e != nil {
		slog.Error("交付编排刷新活动单 blob 引用失败", "错误", e)
	}
	// 目标级（子集）回滚不改单主状态，故这些单不在上面的活动单集合里，需单独扫描推进（FR-270）。
	s.advanceTargetRollbacks()
}

// detectStall 检测某单是否**停滞**（推进器已无事可做、且不会自行恢复），命中即按节律告警（FR-262）。
//
// 为什么必须有这层：推进器每 2s 空转一轮，单卡在「等人工确认」或「根本没有活动批」时，
// 没有任何日志、告警或事件——运维只看到「单还 rolling 但不动了」，只能去翻库猜原因。
// 停滞检测把「不动」这件事本身变成可观测信号，并指明卡在哪一类等待上。
//
// 两类停滞：
//   - confirm_gate：批已到 awaiting_confirm，等人工确认才放量下一批（正常等待，超时才提醒）；
//   - no_active_batch：rolling 单连一个活动批都没有（异常，通常是批被并发迁走或数据不一致）。
func (s *DeliveryOrchestrator) detectStall(rt *orderRuntime) {
	kind := stalledKindOf(rt)
	if kind == "" {
		s.clearStall(rt.order.ID) // 本轮有事可做 → 计时清零，下次重新起算
		return
	}
	now := s.now()
	// 整段「读表 → 判定 → 写表」在 stallMu 下完成：clearStall 可能来自不持 mu 的 afterCommit，
	// 若只在写时加锁而读在外面，仍与并发 delete 构成竞态。
	s.stallMu.Lock()
	defer s.stallMu.Unlock()
	st := s.stallByOrder[rt.order.ID]
	if st == nil || st.kind != kind {
		st = &deliveryStallState{kind: kind, since: now}
		s.stallByOrder[rt.order.ID] = st
	}
	first, every := stallRemindRhythm(kind)
	elapsed := now.Sub(st.since)
	if elapsed < first {
		return
	}
	if !st.remindedAt.IsZero() && now.Sub(st.remindedAt) < every {
		return
	}
	st.remindedAt = now
	batchNo := 0
	if b := activeBatch(rt.batches); b != nil {
		batchNo = b.BatchNo
	}
	if kind == deliveryStallKindConfirmGate {
		slog.Warn("交付编排：批次已到推进门等待人工确认，确认前不会放量下一批",
			"orderId", rt.order.ID, "batchNo", batchNo, "已等待", elapsed.Round(time.Second).String())
		return
	}
	slog.Warn("交付编排：活动单没有活动批，推进器无批可推（批可能被并发迁走或数据不一致，请人工核对）",
		"orderId", rt.order.ID, "status", rt.order.Status, "已停滞", elapsed.Round(time.Second).String())
}

// stalledKindOf 判定某单本轮是否停滞，返回停滞类型；空串表示推进正常、不需告警。
// 只对 rolling 单判「无活动批」——paused / rolling_back 不下发新批是设计使然，不是停滞。
func stalledKindOf(rt *orderRuntime) string {
	if b := activeBatch(rt.batches); b != nil {
		if b.Status == model.ChangeBatchStatusAwaitingConfirm {
			return deliveryStallKindConfirmGate
		}
		return ""
	}
	if rt.order.Status == model.ChangeOrderStatusRolling && rt.order.PayloadState == model.PayloadStateReady {
		return deliveryStallKindNoActiveBatch
	}
	return ""
}

// stallRemindRhythm 返回某类停滞的（首提醒点, 重复间隔）。
func stallRemindRhythm(kind string) (time.Duration, time.Duration) {
	if kind == deliveryStallKindConfirmGate {
		return deliveryConfirmGateRemindAfter, deliveryConfirmGateRemindEvery
	}
	return deliveryNoActiveBatchRemindAfter, deliveryNoActiveBatchRemindEvery
}

// clearStall 清除某单的停滞观测（单终态化 / 恢复推进时调用，防止内存随单无界增长）。
//
// 必须在 stallMu 下操作：本函数的调用方之一是不持 mu 的审批 afterCommit 闭包
// （见 clearObserve），而推进器在 mu 下经 detectStall 读写同一张表——无锁即并发读写。
func (s *DeliveryOrchestrator) clearStall(orderID uint) {
	s.stallMu.Lock()
	defer s.stallMu.Unlock()
	delete(s.stallByOrder, orderID)
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

// restartHealthWarmup 见上；此段补停滞检测的节律常量（FR-262）。
const (
	// deliveryConfirmGateRemindAfter 确认门开启后多久开始提醒（首提醒点）。
	// 取 5 分钟：短于「审批人读完一屏影响面 + 决策」的正常耗时，不打扰正常流程。
	deliveryConfirmGateRemindAfter = 5 * time.Minute
	// deliveryConfirmGateRemindEvery 此后每隔多久重复提醒一次（避免刷屏，也避免只报一次后被淹没）。
	deliveryConfirmGateRemindEvery = 30 * time.Minute
	// deliveryNoActiveBatchRemindAfter rolling 单「无活动批」持续多久开始告警。
	// 取 2 分钟：正常批切换（确认末批 → 启动次批）在秒级完成，超过即异常。
	deliveryNoActiveBatchRemindAfter = 2 * time.Minute
	// deliveryNoActiveBatchRemindEvery 无活动批告警的重复间隔。
	deliveryNoActiveBatchRemindEvery = 10 * time.Minute
)

// deliveryStallState 是某单的停滞观测状态（FR-262）：记录「已停滞多久、提醒过几次」，
// 使提醒可去重（不每 tick 刷屏）且可恢复（单重新推进即清零）。
type deliveryStallState struct {
	// kind 停滞类型（confirm_gate / no_active_batch），类型切换即重置计时。
	kind string
	// since 首次观测到该类型停滞的时刻
	since time.Time
	// remindedAt 上次提醒时刻（零值 = 从未提醒）
	remindedAt time.Time
}

// deliveryStallKindConfirmGate 停滞类型：确认门等待人工确认。
const deliveryStallKindConfirmGate = "confirm_gate"

// deliveryStallKindNoActiveBatch 停滞类型：rolling 单无活动批（推进器无批可推）。
const deliveryStallKindNoActiveBatch = "no_active_batch"

// advanceOrder 按单状态分派推进：rolling 走完整推进；paused 仅收口在途目标（不下发新目标 / 新批 / 不推进批）。
func (s *DeliveryOrchestrator) advanceOrder(rt *orderRuntime) {
	switch rt.order.Status {
	case model.ChangeOrderStatusRolling:
		s.advanceRolling(rt)
	case model.ChangeOrderStatusPaused:
		// 暂停：已在 pushing / activating 的目标继续走到终态（不制造半截覆盖，spec §4.4.5），但不下发新目标 / 新批。
		s.reconcileInFlightTargets(rt)
		s.refreshAllBatchCounts(rt)
	case model.ChangeOrderStatusRollingBack:
		s.advanceRollingBack(rt)
	}
	// 每轮末尾统一做停滞检测：本单本轮是否有事可做、是否已在等人。
	s.detectStall(rt)
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
