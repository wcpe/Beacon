// Package embedweb 提供服务内嵌前端的 HTTP 处理器，实现 SPA history 回退。
package embedweb

import (
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/wcpe/Beacon/apps/server/internal/httpx"
)

// Handler 返回服务内嵌前端的 http.Handler：
// 命中静态文件则返回文件，否则回退 index.html（交给前端路由）。
// dist 应已去除内嵌前缀（调用方用 fs.Sub 处理）。
//
// 全程用 httpx.StallWriter 包装响应：前端静态资源 / index 回写对慢客户端写阻塞兜底，
// 防止浏览器 / 代理接收极慢时 goroutine 永久挂起（FD 耗尽）。SSE 不走本 handler，不受影响。
func Handler(dist fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := httpx.NewStallWriter(w, httpx.DefaultWriteStall)
		defer sw.Release()
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := dist.Open(p); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(sw, r)
			return
		}
		serveIndex(sw, r, dist)
	})
}

// serveIndex 回写 index.html；无构建产物（仅占位）时返回 404。
func serveIndex(w http.ResponseWriter, r *http.Request, dist fs.FS) {
	index, err := dist.Open("index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = index.Close() }()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.Copy(w, index)
}
