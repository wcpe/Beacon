package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/update"
)

// fakeUpdateCore 是更新核心的测试假实现：记 CheckForUpdate 调用次数 + 收到的渠道 / 代理，
// 可注入固定结果或错误（驱动 check-failed 降级），并持一个可控的进度快照。
type fakeUpdateCore struct {
	checkCalls   int
	applyCalls   int
	lastChannel  update.Channel
	lastProxy    string
	lastOperator string
	lastClientIP string
	result       update.CheckResult
	checkErr     error
	applyErr     error
	snap         update.Progress
	// 回滚（FR-120）：可控可用性 + 调用计数 + 注入错误。
	rollbackAvailable bool
	rollbackCalls     int
	rollbackErr       error
}

func (f *fakeUpdateCore) CheckForUpdate(_ context.Context, ch update.Channel, proxyURL, operator, clientIP string) (update.CheckResult, error) {
	f.checkCalls++
	f.lastChannel = ch
	f.lastProxy = proxyURL
	f.lastOperator = operator
	f.lastClientIP = clientIP
	if f.checkErr != nil {
		return update.CheckResult{}, f.checkErr
	}
	return f.result, nil
}

func (f *fakeUpdateCore) ApplyUpdate(_ context.Context, ch update.Channel, proxyURL, operator, clientIP string) error {
	f.applyCalls++
	f.lastChannel = ch
	f.lastProxy = proxyURL
	f.lastOperator = operator
	f.lastClientIP = clientIP
	return f.applyErr
}

func (f *fakeUpdateCore) Snapshot() update.Progress { return f.snap }

func (f *fakeUpdateCore) RollbackAvailable() bool { return f.rollbackAvailable }

func (f *fakeUpdateCore) Rollback(operator, clientIP string) error {
	f.rollbackCalls++
	f.lastOperator = operator
	f.lastClientIP = clientIP
	return f.rollbackErr
}

// TestRollbackUnavailableReturns409 无 .old：服务返回 ErrNoRollbackAvailable（409），不调核心 Rollback（FR-120）。
func TestRollbackUnavailableReturns409(t *testing.T) {
	core := &fakeUpdateCore{rollbackAvailable: false}
	svc := NewUpdateService(core, &fakeSettingsReader{})
	if err := svc.Rollback("tester", "1.2.3.4"); !errors.Is(err, apperr.ErrNoRollbackAvailable) {
		t.Fatalf("无 .old 应返回 ErrNoRollbackAvailable，实际 %v", err)
	}
	if core.rollbackCalls != 0 {
		t.Fatalf("无 .old 不应调核心 Rollback，实际 calls=%d", core.rollbackCalls)
	}
}

// TestRollbackAvailableForwardsToCore 有 .old：转发核心 Rollback（FR-120）。
func TestRollbackAvailableForwardsToCore(t *testing.T) {
	core := &fakeUpdateCore{rollbackAvailable: true}
	svc := NewUpdateService(core, &fakeSettingsReader{})
	if err := svc.Rollback("tester", "1.2.3.4"); err != nil {
		t.Fatalf("有 .old 应成功: %v", err)
	}
	if core.rollbackCalls != 1 {
		t.Fatalf("应转发核心 Rollback 1 次，实际 calls=%d", core.rollbackCalls)
	}
}

// fakeSettingsReader 是设置 store 的测试假读口：渠道 / 代理 / 检查周期可调。
type fakeSettingsReader struct {
	channel       string
	proxy         string
	intervalHours int
}

func (f *fakeSettingsReader) GetString(key string) string {
	switch key {
	case SettingUpdateChannel:
		return f.channel
	case SettingUpdateProxyURL:
		return f.proxy
	default:
		return ""
	}
}

func (f *fakeSettingsReader) GetInt(key string) int {
	if key == SettingUpdateCheckIntervalHours {
		return f.intervalHours
	}
	return 0
}

// newTestUpdateService 构造服务并注入可控时钟（便于测缓存到期）。
func newTestUpdateService(core *fakeUpdateCore, settings *fakeSettingsReader, now func() time.Time) *UpdateService {
	s := NewUpdateService(core, settings)
	s.now = now
	return s
}

