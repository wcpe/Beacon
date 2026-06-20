//go:build e2e

// Proxy 目录注入（FR-4 服务发现延伸出口）的真机端到端测试，纯 Go 原生 go test -tags=e2e。
//
// 本测试自起服务端：先构建并起控制面（SQLite 开发模式，无需 Docker/MySQL），再起真 Paper +
// BeaconAgent（role=bukkit 目标子服）+ 真 Waterfall + BeaconAgentProxy（role=bungee，跑目录同步器）
// + BeaconE2EProxy 目录探针，本测试只读探针快照做断言。相位生命周期全在测试内（不悬挂）：
//
//	directory  控制面 UP、Paper+Waterfall UP：断言「在线 role=bukkit 子服按 serverId 注入 Bungee 目录、
//	           手工 lobby 保留、beacon 命令已注册」。
//	failstatic 控制面 DOWN：断言「已注入目录不被清空」（fail-static）。
package directory_e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"beacon/test/e2e/harness"
)

// 控制面地址：默认 http://localhost:8848，可经 E2E_BEACON_URL 覆盖（本地避端口争用）。
var beaconURL = harness.BeaconURL()

// 服务端编排相关常量（与 runServer/runBungee gradle 任务的约定一致）。
const (
	adminUser      = "admin"
	namespace      = "prod"
	bukkitServerID = "e2e-bukkit-1"
	bungeeServerID = "e2e-bungee-1"
	bukkitPort     = "25566"
	bungeePort     = "25577"
	manualServer   = "lobby"
	bootstrap      = "beacon-bootstrap-2026"
	onlineWait     = 12 * time.Minute // 首跑含下载 Paper/Waterfall + 构建 jar，给足时间
	sqliteDBName   = "beacon-e2e-directory.db"
)

// snapshot 探针覆写的最新快照。
type snapshot struct {
	beaconCommand bool
	servers       map[string]string // 服务器名 → socketAddress
}

// TestDirectoryE2E 按相位顺序编排整套 FR-4 目录注入真机端到端：构建 → 起控制面(SQLite) →
// 起 Paper + Waterfall → 等两端 online → directory → 杀控制面 → failstatic。defer 收口杀全部进程。
func TestDirectoryE2E(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	bungeeRunDir := filepath.Join(repoRoot, ".tmp", "e2e-run", "bungee")
	snapshotPath := filepath.Join(bungeeRunDir, "plugins", "BeaconE2EProxy", "e2e-directory-latest.txt")

	// 控制面 SQLite 库文件（每轮删除从干净库开始）。
	sqliteDB := filepath.Join(repoRoot, ".tmp", sqliteDBName)
	if err := os.MkdirAll(filepath.Dir(sqliteDB), 0o755); err != nil {
		t.Fatalf("创建 .tmp 目录失败：%v", err)
	}
	_ = os.Remove(sqliteDB)

	t.Log("== 构建控制面二进制 ==")
	bin, err := harness.BuildBeacon(repoRoot)
	if err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}

	t.Log("== 起控制面（SQLite）==")
	cp, err := harness.StartControlPlane(harness.ControlPlaneConfig{
		BinPath: bin, RepoRoot: repoRoot, BaseURL: beaconURL,
		DBDriver: "sqlite", DBDSN: sqliteDB,
		AdminPassword: adminPass, AuthSecret: authSecret, BootstrapToken: bootstrap,
		LogPrefix: "beacon-directory",
	})
	if err != nil {
		t.Fatalf("起控制面失败：%v", err)
	}
	cpStopped := false
	defer func() {
		if !cpStopped {
			cp.Stop()
		}
	}()

	t.Log("== 起 Paper 子服（25566）+ Waterfall 代理（25577）==")
	paper, err := harness.StartGradleTask(repoRoot, ":agent-e2e:runServer", []string{"-Pe2eMcPort=" + bukkitPort, harness.BeaconEndpointProp()}, "paper")
	if err != nil {
		t.Fatalf("起 Paper 失败：%v", err)
	}
	defer paper.Stop()
	bungee, err := harness.StartGradleTask(repoRoot, ":agent-e2e-bungee:runBungee", []string{harness.BeaconEndpointProp()}, "bungee")
	if err != nil {
		t.Fatalf("起 Waterfall 失败：%v", err)
	}
	defer bungee.Stop()

	t.Log("== 等子服与代理 online（首跑含下载/构建，耐心等）==")
	token, err := harness.Login(beaconURL, adminUser, adminPass)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	if err := harness.WaitInstanceOnline(beaconURL, token, namespace, bukkitServerID, onlineWait); err != nil {
		t.Fatalf("等 %s online 超时（见 .tmp/paper.out.log）：%v", bukkitServerID, err)
	}
	if err := harness.WaitInstanceOnline(beaconURL, token, namespace, bungeeServerID, onlineWait); err != nil {
		t.Fatalf("等 %s online 超时（见 .tmp/bungee.out.log）：%v", bungeeServerID, err)
	}
	t.Log("子服与代理均 online")

	t.Run("directory", func(t *testing.T) { runDirectory(t, snapshotPath) })

	t.Log("== 相位 failstatic（杀控制面，目录不清空）==")
	cp.Stop()
	cpStopped = true
	time.Sleep(3 * time.Second)
	t.Run("failstatic", func(t *testing.T) { runFailStatic(t, snapshotPath) })
}

