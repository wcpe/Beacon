//go:build e2e && loadtest

// 长轮询 / SSE 挂起探测的冒烟（真有控制面、真注册虚拟 agent、真发一次配置变更）。
//
// 与 driver.go 的固定 worker 池压测互补：那里压「请求很快返回」的端点，这里验证挂起端点的三个指标
// ——能同时挂多少、挂得稳不稳、变更多久能推下去。默认 60s 量级（挂起 45s + 唤醒 10s + 收尾），
// 足够看到一次唤醒与至少一次自然超时，又不至于把 CI 拖长。
//
// 运行：
//
//	export E2E_ADMIN_PASS='...' E2E_AUTH_SECRET='...'
//	go test -tags "e2e loadtest" -timeout=10m -v -run '^TestSuspendedProbe' ./apps/server/test/loadtest/
//
// 可调环境变量：
//
//	SUSPENDED_HELD     同时保持的挂起连接数，默认 16
//	SUSPENDED_DURATION 探测总时长，默认 45s
//	SUSPENDED_WAKE_AT  开始后多久触发一次配置变更唤醒，默认 10s（0 = 不唤醒）
//	SUSPENDED_AGENTS   注册的虚拟 agent 数，默认取 held
package loadtest_e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

// TestSuspendedProbe 跑一次长轮询挂起探测并把结果打出来（挂起数 / 唤醒时延 / 服务端观测值）。
func TestSuspendedProbe(t *testing.T) {
	held := intEnv(t, "SUSPENDED_HELD", 16)
	duration := durationEnv(t, "SUSPENDED_DURATION", "45s")
	wakeAt := durationEnv(t, "SUSPENDED_WAKE_AT", "10s")
	agents := intEnv(t, "SUSPENDED_AGENTS", held)

	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	workDir := filepath.Join(repoRoot, ".tmp", "loadtest")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("创建压测工作目录失败：%v", err)
	}
	removeStaleDB(t, workDir)

	bin, err := harness.BuildBeacon(repoRoot)
	if err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}
	cp, baseURL := startControlPlane(t, harness.ControlPlaneConfig{
		BinPath: bin, RepoRoot: repoRoot,
		DBDriver: "sqlite", DBDSN: filepath.Join(workDir, sqliteDBName),
		AdminPassword: adminPass, AuthSecret: authSecret, BootstrapToken: bootstrapToken,
		LogPrefix: logPrefix,
		ExtraEnv:  map[string]string{"BEACON_LOG_LEVEL": defaultCPLogLevel},
	}, controlPlaneStartAttempts)
	harness.CleanupControlPlane(t, cp)
	t.Logf("控制面已就绪：%s", baseURL)

	token, err := harness.Login(baseURL, adminUser, adminPass, cp)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	client := newLoadClient(max(held, 2))
	defer client.CloseIdleConnections()

	t.Logf("== 注册 %d 个虚拟 agent（环境 %s）==", agents, agentNamespace)
	serverIDs := registerVirtualAgents(t, client, baseURL, agents)

	cfg := suspendedConfig{
		BaseURL: baseURL, Token: bootstrapToken, AdminToken: token,
		Target: suspendedTarget(baseURL, agentNamespace, serverIDs[0]),
		Held:   held, Duration: duration, WakeAt: wakeAt,
	}
	t.Logf("== 挂起探测：Held=%d Duration=%s WakeAt=%s ==", cfg.Held, cfg.Duration, cfg.WakeAt)
	t.Logf("长轮询目标：%s", cfg.Target)

	res, err := runSuspendedProbe(context.Background(), cfg)
	if err != nil {
		t.Fatalf("挂起探测失败：%v", err)
	}
	reportSuspended(t, res)

	// 挂起探测的最低自洽门槛：一条都没连上说明 harness 或端点坏了，
	// 这种结果没有解释价值，直接失败比留下一张全零表更诚实。
	if res.Connected == 0 {
		t.Fatalf("没有任何连接成功挂起（Errors=%d，ErrorKinds=%v）", res.Errors, res.ErrorKinds)
	}
	if res.SuspendedObserved < 1 {
		t.Fatalf("服务端自观测未看到任何挂起（长轮询挂起峰值=0），客户端挂住与服务端登记对不上")
	}
	if wakeAt > 0 && res.CompletedByWake == 0 {
		t.Fatalf("唤醒后没有任何连接返回（CompletedByWake=0）：要么变更没发出去，要么唤醒范围没覆盖目标 serverId")
	}
}

