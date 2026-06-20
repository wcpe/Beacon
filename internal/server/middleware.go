// Package server 是 HTTP 装配层：中间件与路由注册。
package server

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"beacon/internal/apikey"
	"beacon/internal/apperr"
	"beacon/internal/auth"
	"beacon/internal/model"
	"beacon/internal/render"
)

// bearerPrefix 是 Authorization 头的 Bearer 方案前缀。
const bearerPrefix = "Bearer "

// apiKeyHeader 是 API 密钥的独立请求头（与 Authorization: Bearer <bk_...> 二选一，FR-42）。
const apiKeyHeader = "X-Beacon-Api-Key"

// ApiKeyVerifier 校验明文 API 密钥并返回认证身份与角色（由 service 实现，中间件依赖此接口）。
// 失败返回错误：ErrAdminUnauthorized（缺失 / 错误 / 过期 / 吊销）→401，DB 故障 → 500。
type ApiKeyVerifier interface {
	Verify(rawKey string) (principal string, role string, err error)
}

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

// adminAuthMiddleware 校验管理台身份并注入操作者 + 角色到 context（FR-42）。
// 支持两类凭据：① 登录令牌（Authorization: Bearer <HMAC 令牌>，角色恒为 full）；
// ② API 密钥（X-Beacon-Api-Key: <bk_...> 或 Authorization: Bearer <bk_...>，角色随其落库记录）。
// 通过则注入认证身份（写操作审计取用）与角色（只读拒写裁决取用）；缺/错凭据一律 401。
func adminAuthMiddleware(authn *auth.Authenticator, apiKeys ApiKeyVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// ① 独立密钥头优先
			if raw := strings.TrimSpace(r.Header.Get(apiKeyHeader)); raw != "" {
				authenticateApiKey(w, r, next, apiKeys, raw)
				return
			}
			// ② Authorization: Bearer <凭据>
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, bearerPrefix) {
				render.WriteError(w, r, apperr.ErrAdminUnauthorized)
				return
			}
			credential := strings.TrimSpace(strings.TrimPrefix(header, bearerPrefix))
			// bk_ 前缀的按 API 密钥校验，否则按登录令牌校验（两类形态不相交）
			if apikey.HasPrefix(credential) {
				authenticateApiKey(w, r, next, apiKeys, credential)
				return
			}
			operator, err := authn.Verify(credential)
			if err != nil {
				slog.Warn("管理台令牌校验失败", "路径", r.URL.Path, "原因", err, "traceId", render.TraceID(r.Context()))
				render.WriteError(w, r, apperr.ErrAdminUnauthorized)
				return
			}
			// 登录操作者恒为 full 角色（等同现操作者）
			ctx := auth.WithRole(auth.WithOperator(r.Context(), operator), model.RoleFull)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// authenticateApiKey 校验 API 密钥并注入身份 + 角色；失败按 Verify 返回的错误响应（401/500）。
func authenticateApiKey(w http.ResponseWriter, r *http.Request, next http.Handler, apiKeys ApiKeyVerifier, raw string) {
	principal, role, err := apiKeys.Verify(raw)
	if err != nil {
		slog.Warn("管理台 API 密钥校验失败", "路径", r.URL.Path, "原因", err, "traceId", render.TraceID(r.Context()))
		render.WriteError(w, r, err)
		return
	}
	ctx := auth.WithRole(auth.WithOperator(r.Context(), principal), role)
	next.ServeHTTP(w, r.WithContext(ctx))
}

// readonlyWriteGuard 统一裁决"只读拒写"：readonly 角色访问写方法端点一律 403（FR-42）。
// 放在鉴权中间件之后（角色已注入 context）；handler 不再散落角色判断。
func readonlyWriteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.Role(r.Context()) == model.RoleReadonly && isWriteMethod(r.Method) {
			render.WriteError(w, r, apperr.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isWriteMethod 判断 HTTP 方法是否为写操作（POST/PUT/PATCH/DELETE）；纯函数，便于单测。
func isWriteMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
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

// Flush 透传到底层 Flusher，支持 SSE 等流式响应（FR-24 单条推送流）。
// 否则本包装器内嵌的是 http.ResponseWriter 接口、不含 Flush，会遮蔽底层 Flusher，
// 致 StreamHandler 的 w.(http.Flusher) 断言失败、SSE 端点在访问日志中间件后返回 500。
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
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