// ---- 相位实现（断言逻辑自原 main.go 搬入，fatalf→t.Fatalf、pass/logf→t.Log）----

// runDirectory：断言目录注入、手工服保留、beacon 命令注册（headless 维度）。
func runDirectory(t *testing.T, snapshotPath string) {
	// ① 等代理把在线 bukkit 子服按 serverId 注入 Bungee 目录，地址含子服监听端口。
	if !waitUntil(40*time.Second, func() bool {
		s := readSnapshot(snapshotPath)
		addr, ok := s.servers[bukkitServerID]
		return ok && strings.Contains(addr, bukkitPort)
	}) {
		t.Fatalf("directory：超时未观测到 bukkit 子服 %s 被注入 Bungee 目录（端口 %s）；当前快照=%v",
			bukkitServerID, bukkitPort, readSnapshot(snapshotPath).servers)
	}
	s := readSnapshot(snapshotPath)
	t.Logf("directory：bukkit 子服 %s 已注入，地址=%s", bukkitServerID, s.servers[bukkitServerID])

	// ② 手工服务器（Waterfall 默认 lobby）应被保留：Beacon 只管自己创建的条目、不动手工配置。
	if _, ok := s.servers[manualServer]; !ok {
		t.Fatalf("directory：手工服务器 %s 不应被移除，却已从目录消失（当前=%v）", manualServer, s.servers)
	}
	// ③ beacon 主命令应已在代理注册。
	if !s.beaconCommand {
		t.Fatalf("directory：beacon 主命令应已在代理注册，但 COMMAND_BEACON=false")
	}
	t.Log(fmt.Sprintf("PASS directory：bukkit 子服按 serverId 注入目录、手工服 %s 保留、beacon 命令已注册", manualServer))
}

// runFailStatic：控制面已被外部编排杀掉后，断言「已注入目录不被清空」（fail-static）。
func runFailStatic(t *testing.T, snapshotPath string) {
	// fail-static：代理目录同步器不因控制面失联而清空已注入条目。
	if !waitUntil(15*time.Second, func() bool {
		s := readSnapshot(snapshotPath)
		_, bukkit := s.servers[bukkitServerID]
		_, manual := s.servers[manualServer]
		return bukkit && manual
	}) {
		t.Fatalf("failstatic：控制面下线后已注入的 %s 与手工服 %s 不应消失，当前快照=%v",
			bukkitServerID, manualServer, readSnapshot(snapshotPath).servers)
	}
	t.Log("PASS failstatic：控制面下线期间已注入目录与手工服保留（fail-static 成立）")
}

// ---- 探针快照解析 ----

// readSnapshot 读取探针覆写的最新快照；文件不存在视为空快照。
func readSnapshot(path string) snapshot {
	out := snapshot{servers: map[string]string{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "COMMAND_BEACON="):
			out.beaconCommand = strings.TrimPrefix(line, "COMMAND_BEACON=") == "true"
		case strings.HasPrefix(line, "SERVER "):
			// SERVER <名称> <地址>
			parts := strings.SplitN(strings.TrimPrefix(line, "SERVER "), " ", 2)
			if len(parts) == 2 {
				out.servers[parts[0]] = parts[1]
			}
		}
	}
	return out
}

// ---- 小工具 ----

func waitUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(1 * time.Second)
	}
	return cond()
}

// requireEnv 取必填 env，缺失即 t.Skip（让普通 go test ./... 不因缺密钥失败）。
func requireEnv(t *testing.T, key string) string {
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("跳过：缺少必需环境变量 %s（仅在显式 -tags=e2e 且注入密钥时运行）", key)
	}
	return v
}
