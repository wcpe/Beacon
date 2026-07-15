package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
)

// 变更单入参默认值（spec §3.1；与 devmock 创建默认一致）。
const (
	defaultChangeObserveWindowSec       = 120
	defaultChangeActivateTimeoutSec     = 300
	defaultChangeFailureRatePercent     = 20
	defaultChangeUnhealthyRatePercent   = 30
	changeOrderTimeBoundSecMax          = 86400
	changeOrderTargetPageDefault        = 50
	changeOrderListPageDefault          = 20
	changeOrderPageSizeMax              = 500
	changeOrderDiffChunkServers         = 100
	changeOrderDefaultBatchSizesEncoded = "[10,30,60]"
)

// deliverySettings 是交付域对运维设置的窄读依赖（审批职责分离开关，由 SettingsService 实现）。
type deliverySettings interface {
	GetBool(key string) bool
}

// ChangeConfigInput 是配置变更项输入（PATCH configChanges 整组替换；from 锚点由服务端按 ADR-0071 计算，
// 客户端携带的 configFromVersionId 不采信）。
type ChangeConfigInput struct {
	ConfigScopeKind   string
	ConfigScopeID     uint
	ConfigToVersionID uint
}

// ChangeOrderInput 是创建 / 编辑变更单的字段集：指针字段 nil = 未提供（创建取默认、编辑保持不变）。
// SourceServerID 指向空串 = 显式清空模板源。
type ChangeOrderInput struct {
	Title                         *string
	Description                   *string
	SourceServerID                *string
	ScanDir                       *string
	Selector                      *ChangeSelector
	BatchMode                     *string
	BatchSizes                    *[]int
	ActivationMethod              *string
	ObserveWindowSec              *int
	ActivateTimeoutSec            *int
	FailureRateThresholdPercent   *int
	UnhealthyRateThresholdPercent *int
	ConfigChanges                 *[]ChangeConfigInput
}

// DeliveryOrderService 编排变更单 M1 生命周期（FR-162，spec §4.1/§4.3/§4.7）：
// 组单 CRUD、selector 校验、提交 / 撤回 / 审批 / 驳回、targets / observe / events 读端点。
// 差异扫描 / 影响预览 / file-diff 归 DeliveryDiffService（职责分离，防上帝类）。
type DeliveryOrderService struct {
	db        *gorm.DB
	repo      *repository.ChangeOrderRepository
	versions  *repository.ConfigLayerVersionRepository
	auditRepo *repository.AuditLogRepository
	settings  deliverySettings
	health    *healthview.Store
}

// NewDeliveryOrderService 构造服务。
func NewDeliveryOrderService(db *gorm.DB, repo *repository.ChangeOrderRepository,
	versions *repository.ConfigLayerVersionRepository, auditRepo *repository.AuditLogRepository,
	settings deliverySettings, health *healthview.Store) *DeliveryOrderService {
	return &DeliveryOrderService{db: db, repo: repo, versions: versions, auditRepo: auditRepo,
		settings: settings, health: health}
}

// changeIllegalState 构造状态机非法迁移错误（409，message 对齐 devmock）。
func changeIllegalState(current, action string) *apperr.Error {
	return apperr.New(http.StatusConflict, "illegal_state", fmt.Sprintf("当前状态 %s 不允许 %s", current, action))
}

// changeInvalidParam 构造带具体原因的入参错误（code 对齐 devmock invalid_param）。
func changeInvalidParam(reason string) *apperr.Error {
	return apperr.New(http.StatusBadRequest, "invalid_param", reason)
}

// Create 创建 draft 变更单（POST /admin/v2/change-orders）。
// configChanges 按契约为 PATCH 专用，创建时忽略（与 devmock 一致）。
func (s *DeliveryOrderService) Create(namespaceID uint, input ChangeOrderInput, operator, clientIP string) (*ChangeOrderDetailView, error) {
	if namespaceID == 0 || input.Title == nil || strings.TrimSpace(*input.Title) == "" {
		return nil, changeInvalidParam("namespaceId / title 必填")
	}
	nsCode, err := s.namespaceCode(namespaceID)
	if err != nil {
		return nil, err
	}
	order := newDraftChangeOrder(namespaceID, operator)
	if err := s.applyOrderInput(order, input); err != nil {
		return nil, err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if e := s.repo.WithTx(tx).Create(order); e != nil {
			return e
		}
		return s.writeAudit(tx, nsCode, operator, clientIP, model.ActionDeliveryOrderCreate, order.ID,
			map[string]any{"orderId": order.ID, "title": order.Title})
	})
	if err != nil {
		return nil, err
	}
	return s.detailView(order)
}

