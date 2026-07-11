package handler

import (
	"encoding/json"
	"net/http"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/render"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// V2HealthHandler 处理健康与指标管理端点（FR-147，见 §5.2）。
// handler 只做解码 / 参数提取 / 调服务，不碰内存结构与 GORM。
type V2HealthHandler struct {
	weights *service.HealthWeightsService
}

// NewV2HealthHandler 构造处理器。
func NewV2HealthHandler(weights *service.HealthWeightsService) *V2HealthHandler {
	return &V2HealthHandler{weights: weights}
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
