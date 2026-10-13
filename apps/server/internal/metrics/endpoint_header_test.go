package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestEndpointHandlerMultiValueAuthorizationHeader 覆盖「同一请求带多个 Authorization 头」的情形。
//
// 为什么单独锁这条：`http.Header.Get` 只返回**首个**值，故多值头既可能被用来尝试绕过鉴权
// （把错误凭据放首位、正确凭据放次位，寄望校验取到后者），也可能因实现取了末位而让
// 攻击者用「正确 + 任意」的组合通过。本用例钉死实际语义：**只看首个值**，
// 首个不合法即 401——不存在任何多值组合能绕过。
//
// 注意：Go 的 net/http 服务端在真实请求中会把重复头合并进同一个 key 的切片（不拒绝），
// 故这是可实际到达的输入形态，不是理论构造。
func TestEndpointHandlerMultiValueAuthorizationHeader(t *testing.T) {
	const token = "good-token"
	m := newEndpointTestMetrics() // 复用同包构造函数：放行分支会真的抓取指标，nil registry 会 panic

	tests := []struct {
		name    string
		headers []string
		want    int
	}{
		{
			name:    "首个正确、次位错误 → 放行（只看首个）",
			headers: []string{"Bearer " + token, "Bearer evil"},
			want:    http.StatusOK,
		},
		{
			name:    "首个错误、次位正确 → 拒绝（不能靠次位翻盘）",
			headers: []string{"Bearer evil", "Bearer " + token},
			want:    http.StatusUnauthorized,
		},
		{
			name:    "首个为空、次位正确 → 拒绝",
			headers: []string{"", "Bearer " + token},
			want:    http.StatusUnauthorized,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			for _, h := range tc.headers {
				r.Header.Add("Authorization", h)
			}
			w := httptest.NewRecorder()
			m.EndpointHandler(token).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("多值 Authorization=%q 应返回 %d，实际 %d", tc.headers, tc.want, w.Code)
			}
		})
	}
}
