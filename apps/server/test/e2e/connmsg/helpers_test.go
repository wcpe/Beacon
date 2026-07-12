//go:build e2e

// P5a 连接明细与跨服消息 wire 端到端（FR-145/149/150，见 docs/specs/v2-connection-message-storage.md）：
// 让真实 agent 代码经真实门面把请求打到真实控制面，验证 send/poll/ack 与 connections/batch 的 wire 与落库。
//
// 本文件是 message_test.go 与 connection_test.go 共用的编排与断言助手（admin v2 身份编排、SQLite 直读、
// 标记文件解析、轮询工具），仿 schedagent 用例的既有范式。控制面默认 SQLite（无需 docker），
// e2e 端口默认 18850（可经 E2E_BEACON_URL 覆盖）。
package connmsg_e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const adminUser = "admin"

// beaconURL 返回控制面地址：默认 http://localhost:18850，可经 E2E_BEACON_URL 覆盖（本地避端口争用）。
func beaconURL() string {
	if v := os.Getenv("E2E_BEACON_URL"); v != "" {
		return v
	}
	return "http://localhost:18850"
}

// ---- admin v2 身份编排（建 namespace → 等 pending → approve → 等 active）----

type namespaceView struct {
	ID          uint   `json:"id"`
	AccessToken string `json:"accessToken"`
}

