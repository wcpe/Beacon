package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/update"
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
	// FR-124 代理测试注入错误（nil=连通）。
	testProxyErr error
	// fix-1 异步同步钩子：applyStarted 非空时 ApplyUpdate 进入后（写完字段）发信号；
	// applyBlock 非空时阻塞在其上直至测试放行——用于断言 apply 异步不阻塞 + 并发守卫。
	applyStarted chan struct{}
	applyBlock   chan struct{}
	// fix-b 可取消钩子：applyWaitCtx 为真时 ApplyUpdate 阻塞直到 ctx 取消；applyDone 非空时返回前关闭——
	// 用于断言 CancelApply 真能取消进行中的下载 ctx。
	applyWaitCtx bool
	applyDone    chan struct{}
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

func (f *fakeUpdateCore) ApplyUpdate(ctx context.Context, ch update.Channel, proxyURL, operator, clientIP string) error {
	f.applyCalls++
	f.lastChannel = ch
	f.lastProxy = proxyURL
	f.lastOperator = operator
	f.lastClientIP = clientIP
	// 字段写毕再发 started 信号（建立 happens-before，测试 <-applyStarted 后读字段无竞态）。
	if f.applyStarted != nil {
		f.applyStarted <- struct{}{}
	}
	if f.applyWaitCtx {
		<-ctx.Done() // 阻塞直到 ctx 取消（验 CancelApply / 关停取消下载）
	}
	if f.applyBlock != nil {
		<-f.applyBlock
	}
	if f.applyDone != nil {
		close(f.applyDone)
	}
	return f.applyErr
}

func (f *fakeUpdateCore) Snapshot() update.Progress { return f.snap }

func (f *fakeUpdateCore) TestProxy(_ context.Context, proxyURL string) error {
	f.lastProxy = proxyURL
	return f.testProxyErr
}

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

