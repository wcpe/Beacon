//go:build e2e

package harness

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWaitIdentityStatusUsesGradleGuard(t *testing.T) {
	server := emptyItemsServer()
	defer server.Close()
	proc := startHelperGradleProc(t, "delay-zero", ":agent-e2e:servePaper")

	started := time.Now()
	_, err := WaitIdentityStatus(server.URL, "admin-token", 1, "backend-1", "active", 5*time.Second, proc)
	assertGuardedWaitError(t, started, err, proc)
	proc.Stop()
}

func TestWaitInstanceOnlineUsesGradleGuard(t *testing.T) {
	server := emptyItemsServer()
	defer server.Close()
	proc := startHelperGradleProc(t, "delay-zero", ":agent-e2e:serveProxy")

	started := time.Now()
	err := WaitInstanceOnline(server.URL, "admin-token", "e2e", "proxy-1", 5*time.Second, proc)
	assertGuardedWaitError(t, started, err, proc)
	proc.Stop()
}

func TestWaitIdentityStatusCancelsStuckRequestOnGradleExit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	proc := startHelperGradleProc(t, "delay-half", ":agent-e2e:servePaper")

	started := time.Now()
	_, err := WaitIdentityStatus(server.URL, "admin-token", 1, "backend-1", "active", 5*time.Second, proc)
	assertGuardedWaitError(t, started, err, proc)
	proc.Stop()
}

func TestWaitIdentityStatusSelectsActiveIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("namespaceId"); got != "7" {
			t.Errorf("namespaceId 应为 7，实际 %q", got)
		}
		if got := r.URL.Query().Get("status"); got != "active" {
			t.Errorf("status 应为 active，实际 %q", got)
		}
		if got := r.URL.Query().Get("keyword"); got != "backend-1" {
			t.Errorf("keyword 应为 backend-1，实际 %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"identityId":"old-pending","serverId":"backend-1","status":"pending"},{"identityId":"new-active","serverId":"backend-1","status":"active"}]}`))
	}))
	defer server.Close()

	identityID, err := WaitIdentityStatus(server.URL, "admin-token", 7, "backend-1", "active", time.Second)
	if err != nil {
		t.Fatalf("等待 active identity 失败：%v", err)
	}
	if identityID != "new-active" {
		t.Fatalf("应返回 active identity，实际 %q", identityID)
	}
}

func TestWaitIdentityStatusByIDAcceptsUnboundPendingIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/admin/v2/agent-identities/pending-id"; got != want {
			t.Errorf("身份详情路径错误：got %q want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"identityId":"pending-id","serverId":null,"status":"pending"}`))
	}))
	defer server.Close()

	if err := WaitIdentityStatusByID(server.URL, "admin-token", "pending-id", "pending", time.Second); err != nil {
		t.Fatalf("未绑定 pending 身份应可按 identityId 等待：%v", err)
	}
}

func TestWaitAgentIdentityIDReadsGeneratedIdentityFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.yml")
	if err := os.WriteFile(path, []byte("identity-id: \"11111111-1111-4111-8111-111111111111\"\n"), 0o600); err != nil {
		t.Fatalf("写入身份文件失败：%v", err)
	}
	identityID, err := WaitAgentIdentityID(path, time.Second)
	if err != nil {
		t.Fatalf("读取身份文件失败：%v", err)
	}
	if want := "11111111-1111-4111-8111-111111111111"; identityID != want {
		t.Fatalf("身份 ID 错误：got %q want %q", identityID, want)
	}
}

