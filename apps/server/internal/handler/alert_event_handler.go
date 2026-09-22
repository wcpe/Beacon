package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// AlertEventHandler 处理告警事件历史查询与处理工作流（FR-89 只读留痕，FR-157 确认 / 标记已处理，见 ADR-0041/ADR-0064）。
type AlertEventHandler struct {
	svc   *service.AlertEventService
	scope *service.ObservationScopeResolver
}

// SetObservationScopeResolver 装配统一观测范围解析器。
func (h *AlertEventHandler) SetObservationScopeResolver(resolver *service.ObservationScopeResolver) {
	h.scope = resolver
}

// SetHealthQuery 装配健康查询服务，供详情聚合内嵌「该服近期状态」（FR-230）。
func (h *AlertEventHandler) SetHealthQuery(q *service.HealthQueryService) {
	h.svc.SetHealthQuery(q)
}

// NewAlertEventHandler 构造处理器。
func NewAlertEventHandler(svc *service.AlertEventService) *AlertEventHandler {
	return &AlertEventHandler{svc: svc}
}

// alertEventView 是告警事件对外视图（小驼峰，逐字匹配 packages/contracts AlertEventItem）。
// handledBy / handledAt / handleNote 未处理时序列化为 null（用指针），status 恒非空。
type alertEventView struct {
	ID         uint       `json:"id"`
	Type       string     `json:"type"`
	Level      string     `json:"level"`
	ServerID   string     `json:"serverId"`
	Namespace  string     `json:"namespace"`
	Message    string     `json:"message"`
	Detail     string     `json:"detail"`
	CreatedAt  time.Time  `json:"createdAt"`
	Status     string     `json:"status"`
	HandledBy  *string    `json:"handledBy"`
	HandledAt  *time.Time `json:"handledAt"`
	HandleNote *string    `json:"handleNote"`
	// 人工分级覆盖（FR-231）：非空表示已手动调整；overriddenBy/At 记录改级人与时刻。
	SeverityOverride *string    `json:"severityOverride"`
	OverriddenBy     *string    `json:"overriddenBy"`
	OverriddenAt     *time.Time `json:"overriddenAt"`
	// 收敛（FR-232）：同键未恢复期间重复触发只递增 occurrenceCount，lastAt 记最近一次触发时刻。
	// 二者此前定义了契约与 DB 列却漏在视图层，导致前端收敛徽标「×N / 最后」永远拿不到数据。
	OccurrenceCount int        `json:"occurrenceCount"`
	LastAt          *time.Time `json:"lastAt"`
}

// toAlertEventView 把模型转对外视图；空串的处理人 / 说明映射为 null（契约为 string | null）。
func toAlertEventView(e model.AlertEvent) alertEventView {
	return alertEventView{
		ID: e.ID, Type: e.Type, Level: e.Level, ServerID: e.ServerID,
		Namespace: e.Namespace, Message: e.Message, Detail: e.Detail, CreatedAt: e.CreatedAt,
		Status:           e.Status,
		HandledBy:        ptrIfNotEmpty(e.HandledBy),
		HandledAt:        e.HandledAt,
		HandleNote:       ptrIfNotEmpty(e.HandleNote),
		SeverityOverride: ptrIfNotEmpty(e.SeverityOverride),
		OverriddenBy:     ptrIfNotEmpty(e.OverriddenBy),
		OverriddenAt:     e.OverriddenAt,
		OccurrenceCount:  e.OccurrenceCount,
		LastAt:           e.LastAt,
	}
}

// ptrIfNotEmpty 空串返回 nil（→ json null），非空返回指针（→ json 字符串），对齐契约 string | null。
func ptrIfNotEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// List 处理 GET /admin/v1/alert-events（分页 + 过滤，时间倒序）。
func (h *AlertEventHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope, err := resolveObservationScope(r, h.scope)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("size"))
	items, total, err := h.svc.List(repository.AlertEventFilter{
		Type:           q.Get("type"),
		Level:          q.Get("level"),
		Status:         q.Get("status"),
		Namespace:      q.Get("namespace"),
		NamespaceCodes: scope.NamespaceCodes, Scoped: !scope.All,
		From: parseRFC3339(q.Get("from")),
		To:   parseRFC3339(q.Get("to")),
		Page: page,
		Size: size,
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]alertEventView, 0, len(items))
	for _, e := range items {
		views = append(views, toAlertEventView(e))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"total": total, "items": views})
}

// handleAlertRequest 是处理告警的请求体（FR-157，见 ADR-0064）。
// 兼容两种字段措辞：前端契约 HandleAlertBody 用 status(acknowledged/resolved) + note；
// ADR-0064 措辞用 action(acknowledge/resolve) + handleNote。二者归一后交 service。
type handleAlertRequest struct {
	Status     string `json:"status"`
	Action     string `json:"action"`
	Note       string `json:"note"`
	HandleNote string `json:"handleNote"`
}

