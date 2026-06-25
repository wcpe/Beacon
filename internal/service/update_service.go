package service

import (
	"context"
	"sync"
	"time"

	"github.com/wcpe/Beacon/internal/update"
)

// updateCore 是 UpdateService 对在线更新核心（internal/update.Service）的窄依赖（FR-99）。
// 抽成接口便于单测注入假实现，不连真 GitHub / 不落盘。
type updateCore interface {
	// CheckForUpdate 按渠道查最新 release 并与当前版本比对（只读、不下载）。
	CheckForUpdate(ctx context.Context, ch update.Channel, proxyURL, operator, clientIP string) (update.CheckResult, error)
	// ApplyUpdate 执行一次完整更新（下载 → 校验 → 落位 pending → 请求重启）。
	ApplyUpdate(ctx context.Context, ch update.Channel, proxyURL, operator, clientIP string) error
	// Snapshot 返回当前更新进度快照（进程内瞬态）。
	Snapshot() update.Progress
}

// updateSettingsReader 是 UpdateService 对设置 store 的窄读依赖（FR-99 消费 FR-101 加的热改项）。
// 渠道 / 代理 / 缓存 TTL（自动检查周期）均从 store 读、热生效，不在本服务硬编码。
type updateSettingsReader interface {
	GetString(key string) string
	GetInt(key string) int
}

// UpdateCheckView 是更新检查端点对外视图（FR-99，FR-100 前端消费）。
// 一份契约真源：成功时 status="ok"，GitHub 不可达 / 解析失败时 status="check-failed"（非 5xx、不阻断页面）。
type UpdateCheckView struct {
	// 检查状态：ok=查到结果、check-failed=GitHub 不可达 / 限流 / 解析失败（降级、非错误）
	Status string `json:"status"`
	// 当前运行版本（dev 构建为 "dev"）
	CurrentVersion string `json:"currentVersion"`
	// 当前更新渠道（stable / rc，从 store 读）
	Channel string `json:"channel"`
	// 是否有可用更新（远端严格高于当前；dev 构建恒 false）
	HasUpdate bool `json:"hasUpdate"`
	// 当前是否为 dev 构建（版本未知、不提示更新）
	IsDevBuild bool `json:"isDevBuild"`
	// 渠道最新可用版本（tag）；check-failed 时为空
	LatestVersion string `json:"latestVersion"`
	// release 正文（FR-100 安全渲染）；check-failed 时为空
	ReleaseNotes string `json:"releaseNotes"`
	// release 页面 URL；check-failed 时为空
	ReleaseURL string `json:"releaseUrl"`
	// 发布时间（RFC3339 原样透传）；check-failed 时为空
	PublishedAt string `json:"publishedAt"`
	// 本次结果的检查时间（UTC RFC3339）
	CheckedAt string `json:"checkedAt"`
	// 缓存到期时间（UTC RFC3339）：到点后下次检查会重新打 GitHub
	CacheExpiresAt string `json:"cacheExpiresAt"`
}

// cachedCheck 是一次成功 / 失败检查结果的内存缓存（含到期时刻 + 检查时刻 + 检查所用渠道）。
type cachedCheck struct {
	view      UpdateCheckView
	channel   string
	expiresAt time.Time
}

// UpdateService 编排控制面在线更新的 HTTP 触发面（FR-99，见 ADR-0044）：
// 把更新核心（internal/update）接到端点，从设置 store 读渠道 / 代理 / 缓存 TTL（FR-101），
// 检查结果带进程内内存缓存（命中不重复打 GitHub、force 绕缓存刷新）。
// 渠道变更或缓存过期即失效；GitHub 不可达降级为 check-failed（不报 5xx）。
type UpdateService struct {
	core     updateCore
	settings updateSettingsReader
	// now 返回当前时刻（可注入便于单测控制缓存到期）；默认 time.Now。
	now func() time.Time

	mu     sync.Mutex
	cached *cachedCheck
}

