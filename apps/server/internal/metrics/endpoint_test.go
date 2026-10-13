package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// newEndpointTestMetrics 构造一个带样本的指标集，使抓取输出非空、便于断言。
func newEndpointTestMetrics() *Metrics {
	m := New(runtime.NewRegistry())
	m.IncConfigPublish()
	return m
}

// fetchEndpoint 经 EndpointHandler 请求一次抓取端点，返回状态码与响应体。
func fetchEndpoint(t *testing.T, m *Metrics, token, authHeader string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	w := httptest.NewRecorder()
	m.EndpointHandler(token).ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// TestEndpointHandlerNoTokenAllowsAnonymous 锁定「未配置凭据即不鉴权」这一默认行为（见 ADR-0090）。
//
// 为什么默认必须是放行：内网同机抓取是主流形态，而端点的默认监听地址已是回环
// （127.0.0.1:8849），暴露面本就收敛；此时强制鉴权只会破坏开箱即用。
// 这条同时防止有人误把「可选加固」改成「强制鉴权」而破坏既有部署的抓取配置。
func TestEndpointHandlerNoTokenAllowsAnonymous(t *testing.T) {
	m := newEndpointTestMetrics()

	code, body := fetchEndpoint(t, m, "", "")
	if code != http.StatusOK {
		t.Fatalf("未配置凭据时应放行（200），实际 %d", code)
	}
	if !strings.Contains(body, "beacon_config_publish_total") {
		t.Fatalf("放行时应返回真实指标，实际响应：\n%s", body)
	}
}

// TestEndpointHandlerBearerRequired 覆盖配置凭据后的四种情形：正确 / 缺失 / 错误 / 方案不符。
func TestEndpointHandlerBearerRequired(t *testing.T) {
	const token = "s3cr3t-metrics-token"
	m := newEndpointTestMetrics()

	tests := []struct {
		name       string
		authHeader string
		wantCode   int
	}{
		{"正确凭据应放行", "Bearer " + token, http.StatusOK},
		{"方案名大小写不敏感（RFC 7235）", "bearer " + token, http.StatusOK},
		{"缺失 Authorization 头应拒绝", "", http.StatusUnauthorized},
		{"凭据错误应拒绝", "Bearer wrong-token", http.StatusUnauthorized},
		{"凭据少一位应拒绝（防前缀通过）", "Bearer " + token[:len(token)-1], http.StatusUnauthorized},
		{"凭据多一位应拒绝", "Bearer " + token + "x", http.StatusUnauthorized},
		{"方案不符（Basic）应拒绝", "Basic " + token, http.StatusUnauthorized},
		{"裸凭据无方案应拒绝", token, http.StatusUnauthorized},
		{"空凭据应拒绝", "Bearer ", http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, body := fetchEndpoint(t, m, token, tc.authHeader)
			if code != tc.wantCode {
				t.Fatalf("Authorization=%q 应返回 %d，实际 %d", tc.authHeader, tc.wantCode, code)
			}
			// 被拒时绝不能泄漏指标内容——否则鉴权形同虚设。
			if tc.wantCode == http.StatusUnauthorized && strings.Contains(body, "beacon_config_publish_total") {
				t.Fatalf("拒绝时不应返回任何指标内容，实际响应：\n%s", body)
			}
		})
	}
}

// TestEndpointHandlerRejectionDoesNotLeakScheme 锁定拒绝响应不透露期望的凭据方案。
//
// 返回 WWW-Authenticate 头会告诉探测者"本端点用 Bearer"，少给一个信息点。
func TestEndpointHandlerRejectionDoesNotLeakScheme(t *testing.T) {
	m := newEndpointTestMetrics()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	m.EndpointHandler("tok").ServeHTTP(w, req)

	if got := w.Header().Get("WWW-Authenticate"); got != "" {
		t.Fatalf("401 响应不应带 WWW-Authenticate（会透露凭据方案），实际 %q", got)
	}
}

// TestEndpointHandlerTrimsTokenWhitespace 锁定「仅空白的凭据等同未配置」。
//
// 运维在 yaml / env 里误留空格（`token: " "`）是常见笔误。若把它当作有效凭据，
// 端点会要求 `Bearer ` 这种几乎必然配错的头，抓取持续 401 而原因极难定位；
// 归一到"未配置"至少与直觉一致，且不会比误配更危险（默认监听地址是回环）。
func TestEndpointHandlerTrimsTokenWhitespace(t *testing.T) {
	m := newEndpointTestMetrics()
	code, _ := fetchEndpoint(t, m, "   ", "")
	if code != http.StatusOK {
		t.Fatalf("仅空白的凭据应等同未配置（放行），实际 %d", code)
	}
}
