package httpx

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

// noUnwrapRW 是「不支持设置写截止时间」的 ResponseWriter（形态对齐中间件链上漏实现 Unwrap 的包装器）。
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

// TestStallWriterWarnsOncePerProcess 锁定「不支持写截止时间」的 WARN 是全进程一次，
// 而不是每请求一次。
//
// 为什么必须锁这条：StallWriter 由各 handler **按请求新建**（delivery_stream / file_sync /
// embedweb 三处都是 `defer sw.Release()` 的每请求实例），若把去重状态放在实例字段里，
// sync.Once 只能压掉同一请求内的重复写、跨请求仍每请求一条——高 QPS 下会刷爆日志
// （实测首版即如此：5 个请求产出 5 条 WARN），比修复前的静默更糟。
func TestStallWriterWarnsOncePerProcess(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	// 模拟 5 个独立请求：每次请求各建一个 StallWriter（与生产三处调用点同形）。
	for range 5 {
		sw := NewStallWriter(&noUnwrapRW{}, DefaultWriteStall)
		_, _ = sw.Write([]byte("x"))
		_, _ = sw.Write([]byte("y")) // 同一请求内第二次写
	}

	got := strings.Count(buf.String(), "慢客户端保护未生效")
	if got != 1 {
		t.Fatalf("「不支持写截止时间」的 WARN 应全进程只记一次，实际 %d 条——"+
			"去重状态若放在 StallWriter 实例字段里会退化成每请求一条，高 QPS 下刷爆日志", got)
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