func TestWaitPendingAgentIdentityUsesIdentityFileInsteadOfServerID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.yml")
	if err := os.WriteFile(path, []byte("identity-id: \"22222222-2222-4222-8222-222222222222\"\n"), 0o600); err != nil {
		t.Fatalf("写入身份文件失败：%v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/admin/v2/agent-identities/22222222-2222-4222-8222-222222222222"; got != want {
			t.Errorf("身份详情路径错误：got %q want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"identityId":"22222222-2222-4222-8222-222222222222","serverId":null,"status":"pending"}`))
	}))
	defer server.Close()

	identityID, err := WaitPendingAgentIdentity(server.URL, "admin-token", path, time.Second)
	if err != nil {
		t.Fatalf("应通过身份文件找到未绑定 pending 身份：%v", err)
	}
	if want := "22222222-2222-4222-8222-222222222222"; identityID != want {
		t.Fatalf("pending 身份错误：got %q want %q", identityID, want)
	}
}

func TestRequestApproveIdentityUsesApprovalWorkflow(t *testing.T) {
	var requested, decided bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/v2/agent-identities/pending-id/approve":
			assertApprovalIdempotencyKey(t, r.Header.Get("Idempotency-Key"))
			var body struct {
				ServerID            string `json:"serverId"`
				Reason              string `json:"reason"`
				ForceUnbindOccupier bool   `json:"forceUnbindOccupier"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("解码身份确认申请失败：%v", err)
			}
			if body.ServerID != "backend-1" || body.Reason != "端到端确认身份" || !body.ForceUnbindOccupier {
				t.Fatalf("身份确认申请字段错误：%+v", body)
			}
			requested = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"approvalRequestId":"apr_identity","status":"pending"}`))
		case "/admin/v2/approval-requests/apr_identity/approve":
			decided = true
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := RequestApproveIdentityWithGuard(server.URL, "admin-token", "pending-id", "backend-1", "端到端确认身份", nil, true); err != nil {
		t.Fatalf("身份确认应走审批工作流：%v", err)
	}
	if !requested || !decided {
		t.Fatalf("身份确认应先创建申请再作审批决定：requested=%t decided=%t", requested, decided)
	}
}

func TestRequestApprovalAndApproveUsesIndependentKeysForSequentialApplications(t *testing.T) {
	keys := make(map[string]string)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/v2/agent-identities/pending-id/approve":
			var body struct {
				Reason string `json:"reason"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("解码连续身份申请失败：%v", err)
			}
			assertApprovalIdempotencyKey(t, r.Header.Get("Idempotency-Key"))
			keys[body.Reason] = r.Header.Get("Idempotency-Key")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"approvalRequestId":"apr_` + body.Reason + `","status":"pending"}`))
		case "/admin/v2/approval-requests/apr_初次/approve", "/admin/v2/approval-requests/apr_重确认/approve":
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	for _, reason := range []string{"初次", "重确认"} {
		ticket, err := RequestApprovalAndApprove(server.URL, "admin-token", http.MethodPost,
			"/admin/v2/agent-identities/pending-id/approve", map[string]string{"reason": reason}, nil)
		if err != nil {
			t.Fatalf("%s 申请失败：%v", reason, err)
		}
		if want := "apr_" + reason; ticket.ApprovalRequestID != want {
			t.Fatalf("%s 应获得本次申请票据：got %q want %q", reason, ticket.ApprovalRequestID, want)
		}
	}
	if keys["初次"] == keys["重确认"] {
		t.Fatalf("连续领域申请不得复用 Idempotency-Key：%q", keys["初次"])
	}
}

func assertApprovalIdempotencyKey(t *testing.T, key string) {
	t.Helper()
	if key == "" || len(key) > 64 {
		t.Fatalf("危险审批申请必须携带长度 1..64 的 Idempotency-Key，实际 %q", key)
	}
	for _, r := range key {
		if r < 33 || r > 126 {
			t.Fatalf("Idempotency-Key 必须为可见 ASCII，实际 %q", key)
		}
	}
}

func TestRequestApprovalAndApproveAcceptsAlreadySucceededTicket(t *testing.T) {
	var decided bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/v2/agent-identities/pending-id/approve":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"approvalRequestId":"apr_finished","status":"succeeded"}`))
		case "/admin/v2/approval-requests/apr_finished/approve":
			decided = true
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := RequestApproveIdentityWithGuard(server.URL, "admin-token", "pending-id", "backend-1", "端到端确认身份", nil); err != nil {
		t.Fatalf("已完成票据应视为幂等成功：%v", err)
	}
	if decided {
		t.Fatal("已完成票据不得重复作出审批决定")
	}
}

