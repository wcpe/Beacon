package handler

import (
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/render"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/service"
)

// AuditHandler 处理审计查询。
type AuditHandler struct {
	svc *service.AuditService
}

// NewAuditHandler 构造处理器。
func NewAuditHandler(svc *service.AuditService) *AuditHandler {
	return &AuditHandler{svc: svc}
}

// auditView 是审计对外视图。
type auditView struct {
	ID         uint      `json:"id"`
	Namespace  string    `json:"namespace"`
	Operator   string    `json:"operator"`
	Action     string    `json:"action"`
	TargetType string    `json:"targetType"`
	TargetRef  string    `json:"targetRef"`
	Detail     string    `json:"detail"`
	Result     string    `json:"result"`
	ClientIP   string    `json:"clientIp"`
	CreatedAt  time.Time `json:"createdAt"`
}

// List 处理 GET /admin/v1/audits（分页 + 过滤，时间倒序）。
func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("size"))
	items, total, err := h.svc.List(repository.AuditFilter{
		Namespace:     q.Get("namespace"),
		Operator:      q.Get("operator"),
		Action:        q.Get("action"),
		TargetType:    q.Get("targetType"),
		TargetRef:     q.Get("targetRef"),
		DetailKeyword: q.Get("detailKeyword"),
		From:          parseRFC3339(q.Get("from")),
		To:            parseRFC3339(q.Get("to")),
		Page:          page,
		Size:          size,
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	views := make([]auditView, 0, len(items))
	for _, a := range items {
		views = append(views, auditView{
			ID: a.ID, Namespace: a.NamespaceCode, Operator: a.Operator, Action: a.Action,
			TargetType: a.TargetType, TargetRef: a.TargetRef, Detail: a.Detail,
			Result: a.Result, ClientIP: a.ClientIP, CreatedAt: a.CreatedAt,
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"total": total, "items": views})
}

// auditExportFilter 从查询串提取与 List 同口径的过滤（不含分页，导出全量，FR-84）。
func auditExportFilter(q url.Values) repository.AuditFilter {
	return repository.AuditFilter{
		Namespace:     q.Get("namespace"),
		Operator:      q.Get("operator"),
		Action:        q.Get("action"),
		TargetType:    q.Get("targetType"),
		TargetRef:     q.Get("targetRef"),
		DetailKeyword: q.Get("detailKeyword"),
		From:          parseRFC3339(q.Get("from")),
		To:            parseRFC3339(q.Get("to")),
	}
}

// Export 处理 GET /admin/v1/audits/export（复用 List 过滤，流式输出 CSV/JSON，FR-84）。
// format 校验失败在写出响应头前返回 400 统一错误体；流式写出过程中出错只记日志（头已发无法改状态码）。
func (h *AuditHandler) Export(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	if format == "" {
		format = "csv"
	}
	// 写头前先校验 format（仅 csv/json），非法直接 400，不污染响应。
	contentType, ext, ok := exportContentType(format)
	if !ok {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	filename := "audit-export-" + time.Now().UTC().Format("20060102-150405") + "." + ext
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	if err := h.svc.Export(auditExportFilter(q), format, w); err != nil {
		// 响应头已发送，无法再改状态码，仅记录错误日志（旁路）。
		slog.Error("审计导出写出失败", "格式", format, "错误", err)
	}
}

// exportContentType 把导出格式映射到 Content-Type 与文件扩展名；非 csv/json 返回 ok=false。
func exportContentType(format string) (contentType, ext string, ok bool) {
	switch format {
	case "csv":
		return "text/csv; charset=utf-8", "csv", true
	case "json":
		return "application/json; charset=utf-8", "json", true
	default:
		return "", "", false
	}
}

// auditActionCountView 是按动作分布的对外元素（§3.2 契约）。
type auditActionCountView struct {
	Action string `json:"action"`
	Count  int    `json:"count"`
}

// auditDayCountView 是每日趋势的对外元素（§3.2 契约）。
type auditDayCountView struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// auditAnalyticsView 是审计聚合的对外视图（§3.2 契约，小驼峰）。
type auditAnalyticsView struct {
	From      time.Time              `json:"from"`
	To        time.Time              `json:"to"`
	Total     int                    `json:"total"`
	OKCount   int                    `json:"okCount"`
	FailCount int                    `json:"failCount"`
	ByAction  []auditActionCountView `json:"byAction"`
	ByDay     []auditDayCountView    `json:"byDay"`
}

// Analytics 处理 GET /admin/v1/audits/analytics（窗口内审计活动聚合，FR-73）。
// 仅解析 namespace/from/to，缺省与 92 天上限校验在 service 层（超限返 400）。
func (h *AuditHandler) Analytics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res, err := h.svc.Analytics(repository.AuditFilter{
		Namespace: q.Get("namespace"),
		From:      parseRFC3339(q.Get("from")),
		To:        parseRFC3339(q.Get("to")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	byAction := make([]auditActionCountView, 0, len(res.ByAction))
	for _, a := range res.ByAction {
		byAction = append(byAction, auditActionCountView{Action: a.Action, Count: a.Count})
	}
	byDay := make([]auditDayCountView, 0, len(res.ByDay))
	for _, d := range res.ByDay {
		byDay = append(byDay, auditDayCountView{Date: d.Date, Count: d.Count})
	}
	render.WriteJSON(w, http.StatusOK, auditAnalyticsView{
		From: res.From, To: res.To, Total: res.Total,
		OKCount: res.OKCount, FailCount: res.FailCount,
		ByAction: byAction, ByDay: byDay,
	})
}

// parseRFC3339 解析 RFC3339 时间；空或非法返回零值（不设界）。
func parseRFC3339(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
