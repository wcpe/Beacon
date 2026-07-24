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
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
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

// createNamespace 建一个 v2 namespace，返回其 id 与 agent 接入用 accessToken。
func createNamespace(t *testing.T, base, token, name, desc string) namespaceView {
	t.Helper()
	id, accessToken, err := harness.CreateV2Namespace(base, token, name, desc)
	if err != nil {
		t.Fatalf("建 namespace 失败：%v", err)
	}
	return namespaceView{ID: id, AccessToken: accessToken}
}

// waitPendingIdentity 等待某 serverId 的 pending 身份出现，并在全程检查 Gradle 生命周期。
func waitPendingIdentity(t *testing.T, base, token string, namespaceID uint, serverID string, timeout time.Duration, guard *harness.GradleProc) string {
	t.Helper()
	identityID, err := harness.WaitIdentityStatus(base, token, namespaceID, serverID, "pending", timeout, guard)
	if err != nil {
		t.Fatalf("等待 %s 的 pending 身份失败：%v", serverID, err)
	}
	return identityID
}

// waitIdentityStatus 等待某 serverId 身份进入目标状态，并在全程检查 Gradle 生命周期。
func waitIdentityStatus(t *testing.T, base, token string, namespaceID uint, serverID, status string, timeout time.Duration, guard *harness.GradleProc) {
	t.Helper()
	if _, err := harness.WaitIdentityStatus(base, token, namespaceID, serverID, status, timeout, guard); err != nil {
		t.Fatalf("等待 %s 身份进入 %s 失败：%v", serverID, status, err)
	}
}

// approveIdentity 批准身份使其 active（v2 数据面上报端点须 active 才不被 403）。
func approveIdentity(t *testing.T, base, token, identityID string, guard *harness.GradleProc) {
	t.Helper()
	if err := harness.ApproveIdentityWithGuard(base, token, identityID, guard); err != nil {
		t.Fatalf("批准 identity 失败：%v", err)
	}
}

// ---- DB / 小工具 ----

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

// waitUntil 在超时内每秒重试条件，并在全程检查 Gradle 生命周期。
func waitUntil(timeout time.Duration, guard *harness.GradleProc, cond func(context.Context) bool) error {
	return harness.WaitForCondition(timeout, time.Second, guard, cond)
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

// readMarkObservations 读取 mc-testkit 运行目录中的探针观测，文件不存在时返回空。
func readMarkObservations(path string) []markObservation {
	return parseMarkFile(path)
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

// waitMarkSource 轮询标记文件直到出现某来源的观测行，返回该行（失败 t.Fatalf）。
func waitMarkSource(t *testing.T, path, source string, timeout time.Duration, desc string, guard *harness.GradleProc) markObservation {
	t.Helper()
	var found markObservation
	err := waitUntil(timeout, guard, func(context.Context) bool {
		for _, o := range readMarkObservations(path) {
			if o.source == source {
				found = o
				return true
			}
		}
		return false
	})
	if err != nil {
		t.Fatalf("等待探针观测「%s」（source=%s）失败（%s）；标记文件=%s：%v", desc, source, timeout, path, err)
	}
	return found
}
