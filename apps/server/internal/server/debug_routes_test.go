package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// debugTestAuth 构造测试用鉴权器（口令 / 密钥仅测试值）。
func debugTestAuth(t *testing.T) *auth.Authenticator {
	t.Helper()
	authn, err := auth.New("admin", "test-pass", "test-secret", time.Hour)
	if err != nil {
		t.Fatalf("构造鉴权器失败: %v", err)
	}
	return authn
}

// debugRejectAllKeys 是「任何密钥都不通过」的桩校验器：本文件只关心鉴权链是否挡住请求，
// 不关心密钥语义。传 nil 会因中间件解引用而 panic（生产恒有真实校验器），故测试必须给桩。
type debugRejectAllKeys struct{}

// Verify 一律返回 401 语义（与真实校验器对非法密钥的口径一致）。
func (debugRejectAllKeys) Verify(string) (auth.Principal, error) {
	return auth.Principal{}, apperr.ErrAdminUnauthorized
}

// debugTestRouter 装配带真实鉴权链的路由（Web 非 nil，否则 NotFound 回退会 panic）。
// 同时返回 chi.Routes（供遍历断言）与 http.Handler（供真实发起请求）。
func debugTestRouter(t *testing.T) (chi.Routes, http.Handler) {
	t.Helper()
	h := Handlers{Web: debugSPAFallback()}
	handler := NewRouter(h, "", debugTestAuth(t), debugRejectAllKeys{}, nil)
	routes, ok := handler.(chi.Routes)
	if !ok {
		t.Fatal("NewRouter 返回值应实现 chi.Routes")
	}
	return routes, handler
}

// debugSPAFallback 模拟内嵌前端的 SPA 回退：返回 200 + HTML。
// 这正是当年「拿到 200 却其实是 index.html」的成因，故测试里必须复现它，
// 才能证明诊断端点是真实路由而非被回退接走。
func debugSPAFallback() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<!doctype html><title>Beacon</title>"))
	})
}

// TestDebugPprofRoutesRegistered 守护运行时诊断端点确实被注册。
//
// 缺陷背景：`/debug/*` 此前从未注册，请求落到 NotFound 的 SPA 回退，返回 200 + index.html，
// 让排障者误以为已取到 goroutine 转储。本用例断言这些路径是**真实路由**，
// 且逐一覆盖标准库 pprof 的全部路径，避免将来被静默移除。
func TestDebugPprofRoutesRegistered(t *testing.T) {
	routes, _ := debugTestRouter(t)

	registered := map[string]struct{}{}
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		registered[method+" "+route] = struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("遍历路由失败: %v", err)
	}

	want := []string{
		http.MethodGet + " " + debugPprofPrefix,
		http.MethodGet + " " + debugPprofPrefix + "/",
		http.MethodGet + " " + debugPprofPrefix + "/cmdline",
		http.MethodGet + " " + debugPprofPrefix + "/profile",
		http.MethodGet + " " + debugPprofPrefix + "/symbol",
		// symbol 必须同时支持 POST：`go tool pprof` 远程符号化固定走 POST，
		// 只注册 GET 会让「取到转储但函数名全空」——恰是本端点要解决的取证缺口。
		http.MethodPost + " " + debugPprofPrefix + "/symbol",
		http.MethodGet + " " + debugPprofPrefix + "/trace",
		// 兜底通配：未登记的 profile 名（如运行时新增 goroutineleak）必须显式 404，
		// 不得落 SPA 回退返回 200 + HTML（事故取证失灵的原样复现）。
		http.MethodGet + " " + debugPprofPrefix + "/*",
	}
	for _, name := range debugProfileNames() {
		want = append(want, http.MethodGet+" "+debugPprofPrefix+"/"+name)
	}
	for _, key := range want {
		if _, ok := registered[key]; !ok {
			t.Errorf("缺少运行时诊断路由 %s", key)
		}
	}
}

