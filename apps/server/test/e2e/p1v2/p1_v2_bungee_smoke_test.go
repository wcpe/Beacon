//go:build e2e

package p1v2_e2e

import (
	"bytes"
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
	defer cp.Stop()

	adminToken, err := harness.Login(testBeaconURL(), adminUser, adminPass)
	if err != nil {
		t.Fatalf("登录控制面失败：%v", err)
	}
	ns := createNamespace(t, adminToken)

	t.Log("== 启动真实 BungeeCord，等待 v2 pending ==")
	bungee := startBungee(t, repoRoot, bungeeDir, ns.AccessToken)
	defer bungee.Stop()

	identityPath := filepath.Join(bungeeDir, "plugins", "BeaconAgentProxy", "identity.yml")
	identityID := waitIdentityFile(t, identityPath, pendingWait)
	pending := waitIdentityStatus(t, adminToken, identityID, "pending", pendingWait)
	if pending.ServerID != serverID || pending.NamespaceID != ns.ID || pending.Kind != "proxy" {
		t.Fatalf("pending 身份归属不符合预期：%+v namespace=%d", pending, ns.ID)
	}

	t.Log("== approve 后等待 active 与 legacy v1 online ==")
	approveIdentity(t, adminToken, identityID)
	waitIdentityStatus(t, adminToken, identityID, "active", pendingWait)
	if err := harness.WaitInstanceOnline(testBeaconURL(), adminToken, namespace, serverID, onlineWait); err != nil {
		t.Fatalf("v2 active 后应衔接 legacy 数据面 online：%v", err)
	}

	t.Log("== 验证 approve 只创建未分配 server，再做首次 BC 集群分配 ==")
	server := requireUnassignedServer(t, adminToken, ns.ID)
	cluster := createBCCluster(t, adminToken, ns.ID)
	assigned := assignServerToBCCluster(t, adminToken, server.ID, cluster.ID)
	if assigned.BCClusterID == nil || *assigned.BCClusterID != cluster.ID || assigned.ZoneID != nil {
		t.Fatalf("server 首次分配结果不符合预期：server=%+v cluster=%+v", assigned, cluster)
	}

	t.Log("== 重启真实 BungeeCord，验证 identityId 持久不变 ==")
	bungee.Stop()
	bungee = startBungee(t, repoRoot, bungeeDir, ns.AccessToken)
	defer bungee.Stop()
	if got := waitIdentityFile(t, identityPath, pendingWait); got != identityID {
		t.Fatalf("重启后 identityId 应保持不变：want=%s got=%s", identityID, got)
	}
	waitIdentityStatus(t, adminToken, identityID, "active", pendingWait)

	t.Log("== 损坏 identity.yml 后启动，验证不静默重生成 ==")
	bungee.Stop()
	corrupt := []byte("format-version: 1\nidentity-id: not-a-uuid\ncreated-at: \"2026-07-07T00:00:00Z\"\n")
	if err := os.WriteFile(identityPath, corrupt, 0o644); err != nil {
		t.Fatalf("写入损坏 identity.yml 失败：%v", err)
	}
	bungee = startBungee(t, repoRoot, bungeeDir, ns.AccessToken)
	defer bungee.Stop()
	time.Sleep(15 * time.Second)
	after, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatalf("读取损坏 identity.yml 失败：%v", err)
	}
	if !bytes.Equal(bytes.TrimSpace(after), bytes.TrimSpace(corrupt)) {
		t.Fatalf("损坏 identity.yml 不应被静默重生成，实际内容：\n%s", string(after))
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
	var ns namespaceView
	doAdminJSON(t, http.MethodPost, "/admin/v2/namespaces", token, map[string]any{
		"name": namespace, "description": "P1 v2 真机 smoke",
	}, http.StatusCreated, &ns)
	if ns.ID == 0 || ns.AccessToken == "" {
		t.Fatalf("创建 namespace 响应缺少 ID/token：%+v", ns)
	}
	return ns
}