// newDraftChangeOrder 构造带默认值的 draft 单（spec §3.1 默认值）。
func newDraftChangeOrder(namespaceID uint, operator string) *model.ChangeOrder {
	return &model.ChangeOrder{
		NamespaceID:                   namespaceID,
		Status:                        model.ChangeOrderStatusDraft,
		Selector:                      encodeSelector(ChangeSelector{}),
		BatchMode:                     model.BatchModePercent,
		BatchSizes:                    changeOrderDefaultBatchSizesEncoded,
		ActivationMethod:              model.ActivationMethodRestart,
		ObserveWindowSec:              defaultChangeObserveWindowSec,
		ActivateTimeoutSec:            defaultChangeActivateTimeoutSec,
		FailureRateThresholdPercent:   defaultChangeFailureRatePercent,
		UnhealthyRateThresholdPercent: defaultChangeUnhealthyRatePercent,
		PayloadState:                  model.PayloadStatePending,
		CreatedBy:                     operator,
	}
}

// List 变更单分页列表（GET /admin/v2/change-orders）。
func (s *DeliveryOrderService) List(q repository.ChangeOrderListQuery) (*ChangeOrderListView, error) {
	q.Page, q.Size = normalizeChangePage(q.Page, q.Size, changeOrderListPageDefault)
	orders, total, err := s.repo.List(q)
	if err != nil {
		return nil, err
	}
	items := make([]ChangeOrderSummaryView, 0, len(orders))
	for i := range orders {
		items = append(items, changeOrderSummaryView(&orders[i]))
	}
	return &ChangeOrderListView{Items: items, Total: total}, nil
}

// Get 变更单详情（GET /admin/v2/change-orders/{id}）。
func (s *DeliveryOrderService) Get(id uint) (*ChangeOrderDetailView, error) {
	order, err := s.requireOrder(id)
	if err != nil {
		return nil, err
	}
	return s.detailView(order)
}

// Update 编辑变更单（PATCH /admin/v2/change-orders/{id}）：draft 直接编辑；
// approved 编辑触发审批自动作废回 draft（spec §4.1，作废动作入审计 detail）。
func (s *DeliveryOrderService) Update(id uint, input ChangeOrderInput, operator, clientIP string) (*ChangeOrderDetailView, error) {
	order, err := s.requireOrder(id)
	if err != nil {
		return nil, err
	}
	if order.Status != model.ChangeOrderStatusDraft && order.Status != model.ChangeOrderStatusApproved {
		return nil, changeIllegalState(order.Status, "编辑")
	}
	revoked := order.Status == model.ChangeOrderStatusApproved
	if err := s.applyOrderInput(order, input); err != nil {
		return nil, err
	}
	nsCode, err := s.namespaceCode(order.NamespaceID)
	if err != nil {
		return nil, err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		return s.persistOrderUpdate(tx, order, input, revoked, nsCode, operator, clientIP)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(order.ID)
}

// persistOrderUpdate 在事务内落编辑：CAS 写回可编辑列（approved 同步作废回 draft）+ 配置项整组替换 + 审计。
func (s *DeliveryOrderService) persistOrderUpdate(tx *gorm.DB, order *model.ChangeOrder,
	input ChangeOrderInput, revoked bool, nsCode, operator, clientIP string) error {
	updates := editableOrderColumns(order)
	if revoked {
		updates["status"] = model.ChangeOrderStatusDraft
		updates["approved_by"] = ""
		updates["approved_at"] = nil
	}
	ok, err := s.repo.WithTx(tx).UpdateStatusCAS(order.ID,
		[]string{model.ChangeOrderStatusDraft, model.ChangeOrderStatusApproved}, updates)
	if err != nil {
		return err
	}
	if !ok {
		return changeIllegalState(order.Status, "编辑")
	}
	if input.ConfigChanges != nil {
		if e := s.replaceConfigChanges(tx, order, *input.ConfigChanges); e != nil {
			return e
		}
	}
	detail := map[string]any{"orderId": order.ID, "revokedApproval": revoked}
	if input.ConfigChanges != nil {
		detail["configChanges"] = len(*input.ConfigChanges)
	}
	return s.writeAudit(tx, nsCode, operator, clientIP, model.ActionDeliveryOrderUpdate, order.ID, detail)
}

// editableOrderColumns 把编辑后实体的可编辑列铺成 CAS 更新映射（status 由调用方按迁移语义覆盖）。
func editableOrderColumns(order *model.ChangeOrder) map[string]any {
	return map[string]any{
		"title":                            order.Title,
		"description":                      order.Description,
		"source_server_id":                 order.SourceServerID,
		"scan_dir":                         order.ScanDir,
		"selector":                         order.Selector,
		"batch_mode":                       order.BatchMode,
		"batch_sizes":                      order.BatchSizes,
		"activation_method":                order.ActivationMethod,
		"observe_window_sec":               order.ObserveWindowSec,
		"activate_timeout_sec":             order.ActivateTimeoutSec,
		"failure_rate_threshold_percent":   order.FailureRateThresholdPercent,
		"unhealthy_rate_threshold_percent": order.UnhealthyRateThresholdPercent,
		"status":                           order.Status,
	}
}

// Delete 物理删除 draft 单（DELETE /admin/v2/change-orders/{id}，spec §4.1：单 + items 级联 + 审计）。
func (s *DeliveryOrderService) Delete(id uint, operator, clientIP string) error {
	order, err := s.requireOrder(id)
	if err != nil {
		return err
	}
	if order.Status != model.ChangeOrderStatusDraft {
		return changeIllegalState(order.Status, "删除")
	}
	nsCode, err := s.namespaceCode(order.NamespaceID)
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		if e := repoTx.DeleteItemsByOrder(order.ID); e != nil {
			return e
		}
		if e := repoTx.DeleteOrder(order.ID); e != nil {
			return e
		}
		return s.writeAudit(tx, nsCode, operator, clientIP, model.ActionDeliveryOrderDelete, order.ID,
			map[string]any{"orderId": order.ID, "title": order.Title})
	})
}

