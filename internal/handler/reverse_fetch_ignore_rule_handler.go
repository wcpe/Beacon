package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/auth"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/render"
	"github.com/wcpe/Beacon/internal/service"
)

// ReverseFetchIgnoreRuleHandler 处理反向抓取持久忽略规则（FR-59）：列 / 建 / 删。
type ReverseFetchIgnoreRuleHandler struct {
	svc *service.ReverseFetchIgnoreRuleService
}

// NewReverseFetchIgnoreRuleHandler 构造处理器。
func NewReverseFetchIgnoreRuleHandler(svc *service.ReverseFetchIgnoreRuleService) *ReverseFetchIgnoreRuleHandler {
	return &ReverseFetchIgnoreRuleHandler{svc: svc}
}

// ignoreRuleView 是忽略规则对外视图。
type ignoreRuleView struct {
	ID        uint   `json:"id"`
	Namespace string `json:"namespace"`
	Scope     string `json:"scope"`
	Group     string `json:"group"`
	Target    string `json:"target"`
	RuleType  string `json:"ruleType"`
	Pattern   string `json:"pattern"`
	Comment   string `json:"comment"`
	Operator  string `json:"operator"`
	CreatedAt string `json:"createdAt"`
}

func toIgnoreRuleView(r *model.ReverseFetchIgnoreRule) ignoreRuleView {
	return ignoreRuleView{
		ID: r.ID, Namespace: r.NamespaceCode, Scope: r.Scope, Group: r.GroupCode, Target: r.ScopeTarget,
		RuleType: r.RuleType, Pattern: r.Pattern, Comment: r.Comment, Operator: r.Operator,
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// List 处理 GET /admin/v1/reverse-fetch/ignore-rules?namespace=&scope=&group=&target=（FR-59）：列活跃规则。
func (h *ReverseFetchIgnoreRuleHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rules, err := h.svc.List(q.Get("namespace"), q.Get("scope"), q.Get("group"), q.Get("target"))
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	items := make([]ignoreRuleView, len(rules))
	for i := range rules {
		items[i] = toIgnoreRuleView(&rules[i])
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// createIgnoreRuleRequest 是建忽略规则请求体（FR-59）。
type createIgnoreRuleRequest struct {
	Namespace string `json:"namespace"`
	Scope     string `json:"scope"`
	Group     string `json:"group"`
	Target    string `json:"target"`
	RuleType  string `json:"ruleType"`
	Pattern   string `json:"pattern"`
	Comment   string `json:"comment"`
}

// Create 处理 POST /admin/v1/reverse-fetch/ignore-rules（FR-59）：建一条持久忽略规则 + 审计。返回规则视图（201）。
func (h *ReverseFetchIgnoreRuleHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createIgnoreRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	rule, err := h.svc.Create(service.CreateRuleParams{
		Namespace: req.Namespace, Scope: req.Scope, Group: req.Group, ScopeTarget: req.Target,
		RuleType: req.RuleType, Pattern: req.Pattern, Comment: req.Comment,
		Operator: auth.Operator(r.Context()), ClientIP: clientIP(r),
	})
	if err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusCreated, toIgnoreRuleView(rule))
}

// Delete 处理 DELETE /admin/v1/reverse-fetch/ignore-rules/{id}（FR-59）：软删一条规则 + 审计。
func (h *ReverseFetchIgnoreRuleHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUintParam(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(id, auth.Operator(r.Context()), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
