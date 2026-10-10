package service

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// deliveryStartConflictStatuses 是启动冲突守卫认定的「活动单」状态集（ADR-0071 §4.1）：
// 目标集与这些单的目标集相交即拒绝启动（同一目标服同时只允许被一个活动单覆盖）。
var deliveryStartConflictStatuses = []string{
	model.ChangeOrderStatusRolling, model.ChangeOrderStatusPaused, model.ChangeOrderStatusRollingBack,
}

// changeStartConflict 构造带冲突目标清单的启动冲突错误（脱敏无需——serverId 是运维定位上下文非凭据）。
func changeStartConflict(servers []string) *apperr.Error {
	return apperr.New(http.StatusConflict, "start_conflict",
		fmt.Sprintf("目标集与其他进行中的变更单冲突，冲突目标：%s", strings.Join(servers, ", ")))
}

// Start 禁止旧公开启动入口，防止批准后由调用方绕过统一审批 worker 直接启动交付。
func (s *DeliveryOrchestrator) Start(_ uint, _, _, _ string) (*ChangeOrderDetailView, error) {
	return nil, apperr.ErrForbidden
}

// applyStart 仅供同包测试与已批准的领域适配器内部复用，不能作为管理面写入口。
func (s *DeliveryOrchestrator) applyStart(id uint, reason, operator, clientIP string) (*ChangeOrderDetailView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	order, err := requireChangeOrder(s.repo, id)
	if err != nil {
		return nil, err
	}
	if order.Status != model.ChangeOrderStatusApproved {
		return nil, changeIllegalState(order.Status, "启动")
	}
	plan, err := s.prepareStart(order)
	if err != nil {
		return nil, err
	}
	if err := s.persistStart(order, plan, reason, operator, clientIP); err != nil {
		return nil, err
	}
	// 提交成功后：唤醒推进器推进首批 / payload 准备；有上传命令则唤醒模板源 agent。
	s.wake()
	if plan.uploadCommand != nil {
		s.notifyAgent(plan.nsCode, order.SourceServerID)
	}
	return s.detailView(order.ID)
}

// applyStartApprovedInTx 在统一审批 worker 的事务内持久化已批准变更单的启动。
//
// **全程不取 s.mu**（P0 死锁修复，2026-10-10 02:41:05 生产事故）：
// 本函数运行在审批 worker 的外层事务里，该事务已从连接池取走连接（approval_worker.go:151）。
// 若在此请求 s.mu，就是「持连接等 mu」；而 s.mu 的持有者可能在等连接（推进器每轮 `advanceActiveOrders`
// 先取 mu 再查库）。池上限若小到没有第三条连接可破环（事故现场是 `max-open-conns: 1`，
// 后已按 §2 默认提到 4），两条路径永久互等，busy_timeout 也无效——它等的是 SQLite 文件锁，
// 而这里等的是 database/sql 连接池排队，根本没进 SQLite 层。标准库的等待其实尊重 ctx（db.conn(ctx) 的
// select ctx.Done()），但本项目主力写法 db.Transaction(...) 不传 ctx（gorm 默认
// context.Background() 永不取消），故表现为无限等待。
// 复现见 delivery_deadlock_repro_test.go，本半的不变量锁定见 delivery_deadlock_approval_lock_test.go。
//
// 为什么去掉锁不削弱互斥（互斥由另外两层承担，不是「少了一把锁」）：
//   - **状态迁移由 CAS 定序**：persistStart 内的 `UpdateStatusCAS(approved→rolling)` 未命中即拒。
//     控制操作（Cancel / Pause / 确认批…）对同一张单做的也是带 from 状态集的条件更新，
//     故并发下不会出现两次迁移同时成立或半成品落库（迁移与批次 / 目标 / 审计在同一个外层事务里提交）。
//   - **审批执行在进程内串行**：生产只有一个 worker goroutine（cmd/beacon/main.go 的
//     `go runApprovalWorker`），RunOnce 是 claim→execute 串行循环，不存在两个审批启动并发。
//   - 本函数不读写 s.mu 保护的任何内存：`observeByOrder`（observeMu）与 `stallByOrder`（stallMu）
//     在启动路径零引用，只被推进器与控制操作使用。
//
// 口径与本包其余「审批 worker 在事务内执行」的变体一致——`applyResumeInTx` / `applyRollbackInTx` /
// `applyRollbackTargetsInTx` / `persistConfirmInTx` / `applyFinishRollbackInTx` 都不取 mu，
// 一律以「外层事务 + 状态 CAS」定序；本函数此前是全包唯一的例外。
func (s *DeliveryOrchestrator) applyStartApprovedInTx(tx *gorm.DB, id uint, reason, operator, clientIP string) (func(), error) {
	if tx == nil {
		return nil, apperr.ErrForbidden
	}
	transactional := *s
	transactional.db = tx
	transactional.repo = s.repo.WithTx(tx)
	transactional.cmdRepo = s.cmdRepo.WithTx(tx)
	transactional.blobs = s.blobs.withTx(tx)
	// 能力守卫同样绑到本事务：审批适配器在事务内启动变更单，守卫若另开连接会与外层事务互等（FR-264）。
	transactional.capability = s.capability.withTx(tx)
	order, err := requireChangeOrder(transactional.repo, id)
	if err != nil {
		return nil, err
	}
	if order.Status != model.ChangeOrderStatusApproved {
		return nil, changeIllegalState(order.Status, "启动")
	}
	plan, err := transactional.prepareStart(order)
	if err != nil {
		return nil, err
	}
	if err := transactional.persistStart(order, plan, reason, operator, clientIP); err != nil {
		return nil, err
	}
	return func() {
		s.wake()
		if plan.uploadCommand != nil {
			s.notifyAgent(plan.nsCode, order.SourceServerID)
		}
	}, nil
}