// Submit 提交审批（POST .../submit，spec §4.1 前置：≥1 变更项、selector 解析 ≥1 目标、模板源合格）。
func (s *DeliveryOrderService) Submit(id uint, operator, clientIP string) (*ChangeOrderDetailView, error) {
	order, err := s.requireOrder(id)
	if err != nil {
		return nil, err
	}
	if order.Status != model.ChangeOrderStatusDraft {
		return nil, changeIllegalState(order.Status, "提交审批")
	}
	if err := s.validateSubmitPreconditions(order); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return s.transition(order, "提交审批", model.ActionDeliveryOrderSubmit,
		[]string{model.ChangeOrderStatusDraft},
		map[string]any{"status": model.ChangeOrderStatusPendingApproval, "submitted_at": now},
		map[string]any{"orderId": order.ID}, operator, clientIP)
}

// validateSubmitPreconditions 校验提交前置：变更项非空、目标非空、含文件项必有模板源、模板源已确认绑定 + 在线 + backend。
func (s *DeliveryOrderService) validateSubmitPreconditions(order *model.ChangeOrder) error {
	items, err := s.repo.ListItems(order.ID)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return apperr.ErrChangeNoItems
	}
	if order.SourceServerID == "" && hasFileDiffItem(items) {
		return apperr.ErrChangeSourceMissing // 文件差异项必须有模板源（纯配置单才允许无源，spec §4.2.1）
	}
	targets, err := resolveChangeTargets(s.db, order.NamespaceID, decodeSelector(order.Selector), order.SourceServerID)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return apperr.ErrChangeNoTarget
	}
	if order.SourceServerID != "" {
		return s.validateSourceEligible(order.NamespaceID, order.SourceServerID)
	}
	return nil
}

// validateSourceEligible 校验模板源合格性（spec §4.2.1：已确认绑定、在线的 backend 子服）。
// 在线口径与健康域一致：健康视图存在且未失联（lost）。
func (s *DeliveryOrderService) validateSourceEligible(namespaceID uint, serverID string) error {
	srv, err := findServerByRef(s.db, namespaceID, serverID)
	if err != nil {
		return err
	}
	if srv == nil || srv.Kind != model.ServerKindBackend {
		return apperr.ErrChangeSourceInvalid
	}
	var bound int64
	if err := s.db.Model(&model.AgentIdentity{}).
		Where("namespace_id = ? AND server_id = ? AND status = ?", namespaceID, serverID, model.AgentIdentityStatusActive).
		Count(&bound).Error; err != nil {
		return err
	}
	view, ok := s.health.Get(namespaceID, serverID)
	if bound == 0 || !ok || containsReason(view.Reasons, healthview.ReasonLost) {
		return apperr.ErrChangeSourceInvalid
	}
	return nil
}

