//go:build e2e

package harness

import (
	"net"
	"os"
	"strings"
)

// 默认控制面地址：本地开发模式（SQLite）控制面默认 bind 的地址与端口。
const defaultBeaconURL = "http://localhost:8848"

// BeaconURL 返回 E2E 控制面地址：优先取环境变量 E2E_BEACON_URL，
// 为空时回退默认 http://localhost:8848（保持 CI 行为不变）。
// 本地有别的实例占着 8848 时，可经该 env 换端口跑而不改测试代码。
func BeaconURL() string {
	if v := os.Getenv("E2E_BEACON_URL"); v != "" {
		return v
	}
	return defaultBeaconURL
}

// MetricsListenAddrFromEnv 返回抓取端点监听地址，供 e2e 注入控制面的
// BEACON_METRICS_ENDPOINT_LISTEN_ADDR（见 ADR-0090）。
//
// 为什么 e2e 必须显式注入、不能靠控制面默认值：抓取端点的默认地址是固定的
// `127.0.0.1:8849`，而一个 e2e 用例会起一个**独立控制面进程**。多个 e2e 包并行跑
// （或本机已有实例占着 8849）时，后起的进程会因 bind 失败而**直接启动失败**——
// 这是刻意的 fail-fast（见 ADR-0090：静默降级会让"以为在抓、实际没在抓"更难排查），
// 但测试侧必须配合分配不冲突的端口。
//
// 分配策略：优先用 E2E_METRICS_LISTEN_ADDR（CI 或本地显式指定时用），
// 否则探一个本机空闲端口。探到后**立即释放**交给控制面 bind——存在理论上的竞态窗口
// （释放到 bind 之间被他人抢走），但 e2e 场景下并发度极低，且真撞上会以启动失败
// 明确暴露、而非静默错抓，故可接受。
func MetricsListenAddrFromEnv() string {
	if v := os.Getenv("E2E_METRICS_LISTEN_ADDR"); v != "" {
		return v
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		// 探不到端口时退回固定地址：让控制面自己 bind 失败并暴露问题，而不是悄悄不监听。
		return "127.0.0.1:8849"
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

// HTTPAddrFromURL 从形如 http://host:port 的地址提取 bind 用的 ":port"，
// 用于设控制面 BEACON_HTTP_ADDR，使其 bind 端口与测试连接的 BaseURL 一致。
// 取最后一个冒号后的部分作端口（避开 scheme 的 "://"），解析不出则回退 ":8848"。
func HTTPAddrFromURL(url string) string {
	idx := strings.LastIndex(url, ":")
	if idx < 0 {
		return ":8848"
	}
	port := url[idx+1:]
	if port == "" {
		return ":8848"
	}
	return ":" + port
}