// startPlan 是启动前置计算的产物（目标固化 + 批次规划 + payload 准备决策），供 persistStart 一次性落库。
type startPlan struct {
	// 按 serverId 字典序排序的目标 serverId（selector 固化结果）
	serverIDs []string
	// 逐批成员 serverId 切片（planBatchCounts 切分，稳定可复现）
	batchMembers [][]string
	// payload 是否已全部就绪（无缺失 blob → 首批可立即启动）
	payloadReady bool
	// 待下发的 delivery_upload 命令（payload 未就绪时向模板源下发；就绪则为 nil）
	uploadCommand *model.AgentCommand
	// namespace code（命令行与审计用）
	nsCode string
}

// prepareStart 启动前置计算：固化目标 → 目标集冲突守卫 → 配置作用域冲突守卫 → **agent 能力版本守卫**
// → 批次规划 → payload 准备决策。
func (s *DeliveryOrchestrator) prepareStart(order *model.ChangeOrder) (*startPlan, error) {
	targets, err := resolveChangeTargets(s.db, order.NamespaceID, decodeSelector(order.Selector), order.SourceServerID)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, apperr.ErrChangeNoTarget
	}
	serverIDs := make([]string, 0, len(targets))
	for i := range targets {
		serverIDs = append(serverIDs, targets[i].ServerID)
	}
	if err := s.guardStartConflict(order, serverIDs); err != nil {
		return nil, err
	}
	if err := s.guardConfigConflict(order); err != nil {
		return nil, err
	}
	// FR-264：能力守卫在批次规划之前——目标集里有不支持流式交付的旧 agent 时整单拒绝启动，
	// 一条命令都不建（不做「跳过部分目标继续」的部分成功：启动是运维显式动作，拒了要说得清是谁）。
	// 模板源同样要校验：payload 未就绪时上传命令下发给源，源是旧 agent 则整单永远推不动。
	if err := s.guardAgentCapability(order.NamespaceID, serverIDs, order.SourceServerID); err != nil {
		return nil, err
	}
	plan := &startPlan{
		serverIDs:    serverIDs,
		batchMembers: planBatchMembers(order.BatchMode, decodeBatchSizes(order.BatchSizes), serverIDs),
		nsCode:       "",
	}
	nsCode, err := changeNamespaceCode(s.db, order.NamespaceID)
	if err != nil {
		return nil, err
	}
	plan.nsCode = nsCode
	if err := s.resolvePayloadPlan(order, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// resolvePayloadPlan 计算 payload 准备决策：先刷新本单 blob 引用（清理保护）→ 由控制面渲染写入配置项灰度 blob，
// 再查文件项缺失 blob，无缺则 ready、有缺则备下 delivery_upload 命令（spec §4.4.2 / ADR-0071）。
func (s *DeliveryOrchestrator) resolvePayloadPlan(order *model.ChangeOrder, plan *startPlan) error {
	// FR-261：启动即刷新本单引用 blob 的 last_referenced_at——approved 后的准备期上传窗口可能很长
	// （等模板源上传），保留期清理不得在此期间把本单尚在消费的 blob 判为超期删掉。
	if err := s.blobs.TouchReferences(order.ID); err != nil {
		slog.Error("交付编排启动刷新 blob 引用失败", "orderId", order.ID, "错误", err)
	}
	// 配置项载荷由控制面在准备期按目标渲染灰度生效明文并写入内容寻址 blob（区别于文件项的模板源上传中转）：
	// 供目标随文件清单下载落盘、restart 读盘生效。渲染失败即启动失败（脱敏后展示给运维，ADR-0057）。
	if err := s.blobs.PrepareConfigBlobs(order.ID, plan.serverIDs); err != nil {
		return err
	}
	missing, err := s.blobs.MissingBlobs(order.ID)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		plan.payloadReady = true
		return nil
	}
	if order.SourceServerID == "" {
		// 有缺失 blob 却无模板源无法上传（理论上纯配置单已被前置拒，防御性兜底）。
		return apperr.ErrChangeSourceMissing
	}
	plan.uploadCommand = newDeliveryCommand(plan.nsCode, order.SourceServerID,
		model.CommandTypeDeliveryUpload, deliveryUploadPayload{OrderID: order.ID, MissingCount: len(missing)})
	return nil
}

