package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
)

// staleV2AuthStub 是 AgentV2Authenticator 桩：恒返回预置错误，验证 v1 数据面中间件对 v2 兼容鉴权结果状态的透传。
type staleV2AuthStub struct{ err error }

func (s staleV2AuthStub) AuthenticateAgentV2(_, _, _ string) error { return s.err }

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
