package handler

import (
	"net/http"

	"github.com/wcpe/Beacon/internal/auth"
	"github.com/wcpe/Beacon/internal/render"
	"github.com/wcpe/Beacon/internal/service"
)

// UpdateHandler 处理控制面在线更新的 HTTP 触发面（FR-99，见 ADR-0044）：
// 检查（只读、带服务端缓存）/ 状态（读内存进度）/ 触发应用（写、readonly 403 + 审计）。
// 只读拒写与审计由路由中间件统一裁决；handler 不碰 http.Client（出站由更新核心经 FR-98 工厂收口）、不读 store。
type UpdateHandler struct {
	svc *service.UpdateService
}

// NewUpdateHandler 构造处理器。
func NewUpdateHandler(svc *service.UpdateService) *UpdateHandler {
	return &UpdateHandler{svc: svc}
}

// progressView 是更新进度对外视图（FR-99 状态端点）。
type progressView struct {
	Phase             string `json:"phase"`             // idle / checking / downloading / verifying / staging / ready-restart / failed
	Percent           int    `json:"percent"`           // 下载百分比 0-100（仅下载阶段有意义）
	TargetVersion     string `json:"targetVersion"`     // 目标版本；空表示尚未确定
	Error             string `json:"error"`             // 失败原因（仅 failed 非空）
	RollbackAvailable bool   `json:"rollbackAvailable"` // 是否有可回退的上一版本（.old，FR-120 前端按钮显隐）
}

// Check 处理 GET /admin/v1/system/update-check：按渠道检查有无可用更新（只读，full/readonly 皆可见）。
// 服务端内存缓存（TTL 取自 update.check-interval-hours），命中不打 GitHub；?force=true 绕缓存刷新（仍 GET、仅刷缓存）。
// GitHub 不可达 / 限流 / 解析失败 → status=check-failed（200 返回、不阻断页面）；current=='dev' → isDevBuild=true 且不提示。
func (h *UpdateHandler) Check(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "true"
	view := h.svc.Check(r.Context(), force, auth.Operator(r.Context()), clientIP(r))
	render.WriteJSON(w, http.StatusOK, view)
}

// Status 处理 GET /admin/v1/system/update：读更新进度内存态（不查库、不打 GitHub）。
func (h *UpdateHandler) Status(w http.ResponseWriter, _ *http.Request) {
	p := h.svc.Status()
	render.WriteJSON(w, http.StatusOK, progressView{
		Phase:             string(p.Phase),
		Percent:           p.Percent,
		TargetVersion:     p.TargetVersion,
		Error:             p.Error,
		RollbackAvailable: h.svc.RollbackAvailable(),
	})
}

// Apply 处理 POST /admin/v1/system/update：触发应用更新（写方法，readonly 经 readonlyWriteGuard 403）。
// 调更新核心下载 → 校验 → 落位 pending → 请求重启；任一阶段失败保留旧二进制不退、返回对应错误。
// 落位成功后主进程将优雅关停并自替换换二进制重启，故仅在失败时返回错误体，成功时回 202 表示已接受并开始应用。
func (h *UpdateHandler) Apply(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Apply(r.Context(), auth.Operator(r.Context()), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

// Rollback 处理 POST /admin/v1/system/rollback：触发手动回滚到上一版本（写方法，readonly 经 readonlyWriteGuard 403）。
// 无 .old 备份返回 409；成功回 202 表示已接受，随后主进程优雅关停并回退重启（FR-120）。
func (h *UpdateHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Rollback(auth.Operator(r.Context()), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}
