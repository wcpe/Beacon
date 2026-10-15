package httpx

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// noUnwrapRW 是「不支持设置写截止时间」的 ResponseWriter（形态对齐中间件链上漏实现 Unwrap 的包装器）。
//
// 注意它**没有** SetWriteDeadline 方法，故 http.ResponseController 走到这里只能返回
// net/http 内部的 errNotSupported()——那是 `fmt.Errorf("%w", ErrNotSupported)` 包出来的错误，
// `err == http.ErrNotSupported` 恒为 false。本类型因此在两处守门：一是「不支持」的 WARN 去重，
// 二是**必须用 errors.Is 判定**（谁把判定改成直接比较，下面几条用例立刻变红）。
type noUnwrapRW struct {
	header http.Header
}

func (n *noUnwrapRW) Header() http.Header {
	if n.header == nil {
		n.header = http.Header{}
	}
	return n.header
}
func (n *noUnwrapRW) Write(p []byte) (int, error) { return len(p), nil }
func (n *noUnwrapRW) WriteHeader(int)             {}

// errClosedConn 复刻真实传输错误文案：SetWriteDeadline 是 net.Conn.SetWriteDeadline 的透传，
// 连接已关闭时返回的就是这类错误（非 ErrNotSupported）。
// 文案取自实测：对已关闭的真实 TCP 连接调 SetWriteDeadline 得到
// "set tcp 127.0.0.1:<port>: use of closed network connection"。
var errClosedConn = errors.New(
	"set tcp 127.0.0.1:36673: use of closed network connection")

// closedConnRW 模拟「连接已关闭后仍被调用」的 ResponseWriter：
// SetWriteDeadline 返回传输错误而非 ErrNotSupported，Write 也随之失败。
type closedConnRW struct {
	header http.Header
}

func (c *closedConnRW) Header() http.Header {
	if c.header == nil {
		c.header = http.Header{}
	}
	return c.header
}
func (c *closedConnRW) Write(p []byte) (int, error)      { return 0, errClosedConn }
func (c *closedConnRW) WriteHeader(int)                  {}
func (c *closedConnRW) SetWriteDeadline(time.Time) error { return errClosedConn }

// captureStallLogs 把默认 slog 输出收进内存缓冲，返回「取日志文本」的函数。
// 测试断言的是日志文案，故必须真的接管默认 logger 而不能只看返回值。
func captureStallLogs(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf.String
}

// TestStallWriterWarnsOncePerProcess 锁定「不支持写截止时间」的 WARN 是全进程一次，
// 而不是每请求一次。
//
// 为什么必须锁这条：StallWriter 由各 handler **按请求新建**（delivery_stream / file_sync /
// embedweb 三处都是 `defer sw.Release()` 的每请求实例），若把去重状态放在实例字段里，
// sync.Once 只能压掉同一请求内的重复写、跨请求仍每请求一条——高 QPS 下会刷爆日志
// （实测首版即如此：5 个请求产出 5 条 WARN），比修复前的静默更糟。
//
// 本用例注入**独立**的 sync.Once 而非直接用包级 stallWarnOnce：包级 Once 一旦触发
// 便无法复位，第二轮起 WARN 恒为 0 条，`-count=2` 会稳定假红（生产代码无需改，
// 是测试与全局状态耦合）。注入后每轮都是干净实例，断言仍是严格 `== 1`——
// 不能放宽成 `<= 1`，否则「去重状态退化成每请求一条」这个真问题就再也测不出来了。
func TestStallWriterWarnsOncePerProcess(t *testing.T) {
	out := captureStallLogs(t)

	// 每轮测试自带一份去重状态，语义等价于生产里的包级 Once。
	var warnOnce sync.Once

	// 模拟 5 个独立请求：每次请求各建一个 StallWriter（与生产三处调用点同形）。
	for range 5 {
		sw := newStallWriter(&noUnwrapRW{}, DefaultWriteStall, &warnOnce)
		_, _ = sw.Write([]byte("x"))
		_, _ = sw.Write([]byte("y")) // 同一请求内第二次写
	}

	got := strings.Count(out(), "慢客户端保护未生效")
	if got != 1 {
		t.Fatalf("「不支持写截止时间」的 WARN 应全进程只记一次，实际 %d 条——"+
			"去重状态若放在 StallWriter 实例字段里会退化成每请求一条，高 QPS 下刷爆日志", got)
	}
}