// guardAgentCapability agent 交付能力版本守卫（FR-264，落实 ADR-0069 L58）：
// 目标集 + 模板源里任一 agent 不具备流式交付能力即整单拒绝启动，并把不合格 serverId 与原因带在错误里。
// 未装配守卫 / 最低版本设置为空 → 不校验（不因守卫自身的装配缺失阻断交付）。
func (s *DeliveryOrchestrator) guardAgentCapability(namespaceID uint, serverIDs []string, sourceServerID string) error {
	if s.capability == nil {
		return nil
	}
	probe := make([]string, 0, len(serverIDs)+1)
	probe = append(probe, serverIDs...)
	if sourceServerID != "" {
		probe = append(probe, sourceServerID)
	}
	unsupported, err := s.capability.filterUnsupported(namespaceID, probe)
	if err != nil {
		return err
	}
	if len(unsupported) == 0 {
		return nil
	}
	return deliveryCapabilityUnsupported(unsupported, s.capability.minVersion())
}

// guardStartConflict 目标集冲突守卫（ADR-0071 §4.1）：目标集与其他活动单目标集相交即拒绝。
func (s *DeliveryOrchestrator) guardStartConflict(order *model.ChangeOrder, serverIDs []string) error {
	busy, err := s.repo.ListActiveTargetServerIDs(order.NamespaceID, order.ID, deliveryStartConflictStatuses)
	if err != nil {
		return err
	}
	if len(busy) == 0 {
		return nil
	}
	busySet := make(map[string]struct{}, len(busy))
	for _, sid := range busy {
		busySet[sid] = struct{}{}
	}
	conflicts := make([]string, 0)
	for _, sid := range serverIDs {
		if _, hit := busySet[sid]; hit {
			conflicts = append(conflicts, sid)
		}
	}
	if len(conflicts) > 0 {
		return changeStartConflict(conflicts)
	}
	return nil
}

// guardConfigConflict 配置作用域冲突守卫（ADR-0071 决策5）：本单 config_change 的 (config_file, scope)
// 与其他活动单（rolling / paused / rolling_back）的 config_change 重叠即拒绝——防两单并发灰度同一配置
// 作用域时经 head 互相泄漏未定稿的灰度值。纯文件单无 config_change 项，本守卫对其为空操作。
func (s *DeliveryOrchestrator) guardConfigConflict(order *model.ChangeOrder) error {
	mine, err := s.repo.ListConfigScopeKeysForOrder(order.ID)
	if err != nil {
		return err
	}
	if len(mine) == 0 {
		return nil
	}
	busy, err := s.repo.ListActiveConfigScopeKeys(order.NamespaceID, order.ID, deliveryStartConflictStatuses)
	if err != nil {
		return err
	}
	if len(busy) == 0 {
		return nil
	}
	busySet := make(map[repository.ConfigScopeKey]struct{}, len(busy))
	for _, key := range busy {
		busySet[key] = struct{}{}
	}
	conflicts := make([]repository.ConfigScopeKey, 0)
	for _, key := range mine {
		if _, hit := busySet[key]; hit {
			conflicts = append(conflicts, key)
		}
	}
	if len(conflicts) > 0 {
		return changeConfigScopeConflict(conflicts)
	}
	return nil
}

