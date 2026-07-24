package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// fakeAudit 收集写入的审计记录供断言。
type fakeAudit struct {
	entries []*model.AuditLog
}

func (f *fakeAudit) Create(e *model.AuditLog) error {
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeAudit) actions() []string {
	var as []string
	for _, e := range f.entries {
		as = append(as, e.Action)
	}
	return as
}

// directClient 是测试用直连 client 工厂（忽略代理，超时短）。
func directClient(_ string, timeout time.Duration) (*http.Client, error) {
	return &http.Client{Timeout: timeout}, nil
}

// roundTripFunc 把函数适配为测试用 HTTP 传输器。
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// newMockReleaseServer 起一个 mock，提供 /repos/<repo>/releases、二进制资产、SHA256SUMS.txt。
// binContent 为「服务端持有的新二进制内容」，sumsContent 由调用方决定（可故意写错以测校验失败）。
func newMockReleaseServer(t *testing.T, tag, binName, binContent, sumsContent string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)

	mux.HandleFunc("/repos/wcpe/Beacon/releases", func(w http.ResponseWriter, _ *http.Request) {
		releases := []ghRelease{{
			TagName:    tag,
			Prerelease: false,
			Body:       "测试 release 说明",
			HTMLURL:    "https://example.invalid/release",
			Assets: []ghAsset{
				{Name: binName, URL: srv.URL + "/dl/" + binName},
				{Name: "SHA256SUMS.txt", URL: srv.URL + "/dl/SHA256SUMS.txt"},
			},
		}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(releases)
	})
	mux.HandleFunc("/dl/"+binName, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(binContent))
	})
	mux.HandleFunc("/dl/SHA256SUMS.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sumsContent))
	})
	t.Cleanup(srv.Close)
	return srv
}

// sha256hex 算内容的十六进制 SHA256。
func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// currentAssetName 取本测试平台的资产名（与生产 assetName 同口径）。
func currentAssetName(t *testing.T, tag string) string {
	t.Helper()
	name, ok := assetName(tag)
	if !ok {
		t.Skipf("本平台 %s/%s 非已发布平台，跳过资产相关用例", runtime.GOOS, runtime.GOARCH)
	}
	return name
}

// TestAssetNameSupportsOnlyReleasePlatforms 锁定 1.0.0 的四平台资产矩阵，防止 Darwin amd64 回流。
func TestAssetNameSupportsOnlyReleasePlatforms(t *testing.T) {
	cases := []struct {
		name     string
		goos     string
		goarch   string
		wantName string
		wantOK   bool
	}{
		{name: "Linux amd64", goos: "linux", goarch: "amd64", wantName: "beacon-1.0.0-linux-amd64", wantOK: true},
		{name: "Linux arm64", goos: "linux", goarch: "arm64", wantName: "beacon-1.0.0-linux-arm64", wantOK: true},
		{name: "Windows amd64", goos: "windows", goarch: "amd64", wantName: "beacon-1.0.0-windows-amd64.exe", wantOK: true},
		{name: "Darwin arm64", goos: "darwin", goarch: "arm64", wantName: "beacon-1.0.0-darwin-arm64", wantOK: true},
		{name: "Darwin amd64 已移除", goos: "darwin", goarch: "amd64", wantOK: false},
		{name: "Windows arm64 未支持", goos: "windows", goarch: "arm64", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotOK := assetNameFor("v1.0.0", tc.goos, tc.goarch)
			if gotOK != tc.wantOK {
				t.Fatalf("支持状态错误：got=%v want=%v", gotOK, tc.wantOK)
			}
			if gotName != tc.wantName {
				t.Fatalf("资产名错误：got=%q want=%q", gotName, tc.wantName)
			}
		})
	}
}

