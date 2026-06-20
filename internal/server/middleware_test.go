package server

import (
	"net/http"
	"testing"
)

// TestIsWriteMethod 校验写方法判定：只读拒写裁决据此放行 GET/HEAD/OPTIONS、拦 POST/PUT/PATCH/DELETE。
func TestIsWriteMethod(t *testing.T) {
	cases := []struct {
		method string
		want   bool
	}{
		{http.MethodGet, false},
		{http.MethodHead, false},
		{http.MethodOptions, false},
		{http.MethodPost, true},
		{http.MethodPut, true},
		{http.MethodPatch, true},
		{http.MethodDelete, true},
	}
	for _, c := range cases {
		if got := isWriteMethod(c.method); got != c.want {
			t.Fatalf("isWriteMethod(%q) = %v，期望 %v", c.method, got, c.want)
		}
	}
}
