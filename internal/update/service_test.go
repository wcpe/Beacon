package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/model"
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

// TestCheckForUpdateRollingPrereleaseUsesNameVersion 滚动预发布渠道：tag=prerelease 非 semver，
// 版本取 Release name（v<VERSION>）；跨号则报有更新（ADR-0052 滚动路径）。
func TestCheckForUpdateRollingPrereleaseUsesNameVersion(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/wcpe/Beacon/releases", func(w http.ResponseWriter, _ *http.Request) {
		releases := []ghRelease{{
			TagName:    "prerelease", // 移动标签，非 semver
			Name:       "v0.17.0",    // 版本号在 name
			Prerelease: true,
			Body:       "滚动预发布说明",
			HTMLURL:    "https://example.invalid/pre",
		}}
		_ = json.NewEncoder(w).Encode(releases)
	})
	svc := NewService(Config{
		CurrentVersion: "0.16.0", APIBase: srv.URL, PendingPath: filepath.Join(t.TempDir(), "beacon.new"),
		NewHTTPClient: directClient, RequestRestart: func() {}, Audit: &fakeAudit{},
	})
	res, err := svc.CheckForUpdate(context.Background(), ChannelPrerelease, "", "tester", "")
	if err != nil {
		t.Fatalf("检查应成功: %v", err)
	}
	if res.LatestVersion != "v0.17.0" {
		t.Fatalf("滚动预发布最新版本应取自 name=v0.17.0，实际 %q", res.LatestVersion)
	}
	if !res.HasUpdate {
		t.Fatal("0.16.0 → 0.17.0 跨号应报有更新")
	}
}

// TestCheckForUpdateSameVersionNoUpdate 同 X.Y.Z 滚动覆盖不提示更新（ADR-0052 决策 5）。
func TestCheckForUpdateSameVersionNoUpdate(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/wcpe/Beacon/releases", func(w http.ResponseWriter, _ *http.Request) {
		releases := []ghRelease{{TagName: "prerelease", Name: "v0.16.0", Prerelease: true}}
		_ = json.NewEncoder(w).Encode(releases)
	})
	svc := NewService(Config{
		CurrentVersion: "0.16.0", APIBase: srv.URL, PendingPath: filepath.Join(t.TempDir(), "beacon.new"),
		NewHTTPClient: directClient, RequestRestart: func() {}, Audit: &fakeAudit{},
	})
	res, err := svc.CheckForUpdate(context.Background(), ChannelPrerelease, "", "tester", "")
	if err != nil {
		t.Fatalf("检查应成功: %v", err)
	}
	if res.HasUpdate {
		t.Fatal("同号 0.16.0 不应报有更新")
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
