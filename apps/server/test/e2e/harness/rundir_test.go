//go:build e2e

package harness

import (
	"path/filepath"
	"testing"
)

func TestRunDirectoriesUseMcTestkitLayout(t *testing.T) {
	repoRoot := filepath.Join("D:", "workspace", "Beacon")
	base := filepath.Join(repoRoot, "apps", "agent", "agent-e2e", "build", "mc-testkit")

	if got, want := BackendRunDir(repoRoot), filepath.Join(base, "run"); got != want {
		t.Fatalf("backend 运行目录错误：want=%s got=%s", want, got)
	}
	if got, want := ProxyRunDir(repoRoot), filepath.Join(base, "run-proxy"); got != want {
		t.Fatalf("proxy 运行目录错误：want=%s got=%s", want, got)
	}
}

func TestAgentGradleEnvKeepsCredentialsOutOfArguments(t *testing.T) {
	env := AgentGradleEnv("http://127.0.0.1:8848", "token-sentinel", "e2e", "backend-1", "127.0.0.1:25566")
	want := map[string]string{
		"BEACON_AGENT_BEACON_ENDPOINTS":       "http://127.0.0.1:8848",
		"BEACON_AGENT_BEACON_BOOTSTRAP_TOKEN": "token-sentinel",
		"BEACON_AGENT_IDENTITY_NAMESPACE":     "e2e",
		"BEACON_AGENT_IDENTITY_SERVER_ID":     "backend-1",
		"BEACON_AGENT_IDENTITY_ADDRESS":       "127.0.0.1:25566",
	}
	for key, value := range want {
		if env[key] != value {
			t.Fatalf("环境变量 %s 不符：want=%q got=%q", key, value, env[key])
		}
	}
}

func TestDirectoryGradleEnvSeparatesBackendAndProxyIdentity(t *testing.T) {
	env := DirectoryGradleEnv(
		"http://127.0.0.1:8848", "token-sentinel", "e2e",
		"backend-1", "127.0.0.1:25566", "proxy-1", "127.0.0.1:25577",
	)
	want := map[string]string{
		"BEACON_E2E_BACKEND_SERVER_ID": "backend-1",
		"BEACON_E2E_BACKEND_ADDRESS":   "127.0.0.1:25566",
		"BEACON_E2E_PROXY_SERVER_ID":   "proxy-1",
		"BEACON_E2E_PROXY_ADDRESS":     "127.0.0.1:25577",
	}
	for key, value := range want {
		if env[key] != value {
			t.Fatalf("directory 环境变量 %s 不符：want=%q got=%q", key, value, env[key])
		}
	}
}