// TestApplyUpdateHappyPath 完整链路：查 → 下载 → 校验 → 落位 → 请求重启。
func TestApplyUpdateHappyPath(t *testing.T) {
	const tag = "v9.9.9"
	binName := currentAssetName(t, tag)
	const binContent = "新二进制内容-假"
	sums := fmt.Sprintf("%s  %s\n", sha256hex(binContent), binName)
	srv := newMockReleaseServer(t, tag, binName, binContent, sums)

	dir := t.TempDir()
	pending := filepath.Join(dir, "beacon.new")
	audit := &fakeAudit{}
	restarted := false

	svc := NewService(Config{
		CurrentVersion: "1.0.0",
		APIBase:        srv.URL,
		PendingPath:    pending,
		NewHTTPClient:  directClient,
		RequestRestart: func() { restarted = true },
		Audit:          audit,
	})

	if err := svc.ApplyUpdate(context.Background(), ChannelStable, "", "tester", "1.2.3.4"); err != nil {
		t.Fatalf("ApplyUpdate 应成功，实际 %v", err)
	}
	// pending 已落位且内容正确。
	got, err := os.ReadFile(pending)
	if err != nil {
		t.Fatalf("读 pending 失败: %v", err)
	}
	if string(got) != binContent {
		t.Fatalf("pending 内容不符：%q", string(got))
	}
	if !restarted {
		t.Fatal("落位成功后应回调 requestRestart")
	}
	if snap := svc.Snapshot(); snap.Phase != PhaseReadyRestart {
		t.Fatalf("进度应为 ready-restart，实际 %q", snap.Phase)
	}
	// 审计应含 apply（不含 failed）。
	if !contains(audit.actions(), model.ActionSystemUpdateApply) {
		t.Fatalf("应记 update-apply 审计，实际 %v", audit.actions())
	}
	if contains(audit.actions(), model.ActionSystemUpdateFailed) {
		t.Fatalf("成功路径不应记 update-failed，实际 %v", audit.actions())
	}
	// 临时文件不应残留（已 rename 走）。
	assertNoTempLeak(t, dir)
}

// TestApplyUpdateChecksumMismatch 校验失败：中止、删临时文件、不落位、状态 failed、进程不退（返回错误）。
func TestApplyUpdateChecksumMismatch(t *testing.T) {
	const tag = "v9.9.9"
	binName := currentAssetName(t, tag)
	// SHA256SUMS 写一个错误哈希，触发校验不通过。
	sums := fmt.Sprintf("%s  %s\n", sha256hex("别的内容"), binName)
	srv := newMockReleaseServer(t, tag, binName, "真实下载内容", sums)

	dir := t.TempDir()
	pending := filepath.Join(dir, "beacon.new")
	audit := &fakeAudit{}
	restarted := false

	svc := NewService(Config{
		CurrentVersion: "1.0.0",
		APIBase:        srv.URL,
		PendingPath:    pending,
		NewHTTPClient:  directClient,
		RequestRestart: func() { restarted = true },
		Audit:          audit,
	})

	err := svc.ApplyUpdate(context.Background(), ChannelStable, "", "tester", "")
	if err == nil {
		t.Fatal("校验不通过应返回错误")
	}
	if _, statErr := os.Stat(pending); !os.IsNotExist(statErr) {
		t.Fatal("校验失败不应落位 pending")
	}
	if restarted {
		t.Fatal("校验失败不应请求重启")
	}
	if snap := svc.Snapshot(); snap.Phase != PhaseFailed {
		t.Fatalf("进度应为 failed，实际 %q", snap.Phase)
	}
	if !contains(audit.actions(), model.ActionSystemUpdateFailed) {
		t.Fatalf("应记 update-failed 审计，实际 %v", audit.actions())
	}
	assertNoTempLeak(t, dir)
}

// TestApplyUpdateCanceled ctx 取消（运维停止下载 / 关停，FR-125）按「已取消」处理：
// 进度回 idle（非 failed）+ 记 system.update-cancel 审计、不记 update-failed。
func TestApplyUpdateCanceled(t *testing.T) {
	const tag = "v9.9.9"
	binName := currentAssetName(t, tag)
	sums := fmt.Sprintf("%s  %s\n", sha256hex("内容"), binName)
	srv := newMockReleaseServer(t, tag, binName, "内容", sums)

	dir := t.TempDir()
	audit := &fakeAudit{}
	svc := NewService(Config{
		CurrentVersion: "1.0.0",
		APIBase:        srv.URL,
		PendingPath:    filepath.Join(dir, "beacon.new"),
		NewHTTPClient:  directClient,
		RequestRestart: func() {},
		Audit:          audit,
	})

	// 预取消的 context：首个出站请求即 ctx.Canceled。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.ApplyUpdate(ctx, ChannelStable, "", "tester", ""); err == nil {
		t.Fatal("取消应返回错误")
	}
	if snap := svc.Snapshot(); snap.Phase != PhaseIdle {
		t.Fatalf("取消后进度应为 idle（非 failed），实际 %q", snap.Phase)
	}
	if !contains(audit.actions(), model.ActionSystemUpdateCancel) {
		t.Fatalf("应记 update-cancel 审计，实际 %v", audit.actions())
	}
	if contains(audit.actions(), model.ActionSystemUpdateFailed) {
		t.Fatalf("取消不应记 update-failed 审计，实际 %v", audit.actions())
	}
	assertNoTempLeak(t, dir)
}

