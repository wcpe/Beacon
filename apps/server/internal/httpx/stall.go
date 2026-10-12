// Package httpx 收口控制面 HTTP 相关公共工具。
package httpx

import (
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// DefaultWriteStall 是防慢客户端写阻塞的默认单次写窗口：每次 Write 最多阻塞此时长，
// 持续在写则不受限，仅当此时长内一次 Write 都无法完成才中断连接。
// 取 30s 平衡内网大文件下载与慢客户端兜底——远大于正常单次写耗时，正常传输不会误杀。
const DefaultWriteStall = 30 * time.Second

// stallWarnOnce 保证「底层不支持写截止时间」的 WARN 全进程只记一次。
//
// 必须是**包级**而非 StallWriter 的字段：StallWriter 由各 handler 按请求新建
// （delivery_stream / file_sync / embedweb 三处都是 `defer sw.Release()` 的每请求实例），
// 放字段里只能压掉同一请求内的重复写，跨请求仍会每请求一条——高 QPS 下会刷爆日志，
// 比原先的静默更糟。
var stallWarnOnce sync.Once

// StallWriter 包装 http.ResponseWriter，每次 Write 前重置写截止时间，
// 防止慢客户端长期占住写不释放 goroutine（FD 耗尽 / 服务器卡住）。
//
// 背景：main.go 故意不设全局 WriteTimeout（SSE 长连接与长轮询需要无限写时间，
// 全局 WriteTimeout 会误杀这些连接），导致流式下载端点对慢客户端无保护——
// 客户端不读时 io.Copy 的 Write 会阻塞在 TCP 缓冲区满，goroutine 永久挂起。
// 本包装器按「单次写 stall」模型兜底：每次 Write 前把 deadline 推到 now+stall，
// 只要持续在写就不超时（大文件下载不受影响），仅当 stall 时间内一次 Write 都
// 无法完成（客户端彻底卡住）才中断，释放 goroutine。
//
// 不实现 io.ReaderFrom：强制 io.Copy / ServeContent 走 Read+Write 循环，
// 以便每次 Write 都经过本包装器重置 deadline。
type StallWriter struct {
	http.ResponseWriter
	rc    *http.ResponseController
	stall time.Duration
}

// NewStallWriter 构造防慢写的 ResponseWriter 包装。stall<=0 时取默认值。
func NewStallWriter(w http.ResponseWriter, stall time.Duration) *StallWriter {
	if stall <= 0 {
		stall = DefaultWriteStall
	}
	return &StallWriter{
		ResponseWriter: w,
		rc:             http.NewResponseController(w),
		stall:          stall,
	}
}

// Write 在每次写前重置写截止时间，单次写超过 stall 即返回错误中断传输。
//
// 若底层 ResponseWriter 不支持设置写截止时间（返回 http.ErrNotSupported），
// 本包装器无法提供 stall 保护，会**全进程记一次 WARN**——这类失效必须可见：
// 此前这里是 `_ = ...` 静默丢弃，导致中间件链上的包装器漏实现 Unwrap() 时
// 防线整体失效而无人察觉（详见 server/statusWriter.Unwrap 的注释）。
func (s *StallWriter) Write(p []byte) (int, error) {
	if err := s.rc.SetWriteDeadline(time.Now().Add(s.stall)); err != nil {
		stallWarnOnce.Do(func() {
			slog.Warn("底层 ResponseWriter 不支持写截止时间，慢客户端保护未生效",
				"错误", err, "提示", "检查中间件链上的包装器是否实现了 Unwrap() http.ResponseWriter")
		})
	}
	return s.ResponseWriter.Write(p)
}

// Flush 透传底层 Flusher（ServeContent / SSE 等可能调用）。
func (s *StallWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Release 清除写截止时间，应在 handler 结束时调用（defer）。
func (s *StallWriter) Release() {
	_ = s.rc.SetWriteDeadline(time.Time{})
}
