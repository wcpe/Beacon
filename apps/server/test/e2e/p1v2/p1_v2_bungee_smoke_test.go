//go:build e2e

package p1v2_e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	defaultBungeeDir = `D:\Games\MinecraftServer\BungeeCord`

	adminUser   = "admin"
	adminPass   = "p1-v2-smoke-pass"
	authSecret  = "p1-v2-smoke-secret"
	namespace   = "p1v2"
	serverID    = "p1-v2-proxy"
	serverAddr  = "127.0.0.1:25565"
	beaconURL   = "http://localhost:18848"
	sqliteDB    = "beacon-e2e-p1-v2.db"
	onlineWait  = 6 * time.Minute
	pendingWait = 6 * time.Minute
)

type namespaceView struct {
	ID          uint   `json:"id"`
	AccessToken string `json:"accessToken"`
}

type identityView struct {
	IdentityID  string `json:"identityId"`
	NamespaceID uint   `json:"namespaceId"`
	ServerID    string `json:"serverId"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
}

type serverView struct {
	ID          uint
	NamespaceID uint
	ServerID    string
	Kind        string
	BCClusterID *uint
	ZoneID      *uint
	Assigned    bool `json:"assigned"`
}

type assignmentResultView struct {
	ID       uint   `json:"id"`
	ServerID string `json:"serverId"`
	Ok       bool   `json:"ok"`
}

type assignmentResponseView struct {
	Results []assignmentResultView `json:"results"`
}

type bcClusterView struct {
	ID          uint
	NamespaceID uint
	Name        string
}

type listResponse[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

func TestP1V2BungeeRegistrationSmoke(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("当前真机 smoke 指向 Windows 本机 BungeeCord 目录")
	}
	bungeeDir := os.Getenv("E2E_BUNGEE_DIR")
	if bungeeDir == "" {
		bungeeDir = defaultBungeeDir
	}
	if _, err := os.Stat(filepath.Join(bungeeDir, "BungeeCord.jar")); err != nil {
		t.Skipf("未找到 BungeeCord.jar，跳过真机 smoke：%v", err)
	}

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	sqlitePath := filepath.Join(repoRoot, ".tmp", sqliteDB)
	_ = os.Remove(sqlitePath)

	t.Log("== 构建控制面二进制 ==")
	bin, err := harness.BuildBeacon(repoRoot)
	if err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}

	t.Log("== 构建 agent-bungee 真机 jar ==")
	agentJar := buildAgentBungeeJar(t, repoRoot)
	restore := prepareBungeeDirectory(t, repoRoot, bungeeDir, agentJar)
	t.Cleanup(restore)

	t.Log("== 启动临时控制面（SQLite）==")
	cp, err := harness.StartControlPlane(harness.ControlPlaneConfig{
		BinPath: bin, RepoRoot: repoRoot, BaseURL: testBeaconURL(),
		DBDriver: "sqlite", DBDSN: sqlitePath,
		AdminPassword: adminPass, AuthSecret: authSecret, BootstrapToken: "legacy-token-unused-by-v2",
		LogPrefix: "beacon-p1-v2",
	})
	if err != nil {
		t.Fatalf("启动控制面失败：%v", err)
	}
	harness.CleanupControlPlane(t, cp)

	adminToken, err := harness.Login(testBeaconURL(), adminUser, adminPass)
	if err != nil {
		t.Fatalf("登录控制面失败：%v", err)
	}
	ns := createNamespace(t, adminToken)

	t.Log("== 启动真实 BungeeCord，等待 v2 pending ==")
	bungee := startBungee(t, repoRoot, bungeeDir, ns.AccessToken)
	cleanupBungee(t, bungee)

	identityPath := filepath.Join(bungeeDir, "plugins", "BeaconAgentProxy", "identity.yml")
	identityID := waitIdentityFile(t, identityPath, pendingWait, bungee)
	pending := waitIdentityStatus(t, adminToken, identityID, "pending", pendingWait, bungee)
	if pending.ServerID != serverID || pending.NamespaceID != ns.ID || pending.Kind != "proxy" {
		t.Fatalf("pending 身份归属不符合预期：%+v namespace=%d", pending, ns.ID)
	}

	t.Log("== approve 后等待 active 与 legacy v1 online ==")
	approveIdentity(t, adminToken, identityID, bungee)
	waitIdentityStatus(t, adminToken, identityID, "active", pendingWait, bungee)
	if err := harness.WaitInstanceOnline(testBeaconURL(), adminToken, namespace, serverID, onlineWait, bungee); err != nil {
		t.Fatalf("v2 active 后应衔接 legacy 数据面 online：%v", err)
	}

	t.Log("== 验证 approve 只创建未分配 server，再做首次 BC 集群分配 ==")
	server := requireUnassignedServer(t, adminToken, ns.ID, bungee)
	cluster := createBCCluster(t, adminToken, ns.ID, bungee)
	assigned := assignServerToBCCluster(t, adminToken, ns.ID, server.ID, cluster.ID, bungee)
	if assigned.BCClusterID == nil || *assigned.BCClusterID != cluster.ID || assigned.ZoneID != nil || !assigned.Assigned {
		t.Fatalf("server 首次分配结果不符合预期：server=%+v cluster=%+v", assigned, cluster)
	}

	t.Log("== 重启真实 BungeeCord，验证 identityId 持久不变 ==")
	stopBungee(t, bungee)
	bungee = startBungee(t, repoRoot, bungeeDir, ns.AccessToken)
	cleanupBungee(t, bungee)
	if got := waitIdentityFile(t, identityPath, pendingWait, bungee); got != identityID {
		t.Fatalf("重启后 identityId 应保持不变：want=%s got=%s", identityID, got)
	}
	waitIdentityStatus(t, adminToken, identityID, "active", pendingWait, bungee)

	t.Log("== 损坏 identity.yml 后启动，验证不静默重生成 ==")
	stopBungee(t, bungee)
	corrupt := []byte("format-version: 1\nidentity-id: not-a-uuid\ncreated-at: \"2026-07-07T00:00:00Z\"\n")
	if err := os.WriteFile(identityPath, corrupt, 0o644); err != nil {
		t.Fatalf("写入损坏 identity.yml 失败：%v", err)
	}
	bungee = startBungee(t, repoRoot, bungeeDir, ns.AccessToken)
	cleanupBungee(t, bungee)
	time.Sleep(15 * time.Second)
	after, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatalf("读取损坏 identity.yml 失败：%v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(after), bytes.TrimSpace(corrupt)) {
		t.Fatalf("损坏 identity.yml 不应被静默重生成，实际内容：\n%s", string(after))
	}
}

func TestBungeeStopIgnoresTerminationRaceAfterProcessExit(t *testing.T) {
	done := make(chan struct{})
	proc := &bungeeProc{
		done: done, stopDone: make(chan struct{}), state: bungeeProcRunning,
	}
	killCalled := false
	proc.killFn = func(*exec.Cmd) (error, error) {
		killCalled = true
		proc.mu.Lock()
		proc.state = bungeeProcStopped
		proc.mu.Unlock()
		close(done)
		return fmt.Errorf("整树终止返回访问被拒绝"), fmt.Errorf("根进程终止返回访问被拒绝")
	}

	if err := proc.StopE(); err != nil {
		t.Fatalf("进程已确认退出时不应把并发终止错误误报为清理失败：%v", err)
	}
	if !killCalled {
		t.Fatal("测试未覆盖并发终止命令失败路径")
	}
}

func testBeaconURL() string {
	if v := os.Getenv("E2E_BEACON_URL"); v != "" {
		return v
	}
	return beaconURL
}

func createNamespace(t *testing.T, token string) namespaceView {
	t.Helper()
	id, accessToken, err := harness.CreateV2Namespace(testBeaconURL(), token, namespace, "P1 v2 真机 smoke")
	if err != nil {
		t.Fatalf("创建 namespace 失败：%v", err)
	}
	return namespaceView{ID: id, AccessToken: accessToken}
}

func approveIdentity(t *testing.T, token, identityID string, guard harness.ProcessGuard) {
	t.Helper()
	if err := harness.ApproveIdentityWithGuard(testBeaconURL(), token, identityID, guard); err != nil {
		t.Fatalf("批准 identity 失败：%v", err)
	}
}

func requireUnassignedServer(t *testing.T, token string, namespaceID uint, guard harness.ProcessGuard) serverView {
	t.Helper()
	var resp listResponse[serverView]
	path := fmt.Sprintf("/admin/v2/servers?namespaceId=%d&assigned=false&keyword=%s", namespaceID, url.QueryEscape(serverID))
	doAdminJSON(t, http.MethodGet, path, token, nil, http.StatusOK, &resp, guard)
	for _, item := range resp.Items {
		if item.ServerID == serverID && item.Kind == "proxy" && item.BCClusterID == nil && item.ZoneID == nil {
			return item
		}
	}
	t.Fatalf("未找到未分配 proxy server，响应：%+v", resp)
	return serverView{}
}

func createBCCluster(t *testing.T, token string, namespaceID uint, guard harness.ProcessGuard) bcClusterView {
	t.Helper()
	var cluster bcClusterView
	doAdminJSON(t, http.MethodPost, "/admin/v2/bc-clusters", token, map[string]any{
		"namespaceId": namespaceID, "name": "p1-v2-bc", "description": "P1 v2 smoke 集群",
	}, http.StatusCreated, &cluster, guard)
	if cluster.ID == 0 || cluster.NamespaceID != namespaceID {
		t.Fatalf("BC 集群创建响应不符合预期：%+v", cluster)
	}
	return cluster
}

func assignServerToBCCluster(t *testing.T, token string, namespaceID, serverRowID, clusterID uint, guard harness.ProcessGuard) serverView {
	t.Helper()
	var assignmentResp assignmentResponseView
	doAdminJSON(t, http.MethodPost, "/admin/v2/server-assignments", token, map[string]any{
		"serverIds": []uint{serverRowID},
		"target": map[string]any{
			"kind": "bc_cluster",
			"id":   clusterID,
		},
		"reason": "P1 v2 真机 smoke 首次分配",
	}, http.StatusOK, &assignmentResp, guard)
	if len(assignmentResp.Results) != 1 {
		t.Fatalf("server 分配响应应返回 1 项，实际 %+v", assignmentResp)
	}
	result := assignmentResp.Results[0]
	if result.ID != serverRowID || result.ServerID != serverID || !result.Ok {
		t.Fatalf("server 分配响应不符合预期：want id=%d serverId=%s ok=true，实际 %+v", serverRowID, serverID, result)
	}

	var servers listResponse[serverView]
	path := fmt.Sprintf("/admin/v2/servers?namespaceId=%d&assigned=true&keyword=%s", namespaceID, url.QueryEscape(serverID))
	doAdminJSON(t, http.MethodGet, path, token, nil, http.StatusOK, &servers, guard)
	for _, item := range servers.Items {
		if item.ID == serverRowID && item.ServerID == serverID {
			return item
		}
	}
	t.Fatalf("未找到写后已分配 server：rowID=%d serverId=%s 响应=%+v", serverRowID, serverID, servers)
	return serverView{}
}

func waitIdentityStatus(
	t *testing.T,
	token, identityID, status string,
	timeout time.Duration,
	guard harness.ProcessGuard,
) identityView {
	t.Helper()
	var matched identityView
	err := harness.WaitForCondition(timeout, time.Second, guard, func(ctx context.Context) bool {
		var resp listResponse[identityView]
		path := "/admin/v2/agent-identities?keyword=" + url.QueryEscape(identityID)
		if err := requestAdminJSON(ctx, http.MethodGet, path, token, nil, http.StatusOK, &resp, guard); err != nil {
			return false
		}
		for _, item := range resp.Items {
			if item.IdentityID == identityID && item.Status == status {
				matched = item
				return true
			}
		}
		return false
	})
	if err != nil {
		t.Fatalf("等待 identity=%s 进入 %s 失败：%v", identityID, status, err)
	}
	return matched
}

func waitIdentityFile(t *testing.T, path string, timeout time.Duration, guard harness.ProcessGuard) string {
	t.Helper()
	var identityID string
	err := harness.WaitForCondition(timeout, time.Second, guard, func(context.Context) bool {
		var err error
		identityID, err = readIdentityID(path)
		return err == nil && identityID != ""
	})
	if err != nil {
		t.Fatalf("等待 identity.yml 生成失败：%s：%v", path, err)
	}
	return identityID
}

func readIdentityID(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "identity-id:") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "identity-id:")), `"`), nil
		}
	}
	return "", fmt.Errorf("identity.yml 缺少 identity-id")
}

