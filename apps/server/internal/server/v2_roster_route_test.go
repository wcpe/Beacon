package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/handler"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/roster"
)

// rosterEndpointPath 是玩家名册读端点的规范路径（与 agent 侧调用方共用，契约冻结）。
const rosterEndpointPath = "/beacon/v2/agent/player-roster"

// rosterAuthStub 是 AgentV2ReportAuthenticator 桩：按 token 映射权威身份，未登记 token 一律 401。
type rosterAuthStub map[string]agentauth.Identity

func (s rosterAuthStub) AuthenticateAgentReport(token, _, _, _ string) (agentauth.Identity, error) {
	id, ok := s[token]
	if !ok {
		return agentauth.Identity{}, apperr.ErrUnauthorized
	}
	return id, nil
}

// newRosterAgentRouter 装配与生产同一条最小链路：真实路径 + 真实 agent 鉴权中间件 + 真实 handler，
// 且注册走生产同一个 registerV2RosterAgentRoutes（nil 守卫之外逻辑完全一致）。
// 用桩身份替代 DB 鉴权，使名册端点的隔离 / 鉴权语义可在不起库的前提下端到端验证。
func newRosterAgentRouter(store *roster.Store, authn AgentV2ReportAuthenticator) http.Handler {
	r := chi.NewRouter()
	r.Route("/beacon/v2/agent", func(r chi.Router) {
		registerV2RosterAgentRoutes(r, Handlers{V2Roster: handler.NewV2RosterHandler(store)}, authn)
	})
	return r
}

