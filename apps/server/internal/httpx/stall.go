// Package httpx 收口控制面 HTTP 相关公共工具。
package httpx

import (
	"net/http"
	"time"
)

// DefaultWriteStall 是防慢客户端写阻塞的默认单次写窗口：每次 Write 最多阻塞此时长，
// 持续在写则不受限，仅当此时长内一次 Write 都无法完成才中断连接。
// 取 30s 平衡内网大文件下载与慢客户端兜底——远大于正常单次写耗时，正常传输不会误杀。
const DefaultWriteStall = 30 * time.Second

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
// SetWriteDeadline 失败（底层 ResponseWriter 不支持）时忽略——退化为无 deadline，
// 不会比未包装时更差。
func (s *StallWriter) Write(p []byte) (int, error) {
	_ = s.rc.SetWriteDeadline(time.Now().Add(s.stall))
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