// TestApplyUpdateNoNewerVersion 远端不高于当前：不下载、不落位、failed 带原因。
func TestApplyUpdateNoNewerVersion(t *testing.T) {
	const tag = "v1.0.0"
	binName := currentAssetName(t, tag)
	sums := fmt.Sprintf("%s  %s\n", sha256hex("x"), binName)
	srv := newMockReleaseServer(t, tag, binName, "x", sums)

	dir := t.TempDir()
	pending := filepath.Join(dir, "beacon.new")
	audit := &fakeAudit{}

	svc := NewService(Config{
		CurrentVersion: "1.0.0", // 与远端相等 → 无更新
		APIBase:        srv.URL,
		PendingPath:    pending,
		NewHTTPClient:  directClient,
		RequestRestart: func() {},
		Audit:          audit,
	})

	if err := svc.ApplyUpdate(context.Background(), ChannelStable, "", "tester", ""); err == nil {
		t.Fatal("无更新时应返回错误（不静默落位）")
	}
	if _, statErr := os.Stat(pending); !os.IsNotExist(statErr) {
		t.Fatal("无更新不应落位")
	}
}

// TestApplyUpdateDownloadHTTPError 下载阶段 HTTP 失败（资产 URL 404）：中止、删临时文件、不落位、failed。
// （大小上限分支：maxBinaryBytes 为大常量、构造恰好超限响应不经济；其护栏逻辑由 downloadBinary 的 LimitReader+written 校验保证，
// 等价于校验失败路径已由 TestApplyUpdateChecksumMismatch 覆盖「内容异常即不落位」的总闸。）
func TestApplyUpdateDownloadHTTPError(t *testing.T) {
	const tag = "v9.9.9"
	binName := currentAssetName(t, tag)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/wcpe/Beacon/releases", func(w http.ResponseWriter, _ *http.Request) {
		releases := []ghRelease{{
			TagName: tag,
			Assets: []ghAsset{
				{Name: binName, URL: srv.URL + "/dl/missing"}, // 指向 404
				{Name: "SHA256SUMS.txt", URL: srv.URL + "/dl/sums"},
			},
		}}
		_ = json.NewEncoder(w).Encode(releases)
	})
	mux.HandleFunc("/dl/sums", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) })
	// /dl/missing 未注册 → 404

	dir := t.TempDir()
	pending := filepath.Join(dir, "beacon.new")
	svc := NewService(Config{
		CurrentVersion: "1.0.0", APIBase: srv.URL, PendingPath: pending,
		NewHTTPClient: directClient, RequestRestart: func() {}, Audit: &fakeAudit{},
	})
	if err := svc.ApplyUpdate(context.Background(), ChannelStable, "", "tester", ""); err == nil {
		t.Fatal("下载 404 应返回错误")
	}
	if _, statErr := os.Stat(pending); !os.IsNotExist(statErr) {
		t.Fatal("下载失败不应落位")
	}
	assertNoTempLeak(t, dir)
}