// action 取归一后的处理动作：优先 status（前端契约），回退 action（ADR 措辞）。
func (req handleAlertRequest) action() string {
	if req.Status != "" {
		return req.Status
	}
	return req.Action
}

// note 取归一后的处置说明：优先 note（前端契约），回退 handleNote（ADR 措辞）。
func (req handleAlertRequest) note() string {
	if req.Note != "" {
		return req.Note
	}
	return req.HandleNote
}

// Handle 处理 POST /admin/v1/alert-events/{id}/handle（FR-157，见 ADR-0064）：确认 / 标记已处理。
// 走 adminAuth → readonlyWriteGuard → auditWrite 链（写方法，readonly 403）；service 内在事务中更新状态 + 写专项审计。
func (h *AlertEventHandler) Handle(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	var req handleAlertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	updated, err := h.svc.Handle(id, req.action(), req.note(), auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, toAlertEventView(*updated))
}

// handleAlertBatchRequest 是批量处理请求体（FR-229）：按筛选条件处理整个结果集，而非仅当前页勾选行。
// 观测范围由服务端解析器决定（客户端不传时即全量，越界不由客户端指定）；批量仅作用于「未处理（open）」条目，
// 故筛选维不含 status（目标状态由顶层 status 决定）。
type handleAlertBatchRequest struct {
	Status string `json:"status"`
	Action string `json:"action"`
	Note   string `json:"note"`
	Filter struct {
		Type  string `json:"type"`
		Level string `json:"level"`
		From  string `json:"from"`
		To    string `json:"to"`
	} `json:"filter"`
}

// HandleBatch 处理 POST /admin/v1/alert-events/handle（FR-229）：按筛选条件跨页批量确认 / 标记已处理。
// 走 adminAuth → readonlyWriteGuard → auditWrite 链（写方法，readonly 403）；service 在事务内一条 UPDATE + 一条批量审计。
func (h *AlertEventHandler) HandleBatch(w http.ResponseWriter, r *http.Request) {
	var req handleAlertBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	action := req.Status
	if action == "" {
		action = req.Action
	}
	scope, err := resolveObservationScope(r, h.scope)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	affected, err := h.svc.HandleBatch(repository.AlertEventFilter{
		Type:           req.Filter.Type,
		Level:          req.Filter.Level,
		NamespaceCodes: scope.NamespaceCodes, Scoped: !scope.All,
		From: parseRFC3339(req.Filter.From),
		To:   parseRFC3339(req.Filter.To),
	}, action, req.Note, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"affected": affected})
}

// alertLevelOverrideRequest 是人工改级请求体（FR-231）。
type alertLevelOverrideRequest struct {
	Level string `json:"level"`
}

// OverrideLevel 处理 POST /admin/v1/alert-events/{id}/level（FR-231）：人工升降级别 + 落审计。
func (h *AlertEventHandler) OverrideLevel(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	var req alertLevelOverrideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	updated, err := h.svc.OverrideAlertLevel(id, req.Level, auth.Operator(r.Context()), clientIP(r))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, toAlertEventView(*updated))
}

// alertContextServerView 是该服近期状态视图（json 形状对齐 contracts AlertContext.server）。
type alertContextServerView struct {
	ServerID    string   `json:"serverId"`
	Online      bool     `json:"online"`
	Level       string   `json:"level"`
	Score       int      `json:"score"`
	Schedulable bool     `json:"schedulable"`
	Reasons     []string `json:"reasons"`
	SampledAtMs int64    `json:"sampledAtMs"`
}

// Context 处理 GET /admin/v1/alert-events/{id}/context（FR-230）：内嵌该服近期状态 + 该服告警时间线。
// 只读端点（观测范围循 FR-213）；服务器已归档 / 无 serverId 时 server 为 null，时间线按默认退化，不报错。
func (h *AlertEventHandler) Context(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	scope, err := resolveObservationScope(r, h.scope)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	res, err := h.svc.Context(id, scope)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	var server *alertContextServerView
	if res.Server != nil {
		server = &alertContextServerView{
			ServerID: res.Server.ServerID, Online: res.Server.Online, Level: res.Server.Level,
			Score: res.Server.Score, Schedulable: res.Server.Schedulable,
			Reasons: res.Server.Reasons, SampledAtMs: res.Server.SampledAtMs,
		}
	}
	timeline := make([]alertEventView, 0, len(res.Timeline))
	for _, e := range res.Timeline {
		timeline = append(timeline, toAlertEventView(e))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"server":              server,
		"timeline":            timeline,
		"timelineLimit":       res.TimelineLimit,
		"timelineWindowHours": res.TimelineWindowHours,
	})
}