// NewUpdateService 构造服务（core=更新核心，settings=设置 store 读口）。
func NewUpdateService(core updateCore, settings updateSettingsReader) *UpdateService {
	return &UpdateService{core: core, settings: settings, now: time.Now}
}

// Check 执行 / 复用一次更新检查（FR-99）：
//   - 非 force 且缓存未过期、渠道未变 → 直接返回缓存（不打 GitHub）；
//   - force 或缓存失效 → 调更新核心查 release，结果与失败均缓存（TTL 取 store 的检查周期小时）；
//   - GitHub 不可达 / 解析失败 → 返回 status=check-failed 视图（无 error、由 handler 以 200 回，不阻断页面）。
//
// 缓存与渠道读取均在本服务，handler 不碰 store、不构造 http.Client。
func (s *UpdateService) Check(ctx context.Context, force bool, operator, clientIP string) UpdateCheckView {
	channel := s.settings.GetString(SettingUpdateChannel)
	proxyURL := s.settings.GetString(SettingUpdateProxyURL)

	now := s.now().UTC()
	s.mu.Lock()
	if !force && s.cached != nil && s.cached.channel == channel && now.Before(s.cached.expiresAt) {
		view := s.cached.view
		s.mu.Unlock()
		return view
	}
	s.mu.Unlock()

	ttl := s.cacheTTL()
	expiresAt := now.Add(ttl)
	res, err := s.core.CheckForUpdate(ctx, update.Channel(channel), proxyURL, operator, clientIP)

	var view UpdateCheckView
	if err != nil {
		// GitHub 不可达 / 限流 / 解析失败：降级为 check-failed，不报 5xx、不阻断页面（FR-99）。
		view = UpdateCheckView{
			Status:         updateCheckStatusFailed,
			CurrentVersion: s.currentVersionFor(res),
			Channel:        channel,
			CheckedAt:      now.Format(time.RFC3339),
			CacheExpiresAt: expiresAt.Format(time.RFC3339),
		}
	} else {
		view = UpdateCheckView{
			Status:         updateCheckStatusOK,
			CurrentVersion: res.CurrentVersion,
			Channel:        channel,
			HasUpdate:      res.HasUpdate,
			IsDevBuild:     res.IsDevBuild,
			LatestVersion:  res.LatestVersion,
			ReleaseNotes:   res.ReleaseNotes,
			ReleaseURL:     res.ReleaseURL,
			PublishedAt:    res.PublishedAt,
			CheckedAt:      now.Format(time.RFC3339),
			CacheExpiresAt: expiresAt.Format(time.RFC3339),
		}
	}

	s.mu.Lock()
	s.cached = &cachedCheck{view: view, channel: channel, expiresAt: expiresAt}
	s.mu.Unlock()
	return view
}

// Status 返回当前更新进度快照（读内存态，不查库、不打 GitHub）。
func (s *UpdateService) Status() update.Progress {
	return s.core.Snapshot()
}

// Apply 触发一次应用更新（FR-99）：从 store 读渠道 / 代理后调更新核心，落位成功即请求重启。
// 只读拒写 + 审计由上层中间件保证（写方法、复用 system.update-apply 审计）。
func (s *UpdateService) Apply(ctx context.Context, operator, clientIP string) error {
	channel := s.settings.GetString(SettingUpdateChannel)
	proxyURL := s.settings.GetString(SettingUpdateProxyURL)
	return s.core.ApplyUpdate(ctx, update.Channel(channel), proxyURL, operator, clientIP)
}

// cacheTTL 取检查结果缓存时长：store 的 update.check-interval-hours（小时）转 Duration。
func (s *UpdateService) cacheTTL() time.Duration {
	return time.Duration(s.settings.GetInt(SettingUpdateCheckIntervalHours)) * time.Hour
}

// currentVersionFor 在 check-failed 时尽量回显当前版本：核心已填则用之（部分失败前已知），否则留空。
func (s *UpdateService) currentVersionFor(res update.CheckResult) string {
	return res.CurrentVersion
}

// 更新检查状态取值（FR-99）。
const (
	updateCheckStatusOK     = "ok"
	updateCheckStatusFailed = "check-failed"
)