// changeConfigScopeConflict 构造带冲突 (文件, 作用域) 清单的配置作用域冲突错误
// （文件 id / 作用域是运维定位上下文非凭据，无需脱敏）。
func changeConfigScopeConflict(keys []repository.ConfigScopeKey) *apperr.Error {
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("文件 %d 作用域 %s/%d", key.ConfigFileID, key.ScopeKind, key.ScopeID))
	}
	return apperr.New(http.StatusConflict, "config_scope_conflict",
		fmt.Sprintf("配置作用域与其他进行中的变更单冲突：%s", strings.Join(parts, "，")))
}

// persistStart 在事务内落启动：CAS approved→rolling + 批次 / 目标固化落库 + payload 状态 + 首批就绪则置 running + 审计。
func (s *DeliveryOrchestrator) persistStart(order *model.ChangeOrder, plan *startPlan, reason, operator, clientIP string) error {
	now := s.now()
	payloadState := model.PayloadStateUploading
	if plan.payloadReady {
		payloadState = model.PayloadStateReady
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		ok, err := repoTx.UpdateStatusCAS(order.ID, []string{model.ChangeOrderStatusApproved},
			map[string]any{"status": model.ChangeOrderStatusRolling, "started_at": now, "payload_state": payloadState})
		if err != nil {
			return err
		}
		if !ok {
			return changeIllegalState(order.Status, "启动")
		}
		if err := persistBatchesAndTargets(repoTx, order.ID, plan, now); err != nil {
			return err
		}
		if plan.uploadCommand != nil {
			if e := s.cmdRepo.WithTx(tx).Create(plan.uploadCommand); e != nil {
				return e
			}
		}
		detail := map[string]any{"orderId": order.ID, "targetCount": len(plan.serverIDs),
			"batchCount": len(plan.batchMembers), "payloadState": payloadState}
		if strings.TrimSpace(reason) != "" {
			detail["reason"] = reason
		}
		return s.writeOrchestratorAudit(tx, plan.nsCode, operator, clientIP,
			model.ActionDeliveryOrderStart, order.ID, detail)
	})
}

// persistBatchesAndTargets 事务内固化批次与目标：建批次取回 id → 建目标绑批 → payload 就绪则首批 pending→running。
func persistBatchesAndTargets(repoTx *repository.ChangeOrderRepository, orderID uint, plan *startPlan, now time.Time) error {
	batches := make([]model.ChangeBatch, 0, len(plan.batchMembers))
	for i, members := range plan.batchMembers {
		status := model.ChangeBatchStatusPending
		var startedAt *time.Time
		if i == 0 && plan.payloadReady {
			status = model.ChangeBatchStatusRunning
			startedAt = &now
		}
		batches = append(batches, model.ChangeBatch{
			OrderID: orderID, BatchNo: i + 1, Status: status,
			PlannedCount: len(members), StartedAt: startedAt,
		})
	}
	if err := repoTx.CreateBatches(batches); err != nil {
		return err
	}
	targets := make([]model.ChangeTarget, 0, len(plan.serverIDs))
	for i, members := range plan.batchMembers {
		for _, serverID := range members {
			targets = append(targets, model.ChangeTarget{
				OrderID: orderID, BatchID: batches[i].ID, ServerID: serverID,
				Status: model.ChangeTargetStatusPending,
			})
		}
	}
	return repoTx.CreateTargets(targets)
}

// planBatchMembers 按批次规划把字典序目标切成逐批成员（planBatchCounts 定切分，稳定可复现，spec §4.4.1）。
func planBatchMembers(mode string, sizes []int, serverIDs []string) [][]string {
	counts := planBatchCounts(mode, sizes, len(serverIDs))
	members := make([][]string, 0, len(counts))
	idx := 0
	for _, count := range counts {
		members = append(members, serverIDs[idx:idx+count])
		idx += count
	}
	return members
}

// planBatchCounts 批次切分核心（spec §4.4.1，穷举单测覆盖）：percent 逐批向上取整、count 逐批固定台数，
// 均不超过剩余；百分比之和不足 100 或末批有余则补一个「剩余」末批。同输入必同输出。
func planBatchCounts(mode string, sizes []int, total int) []int {
	counts := make([]int, 0, len(sizes)+1)
	remaining := total
	for _, size := range sizes {
		if remaining <= 0 {
			break
		}
		raw := size
		if mode == model.BatchModePercent {
			raw = (total*size + 99) / 100
		}
		count := min(raw, remaining)
		if count <= 0 {
			continue
		}
		remaining -= count
		counts = append(counts, count)
	}
	if remaining > 0 {
		counts = append(counts, remaining)
	}
	return counts
}
