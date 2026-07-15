package handler

import (
	"encoding/json"
	"net/http"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// DeliveryAdminHandler 处理交付编排 V2 变更单管理面端点（FR-162，spec §5.1，M1：
// 组单 CRUD / 差异扫描 / 影响预览 / 审批链路 / targets / observe / events / file-diff）。
// 薄 handler——只解析请求、调 service、经 render 统一写出；响应视图逐字对齐 contracts delivery.ts。
// M3+ 的 start / pause / resume / cancel / batches confirm / rollback 端点本切片不建路由。
type DeliveryAdminHandler struct {
	orders *service.DeliveryOrderService
	diff   *service.DeliveryDiffService
}

// NewDeliveryAdminHandler 构造处理器。
func NewDeliveryAdminHandler(orders *service.DeliveryOrderService, diff *service.DeliveryDiffService) *DeliveryAdminHandler {
	return &DeliveryAdminHandler{orders: orders, diff: diff}
}

// optionalString 区分 JSON 字段「缺省」与「显式 null / 值」：缺省时 set=false；显式 null 视同空串（清空语义）。
type optionalString struct {
	set   bool
	value string
}

// UnmarshalJSON 实现显式 null → 空串的解码语义。
func (o *optionalString) UnmarshalJSON(b []byte) error {
	o.set = true
	if string(b) == "null" {
		o.value = ""
		return nil
	}
	return json.Unmarshal(b, &o.value)
}

// changeOrderRequest 是创建 / 编辑变更单请求体（对齐 apps/web ChangeOrderInput，全部字段可选）。
type changeOrderRequest struct {
	NamespaceID                   uint                       `json:"namespaceId"`
	Title                         *string                    `json:"title"`
	Description                   *string                    `json:"description"`
	SourceServerID                optionalString             `json:"sourceServerId"`
	ScanDir                       *string                    `json:"scanDir"`
	Selector                      *service.ChangeSelector    `json:"selector"`
	BatchMode                     *string                    `json:"batchMode"`
	BatchSizes                    *[]int                     `json:"batchSizes"`
	ActivationMethod              *string                    `json:"activationMethod"`
	ObserveWindowSec              *int                       `json:"observeWindowSec"`
	ActivateTimeoutSec            *int                       `json:"activateTimeoutSec"`
	FailureRateThresholdPercent   *int                       `json:"failureRateThresholdPercent"`
	UnhealthyRateThresholdPercent *int                       `json:"unhealthyRateThresholdPercent"`
	ConfigChanges                 *[]changeConfigChangeInput `json:"configChanges"`
}

// changeConfigChangeInput 是配置变更项输入（对齐 contracts ConfigChangeInput；
// configFromVersionId 由服务端按 ADR-0071 计算，客户端携带值不采信、此处不解码）。
type changeConfigChangeInput struct {
	ConfigScopeKind   string `json:"configScopeKind"`
	ConfigScopeID     uint   `json:"configScopeId"`
	ConfigToVersionID uint   `json:"configToVersionId"`
}

// toServiceInput 把请求体映射为 service 入参。
func (req *changeOrderRequest) toServiceInput() service.ChangeOrderInput {
	input := service.ChangeOrderInput{
		Title: req.Title, Description: req.Description, ScanDir: req.ScanDir,
		Selector: req.Selector, BatchMode: req.BatchMode, BatchSizes: req.BatchSizes,
		ActivationMethod: req.ActivationMethod, ObserveWindowSec: req.ObserveWindowSec,
		ActivateTimeoutSec: req.ActivateTimeoutSec, FailureRateThresholdPercent: req.FailureRateThresholdPercent,
		UnhealthyRateThresholdPercent: req.UnhealthyRateThresholdPercent,
	}
	if req.SourceServerID.set {
		v := req.SourceServerID.value
		input.SourceServerID = &v
	}
	if req.ConfigChanges != nil {
		changes := make([]service.ChangeConfigInput, 0, len(*req.ConfigChanges))
		for _, c := range *req.ConfigChanges {
			changes = append(changes, service.ChangeConfigInput{
				ConfigScopeKind: c.ConfigScopeKind, ConfigScopeID: c.ConfigScopeID,
				ConfigToVersionID: c.ConfigToVersionID,
			})
		}
		input.ConfigChanges = &changes
	}
	return input
}

// Create 处理 POST /admin/v2/change-orders：创建 draft 单，201 返回详情。
func (h *DeliveryAdminHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req changeOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := h.orders.Create(req.NamespaceID, req.toServiceInput(), auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, view)
}

// List 处理 GET /admin/v2/change-orders：status / namespaceId / createdBy / keyword 过滤 + 分页。
func (h *DeliveryAdminHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	view, err := h.orders.List(repository.ChangeOrderListQuery{
		NamespaceID: uint(intQuery(q.Get("namespaceId"))),
		Status:      q.Get("status"),
		CreatedBy:   q.Get("createdBy"),
		Keyword:     q.Get("keyword"),
		Page:        intQuery(q.Get("page")),
		Size:        intQuery(q.Get("pageSize")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// Get 处理 GET /admin/v2/change-orders/{id}：详情（单 + items + 批次概要 + 计数）。
func (h *DeliveryAdminHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	view, err := h.orders.Get(id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// Patch 处理 PATCH /admin/v2/change-orders/{id}：编辑（draft；approved 编辑自动作废审批回 draft）。
func (h *DeliveryAdminHandler) Patch(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	var req changeOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	view, err := h.orders.Update(id, req.toServiceInput(), auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// Delete 处理 DELETE /admin/v2/change-orders/{id}：物理删除 draft 单（单 + items 级联 + 审计），204。
func (h *DeliveryAdminHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.orders.Delete(id, auth.Operator(r.Context()), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusNoContent, nil)
}

// DiffScan 处理 POST /admin/v2/change-orders/{id}/diff-scan：同步读最新文件资产快照重算差异并返回 items。
func (h *DeliveryAdminHandler) DiffScan(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	view, err := h.diff.DiffScan(id, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// Impact 处理 GET /admin/v2/change-orders/{id}/impact：影响预览（汇总 + 逐目标分页）。
func (h *DeliveryAdminHandler) Impact(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	q := r.URL.Query()
	view, err := h.diff.Impact(id, intQuery(q.Get("page")), intQuery(q.Get("pageSize")))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// reasonBody 是审批 / 驳回请求体（approve 原因可选、reject 原因必填，由 service 校验）。
type reasonBody struct {
	Reason string `json:"reason"`
}

// decodeReason 解析可选 reason 请求体（空 body 合法）。
func decodeReason(r *http.Request) string {
	var body reasonBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	return body.Reason
}

// Submit 处理 POST /admin/v2/change-orders/{id}/submit：draft → pending_approval（前置校验见 spec §4.1）。
func (h *DeliveryAdminHandler) Submit(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, func(id uint, operator, ip string) (*service.ChangeOrderDetailView, error) {
		return h.orders.Submit(id, operator, ip)
	})
}

// Withdraw 处理 POST /admin/v2/change-orders/{id}/withdraw：创建人撤回回 draft（审批一并作废）。
func (h *DeliveryAdminHandler) Withdraw(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, func(id uint, operator, ip string) (*service.ChangeOrderDetailView, error) {
		return h.orders.Withdraw(id, operator, ip)
	})
}

// Approve 处理 POST /admin/v2/change-orders/{id}/approve：审批通过（审批人 ≠ 创建人默认强制）。
func (h *DeliveryAdminHandler) Approve(w http.ResponseWriter, r *http.Request) {
	reason := decodeReason(r)
	h.lifecycle(w, r, func(id uint, operator, ip string) (*service.ChangeOrderDetailView, error) {
		return h.orders.Approve(id, reason, operator, ip)
	})
}

// Reject 处理 POST /admin/v2/change-orders/{id}/reject：驳回（原因必填）回 draft。
func (h *DeliveryAdminHandler) Reject(w http.ResponseWriter, r *http.Request) {
	reason := decodeReason(r)
	h.lifecycle(w, r, func(id uint, operator, ip string) (*service.ChangeOrderDetailView, error) {
		return h.orders.Reject(id, reason, operator, ip)
	})
}

// lifecycle 统一生命周期端点骨架：解析 id → 执行迁移 → 200 返回最新详情。
func (h *DeliveryAdminHandler) lifecycle(w http.ResponseWriter, r *http.Request,
	run func(id uint, operator, ip string) (*service.ChangeOrderDetailView, error)) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	view, err := run(id, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// Targets 处理 GET /admin/v2/change-orders/{id}/targets：目标分页（batch / status / serverId 过滤；未启动为空页）。
func (h *DeliveryAdminHandler) Targets(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	q := r.URL.Query()
	view, err := h.orders.Targets(id, repository.ChangeTargetQuery{
		BatchNo:  intQuery(q.Get("batch")),
		Status:   q.Get("status"),
		ServerID: q.Get("serverId"),
		Page:     intQuery(q.Get("page")),
		Size:     intQuery(q.Get("pageSize")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// Observe 处理 GET /admin/v2/change-orders/{id}/observe：当前批观察窗数据（M1 恒空形态）。
func (h *DeliveryAdminHandler) Observe(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	view, err := h.orders.Observe(id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// Events 处理 GET /admin/v2/change-orders/{id}/events：进度事件（M1 生命周期字段确定性派生，轮询形态）。
func (h *DeliveryAdminHandler) Events(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	view, err := h.orders.Events(id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// FileDiff 处理 GET /admin/v2/change-orders/{id}/items/{itemId}/file-diff：
// 变更项文件内容预览（?serverId 可选目标、?reason 敏感路径放行原因）。GET 带写副作用
// （下发 asset-read 命令 + 查看审计），路由挂 requireFullRole 挡 readonly。
func (h *DeliveryAdminHandler) FileDiff(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	itemID, ok := parseUintParam(w, r, "itemId")
	if !ok {
		return
	}
	q := r.URL.Query()
	view, err := h.diff.FileDiff(r.Context(), id, itemID, q.Get("serverId"), q.Get("reason"),
		auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}