// Withdraw 创建人撤回（POST .../withdraw）：pending_approval / approved → draft，审批记录一并作废。
func (s *DeliveryOrderService) Withdraw(id uint, operator, clientIP string) (*ChangeOrderDetailView, error) {
	order, err := s.requireOrder(id)
	if err != nil {
		return nil, err
	}
	if operator != order.CreatedBy {
		return nil, apperr.ErrChangeNotCreator
	}
	from := []string{model.ChangeOrderStatusPendingApproval, model.ChangeOrderStatusApproved}
	if order.Status != model.ChangeOrderStatusPendingApproval && order.Status != model.ChangeOrderStatusApproved {
		return nil, changeIllegalState(order.Status, "撤回")
	}
	return s.transition(order, "撤回", model.ActionDeliveryOrderWithdraw, from,
		map[string]any{"status": model.ChangeOrderStatusDraft, "approved_by": "", "approved_at": nil},
		map[string]any{"orderId": order.ID, "from": order.Status}, operator, clientIP)
}

// Approve 审批通过（POST .../approve）：审批职责分离默认开启时审批人不得是创建人（spec §4.7）。
func (s *DeliveryOrderService) Approve(id uint, reason, operator, clientIP string) (*ChangeOrderDetailView, error) {
	order, err := s.requireOrder(id)
	if err != nil {
		return nil, err
	}
	if order.Status != model.ChangeOrderStatusPendingApproval {
		return nil, changeIllegalState(order.Status, "审批")
	}
	if s.settings.GetBool(SettingDeliveryApproverSeparationEnabled) && operator == order.CreatedBy {
		return nil, apperr.ErrChangeApproverSeparation
	}
	detail := map[string]any{"orderId": order.ID}
	if strings.TrimSpace(reason) != "" {
		detail["reason"] = reason
	}
	now := time.Now().UTC()
	return s.transition(order, "审批", model.ActionDeliveryOrderApprove,
		[]string{model.ChangeOrderStatusPendingApproval},
		map[string]any{"status": model.ChangeOrderStatusApproved, "approved_by": operator, "approved_at": now},
		detail, operator, clientIP)
}

// Reject 审批驳回（POST .../reject）：原因必填，回 draft 并记录最近驳回原因。
func (s *DeliveryOrderService) Reject(id uint, reason, operator, clientIP string) (*ChangeOrderDetailView, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, apperr.New(http.StatusBadRequest, "missing_reason", "驳回原因必填")
	}
	order, err := s.requireOrder(id)
	if err != nil {
		return nil, err
	}
	if order.Status != model.ChangeOrderStatusPendingApproval {
		return nil, changeIllegalState(order.Status, "驳回")
	}
	return s.transition(order, "驳回", model.ActionDeliveryOrderReject,
		[]string{model.ChangeOrderStatusPendingApproval},
		map[string]any{"status": model.ChangeOrderStatusDraft, "reject_reason": reason},
		map[string]any{"orderId": order.ID, "reason": reason}, operator, clientIP)
}