func doAdminJSON(t *testing.T, method, path, token string, body any, want int, out any, guard harness.ProcessGuard) {
	t.Helper()
	if err := requestAdminJSON(context.Background(), method, path, token, body, want, out, guard); err != nil {
		t.Fatalf("%s %s 请求失败：%v", method, path, err)
	}
}

func requestAdminJSON(ctx context.Context, method, path, token string, body any, want int, out any, guard harness.ProcessGuard) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("编码请求体失败：%w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(testBeaconURL(), "/")+path, reader)
	if err != nil {
		return fmt.Errorf("构造请求失败：%w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := harness.DoRequestWithGuard(req, 0, guard)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("读取响应失败：%w", err)
	}
	if resp.StatusCode != want {
		return fmt.Errorf("应返回 %d，实际 %d", want, resp.StatusCode)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("解析响应失败：%w", err)
		}
	}
	return nil
}

func buildAgentBungeeJar(t *testing.T, repoRoot string) string {
	t.Helper()
	gradlew := filepath.Join(repoRoot, "apps", "agent", "gradlew.bat")
	cmd := exec.Command(gradlew, ":agent-bungee:build", "--no-daemon", "--console=plain")
	cmd.Dir = filepath.Join(repoRoot, "apps", "agent")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("构建 agent-bungee 失败：%v\n%s", err, string(out))
	}
	versionRaw, err := os.ReadFile(filepath.Join(repoRoot, "VERSION"))
	if err != nil {
		t.Fatalf("读取 VERSION 失败：%v", err)
	}
	jar := filepath.Join(repoRoot, "apps", "agent", "agent-bungee", "build", "libs", "BeaconAgentProxy-"+strings.TrimSpace(string(versionRaw))+".jar")
	if _, err := os.Stat(jar); err != nil {
		t.Fatalf("未找到 agent-bungee jar：%s：%v", jar, err)
	}
	return jar
}

