package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/httpx"
)

// deadlineRecorder 是一个记录 SetWriteDeadline 调用的假 ResponseWriter，
// 形态对齐 net/http 传给 handler 的真实 *http.response（它实现了 SetWriteDeadline）。
type deadlineRecorder struct {
	header      http.Header
	deadlines   []time.Time
	writeStatus int
}

func (d *deadlineRecorder) Header() http.Header {
	if d.header == nil {
		d.header = http.Header{}
	}
	return d.header
}

func (d *deadlineRecorder) Write(p []byte) (int, error) {
	if d.writeStatus == 0 {
		d.writeStatus = http.StatusOK
	}
	return len(p), nil
}

func (d *deadlineRecorder) WriteHeader(status int) { d.writeStatus = status }

// SetWriteDeadline 让 http.ResponseController 能在本层成功设置写截止时间。
func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.deadlines = append(d.deadlines, t)
	return nil
}

// TestStatusWriterUnwrapsForResponseController 锁定 statusWriter 的 Unwrap()：
// 缺了它，http.ResponseController 在本层即断，SetWriteDeadline 返回 "feature not supported"。
//
// 为什么值得专门守：httpx.StallWriter 的慢客户端保护完全依赖 SetWriteDeadline，
// 而它挂在全局中间件链（httpMetricsMiddleware → recover → trace → accessLog）之后，
// 中间件一旦漏实现 Unwrap()，全站三处流式下载端点的 stall 防线会**静默**失效
// （实测慢客户端挂满 75s 无一被中断）。这条用例让该装配点无法再悄悄退化。
func TestStatusWriterUnwrapsForResponseController(t *testing.T) {
	rec := &deadlineRecorder{}
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}

	rc := http.NewResponseController(sw)
	if err := rc.SetWriteDeadline(time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("经 statusWriter 设置写截止时间应成功（Unwrap 缺失会返回 feature not supported），实际：%v", err)
	}
	if len(rec.deadlines) != 1 {
		t.Fatalf("底层 SetWriteDeadline 应被调用 1 次，实际 %d 次——Unwrap 未把调用下钻到底层", len(rec.deadlines))
	}
}

// TestStallWriterSetsDeadlineThroughMiddlewareChain 是上条的端到端形态：
// 用真实中间件链（accessLog + 指标中间件）包一层，再套 StallWriter，
// 断言写截止时间确实落到底层——即「生产链路上 stall 保护真的生效」。
func TestStallWriterSetsDeadlineThroughMiddlewareChain(t *testing.T) {
	rec := &deadlineRecorder{}
	// 复刻生产链：httpMetricsMiddleware(obs) → accessLog → handler。
	// 指标中间件在 obs 非 nil 时才包装 w，故此处注入一个记录型观察者。
	obs := &recordingMetricsObserver{}
	chain := httpMetricsMiddleware(obs)(accessLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sw := httpx.NewStallWriter(w, httpx.DefaultWriteStall)
		defer sw.Release()
		if _, err := sw.Write([]byte("hello")); err != nil {
			t.Errorf("StallWriter.Write 失败：%v", err)
		}
	})))

	chain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/v2/change-orders", nil))

	// 期望两次：Write 前设置 now+stall、Release 清除（time.Time{}）。
	if len(rec.deadlines) != 2 {
		t.Fatalf("底层 SetWriteDeadline 应被调用 2 次（写前设置 + Release 清除），实际 %d 次——"+
			"中间件链上存在未实现 Unwrap() 的包装器，stall 保护已失效", len(rec.deadlines))
	}
	if rec.deadlines[0].IsZero() {
		t.Fatal("第一次调用应设置一个未来的截止时间，实际为零值")
	}
	if !rec.deadlines[1].IsZero() {
		t.Fatalf("Release 应把截止时间清为零值，实际 %v", rec.deadlines[1])
	}
}

// recordingMetricsObserver 是记录型 HTTP 指标观察者（只为让中间件真的包装 w）。
type recordingMetricsObserver struct {
	calls int
}

func (o *recordingMetricsObserver) ObserveHTTP(_, _ string, _ int, _ float64) { o.calls++ }

// flushRecorder 记录 Flush 调用次数，并实现 http.Flusher（形态对齐真实 *http.response）。
type flushRecorder struct {
	header  http.Header
	flushes int
}

func (f *flushRecorder) Header() http.Header {
	if f.header == nil {
		f.header = http.Header{}
	}
	return f.header
}
func (f *flushRecorder) Write(p []byte) (int, error) { return len(p), nil }
func (f *flushRecorder) WriteHeader(int)             {}
func (f *flushRecorder) Flush()                      { f.flushes++ }

// TestFlushSurvivesDoubleStatusWriter 锁定 Flush 能穿透「指标中间件 + 访问日志」双层包装。
//
// 为什么值得专门守：statusWriter 内嵌的是 http.ResponseWriter **接口**，不含 Flush，
// 故它必须显式透传，否则 handler 的 `w.(http.Flusher)` 断言失败、SSE 端点直接 500
// ——这是历史上真实发生过的事故（见 middleware.go 的 Flush 注释）。而生产链上是**两层**
// statusWriter 串联（httpMetricsMiddleware 与 accessLog 各包一层），比单层更容易被改坏。
//
// 同时覆盖两条路径：handler 的直接类型断言（`w.(http.Flusher)`，三处 handler 在用），
// 与标准库的 http.ResponseController（依赖 Unwrap，见上一条用例）。
func TestFlushSurvivesDoubleStatusWriter(t *testing.T) {
	rec := &flushRecorder{}
	var assertedOK bool
	var ctrlErr error

	chain := httpMetricsMiddleware(&recordingMetricsObserver{})(accessLog(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			if f, ok := w.(http.Flusher); ok {
				assertedOK = true
				f.Flush()
			}
			ctrlErr = http.NewResponseController(w).Flush()
		})))

	chain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/beacon/v1/agent/stream", nil))

	if !assertedOK {
		t.Fatal("handler 的 w.(http.Flusher) 断言失败——SSE 端点在中间件链后会 500（历史事故）")
	}
	if ctrlErr != nil {
		t.Fatalf("ResponseController.Flush 应成功（依赖 Unwrap），实际：%v", ctrlErr)
	}
	if rec.flushes != 2 {
		t.Fatalf("底层 Flush 应被调用 2 次（直接断言 1 次 + ResponseController 1 次），实际 %d 次", rec.flushes)
	}
}