type identityView struct {
	IdentityID string `json:"identityId"`
	ServerID   string `json:"serverId"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
}

// createNamespace 建一个 v2 namespace，返回其 id 与 agent 接入用 accessToken。
func createNamespace(t *testing.T, base, token, name, desc string) namespaceView {
	t.Helper()
	var ns namespaceView
	doAdminJSON(t, base, http.MethodPost, "/admin/v2/namespaces", token, map[string]any{
		"name": name, "description": desc,
	}, http.StatusCreated, &ns)
	if ns.ID == 0 || ns.AccessToken == "" {
		t.Fatalf("建 namespace 响应缺 id/accessToken：%+v", ns)
	}
	return ns
}

// findIdentity 在某 namespace 下按 serverId 找 agent 身份。
func findIdentity(t *testing.T, base, token string, namespaceID uint, serverID string) (identityView, bool) {
	t.Helper()
	var resp struct {
		Items []identityView `json:"items"`
	}
	doAdminJSON(t, base, http.MethodGet, "/admin/v2/agent-identities?namespaceId="+utoa(namespaceID), token, nil, http.StatusOK, &resp)
	for _, it := range resp.Items {
		if it.ServerID == serverID {
			return it, true
		}
	}
	return identityView{}, false
}

// waitPendingIdentity 轮询等待某 serverId 的 pending 身份出现（首跑含下载/构建，调用方给足超时）。
func waitPendingIdentity(t *testing.T, base, token string, namespaceID uint, serverID string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if it, found := findIdentity(t, base, token, namespaceID, serverID); found && it.Status == "pending" {
			return it.IdentityID
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("等待 %s 的 pending 身份超时", serverID)
	return ""
}

// waitIdentityStatus 轮询等待某 serverId 身份进入目标状态。
func waitIdentityStatus(t *testing.T, base, token string, namespaceID uint, serverID, status string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if it, found := findIdentity(t, base, token, namespaceID, serverID); found && it.Status == status {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("等待 %s 身份进入 %s 超时", serverID, status)
}

// approveIdentity 批准身份使其 active（v2 数据面上报端点须 active 才不被 403）。
func approveIdentity(t *testing.T, base, token, identityID string) {
	t.Helper()
	var ident identityView
	doAdminJSON(t, base, http.MethodPost,
		"/admin/v2/agent-identities/"+identityID+"/approve", token,
		map[string]any{"forceUnbindOccupier": false}, http.StatusOK, &ident)
	if ident.Status != "active" {
		t.Fatalf("approve 后身份应 active，实际 %+v", ident)
	}
}

// ---- HTTP / DB / 小工具 ----

func doAdminJSON(t *testing.T, base, method, path, token string, body any, wantStatus int, out any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败：%v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, strings.TrimRight(base, "/")+path, reader)
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
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s 期望 HTTP %d，得 %d：%s", method, path, wantStatus, resp.StatusCode, string(raw))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("解析 %s 响应失败：%v（%s）", path, err, string(raw))
		}
	}
}

// openE2EDB 以只读友好参数打开 e2e SQLite 库（供直读日表断言）。
func openE2EDB(t *testing.T, sqliteDB string) *gorm.DB {
	t.Helper()
	dsn := sqliteDB
	if !strings.Contains(dsn, "?") {
		dsn += "?_busy_timeout=5000"
	}
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("连接数据库失败：%v", err)
	}
	return db
}

// waitUntil 在超时内每秒重试 cond，命中即返回 true。
func waitUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Second)
	}
	return cond()
}

// requireEnv 取必需环境变量；缺失则跳过（仅在显式 -tags=e2e 且注入密钥时运行）。
func requireEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("跳过：缺少必需环境变量 %s（仅在显式 -tags=e2e 且注入密钥时运行）", key)
	}
	return v
}

// removeIfExists 删除文件（不存在时返回的错误由调用方忽略），供每轮从干净 SQLite 库开始。
func removeIfExists(path string) error { return os.Remove(path) }

// contains 是 strings.Contains 的薄封装，避免在断言文件重复引入 strings 导入。
func contains(s, sub string) bool { return strings.Contains(s, sub) }

// utoa 把无符号整数转十进制字符串。
func utoa(v uint) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// ---- 标记文件解析（探针观测）----

// markObservation 是探针标记文件的单行观测（首两段固定为 时间 | 来源，其后为明细/字段）。
type markObservation struct {
	source string
	rest   string
	raw    string
}

// readMarkObservations 读标记文件全部观测行（文件不存在返回空）；兼容 .tmp 与 apps/.tmp 两种 runDir 解析。
func readMarkObservations(path string) []markObservation {
	obs := parseMarkFile(path)
	if len(obs) > 0 {
		return obs
	}
	if alt := altMarkPath(path); alt != "" {
		return parseMarkFile(alt)
	}
	return obs
}

// altMarkPath 返回另一候选标记路径（.tmp ↔ apps/.tmp 切换），兼容 runDir 落点差异。
func altMarkPath(path string) string {
	if strings.Contains(path, "/apps/.tmp/") || strings.Contains(path, "\\apps\\.tmp\\") {
		return strings.NewReplacer("/apps/.tmp/", "/.tmp/", "\\apps\\.tmp\\", "\\.tmp\\").Replace(path)
	}
	return strings.NewReplacer("/.tmp/", "/apps/.tmp/", "\\.tmp\\", "\\apps\\.tmp\\").Replace(path)
}

// parseMarkFile 按行解析标记文件（每行以 | 分隔，取第 2 段为来源，其余合并为明细）。
func parseMarkFile(path string) []markObservation {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var out []markObservation
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 2 {
			continue
		}
		rest := ""
		if len(parts) == 3 {
			rest = parts[2]
		}
		out = append(out, markObservation{source: parts[1], rest: rest, raw: line})
	}
	return out
}

// waitMarkSource 轮询标记文件直到出现某来源的观测行，返回该行（超时 t.Fatalf）。
func waitMarkSource(t *testing.T, path, source string, timeout time.Duration, desc string) markObservation {
	t.Helper()
	var found markObservation
	ok := waitUntil(timeout, func() bool {
		for _, o := range readMarkObservations(path) {
			if o.source == source {
				found = o
				return true
			}
		}
		return false
	})
	if !ok {
		t.Fatalf("等待探针观测「%s」（source=%s）超时（%s）；标记文件=%s", desc, source, timeout, path)
	}
	return found
}
