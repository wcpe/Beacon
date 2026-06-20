//go:build e2e

package harness

import (
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

// BeaconEndpointProp 返回透传给 gradle 任务的 -Pe2eBeaconEndpoint 属性，
// 让真机 agent（runServer/runBungee）连到与测试一致的控制面地址。
func BeaconEndpointProp() string {
	return "-Pe2eBeaconEndpoint=" + BeaconURL()
}
