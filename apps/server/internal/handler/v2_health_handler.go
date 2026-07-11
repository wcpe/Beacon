package handler

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// V2HealthHandler 处理健康与指标管理端点（FR-147，见 §5.2）。
// handler 只做解码 / 参数提取 / 调服务，不碰内存结构与 GORM。
type V2HealthHandler struct {
	query   *service.HealthQueryService
	weights *service.HealthWeightsService
}

// NewV2HealthHandler 构造处理器。
func NewV2HealthHandler(query *service.HealthQueryService, weights *service.HealthWeightsService) *V2HealthHandler {
	return &V2HealthHandler{query: query, weights: weights}
}

// ListHealth 处理 GET /admin/v2/health：全部服务器当前健康列表（内存实时；分页 + 筛选）。
func (h *V2HealthHandler) ListHealth(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	namespaceID, err := optionalUintQuery(q.Get("namespaceId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	schedulable, err := optionalBoolQuery(q.Get("schedulable"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items, total, err := h.query.ListHealth(service.ListHealthParams{
		NamespaceID: namespaceID, Zone: q.Get("zone"), Level: q.Get("level"),
		Schedulable: schedulable, Keyword: q.Get("keyword"),
		Page: intQuery(q.Get("page")), PageSize: intQuery(q.Get("pageSize")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// GetHealthDetail 处理 GET /admin/v2/health/{serverId}：单服健康详情（因子分解 + 权重版本）。
func (h *V2HealthHandler) GetHealthDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := h.query.HealthDetail(chi.URLParam(r, "serverId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, detail)
}

// ListHealthSnapshots 处理 GET /admin/v2/health/snapshots?serverId=&from=&to=：健康快照回放。
func (h *V2HealthHandler) ListHealthSnapshots(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fromMs, toMs, err := timeRangeQuery(q)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items, err := h.query.HealthSnapshots(q.Get("serverId"), fromMs, toMs)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// MetricsSummary 处理 GET /admin/v2/metrics/summary：集群聚合概览（内存实时）。
func (h *V2HealthHandler) MetricsSummary(w http.ResponseWriter, _ *http.Request) {
	render.WriteJSON(w, http.StatusOK, h.query.MetricsSummary())
}

// MetricsSeries 处理 GET /admin/v2/metrics/series?serverId=&from=&to=&step=：单服 / 多服指标时序
// （serverId 必填、逗号分隔多服；step 服务端桶聚合）。
func (h *V2HealthHandler) MetricsSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fromMs, toMs, err := timeRangeQuery(q)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	series, err := h.query.MetricsSeries(service.MetricsSeriesParams{
		ServerIDs: splitCSVQuery(q.Get("serverId")),
		FromMs:    fromMs, ToMs: toMs,
		StepSec: intQuery(q.Get("step")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, series)
}

// timeRangeQuery 解析 from / to 查询参数（缺省为 0，由服务层套默认范围）。
func timeRangeQuery(q url.Values) (fromMs, toMs int64, err error) {
	if fromMs, err = timeMsQuery(q.Get("from")); err != nil {
		return 0, 0, err
	}
	if toMs, err = timeMsQuery(q.Get("to")); err != nil {
		return 0, 0, err
	}
	return fromMs, toMs, nil
}

// timeMsQuery 解析时间查询参数为毫秒时间戳：支持 epoch 毫秒整数与 RFC3339；空串返回 0（缺省）。
func timeMsQuery(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	if ms, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return ms, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UnixMilli(), nil
	}
	return 0, apperr.ErrInvalidParam
}

// splitCSVQuery 拆分逗号分隔的查询值（去空项）。
func splitCSVQuery(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// GetHealthWeights 处理 GET /admin/v2/settings/health-weights：当前配置 + rev + 历史 rev 列表。
func (h *V2HealthHandler) GetHealthWeights(w http.ResponseWriter, r *http.Request) {
	overview, err := h.weights.Overview()
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, overview)
}

// PutHealthWeights 处理 PUT /admin/v2/settings/health-weights：全量替换权重配置
// （校验 → 写设置镜像 + 新 rev + 审计 → 热更下一轮生效），成功返回更新后的当前配置 + 历史。
func (h *V2HealthHandler) PutHealthWeights(w http.ResponseWriter, r *http.Request) {
	var cfg service.HealthWeightsConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	if err := h.weights.Update(cfg, auth.Operator(r.Context()), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	overview, err := h.weights.Overview()
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, overview)
}