func prepareBungeeDirectory(t *testing.T, repoRoot, bungeeDir, agentJar string) func() {
	t.Helper()
	pluginsDir := filepath.Join(bungeeDir, "plugins")
	dataDir := filepath.Join(pluginsDir, "BeaconAgentProxy")
	backupDir := filepath.Join(repoRoot, ".tmp", "p1-v2-bungee-backup", strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("创建备份目录失败：%v", err)
	}

	jarBackups := backupPluginJars(t, pluginsDir, backupDir)
	dataBackups := backupAgentDataFiles(t, dataDir, backupDir)
	testJar := filepath.Join(pluginsDir, "BeaconAgentProxy-p1-v2-smoke.jar")

	restore := func() {
		_ = os.Remove(testJar)
		for _, backup := range jarBackups {
			_ = copyFileNoFatal(backup.backup, backup.original)
		}
		for _, backup := range dataBackups {
			if backup.hadOriginal {
				_ = copyFileNoFatal(backup.backup, backup.original)
			} else {
				_ = os.Remove(backup.original)
			}
		}
	}
	prepared := false
	defer func() {
		if !prepared {
			restore()
		}
	}()

	copyFile(t, agentJar, testJar)
	prepared = true
	return restore
}

type jarBackup struct {
	original string
	backup   string
}

