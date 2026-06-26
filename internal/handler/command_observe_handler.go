package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/wcpe/Beacon/internal/render"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/service"
)

// CommandObserveHandler 处理命令观测 admin 请求（FR-104，增强 FR-17/FR-82）：
// 列表（按 namespace/serverId/type/status/时间过滤 + 分页）+ 聚合（计数 + 趋势）。只读、不写不改命令。
// 仅请求期解码 / 编码；过滤校验、缺省与上限、Go 侧分桶在 service 层。
type CommandObserveHandler struct {
	svc *service.CommandObserveService
}

// NewCommandObserveHandler 构造处理器。
func NewCommandObserveHandler(svc *service.CommandObserveService) *CommandObserveHandler {
	return &CommandObserveHandler{svc: svc}
}

// commandMetaView 是命令元数据对外视图（小驼峰）：仅元数据 + 结果摘要 + 派生已等时长，
// **绝不含** imprintContent / logContent / payload（投影在 repo 已排除）。
type commandMetaView struct {
	CommandID    uint      `json:"commandId"`
	Namespace    string    `json:"namespace"`
	ServerID     string    `json:"serverId"`
	Type         string    `json:"type"`
	Status       string    `json:"status"`
	ResultDetail string    `json:"resultDetail"`
	Operator     string    `json:"operator"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	// 已等时长（秒）= now - createdAt，供前端实时队列显示「已等多久」（也可前端按 createdAt 自算）
	AgeSeconds int64 `json:"ageSeconds"`
}

// List 处理 GET /admin/v1/commands（分页 + 过滤，创建时间倒序）。
func (h *CommandObserveHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("size"))
	items, total, err := h.svc.List(repository.CommandFilter{
		Namespace: q.Get("namespace"),
		ServerID:  q.Get("serverId"),
		Type:      q.Get("type"),
		Status:    q.Get("status"),
		From:      parseRFC3339(q.Get("from")),
		To:        parseRFC3339(q.Get("to")),
		Page:      page,
		Size:      size,
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	now := time.Now().UTC()
	views := make([]commandMetaView, 0, len(items))
	for _, c := range items {
		views = append(views, commandMetaView{
			CommandID: c.ID, Namespace: c.NamespaceCode, ServerID: c.ServerID,
			Type: c.Type, Status: c.Status, ResultDetail: c.ResultDetail, Operator: c.Operator,
			CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
			AgeSeconds: ageSeconds(now, c.CreatedAt),
		})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"total": total, "items": views})
}

// ageSeconds 计算 createdAt 距 now 的秒数（不为负，零值时间返 0）。
func ageSeconds(now, createdAt time.Time) int64 {
	if createdAt.IsZero() {
		return 0
	}
	d := int64(now.Sub(createdAt).Seconds())
	if d < 0 {
		return 0
	}
	return d
}

// commandStatusCountView 是按状态计数的对外元素。
type commandStatusCountView struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

// commandTypeCountView 是按类型计数的对外元素。
type commandTypeCountView struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// commandServerCountView 是按服务器计数的对外元素（top-N）。
type commandServerCountView struct {
	ServerID string `json:"serverId"`
	Count    int    `json:"count"`
}

// commandDayCountView 是命令量每日趋势的对外元素（下发 / 完成 / 失败）。
type commandDayCountView struct {
	Date   string `json:"date"`
	Issued int    `json:"issued"`
	Done   int    `json:"done"`
	Failed int    `json:"failed"`
}

// commandAnalyticsView 是命令聚合的对外视图（小驼峰）。
type commandAnalyticsView struct {
	From     time.Time                `json:"from"`
	To       time.Time                `json:"to"`
	Total    int                      `json:"total"`
	ByStatus []commandStatusCountView `json:"byStatus"`
	ByType   []commandTypeCountView   `json:"byType"`
	ByServer []commandServerCountView `json:"byServer"`
	ByDay    []commandDayCountView    `json:"byDay"`
}

// Analytics 处理 GET /admin/v1/commands/analytics（窗口内命令活动聚合，FR-104）。
// 仅解析 namespace/from/to，缺省与 92 天上限校验在 service 层（超限返 400）。
func (h *CommandObserveHandler) Analytics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res, err := h.svc.Analytics(repository.CommandFilter{
		Namespace: q.Get("namespace"),
		From:      parseRFC3339(q.Get("from")),
		To:        parseRFC3339(q.Get("to")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	byStatus := make([]commandStatusCountView, 0, len(res.ByStatus))
	for _, c := range res.ByStatus {
		byStatus = append(byStatus, commandStatusCountView{Status: c.Status, Count: c.Count})
	}
	byType := make([]commandTypeCountView, 0, len(res.ByType))
	for _, c := range res.ByType {
		byType = append(byType, commandTypeCountView{Type: c.Type, Count: c.Count})
	}
	byServer := make([]commandServerCountView, 0, len(res.ByServer))
	for _, c := range res.ByServer {
		byServer = append(byServer, commandServerCountView{ServerID: c.ServerID, Count: c.Count})
	}
	byDay := make([]commandDayCountView, 0, len(res.ByDay))
	for _, d := range res.ByDay {
		byDay = append(byDay, commandDayCountView{Date: d.Date, Issued: d.Issued, Done: d.Done, Failed: d.Failed})
	}
	render.WriteJSON(w, http.StatusOK, commandAnalyticsView{
		From: res.From, To: res.To, Total: res.Total,
		ByStatus: byStatus, ByType: byType, ByServer: byServer, ByDay: byDay,
	})
}