// rosterGet 以指定 token 请求名册端点（rawQuery 可为空，用于验证请求参数不参与归属判定）。
func rosterGet(t *testing.T, srv http.Handler, token, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, rosterEndpointPath+rawQuery, nil)
	if token != "" {
		req.Header.Set("X-Beacon-Token", token)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// decodeRoster 解析名册响应并逐键断言契约：只有 namespace / count / players 三键，
// count 与 players 条目数一致，players 恒为对象（空名册为 {} 而非 null）。
func decodeRoster(t *testing.T, rec *httptest.ResponseRecorder) (string, int, map[string]string) {
	t.Helper()
	var body struct {
		Namespace string            `json:"namespace"`
		Count     int               `json:"count"`
		Players   map[string]string `json:"players"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应非 json: %v（%s）", err, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("响应非 json 对象: %v（%s）", err, rec.Body.String())
	}
	if len(raw) != 3 {
		t.Fatalf("响应只应有 namespace/count/players 三键，实际 %v", raw)
	}
	for _, key := range []string{"namespace", "count", "players"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("响应缺少契约键 %q：%v", key, raw)
		}
	}
	if body.Players == nil {
		t.Fatalf("players 必须是对象（空名册为 {}），不得为 null：%s", rec.Body.String())
	}
	if body.Count != len(body.Players) {
		t.Fatalf("count(%d) 应与 players 条目数(%d) 一致：%s", body.Count, len(body.Players), rec.Body.String())
	}
	return body.Namespace, body.Count, body.Players
}

// TestPlayerRosterEndpointIsNamespaceIsolated 名册端点经真实路由 + 鉴权中间件请求：归属只取已鉴权身份，
// 返回且仅返回该 namespace 的具名在线玩家；异域 token 只看得到自己那域；伪造请求参数不改归属。
func TestPlayerRosterEndpointIsNamespaceIsolated(t *testing.T) {
	store := roster.NewStore()
	store.ApplyOpen(1, "uuid-alice", "Alice", "c1", "game-3")
	store.ApplyOpen(1, "uuid-noname", "", "c2", "game-9") // 匿名条目：不进名册
	store.ApplyOpen(2, "uuid-bob", "Bob", "c3", "game-5") // 异域玩家：不得越界

	srv := newRosterAgentRouter(store, rosterAuthStub{
		"token-ns1": {NamespaceID: 1, Namespace: "prod", ServerID: "proxy-1", Kind: "proxy"},
		"token-ns2": {NamespaceID: 2, Namespace: "dev", ServerID: "proxy-2", Kind: "proxy"},
	})

	// ① 参数伪造（namespace=dev / namespaceId=2）不得改变归属：仍只返回 token 所属的 prod
	rec := rosterGet(t, srv, "token-ns1", "?namespace=dev&namespaceId=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	ns, count, players := decodeRoster(t, rec)
	if ns != "prod" || count != 1 || len(players) != 1 || players["Alice"] != "game-3" {
		t.Fatalf("应只返回本域 prod 的 Alice→game-3，实际 ns=%s count=%d players=%v", ns, count, players)
	}

	// ② 异域 token 只看到自己那域（namespace 强隔离边界）
	rec = rosterGet(t, srv, "token-ns2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	ns, count, players = decodeRoster(t, rec)
	if ns != "dev" || count != 1 || len(players) != 1 || players["Bob"] != "game-5" {
		t.Fatalf("应只返回本域 dev 的 Bob→game-5，实际 ns=%s count=%d players=%v", ns, count, players)
	}
}

// TestPlayerRosterEndpointRejectsBadToken 缺 / 错 token 一律 401 且不进入 handler（响应不得含名册内容）。
func TestPlayerRosterEndpointRejectsBadToken(t *testing.T) {
	store := roster.NewStore()
	store.ApplyOpen(1, "uuid-alice", "Alice", "c1", "game-3")
	srv := newRosterAgentRouter(store, rosterAuthStub{
		"token-ns1": {NamespaceID: 1, Namespace: "prod", ServerID: "proxy-1", Kind: "proxy"},
	})

	cases := []struct{ name, token string }{
		{name: "缺 token", token: ""},
		{name: "错 token", token: "token-nope"},
		{name: "未登记 token", token: "token-ns9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := rosterGet(t, srv, tc.token, "")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("应 401，实际 %d：%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "Alice") {
				t.Fatalf("鉴权失败响应不得泄漏名册内容：%s", rec.Body.String())
			}
		})
	}
}

// TestPlayerRosterEndpointEmptyWhenNobodyOnline 本域无人在线 → 200 + 空 players（非 null、非 404）。
func TestPlayerRosterEndpointEmptyWhenNobodyOnline(t *testing.T) {
	store := roster.NewStore()
	store.ApplyOpen(2, "uuid-bob", "Bob", "c3", "game-5") // 只有异域有人
	srv := newRosterAgentRouter(store, rosterAuthStub{
		"token-ns1": {NamespaceID: 1, Namespace: "prod", ServerID: "proxy-1", Kind: "proxy"},
	})

	rec := rosterGet(t, srv, "token-ns1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200（不是 404——本域暂时无人服务在线但端点可用），实际 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"players":{}`) {
		t.Fatalf("空名册必须序列化为 {}，实际 %s", rec.Body.String())
	}
	ns, count, players := decodeRoster(t, rec)
	if ns != "prod" || count != 0 || len(players) != 0 {
		t.Fatalf("应 prod + count=0 + 空 players，实际 ns=%s count=%d players=%v", ns, count, players)
	}
}

// TestPlayerRosterRouteRegisteredWithAgentAuth 名册端点必须落在 /beacon/v2/agent 组内并挂 agent 鉴权中间件：
// 路由走查断言路径已装配，且其内联中间件链与同组的 /metrics/report 上报端点同构（同款 token↔namespace 鉴权）；
// 以同组无内联中间件的 /register 作对照，证明比的是「内联挂载」而非组级链。
func TestPlayerRosterRouteRegisteredWithAgentAuth(t *testing.T) {
	routes, ok := NewRouter(Handlers{
		V2:        &handler.V2ControlPlaneHandler{},
		V2Metrics: &handler.V2MetricsHandler{},
		V2Roster:  handler.NewV2RosterHandler(roster.NewStore()),
		Web:       http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
	}, "", nil, nil, nil).(chi.Routes)
	if !ok {
		t.Fatal("路由器应实现 chi.Routes")
	}

	mwCounts := map[string]int{}
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		mwCounts[method+" "+route] = len(mws)
		return nil
	}); err != nil {
		t.Fatalf("遍历路由失败: %v", err)
	}

	reported, bare := mwCounts["POST /beacon/v2/agent/metrics/report"], mwCounts["POST /beacon/v2/agent/register"]
	if reported == 0 || bare != reported-1 {
		t.Fatalf("对照组中间件链异常：上报端点 %d、无内联中间件的注册端点 %d", reported, bare)
	}
	if got, ok := mwCounts["GET "+rosterEndpointPath]; !ok {
		t.Fatalf("缺少名册读端点 %s", rosterEndpointPath)
	} else if got != reported {
		t.Fatalf("名册端点中间件链应与上报端点同构（%d 层），实际 %d 层", reported, got)
	}
}
