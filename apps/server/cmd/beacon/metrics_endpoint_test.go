package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/metrics"
	rt "github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// TestMetricsAuthEnabledTrimsWhitespace 锁定「启动日志的鉴权判据 == 实现判据」（见 ADR-0090）。
//
// 为什么是安全属性而非文案问题：EndpointHandler 先 TrimSpace 再判空，故纯空白 token 实际等价于
// 「不鉴权」。若日志判据用裸 `!= ""`，一个 `BEACON_METRICS_ENDPOINT_BEARER_TOKEN=" "` 的部署
// 会被报成「鉴权=true」——运维看到有凭据保护、便不再加网络层限制，而端点对任何可达者都是敞开的。
// 这类"日志说安全、实际不安全"的偏差比不打印更危险，故逐条锁死。
//
// 判别力：把实现改回 `token != ""` 本用例立即变红。
func TestMetricsAuthEnabledTrimsWhitespace(t *testing.T) {
	tests := []struct {
		token string
		want  bool
		why   string
	}{
		{"", false, "留空 = 不鉴权（默认形态，靠回环地址收敛）"},
		{" ", false, "纯空白：实现侧 TrimSpace 后为空 = 不鉴权，日志必须同口径"},
		{"\t\n ", false, "各类空白字符同上"},
		{"tok-123", true, "真实凭据"},
		{" tok-123 ", true, "带首尾空白的真实凭据：实现侧 TrimSpace 后非空 = 会鉴权"},
	}
	for _, tc := range tests {
		if got := metricsAuthEnabled(tc.token); got != tc.want {
			t.Fatalf("metricsAuthEnabled(%q) 应为 %v（%s），实际 %v", tc.token, tc.want, tc.why, got)
		}
	}
}

// TestMetricsAuthEnabledMatchesEndpointHandler 用真实实现交叉印证上面的判据，而非只比字面量。
//
// 上一条用例断言的是"我们希望的语义"，本用例则直接问实现本身：给它一个纯空白 token，
// 它到底放不放行匿名请求？两者一致才说明日志不会骗人。若将来 EndpointHandler 改成别的
// 归一化方式（比如只 TrimRight），这里的断言会先于生产暴露偏差。
func TestMetricsAuthEnabledMatchesEndpointHandler(t *testing.T) {
	for _, token := range []string{"", " ", "\t", "tok-123", " tok-123 "} {
		// 不带 Authorization 头的请求：实现若鉴权则 401，放行则 200。
		req, err := http.NewRequest(http.MethodGet, "/metrics", nil)
		if err != nil {
			t.Fatalf("构造请求失败: %v", err)
		}
		rec := &statusRecorder{header: http.Header{}}
		metrics.New(rt.NewRegistry()).EndpointHandler(token).ServeHTTP(rec, req)
		actuallyAuthenticated := rec.status == http.StatusUnauthorized

		if got := metricsAuthEnabled(token); got != actuallyAuthenticated {
			t.Fatalf("日志判据与实现不一致：token=%q 时 metricsAuthEnabled=%v，但实现的实际行为是「%s」——"+
				"「日志如实反映鉴权状态」这一前提被打破",
				token, got, map[bool]string{true: "拒绝匿名请求（确实在鉴权）", false: "放行匿名请求（未鉴权）"}[actuallyAuthenticated])
		}
	}
}

// statusRecorder 是只记状态码的最小 ResponseWriter（本包只做状态断言，不需要 httptest 的完整能力）。
type statusRecorder struct {
	header http.Header
	status int
}

func (r *statusRecorder) Header() http.Header         { return r.header }
func (r *statusRecorder) WriteHeader(code int)        { r.status = code }
func (r *statusRecorder) Write(p []byte) (int, error) { return len(p), nil }

