package server

import (
	"net/http"
	"net/http/pprof"
	runtimepprof "runtime/pprof"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/wcpe/Beacon/apps/server/internal/auth"
)

// debugPprofPrefix 是运行时诊断（pprof）端点的统一前缀。
//
// 刻意沿用 Go 标准库与 `go tool pprof` 默认的 /debug/pprof/：换前缀会让运维每次排障都要改命令行，
// 也会让现成的排障文档失效。
const debugPprofPrefix = "/debug/pprof"

// debugProfileNames 返回**当前运行时实际存在**的 profile 名（取自 runtime/pprof.Profiles()）。
//
// 为什么动态取而非硬编码：硬编码列表会与运行时脱节——Go 每新增一个 profile（如 1.27 的
// `goroutineleak`），索引页会自动列出它，但未注册的路由会落进兜底分支（修复前是 200+HTML
// 的 SPA 回退，现为显式 404），形成「索引页有链接、点进去取不到」的取证陷阱。
// 动态取保证「索引页所列 == 路由已注册」，两者永不失配。
func debugProfileNames() []string {
	profiles := runtimepprof.Profiles()
	names := make([]string, 0, len(profiles))
	for _, p := range profiles {
		names = append(names, p.Name())
	}
	sort.Strings(names)
	return names
}

// registerDebugRoutes 注册控制面运行时诊断端点（pprof）。
//
// 缺陷背景：事故排查时 `GET /debug/pprof/goroutine` 返回 200，但内容其实是内嵌前端的 index.html——
// `/debug/*` 从未被注册，请求一路落到 `r.NotFound(h.Web.ServeHTTP)` 的 SPA history 回退，
// 于是「HTTP 200 + HTML」被误读成「拿到了转储」。后果是环图只能靠代码推理 + 测试复现，无生产栈佐证。
//
// 安全边界（生产控制面，见 SECURITY.md「建议不要把管理端口暴露到公网」）：
//   - 复用管理面既有鉴权中间件（登录令牌 / API 密钥），**不裸暴露**；
//   - 再叠 requireFullRole：pprof 暴露的是进程内部态（堆内容可能含内存中的配置明文与凭据），
//     且 CPU profile / trace 属「方法为 GET 但有真实副作用」（会长时间占用 CPU、heap 取样会触发 GC），
//     与仓库既有「GET 但有写副作用者显式挂 requireFullRole」的归类一致，故挡掉 readonly 角色与只读密钥。
//
// 路由注册在 `r.NotFound` 之前，chi 匹配具名路由优先，故不会再被 SPA 回退吞掉。
func registerDebugRoutes(r chi.Router, authn *auth.Authenticator, apiKeys APIKeyVerifier) {
	r.Group(func(r chi.Router) {
		r.Use(adminAuthMiddleware(authn, apiKeys))
		r.Use(requireFullRole)

		// 无尾斜杠入口重定向到标准前缀（`go tool pprof` 与手工 curl 都可能省掉斜杠）。
		r.Get(debugPprofPrefix, func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, debugPprofPrefix+"/", http.StatusMovedPermanently)
		})
		// 索引页：列出各 profile 当前样本数与说明。
		// 注意 pprof.Index 内部按 `r.URL.Path` 前缀分发子 profile；本处各子路径已显式注册，
		// 匹配具名路由优先，Index 只承担索引页职责。
		r.Get(debugPprofPrefix+"/", pprof.Index)
		r.Get(debugPprofPrefix+"/cmdline", pprof.Cmdline)
		r.Get(debugPprofPrefix+"/profile", pprof.Profile)
		// symbol 必须同时接受 POST：`go tool pprof` 远程符号化固定走 POST
		// （Go 标准库 internal/symbolizer 的 symbolizer.go）；只注册 GET 会让
		// 「取到转储但函数名全空」——恰是本端点要解决的取证缺口。
		// 标准库 pprof.Symbol 自身两种方法都支持，故一并注册。
		r.Get(debugPprofPrefix+"/symbol", pprof.Symbol)
		r.Post(debugPprofPrefix+"/symbol", pprof.Symbol)
		r.Get(debugPprofPrefix+"/trace", pprof.Trace)
		for _, name := range debugProfileNames() {
			// 显式 GET：Go 1.22 起标准库 pprof 各路径本就只接受 GET；显式声明避免全方法注册。
			r.Get(debugPprofPrefix+"/"+name, pprof.Handler(name).ServeHTTP)
		}
		// 未登记的 profile 名（如运行时新增 `goroutineleak`）**显式 404**，不得落到
		// `r.NotFound` 的 SPA history 回退——否则又是「200 + index.html」被误读成
		// 「已拿到转储」，正是 2026-10-10 事故取证失灵的原样复现。
		// 索引页会列出运行时实际存在的 profile，故这里必须兜住索引页与注册表的差集。
		r.Get(debugPprofPrefix+"/*", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "未知的 pprof 端点（可用端点见 /debug/pprof/ 索引页）", http.StatusNotFound)
		})
	})
}
