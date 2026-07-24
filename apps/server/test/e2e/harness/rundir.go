//go:build e2e

package harness

import "path/filepath"

const (
	backendServerIDEnv = "BEACON_E2E_BACKEND_SERVER_ID"
	backendAddressEnv  = "BEACON_E2E_BACKEND_ADDRESS"
	proxyServerIDEnv   = "BEACON_E2E_PROXY_SERVER_ID"
	proxyAddressEnv    = "BEACON_E2E_PROXY_ADDRESS"
)

// BackendRunDir 返回 mc-testkit 的 Paper 后端运行目录。
func BackendRunDir(repoRoot string) string {
	return filepath.Join(mcTestkitDir(repoRoot), "run")
}

// ProxyRunDir 返回 mc-testkit 的 BungeeCord 代理运行目录。
func ProxyRunDir(repoRoot string) string {
	return filepath.Join(mcTestkitDir(repoRoot), "run-proxy")
}

// AgentGradleEnv 组装单节点 serve 任务继承的 Agent 环境。
func AgentGradleEnv(endpoint, token, namespace, serverID, address string) map[string]string {
	env := commonAgentGradleEnv(endpoint, token, namespace)
	env["BEACON_AGENT_IDENTITY_SERVER_ID"] = serverID
	env["BEACON_AGENT_IDENTITY_ADDRESS"] = address
	return env
}

// DirectoryGradleEnv 为同一 serveDirectory 进程分别声明后端与代理身份。
func DirectoryGradleEnv(
	endpoint, token, namespace string,
	backendServerID, backendAddress string,
	proxyServerID, proxyAddress string,
) map[string]string {
	env := commonAgentGradleEnv(endpoint, token, namespace)
	env[backendServerIDEnv] = backendServerID
	env[backendAddressEnv] = backendAddress
	env[proxyServerIDEnv] = proxyServerID
	env[proxyAddressEnv] = proxyAddress
	return env
}

func mcTestkitDir(repoRoot string) string {
	return filepath.Join(repoRoot, "apps", "agent", "agent-e2e", "build", "mc-testkit")
}

func commonAgentGradleEnv(endpoint, token, namespace string) map[string]string {
	return map[string]string{
		"BEACON_AGENT_BEACON_ENDPOINTS":       endpoint,
		"BEACON_AGENT_BEACON_BOOTSTRAP_TOKEN": token,
		"BEACON_AGENT_IDENTITY_NAMESPACE":     namespace,
	}
}