// transition 在事务内执行一次 CAS 状态迁移 + 专项审计，成功后返回最新详情。
// CAS 未命中说明状态已被并发迁移，按非法迁移 409 返回（不静默覆盖）。
func (s *DeliveryOrderService) transition(order *model.ChangeOrder, action, auditAction string,
	from []string, updates, detail map[string]any, operator, clientIP string) (*ChangeOrderDetailView, error) {
	nsCode, err := s.namespaceCode(order.NamespaceID)
	if err != nil {
		return nil, err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		ok, e := s.repo.WithTx(tx).UpdateStatusCAS(order.ID, from, updates)
		if e != nil {
			return e
		}
		if !ok {
			return changeIllegalState(order.Status, action)
		}
		return s.writeAudit(tx, nsCode, operator, clientIP, auditAction, order.ID, detail)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(order.ID)
}

// Targets 目标分页（GET .../targets）：未启动的单恒为空页（M1 形态，spec §5.1）。
func (s *DeliveryOrderService) Targets(id uint, q repository.ChangeTargetQuery) (*ChangeTargetPageView, error) {
	order, err := s.requireOrder(id)
	if err != nil {
		return nil, err
	}
	q.Page, q.Size = normalizeChangePage(q.Page, q.Size, changeOrderTargetPageDefault)
	targets, total, err := s.repo.ListTargets(order.ID, q)
	if err != nil {
		return nil, err
	}
	batchNoByID, err := s.batchNoIndex(order.ID)
	if err != nil {
		return nil, err
	}
	return &ChangeTargetPageView{Items: changeTargetViews(targets, batchNoByID), Total: total}, nil
}

// Observe 当前批观察窗数据（GET .../observe）：M1 无执行批次，恒返回空形态；M3 接真实观察窗内存序列。
func (s *DeliveryOrderService) Observe(id uint) (*ChangeObserveView, error) {
	if _, err := s.requireOrder(id); err != nil {
		return nil, err
	}
	return &ChangeObserveView{BatchNo: nil, ObserveStartedAt: nil, Targets: []ChangeObserveTargetSeries{}}, nil
}

// Events 进度事件（GET .../events）：M1 由单生命周期字段确定性派生 order_status 事件
// （只反映真实时间戳，不造假数据）；M3 换真实事件流 + SSE。
func (s *DeliveryOrderService) Events(id uint) (*ChangeEventsView, error) {
	order, err := s.requireOrder(id)
	if err != nil {
		return nil, err
	}
	return &ChangeEventsView{Events: deriveChangeOrderEvents(order)}, nil
}

// deriveChangeOrderEvents 按生命周期字段派生事件序列（seq 从 1 递增；参考 devmock seedEvents 派生法）。
// 驳回 / 撤回 / 改单等「回退迁移」无独立时间戳字段：末事件状态与当前状态不符时按 updated_at 补一条对齐。
func deriveChangeOrderEvents(order *model.ChangeOrder) []ChangeOrderEventView {
	events := make([]ChangeOrderEventView, 0, 6)
	add := func(status string, at time.Time) {
		events = append(events, ChangeOrderEventView{
			Seq: len(events) + 1, At: at, Type: "order_status", OrderID: order.ID, Status: status,
		})
	}
	add(model.ChangeOrderStatusDraft, order.CreatedAt)
	if order.SubmittedAt != nil {
		add(model.ChangeOrderStatusPendingApproval, *order.SubmittedAt)
	}
	if order.ApprovedAt != nil {
		add(model.ChangeOrderStatusApproved, *order.ApprovedAt)
	}
	if order.StartedAt != nil {
		add(model.ChangeOrderStatusRolling, *order.StartedAt)
	}
	if order.RollbackAt != nil {
		add(model.ChangeOrderStatusRollingBack, *order.RollbackAt)
	}
	if (order.Status == model.ChangeOrderStatusCompleted || order.Status == model.ChangeOrderStatusRolledBack) &&
		order.FinishedAt != nil {
		add(order.Status, *order.FinishedAt)
	}
	if events[len(events)-1].Status != order.Status {
		add(order.Status, order.UpdatedAt)
	}
	return events
}

// —— 输入应用与校验 ——

// applyOrderInput 把提供的字段应用到实体并校验（创建与编辑共用；nil 字段跳过）。
func (s *DeliveryOrderService) applyOrderInput(order *model.ChangeOrder, input ChangeOrderInput) error {
	if err := applyOrderTextFields(order, input); err != nil {
		return err
	}
	if err := applyOrderStrategyFields(order, input); err != nil {
		return err
	}
	if input.Selector != nil {
		if err := s.validateSelectorNamespace(order.NamespaceID, *input.Selector); err != nil {
			return err
		}
		order.Selector = encodeSelector(*input.Selector)
	}
	if input.SourceServerID != nil && *input.SourceServerID != "" {
		if err := s.validateSourceStructural(order.NamespaceID, *input.SourceServerID); err != nil {
			return err
		}
	}
	return nil
}

// applyOrderTextFields 应用标题 / 说明 / 模板源 / 扫描目录并做字段级校验。
func applyOrderTextFields(order *model.ChangeOrder, input ChangeOrderInput) error {
	if input.Title != nil {
		title := strings.TrimSpace(*input.Title)
		if title == "" || len([]rune(title)) > 128 {
			return changeInvalidParam("标题必填且不超过 128 字")
		}
		order.Title = title
	}
	if input.Description != nil {
		order.Description = *input.Description
	}
	if input.SourceServerID != nil {
		order.SourceServerID = strings.TrimSpace(*input.SourceServerID)
	}
	if input.ScanDir != nil {
		dir := strings.TrimSpace(*input.ScanDir)
		if dir != "" {
			normalized, err := NormalizeFileSyncDirectory(dir)
			if err != nil {
				return changeInvalidParam("扫描目录不合法：须为服务器根内相对目录")
			}
			dir = normalized
		}
		order.ScanDir = dir
	}
	return nil
}

// applyOrderStrategyFields 应用批次 / 生效 / 观察窗 / 熔断阈值策略字段并校验取值范围。
func applyOrderStrategyFields(order *model.ChangeOrder, input ChangeOrderInput) error {
	if input.BatchMode != nil {
		if *input.BatchMode != model.BatchModePercent && *input.BatchMode != model.BatchModeCount {
			return changeInvalidParam("batchMode 仅支持 percent / count")
		}
		order.BatchMode = *input.BatchMode
	}
	if input.ActivationMethod != nil {
		if !isValidActivationMethod(*input.ActivationMethod) {
			return changeInvalidParam("activationMethod 仅支持 restart / hot_reload / push_only")
		}
		order.ActivationMethod = *input.ActivationMethod
	}
	if input.BatchSizes != nil {
		if err := validateBatchSizes(order.BatchMode, *input.BatchSizes); err != nil {
			return err
		}
		order.BatchSizes = encodeBatchSizes(*input.BatchSizes)
	}
	return applyOrderNumericFields(order, input)
}

// applyOrderNumericFields 应用观察窗 / 生效超时 / 两条熔断阈值并校验范围（0 = 关闭对应熔断）。
func applyOrderNumericFields(order *model.ChangeOrder, input ChangeOrderInput) error {
	if input.ObserveWindowSec != nil {
		if *input.ObserveWindowSec < 1 || *input.ObserveWindowSec > changeOrderTimeBoundSecMax {
			return changeInvalidParam("observeWindowSec 须在 1~86400 之间")
		}
		order.ObserveWindowSec = *input.ObserveWindowSec
	}
	if input.ActivateTimeoutSec != nil {
		if *input.ActivateTimeoutSec < 1 || *input.ActivateTimeoutSec > changeOrderTimeBoundSecMax {
			return changeInvalidParam("activateTimeoutSec 须在 1~86400 之间")
		}
		order.ActivateTimeoutSec = *input.ActivateTimeoutSec
	}
	if input.FailureRateThresholdPercent != nil {
		if *input.FailureRateThresholdPercent < 0 || *input.FailureRateThresholdPercent > 100 {
			return changeInvalidParam("failureRateThresholdPercent 须在 0~100 之间（0=关闭）")
		}
		order.FailureRateThresholdPercent = *input.FailureRateThresholdPercent
	}
	if input.UnhealthyRateThresholdPercent != nil {
		if *input.UnhealthyRateThresholdPercent < 0 || *input.UnhealthyRateThresholdPercent > 100 {
			return changeInvalidParam("unhealthyRateThresholdPercent 须在 0~100 之间（0=关闭）")
		}
		order.UnhealthyRateThresholdPercent = *input.UnhealthyRateThresholdPercent
	}
	return nil
}

// isValidActivationMethod 校验生效方式取值。
func isValidActivationMethod(method string) bool {
	switch method {
	case model.ActivationMethodRestart, model.ActivationMethodHotReload, model.ActivationMethodPushOnly:
		return true
	default:
		return false
	}
}

// validateBatchSizes 校验批次规划：非空、逐批 ≥1，percent 模式逐批 ≤100（spec §4.4.1）。
func validateBatchSizes(mode string, sizes []int) error {
	if len(sizes) == 0 {
		return changeInvalidParam("batchSizes 不能为空")
	}
	for _, size := range sizes {
		if size < 1 || (mode == model.BatchModePercent && size > 100) {
			return changeInvalidParam("batchSizes 逐批须 ≥1（percent 模式且 ≤100）")
		}
	}
	return nil
}

// validateSelectorNamespace 编辑期即校验 selector 引用实体归属（引用异 namespace 实体直接失败，FR-162）。
func (s *DeliveryOrderService) validateSelectorNamespace(namespaceID uint, selector ChangeSelector) error {
	topo, err := loadDeliveryTopology(s.db, namespaceID)
	if err != nil {
		return err
	}
	_, err = validateSelectorRefs(topo, selector)
	return err
}

// validateSourceStructural 组单期模板源结构校验：存在于本 namespace 且为 backend（在线与绑定在提交时校验）。
func (s *DeliveryOrderService) validateSourceStructural(namespaceID uint, serverID string) error {
	srv, err := findServerByRef(s.db, namespaceID, serverID)
	if err != nil {
		return err
	}
	if srv == nil || srv.Kind != model.ServerKindBackend {
		return apperr.ErrChangeSourceInvalid
	}
	return nil
}

// —— 配置变更项（整组替换 + from 锚点，ADR-0071）——

// replaceConfigChanges 在事务内整组替换 config_change 项：逐项校验 to_version 与作用域归属，
// from 锚点按 ADR-0071 三分支计算；file_diff 项不受影响。审计由调用方统一落（detail 含 configChanges 数）。
func (s *DeliveryOrderService) replaceConfigChanges(tx *gorm.DB, order *model.ChangeOrder,
	changes []ChangeConfigInput) error {
	seen := map[string]struct{}{}
	rows := make([]model.ChangeOrderItem, 0, len(changes))
	for _, change := range changes {
		key := change.ConfigScopeKind + "/" + fmt.Sprint(change.ConfigScopeID)
		if _, dup := seen[key]; dup {
			return changeInvalidParam("同一作用域在一单内只能出现一次：" + key)
		}
		seen[key] = struct{}{}
		item, err := s.buildConfigChangeItem(tx, order, change)
		if err != nil {
			return err
		}
		rows = append(rows, *item)
	}
	repoTx := s.repo.WithTx(tx)
	if err := repoTx.DeleteItemsByKind(order.ID, model.ChangeItemKindConfigChange); err != nil {
		return err
	}
	return repoTx.CreateItems(rows)
}

// buildConfigChangeItem 校验单个配置变更输入并构造变更项（含 from 锚点计算）。
func (s *DeliveryOrderService) buildConfigChangeItem(tx *gorm.DB, order *model.ChangeOrder,
	change ChangeConfigInput) (*model.ChangeOrderItem, error) {
	toVersion, err := s.validateConfigChangeTarget(tx, order, change)
	if err != nil {
		return nil, err
	}
	fromID, err := s.resolveConfigFromAnchor(tx, toVersion, change)
	if err != nil {
		return nil, err
	}
	kind := change.ConfigScopeKind
	scopeID := change.ConfigScopeID
	toID := change.ConfigToVersionID
	return &model.ChangeOrderItem{
		OrderID: order.ID, Kind: model.ChangeItemKindConfigChange,
		ConfigScopeKind: &kind, ConfigScopeID: &scopeID,
		ConfigFromVersionID: fromID, ConfigToVersionID: &toID,
	}, nil
}

// validateConfigChangeTarget 校验目标版本存在、scope 匹配、文件未删且与作用域同属本单 namespace。
func (s *DeliveryOrderService) validateConfigChangeTarget(tx *gorm.DB, order *model.ChangeOrder,
	change ChangeConfigInput) (*model.ConfigLayerVersion, error) {
	if !model.IsValidConfigScopeLevel(change.ConfigScopeKind) || change.ConfigScopeID == 0 || change.ConfigToVersionID == 0 {
		return nil, apperr.ErrChangeConfigVersionInvalid
	}
	toVersion, err := s.versions.WithTx(tx).FindByID(change.ConfigToVersionID)
	if err != nil {
		return nil, err
	}
	if toVersion == nil || toVersion.ScopeLevel != change.ConfigScopeKind || toVersion.ScopeRefID != change.ConfigScopeID {
		return nil, apperr.ErrChangeConfigVersionInvalid
	}
	var file model.ConfigFile
	if e := tx.First(&file, toVersion.ConfigFileID).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrChangeConfigVersionInvalid
		}
		return nil, e
	}
	if file.DeletedAt != nil || file.NamespaceID != order.NamespaceID {
		return nil, apperr.ErrChangeConfigVersionInvalid
	}
	ownerNS, err := scopeNamespaceID(tx, change.ConfigScopeKind, change.ConfigScopeID)
	if err != nil {
		return nil, err
	}
	if ownerNS != order.NamespaceID {
		return nil, apperr.ErrChangeConfigVersionInvalid
	}
	return toVersion, nil
}

