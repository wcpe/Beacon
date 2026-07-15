package service

import (
	"fmt"
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

// Start 启动变更单灰度（POST .../start，spec §4.1 approved→rolling）：
// 校验 approved + 冲突守卫 + 目标固化 + 批次规划落库 + payload 准备，事务提交后唤醒推进器与模板源 agent。
// reason 为可选二次确认原因（前端二次确认弹窗，服务端不强制）。
func (s *DeliveryOrchestrator) Start(id uint, reason, operator, clientIP string) (*ChangeOrderDetailView, error) {
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

// prepareStart 启动前置计算：固化目标 → 目标集冲突守卫 → 配置作用域冲突守卫 → 批次规划 → payload 准备决策。
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

// resolvePayloadPlan 计算 payload 准备决策：查缺失 blob，无缺则 ready、有缺则备下 delivery_upload 命令（spec §4.4.2）。
func (s *DeliveryOrchestrator) resolvePayloadPlan(order *model.ChangeOrder, plan *startPlan) error {
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
