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
// labels 是该节点**自己声明**的键值标签（FR-244 起随候选一起下发）。它按"真源装配没装配"决定在不在：
// 装配了 → 总是下发（没声明标签即 `{}`，是稳定事实）；未装配 → **整个键不发**，调用方据此判"看不到声明"
// （不得读成"没有声明"）。故这里用指针 + omitempty：nil 不发、指向空 map 发 `{}`。
type schedCandidateJS struct {
	ServerID    string             `json:"serverId"`
	Score       int                `json:"score"`
	Level       string             `json:"level"`
	Schedulable bool               `json:"schedulable"`
	OnlineCount int                `json:"onlineCount"`
	MaxOnline   int                `json:"maxOnline"`
	Labels      *map[string]string `json:"labels,omitempty"`
}

// schedZoneJS 是单 zone 候选集。
type schedZoneJS struct {
	Zone       string             `json:"zone"`
	Candidates []schedCandidateJS `json:"candidates"`
}

// labelsField 把候选的标签表翻成响应字段：nil（真源未装配 = 看不到声明）→ 不发该键；
// 非 nil（含空 map）→ 发出去，空 map 序列化为 `{}`（= 这台节点确实没声明过标签，稳定事实）。
func labelsField(labels map[string]string) *map[string]string {
	if labels == nil {
		return nil
	}
	return &labels
}

// schedCandidatesResponse 是 GET /beacon/v2/agent/schedule/candidates 的响应体。
type schedCandidatesResponse struct {
	GeneratedAtMs int64         `json:"generatedAtMs"`
	Lobby         schedLobbyJS  `json:"lobby"`
	Zones         []schedZoneJS `json:"zones"`
}

// schedLobbyJS 是 namespace 大厅候选集；空集仍返回 ready=false，避免与协议缺失混淆。
type schedLobbyJS struct {
	ClusterID  uint               `json:"clusterId"`
	Ready      bool               `json:"ready"`
	Candidates []schedCandidateJS `json:"candidates"`
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
				Labels: labelsField(c.Labels),
			})
		}
		zones = append(zones, schedZoneJS{Zone: z.Zone, Candidates: candidates})
	}
	lobbyCandidates := make([]schedCandidateJS, 0, len(result.Lobby.Candidates))
	for _, c := range result.Lobby.Candidates {
		lobbyCandidates = append(lobbyCandidates, schedCandidateJS{
			ServerID: c.ServerID, Score: c.Score, Level: c.Level, Schedulable: c.Schedulable,
			OnlineCount: c.OnlineCount, MaxOnline: c.MaxOnline, Labels: labelsField(c.Labels),
		})
	}
	render.WriteJSON(w, http.StatusOK, schedCandidatesResponse{
		GeneratedAtMs: result.GeneratedAtMs, Zones: zones,
		Lobby: schedLobbyJS{ClusterID: result.Lobby.ClusterID, Ready: result.Lobby.Ready, Candidates: lobbyCandidates},
	})
}

// schedDecideRequest 是 POST /beacon/v2/agent/schedule/decide 的请求体（§5.1：purpose/plugin 可空）。
//
// admissionScope 是可选的**准入作用域**（FR-244）：若干**备选**，每个备选是一组
// 「候选节点必须自己声明过的键值标签」（备选之间 OR、备选之内 AND，逐项精确相等，键值上限同 FR-227）。
// 缺键 / 空数组 = 不按标签收窄，行为与不带它逐位一致（旧客户端逐字不变）；备选全为空对象（如 `[{}]`）
// 任何候选都满足、没有约束力，服务层会把它归一为同一档（不读标签真源）。
// 带**有约束力**的作用域而本进程没有标签真源时按 503 报出，**不**静默忽略。
type schedDecideRequest struct {
	Scope          string              `json:"scope"`
	Zone           string              `json:"zone"`
	Purpose        string              `json:"purpose"`
	Plugin         string              `json:"plugin"`
	AdmissionScope []map[string]string `json:"admissionScope"`
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
	// AdmissionExcludedCount 是本次**因准入作用域**被排除的候选台数（FR-244 的增量键）。
	// 只在 >0 时下发：调用方据此留一条"决策阶段确实收窄了"的读数行，而没被收窄的响应
	// （含全部旧客户端路径）键集合与 §5.1 逐字一致。
	AdmissionExcludedCount int `json:"admissionExcludedCount,omitempty"`
}

// Decide 处理 POST /beacon/v2/agent/schedule/decide：控制面在线调度决策（纯内存，目标 <5ms）。
// 200 含选择结果 + traceId + 解释摘要；404 zone_not_found；无候选为 200 + failReason=no_candidate
// （带准入作用域时全被滤掉则为 no_candidate_in_scope）；作用域判不了为 503 admission_unavailable。
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
	scope := req.Scope
	if scope == "" {
		scope = service.SchedScopeZone
	}
	outcome, err := h.svc.DecideScopedWithAdmission(identity, scope, req.Zone, req.Purpose, req.Plugin,
		req.AdmissionScope)
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	resp := schedDecideResponse{
		TraceID:                outcome.TraceID,
		CandidateCount:         outcome.CandidateCount,
		ExcludedCount:          len(outcome.Excluded),
		AdmissionExcludedCount: outcome.AdmissionExcludedCount,
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