// TestNewMetricsServerSetsWriteTimeout 锁定抓取端点必须设 WriteTimeout。
//
// 为什么这条不能靠"看起来合理"随手改掉：主服务刻意不设 WriteTimeout（SSE / 长轮询需要无限写时间，
// 见主服务构造处的注释），而抓取端点容易被类比成"也是 HTTP 服务"而跟着不设。但抓取端点没有长连接：
// 每次抓取返回一次文本快照即结束，不存在任何"合法地长时间不写完"的请求。不设的后果是客户端连上
// 却不读响应时，handler 的 Write 一直阻塞、goroutine 挂住（该端点也没有 httpx.StallWriter 兜底），
// 这些挂住的请求会吃掉 shutdownAll 共用的 35s 关停预算，使主服务的长轮询 drain 拿不到剩余预算。
func TestNewMetricsServerSetsWriteTimeout(t *testing.T) {
	srv := newMetricsServer(config.MetricsEndpointConfig{ListenAddr: "127.0.0.1:8849"}, metrics.New(rt.NewRegistry()))
	if srv == nil {
		t.Fatal("指定了 listen-addr 时应返回监听器")
	}
	if srv.WriteTimeout <= 0 {
		t.Fatalf("抓取端点必须设 WriteTimeout（无 SSE / 长轮询，设了不会误杀正常请求），实际 %v", srv.WriteTimeout)
	}
	// 与 ReadTimeout 同量级即可：抓取是纯短响应，30s 足够宽裕，不必更大。
	if srv.WriteTimeout > srv.ReadTimeout {
		t.Fatalf("抓取端点的 WriteTimeout(%v) 不应大于 ReadTimeout(%v)——写比读还宽松说明取值失去意义",
			srv.WriteTimeout, srv.ReadTimeout)
	}
}

// TestNewMetricsServerDisabledWhenAddrEmpty 置空即不启动端点（显式关闭语义，ADR-0090）。
func TestNewMetricsServerDisabledWhenAddrEmpty(t *testing.T) {
	for _, addr := range []string{"", " ", "\t"} {
		if srv := newMetricsServer(config.MetricsEndpointConfig{ListenAddr: addr}, metrics.New(rt.NewRegistry())); srv != nil {
			t.Fatalf("listen-addr=%q 应表示不启动抓取端点，实际返回了监听器", addr)
		}
	}
}

// TestNewMetricsServerTrimAddr 地址首尾空白被归一（否则 net.Listen 会把它当端口名去解析而失败）。
func TestNewMetricsServerTrimAddr(t *testing.T) {
	srv := newMetricsServer(config.MetricsEndpointConfig{ListenAddr: "  127.0.0.1:8849  "}, metrics.New(rt.NewRegistry()))
	if srv == nil {
		t.Fatal("应返回监听器")
	}
	if srv.Addr != strings.TrimSpace(srv.Addr) {
		t.Fatalf("监听地址应已 TrimSpace，实际 %q", srv.Addr)
	}
}

// TestPreferShutdownError 锁定「两者都失败时上报主服务的错误」这条反直觉的优先级。
//
// 为什么不按字面的"首个错误"：关停顺序是先 metricsSrv（它只有短请求、几乎瞬间完成，先关才能
// 把完整的剩余预算留给主服务的 long-poll drain）。若按顺序取首错，两者都失败时报出去的会是
// 抓取端点的失败——而主服务 drain 被截断意味着在途的长轮询与 SSE 被硬断，严重程度明显更高。
// 故取值与顺序解耦：主服务错误优先，仅当主服务成功时才回落到 metricsSrv 的错误。
//
// 判别力：把实现改回 `if mainErr != nil && metricsErr == nil` 的旧写法（等价于"先关的优先"），
// 第一条子用例立即变红；改成"永不回落到 metricsErr"则第二条变红。
func TestPreferShutdownError(t *testing.T) {
	mainErr := errors.New("主服务 drain 被截断")
	metricsErr := errors.New("抓取端点关停失败")

	t.Run("两者都失败时上报主服务错误", func(t *testing.T) {
		if got := preferShutdownError(mainErr, metricsErr); !errors.Is(got, mainErr) {
			t.Fatalf("两者都失败时应上报主服务的错误，实际 %v", got)
		}
	})
	t.Run("仅抓取端点失败时上报它", func(t *testing.T) {
		// 主服务成功、抓取端点失败：错误必须被上报，不能被静默吞掉。
		if got := preferShutdownError(nil, metricsErr); !errors.Is(got, metricsErr) {
			t.Fatalf("主服务成功时应回落到抓取端点的错误，实际 %v", got)
		}
	})
	t.Run("仅主服务失败时上报它", func(t *testing.T) {
		if got := preferShutdownError(mainErr, nil); !errors.Is(got, mainErr) {
			t.Fatalf("应上报主服务错误，实际 %v", got)
		}
	})
	t.Run("都成功时无错误", func(t *testing.T) {
		if got := preferShutdownError(nil, nil); got != nil {
			t.Fatalf("两者都成功时不应报错，实际 %v", got)
		}
	})
}