// TestSSEProbe 跑一次 SSE 长连接探测：保持能力 + 保活帧间隔 + 变更推送时延。
// 保活间隔取 longpoll.max-hold-ms（默认 30s），故时长默认给到 70s 才能看到至少一条 `: ping`。
func TestSSEProbe(t *testing.T) {
	held := intEnv(t, "SUSPENDED_HELD", 2)
	duration := durationEnv(t, "SUSPENDED_DURATION", "70s")
	wakeAt := durationEnv(t, "SUSPENDED_WAKE_AT", "20s")

	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")
	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	workDir := filepath.Join(repoRoot, ".tmp", "loadtest")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("创建压测工作目录失败：%v", err)
	}
	removeStaleDB(t, workDir)

	bin, err := harness.BuildBeacon(repoRoot)
	if err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}
	cp, baseURL := startControlPlane(t, harness.ControlPlaneConfig{
		BinPath: bin, RepoRoot: repoRoot,
		DBDriver: "sqlite", DBDSN: filepath.Join(workDir, sqliteDBName),
		AdminPassword: adminPass, AuthSecret: authSecret, BootstrapToken: bootstrapToken,
		LogPrefix: logPrefix,
		ExtraEnv:  map[string]string{"BEACON_LOG_LEVEL": defaultCPLogLevel},
	}, controlPlaneStartAttempts)
	harness.CleanupControlPlane(t, cp)
	t.Logf("控制面已就绪：%s", baseURL)

	token, err := harness.Login(baseURL, adminUser, adminPass, cp)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	client := newLoadClient(max(held, 2))
	defer client.CloseIdleConnections()

	serverIDs := registerVirtualAgents(t, client, baseURL, held)
	cfg := suspendedConfig{
		BaseURL: baseURL, Token: bootstrapToken, AdminToken: token,
		Target: sseTarget(baseURL, agentNamespace, serverIDs[0]),
		Held:   held, Duration: duration, WakeAt: wakeAt,
	}
	t.Logf("== SSE 探测：Held=%d Duration=%s WakeAt=%s ==", cfg.Held, cfg.Duration, cfg.WakeAt)
	t.Logf("SSE 目标：%s", cfg.Target)

	res, err := runSSEProbe(context.Background(), cfg)
	if err != nil {
		t.Fatalf("SSE 探测失败：%v", err)
	}
	reportSuspended(t, res)
	if res.PingInterval != nil {
		t.Logf("保活帧间隔：n=%d 平均 %.1fms p50 %.1fms p95 %.1fms（应≈longpoll.max-hold-ms，默认 30000ms）",
			res.PingInterval.Count, res.PingInterval.AvgMs, res.PingInterval.P50Ms, res.PingInterval.P95Ms)
	} else {
		t.Logf("保活帧：探测窗口内没收到 `: ping`（时长 %s 可能短于保活间隔）", cfg.Duration)
	}
	if res.Connected == 0 {
		t.Fatalf("没有建立任何 SSE 连接（Errors=%d，ErrorKinds=%v）", res.Errors, res.ErrorKinds)
	}
	if res.CompletedByTimeout == 0 {
		t.Fatalf("没有一条 SSE 连接保持到探测结束（CompletedByTimeout=0），连接被提前断开")
	}
}

// suspendedTarget 拼长轮询目标地址：带目标 serverId 与一个占位 md5（探测内部会换成真实当前 md5）。
// timeoutMs 不写在 URL 里：让它走服务端上限（longpoll.max-hold-ms），探测自行读取该上限来定客户端超时。
func suspendedTarget(baseURL, namespace, serverID string) string {
	return fmt.Sprintf("%s/beacon/v1/agent/config/effective?namespace=%s&serverId=%s&md5=%s",
		baseURL, namespace, serverID, suspendedPrimeMD5)
}

// sseTarget 拼 SSE 目标地址：不带任何 md5，让首轮对账把当前有效配置当增量补发（拿到 config-changed + ready）。
func sseTarget(baseURL, namespace, serverID string) string {
	return fmt.Sprintf("%s/beacon/v1/agent/stream?namespace=%s&serverId=%s",
		baseURL, namespace, serverID)
}

// reportSuspended 把挂起探测结果打成可读摘要（长轮询与 SSE 共用，字段口径见各自注释）。
func reportSuspended(t *testing.T, res *suspendedResult) {
	t.Helper()
	t.Logf("结果：Held=%d Connected=%d 唤醒返回=%d 超时返回=%d Errors=%d ErrorKinds=%v",
		res.Held, res.Connected, res.CompletedByWake, res.CompletedByTimeout, res.Errors, res.ErrorKinds)
	t.Logf("挂起时延（发出→服务端登记 waiter）：n=%d 平均 %.2fms p50 %.2fms p95 %.2fms 最大 %.2fms",
		res.HoldLatency.Count, res.HoldLatency.AvgMs, res.HoldLatency.P50Ms,
		res.HoldLatency.P95Ms, res.HoldLatency.MaxMs)
	t.Logf("唤醒时延（变更发出→连接返回）：n=%d 平均 %.2fms p50 %.2fms p95 %.2fms 最大 %.2fms",
		res.WakeLatency.Count, res.WakeLatency.AvgMs, res.WakeLatency.P50Ms,
		res.WakeLatency.P95Ms, res.WakeLatency.MaxMs)
	t.Logf("服务端自观测长轮询挂起峰值：%d", res.SuspendedObserved)
}
