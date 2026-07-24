//go:build e2e

// Proxy 目录注入（FR-4 服务发现延伸出口）的真机端到端测试，纯 Go 原生 go test -tags=e2e。
//
// 本测试自起服务端：先构建并起控制面（SQLite 开发模式，无需 Docker/MySQL），再以单个
// serveDirectory 启动真 Paper 后端、原生 BungeeCord 代理、双端 Agent 与目录探针；测试只读快照断言：
//
//	directory  控制面 UP、Directory 拓扑 UP：断言「在线 backend 按 serverId 注入 Bungee 目录、
//	           mc-testkit 固定手工路由 backend 保留、代理实现为 BungeeCord、beacon 命令已注册」。
//	failstatic 控制面 DOWN：断言「已注入目录不被清空」（fail-static）。
package directory_e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

// 控制面地址：默认 http://localhost:8848，可经 E2E_BEACON_URL 覆盖（本地避端口争用）。
var beaconURL = harness.BeaconURL()

// 服务端编排相关常量（与 serveDirectory 任务的约定一致）。
const (
	adminUser      = "admin"
	namespace      = "e2e-directory"
	bukkitServerID = "e2e-bukkit-1"
	bungeeServerID = "e2e-bungee-1"
	bukkitPort     = "25566"
	bungeePort     = "25577"
	manualServer   = "backend"
	bootstrap      = "beacon-bootstrap-2026"
	onlineWait     = 12 * time.Minute // 首跑含下载 Paper/BungeeCord + 构建 jar，给足时间
	sqliteDBName   = "beacon-e2e-directory.db"

	// Agent 目录同步周期为 10 秒；连续观察 12 秒，完整跨过一轮并留 2 秒裕量。
	failStaticObserve = 12 * time.Second
)

// snapshot 探针覆写的最新快照。
type snapshot struct {
	implementation string
	beaconCommand  bool
	servers        map[string]string // 服务器名 → socketAddress
}

// TestDirectoryE2E 编排整套目录注入真机端到端：构建 → 起控制面 → 单进程起 Directory 拓扑 →
// 等两端 online → directory → 杀控制面 → failstatic。defer 收口杀全部进程。
func TestDirectoryE2E(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	// 启动前清掉代理运行目录中的旧快照，避免历史观测造成假绿。
	snapshotPath := filepath.Join(
		harness.ProxyRunDir(repoRoot), "plugins", "BeaconE2EProxy", "e2e-directory-latest.txt",
	)
	if err := os.Remove(snapshotPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("清理旧目录快照失败：%v", err)
	}

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
	harness.CleanupControlPlane(t, cp)

	token, err := harness.Login(beaconURL, adminUser, adminPass)
	if err != nil {
		t.Fatalf("登录失败：%v", err)
	}
	namespaceID, accessToken, err := harness.CreateV2Namespace(beaconURL, token, namespace, "FR-4 目录注入 e2e")
	if err != nil {
		t.Fatalf("创建 %s namespace 失败：%v", namespace, err)
	}
	t.Logf("已建 v2 namespace id=%d", namespaceID)

	t.Log("== 单进程起 Paper 后端（25566）+ 原生 BungeeCord 代理（25577）==")
	directoryEnv := harness.DirectoryGradleEnv(
		beaconURL, accessToken, namespace,
		bukkitServerID, "127.0.0.1:"+bukkitPort,
		bungeeServerID, "127.0.0.1:"+bungeePort,
	)
	directoryProc, err := harness.StartGradleTask(
		repoRoot, ":agent-e2e:serveDirectory", nil, directoryEnv, "directory",
	)
	if err != nil {
		t.Fatalf("起 Directory 拓扑失败：%v", err)
	}
	harness.CleanupGradle(t, directoryProc)

	t.Log("== 等子服与代理 identity pending，批准后继续等 legacy online ==")
	bukkitIdentityID, err := harness.WaitIdentityStatus(
		beaconURL, token, namespaceID, bukkitServerID, "pending", onlineWait, directoryProc,
	)
	if err != nil {
		t.Fatalf("等 %s identity pending 失败：%v", bukkitServerID, err)
	}
	if err := harness.ApproveIdentityWithGuard(beaconURL, token, bukkitIdentityID, directoryProc); err != nil {
		t.Fatalf("批准 %s identity 失败：%v", bukkitServerID, err)
	}
	if _, err := harness.WaitIdentityStatus(
		beaconURL, token, namespaceID, bukkitServerID, "active", onlineWait, directoryProc,
	); err != nil {
		t.Fatalf("等 %s identity active 失败：%v", bukkitServerID, err)
	}
	bungeeIdentityID, err := harness.WaitIdentityStatus(
		beaconURL, token, namespaceID, bungeeServerID, "pending", onlineWait, directoryProc,
	)
	if err != nil {
		t.Fatalf("等 %s identity pending 失败：%v", bungeeServerID, err)
	}
	if err := harness.ApproveIdentityWithGuard(beaconURL, token, bungeeIdentityID, directoryProc); err != nil {
		t.Fatalf("批准 %s identity 失败：%v", bungeeServerID, err)
	}
	if _, err := harness.WaitIdentityStatus(
		beaconURL, token, namespaceID, bungeeServerID, "active", onlineWait, directoryProc,
	); err != nil {
		t.Fatalf("等 %s identity active 失败：%v", bungeeServerID, err)
	}
	if err := harness.WaitInstanceOnline(
		beaconURL, token, namespace, bukkitServerID, onlineWait, directoryProc,
	); err != nil {
		t.Fatalf("等 %s online 失败：%v", bukkitServerID, err)
	}
	if err := harness.WaitInstanceOnline(
		beaconURL, token, namespace, bungeeServerID, onlineWait, directoryProc,
	); err != nil {
		t.Fatalf("等 %s online 失败：%v", bungeeServerID, err)
	}
	t.Log("子服与代理均 active + online")

	t.Run("directory", func(t *testing.T) { runDirectory(t, snapshotPath, directoryProc) })

	t.Log("== 相位 failstatic（杀控制面，目录不清空）==")
	if err := cp.StopE(); err != nil {
		t.Fatalf("fail-static 前停止控制面失败：%v", err)
	}
	t.Run("failstatic", func(t *testing.T) { runFailStatic(t, snapshotPath, directoryProc) })
}