// TestApplyUpdateRedactsPresignedDownloadFailure 回归预签名下载 URL 泄露：
// GitHub 下载 URL 与对象存储跳转 URL 中的凭据不得进入返回错误、进度、审计或日志，非敏感诊断仍须保留。
func TestApplyUpdateRedactsPresignedDownloadFailure(t *testing.T) {
	const tag = "v9.9.9"
	binName := currentAssetName(t, tag)
	githubURL := "https://github.com/wcpe/Beacon/releases/download/v9.9.9/" + binName +
		"?token=github-token-raw&Password=github-password-raw&SECRET=github-secret-raw&API_KEY=github-api-key-raw&download=1"
	objectURL := "https://objects.githubusercontent.com/releases/download/v9.9.9/" + binName +
		"?X-Amz-Algorithm=AWS4-HMAC-SHA256&x-AmZ-SiGnAtUrE=amz-signature-raw&X-AMZ-CREDENTIAL=amz-credential-raw" +
		"&x-amz-security-token=amz-session-raw&response-content-disposition=attachment"

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/wcpe/Beacon/releases", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]ghRelease{{
			TagName: tag,
			Name:    tag,
			Assets: []ghAsset{
				{ID: 1, Name: binName, URL: githubURL},
				{ID: 2, Name: "SHA256SUMS.txt", URL: srv.URL + "/dl/sums"},
			},
		}})
	})

	audit := &fakeAudit{}
	var logBuf bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(originalLogger)

	svc := NewService(Config{
		CurrentVersion: "1.0.0",
		APIBase:        srv.URL,
		PendingPath:    filepath.Join(t.TempDir(), "beacon.new"),
		NewHTTPClient: func(_ string, timeout time.Duration) (*http.Client, error) {
			return &http.Client{
				Timeout: timeout,
				Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if req.URL.Host == "github.com" {
						return nil, fmt.Errorf("GitHub 跳转对象存储 %s 失败: %w", objectURL, errors.New("连接被重置"))
					}
					return http.DefaultTransport.RoundTrip(req)
				}),
			}, nil
		},
		RequestRestart: func() {},
		Audit:          audit,
	})

	err := svc.ApplyUpdate(context.Background(), ChannelStable, "", "tester", "1.2.3.4")
	if err == nil {
		t.Fatal("预签名下载失败应返回错误")
	}
	if len(audit.entries) == 0 {
		t.Fatal("下载失败应写入审计")
	}
	observations := map[string]string{
		"返回错误": err.Error(),
		"进度记录": svc.Snapshot().Error,
		"审计详情": audit.entries[len(audit.entries)-1].Detail,
		"日志输出": logBuf.String(),
	}
	assertPresignedFailureRedacted(t, observations)
}

// TestCheckForUpdateReportsNewer 检查端点：远端更高报有更新，记 check 审计。
func TestCheckForUpdateReportsNewer(t *testing.T) {
	const tag = "v2.0.0"
	binName := currentAssetName(t, tag)
	srv := newMockReleaseServer(t, tag, binName, "x", fmt.Sprintf("%s  %s\n", sha256hex("x"), binName))
	audit := &fakeAudit{}
	svc := NewService(Config{
		CurrentVersion: "1.0.0", APIBase: srv.URL, PendingPath: filepath.Join(t.TempDir(), "beacon.new"),
		NewHTTPClient: directClient, RequestRestart: func() {}, Audit: audit,
	})
	res, err := svc.CheckForUpdate(context.Background(), ChannelStable, "", "tester", "")
	if err != nil {
		t.Fatalf("检查应成功: %v", err)
	}
	if !res.HasUpdate {
		t.Fatal("远端 2.0.0 > 当前 1.0.0 应报有更新")
	}
	if res.LatestVersion != tag {
		t.Fatalf("最新版本应为 %s，实际 %s", tag, res.LatestVersion)
	}
	if !contains(audit.actions(), model.ActionSystemUpdateCheck) {
		t.Fatalf("应记 update-check 审计，实际 %v", audit.actions())
	}
}

// TestCheckForUpdateCarriesCurrentVersionOnError check-failed（查 release 失败）时仍回显当前版本。
// 修复前失败路径返回空 CheckResult，致前端更新模态框「当前版本」空白（真机暴露）。
func TestCheckForUpdateCarriesCurrentVersionOnError(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/wcpe/Beacon/releases", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // 模拟 GitHub 不可达 / 限流
	})
	svc := NewService(Config{
		CurrentVersion: "1.0.0", APIBase: srv.URL, PendingPath: filepath.Join(t.TempDir(), "beacon.new"),
		NewHTTPClient: directClient, RequestRestart: func() {}, Audit: &fakeAudit{},
	})
	res, err := svc.CheckForUpdate(context.Background(), ChannelStable, "", "tester", "")
	if err == nil {
		t.Fatal("releases 500 应返回错误（check-failed）")
	}
	if res.CurrentVersion != "1.0.0" {
		t.Fatalf("check-failed 时应回显当前版本 1.0.0，实际 %q", res.CurrentVersion)
	}
}