func backupPluginJars(t *testing.T, pluginsDir, backupDir string) []jarBackup {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(pluginsDir, "BeaconAgentProxy*.jar"))
	if err != nil {
		t.Fatalf("扫描 BeaconAgentProxy jar 失败：%v", err)
	}
	backups := make([]jarBackup, 0, len(matches))
	for _, original := range matches {
		backup := filepath.Join(backupDir, filepath.Base(original))
		copyFile(t, original, backup)
		if err := os.Remove(original); err != nil {
			t.Fatalf("移除旧 BeaconAgentProxy jar 失败：%s：%v", original, err)
		}
		backups = append(backups, jarBackup{original: original, backup: backup})
	}
	return backups
}

type dataFileBackup struct {
	original    string
	backup      string
	hadOriginal bool
}

func backupAgentDataFiles(t *testing.T, dataDir, backupDir string) []dataFileBackup {
	t.Helper()
	names := []string{
		"identity.yml",
		"effective-config.snapshot.json",
		"file-tree.applied.json",
	}
	backups := make([]dataFileBackup, 0, len(names))
	for _, name := range names {
		original := filepath.Join(dataDir, name)
		backup := filepath.Join(backupDir, name)
		if _, err := os.Stat(original); err == nil {
			copyFile(t, original, backup)
			if err := os.Remove(original); err != nil {
				t.Fatalf("移除原 agent 运行态文件失败：%s：%v", original, err)
			}
			backups = append(backups, dataFileBackup{original: original, backup: backup, hadOriginal: true})
			continue
		} else if !os.IsNotExist(err) {
			t.Fatalf("读取原 agent 运行态文件失败：%s：%v", original, err)
		}
		_ = os.MkdirAll(dataDir, 0o755)
		backups = append(backups, dataFileBackup{original: original, backup: backup, hadOriginal: false})
	}
	return backups
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	if err := copyFileNoFatal(src, dst); err != nil {
		t.Fatalf("复制文件失败 %s -> %s：%v", src, dst, err)
	}
}

