package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/roster"
)

// rosterAgentRequest 构造带已鉴权身份的请求（模拟 agentV2ReportMiddleware 注入的权威身份）。
func rosterAgentRequest(nsID uint, ns, target string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	id := agentauth.Identity{NamespaceID: nsID, Namespace: ns, ServerID: "req-1", Kind: "backend"}
	return req.WithContext(agentauth.WithIdentity(req.Context(), id))
}

// TestV2RosterHandlerServesOwnNamespaceOnly 名册读端点只返回调用方所属 namespace 的具名条目：
// 异域玩家不得越界出现，匿名条目（无玩家名，无法按名寻址）不进名册。
func TestV2RosterHandlerServesOwnNamespaceOnly(t *testing.T) {
	store := roster.NewStore()
	store.ApplyOpen(1, "uuid-alice", "Alice", "c1", "game-3")
	store.ApplyOpen(1, "uuid-noname", "", "c2", "game-9") // 匿名条目：不进名册
	store.ApplyOpen(2, "uuid-bob", "Bob", "c3", "game-5") // 异域玩家：不得越界

	rec := httptest.NewRecorder()
	NewV2RosterHandler(store).PlayerRoster(rec, rosterAgentRequest(1, "prod", "/beacon/v2/agent/player-roster"))

	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应非 json: %v", err)
	}
	assertKeys(t, body, "namespace", "count", "players")
	if body["namespace"] != "prod" || body["count"] != float64(1) {
		t.Fatalf("应回显本域 namespace=prod count=1，实际 %v", body)
	}
	players, ok := body["players"].(map[string]any)
	if !ok || len(players) != 1 || players["Alice"] != "game-3" {
		t.Fatalf("players 应只含本域 Alice→game-3，实际 %v", body["players"])
	}
}

// TestV2RosterHandlerEmptyWhenNobodyOnline 本域无人在线返回 200 + 空 players（{} 而非 null，也不是 404）：
// agent 侧据此把本地名册清空，与「端点不可用」区分开。
func TestV2RosterHandlerEmptyWhenNobodyOnline(t *testing.T) {
	store := roster.NewStore()
	store.ApplyOpen(2, "uuid-bob", "Bob", "c3", "game-5") // 只有异域有人

	rec := httptest.NewRecorder()
	NewV2RosterHandler(store).PlayerRoster(rec, rosterAgentRequest(1, "prod", "/beacon/v2/agent/player-roster"))

	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"players":{}`) {
		t.Fatalf("空名册必须序列化为 {}，实际 %s", rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应非 json: %v", err)
	}
	if body["count"] != float64(0) || body["namespace"] != "prod" {
		t.Fatalf("应 count=0 且仍回显 namespace，实际 %v", body)
	}
}

// TestV2RosterHandlerRejectsUnauthenticated 无注入身份（中间件未放行）时 401，且不得泄漏名册内容。
func TestV2RosterHandlerRejectsUnauthenticated(t *testing.T) {
	store := roster.NewStore()
	store.ApplyOpen(1, "uuid-alice", "Alice", "c1", "game-3")

	rec := httptest.NewRecorder()
	NewV2RosterHandler(store).PlayerRoster(rec, httptest.NewRequest(http.MethodGet, "/beacon/v2/agent/player-roster", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无身份应 401，实际 %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Alice") {
		t.Fatalf("未鉴权响应不得泄漏名册内容：%s", rec.Body.String())
	}
}
