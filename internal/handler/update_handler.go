package handler

import (
	"net/http"

	"github.com/wcpe/Beacon/internal/auth"
	"github.com/wcpe/Beacon/internal/redact"
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
		Phase:         string(p.Phase),
		Percent:       p.Percent,
		TargetVersion: p.TargetVersion,
		// 失败原因脱敏后展示（FR-122/ADR-0057）：让运维看见更新为何失败又不泄露凭据（如代理账密）。
		Error:             redact.Desensitize(p.Error),
		RollbackAvailable: h.svc.RollbackAvailable(),
	})
}

// Apply 处理 POST /admin/v1/system/update：触发应用更新（写方法，readonly 经 readonlyWriteGuard 403）。
// fix-1：apply 改异步——受理后立即回 202，下载 / 校验 / 落位 / 重启在后台进行，前端经状态端点轮询进度；
// 已有更新进行中再触发 → 409 UPDATE_IN_PROGRESS。失败原因写入进度态（脱敏）由前端轮询展示，不静默。
func (h *UpdateHandler) Apply(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Apply(auth.Operator(r.Context()), clientIP(r)); err != nil {
		render.WriteError(w, r, err)
		return
	}
	render.WriteJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

// Cancel 处理 POST /admin/v1/system/update/cancel：取消进行中的更新下载（写方法，readonly 经 readonlyWriteGuard 403）。
// 有进行中→取消其下载 context 回 202 {cancelled:true}（核心于下载中断时审计 system.update-cancel、进度回 idle）；
// 无进行中→幂等回 200 {cancelled:false}（非错误）。
func (h *UpdateHandler) Cancel(w http.ResponseWriter, _ *http.Request) {
	cancelled := h.svc.CancelApply()
	status := http.StatusOK
	if cancelled {
		status = http.StatusAccepted
	}
	render.WriteJSON(w, status, map[string]any{"cancelled": cancelled})
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
