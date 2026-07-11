package handler

import (
	"encoding/json"
	"net/http"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// V2SchedHandler 处理 v2 agent 调度端点（FR-146，见 spec §5.1）：候选快照 / 决策 / 降级补报。
// handler 只做解码 + 取注入身份 + 调服务；决策与判重全在服务层纯内存，请求 goroutine 不碰 DB。
type V2SchedHandler struct {
	svc *service.SchedulingV2Service
}

// NewV2SchedHandler 构造处理器。
func NewV2SchedHandler(svc *service.SchedulingV2Service) *V2SchedHandler {
	return &V2SchedHandler{svc: svc}
}

// schedCandidateJS 是候选快照中单台候选（键逐字对齐 §5.1 candidates 行）。
type schedCandidateJS struct {
	ServerID    string `json:"serverId"`
	Score       int    `json:"score"`
	Level       string `json:"level"`
	Schedulable bool   `json:"schedulable"`
	OnlineCount int    `json:"onlineCount"`
	MaxOnline   int    `json:"maxOnline"`
}

// schedZoneJS 是单 zone 候选集。
type schedZoneJS struct {
	Zone       string             `json:"zone"`
	Candidates []schedCandidateJS `json:"candidates"`
}

// schedCandidatesResponse 是 GET /beacon/v2/agent/schedule/candidates 的响应体。
type schedCandidatesResponse struct {
	GeneratedAtMs int64         `json:"generatedAtMs"`
	Zones         []schedZoneJS `json:"zones"`
}

// Candidates 处理 GET /beacon/v2/agent/schedule/candidates：按请求方 namespace 圈定，
// 返回全部有候选的 zone 及其可调度候选（agent 每 10s 拉取刷新本地降级快照，spec §4.6）。
func (h *V2SchedHandler) Candidates(w http.ResponseWriter, r *http.Request) {
	identity, ok := agentauth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	result := h.svc.Candidates(identity)
	zones := make([]schedZoneJS, 0, len(result.Zones))
	for _, z := range result.Zones {
		candidates := make([]schedCandidateJS, 0, len(z.Candidates))
		for _, c := range z.Candidates {
			candidates = append(candidates, schedCandidateJS{
				ServerID: c.ServerID, Score: c.Score, Level: c.Level,
				Schedulable: c.Schedulable, OnlineCount: c.OnlineCount, MaxOnline: c.MaxOnline,
			})
		}
		zones = append(zones, schedZoneJS{Zone: z.Zone, Candidates: candidates})
	}
	render.WriteJSON(w, http.StatusOK, schedCandidatesResponse{GeneratedAtMs: result.GeneratedAtMs, Zones: zones})
}

// schedDecideRequest 是 POST /beacon/v2/agent/schedule/decide 的请求体（§5.1：purpose/plugin 可空）。
type schedDecideRequest struct {
	Zone    string `json:"zone"`
	Purpose string `json:"purpose"`
	Plugin  string `json:"plugin"`
}

// schedChosenJS 是决策选中结果（失败时整体为 null）。
type schedChosenJS struct {
	ServerID string `json:"serverId"`
	Score    int    `json:"score"`
}

// schedDecideResponse 是 decide 的 200 响应体（键逐字对齐 §5.1）。
type schedDecideResponse struct {
	TraceID        string         `json:"traceId"`
	Chosen         *schedChosenJS `json:"chosen"`
	CandidateCount int            `json:"candidateCount"`
	ExcludedCount  int            `json:"excludedCount"`
	FailReason     *string        `json:"failReason"`
}

// Decide 处理 POST /beacon/v2/agent/schedule/decide：控制面在线调度决策（纯内存，目标 <5ms）。
// 200 含选择结果 + traceId + 解释摘要；404 zone_not_found；无候选为 200 + failReason=no_candidate。
func (h *V2SchedHandler) Decide(w http.ResponseWriter, r *http.Request) {
	identity, ok := agentauth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	var req schedDecideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	outcome, err := h.svc.Decide(identity, req.Zone, req.Purpose, req.Plugin)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	resp := schedDecideResponse{
		TraceID:        outcome.TraceID,
		CandidateCount: outcome.CandidateCount,
		ExcludedCount:  len(outcome.Excluded),
	}
	if outcome.Chosen() {
		resp.Chosen = &schedChosenJS{ServerID: outcome.ChosenServerID, Score: outcome.ChosenScore}
	}
	if outcome.FailReason != "" {
		resp.FailReason = &outcome.FailReason
	}
	render.WriteJSON(w, http.StatusOK, resp)
}

// schedLocalDecisionJS 是单条降级补报（键逐字对齐 §5.1 report-local 行）。
type schedLocalDecisionJS struct {
	LocalTraceID   string                  `json:"localTraceId"`
	TsMs           int64                   `json:"tsMs"`
	Zone           string                  `json:"zone"`
	Plugin         string                  `json:"plugin"`
	Purpose        string                  `json:"purpose"`
	CandidateCount int                     `json:"candidateCount"`
	Excluded       []service.SchedExcluded `json:"excluded"`
	ChosenServerID string                  `json:"chosenServerId"`
	FailReason     string                  `json:"failReason"`
}

// schedReportLocalRequest 是 POST /beacon/v2/agent/schedule/report-local 的请求体。
type schedReportLocalRequest struct {
	Decisions []schedLocalDecisionJS `json:"decisions"`
}

// ReportLocal 处理 POST /beacon/v2/agent/schedule/report-local：降级期本地决策批量补报。
// 202 {accepted, deduplicated}（按 localTraceId 幂等）；单批 >100 条 400。
func (h *V2SchedHandler) ReportLocal(w http.ResponseWriter, r *http.Request) {
	identity, ok := agentauth.FromContext(r.Context())
	if !ok {
		render.WriteError(w, r, apperr.ErrUnauthorized)
		return
	}
	var req schedReportLocalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	decisions := make([]service.LocalDecisionReport, 0, len(req.Decisions))
	for _, d := range req.Decisions {
		decisions = append(decisions, service.LocalDecisionReport{
			LocalTraceID: d.LocalTraceID, TsMs: d.TsMs, Zone: d.Zone,
			Plugin: d.Plugin, Purpose: d.Purpose, CandidateCount: d.CandidateCount,
			Excluded: d.Excluded, ChosenServerID: d.ChosenServerID, FailReason: d.FailReason,
		})
	}
	result, err := h.svc.ReportLocal(identity, decisions)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, map[string]any{
		"accepted":     result.Accepted,
		"deduplicated": result.Deduplicated,
	})
}
