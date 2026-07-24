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
	_, err := WaitIdentityStatus(server.URL, "admin-token", 1, "backend-1", "pending", 5*time.Second, proc)
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
	_, err := WaitIdentityStatus(server.URL, "admin-token", 1, "backend-1", "pending", 5*time.Second, proc)
	assertGuardedWaitError(t, started, err, proc)
	proc.Stop()
}

func TestWaitIdentityStatusSelectsPendingIdentityInsteadOfOldActive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("namespaceId"); got != "7" {
			t.Errorf("namespaceId 应为 7，实际 %q", got)
		}
		if got := r.URL.Query().Get("status"); got != "pending" {
			t.Errorf("status 应为 pending，实际 %q", got)
		}
		if got := r.URL.Query().Get("keyword"); got != "backend-1" {
			t.Errorf("keyword 应为 backend-1，实际 %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"identityId":"old-active","serverId":"backend-1","status":"active"},{"identityId":"new-pending","serverId":"backend-1","status":"pending"}]}`))
	}))
	defer server.Close()

	identityID, err := WaitIdentityStatus(server.URL, "admin-token", 7, "backend-1", "pending", time.Second)
	if err != nil {
		t.Fatalf("等待本轮 pending identity 失败：%v", err)
	}
	if identityID != "new-pending" {
		t.Fatalf("应返回本轮 pending identity，实际 %q", identityID)
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

func TestApproveIdentityCanForceUnbindOccupier(t *testing.T) {
	forced := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ForceUnbindOccupier bool `json:"forceUnbindOccupier"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "审批请求体无效", http.StatusBadRequest)
			return
		}
		forced <- body.ForceUnbindOccupier
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"active"}`))
	}))
	defer server.Close()

	if err := ApproveIdentity(server.URL, "admin-token", "new-pending", true); err != nil {
		t.Fatalf("强制解绑旧占用者并审批失败：%v", err)
	}
	if !<-forced {
		t.Fatal("审批本轮新 identity 时应请求解绑旧 active 占用者")
	}
}

func TestApproveIdentityContextCancelsStuckRequestOnGradleExit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	proc := startHelperGradleProc(t, "delay-half", ":agent-e2e:servePaper")

	started := time.Now()
	err := ApproveIdentityContext(context.Background(), server.URL, "admin-token", "pending-1", proc)
	assertGuardedWaitError(t, started, err, proc)
	_ = proc.Stop()
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
