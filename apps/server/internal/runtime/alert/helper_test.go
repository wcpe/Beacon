package alert

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

// newTestServer 起一个仅供 webhook 测试用的 HTTP 服务，回调收到的 Content-Type 与请求体。
func newTestServer(onPost func(contentType string, body []byte)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		onPost(r.Header.Get("Content-Type"), body)
		w.WriteHeader(http.StatusOK)
	}))
}

// containsAll 判断 s 是否包含全部子串。
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
