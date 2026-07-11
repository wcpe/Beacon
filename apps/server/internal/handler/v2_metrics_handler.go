package handler

import (
	"encoding/json"
	"net/http"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// V2MetricsHandler 处理 v2 agent 指标上报端点（FR-144，见 §5.1）。
// handler 只做解码 + 取注入身份 + 调服务；校验 / 窗口 / 入队全在服务层，请求 goroutine 不碰 DB。
type V2MetricsHandler struct {
	svc *service.MetricIngestService
}

// NewV2MetricsHandler 构造处理器。
func NewV2MetricsHandler(svc *service.MetricIngestService) *V2MetricsHandler {
	return &V2MetricsHandler{svc: svc}
}

// metricReportRequest 是 POST /beacon/v2/agent/metrics/report 的请求体（camelCase，对齐 §5.1）。
type metricReportRequest struct {
	Namespace        string                 `json:"namespace"`
	ServerID         string                 `json:"serverId"`
	Kind             string                 `json:"kind"`
	AgentTimeMs      int64                  `json:"agentTimeMs"`
	DroppedSinceLast int                    `json:"droppedSinceLast"`
	Samples          []metricReportSampleJS `json:"samples"`
}

// metricReportSampleJS 是单条 5s 批聚合样本（agent 端已聚合，字段对齐 §3.1 列的 camelCase 形态）。
type metricReportSampleJS struct {
	BucketStartMs   int64   `json:"bucketStartMs"`
	SampleCount     int     `json:"sampleCount"`
	CPUPctAvg       float64 `json:"cpuPctAvg"`
	CPUPctMax       float64 `json:"cpuPctMax"`
	MemUsedMbAvg    float64 `json:"memUsedMbAvg"`
	MemMaxMb        int     `json:"memMaxMb"`
	TPSAvg          float64 `json:"tpsAvg"`
	TPSMin          float64 `json:"tpsMin"`
	OnlineAvg       int     `json:"onlineAvg"`
	OnlineMax       int     `json:"onlineMax"`
	MaxOnline       int     `json:"maxOnline"`
	ConnAvg         int     `json:"connAvg"`
	ConnMax         int     `json:"connMax"`
	BackendUp       int     `json:"backendUp"`
	BackendTotal    int     `json:"backendTotal"`
	BackendRttMsAvg float64 `json:"backendRttMsAvg"`
	ReportRttMs     int     `json:"reportRttMs"`
}

// Report 处理 POST /beacon/v2/agent/metrics/report：接收 agent 5s 批聚合指标。
// 202 {accepted, deduplicated, self}；429 metrics_ingest_busy；400 clock_skew_too_large / 参数错误。
// self（自身健康）留空占位——健康模型是 P4b（v2-metrics-health-scheduling.md §4.4），本片只做采样入库。
func (h *V2MetricsHandler) Report(w http.ResponseWriter, r *http.Request) {
	identity, ok := agentauth.FromContext(r.Context())
	if !ok {
		// 未经中间件注入身份即到达（装配错误）；按未授权兜底，不泄露内部细节。
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	var req metricReportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	result, err := h.svc.Ingest(service.MetricReportParams{
		Identity:         identity,
		BodyServerID:     req.ServerID,
		BodyKind:         req.Kind,
		AgentTimeMs:      req.AgentTimeMs,
		DroppedSinceLast: req.DroppedSinceLast,
		Samples:          toServiceSamples(req.Samples),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, map[string]any{
		"accepted":     result.Accepted,
		"deduplicated": result.Deduplicated,
		// self 健康视图数据源在 P4b 健康计算就绪后填充，本片占位为 null。
		"self": nil,
	})
}

// toServiceSamples 把请求体样本映射为服务层样本（字段直传，不在 handler 做业务归一化）。
func toServiceSamples(in []metricReportSampleJS) []service.MetricReportSample {
	out := make([]service.MetricReportSample, 0, len(in))
	for _, s := range in {
		out = append(out, service.MetricReportSample{
			BucketStartMs:   s.BucketStartMs,
			SampleCount:     s.SampleCount,
			CPUPctAvg:       s.CPUPctAvg,
			CPUPctMax:       s.CPUPctMax,
			MemUsedMbAvg:    s.MemUsedMbAvg,
			MemMaxMb:        s.MemMaxMb,
			TPSAvg:          s.TPSAvg,
			TPSMin:          s.TPSMin,
			OnlineAvg:       s.OnlineAvg,
			OnlineMax:       s.OnlineMax,
			MaxOnline:       s.MaxOnline,
			ConnAvg:         s.ConnAvg,
			ConnMax:         s.ConnMax,
			BackendUp:       s.BackendUp,
			BackendTotal:    s.BackendTotal,
			BackendRttMsAvg: s.BackendRttMsAvg,
			ReportRttMs:     s.ReportRttMs,
		})
	}
	return out
}