// TestCheckForUpdatePreservesDownloadProgress 检查是只读操作，不能覆盖正在下载的进度、目标版本或失败信息。
func TestCheckForUpdatePreservesDownloadProgress(t *testing.T) {
	const tag = "v2.0.0"
	binName := currentAssetName(t, tag)
	srv := newMockReleaseServer(t, tag, binName, "x", fmt.Sprintf("%s  %s\n", sha256hex("x"), binName))
	svc := NewService(Config{
		CurrentVersion: "1.0.0", APIBase: srv.URL, PendingPath: filepath.Join(t.TempDir(), "beacon.new"),
		NewHTTPClient: directClient, RequestRestart: func() {}, Audit: &fakeAudit{},
	})
	svc.progress.reset("v1.5.0")
	svc.progress.setPhase(PhaseDownloading, "v1.5.0")
	svc.progress.setPercent(42)
	before := svc.Snapshot()

	if _, err := svc.CheckForUpdate(context.Background(), ChannelStable, "", "tester", ""); err != nil {
		t.Fatalf("检查应成功: %v", err)
	}
	if after := svc.Snapshot(); after != before {
		t.Fatalf("检查不得写下载进度：before=%+v after=%+v", before, after)
	}
}

// TestCheckForUpdatePopulatesPublishedAt 检查结果回填 release 发布时间（FR-99 端点透传）。
func TestCheckForUpdatePopulatesPublishedAt(t *testing.T) {
	const tag = "v2.0.0"
	binName := currentAssetName(t, tag)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/wcpe/Beacon/releases", func(w http.ResponseWriter, _ *http.Request) {
		releases := []ghRelease{{
			TagName:     tag,
			Body:        "说明",
			HTMLURL:     "https://example.invalid/r",
			PublishedAt: "2026-06-20T08:00:00Z",
			Assets:      []ghAsset{{Name: binName, URL: srv.URL + "/dl/bin"}},
		}}
		_ = json.NewEncoder(w).Encode(releases)
	})
	svc := NewService(Config{
		CurrentVersion: "1.0.0", APIBase: srv.URL, PendingPath: filepath.Join(t.TempDir(), "beacon.new"),
		NewHTTPClient: directClient, RequestRestart: func() {}, Audit: &fakeAudit{},
	})
	res, err := svc.CheckForUpdate(context.Background(), ChannelStable, "", "tester", "")
	if err != nil {
		t.Fatalf("检查应成功: %v", err)
	}
	if res.PublishedAt != "2026-06-20T08:00:00Z" {
		t.Fatalf("应回填发布时间，实际 %q", res.PublishedAt)
	}
	if res.IsDevBuild {
		t.Fatal("1.0.0 非 dev 构建，IsDevBuild 应为 false")
	}
}

// TestCheckForUpdateDevBuildMarked dev 构建：标 IsDevBuild、不报有更新（不参与比较）。
func TestCheckForUpdateDevBuildMarked(t *testing.T) {
	const tag = "v2.0.0"
	binName := currentAssetName(t, tag)
	srv := newMockReleaseServer(t, tag, binName, "x", fmt.Sprintf("%s  %s\n", sha256hex("x"), binName))
	svc := NewService(Config{
		CurrentVersion: "dev", APIBase: srv.URL, PendingPath: filepath.Join(t.TempDir(), "beacon.new"),
		NewHTTPClient: directClient, RequestRestart: func() {}, Audit: &fakeAudit{},
	})
	res, err := svc.CheckForUpdate(context.Background(), ChannelStable, "", "tester", "")
	if err != nil {
		t.Fatalf("检查应成功: %v", err)
	}
	if !res.IsDevBuild {
		t.Fatal("dev 构建应标 IsDevBuild=true")
	}
	if res.HasUpdate {
		t.Fatal("dev 构建不应报有更新")
	}
}