// TestDebugPprofRequiresAuthentication 守护诊断端点不裸暴露：缺 / 错凭据一律 401，
// 绝不能被 SPA 回退接走而返回 200 + HTML（那正是事故时误判取到转储的原因）。
func TestDebugPprofRequiresAuthentication(t *testing.T) {
	_, handler := debugTestRouter(t)

	paths := []string{
		debugPprofPrefix,
		debugPprofPrefix + "/",
		debugPprofPrefix + "/goroutine",
		debugPprofPrefix + "/heap",
		debugPprofPrefix + "/profile",
		debugPprofPrefix + "/trace",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			cases := []struct {
				name   string
				header map[string]string
			}{
				{name: "无凭据"},
				{name: "错误令牌", header: map[string]string{"Authorization": "Bearer not-a-token"}},
				{name: "错误 API 密钥", header: map[string]string{"X-Beacon-Api-Key": "bk_forged"}},
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					req := httptest.NewRequest(http.MethodGet, path, nil)
					for k, v := range c.header {
						req.Header.Set(k, v)
					}
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, req)

					if w.Code != http.StatusUnauthorized {
						t.Fatalf("未鉴权访问 %s 应 401，实际 %d（正文前 80 字符: %.80q）",
							path, w.Code, w.Body.String())
					}
					if strings.Contains(w.Body.String(), "<!doctype html>") {
						t.Fatalf("未鉴权访问 %s 不应落到 SPA 回退：实际返回了 index.html", path)
					}
				})
			}
		})
	}
}

// TestDebugPprofAllowsDumpWithValidToken 守护「带有效登录令牌时确实能取到 goroutine 转储」——
// 这是本次修复的目标能力：事故时能拿到真实栈，而不是 200 + HTML。
func TestDebugPprofAllowsDumpWithValidToken(t *testing.T) {
	authn := debugTestAuth(t)
	token, err := authn.Login("admin", "test-pass")
	if err != nil {
		t.Fatalf("测试登录失败: %v", err)
	}
	h := Handlers{Web: debugSPAFallback()}
	routes, ok := NewRouter(h, "", authn, nil, nil).(chi.Routes)
	if !ok {
		t.Fatal("NewRouter 返回值应实现 chi.Routes")
	}
	handler := routes.(http.Handler)

	// 索引页：列出各 profile（HTML，含 goroutine 条目）。
	req := httptest.NewRequest(http.MethodGet, debugPprofPrefix+"/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("带有效令牌访问索引页应 200，实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "goroutine") {
		t.Fatalf("索引页应列出 goroutine profile，实际正文前 200 字符: %.200q", w.Body.String())
	}

	// goroutine 转储（debug=2）：必须是纯文本栈，而非 SPA 回退的 HTML。
	req = httptest.NewRequest(http.MethodGet, debugPprofPrefix+"/goroutine?debug=2", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("带有效令牌取 goroutine 转储应 200，实际 %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "goroutine ") {
		t.Fatalf("goroutine 转储应含栈信息，实际正文前 200 字符: %.200q", body)
	}
	if strings.Contains(body, "<!doctype html>") {
		t.Fatal("goroutine 转储不应是 SPA 回退的 HTML")
	}
}

// TestDebugPprofRejectsReadonlyRole 守护只读角色被挡：pprof 暴露进程内部态（堆可能含内存中的
// 敏感配置明文与凭据），且 CPU profile / trace 会长时间占用 CPU，故按仓库既有归类
// 「方法是 GET 但带真实副作用」显式挂 requireFullRole。
func TestDebugPprofRejectsReadonlyRole(t *testing.T) {
	ro := auth.APIKeyPrincipal("key-1", "只读脚本", model.RoleReadonly, "ref-1")
	nextCalled := false
	guarded := requireFullRole(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, debugPprofPrefix+"/goroutine", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), ro))
	w := httptest.NewRecorder()
	guarded.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("readonly 角色访问诊断端点应 403，实际 %d", w.Code)
	}
	if nextCalled {
		t.Fatal("readonly 角色不应进入诊断端点处理器")
	}
}

// TestDebugPprofDoesNotPolluteAdminRouteCatalog 守护诊断端点不落入管理面路由目录门禁：
// 它们挂在 /debug 前缀（非 /admin/v1|v2），故不参与授权描述符登记，也不该触发启动校验失败。
func TestDebugPprofDoesNotPolluteAdminRouteCatalog(t *testing.T) {
	routes, _ := debugTestRouter(t)
	for key := range walkedAdminRoutes(routes) {
		if strings.Contains(key, "/debug/") {
			t.Fatalf("诊断端点不应被当作管理面路由登记：%s", key)
		}
	}
}
