package metrics

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// bearerPrefix 是标准 Authorization 头的凭据方案前缀（大小写不敏感比对）。
const bearerPrefix = "bearer "

// EndpointHandler 返回抓取端点的完整处理器：指标输出 + 可选 Bearer 鉴权（见 ADR-0090）。
//
// token 为空 = 不鉴权，直接返回指标 handler——此时收敛完全依赖监听地址（默认只绑回环）。
// token 非空 = 要求标准 `Authorization: Bearer <token>`，缺失或不匹配一律 401。
//
// 为什么用「空 token 即放行」而不是「强制鉴权」：内网同机抓取（Prometheus 与控制面同机、
// 或经反代收敛）是最常见形态，强制鉴权会破坏开箱即用；且本端点的默认监听地址已是回环，
// 未设凭据时的暴露面本就收敛。这与 Grafana / Gitea 等同类端点的既有做法一致。
func (m *Metrics) EndpointHandler(token string) http.Handler {
	h := m.Handler()
	token = strings.TrimSpace(token)
	if token == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hasValidBearer(r.Header.Get("Authorization"), token) {
			// 不带 WWW-Authenticate：不向外透露本端点期望的凭据方案（少给探测者一个信息点）。
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// hasValidBearer 校验 Authorization 头是否为 `Bearer <token>`。
//
// 两点刻意为之：
//   - **恒定时间比较**（subtle.ConstantTimeCompare）：逐字节比较会在首个不同字节处提前返回，
//     使攻击者能靠响应耗时差逐位猜出凭据。仓内管理面口令校验同样用该函数（internal/auth）。
//   - **不先判长度再比较**：ConstantTimeCompare 对长度不等会立即返回 0，这本身会泄露长度信息；
//     但对固定长度的随机 token 而言该泄露无实际价值，且提前返回长度差的开销量级远小于一次 HTTP
//     往返的抖动，实际不可观测。此处沿用仓内既有写法，不为理论上的极微泄露引入非常规实现。
func hasValidBearer(header, token string) bool {
	// 方案名大小写不敏感（RFC 7235：scheme 是 case-insensitive），凭据部分保持大小写敏感。
	if len(header) < len(bearerPrefix) || !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return false
	}
	got := strings.TrimSpace(header[len(bearerPrefix):])
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}
