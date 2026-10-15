//go:build e2e

package harness

import (
	"net"
	"strings"
	"testing"
)

// TestMetricsListenAddrFromEnvAllocatesBindableAddr 锁定 e2e 的抓取端点端口分配可用。
//
// 为什么这条必须存在：抓取端点默认监听 `127.0.0.1:8849` 是**固定**地址，而每个 e2e 用例
// 会起一个独立控制面进程；控制面在 bind 失败时**直接终止启动**（刻意的 fail-fast，见 ADR-0090）。
// 故若分配逻辑退化（例如退化成恒返回默认固定地址），e2e 在并行跑多个包时会出现
// 「控制面起不来」的偶发失败——而且只在特定并行顺序下复现，极难排查。
// 本用例至少保证：分配出来的地址是 host:port 形式、且**当下真的可被 bind**。
func TestMetricsListenAddrFromEnvAllocatesBindableAddr(t *testing.T) {
	addr := MetricsListenAddrFromEnv()

	if !strings.Contains(addr, ":") {
		t.Fatalf("分配结果应为 host:port 形式，实际 %q", addr)
	}
	// 分配后端口应已被释放、可立即 bind（否则控制面会启动失败）。
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("分配出的地址 %q 应可立即 bind，实际失败：%v", addr, err)
	}
	_ = listener.Close()

	// 显式 env 优先：CI 或本地指定固定地址时应原样采用，不被自动探测覆盖。
	t.Setenv("E2E_METRICS_LISTEN_ADDR", "127.0.0.1:39123")
	if got := MetricsListenAddrFromEnv(); got != "127.0.0.1:39123" {
		t.Fatalf("设置 E2E_METRICS_LISTEN_ADDR 后应原样采用，实际 %q", got)
	}
}