// TestStallWriterClosedConnDoesNotConsumeWarnOnce 锁定错误分类与「唯一一次告警机会」的归属。
//
// 为什么必须锁这条：SetWriteDeadline 的失败**不止 ErrNotSupported 一种**——它是
// net.Conn.SetWriteDeadline 的透传，连接已关闭时返回传输错误。若不分流，一句
// 「底层不支持 + 请检查 Unwrap」就把**成因完全错位**地报出去了；更糟的是它写在
// sync.Once 里，会**用掉唯一一次告警机会**，此后真正因「中间件链漏实现 Unwrap」
// 导致的防线失效**永远不再被报出**——本意在修「防线静默失效不可见」，
// 结果留了个反向静默口子，也违反项目规则「不隐藏真实原因」。
//
// 故本用例是**两段式**：先制造「连接已关闭」，再制造「不支持」，断言后者仍能报出来。
func TestStallWriterClosedConnDoesNotConsumeWarnOnce(t *testing.T) {
	out := captureStallLogs(t)

	var warnOnce sync.Once // 同上：注入干净实例，让用例可复跑

	// 第一段：连接已关闭（非「不支持」类）。写两次以锁定「该类不去重、每次记录」。
	for range 2 {
		sw := newStallWriter(&closedConnRW{}, DefaultWriteStall, &warnOnce)
		_, _ = sw.Write([]byte("x"))
	}

	// 断言 1：连接断开**不得**被报成「底线不支持」——成因错位正是本问题要修的。
	if got := strings.Count(out(), "慢客户端保护未生效"); got != 0 {
		t.Fatalf("连接已关闭时不应报「不支持写截止时间」，实际 %d 条——"+
			"成因错位会把排查方向彻底带偏（真因是客户端断开，不是漏实现 Unwrap）", got)
	}
	// 断言 2：确实记了「非不支持类」的 WARN，且**带上真实错误**（不隐藏真实原因）。
	if got := strings.Count(out(), "多为连接已关闭"); got != 2 {
		t.Fatalf("「非不支持类」的写截止时间失败应逐次记录（共 2 次写），实际 %d 条", got)
	}
	if !strings.Contains(out(), "use of closed network connection") {
		t.Fatalf("WARN 应带上底层真实错误，实际日志：\n%s", out())
	}

	// 第二段（关键）：真正的不支持仍必须能被报出——即上面那次失败**没有**占用 Once。
	sw := newStallWriter(&noUnwrapRW{}, DefaultWriteStall, &warnOnce)
	_, _ = sw.Write([]byte("x"))

	if got := strings.Count(out(), "慢客户端保护未生效"); got != 1 {
		t.Fatalf("连接断开后，真正的「不支持写截止时间」告警仍应报出 1 次，实际 %d 条——"+
			"说明非「不支持」类错误占用了唯一的告警机会，把防线失效重新变成静默", got)
	}
}

// TestStallWriterUnwrapsForResponseController 锁定 StallWriter 自身的 Unwrap()。
//
// 为什么值得守：本 PR 刚在 server/statusWriter 上补过同一个缺口，并立下「Unwrap 才是正解」
// 的规矩；StallWriter 内嵌的同样是 http.ResponseWriter **接口**，不实现 Unwrap 时
// 任何「handler 内对 sw 取 http.ResponseController」的写法都会在**这一层**断掉，
// 症状正是上面那种「防线静默失效」的难查形态。当前无调用方依赖，属预防性覆盖。
func TestStallWriterUnwrapsForResponseController(t *testing.T) {
	rec := &deadlineRW{}
	sw := NewStallWriter(rec, 30*time.Second)

	// 直接经 StallWriter 取 ResponseController：缺 Unwrap 会返回 feature not supported。
	if err := http.NewResponseController(sw).SetWriteDeadline(time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("经 StallWriter 设置写截止时间应成功（Unwrap 缺失会返回 feature not supported），实际：%v", err)
	}
	if len(rec.deadlines) != 1 {
		t.Fatalf("底层 SetWriteDeadline 应被调用 1 次，实际 %d 次——Unwrap 未把调用下钻到底层", len(rec.deadlines))
	}

	// 双层 StallWriter 串联：下钻要能穿过两层（包装链上常见的叠加形态）。
	// 若 Unwrap 误返回自身，这里会无限递归而非报错，用例直接挂死——顺带守住自递归。
	outer := NewStallWriter(sw, 30*time.Second)
	if err := http.NewResponseController(outer).SetWriteDeadline(time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("双层 StallWriter 下钻失败：%v", err)
	}
	if len(rec.deadlines) != 2 {
		t.Fatalf("双层包装下底层应共被调用 2 次，实际 %d 次", len(rec.deadlines))
	}

	// Unwrap 必须返回**被包装的底层对象**（与 server/statusWriter.Unwrap 语义一致）。
	if got := sw.Unwrap(); got != http.ResponseWriter(rec) {
		t.Fatalf("Unwrap 应返回被封装的底层 ResponseWriter，实际 %T", got)
	}
}

// TestStallWriterSetsDeadlineWhenSupported 验证底层支持时确实设置截止时间（正向路径）。
func TestStallWriterSetsDeadlineWhenSupported(t *testing.T) {
	rec := &deadlineRW{}
	sw := NewStallWriter(rec, 30*time.Second)
	if _, err := sw.Write([]byte("hello")); err != nil {
		t.Fatalf("Write 失败：%v", err)
	}
	sw.Release()

	if len(rec.deadlines) != 2 {
		t.Fatalf("应设置 2 次截止时间（写前设置 + Release 清除），实际 %d 次", len(rec.deadlines))
	}
	if rec.deadlines[0].IsZero() {
		t.Fatal("第一次应设置未来时刻，实际为零值")
	}
	if !rec.deadlines[1].IsZero() {
		t.Fatalf("Release 应清为零值，实际 %v", rec.deadlines[1])
	}
}

// deadlineRW 是支持 SetWriteDeadline 的 ResponseWriter（形态对齐真实 *http.response）。
type deadlineRW struct {
	header    http.Header
	deadlines []time.Time
}

func (d *deadlineRW) Header() http.Header {
	if d.header == nil {
		d.header = http.Header{}
	}
	return d.header
}
func (d *deadlineRW) Write(p []byte) (int, error) { return len(p), nil }
func (d *deadlineRW) WriteHeader(int)             {}
func (d *deadlineRW) SetWriteDeadline(t time.Time) error {
	d.deadlines = append(d.deadlines, t)
	return nil
}