// TestShutdownAllShutsBothServers 验证两个监听器**都被关停**：优先级取值不得让任一方被跳过。
//
// 为什么单独测这个：上面那只测了错误怎么挑，测不到"两次 Shutdown 是否都真的执行了"——
// 若有人为了优先取主服务错误而提前 return，抓取端点就不再被关停，自更新分支下新进程会
// bind 不上抓取端口（正是 ADR-0090 要修的那个形态）。
//
// 判据用 RegisterOnShutdown 回调是否触发，而不是「端口能否被重新绑定」或「再 Serve 是否报
// ErrServerClosed」——后两者都不可靠：Shutdown 只关闭经 Serve 注册过的监听器，未注册时端口
// 仍被测试持有的 net.Listener 占着（实测重绑必然失败）；而"再 Serve"在 Server 未被关停时是
// 阻塞而非报错，回归时会让用例挂到超时才失败（实测 60s）。回调则直接反映"Shutdown 被调用过"
// 这一事实，不依赖时序，回归时立刻报红。
func TestShutdownAllShutsBothServers(t *testing.T) {
	// newLiveServer 起一个真实在跑的 server，并返回"Shutdown 被调用时关闭"的信号 channel。
	newLiveServer := func(t *testing.T) (*http.Server, <-chan struct{}) {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("监听失败: %v", err)
		}
		srv := &http.Server{Handler: http.NotFoundHandler()}
		fired := make(chan struct{})
		srv.RegisterOnShutdown(func() { close(fired) })
		go func() { _ = srv.Serve(ln) }()
		// 真打一次请求：确认 Serve 已把监听器注册进 Server（此后 Shutdown 才会去关它），
		// 顺带说明这两个 server 是"活着"的，不是空壳。
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + ln.Addr().String() + "/")
		if err != nil {
			t.Fatalf("预热请求失败: %v", err)
		}
		_ = resp.Body.Close()
		return srv, fired
	}

	mainSrv, mainFired := newLiveServer(t)
	metricsSrv, metricsFired := newLiveServer(t)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 两个都在途无请求，关停应无错误（顺带覆盖"都成功 → 返回 nil"）。
	if err := shutdownAll(shutdownCtx, mainSrv, metricsSrv); err != nil {
		t.Fatalf("无在途请求时关停应无错误，实际 %v", err)
	}

	for _, tc := range []struct {
		name  string
		fired <-chan struct{}
	}{{"主服务", mainFired}, {"抓取端点", metricsFired}} {
		select {
		case <-tc.fired:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s 的 Shutdown 从未被调用——shutdownAll 跳过了它，"+
				"自更新分支下新进程将 bind 不上抓取端口", tc.name)
		}
	}
}

// TestMetricsServerServesUnderWriteTimeout 用真实监听确认新的 WriteTimeout 不会误杀正常抓取。
//
// 为什么值得跑一次真实 HTTP：WriteTimeout 是 http.Server 字段，仅断言字段非零只能证明"赋值了"，
// 不能证明取值可用。这里起一个真实监听器跑一次真实抓取，确认 30s 的写上限对正常短响应无影响
// （即"抓取是短响应、设 WriteTimeout 不会误杀"这一前提的直接验证）。
func TestMetricsServerServesUnderWriteTimeout(t *testing.T) {
	srv := newMetricsServer(config.MetricsEndpointConfig{ListenAddr: "127.0.0.1:0"}, metrics.New(rt.NewRegistry()))
	if srv == nil {
		t.Fatal("应返回监听器")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + ln.Addr().String() + "/metrics")
	if err != nil {
		t.Fatalf("抓取失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("抓取应返回 200，实际 %d", resp.StatusCode)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("关停失败: %v", err)
	}
	<-done
}