func copyFileNoFatal(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

type bungeeProcState uint8

const (
	bungeeProcRunning bungeeProcState = iota
	bungeeProcExited
	bungeeProcStopping
	bungeeProcStopped

	bungeeStopTimeout = 10 * time.Second
)

type bungeeProc struct {
	cmd        *exec.Cmd
	outFile    *os.File
	errFile    *os.File
	stdoutPath string
	stderrPath string
	done       chan struct{}
	stopDone   chan struct{}
	stopOnce   sync.Once
	mu         sync.Mutex
	state      bungeeProcState
	waitErr    error
	stopErr    error
	killFn     func(*exec.Cmd) (error, error)
}

func startBungee(t *testing.T, repoRoot, bungeeDir, namespaceToken string) *bungeeProc {
	t.Helper()
	outLog := filepath.Join(repoRoot, ".tmp", "p1-v2-bungee.out.log")
	errLog := filepath.Join(repoRoot, ".tmp", "p1-v2-bungee.err.log")
	outFile, err := os.Create(outLog)
	if err != nil {
		t.Fatalf("创建 Bungee stdout 日志失败：%v", err)
	}
	errFile, err := os.Create(errLog)
	if err != nil {
		_ = outFile.Close()
		t.Fatalf("创建 Bungee stderr 日志失败：%v", err)
	}
	cmd := exec.Command(javaPath(), "-jar", "BungeeCord.jar", "--nogui")
	cmd.Dir = bungeeDir
	cmd.Env = append(os.Environ(),
		"BEACON_AGENT_BEACON_ENDPOINTS="+testBeaconURL(),
		"BEACON_AGENT_BEACON_BOOTSTRAP_TOKEN="+namespaceToken,
		"BEACON_AGENT_IDENTITY_NAMESPACE="+namespace,
		"BEACON_AGENT_IDENTITY_SERVER_ID="+serverID,
		"BEACON_AGENT_IDENTITY_ADDRESS="+serverAddr,
		"BEACON_AGENT_MESSAGING_ENABLED=false",
	)
	cmd.Stdout = outFile
	cmd.Stderr = errFile
	if err := cmd.Start(); err != nil {
		_ = outFile.Close()
		_ = errFile.Close()
		t.Fatalf("启动 BungeeCord 失败：%v", err)
	}
	proc := &bungeeProc{
		cmd: cmd, outFile: outFile, errFile: errFile,
		stdoutPath: outLog, stderrPath: errLog,
		done: make(chan struct{}), stopDone: make(chan struct{}), state: bungeeProcRunning,
		killFn: killBungee,
	}
	go proc.wait()
	return proc
}

func (p *bungeeProc) Done() <-chan struct{} {
	if p == nil {
		return nil
	}
	return p.done
}

func (p *bungeeProc) LogPaths() (string, string) {
	if p == nil {
		return "", ""
	}
	return p.stdoutPath, p.stderrPath
}

func (p *bungeeProc) CheckEarlyExit() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != bungeeProcExited {
		return nil
	}
	return fmt.Errorf(
		"BungeeCord 意外早退：退出结果=%s；stdout=%s；stderr=%s",
		exitResult(p.waitErr), p.stdoutPath, p.stderrPath,
	)
}