// TestApplyUpdateRevalidatesGAReleaseBeforeDownload 首次选择 GA 后，下载前若公开列表只剩 RC，必须在下载前失败。
func TestApplyUpdateRevalidatesGAReleaseBeforeDownload(t *testing.T) {
	var calls int
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/wcpe/Beacon/releases", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			_ = json.NewEncoder(w).Encode([]ghRelease{{TagName: "v1.0.0", Name: "v1.0.0", Prerelease: false}})
			return
		}
		_ = json.NewEncoder(w).Encode([]ghRelease{{TagName: "v1.0.1-rc.1", Name: "v1.0.1-rc.1", Prerelease: true}})
	})

	svc := NewService(Config{
		CurrentVersion: "0.29.0", APIBase: srv.URL, PendingPath: filepath.Join(t.TempDir(), "beacon.new"),
		NewHTTPClient: directClient, RequestRestart: func() {}, Audit: &fakeAudit{},
	})
	err := svc.ApplyUpdate(context.Background(), ChannelStable, "", "tester", "")
	if err == nil || !strings.Contains(err.Error(), "下载前复验 GA release 失败") {
		t.Fatalf("下载前 GA 漂移应中止，实际错误：%v", err)
	}
	if calls != 2 {
		t.Fatalf("下载前应查询两次，实际 %d 次", calls)
	}
}

// TestApplyUpdateRejectsChangedGAAssetBeforeDownload 二次查询同 tag 但目标资产被替换时必须中止。
func TestApplyUpdateRejectsChangedGAAssetBeforeDownload(t *testing.T) {
	binName := currentAssetName(t, "v1.0.0")
	var calls int
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/wcpe/Beacon/releases", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		assetID := int64(1)
		if calls == 2 {
			assetID = 2
		}
		_ = json.NewEncoder(w).Encode([]ghRelease{{
			TagName: "v1.0.0",
			Name:    "v1.0.0",
			Assets: []ghAsset{
				{ID: assetID, Name: binName, URL: "https://example.invalid/beacon"},
				{ID: 3, Name: "SHA256SUMS.txt", URL: "https://example.invalid/sums"},
			},
		}})
	})

	svc := NewService(Config{
		CurrentVersion: "0.29.0", APIBase: srv.URL, PendingPath: filepath.Join(t.TempDir(), "beacon.new"),
		NewHTTPClient: directClient, RequestRestart: func() {}, Audit: &fakeAudit{},
	})
	err := svc.ApplyUpdate(context.Background(), ChannelStable, "", "tester", "")
	if err == nil || !strings.Contains(err.Error(), "GA 资产已变化") {
		t.Fatalf("资产漂移应在下载前中止，实际错误：%v", err)
	}
	if calls != 2 {
		t.Fatalf("下载前应查询两次，实际 %d 次", calls)
	}
}

// TestApplyUpdateReportsDownloadPercent 下载阶段应实时更新进度百分比（复现并回归「Percent 恒 0%」缺陷）。
// Content-Length 已知时，下载完成后百分比应达 100；修复前 downloadBinary 从不写 Percent，恒为 0。
func TestApplyUpdateReportsDownloadPercent(t *testing.T) {
	const tag = "v9.9.9"
	binName := currentAssetName(t, tag)
	const binContent = "新二进制内容用于进度百分比断言"
	sums := fmt.Sprintf("%s  %s\n", sha256hex(binContent), binName)
	srv := newMockReleaseServer(t, tag, binName, binContent, sums)

	dir := t.TempDir()
	svc := NewService(Config{
		CurrentVersion: "1.0.0", APIBase: srv.URL,
		PendingPath:   filepath.Join(dir, "beacon.new"),
		NewHTTPClient: directClient, RequestRestart: func() {}, Audit: &fakeAudit{},
	})

	if err := svc.ApplyUpdate(context.Background(), ChannelStable, "", "tester", ""); err != nil {
		t.Fatalf("ApplyUpdate 应成功，实际 %v", err)
	}
	if pct := svc.Snapshot().Percent; pct != 100 {
		t.Fatalf("下载完成后进度百分比应为 100，实际 %d（Percent 恒 0 即下载阶段未实时更新的缺陷）", pct)
	}
}