// TestCheckReportsNewerFromStoreChannel 检查按 store 渠道走、报有更新、回填字段。
func TestCheckReportsNewerFromStoreChannel(t *testing.T) {
	core := &fakeUpdateCore{result: update.CheckResult{
		CurrentVersion: "v1.0.0", LatestVersion: "v2.0.0", HasUpdate: true,
		ReleaseNotes: "说明", ReleaseURL: "https://x/r", PublishedAt: "2026-06-20T00:00:00Z",
	}}
	settings := &fakeSettingsReader{channel: "prerelease", proxy: "http://p:8080", intervalHours: 6}
	svc := newTestUpdateService(core, settings, time.Now)

	v := svc.Check(context.Background(), false, "tester", "1.2.3.4")
	if v.Status != "ok" {
		t.Fatalf("应为 ok，实际 %q", v.Status)
	}
	if !v.HasUpdate || v.LatestVersion != "v2.0.0" {
		t.Fatalf("应报有更新且最新 v2.0.0，实际 %+v", v)
	}
	if v.Channel != "prerelease" {
		t.Fatalf("渠道应取 store 的 prerelease，实际 %q", v.Channel)
	}
	if core.lastChannel != update.Channel("prerelease") || core.lastProxy != "http://p:8080" {
		t.Fatalf("应把 store 渠道 / 代理透传给核心，实际 ch=%q proxy=%q", core.lastChannel, core.lastProxy)
	}
	if core.lastOperator != "tester" || core.lastClientIP != "1.2.3.4" {
		t.Fatalf("应把 operator/clientIP 透传，实际 op=%q ip=%q", core.lastOperator, core.lastClientIP)
	}
	if v.CheckedAt == "" || v.CacheExpiresAt == "" {
		t.Fatalf("应回填检查时间与缓存到期时间，实际 %+v", v)
	}
}

// TestCheckCacheHitSkipsSecondCall 缓存未过期、渠道未变 → 第二次不打 GitHub（核心仅被调一次）。
func TestCheckCacheHitSkipsSecondCall(t *testing.T) {
	core := &fakeUpdateCore{result: update.CheckResult{CurrentVersion: "v1.0.0", LatestVersion: "v1.0.0"}}
	settings := &fakeSettingsReader{channel: "stable", intervalHours: 6}
	fixed := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	svc := newTestUpdateService(core, settings, func() time.Time { return fixed })

	_ = svc.Check(context.Background(), false, "a", "")
	_ = svc.Check(context.Background(), false, "a", "")
	if core.checkCalls != 1 {
		t.Fatalf("缓存命中应只打一次 GitHub，实际 %d 次", core.checkCalls)
	}
}

// TestCheckForceBypassesCache force=true 绕缓存刷新（核心被再次调用）。
func TestCheckForceBypassesCache(t *testing.T) {
	core := &fakeUpdateCore{result: update.CheckResult{CurrentVersion: "v1.0.0", LatestVersion: "v1.0.0"}}
	settings := &fakeSettingsReader{channel: "stable", intervalHours: 6}
	fixed := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	svc := newTestUpdateService(core, settings, func() time.Time { return fixed })

	_ = svc.Check(context.Background(), false, "a", "")
	_ = svc.Check(context.Background(), true, "a", "") // force：即便缓存新鲜也刷
	if core.checkCalls != 2 {
		t.Fatalf("force 应绕缓存再打一次，实际 %d 次", core.checkCalls)
	}
}

// TestCheckCacheExpiresByTTL 缓存按 TTL（检查周期小时）过期后重新打 GitHub。
func TestCheckCacheExpiresByTTL(t *testing.T) {
	core := &fakeUpdateCore{result: update.CheckResult{CurrentVersion: "v1.0.0", LatestVersion: "v1.0.0"}}
	settings := &fakeSettingsReader{channel: "stable", intervalHours: 6}
	cur := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	svc := newTestUpdateService(core, settings, func() time.Time { return cur })

	_ = svc.Check(context.Background(), false, "a", "")
	cur = cur.Add(7 * time.Hour) // 超过 6h TTL
	_ = svc.Check(context.Background(), false, "a", "")
	if core.checkCalls != 2 {
		t.Fatalf("缓存过期应重新打 GitHub，实际 %d 次", core.checkCalls)
	}
}

