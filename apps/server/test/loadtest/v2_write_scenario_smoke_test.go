//go:build e2e && loadtest

// v2_write_scenario.go 的独立冒烟：自起控制面 → bootstrap v2 身份 → 跑三个 build 函数 →
// 对每个端点打请求断言真实状态码 → 连接入队背压探测。
//
// 仅在 -tags "e2e loadtest" 且注入 E2E_ADMIN_PASS / E2E_AUTH_SECRET 时运行（缺则 requireEnv 跳过）。
// 铁律同主文件：不改任何生产代码 / 既有 loadtest 文件；只复用 harness 与既有 helper。
package loadtest_e2e

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

// TestV2WriteScenarioSmoke 是全链路冒烟：bootstrap → 三组场景预检 → 连接入队背压探测。
func TestV2WriteScenarioSmoke(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")
	t.Setenv("LOADTEST_ENABLE_WRITE_SCENARIOS", "1") // 纳入写场景一并预检

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	workDir := filepath.Join(repoRoot, ".tmp", "loadtest")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("创建压测工作目录失败：%v", err)
	}
	// 清掉本冒烟上次遗留的独立库（含 WAL/SHM/归档），保证每轮从干净库存始：
	// 否则上一轮的 namespace / server 行还在，本轮建同名 namespace 会 409 NAMESPACE_CONFLICT。
	stale, err := filepath.Glob(filepath.Join(workDir, "beacon-loadtest-v2smoke.db*"))
	if err != nil {
		t.Fatalf("枚举冒烟遗留库失败：%v", err)
	}
	for _, p := range stale {
		if err := os.Remove(p); err != nil {
			t.Fatalf("删除冒烟遗留库 %s 失败：%v", p, err)
		}
	}

	t.Log("== 构建控制面二进制 ==")
	bin, err := harness.BuildBeacon(repoRoot)
	if err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}
	t.Log("== 起控制面（独立 sqlite + 动态端口）==")
	cp, baseURL := startControlPlane(t, harness.ControlPlaneConfig{
		BinPath: bin, RepoRoot: repoRoot,
		DBDriver: "sqlite", DBDSN: filepath.Join(workDir, "beacon-loadtest-v2smoke.db"),
		AdminPassword: adminPass, AuthSecret: authSecret, BootstrapToken: bootstrapToken,
		LogPrefix: "beacon-v2smoke",
	}, controlPlaneStartAttempts)
	harness.CleanupControlPlane(t, cp)

	token, err := harness.Login(baseURL, adminUser, adminPass, cp)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	client := newLoadClient(8)
	defer client.CloseIdleConnections()

	const namespace = "loadtestv2smoke"
	t.Log("== bootstrap v2 身份（建命名空间 → 注册 proxy → 审批 active）==")
	boot := bootstrapV2Identity(t, baseURL, token, namespace, 2, cp)

	cfg := scenarioConfig{
		BaseURL: baseURL, AdminToken: token,
		BootstrapToken: bootstrapToken, Namespace: namespace, ServerIDs: boot.ServerIDs,
	}

	t.Log("== 预检 v2 上报场景 ==")
	v2Scenarios := buildV2Scenarios(cfg, boot)
	for i := range v2Scenarios {
		assertScenarioSmoke(t, client, &v2Scenarios[i])
	}
	t.Log("== 预检写场景 ==")
	writeScenarios := buildWriteScenarios(cfg, boot)
	for i := range writeScenarios {
		assertScenarioSmoke(t, client, &writeScenarios[i])
	}

	t.Log("== 连接入队背压探测 ==")
	res := probeIngestQueueHeadroom(t, client, baseURL, boot, cp, 200, 30, 8)
	t.Logf("连接上报探测：total=%d，状态分布=%v，触及429(队列满)=%v，触及503(DB超时)=%v",
		res.Total, res.StatusCounts, res.SawBackpressure, res.SawUnavailable)
}

// assertScenarioSmoke 对单个场景按其请求体轮转打若干发（无请求体的 GET 打 4 发），逐一断言 WantStatus
// 并把每个端点的真实状态码打出来（供人工核对后再由主代理接入主矩阵）。
func assertScenarioSmoke(t *testing.T, client *http.Client, s *scenario) {
	t.Helper()
	times := len(s.bodies)
	if times == 0 {
		times = 4 // GET / 无体场景固定打 4 发
	}
	for i := range times {
		var reader io.Reader
		if len(s.bodies) > 0 {
			reader = bytes.NewReader(s.bodies[i%len(s.bodies)])
		}
		ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
		req, err := http.NewRequestWithContext(ctx, s.Method, s.URL, reader)
		if err != nil {
			cancel()
			t.Fatalf("%s（%s %s）构造请求失败：%v", s.Name, s.Method, s.Path, err)
		}
		for _, h := range s.headers {
			req.Header.Set(h.key, h.value)
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			t.Fatalf("%s（%s %s）第 %d 发失败：%v", s.Name, s.Method, s.Path, i, err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, probeSnippetLimit))
		_ = resp.Body.Close()
		status := resp.StatusCode
		cancel()
		if status != s.WantStatus {
			t.Fatalf("%s（%s %s）第 %d 发期望 %d，实际 %d；片段：%s",
				s.Name, s.Method, s.Path, i, s.WantStatus, status, snippetOf(raw))
		}
		t.Logf("OK %-34s %-4s %-42s → %d", s.Name, s.Method, s.Path, status)
	}
}