// resolveConfigFromAnchor 计算 from 锚点（ADR-0071 三分支）：
// ① 该 (config_file, scope) 最近一次已 completed 单交付的 to 版本；② 无则取 to 版本的 based_on；③ 链首版为 nil。
func (s *DeliveryOrderService) resolveConfigFromAnchor(tx *gorm.DB, toVersion *model.ConfigLayerVersion,
	change ChangeConfigInput) (*uint, error) {
	delivered, err := s.repo.WithTx(tx).FindLatestDeliveredToVersionID(
		toVersion.ConfigFileID, change.ConfigScopeKind, change.ConfigScopeID)
	if err != nil {
		return nil, err
	}
	if delivered != nil {
		return delivered, nil
	}
	return toVersion.BasedOnVersionID, nil
}

// —— 共享 helper（本域两个 service 共用的包级函数）——

// requireChangeOrder 取变更单，不存在返回 change_order_not_found。
func requireChangeOrder(repo *repository.ChangeOrderRepository, id uint) (*model.ChangeOrder, error) {
	order, err := repo.FindByID(id)
	if err != nil {
		return nil, err
	}
	if order == nil {
		return nil, apperr.ErrChangeOrderNotFound
	}
	return order, nil
}

// requireOrder 取变更单（服务内薄封装）。
func (s *DeliveryOrderService) requireOrder(id uint) (*model.ChangeOrder, error) {
	return requireChangeOrder(s.repo, id)
}