func TestRequestApprovalAndWaitAcceptsVerifiedImmediateSettingCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/admin/v1/settings/delivery.approver-separation-enabled"; got != want {
			t.Errorf("设置即时完成路径错误：got %q want %q", got, want)
		}
		if r.Method != http.MethodPut {
			t.Errorf("设置即时完成方法错误：got %q", r.Method)
		}
		assertApprovalIdempotencyKey(t, r.Header.Get("Idempotency-Key"))
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("解码设置即时完成请求失败：%v", err)
		}
		if body["value"] != "false" || body["reason"] != "单管理员端到端前置" {
			t.Fatalf("设置即时完成请求字段错误：%+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	ticket, err := RequestApprovalAndWait(server.URL, "admin-token", http.MethodPut,
		"/admin/v1/settings/delivery.approver-separation-enabled", map[string]any{
			"value": "false", "reason": "单管理员端到端前置",
		}, "succeeded", time.Second, nil)
	if err != nil {
		t.Fatalf("可验证的设置即时完成应成功：%v", err)
	}
	if ticket.Status != "succeeded" || ticket.ApprovalRequestID != "" {
		t.Fatalf("设置即时完成票据错误：%+v", ticket)
	}
}

func TestRequestApprovalAndWaitRejectsUnverifiedHTTP200(t *testing.T) {
	tests := []struct {
		name string
		path string
		body map[string]any
		resp string
	}{
		{
			name: "非设置端点",
			path: "/admin/v1/files/1",
			body: map[string]any{"value": "false", "reason": "不得旁路"},
			resp: `{"ok":true}`,
		},
		{
			name: "未确认完成",
			path: "/admin/v1/settings/delivery.approver-separation-enabled",
			body: map[string]any{"value": "false", "reason": "不得旁路"},
			resp: `{"ok":false}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.resp))
			}))
			defer server.Close()

			_, err := RequestApprovalAndWait(server.URL, "admin-token", http.MethodPut, tt.path, tt.body, "succeeded", time.Second, nil)
			if err == nil {
				t.Fatal("不可验证的 HTTP 200 不得视为审批完成")
			}
		})
	}
}

func TestWaitApprovalStatusWaitsForWorkerResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/admin/v2/approval-requests/apr-topology"; got != want {
			t.Errorf("审批详情路径错误：got %q want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"requestId":"apr-topology","status":"succeeded"}`))
	}))
	defer server.Close()

	if err := WaitApprovalStatus(server.URL, "admin-token", "apr-topology", "succeeded", time.Second); err != nil {
		t.Fatalf("应等待审批 worker 成功：%v", err)
	}
}

func TestDoAdminJSONReportsSafeErrorCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"approval_reason_required","message":"不得输出此正文"}`))
	}))
	defer server.Close()

	err := doAdminJSON(server.URL, http.MethodPut, "/settings/archive.auto-enabled", "admin-token", map[string]string{"value": "false"}, http.StatusAccepted, nil)
	if err == nil || !strings.Contains(err.Error(), "错误码=approval_reason_required") || strings.Contains(err.Error(), "不得输出此正文") {
		t.Fatalf("非预期状态应保留错误码且不泄露正文：%v", err)
	}
}

func TestWaitInstanceOnlineRemainsCompatibleWithoutGuard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"serverId":"backend-1","status":"online"}]}`))
	}))
	defer server.Close()

	if err := WaitInstanceOnline(server.URL, "admin-token", "e2e", "backend-1", time.Second); err != nil {
		t.Fatalf("无 guard 的既有调用应保持兼容：%v", err)
	}
}

func TestDoAdminJSONAppliesDefaultDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	oldTimeout := adminRequestTimeout
	adminRequestTimeout = 50 * time.Millisecond
	t.Cleanup(func() { adminRequestTimeout = oldTimeout })

	started := time.Now()
	err := doAdminJSON(server.URL, http.MethodGet, "/stuck", "admin-token", nil, http.StatusOK, nil)
	if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("无显式 context 的管理请求也应按默认 deadline 退出：%v", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("默认 deadline 未及时取消请求，实际耗时 %s", elapsed)
	}
}

func TestRegisterArtifactSecretPersistsFingerprintWithoutPlaintext(t *testing.T) {
	fingerprintFile, generatedFile, envFile := configureArtifactSecretFiles(t)
	const token = "test-secret-actions"
	output := captureStdout(t, func() {
		if err := registerArtifactSecret(token); err != nil {
			t.Fatalf("登记动态 token 失败：%v", err)
		}
	})
	if output != "::add-mask::"+token+"\n" {
		t.Fatalf("Actions 应只输出掩码命令：%q", output)
	}
	assertFileContent(t, envFile, "KEEP=value\n", "动态 token 不得写入 GITHUB_ENV")
	assertFileContent(t, generatedFile, "generated\n", "动态凭据生成状态格式错误")

	sum := sha256.Sum256([]byte(token))
	wantFingerprint := fmt.Sprintf("%x %d\n", sum, len([]byte(token)))
	assertFileContent(t, fingerprintFile, wantFingerprint, "指纹文件只能包含 SHA-256 与字节长度")
	for _, path := range []string{envFile, generatedFile, fingerprintFile} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取安全状态文件失败：%v", err)
		}
		if strings.Contains(string(raw), token) {
			t.Fatalf("安全状态文件不得包含 token 原文：%s", path)
		}
	}
	info, err := os.Stat(fingerprintFile)
	if err != nil {
		t.Fatalf("读取指纹文件权限失败：%v", err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("指纹文件权限应为 0600，实际 %04o", got)
	}
}

func TestRegisterArtifactSecretDeduplicatesFingerprint(t *testing.T) {
	fingerprintFile, generatedFile, _ := configureArtifactSecretFiles(t)
	const token = "test-secret-repeat"
	captureStdout(t, func() {
		for range 2 {
			if err := registerArtifactSecret(token); err != nil {
				t.Fatalf("重复登记动态 token 失败：%v", err)
			}
		}
	})
	raw, err := os.ReadFile(fingerprintFile)
	if err != nil {
		t.Fatalf("读取指纹文件失败：%v", err)
	}
	if strings.Count(string(raw), "\n") != 1 {
		t.Fatalf("重复 token 的指纹必须去重：%q", raw)
	}
	assertFileContent(t, generatedFile, "generated\n", "重复 token 不得重复记录生成状态")
}

func TestRegisterArtifactSecretDoesNothingOutsideActions(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "false")
	output := captureStdout(t, func() {
		if err := registerArtifactSecret("test-secret-local"); err != nil {
			t.Fatalf("非 Actions 登记应直接 no-op：%v", err)
		}
	})
	if output != "" {
		t.Fatal("非 Actions 运行不应写 workflow command")
	}
}

func TestRegisterArtifactSecretFailsBeforeMaskWhenFingerprintCannotPersist(t *testing.T) {
	dir := t.TempDir()
	fingerprintPath := filepath.Join(dir, "fingerprints")
	if err := os.Mkdir(fingerprintPath, 0o700); err != nil {
		t.Fatalf("创建无效指纹路径失败：%v", err)
	}
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv(artifactSecretFingerprintFileEnv, fingerprintPath)
	t.Setenv(artifactSecretGeneratedFileEnv, filepath.Join(dir, "generated"))
	const token = "test-secret-persist-failure"
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	var registerErr error
	output := captureStdout(t, func() { registerErr = registerArtifactSecret(token) })
	if registerErr == nil {
		t.Fatal("指纹无法持久化时登记必须失败")
	}
	if output != "" {
		t.Fatal("指纹持久化失败前不得输出掩码或交付 token")
	}
	if strings.Contains(registerErr.Error(), token) || strings.Contains(registerErr.Error(), sum) {
		t.Fatalf("登记错误不得泄漏 token 或指纹：%v", registerErr)
	}
}