// TestProgressWriterUpdatesPercent 旁路计数器：按累计字节实时更新百分比、未知总长不更新、超额封顶 100。
func TestProgressWriterUpdatesPercent(t *testing.T) {
	// total 已知：分批写入，百分比随累计递增。
	tr := newProgressTracker()
	pw := &progressWriter{tracker: tr, total: 100}
	if _, err := pw.Write(make([]byte, 25)); err != nil {
		t.Fatalf("Write 不应出错: %v", err)
	}
	if got := tr.Snapshot().Percent; got != 25 {
		t.Fatalf("写 25/100 后百分比应为 25，实际 %d", got)
	}
	_, _ = pw.Write(make([]byte, 75))
	if got := tr.Snapshot().Percent; got != 100 {
		t.Fatalf("写满 100/100 后百分比应为 100，实际 %d", got)
	}
	// 超额写入（如 LimitReader 多读 1 字节）封顶 100。
	_, _ = pw.Write(make([]byte, 50))
	if got := tr.Snapshot().Percent; got != 100 {
		t.Fatalf("超额写入应封顶 100，实际 %d", got)
	}

	// total 未知（0）：不更新百分比，保持 0（不误报）。
	tr2 := newProgressTracker()
	pw2 := &progressWriter{tracker: tr2, total: 0}
	_, _ = pw2.Write(make([]byte, 1000))
	if got := tr2.Snapshot().Percent; got != 0 {
		t.Fatalf("total 未知时不应更新百分比，实际 %d", got)
	}
}

// TestRollbackTriggersCallbackAndAudit 有 .old：RollbackAvailable 为真，Rollback 记 update-rollback 审计并触发回调（FR-120）。
func TestRollbackTriggersCallbackAndAudit(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "beacon")
	if err := os.WriteFile(run, []byte("当前版"), 0o644); err != nil {
		t.Fatalf("写运行二进制失败: %v", err)
	}
	if err := os.WriteFile(run+".old", []byte("旧版"), 0o644); err != nil {
		t.Fatalf("写 .old 失败: %v", err)
	}
	audit := &fakeAudit{}
	rolledBack := false
	svc := NewService(Config{
		CurrentVersion:  "1.0.0",
		RunPath:         run,
		RequestRollback: func() { rolledBack = true },
		Audit:           audit,
	})
	if !svc.RollbackAvailable() {
		t.Fatal("有 .old 应可回滚")
	}
	if err := svc.Rollback("tester", "1.2.3.4"); err != nil {
		t.Fatalf("Rollback 应成功: %v", err)
	}
	if !rolledBack {
		t.Fatal("应触发回滚回调")
	}
	if !contains(audit.actions(), model.ActionSystemUpdateRollback) {
		t.Fatalf("应记 update-rollback 审计，实际 %v", audit.actions())
	}
}

// TestRollbackNoBackupRejected 无 .old：RollbackAvailable 为假，Rollback 返回错误且不触发回调（FR-120）。
func TestRollbackNoBackupRejected(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "beacon")
	if err := os.WriteFile(run, []byte("当前版"), 0o644); err != nil {
		t.Fatalf("写运行二进制失败: %v", err)
	}
	rolledBack := false
	svc := NewService(Config{
		CurrentVersion:  "1.0.0",
		RunPath:         run,
		RequestRollback: func() { rolledBack = true },
		Audit:           &fakeAudit{},
	})
	if svc.RollbackAvailable() {
		t.Fatal("无 .old 应不可回滚")
	}
	if err := svc.Rollback("tester", ""); err == nil {
		t.Fatal("无 .old 应返回错误")
	}
	if rolledBack {
		t.Fatal("无 .old 不应触发回调")
	}
}

func assertPresignedFailureRedacted(t *testing.T, observations map[string]string) {
	t.Helper()
	secrets := []string{
		"github-token-raw", "github-password-raw", "github-secret-raw", "github-api-key-raw",
		"amz-signature-raw", "amz-credential-raw", "amz-session-raw",
	}
	diagnostics := []string{
		"下载资产失败", "连接被重置", "github.com/wcpe/Beacon/releases/download",
		"objects.githubusercontent.com/releases/download", "download=1", "response-content-disposition=attachment",
	}
	for name, value := range observations {
		for _, secret := range secrets {
			if strings.Contains(value, secret) {
				t.Errorf("%s 泄露凭据 %q：%s", name, secret, value)
			}
		}
		for _, diagnostic := range diagnostics {
			if !strings.Contains(value, diagnostic) {
				t.Errorf("%s 丢失正常失败诊断 %q：%s", name, diagnostic, value)
			}
		}
	}
}

func contains(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}

// assertNoTempLeak 断言更新目录无残留 beacon-update-*.tmp 临时文件（资源泄露守护）。
func assertNoTempLeak(t *testing.T, dir string) {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(dir, "beacon-update-*.tmp"))
	if len(matches) > 0 {
		t.Fatalf("不应残留更新临时文件，实际 %v", matches)
	}
}
