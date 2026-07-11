package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// SchedDecisionAdminHandler 处理调度决策记录的管理面查询端点（FR-146，见 spec §5.2）：
// 列表（分页 + 过滤）/ 单条详情 / 概览聚合。响应形状对齐 contracts SchedDecisionItem/Detail/Summary。
type SchedDecisionAdminHandler struct {
	svc *service.SchedDecisionQueryService
}

// NewSchedDecisionAdminHandler 构造处理器。
func NewSchedDecisionAdminHandler(svc *service.SchedDecisionQueryService) *SchedDecisionAdminHandler {
	return &SchedDecisionAdminHandler{svc: svc}
}

// schedDecisionItemJS 是决策记录列表项（键对齐 contracts SchedDecisionItem，camelCase）。
type schedDecisionItemJS struct {
	TraceID           string  `json:"traceId"`
	TsMs              int64   `json:"tsMs"`
	NamespaceID       uint    `json:"namespaceId"`
	CrossNamespace    bool    `json:"crossNamespace"`
	RequesterServerID string  `json:"requesterServerId"`
	Plugin            *string `json:"plugin"`
	Purpose           *string `json:"purpose"`
	ZoneName          string  `json:"zoneName"`
	Strategy          string  `json:"strategy"`
	Source            string  `json:"source"`
	WeightsRev        *int    `json:"weightsRev"`
	CandidateCount    int     `json:"candidateCount"`
	ExcludedCount     int     `json:"excludedCount"`
	ChosenServerID    *string `json:"chosenServerId"`
	ChosenScore       int     `json:"chosenScore"`
	FailReason        *string `json:"failReason"`
	DurationMs        int     `json:"durationMs"`
}

// schedDecisionDetailJS 是单条决策详情（列表项 + 逐台排除明细，对齐 contracts SchedDecisionDetail）。
type schedDecisionDetailJS struct {
	schedDecisionItemJS
	Excluded []service.SchedExcluded `json:"excluded"`
}

// List 处理 GET /admin/v2/sched-decisions：跨日并表分页查询（from/to 必填毫秒时间戳，范围 ≤60 天）。
func (h *SchedDecisionAdminHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fromMs, errFrom := strconv.ParseInt(q.Get("from"), 10, 64)
	toMs, errTo := strconv.ParseInt(q.Get("to"), 10, 64)
	if errFrom != nil || errTo != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	namespaceID, err := optionalUintQuery(q.Get("namespaceId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	rows, total, err := h.svc.List(service.ListSchedDecisionsParams{
		NamespaceID: namespaceID,
		Zone:        q.Get("zone"),
		ServerID:    q.Get("serverId"),
		Result:      q.Get("result"),
		FromMs:      fromMs,
		ToMs:        toMs,
		Page:        intQuery(q.Get("page")),
		PageSize:    intQuery(q.Get("pageSize")),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items := make([]schedDecisionItemJS, 0, len(rows))
	for i := range rows {
		items = append(items, schedDecisionItem(&rows[i]))
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// Detail 处理 GET /admin/v2/sched-decisions/{traceId}：单条决策详情（含逐台排除原因）。
func (h *SchedDecisionAdminHandler) Detail(w http.ResponseWriter, r *http.Request) {
	row, err := h.svc.Detail(chi.URLParam(r, "traceId"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, schedDecisionDetailJS{
		schedDecisionItemJS: schedDecisionItem(&row),
		Excluded:            schedExcludedOf(&row),
	})
}

// Summary 处理 GET /admin/v2/sched-decisions/summary?window=1h：决策概览聚合。
func (h *SchedDecisionAdminHandler) Summary(w http.ResponseWriter, r *http.Request) {
	result, err := h.svc.Summary(r.URL.Query().Get("window"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	top := make([]map[string]any, 0, len(result.FailReasonTop))
	for _, item := range result.FailReasonTop {
		top = append(top, map[string]any{"reason": item.Reason, "count": item.Count})
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{
		"window":               result.Window,
		"total":                result.Total,
		"successCount":         result.SuccessCount,
		"successRatePercent":   result.SuccessRatePercent,
		"failReasonTop":        top,
		"localFallbackPercent": result.LocalFallbackPercent,
	})
}

// schedDecisionItem 把决策行映射为列表项：可空字段（plugin/purpose/chosen/failReason）空串显 null，
// weightsRev 在降级补报行（source=local_fallback）显 null（对齐 contracts / devmock 语义）。
func schedDecisionItem(row *model.SchedDecisionV2) schedDecisionItemJS {
	item := schedDecisionItemJS{
		TraceID:           row.TraceID,
		TsMs:              row.TsMs,
		NamespaceID:       row.NamespaceID,
		CrossNamespace:    row.CrossNamespace,
		RequesterServerID: row.RequesterServerID,
		Plugin:            nullableStr(row.Plugin),
		Purpose:           nullableStr(row.Purpose),
		ZoneName:          row.ZoneName,
		Strategy:          row.Strategy,
		Source:            row.Source,
		CandidateCount:    row.CandidateCount,
		ExcludedCount:     len(schedExcludedOf(row)),
		ChosenServerID:    nullableStr(row.ChosenServerID),
		ChosenScore:       row.ChosenScore,
		FailReason:        nullableStr(row.FailReason),
		DurationMs:        row.DurationMs,
	}
	if row.Source != model.SchedSourceLocalFallback {
		rev := row.WeightsRev
		item.WeightsRev = &rev
	}
	return item
}

// schedExcludedOf 解析行内 excluded json 数组文本；空 / 非法文本按空数组处理（防坏行拖垮查询）。
func schedExcludedOf(row *model.SchedDecisionV2) []service.SchedExcluded {
	var excluded []service.SchedExcluded
	if row.Excluded != "" {
		_ = json.Unmarshal([]byte(row.Excluded), &excluded)
	}
	if excluded == nil {
		excluded = []service.SchedExcluded{}
	}
	return excluded
}

// nullableStr 把「空串即缺省」的 VARCHAR 字段映射为 json 可空值。
func nullableStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