func TestCreateV2NamespaceDoesNotReturnTokenBeforeFingerprintPersists(t *testing.T) {
	const token = "test-secret-create-response"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":7,"accessToken":"` + token + `"}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	fingerprintPath := filepath.Join(dir, "fingerprints")
	if err := os.Mkdir(fingerprintPath, 0o700); err != nil {
		t.Fatalf("创建无效指纹路径失败：%v", err)
	}
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv(artifactSecretFingerprintFileEnv, fingerprintPath)
	t.Setenv(artifactSecretGeneratedFileEnv, filepath.Join(dir, "generated"))

	id, returnedToken, err := CreateV2Namespace(server.URL, "admin-token", "e2e", "test")
	if err == nil || id != 0 || returnedToken != "" {
		t.Fatalf("指纹持久化失败时不得返回 namespace token：id=%d token=%q err=%v", id, returnedToken, err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), sum) {
		t.Fatalf("创建 namespace 错误不得泄漏 token 或指纹：%v", err)
	}
}

func TestRegisterArtifactSecretRejectsCorruptFingerprintWithoutLeakingSecrets(t *testing.T) {
	fingerprintFile, _, _ := configureArtifactSecretFiles(t)
	const token = "test-secret-corrupt-file"
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	if err := os.WriteFile(fingerprintFile, []byte(sum+" invalid\n"), 0o600); err != nil {
		t.Fatalf("写入损坏指纹文件失败：%v", err)
	}
	var registerErr error
	output := captureStdout(t, func() { registerErr = registerArtifactSecret(token) })
	if registerErr == nil {
		t.Fatal("已有指纹文件损坏时登记必须 fail-close")
	}
	if output != "" {
		t.Fatal("损坏指纹文件修复前不得输出掩码")
	}
	if strings.Contains(registerErr.Error(), token) || strings.Contains(registerErr.Error(), sum) {
		t.Fatalf("损坏格式错误不得泄漏 token 或指纹：%v", registerErr)
	}
}

func TestScanRegisteredArtifactSecretsRejectsLeakedToken(t *testing.T) {
	repoRoot := t.TempDir()
	logDir := filepath.Join(repoRoot, ".tmp")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("创建日志目录失败：%v", err)
	}
	configureArtifactSecretFiles(t)
	const token = "test-secret-artifact-leak"
	if err := os.WriteFile(filepath.Join(logDir, "leak.log"), []byte(token), 0o600); err != nil {
		t.Fatalf("写入测试日志失败：%v", err)
	}
	captureStdout(t, func() {
		if err := registerArtifactSecret(token); err != nil {
			t.Fatalf("登记动态 token 失败：%v", err)
		}
	})
	err := scanRegisteredArtifactSecrets(repoRoot)
	if err == nil || !strings.Contains(err.Error(), "leak.log") {
		t.Fatalf("泄漏动态 token 应阻断归档：%v", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("扫描错误不得泄漏 token：%v", err)
	}
	if _, err := os.Stat(filepath.Join(logDir, "e2e-artifact-secret-leak")); err != nil {
		t.Fatalf("泄漏时必须创建阻断归档标记：%v", err)
	}
}

func TestRegisterArtifactSecretRejectsLineBreaksWithoutLeakingToken(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	token := "test-secret-line\nbreak"
	err := registerArtifactSecret(token)
	if err == nil {
		t.Fatal("包含换行的 token 必须被拒绝")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "test-secret-line") {
		t.Fatalf("错误不得包含 token：%v", err)
	}
}