// TestCheckChannelChangeInvalidatesCache 渠道变更使缓存失效（重新打 GitHub）。
func TestCheckChannelChangeInvalidatesCache(t *testing.T) {
	core := &fakeUpdateCore{result: update.CheckResult{CurrentVersion: "v1.0.0", LatestVersion: "v1.0.0"}}
	settings := &fakeSettingsReader{channel: "stable", intervalHours: 6}
	fixed := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	svc := newTestUpdateService(core, settings, func() time.Time { return fixed })

	_ = svc.Check(context.Background(), false, "a", "")
	settings.channel = "prerelease" // 渠道改了
	_ = svc.Check(context.Background(), false, "a", "")
	if core.checkCalls != 2 {
		t.Fatalf("渠道变更应使缓存失效、重新打 GitHub，实际 %d 次", core.checkCalls)
	}
}

// TestCheckFailedDegradesNot5xx GitHub 不可达 → status=check-failed、无 panic、对外不报错（由 handler 200 回）。
func TestCheckFailedDegradesNot5xx(t *testing.T) {
	core := &fakeUpdateCore{checkErr: errors.New("dial tcp: connection refused")}
	settings := &fakeSettingsReader{channel: "stable", intervalHours: 6}
	svc := newTestUpdateService(core, settings, time.Now)

	v := svc.Check(context.Background(), false, "a", "")
	if v.Status != "check-failed" {
		t.Fatalf("GitHub 不可达应降级 check-failed，实际 %q", v.Status)
	}
	if v.HasUpdate {
		t.Fatal("check-failed 不应报有更新")
	}
	if v.Channel != "stable" {
		t.Fatalf("check-failed 仍应回显渠道，实际 %q", v.Channel)
	}
}

// TestCheckDevBuildNotPrompted dev 构建：核心返回 IsDevBuild=true、hasUpdate=false → 视图据实标记不提示。
func TestCheckDevBuildNotPrompted(t *testing.T) {
	core := &fakeUpdateCore{result: update.CheckResult{
		CurrentVersion: "dev", LatestVersion: "v2.0.0", HasUpdate: false, IsDevBuild: true,
	}}
	settings := &fakeSettingsReader{channel: "stable", intervalHours: 6}
	svc := newTestUpdateService(core, settings, time.Now)

	v := svc.Check(context.Background(), false, "a", "")
	if !v.IsDevBuild {
		t.Fatal("dev 构建应标 isDevBuild=true")
	}
	if v.HasUpdate {
		t.Fatal("dev 构建不应提示更新")
	}
}

// TestStatusReadsSnapshot 状态端点读内存进度态（不打 GitHub）。
func TestStatusReadsSnapshot(t *testing.T) {
	core := &fakeUpdateCore{snap: update.Progress{Phase: update.PhaseDownloading, Percent: 42, TargetVersion: "v2.0.0"}}
	svc := NewUpdateService(core, &fakeSettingsReader{})
	p := svc.Status()
	if p.Phase != update.PhaseDownloading || p.Percent != 42 {
		t.Fatalf("应原样返回内存进度，实际 %+v", p)
	}
	if core.checkCalls != 0 {
		t.Fatal("状态端点不应打 GitHub")
	}
}

// TestApplyUsesStoreChannelAndProxy 触发应用：从 store 读渠道 / 代理透传给核心。
func TestApplyUsesStoreChannelAndProxy(t *testing.T) {
	core := &fakeUpdateCore{}
	settings := &fakeSettingsReader{channel: "prerelease", proxy: "http://p:9090"}
	svc := NewUpdateService(core, settings)

	if err := svc.Apply(context.Background(), "tester", "5.6.7.8"); err != nil {
		t.Fatalf("apply 不应返回错误：%v", err)
	}
	if core.applyCalls != 1 {
		t.Fatalf("应调一次核心 ApplyUpdate，实际 %d", core.applyCalls)
	}
	if core.lastChannel != update.Channel("prerelease") || core.lastProxy != "http://p:9090" {
		t.Fatalf("应把 store 渠道 / 代理透传，实际 ch=%q proxy=%q", core.lastChannel, core.lastProxy)
	}
}

// TestApplyPropagatesError 核心失败原样上抛（handler 据此回错误体、进程不退由核心保证）。
func TestApplyPropagatesError(t *testing.T) {
	core := &fakeUpdateCore{applyErr: errors.New("校验不通过")}
	svc := NewUpdateService(core, &fakeSettingsReader{channel: "stable"})
	if err := svc.Apply(context.Background(), "a", ""); err == nil {
		t.Fatal("核心失败应上抛错误")
	}
}