// TestCheckNormalizesLegacyPrereleaseChannel 历史 prerelease 设置被防御性归一为 stable，并且响应与核心参数均不得再暴露 prerelease。
func TestCheckNormalizesLegacyPrereleaseChannel(t *testing.T) {
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
	if v.Channel != "stable" {
		t.Fatalf("历史渠道应归一为 stable，实际 %q", v.Channel)
	}
	if core.lastChannel != update.ChannelStable || core.lastProxy != "http://p:8080" {
		t.Fatalf("核心必须只收到 stable / 原代理，实际 ch=%q proxy=%q", core.lastChannel, core.lastProxy)
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

// TestCheckLegacyChannelUsesStableCache 历史 prerelease 与 stable 是同一规范化渠道，不得制造第二条缓存或再次请求远端。
func TestCheckLegacyChannelUsesStableCache(t *testing.T) {
	core := &fakeUpdateCore{result: update.CheckResult{CurrentVersion: "v1.0.0", LatestVersion: "v1.0.0"}}
	settings := &fakeSettingsReader{channel: "stable", intervalHours: 6}
	fixed := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	svc := newTestUpdateService(core, settings, func() time.Time { return fixed })

	_ = svc.Check(context.Background(), false, "a", "")
	settings.channel = "prerelease"
	_ = svc.Check(context.Background(), false, "a", "")
	if core.checkCalls != 1 {
		t.Fatalf("归一化后应复用 stable 缓存，实际请求 %d 次", core.checkCalls)
	}
}

// TestCheckFailedDegradesNot5xx GitHub 不可达 → status=check-failed、无 panic、对外不报错（由 handler 200 回）。
func TestCheckFailedDegradesNot5xx(t *testing.T) {
	core := &fakeUpdateCore{checkErr: errors.New("代理请求失败: http://admin:s3cr3t@10.0.0.5:7890 token=abc123")}
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
	wantReason := "代理请求失败: http://***:***@10.0.0.5:7890 token=***"
	if v.FailureReason != wantReason {
		t.Fatalf("check-failed 应返回脱敏原因 %q，实际 %q", wantReason, v.FailureReason)
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

// TestProxyTestUsesStoreProxy 代理测试（FR-124）：从 store 读 update.proxy-url 透传给核心；连通返回 nil、不通返回错误。
func TestProxyTestUsesStoreProxy(t *testing.T) {
	core := &fakeUpdateCore{testProxyErr: errors.New("连接 GitHub 失败")}
	svc := NewUpdateService(core, &fakeSettingsReader{proxy: "http://p:9090"})

	if err := svc.TestProxy(context.Background()); err == nil {
		t.Fatal("代理不通应返回错误")
	}
	if core.lastProxy != "http://p:9090" {
		t.Fatalf("应把 store 代理透传给核心，实际 %q", core.lastProxy)
	}

	core.testProxyErr = nil
	if err := svc.TestProxy(context.Background()); err != nil {
		t.Fatalf("代理连通应返回 nil，实际 %v", err)
	}
}

// TestApplyNormalizesLegacyChannel 触发应用时也必须把历史 prerelease 归一为 stable，避免下载 RC 或开发对象。
func TestApplyNormalizesLegacyChannel(t *testing.T) {
	core := &fakeUpdateCore{applyStarted: make(chan struct{}, 1)}
	settings := &fakeSettingsReader{channel: "prerelease", proxy: "http://p:9090"}
	svc := NewUpdateService(core, settings)

	if err := svc.Apply("tester", "5.6.7.8"); err != nil {
		t.Fatalf("apply 不应返回错误：%v", err)
	}
	<-core.applyStarted // 等后台 goroutine 进入核心（字段已写毕）
	if core.applyCalls != 1 {
		t.Fatalf("应调一次核心 ApplyUpdate，实际 %d", core.applyCalls)
	}
	if core.lastChannel != update.ChannelStable || core.lastProxy != "http://p:9090" {
		t.Fatalf("应用更新必须只传 stable / 原代理，实际 ch=%q proxy=%q", core.lastChannel, core.lastProxy)
	}
}

// TestApplyIsAsyncAndGuardsConcurrency fix-1 复现/回归：apply 受理后立即返回（异步不阻塞），
// 进行中再触发被并发守卫拒绝（409 ErrUpdateInProgress）。原同步实现会把整段下载压在调用内、
// 且无并发守卫——此测试锁定异步 + 守卫语义。
func TestApplyIsAsyncAndGuardsConcurrency(t *testing.T) {
	core := &fakeUpdateCore{
		applyStarted: make(chan struct{}, 1),
		applyBlock:   make(chan struct{}),
	}
	svc := NewUpdateService(core, &fakeSettingsReader{channel: "stable"})

	// 首次触发：异步受理，立即返回（若仍同步阻塞，此调用会卡在核心的 applyBlock 上不返回）。
	if err := svc.Apply("a", ""); err != nil {
		t.Fatalf("首次 Apply 应立即受理返回 nil，实际 %v", err)
	}
	<-core.applyStarted // 确认后台已进核心并阻塞在 applyBlock

	// 进行中再触发：并发守卫返回 409 ErrUpdateInProgress（不再开第二次下载）。
	if err := svc.Apply("b", ""); !errors.Is(err, apperr.ErrUpdateInProgress) {
		t.Fatalf("进行中再触发应返回 ErrUpdateInProgress，实际 %v", err)
	}

	close(core.applyBlock) // 放行首次后台完成（避免 goroutine 泄漏）
}

// TestCancelApplyCancelsRunningApply fix-b/FR-125：CancelApply 取消进行中更新的 context，使下载中断；
// 无进行中时返回 false。也是 fix-b「Ctrl+C/关停取消下载」的可取消基建回归。
func TestCancelApplyCancelsRunningApply(t *testing.T) {
	core := &fakeUpdateCore{
		applyStarted: make(chan struct{}, 1),
		applyWaitCtx: true, // 核心阻塞直到 ctx 取消
		applyDone:    make(chan struct{}),
	}
	svc := NewUpdateService(core, &fakeSettingsReader{channel: "stable"})

	// 无进行中：CancelApply 返回 false。
	if svc.CancelApply() {
		t.Fatal("无进行中更新时 CancelApply 应返回 false")
	}

	if err := svc.Apply("a", ""); err != nil {
		t.Fatalf("Apply 应受理返回 nil，实际 %v", err)
	}
	<-core.applyStarted // 后台已进核心、阻塞在 ctx 上

	// 进行中：CancelApply 返回 true 并取消核心 ctx → 核心从 <-ctx.Done() 解阻塞返回。
	if !svc.CancelApply() {
		t.Fatal("进行中更新时 CancelApply 应返回 true")
	}
	select {
	case <-core.applyDone:
	case <-time.After(2 * time.Second):
		t.Fatal("CancelApply 后核心未因 ctx 取消而返回（下载未被取消）")
	}
}
