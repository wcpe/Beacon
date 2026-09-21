package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
)

// staleV2AuthStub 是 AgentV2Authenticator 桩：恒返回预置错误，验证 v1 数据面中间件对 v2 兼容鉴权结果状态的透传。
type staleV2AuthStub struct{ err error }

func (s staleV2AuthStub) AuthenticateAgentV2(_, _, _ string) error { return s.err }

// v2AuthAllowStub 是 AgentV2Authenticator 桩：恒放行，用于验证兼容鉴权分支的 context 语义。
type v2AuthAllowStub struct{}

func (v2AuthAllowStub) AuthenticateAgentV2(_, _, _ string) error { return nil }

// TestAgentTokenMiddlewareRejectsAnonymousWhenGlobalTokenEmpty 验证空全局 token 不得让匿名请求绕过鉴权。
func TestAgentTokenMiddlewareRejectsAnonymousWhenGlobalTokenEmpty(t *testing.T) {
	mw := agentTokenMiddleware("", nil)
	served := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/beacon/v1/agent/heartbeat", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("空全局 token 的匿名请求应返回 401，实际 %d", rec.Code)
	}
	if served {
		t.Fatal("空全局 token 的匿名请求不应放行到 next handler")
	}
}

// TestAgentTokenMiddlewarePreservesStaleReregister 回归（FR-177 真机缺口）：
// v1 数据面中间件在 agent token 不匹配、转 v2 兼容鉴权失败时，必须透传 v2 的真实错误状态
// （陈旧 boot → 404 促重注册），不得吞成固定 401——否则 agent 心跳收不到 404、
// 无法据此触发重注册喂养并发双实例往复检测（spec §4.5）。
func TestAgentTokenMiddlewarePreservesStaleReregister(t *testing.T) {
	mw := agentTokenMiddleware("global-agent-token", staleV2AuthStub{err: apperr.ErrAgentStaleReregister})
	served := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/beacon/v1/agent/heartbeat", nil)
	req.Header.Set("X-Beacon-Token", "namespace-token") // 不匹配全局 agent token → 走 v2 兼容鉴权
	req.Header.Set("X-Beacon-Identity", "id-1")
	req.Header.Set("X-Beacon-Boot", "stale-boot")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("陈旧 boot 应透传 404 促重注册，实际 %d", rec.Code)
	}
	if served {
		t.Fatal("鉴权失败不应放行到 next handler")
	}
}

// TestAgentTokenMiddlewareMatchingTokenPasses agent token 匹配全局 → 直接放行（不走 v2 兼容鉴权、不受透传改动影响）。
func TestAgentTokenMiddlewareMatchingTokenPasses(t *testing.T) {
	mw := agentTokenMiddleware("global-agent-token", staleV2AuthStub{err: apperr.ErrAgentStaleReregister})
	served := false
	h := mw(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { served = true }))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/beacon/v1/agent/heartbeat", nil)
	req.Header.Set("X-Beacon-Token", "global-agent-token")
	h.ServeHTTP(rec, req)

	if !served {
		t.Fatal("匹配全局 agent token 应直接放行")
	}
}

// TestAgentTokenMiddlewareMarksTrustedInternal 守护 FR-222：命中共享 token 的请求带「受信内部调用方」标记，
// 机器注册分支据此判定（而非请求体）；v2 兼容鉴权放行与鉴权失败分支一律不带标记。
func TestAgentTokenMiddlewareMarksTrustedInternal(t *testing.T) {
	cases := []struct {
		name        string
		token       string
		headerToken string
		v2          AgentV2Authenticator
		wantTrusted bool
		wantServed  bool
	}{
		{name: "命中共享 token", token: "global-agent-token", headerToken: "global-agent-token", wantTrusted: true, wantServed: true},
		{name: "v2 兼容鉴权放行", token: "global-agent-token", headerToken: "namespace-token", v2: nil, wantServed: false},
		{name: "token 不匹配且无 v2 鉴权", token: "global-agent-token", headerToken: "wrong", wantServed: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			served, trusted := false, false
			h := agentTokenMiddleware(tc.token, tc.v2)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				served, trusted = true, agentauth.IsTrustedInternal(r.Context())
			}))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/beacon/v2/agent/register", nil)
			req.Header.Set("X-Beacon-Token", tc.headerToken)
			h.ServeHTTP(rec, req)

			if served != tc.wantServed {
				t.Fatalf("放行与否不符：served=%v want=%v（状态 %d）", served, tc.wantServed, rec.Code)
			}
			if tc.wantServed && trusted != tc.wantTrusted {
				t.Fatalf("受信内部调用方标记应为 %v，实际 %v", tc.wantTrusted, trusted)
			}
		})
	}
}

// TestAgentTokenMiddlewareV2PathNotTrusted v2 已确认身份访问（走兼容鉴权）不得被标成受信内部调用方。
func TestAgentTokenMiddlewareV2PathNotTrusted(t *testing.T) {
	served, trusted := false, true
	h := agentTokenMiddleware("global-agent-token", v2AuthAllowStub{})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		served, trusted = true, agentauth.IsTrustedInternal(r.Context())
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/beacon/v1/agent/heartbeat", nil)
	req.Header.Set("X-Beacon-Token", "namespace-token")
	req.Header.Set("X-Beacon-Identity", "id-1")
	req.Header.Set("X-Beacon-Boot", "boot-1")
	h.ServeHTTP(rec, req)

	if !served {
		t.Fatal("v2 已确认身份应放行")
	}
	if trusted {
		t.Fatal("agent 自持身份不得被标成受信内部调用方（否则可绕过人工审批）")
	}
}