func approveIdentity(t *testing.T, token, identityID string) {
	t.Helper()
	var ident identityView
	doAdminJSON(t, http.MethodPost, "/admin/v2/agent-identities/"+url.PathEscape(identityID)+"/approve", token, map[string]any{
		"forceUnbindOccupier": false,
	}, http.StatusOK, &ident)
	if ident.Status != "active" {
		t.Fatalf("approve 后身份应 active，实际 %+v", ident)
	}
}

func requireUnassignedServer(t *testing.T, token string, namespaceID uint) serverView {
	t.Helper()
	var resp listResponse[serverView]
	path := fmt.Sprintf("/admin/v2/servers?namespaceId=%d&assigned=false&keyword=%s", namespaceID, url.QueryEscape(serverID))
	doAdminJSON(t, http.MethodGet, path, token, nil, http.StatusOK, &resp)
	for _, item := range resp.Items {
		if item.ServerID == serverID && item.Kind == "proxy" && item.BCClusterID == nil && item.ZoneID == nil {
			return item
		}
	}
	t.Fatalf("未找到未分配 proxy server，响应：%+v", resp)
	return serverView{}
}

func createBCCluster(t *testing.T, token string, namespaceID uint) bcClusterView {
	t.Helper()
	var cluster bcClusterView
	doAdminJSON(t, http.MethodPost, "/admin/v2/bc-clusters", token, map[string]any{
		"namespaceId": namespaceID, "name": "p1-v2-bc", "description": "P1 v2 smoke 集群",
	}, http.StatusCreated, &cluster)
	if cluster.ID == 0 || cluster.NamespaceID != namespaceID {
		t.Fatalf("BC 集群创建响应不符合预期：%+v", cluster)
	}
	return cluster
}

func assignServerToBCCluster(t *testing.T, token string, serverRowID, clusterID uint) serverView {
	t.Helper()
	var resp listResponse[serverView]
	doAdminJSON(t, http.MethodPost, "/admin/v2/server-assignments", token, map[string]any{
		"serverIds": []uint{serverRowID},
		"target": map[string]any{
			"kind": "bc_cluster",
			"id":   clusterID,
		},
		"reason": "P1 v2 真机 smoke 首次分配",
	}, http.StatusOK, &resp)
	if len(resp.Items) != 1 {
		t.Fatalf("server 分配响应应返回 1 项，实际 %+v", resp)
	}
	return resp.Items[0]
}

func waitIdentityStatus(t *testing.T, token, identityID, status string, timeout time.Duration) identityView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var resp listResponse[identityView]
		path := "/admin/v2/agent-identities?keyword=" + url.QueryEscape(identityID)
		doAdminJSON(t, http.MethodGet, path, token, nil, http.StatusOK, &resp)
		for _, item := range resp.Items {
			if item.IdentityID == identityID && item.Status == status {
				return item
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("等待 identity=%s 进入 %s 超时", identityID, status)
	return identityView{}
}

func waitIdentityFile(t *testing.T, path string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		identityID, err := readIdentityID(path)
		if err == nil && identityID != "" {
			return identityID
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("等待 identity.yml 生成超时：%s", path)
	return ""
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

func doAdminJSON(t *testing.T, method, path, token string, body any, want int, out any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败：%v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, strings.TrimRight(testBeaconURL(), "/")+path, reader)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s 请求失败：%v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s 应返回 %d，实际 %d：%s", method, path, want, resp.StatusCode, string(raw))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("解析响应失败：%v\n%s", err, string(raw))
		}
	}
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

type bungeeProc struct {
	cmd     *exec.Cmd
	outFile *os.File
	errFile *os.File
	stopped bool
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
	return &bungeeProc{cmd: cmd, outFile: outFile, errFile: errFile}
}

func (p *bungeeProc) Stop() {
	if p == nil {
		return
	}
	if p.stopped {
		return
	}
	p.stopped = true
	if p.cmd != nil && p.cmd.Process != nil {
		if runtime.GOOS == "windows" {
			_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(p.cmd.Process.Pid)).Run()
		} else {
			_ = p.cmd.Process.Kill()
		}
		_ = p.cmd.Wait()
	}
	if p.outFile != nil {
		_ = p.outFile.Close()
	}
	if p.errFile != nil {
		_ = p.errFile.Close()
	}
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