func (p *bungeeProc) Stop() {
	_ = p.StopE()
}

func (p *bungeeProc) StopE() error {
	if p == nil {
		return nil
	}
	p.stopOnce.Do(func() {
		shouldKill := p.beginStop()
		var treeErr, rootErr error
		if shouldKill {
			killFn := p.killFn
			if killFn == nil {
				killFn = killBungee
			}
			treeErr, rootErr = killFn(p.cmd)
		}
		select {
		case <-p.done:
			// 已确认进程退出即完成收尾；Windows 下进程并发自退时 taskkill 可能返回假失败。
		case <-time.After(bungeeStopTimeout):
			p.stopErr = p.stopDiagnostic("等待进程退出超时", treeErr, rootErr)
		}
		close(p.stopDone)
	})
	<-p.stopDone
	return p.stopErr
}

func (p *bungeeProc) beginStop() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	shouldKill := p.state == bungeeProcRunning
	if p.state != bungeeProcStopped {
		p.state = bungeeProcStopping
	}
	return shouldKill
}

func (p *bungeeProc) stopDiagnostic(reason string, treeErr, rootErr error) error {
	return fmt.Errorf(
		"停止 BungeeCord 失败：%s；整树终止=%s；根进程回退=%s；等待上限=%s；stdout=%s；stderr=%s",
		reason, localErrorResult(treeErr), localErrorResult(rootErr), bungeeStopTimeout, p.stdoutPath, p.stderrPath,
	)
}

func (p *bungeeProc) wait() {
	err := p.cmd.Wait()
	_ = p.outFile.Close()
	_ = p.errFile.Close()
	p.mu.Lock()
	p.waitErr = err
	if p.state == bungeeProcStopping {
		p.state = bungeeProcStopped
	} else {
		p.state = bungeeProcExited
	}
	p.mu.Unlock()
	close(p.done)
}

func killBungee(cmd *exec.Cmd) (treeErr, rootErr error) {
	if cmd == nil || cmd.Process == nil {
		return nil, nil
	}
	if runtime.GOOS == "windows" {
		treeErr = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		if treeErr != nil {
			rootErr = cmd.Process.Kill()
		}
		return treeErr, rootErr
	}
	return cmd.Process.Kill(), nil
}

func cleanupBungee(t *testing.T, proc *bungeeProc) {
	t.Helper()
	t.Cleanup(func() {
		if err := proc.StopE(); err != nil {
			t.Errorf("清理 BungeeCord 失败：%v", err)
		}
	})
}

func stopBungee(t *testing.T, proc *bungeeProc) {
	t.Helper()
	if err := proc.StopE(); err != nil {
		t.Fatalf("停止 BungeeCord 失败：%v", err)
	}
}

func exitResult(err error) string {
	if err == nil {
		return "退出码 0"
	}
	return err.Error()
}

func localErrorResult(err error) string {
	if err == nil {
		return "无错误"
	}
	return err.Error()
}

func javaPath() string {
	if v := os.Getenv("E2E_JAVA"); v != "" {
		return v
	}
	if home := os.Getenv("JAVA_HOME"); home != "" {
		candidate := filepath.Join(home, "bin", "java.exe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	candidate := `C:\Users\Admin\.jdks\ms-21.0.9\bin\java.exe`
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return "java"
}