// detailView 组装详情视图（单 + items + 批次 + 目标计数）。
func (s *DeliveryOrderService) detailView(order *model.ChangeOrder) (*ChangeOrderDetailView, error) {
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

// batchNoIndex 建 batch_id → batch_no 映射（目标视图换算用）。
func (s *DeliveryOrderService) batchNoIndex(orderID uint) (map[uint]int, error) {
	batches, err := s.repo.ListBatches(orderID)
	if err != nil {
		return nil, err
	}
	index := make(map[uint]int, len(batches))
	for _, batch := range batches {
		index[batch.ID] = batch.BatchNo
	}
	return index, nil
}

// changeNamespaceCode 解析 namespace code（审计 NamespaceCode 用）；不存在按参数错误返回。
func changeNamespaceCode(db *gorm.DB, namespaceID uint) (string, error) {
	var ns model.Namespace
	err := db.First(&ns, namespaceID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", changeInvalidParam("namespace 不存在")
	}
	if err != nil {
		return "", err
	}
	return ns.Code, nil
}

// namespaceCode 解析 namespace code（服务内薄封装）。
func (s *DeliveryOrderService) namespaceCode(namespaceID uint) (string, error) {
	return changeNamespaceCode(s.db, namespaceID)
}

// writeChangeOrderAudit 在事务内写一条变更单专项审计（detail 必含 orderId；
// 绝不落文件内容 / 配置明文 / blob 数据，spec §4.8.2）。
func writeChangeOrderAudit(tx *gorm.DB, auditRepo *repository.AuditLogRepository,
	nsCode, operator, clientIP, action string, orderID uint, detail map[string]any) error {
	raw, _ := json.Marshal(detail)
	return auditRepo.WithTx(tx).Create(&model.AuditLog{
		NamespaceCode: nsCode, Operator: operator, Action: action,
		TargetType: model.TargetTypeChangeOrder, TargetRef: fmt.Sprintf("%d", orderID),
		Detail: string(raw), Result: model.ResultOK, ClientIP: clientIP,
	})
}

// writeAudit 在事务内写审计（服务内薄封装）。
func (s *DeliveryOrderService) writeAudit(tx *gorm.DB, nsCode, operator, clientIP, action string,
	orderID uint, detail map[string]any) error {
	return writeChangeOrderAudit(tx, s.auditRepo, nsCode, operator, clientIP, action, orderID, detail)
}

// normalizeChangePage 归一分页参数：页码 ≥1、页大小取默认并封顶（防异常大页拖垮查询）。
func normalizeChangePage(page, size, defaultSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = defaultSize
	}
	if size > changeOrderPageSizeMax {
		size = changeOrderPageSizeMax
	}
	return page, size
}
