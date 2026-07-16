package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// DeliveryAdminHandler 处理交付编排 V2 变更单管理面端点（FR-162/166/171，spec §5.1）：
// 组单 CRUD / 差异扫描 / 影响预览 / 审批链路 / 启动 / 暂停 / 继续 / 终止 / 批次推进门 / targets / observe / events / file-diff。
// 薄 handler——只解析请求、调 service、经 render 统一写出；响应视图逐字对齐 contracts delivery.ts。
// M5 的整单回滚（rollback / rollback/finish）端点本版不建路由。
type DeliveryAdminHandler struct {
	orders *service.DeliveryOrderService
	diff   *service.DeliveryDiffService
	orch   *service.DeliveryOrchestrator
}

// NewDeliveryAdminHandler 构造处理器（orch 为 M3 灰度编排推进器，承载 start / pause / resume / cancel / confirm 与 SSE）。
func NewDeliveryAdminHandler(orders *service.DeliveryOrderService, diff *service.DeliveryDiffService,
	orch *service.DeliveryOrchestrator) *DeliveryAdminHandler {
	return &DeliveryAdminHandler{orders: orders, diff: diff, orch: orch}
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
	reason := decodeReason(r)
	if err := h.orders.Delete(id, reason, auth.Operator(r.Context()), clientIP(r)); err != nil {
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

// Start 处理 POST /admin/v2/change-orders/{id}/start：启动灰度（二次确认原因可选，冲突守卫 + 目标固化 + payload 准备）。
func (h *DeliveryAdminHandler) Start(w http.ResponseWriter, r *http.Request) {
	reason := decodeReason(r)
	h.lifecycle(w, r, func(id uint, operator, ip string) (*service.ChangeOrderDetailView, error) {
		return h.orch.Start(id, reason, operator, ip)
	})
}

// Pause 处理 POST /admin/v2/change-orders/{id}/pause：人工暂停（不打断在途目标）。
func (h *DeliveryAdminHandler) Pause(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, func(id uint, operator, ip string) (*service.ChangeOrderDetailView, error) {
		return h.orch.Pause(id, operator, ip)
	})
}

// resumeBody 是继续请求体（mode / reason；熔断与准备失败场景由 service 校验必填）。
type resumeBody struct {
	Mode   string `json:"mode"`
	Reason string `json:"reason"`
}

// Resume 处理 POST /admin/v2/change-orders/{id}/resume：继续暂停单（熔断 / 准备失败需 mode / reason）。
func (h *DeliveryAdminHandler) Resume(w http.ResponseWriter, r *http.Request) {
	var body resumeBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	h.lifecycle(w, r, func(id uint, operator, ip string) (*service.ChangeOrderDetailView, error) {
		return h.orch.Resume(id, body.Mode, body.Reason, operator, ip)
	})
}

// Cancel 处理 POST /admin/v2/change-orders/{id}/cancel：紧急终止（原因必填 + 二次确认）。
func (h *DeliveryAdminHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	reason := decodeReason(r)
	h.lifecycle(w, r, func(id uint, operator, ip string) (*service.ChangeOrderDetailView, error) {
		return h.orch.Cancel(id, reason, operator, ip)
	})
}

// Rollback 处理 POST /admin/v2/change-orders/{id}/rollback：整单回滚（原因必填 + 高摩擦确认，FR-167）。
func (h *DeliveryAdminHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	reason := decodeReason(r)
	h.lifecycle(w, r, func(id uint, operator, ip string) (*service.ChangeOrderDetailView, error) {
		return h.orch.Rollback(id, reason, operator, ip)
	})
}

// RollbackFinish 处理 POST /admin/v2/change-orders/{id}/rollback/finish：结束回滚（残留失败目标时人工收单，FR-167）。
func (h *DeliveryAdminHandler) RollbackFinish(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, func(id uint, operator, ip string) (*service.ChangeOrderDetailView, error) {
		return h.orch.FinishRollback(id, operator, ip)
	})
}

// ConfirmBatch 处理 POST /admin/v2/change-orders/{id}/batches/{batchNo}/confirm：推进门放行（末批确认即完成整单）。
func (h *DeliveryAdminHandler) ConfirmBatch(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	batchNo, ok := parseUintParam(w, r, "batchNo")
	if !ok {
		return
	}
	view, err := h.orch.ConfirmBatch(id, int(batchNo), auth.Operator(r.Context()), clientIP(r))
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

// Events 处理 GET /admin/v2/change-orders/{id}/events：进度事件。
// 按 Accept 内容协商：text/event-stream → 真 SSE 实时推流（推进器事件 Hub）；否则回 JSON 派生快照（断线回退轮询形态）。
// SSE 分支先校验单存在（设 SSE 头前返 404 JSON），再补发派生快照 + 流式实时事件直到断连。
func (h *DeliveryAdminHandler) Events(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	if h.orch != nil && wantsEventStream(r) {
		h.streamEvents(w, r, id)
		return
	}
	view, err := h.orders.Events(id)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, view)
}

// streamEvents 以 SSE 推送某单实时进度：先校验存在与流式支持，再交推进器补发快照 + 流实时事件。
func (h *DeliveryAdminHandler) streamEvents(w http.ResponseWriter, r *http.Request, id uint) {
	if _, err := h.orders.Get(id); err != nil { // 设 SSE 头前校验存在，404 走 JSON 错误出口
		render.WriteError(w, r, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		render.WriteError(w, r, apperr.ErrStreamingUnsupported)
		return
	}
	writeDeliverySSEHeaders(w)
	flusher.Flush()
	_ = h.orch.StreamEvents(r.Context(), id, &deliverySSESink{w: w, flusher: flusher})
}

// wantsEventStream 判客户端是否请求 SSE（Accept 含 text/event-stream）。
func wantsEventStream(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/event-stream")
}

// writeDeliverySSEHeaders 写 SSE 响应头（禁缓存 / keep-alive / 关代理缓冲）。
func writeDeliverySSEHeaders(w http.ResponseWriter) {
	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream; charset=utf-8")
	hd.Set("Cache-Control", "no-cache")
	hd.Set("Connection", "keep-alive")
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

// deliverySSESink 是交付进度 SSE 输出汇（写事件帧与保活注释）。
type deliverySSESink struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// Send 写一条 SSE 事件帧（event: 类型 / data: JSON）。
func (s *deliverySSESink) Send(evt service.ChangeOrderEventView) error {
	raw, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", evt.Type, raw); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// Ping 写一条 SSE 保活注释（穿透代理缓冲、保持连接）。
func (s *deliverySSESink) Ping() error {
	if _, err := fmt.Fprint(s.w, ": keepalive\n\n"); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
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
