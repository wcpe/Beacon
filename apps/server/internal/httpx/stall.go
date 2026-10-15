// Package httpx 收口控制面 HTTP 相关公共工具。
package httpx

import (
	"errors"
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
//
// 生产构造（NewStallWriter）把**它的地址**交给每个实例共享，故「全进程一次」的语义不变；
// 同包测试可通过 newStallWriter 注入自己那份 sync.Once——包级 Once 在测试进程内不可重置，
// 直接拿它断言「只记一次」时第二轮起早已触发、WARN 恒为 0 条，`-count=2` 必然假红。
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

	// rc 由构造时的底层 w 建出（http.NewResponseController(w)），**不是**由 s 自建。
	//
	// 这点在补了下面的 Unwrap() 之后尤其要留意：ResponseController 是「持有起点、
	// 每层现查能力」的惰性下钻，不预先绑定任何连接。即便 s 自己实现了
	// Unwrap() http.ResponseWriter，rc 仍从 w 起步、跳过本层，语义与补 Unwrap 之前
	// 完全一致（本层本就没有 SetWriteDeadline 方法，下钻结果不受影响），
	// 故不存在「rc 指向 s 而自递归」的隐患。
	rc *http.ResponseController

	stall time.Duration

	// warnOnce 是「不支持写截止时间」这条 WARN 的去重状态，生产构造一律注入包级
	// stallWarnOnce 的地址（语义=全进程一次），测试可注入独立实例。
	//
	// 必须是*注入的指针*而非实例自带的 sync.Once：StallWriter 按请求新建，
	// 自带 Once 只能压掉同一请求内的重复写（见 stallWarnOnce 注释）。
	warnOnce *sync.Once
}

// NewStallWriter 构造防慢写的 ResponseWriter 包装。stall<=0 时取默认值。
func NewStallWriter(w http.ResponseWriter, stall time.Duration) *StallWriter {
	return newStallWriter(w, stall, &stallWarnOnce)
}

// newStallWriter 是带 warnOnce 注入的构造：生产由 NewStallWriter 传包级 stallWarnOnce，
// 测试传独立 Once——包级 Once 触发后无法复位，若测试直接用它，第二轮断言「只记一次」
// 时 WARN 恒为 0 条，复跑（`-count=2`）会稳定假红。
//
// 保持未导出：注入点只为测试服务，对外 API 仍只有 NewStallWriter。
func newStallWriter(w http.ResponseWriter, stall time.Duration, warnOnce *sync.Once) *StallWriter {
	if stall <= 0 {
		stall = DefaultWriteStall
	}
	return &StallWriter{
		ResponseWriter: w,
		rc:             http.NewResponseController(w),
		stall:          stall,
		warnOnce:       warnOnce,
	}
}

// Write 在每次写前重置写截止时间，单次写超过 stall 即返回错误中断传输。
//
// 失败按类型分流，**只有「底层不支持」才占用那唯一一次 WARN**：
//
//   - errors.Is(err, http.ErrNotSupported)：包装链上没人提供 SetWriteDeadline
//     （典型成因是中间件链上的包装器漏实现 Unwrap()）。本包装器无法提供 stall 保护，
//     这类失效**必须可见**——此前这里是 `_ = ...` 静默丢弃，导致防线整体失效而无人察觉
//     （详见 server/statusWriter.Unwrap 的注释）。去重后全进程记一次，避免高 QPS 刷爆日志。
//   - 其他错误：SetWriteDeadline 走的是 net.Conn.SetWriteDeadline 透传，连接已关闭时
//     会返回传输错误（形如 "set tcp 127.0.0.1:36673: use of closed network connection"）。
//     这是「客户端已断开」的正常终态、不是防线缺口，且**绝不能占用 warnOnce**：
//     它发生在客户端断开时，远比「漏实现 Unwrap」的装配缺陷更早出现，一旦占了那唯一一次
//     告警机会，真正的「不支持」告警此后**永远不再被报出**——等于用新的静默口子换旧的，
//     也违反 .claude/rules/error-surfacing.md「不隐藏真实原因」。
func (s *StallWriter) Write(p []byte) (int, error) {
	if err := s.rc.SetWriteDeadline(time.Now().Add(s.stall)); err != nil {
		// 注意必须用 errors.Is：net/http 的 errNotSupported 是 `fmt.Errorf("%w", ErrNotSupported)`
		// 包出来的，`err == http.ErrNotSupported` 恒为 false，直接比较会把装配缺陷误判成连接断开。
		if errors.Is(err, http.ErrNotSupported) {
			s.warnOnce.Do(func() {
				slog.Warn("底层 ResponseWriter 不支持写截止时间，慢客户端保护未生效",
					"错误", err, "提示", "检查中间件链上的包装器是否实现了 Unwrap() http.ResponseWriter")
			})
		} else {
			// 不占用 warnOnce（见函数注释）；每次出现都记，便于区分「客户端断开」与装配缺陷。
			slog.Warn("设置写截止时间失败（非「不支持」类，多为连接已关闭）", "错误", err)
		}
	}
	return s.ResponseWriter.Write(p)
}

// Unwrap 暴露被包装的 ResponseWriter，供 http.ResponseController 沿包装链逐层下钻。
//
// 与 server/statusWriter.Unwrap 同规矩、同一类坑：本包装器内嵌的同样是
// http.ResponseWriter **接口**，不实现 Unwrap 时 `http.NewResponseController(sw)`
// 会在**这一层**就断掉——SetWriteDeadline / SetReadDeadline / Hijack / Flush
// 一律返回 http.ErrNotSupported（错误文案 "feature not supported"）。
// 当前无调用方在 handler 内对 sw 取 ResponseController，故补之前并无实际影响；
// 但本 PR 刚在 statusWriter 上补过同一个缺口，此处不该再留一处**同类**断点：
// 将来任何「handler 里对 sw 取 ResponseController」的写法都会踩中它，
// 而症状恰是上面那种「防线静默失效」的难查形态。
//
// 与 Flush 的关系同上：Flush 是「显式透传某个具体能力」，Unwrap 是「让标准库自己
// 找到底层 writer」——两者不可互替，且能力集合随 Go 版本增长，逐个透传必然漏。
//
// 安全性：rc 由构造时的底层 w 建出（见字段注释），不经过本层，
// 故补了 Unwrap 也不会让下钻落回 s 自己、不存在自递归。
func (s *StallWriter) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
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
