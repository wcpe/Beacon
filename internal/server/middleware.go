// Package server 是 HTTP 装配层：中间件与路由注册。
package server

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"beacon/internal/apperr"
	"beacon/internal/auth"
	"beacon/internal/render"
)

// bearerPrefix 是 Authorization 头的 Bearer 方案前缀。
const bearerPrefix = "Bearer "

// agentTokenMiddleware 校验 agent 端共享 token（仅防误连，非安全边界）。
// token 为空表示停用校验（开发场景）。
func agentTokenMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token != "" && r.Header.Get("X-Beacon-Token") != token {
				render.WriteError(w, r, apperr.ErrUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// adminAuthMiddleware 校验管理台登录令牌（Authorization: Bearer <token>），
// 通过则把认证操作者身份注入 context 供写操作审计取用；缺/错令牌一律 401。
func adminAuthMiddleware(authn *auth.Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, bearerPrefix) {
				render.WriteError(w, r, apperr.ErrAdminUnauthorized)
				return
			}
			token := strings.TrimSpace(strings.TrimPrefix(header, bearerPrefix))
			operator, err := authn.Verify(token)
			if err != nil {
				slog.Warn("管理台令牌校验失败", "路径", r.URL.Path, "原因", err, "traceId", render.TraceID(r.Context()))
				render.WriteError(w, r, apperr.ErrAdminUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithOperator(r.Context(), operator)))
		})
	}
}

// traceMiddleware 为每个请求生成 traceId，注入 context 并回写响应头。
func traceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid := newTraceID()
		w.Header().Set("X-Trace-Id", tid)
		next.ServeHTTP(w, r.WithContext(render.WithTraceID(r.Context(), tid)))
	})
}

// newTraceID 生成 16 位十六进制 traceId。
func newTraceID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// statusWriter 包装 ResponseWriter 以记录响应状态码供访问日志使用。
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// accessLog 输出中文访问日志。
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		slog.Info("访问",
			"方法", r.Method,
			"路径", r.URL.Path,
			"状态", sw.status,
			"耗时", time.Since(start).String(),
			"traceId", render.TraceID(r.Context()),
		)
	})
}

// recoverMiddleware 捕获 panic，避免单个请求拖垮进程。
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("请求处理 panic", "路径", r.URL.Path, "panic", rec)
				render.WriteJSON(w, http.StatusInternalServerError, map[string]string{
					"code":    "INTERNAL",
					"message": "内部错误",
					"traceId": render.TraceID(r.Context()),
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