// ---- 相位实现（断言逻辑自原 main.go 搬入，fatalf→t.Fatalf、pass/logf→t.Log）----

// runDirectory 断言目录注入、固定手工路由、原生实现与 beacon 命令注册。
func runDirectory(t *testing.T, snapshotPath string, guard *harness.GradleProc) {
	err := harness.WaitForCondition(40*time.Second, time.Second, guard, func(context.Context) bool {
		s := readSnapshot(snapshotPath)
		addr, ok := s.servers[bukkitServerID]
		return ok && strings.Contains(addr, bukkitPort)
	})
	if err != nil {
		t.Fatalf("directory：未观测到 backend %s 注入 Bungee 目录：%v；当前快照=%v",
			bukkitServerID, err, readSnapshot(snapshotPath).servers)
	}
	s := readSnapshot(snapshotPath)
	t.Logf("directory：backend %s 已注入，地址=%s", bukkitServerID, s.servers[bukkitServerID])

	if _, ok := s.servers[manualServer]; !ok {
		t.Fatalf("directory：mc-testkit 固定路由 %s 不应被移除，当前=%v", manualServer, s.servers)
	}
	if s.implementation != "BungeeCord" {
		t.Fatalf("directory：代理实现必须为原生 BungeeCord，实际=%q", s.implementation)
	}
	if !s.beaconCommand {
		t.Fatal("directory：beacon 主命令应已在代理注册，但 COMMAND_BEACON=false")
	}
	t.Logf("PASS directory：backend 按 serverId 注入、固定路由 %s 保留、实现为 BungeeCord、beacon 命令已注册", manualServer)
}

// runFailStatic 在控制面下线后持续断言已注入目录与固定手工路由都不被清空。
func runFailStatic(t *testing.T, snapshotPath string, guard *harness.GradleProc) {
	t.Logf("failstatic：控制面已下线，连续观察 %s", failStaticObserve)
	err := harness.ObserveFor(failStaticObserve, time.Second, guard, func(context.Context) error {
		s := readSnapshot(snapshotPath)
		if _, ok := s.servers[bukkitServerID]; !ok {
			return fmt.Errorf("动态目录条目 %s 消失，当前快照=%v", bukkitServerID, s.servers)
		}
		if _, ok := s.servers[manualServer]; !ok {
			return fmt.Errorf("固定手工路由 %s 消失，当前快照=%v", manualServer, s.servers)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failstatic：持续观察失败：%v", err)
	}
	t.Logf("PASS failstatic：控制面下线后连续 %s 动态目录与固定路由均保留（完整覆盖 10 秒同步周期）", failStaticObserve)
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
		case strings.HasPrefix(line, "IMPLEMENTATION="):
			out.implementation = strings.TrimPrefix(line, "IMPLEMENTATION=")
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

// requireEnv 取必填 env，缺失即 t.Skip（让普通 go test ./... 不因缺密钥失败）。
func requireEnv(t *testing.T, key string) string {
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("跳过：缺少必需环境变量 %s（仅在显式 -tags=e2e 且注入密钥时运行）", key)
	}
	return v
}