func TestLoginOfflineAndCancelApplyDefaultDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	oldTimeout := adminRequestTimeout
	adminRequestTimeout = 50 * time.Millisecond
	t.Cleanup(func() { adminRequestTimeout = oldTimeout })

	tests := []struct {
		name string
		run  func() error
	}{
		{name: "login", run: func() error { _, err := Login(server.URL, "admin", "password"); return err }},
		{name: "offline", run: func() error { return OfflineInstance(server.URL, "admin-token", "e2e", "backend-1") }},
		{name: "cancel", run: func() error { return CancelOfflineInstance(server.URL, "admin-token", "e2e", "backend-1") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started := time.Now()
			err := test.run()
			if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
				t.Fatalf("请求应按默认 deadline 退出：%v", err)
			}
			if elapsed := time.Since(started); elapsed >= time.Second {
				t.Fatalf("默认 deadline 未及时取消请求，实际耗时 %s", elapsed)
			}
		})
	}
}

func TestAdminRequestsUseProcessGuard(t *testing.T) {
	tests := []struct {
		name string
		run  func(string, ProcessGuard) error
	}{
		{name: "login", run: func(base string, guard ProcessGuard) error {
			_, err := Login(base, "admin", "password", guard)
			return err
		}},
		{name: "offline", run: func(base string, guard ProcessGuard) error {
			return OfflineInstance(base, "admin-token", "e2e", "backend-1", guard)
		}},
		{name: "cancel", run: func(base string, guard ProcessGuard) error {
			return CancelOfflineInstance(base, "admin-token", "e2e", "backend-1", guard)
		}},
		{name: "admin-json", run: func(base string, guard ProcessGuard) error {
			return doAdminJSON(base, http.MethodGet, "/stuck", "admin-token", nil, http.StatusOK, nil, guard)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				<-r.Context().Done()
			}))
			defer server.Close()
			proc := startHelperGradleProc(t, "delay-half", ":agent-e2e:servePaper")

			started := time.Now()
			err := test.run(server.URL, proc)
			assertGuardedWaitError(t, started, err, proc)
			for _, secret := range []string{"password", "admin-token"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("guard 错误不得包含凭据 %q：%v", secret, err)
				}
			}
			_ = proc.Stop()
		})
	}
}

func configureArtifactSecretFiles(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	fingerprintFile := filepath.Join(dir, "fingerprints")
	generatedFile := filepath.Join(dir, "generated")
	envFile := filepath.Join(dir, "github-env")
	if err := os.WriteFile(envFile, []byte("KEEP=value\n"), 0o600); err != nil {
		t.Fatalf("预置 GITHUB_ENV 失败：%v", err)
	}
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_ENV", envFile)
	t.Setenv(artifactSecretFingerprintFileEnv, fingerprintFile)
	t.Setenv(artifactSecretGeneratedFileEnv, generatedFile)
	artifactSecretsMu.Lock()
	artifactSecrets = make(map[string]struct{})
	artifactSecretsMu.Unlock()
	return fingerprintFile, generatedFile, envFile
}

func assertFileContent(t *testing.T, path, want, message string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s：%v", message, err)
	}
	if string(raw) != want {
		t.Fatalf("%s：%q", message, raw)
	}
}

func captureStdout(t *testing.T, run func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("创建 stdout 捕获管道失败：%v", err)
	}
	oldStdout := os.Stdout
	os.Stdout = writer
	result := make(chan string, 1)
	go func() {
		raw, _ := io.ReadAll(reader)
		result <- string(raw)
	}()
	run()
	_ = writer.Close()
	os.Stdout = oldStdout
	output := <-result
	_ = reader.Close()
	return output
}

func emptyItemsServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
}

func assertGuardedWaitError(t *testing.T, started time.Time, err error, proc *GradleProc) {
	t.Helper()
	if err == nil {
		t.Fatal("Gradle 早退时等待应失败")
	}
	if elapsed := time.Since(started); elapsed >= 3*time.Second {
		t.Fatalf("guard 应在有界窗口内返回早退诊断，实际耗时 %s：%v", elapsed, err)
	}
	for _, want := range []string{proc.task, proc.stdoutPath, proc.stderrPath} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("guard 错误缺少 %q：%v", want, err)
		}
	}
}
