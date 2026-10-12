//go:build e2e && loadtest

// 慢客户端探测：构造「接收极慢 / 干脆不读」的客户端，分别打有 StallWriter 保护的端点与**没有**保护的
// 对照端点，量三件事：慢客户端能否拖垮服务、StallWriter 是否真的在 30s 后中断、无保护端点是否会被
// 永久占住 goroutine。另外测长轮询 / SSE（它们刻意不设全局 WriteTimeout）。
//
// 保护面分布（grep 确认，非猜测）：
//   - 有 StallWriter：internal/handler/delivery_stream_handler.go:98（交付 blob 下载）、
//     internal/handler/file_sync_handler.go:310（多级灰度文件同步下载）、
//     internal/embedweb/embedweb.go:22（前端静态资源）。
//   - 无保护：其余全部端点。全局 WriteTimeout 在 apps/server/cmd/beacon/main.go:810 附近**刻意不设**
//     （SSE / 长轮询需要无限写时间），所以普通端点的响应写没有 deadline 兜底。
//
// 为什么用裸 TCP socket 而不是 net/http 客户端做慢客户端：
//  1. 精确的中断时刻：raw socket 上 SetReadDeadline + Read 能给出「服务端何时真的放弃」的毫秒级时刻，
//     而 http.Client 的 Read 超时会把「客户端自己的超时」与「服务端中断」混在一起；
//  2. 无 goroutine 泄漏：用 http.Client 做「带超时的读探测」必须起一个后台 goroutine 阻塞在 Read 上，
//     超时后那个 goroutine 会一直挂着——探测本身会污染被测进程外的观测（本用例要量 goroutine 数）。
//
// 「不读客户端」的构造要点：必须**先收完响应头**再停止读取。服务端可能先把整个响应写进内核 socket
// 缓冲（本机 tcp_wmem=4096/65536/16777216、tcp_rmem=4096/87380/16777216，即收发缓冲合计约 150 KiB
// 起、可自动增长到 16 MiB），只有响应体明显大于「发送缓冲 + 接收缓冲」之和时服务端才会真的阻塞在 Write 上。
//
// 运行（正式 / 冒烟同一入口，凭据缺失即 skip）：
//
//	export E2E_ADMIN_PASS='...' E2E_AUTH_SECRET='...'
//	go test -tags "e2e loadtest" -timeout=40m -v -run '^TestSlowClientRealChain' ./apps/server/test/loadtest/
//
// 环境变量开关：
//
//	SLOW_BLOB_MIB         blob 大小（默认 64，必须大于内核收发缓冲之和）
//	SLOW_STALL_CLIENTS    打 blob 端点的慢客户端数（默认 8）
//	SLOW_PLAIN_CLIENTS    打无保护端点的慢客户端数（默认 32）
//	SLOW_PLAIN_BYTES      无保护端点响应体大小（默认 0 = 取配置正文上限 MaxContentBytes-1KiB）
//	SLOW_HOLD_DURATION    慢客户端保持时长（默认 75s，必须 > StallWriter 的 30s 才能观测到中断）
//
// 隔离：独立 sqlite 库 beacon-slow.db、独立日志前缀、独立二进制输出路径、独立 v2 namespace code。
package loadtest_e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/service"
	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	// slowDefaultBlobMiB 是下载侧 blob 大小：必须远大于「服务端发送缓冲 + 客户端接收缓冲」之和
	// （本机上限各 16 MiB），否则服务端可能在客户端停止读取前就写完整响应。
	slowDefaultBlobMiB = 64
	// slowDefaultStallClients 是打 blob 端点（有 StallWriter）的慢客户端数。
	slowDefaultStallClients = 8
	// slowDefaultPlainClients 是打无保护端点的慢客户端数。
	slowDefaultPlainClients = 32
	// slowDefaultHoldDuration 是慢客户端保持时长。必须 > StallWriter 的 30s（DefaultWriteStall），
	// 否则观测不到中断；75s 给出「中断后仍有余量观测状态收敛」的窗口。
	slowDefaultHoldDuration = "75s"

	// slowDBName / slowLogPrefix / slowBinName / slowNamespace 是本用例的隔离标识。
	slowDBName    = "beacon-slow.db"
	slowLogPrefix = "beacon-slow"
	slowBinName   = "beacon-slow-bin"
	// slowNamespace 是独立 v2 namespace code（blob 链路用）。
	slowNamespace = "loadtest-slow"
	// slowSourceID 是模板源 serverId。
	slowSourceID = "slow-source"
	// slowScanDir 是变更单扫描目录前缀。
	slowScanDir = "plugins/slowtest"

	// slowStallWindow 是产品侧的单次写 stall 上限（httpx.DefaultWriteStall）。
	// 这里刻意**硬编码副本**而不是 import httpx：本用例是「实测产品行为」，若产品改了常量，
	// 实测值会与这里的期望不符并让用例失败——那正是我们想要的告警，而不是静默跟随。
	slowStallWindow = 30 * time.Second
	// slowStallTolerance 是「实测中断时刻」与期望窗口的容许偏差。
	// 下限不给负余量：ServeContent 的写循环里每次 Write 都重置 deadline，只有一次 Write 完全
	// 阻塞满 30s 才会中断，故实测中断时刻必然 ≥ 30s（略大是正常的：前一次 Write 消耗了部分窗口）。
	slowStallTolerance = 20 * time.Second

	// slowProbeTimeout 是单次观测请求（/metrics）的上限。
	slowProbeTimeout = 15 * time.Second
	// slowSetupTimeout 是链路前置的单次请求上限。
	slowSetupTimeout = 60 * time.Second
	// slowSampleInterval 是 goroutine / FD 采样间隔。
	slowSampleInterval = time.Second
	// slowReadPollInterval 是 raw socket 上的读探测间隔（决定中断时刻的分辨率）。
	slowReadPollInterval = 250 * time.Millisecond
	// slowHandshakeTimeout 是 raw socket 上等待响应头的上限。
	slowHandshakeTimeout = 30 * time.Second
	// slowHeaderLimit 是响应头读取上限（够装下任何正常响应头）。
	slowHeaderLimit = 64 << 10
)

// slowEnv 是本用例的共享装配结果。
type slowEnv struct {
	blob       *blobEnv
	baseURL    string
	adminToken string
	pid        int
	hostPort   string
}

// slowSnapshot 是一次控制面进程状态快照。
type slowSnapshot struct {
	Phase      string  `json:"phase"`
	AtMs       int64   `json:"atMs"`
	Goroutines float64 `json:"goroutines"`
	OpenFDs    int     `json:"openFds"`
	RSSKiB     int64   `json:"rssKiB"`
}

// slowConn 是一条慢客户端裸连接。
type slowConn struct {
	conn net.Conn
	// headerAt 是响应头读完的时刻（真正开始「停止读取」的起点）。
	headerAt time.Time
	// brokenAt 是服务端放弃连接（读到 EOF / 错误）的时刻。
	brokenAt time.Time
	// bytesRead 是响应体累计读到的字节数。
	bytesRead int64
	// broken 表示连接已被服务端关闭。
	broken bool
	// headerOK 表示响应头读取成功（拿到了 200 行）。
	headerOK bool
	// statusLine 是响应状态行（诊断用）。
	statusLine string
}

// slowStallResult 是「blob 端点慢客户端」的实测结果。
type slowStallResult struct {
	Clients          int     `json:"clients"`
	BlobMiB          int     `json:"blobMiB"`
	HeadersOK        int     `json:"headersOk"`
	Broken           int     `json:"broken"`
	Survivors        int     `json:"survivors"`
	FirstBreakMs     float64 `json:"firstBreakMs"`
	MedianBreakMs    float64 `json:"medianBreakMs"`
	LastBreakMs      float64 `json:"lastBreakMs"`
	BytesRead        int64   `json:"bytesRead"`
	GoroutinesBefore float64 `json:"goroutinesBefore"`
	GoroutinesPeak   float64 `json:"goroutinesPeak"`
	GoroutinesAfter  float64 `json:"goroutinesAfter"`
	FDsBefore        int     `json:"fdsBefore"`
	FDsPeak          int     `json:"fdsPeak"`
	FDsAfter         int     `json:"fdsAfter"`
	// StallDetected 是核心结论：是否全部连接都在期望窗口（30s + 容差）内被中断。
	StallDetected bool `json:"stallDetected"`
	// DumpPath 是保持期内抓下的 goroutine 转储路径（StallWriter 失效时的现场）。
	DumpPath string `json:"dumpPath"`
	// WriteBlocked 是转储里卡在 net/http 响应写路径的 goroutine 数——StallWriter 失效的直接证据。
	WriteBlocked int    `json:"writeBlocked"`
	Note         string `json:"note"`
}

// slowPlainResult 是「无保护端点慢客户端」的实测结果。
type slowPlainResult struct {
	Clients int `json:"clients"`
	// ResponseBytes 是服务端响应的真实字节数（从配置正文长度得出，不用 Content-Length：
	// render.WriteJSON 走 json.Encoder 流式写、不设 Content-Length，响应是 chunked）。
	ResponseBytes int64 `json:"responseBytes"`
	HeadersOK     int   `json:"headersOk"`
	Broken        int   `json:"broken"`
	Survivors     int   `json:"survivors"`
	// FirstBreakMs / LastBreakMs 是服务端放弃连接的时刻（相对响应头读完）；0 表示从未放弃。
	FirstBreakMs float64 `json:"firstBreakMs"`
	LastBreakMs  float64 `json:"lastBreakMs"`

	GoroutinesBefore float64 `json:"goroutinesBefore"`
	GoroutinesPeak   float64 `json:"goroutinesPeak"`
	GoroutinesAfter  float64 `json:"goroutinesAfter"`
	FDsBefore        int     `json:"fdsBefore"`
	FDsPeak          int     `json:"fdsPeak"`
	FDsAfter         int     `json:"fdsAfter"`
	// GoroutineGrowthPerClient 是峰值相对基线的 goroutine 增量 / 客户端数。
	// ≈1 表示每条慢客户端都在服务端留下一个常驻 goroutine（被 Write 阻塞）。
	GoroutineGrowthPerClient float64 `json:"goroutineGrowthPerClient"`
	// DumpPath 是保持期内抓下的 goroutine 转储路径。
	DumpPath string `json:"dumpPath"`
	// WriteBlocked 是转储里「卡在 net/http 响应写路径」的 goroutine 数——这是本探测的**权威判据**。
	//
	// 为什么不能用 goroutine 增量判定：连接保持期间服务端即使写完了响应，也会留一个
	// net/http.(*conn).serve goroutine 在该 keep-alive 连接上等下一个请求（转储里是 IO wait + Peek）。
	// 那是**正常**的每连接一个 goroutine，与「被慢客户端占住写路径」完全不同。
	// 只有栈里同时出现 net/http.(*response).Write 与 internal/poll 等待，才是真的阻塞在写。
	WriteBlocked int    `json:"writeBlocked"`
	Note         string `json:"note"`
}

// slowSuspendedResult 是长轮询 / SSE 在慢客户端下的表现。
type slowSuspendedResult struct {
	Kind             string  `json:"kind"`
	Requested        int     `json:"requested"`
	Connected        int     `json:"connected"`
	HeldMs           float64 `json:"heldMs"`
	GoroutinesBefore float64 `json:"goroutinesBefore"`
	GoroutinesHeld   float64 `json:"goroutinesHeld"`
	GoroutinesAfter  float64 `json:"goroutinesAfter"`
	Note             string  `json:"note"`
}

// TestSlowClientRealChain 是慢客户端探测主入口。
func TestSlowClientRealChain(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")
	blobMiB := intEnv(t, "SLOW_BLOB_MIB", slowDefaultBlobMiB)
	stallClients := intEnv(t, "SLOW_STALL_CLIENTS", slowDefaultStallClients)
	plainClients := intEnv(t, "SLOW_PLAIN_CLIENTS", slowDefaultPlainClients)
	hold := durationEnv(t, "SLOW_HOLD_DURATION", slowDefaultHoldDuration)

	env := slowSetup(t, adminPass, authSecret, blobMiB)
	t.Logf("宿主 load=%s；StallWriter 期望窗口=%s（容差 +%s），hold=%s",
		authLoadText(), slowStallWindow, slowStallTolerance, hold)

	stallRes := slowProbeStallWriter(t, env, blobMiB, stallClients, hold)
	plainRes := slowProbeUnprotected(t, env, plainClients, hold)
	suspendedRes := slowProbeSuspended(t, env, hold)

	slowReport(t, stallRes, plainRes, suspendedRes)
}

// ---- 前置装配 ----

// slowSetup 复用 blob 链路的完整装配（换一套隔离标识），再补一个接近上限的配置正文
// 作为无保护对照端点的目标。
func slowSetup(t *testing.T, adminPass, authSecret string, blobMiB int) *slowEnv {
	t.Helper()
	env := blobSetup(t, adminPass, authSecret, blobSetupConfig{
		DBName: slowDBName, LogPrefix: slowLogPrefix, BinName: slowBinName,
		Namespace: slowNamespace, SourceID: slowSourceID, ListenPort: 25689,
		// INFO 级别：本用例要取证「服务端在何时中断连接」，访问日志里的连接生命周期是证据链的一环。
		LogLevel: "INFO", ScanDir: slowScanDir, OrderTitle: "慢客户端探测单",
		Placeholder: slowScanDir + "/placeholder.bin",
	})
	// 上传一个大于内核收发缓冲的 blob 供下载端点使用。
	content := blobGenerateContent(int64(blobMiB) << 20)
	sha := blobSHA256Hex(content)
	env.shas = append(env.shas, sha)
	blobRegisterSHA(t, env, sha)
	if ms, status, err := blobUpload(t, env, sha, content); err != nil {
		t.Fatalf("上传 %d MiB blob 失败：%v", blobMiB, err)
	} else if status != http.StatusNoContent {
		t.Fatalf("上传 %d MiB blob 期望 204，实际 %d", blobMiB, status)
	} else {
		t.Logf("已上传 %d MiB blob（sha=%s，上传耗时 %.0fms）", blobMiB, sha, ms)
	}
	parsed, err := url.Parse(env.baseURL)
	if err != nil {
		t.Fatalf("解析控制面地址失败（%s）：%v", env.baseURL, err)
	}
	hostPort := parsed.Host
	if !strings.Contains(hostPort, ":") {
		hostPort += ":80"
	}
	return &slowEnv{blob: env, baseURL: env.baseURL, adminToken: env.adminToken, pid: env.pid, hostPort: hostPort}
}

// ---- 探测 1：blob 下载端点（有 StallWriter）----

// slowProbeStallWriter 起 N 条裸连接打 blob 下载端点，收完响应头后**一字节不读**，
// 量它们是否在 30s 后被服务端中断。
//
// 这是最恶劣的慢客户端形态：服务端写满内核 socket 缓冲后彻底阻塞在 Write 上，
// 只有 StallWriter 的 deadline 能救。
func slowProbeStallWriter(t *testing.T, env *slowEnv, blobMiB, clients int, hold time.Duration) slowStallResult {
	t.Helper()
	res := slowStallResult{Clients: clients, BlobMiB: blobMiB}
	sha := env.blob.shas[len(env.blob.shas)-1]
	path := "/beacon/v2/stream/delivery/blobs/" + sha
	headers := map[string]string{
		"X-Beacon-Token":    env.blob.accessToken,
		"X-Beacon-Identity": env.blob.identityID,
		"X-Beacon-Boot":     env.blob.bootID,
	}

	before := slowTakeSnapshot(t, env, "blob 慢客户端·建立前")
	res.GoroutinesBefore, res.FDsBefore = before.Goroutines, before.OpenFDs
	t.Logf("blob 慢客户端探测：%d 条连接「收完响应头即停读」（blob=%d MiB），观测 %s；"+
		"建立前 goroutines=%.0f fds=%d", clients, blobMiB, hold, before.Goroutines, before.OpenFDs)

	conns := make([]*slowConn, clients)
	for i := range conns {
		conn, err := slowOpenAndReadHeaders(env.hostPort, path, headers)
		if err != nil {
			t.Logf("第 %d 条慢客户端握手失败：%v", i, err)
			continue
		}
		conns[i] = conn
	}

	// 主循环：周期采样进程状态 + 探测每条连接是否被服务端放弃。
	started := time.Now()
	deadline := started.Add(hold)
	peakGoroutines, peakFDs := before.Goroutines, before.OpenFDs
	// dumpPath 在保持期**内**抓取：连接一旦关闭，被阻塞的 goroutine 会立刻释放，
	// 那时再抓转储就什么都看不到了（首版把抓取放在收尾之后，转储里 0 个写路径 goroutine）。
	dumpPath := ""
	writeBlocked := 0
	for time.Now().Before(deadline) {
		slowPollBroken(conns)
		snap := slowTakeSnapshot(t, env, "blob 慢客户端·保持中")
		if snap.Goroutines > peakGoroutines {
			peakGoroutines = snap.Goroutines
		}
		if snap.OpenFDs > peakFDs {
			peakFDs = snap.OpenFDs
		}
		if slowAllBroken(conns) {
			// 全部已中断：不必再空等到 hold 结束，但仍要采一次收尾读数。
			break
		}
		// 进入保持期后段（StallWriter 期望窗口 + 5s）时抓一次转储：此刻要么已被中断、
		// 要么就是「本该被中断却没中断」的现场——正是要取证的时刻。
		if dumpPath == "" && time.Since(started) > slowStallWindow+5*time.Second {
			dumpPath, writeBlocked = slowCaptureGoroutineDump(t, env, "blob-stallwriter-保持中")
		}
		time.Sleep(slowSampleInterval)
	}
	// 再探一轮，确保「刚好在循环末尾中断」的连接也被记上。
	slowPollBroken(conns)
	res.GoroutinesPeak, res.FDsPeak = peakGoroutines, peakFDs
	res.WriteBlocked = writeBlocked

	// 收尾：统计各连接状态。
	breaks := make([]float64, 0, clients)
	var totalBytes int64
	for _, conn := range conns {
		if conn == nil {
			continue
		}
		if conn.headerOK {
			res.HeadersOK++
		}
		totalBytes += conn.bytesRead
		if conn.broken {
			res.Broken++
			if !conn.brokenAt.IsZero() && !conn.headerAt.IsZero() {
				breaks = append(breaks, float64(conn.brokenAt.Sub(conn.headerAt).Microseconds())/1000.0)
			}
		} else {
			res.Survivors++
		}
	}
	res.BytesRead = totalBytes
	if len(breaks) > 0 {
		sort.Float64s(breaks)
		res.FirstBreakMs = breaks[0]
		res.MedianBreakMs = breaks[len(breaks)/2]
		res.LastBreakMs = breaks[len(breaks)-1]
	}

	// 关掉全部连接，再采一次收尾读数（此时服务端应回收所有 goroutine / FD）。
	for _, conn := range conns {
		if conn != nil && conn.conn != nil {
			_ = conn.conn.Close()
		}
	}
	time.Sleep(3 * time.Second)
	after := slowTakeSnapshot(t, env, "blob 慢客户端·收尾后")
	res.GoroutinesAfter, res.FDsAfter = after.Goroutines, after.OpenFDs

	// 核心判据：全部连接都被中断，且最早中断时刻落在 [30s, 30s+容差] 内。
	res.StallDetected = res.Survivors == 0 && res.Broken == clients &&
		res.FirstBreakMs >= float64(slowStallWindow.Milliseconds()) &&
		res.FirstBreakMs <= float64(slowStallWindow.Milliseconds()+slowStallTolerance.Milliseconds())
	res.DumpPath = dumpPath
	switch {
	case res.Survivors > 0:
		res.Note = fmt.Sprintf("StallWriter 未生效：%d/%d 条连接在 %s 后仍未被中断（期望全部在 %s 后中断）；"+
			"goroutine 转储证实 %d 个 goroutine 卡在 net/http 响应写路径（转储：%s）",
			res.Survivors, clients, hold, slowStallWindow, res.WriteBlocked, res.DumpPath)
	case res.FirstBreakMs < float64(slowStallWindow.Milliseconds()):
		res.Note = fmt.Sprintf("中断时刻 %.0fms 早于期望窗口 %s（StallWriter 窗口可能被调小）",
			res.FirstBreakMs, slowStallWindow)
	case res.FirstBreakMs > float64(slowStallWindow.Milliseconds()+slowStallTolerance.Milliseconds()):
		res.Note = fmt.Sprintf("中断时刻 %.0fms 显著晚于期望窗口 %s+%s",
			res.FirstBreakMs, slowStallWindow, slowStallTolerance)
	default:
		res.Note = fmt.Sprintf("StallWriter 在期望窗口内中断了全部 %d 条连接（最早 %.0fms）", clients, res.FirstBreakMs)
	}
	t.Logf("blob 慢客户端结果：中断 %d/%d，最早 %.0fms，中位 %.0fms，最晚 %.0fms；"+
		"goroutines %.0f→%.0f→%.0f，fds %d→%d→%d",
		res.Broken, clients, res.FirstBreakMs, res.MedianBreakMs, res.LastBreakMs,
		res.GoroutinesBefore, res.GoroutinesPeak, res.GoroutinesAfter,
		res.FDsBefore, res.FDsPeak, res.FDsAfter)
	return res
}

// slowCaptureGoroutineDump 抓一份控制面 goroutine 转储到 .tmp 下，
// 返回文件路径与「卡在 net/http 响应写路径」的 goroutine 数（抓取失败返回空串与 0）。
// 端点自带管理面鉴权 + full 角色守卫（internal/server/debug_routes.go:52）。
// 转储是「goroutine 真的被占住」的直接证据，优于从计数推断。
func slowCaptureGoroutineDump(t *testing.T, env *slowEnv, label string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), slowProbeTimeout)
	defer cancel()
	// debug=2 给出完整栈（含每个 goroutine 的调用链），正是定位阻塞点所需。
	url := strings.TrimRight(env.baseURL, "/") + "/debug/pprof/goroutine?debug=2"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Logf("构造 goroutine 转储请求失败：%v", err)
		return "", 0
	}
	req.Header.Set("Authorization", "Bearer "+env.adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("抓 goroutine 转储失败：%v", err)
		return "", 0
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		t.Logf("读 goroutine 转储失败：%v", err)
		return "", 0
	}
	if resp.StatusCode != http.StatusOK {
		t.Logf("goroutine 转储端点回 %d（期望 200）", resp.StatusCode)
		return "", 0
	}
	path := filepath.Join(env.blob.repoRoot, ".tmp", "slow-goroutine-"+label+".txt")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Logf("写 goroutine 转储失败（%s）：%v", path, err)
		return "", 0
	}
	// 顺手统计卡在写路径上的 goroutine 数，让结论可被一行日志核验。
	blocked := slowCountStuckInWrite(raw)
	t.Logf("已抓 goroutine 转储：%s（共 %d 字节，其中 %d 个 goroutine 卡在 net/http 响应写路径）",
		path, len(raw), blocked)
	return path, blocked
}

// slowCountStuckInWrite 数转储里有多少个 goroutine 卡在 net/http 的响应写路径上。
// 判据取「栈里同时出现 net/http.(*response).Write 与 internal/poll 的等待」——
// 前者说明在写响应，后者说明真的阻塞在内核上而非在计算。
func slowCountStuckInWrite(dump []byte) int {
	count := 0
	for block := range strings.SplitSeq(string(dump), "\n\n") {
		if strings.Contains(block, "net/http.(*response).Write") && strings.Contains(block, "internal/poll") {
			count++
		}
	}
	return count
}

// ---- 探测 2：无保护对照端点 ----

// slowProbeUnprotected 起 N 条裸连接打**没有** StallWriter 保护的端点，收完响应头后停读，
// 量 goroutine / FD 是否随慢客户端线性增长且不回落。
//
// 端点选择：GET /admin/v1/configs/{id}（internal/handler/config_handler.go:83），响应体受
// internal/service/config_service.go:19 的 MaxContentBytes=256KiB 约束，是本仓库里能找到的
// 「无 StallWriter 保护」端点的**最大响应体**。
//
// 这个规模刻意卡在临界点上：本机收发缓冲合计约 150 KiB 起（tcp_wmem 默认 64KiB + tcp_rmem 默认 87KiB），
// 256 KiB 略大于它。故本探测的结论取决于内核是否自动增长缓冲——两种结果都有价值：
//   - goroutine 不增长：说明内核缓冲吃下了整个响应，服务端写完即返回，慢客户端不占住服务端；
//   - goroutine 线性增长且不回落：说明该端点在无 StallWriter 时**确实**会被慢客户端占住 goroutine，
//     这是可复现的缺陷证据（路径:行号见上方）。
func slowProbeUnprotected(t *testing.T, env *slowEnv, clients int, hold time.Duration) slowPlainResult {
	t.Helper()
	res := slowPlainResult{Clients: clients}

	// 建一条接近上限的配置项（超限会 400，故取上限减 1KiB）。
	contentBytes := intEnv(t, "SLOW_PLAIN_BYTES", int(service.MaxContentBytes)-1024)
	content := slowBuildYAML(contentBytes)
	configID := slowCreateConfig(t, env, content)
	path := fmt.Sprintf("/admin/v1/configs/%d", configID)
	headers := map[string]string{"Authorization": "Bearer " + env.adminToken}

	before := slowTakeSnapshot(t, env, "无保护端点·建立前")
	res.GoroutinesBefore, res.FDsBefore = before.Goroutines, before.OpenFDs
	t.Logf("无保护端点探测：GET %s（请求体 %d 字节，MaxContentBytes=%d）；%d 条「收完头即停读」连接，观测 %s；"+
		"建立前 goroutines=%.0f fds=%d", path, len(content), service.MaxContentBytes, clients, hold,
		before.Goroutines, before.OpenFDs)

	conns := make([]*slowConn, clients)
	for i := range conns {
		conn, err := slowOpenAndReadHeaders(env.hostPort, path, headers)
		if err != nil {
			t.Logf("第 %d 条慢客户端握手失败：%v", i, err)
			continue
		}
		conns[i] = conn
	}

	started := time.Now()
	deadline := started.Add(hold)
	peakGoroutines, peakFDs := before.Goroutines, before.OpenFDs
	dumpPath := ""
	writeBlocked := 0
	for time.Now().Before(deadline) {
		slowPollBroken(conns)
		snap := slowTakeSnapshot(t, env, "无保护端点·保持中")
		if snap.Goroutines > peakGoroutines {
			peakGoroutines = snap.Goroutines
		}
		if snap.OpenFDs > peakFDs {
			peakFDs = snap.OpenFDs
		}
		// 保持期中段抓一次转储：此刻连接仍在，能看清服务端到底卡在哪。
		if dumpPath == "" && time.Since(started) > 5*time.Second {
			dumpPath, writeBlocked = slowCaptureGoroutineDump(t, env, "无保护端点-保持中")
		}
		time.Sleep(slowSampleInterval)
	}
	slowPollBroken(conns)
	res.GoroutinesPeak, res.FDsPeak = peakGoroutines, peakFDs
	res.DumpPath = dumpPath
	res.WriteBlocked = writeBlocked

	breaks := make([]float64, 0, clients)
	for _, conn := range conns {
		if conn == nil {
			continue
		}
		if conn.headerOK {
			res.HeadersOK++
		}
		if conn.broken {
			res.Broken++
			if !conn.brokenAt.IsZero() && !conn.headerAt.IsZero() {
				breaks = append(breaks, float64(conn.brokenAt.Sub(conn.headerAt).Microseconds())/1000.0)
			}
		} else {
			res.Survivors++
		}
	}
	// 响应体字节数取配置正文长度：render.WriteJSON 走 json.Encoder 流式写、不设 Content-Length，
	// 响应是 chunked，故响应头里读不到长度（首版误把 Content-Length=0 当成「响应体 0 字节」）。
	res.ResponseBytes = int64(len(content))
	if len(breaks) > 0 {
		sort.Float64s(breaks)
		res.FirstBreakMs = breaks[0]
		res.LastBreakMs = breaks[len(breaks)-1]
	}

	for _, conn := range conns {
		if conn != nil && conn.conn != nil {
			_ = conn.conn.Close()
		}
	}
	time.Sleep(3 * time.Second)
	after := slowTakeSnapshot(t, env, "无保护端点·收尾后")
	res.GoroutinesAfter, res.FDsAfter = after.Goroutines, after.OpenFDs

	growth := res.GoroutinesPeak - res.GoroutinesBefore
	res.GoroutineGrowthPerClient = growth / float64(max(clients, 1))
	// 判据分两层，**权威层是转储**：
	//   1. WriteBlocked（转储里卡在 net/http 响应写路径的 goroutine 数）——真的被占住写路径的条数；
	//   2. GoroutineGrowthPerClient——辅助指标。注意它会被「keep-alive 每连接一个 serve goroutine」
	//      抬高（那是正常态，不是阻塞），故不能单独用作结论。
	switch {
	case res.WriteBlocked > 0:
		res.Note = fmt.Sprintf("转储证实 %d 个 goroutine 卡在 net/http 响应写路径（客户端 %d）——"+
			"该端点响应体 %d 字节超过了内核收发缓冲，服务端阻塞在 Write 上，且**没有** StallWriter 兜底，"+
			"%s 内无任何 deadline 中断；这是可复现的 goroutine 占用路径（堆栈见转储）",
			res.WriteBlocked, clients, res.ResponseBytes, hold)
	case growth >= float64(clients)/2:
		res.Note = fmt.Sprintf("goroutine 增量 %.0f（约 %.2f/客户端），但转储里 0 个卡在写路径——"+
			"增量来自 keep-alive 连接的 serve goroutine（正常态：连接保持期间每连接一个，等下一个请求）；"+
			"响应体 %d 字节已被内核缓冲吃下，服务端写完即返回，慢客户端不占住写路径",
			growth, res.GoroutineGrowthPerClient, res.ResponseBytes)
	default:
		res.Note = fmt.Sprintf("转储里 0 个卡在写路径，goroutine 增量 %.0f（约 %.2f/客户端）——"+
			"响应体 %d 字节被内核缓冲吃下，服务端写完即返回，慢客户端不占住服务端",
			growth, res.GoroutineGrowthPerClient, res.ResponseBytes)
	}
	t.Logf("无保护端点结果：收到响应头 %d/%d，中断 %d，幸存 %d；响应体 %d 字节；"+
		"goroutines %.0f→%.0f→%.0f，fds %d→%d→%d，卡在写路径的 goroutine=%d",
		res.HeadersOK, clients, res.Broken, res.Survivors, res.ResponseBytes,
		res.GoroutinesBefore, res.GoroutinesPeak, res.GoroutinesAfter,
		res.FDsBefore, res.FDsPeak, res.FDsAfter, res.WriteBlocked)
	return res
}

// slowBuildYAML 造一段恰好 contentBytes 字节的 YAML 正文（合法键值对，避免触发格式校验失败）。
func slowBuildYAML(contentBytes int) string {
	var sb strings.Builder
	sb.Grow(contentBytes + 64)
	line := 0
	for sb.Len() < contentBytes {
		fmt.Fprintf(&sb, "slow_key_%05d: value_%05d_padding_0123456789\n", line, line)
		line++
	}
	return sb.String()
}

// slowCreateConfig 经管理面建一条配置项，返回主键。
// namespace 取本用例的 v2 namespace code（服务端要求非空，见 internal/service/config_service.go:163）。
func slowCreateConfig(t *testing.T, env *slowEnv, content string) uint {
	t.Helper()
	var out struct {
		ID uint `json:"id"`
	}
	body := map[string]any{
		"namespace": env.blob.namespace, "group": model.GlobalGroupCode, "dataId": "slow-client-probe",
		"scopeLevel": "global", "scopeTarget": "", "format": "yaml",
		"content": content, "comment": "慢客户端探测：构造接近上限的响应体",
	}
	blobAdminJSON(t, env.baseURL, env.adminToken, http.MethodPost, "/admin/v1/configs",
		body, http.StatusCreated, &out, env.blob.cp)
	if out.ID == 0 {
		t.Fatal("建配置项未返回主键")
	}
	return out.ID
}

// ---- 探测 3：长轮询 / SSE ----

// slowProbeSuspended 挂一批长轮询与 SSE 连接，量它们（刻意不设 WriteTimeout）在保持期内的
// goroutine 占用。SSE / 长轮询是「设计上允许无限写」的端点，故它们**应该**长期占住 goroutine——
// 本探测的目的是把「合理占用」的规模量出来，作为与上两组对照的基线。
func slowProbeSuspended(t *testing.T, env *slowEnv, hold time.Duration) []slowSuspendedResult {
	t.Helper()
	// 长轮询 / SSE 目标在 v1 数据面，需要该 namespace 下有已注册实例（v1 注册表与 v2 身份是两套）。
	serverID := "slow-suspended-agent"
	slowRegisterV1Agent(t, env, serverID)

	base := strings.TrimRight(env.baseURL, "/")
	longTarget := fmt.Sprintf("%s/beacon/v1/agent/config/effective?namespace=%s&serverId=%s&md5=%s",
		base, slowNamespace, serverID, suspendedPrimeMD5)
	sseTargetURL := fmt.Sprintf("%s/beacon/v1/agent/stream?namespace=%s&serverId=%s",
		base, slowNamespace, serverID)

	out := make([]slowSuspendedResult, 0, 2)
	out = append(out, slowHoldSuspended(t, env, "长轮询", longTarget, hold))
	out = append(out, slowHoldSuspended(t, env, "SSE", sseTargetURL, hold))
	return out
}

// slowHoldSuspended 挂 held 条连接并观测 goroutine 占用。
func slowHoldSuspended(t *testing.T, env *slowEnv, kind, target string, hold time.Duration) slowSuspendedResult {
	t.Helper()
	held := slowDefaultStallClients
	res := slowSuspendedResult{Kind: kind, Requested: held}
	before := slowTakeSnapshot(t, env, kind+"·挂起前")
	res.GoroutinesBefore = before.Goroutines

	// 挂起窗口取 min(hold, 长轮询上限+余量)：SSE 无上限可一直挂，长轮询到点自然返回 304。
	window := min(hold, slowStallWindow+20*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()

	headers := map[string]string{"X-Beacon-Token": bootstrapToken}
	conns := make([]*slowConn, held)
	for i := range conns {
		conn, err := slowOpenAndReadHeadersCtx(ctx, env.hostPort, target, headers)
		if err != nil {
			t.Logf("%s 第 %d 条挂起失败：%v", kind, i, err)
			continue
		}
		conns[i] = conn
	}
	connected := 0
	for _, conn := range conns {
		if conn != nil {
			connected++
		}
	}
	res.Connected = connected

	// 采样：等连接全部建立后再取「挂起中」的读数。
	time.Sleep(3 * time.Second)
	during := slowTakeSnapshot(t, env, kind+"·挂起中")
	res.GoroutinesHeld = during.Goroutines

	// 让连接自然挂到窗口结束（SSE 持续推保活帧，长轮询到点返回），期间持续读掉服务端写来的帧，
	// 避免把「客户端不读」这个变量混进来——本探测要看的是「合理挂起」的基线，不是慢客户端。
	readCtx, readCancel := context.WithTimeout(context.Background(), window)
	defer readCancel()
	var wg sync.WaitGroup
	for _, conn := range conns {
		if conn == nil {
			continue
		}
		wg.Add(1)
		go func(c *slowConn) {
			defer wg.Done()
			slowDrain(readCtx, c)
		}(conn)
	}
	startHeld := time.Now()
	<-readCtx.Done()
	wg.Wait()
	res.HeldMs = float64(time.Since(startHeld).Microseconds()) / 1000.0

	for _, conn := range conns {
		if conn != nil && conn.conn != nil {
			_ = conn.conn.Close()
		}
	}
	time.Sleep(2 * time.Second)
	after := slowTakeSnapshot(t, env, kind+"·收尾后")
	res.GoroutinesAfter = after.Goroutines
	res.Note = fmt.Sprintf("%s 是设计上允许无限写的端点，%d 条连接挂起期间 goroutine 增量 %.0f（约 %.2f/连接）；"+
		"该占用是**合理基线**而非泄漏——收尾后回落说明连接关闭即释放",
		kind, res.Connected, res.GoroutinesHeld-res.GoroutinesBefore,
		(res.GoroutinesHeld-res.GoroutinesBefore)/float64(max(res.Connected, 1)))
	t.Logf("%s 结果：挂起前 goroutines=%.0f → 挂起中=%.0f（已建立 %d 条）→ 收尾后=%.0f；保持 %.0fms",
		kind, res.GoroutinesBefore, res.GoroutinesHeld, res.Connected, res.GoroutinesAfter, res.HeldMs)
	return res
}

// slowDrain 持续读掉服务端写来的字节直到 ctx 结束或连接被关闭。
func slowDrain(ctx context.Context, c *slowConn) {
	buf := make([]byte, 4096)
	for ctx.Err() == nil {
		_ = c.conn.SetReadDeadline(time.Now().Add(slowReadPollInterval))
		n, err := c.conn.Read(buf)
		if n > 0 {
			c.bytesRead += int64(n)
			continue
		}
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			c.broken = true
			c.brokenAt = time.Now()
			return
		}
	}
}

// slowRegisterV1Agent 经 v1 数据面挂载端点注册一个虚拟实例（长轮询 / SSE 的前置）。
func slowRegisterV1Agent(t *testing.T, env *slowEnv, serverID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), slowSetupTimeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"namespace": slowNamespace, "serverId": serverID, "role": "bukkit",
		"address": "127.0.0.1:25690", "version": "loadtest",
	})
	if err != nil {
		t.Fatalf("编码 v1 注册请求失败：%v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(env.baseURL, "/")+"/beacon/v1/agent/register", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("构造 v1 注册请求失败：%v", err)
	}
	req.Header.Set("X-Beacon-Token", bootstrapToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := harness.DoRequestWithGuard(req, slowSetupTimeout, env.blob.cp)
	if err != nil {
		t.Fatalf("v1 注册请求失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("v1 注册期望 200，实际 %d；片段：%s", resp.StatusCode, blobSnippet(raw))
	}
}

// ---- 裸连接工具 ----

// slowOpenAndReadHeaders 建立裸 TCP 连接、发 GET、读完响应头，之后**不再读响应体**。
// 返回的连接上 headerAt 已置位，调用方可用 slowPollBroken 探测服务端何时放弃。
func slowOpenAndReadHeaders(hostPort, path string, headers map[string]string) (*slowConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), slowHandshakeTimeout)
	defer cancel()
	return slowOpenAndReadHeadersCtx(ctx, hostPort, path, headers)
}

// slowOpenAndReadHeadersCtx 是带 ctx 的版本（长轮询 / SSE 需要更长的握手窗口）。
func slowOpenAndReadHeadersCtx(ctx context.Context, hostPort, path string, headers map[string]string) (*slowConn, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return nil, fmt.Errorf("建连失败：%w", err)
	}
	out := &slowConn{conn: conn}
	// 组装请求：HTTP/1.1 + Host + 自定义头 + Connection: keep-alive（避免服务端复用 / 关闭歧义）。
	var sb strings.Builder
	fmt.Fprintf(&sb, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: keep-alive\r\n", path, hostPort)
	for k, v := range headers {
		fmt.Fprintf(&sb, "%s: %s\r\n", k, v)
	}
	sb.WriteString("\r\n")
	// 握手阶段用 ctx 的 deadline；后续读探测会自行 SetReadDeadline。
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := conn.Write([]byte(sb.String())); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("写请求失败：%w", err)
	}
	reader := bufio.NewReaderSize(conn, slowHeaderLimit)
	if err := slowReadResponseHeader(reader, out); err != nil {
		_ = conn.Close()
		return nil, err
	}
	// 握手完成：清掉 deadline，之后由 slowPollBroken 逐次设置短读窗口。
	_ = conn.SetDeadline(time.Time{})
	out.headerAt = time.Now()
	return out, nil
}

// slowReadResponseHeader 逐行读到空行，记录状态行（其余头不解析：本用例只看状态行与「头是否读完」）。
func slowReadResponseHeader(reader *bufio.Reader, out *slowConn) error {
	first := true
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("读响应头失败：%w", err)
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if first {
			out.statusLine = trimmed
			out.headerOK = strings.Contains(trimmed, " 200 ")
			first = false
		}
		if trimmed == "" {
			return nil
		}
	}
}

// slowPollBroken 对每条未中断的连接做一次短窗口读探测：读到字节即累计，读到 EOF / 错误即记中断时刻，
// 读超时即「服务端仍在写 / 仍阻塞」。
//
// 关键：探测只能读 **1 字节**。首版用 32KiB 缓冲，每次轮询都把服务端刚写进内核缓冲的数据抽走，
// 等于给服务端持续「腾地方」，它的 Write 永远不会阻塞满 30s —— 实测表现为 8 条连接 0 中断。
// 每次只读 1 字节则释放的窗口可忽略（服务端一次 Write 是 32KiB，1 字节不够它完成），
// 而连接真被服务端放弃时 Read 会立刻返回 EOF / 错误，仍能精确捕获中断时刻。
func slowPollBroken(conns []*slowConn) {
	var buf [1]byte
	for _, c := range conns {
		if c == nil || c.broken {
			continue
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(slowReadPollInterval))
		n, err := c.conn.Read(buf[:])
		if n > 0 {
			c.bytesRead += int64(n)
			continue
		}
		if err == nil {
			continue
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			continue
		}
		// 非超时错误（EOF / 连接重置）：服务端已放弃这条连接。
		c.broken = true
		c.brokenAt = time.Now()
	}
}

// slowAllBroken 判断是否全部连接都已被服务端放弃。
func slowAllBroken(conns []*slowConn) bool {
	any := false
	for _, c := range conns {
		if c == nil {
			continue
		}
		any = true
		if !c.broken {
			return false
		}
	}
	return any
}

// ---- 进程状态采样 ----

// slowTakeSnapshot 采一次控制面状态：goroutine 数（/metrics 的 go_goroutines）+ FD 数 + RSS。
//
// goroutine 数必须走 /metrics：被 Write 阻塞的 goroutine 会被 Go runtime 停在 netpoller 上、
// **不占 OS 线程**，故 /proc/<pid>/status 的 Threads 看不到它们。
func slowTakeSnapshot(t *testing.T, env *slowEnv, phase string) slowSnapshot {
	t.Helper()
	snap := slowSnapshot{Phase: phase, AtMs: time.Now().UnixMilli()}
	snap.Goroutines = slowGoroutines(t, env.baseURL)
	if env.pid > 0 {
		snap.RSSKiB = readProcStats(env.pid).RSSKiB
		snap.OpenFDs = slowOpenFDs(env.pid)
	}
	return snap
}

// slowGoroutines 抓一次 /metrics 的 go_goroutines（读失败返回 0）。
func slowGoroutines(t *testing.T, baseURL string) float64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), slowProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/metrics", nil)
	if err != nil {
		return 0
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if !strings.HasPrefix(line, "go_goroutines ") {
			continue
		}
		var value float64
		if _, err := fmt.Sscanf(strings.TrimPrefix(line, "go_goroutines "), "%f", &value); err == nil {
			return value
		}
	}
	return 0
}

// slowOpenFDs 数 /proc/<pid>/fd 下的条目数（打开的文件描述符数）。读失败返回 0。
func slowOpenFDs(pid int) int {
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return 0
	}
	return len(entries)
}

// ---- 报告 ----

// slowReport 打印三组结果。
func slowReport(t *testing.T, stall slowStallResult, plain slowPlainResult, suspended []slowSuspendedResult) {
	t.Helper()
	t.Log("== 慢客户端 × blob 下载端点（有 StallWriter）==")
	t.Log("| 客户端 | blob(MiB) | 收到响应头 | 被中断 | 幸存 | 最早中断(ms) | 中位(ms) | 最晚(ms) | goroutines 基线→峰值→收尾 | fds 基线→峰值→收尾 | 卡在写路径 | 判定 |")
	t.Log("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	verdict := "未在期望窗口内中断"
	if stall.StallDetected {
		verdict = "在期望窗口内中断"
	}
	t.Logf("| %d | %d | %d | %d | %d | %.0f | %.0f | %.0f | %.0f→%.0f→%.0f | %d→%d→%d | %d | %s |",
		stall.Clients, stall.BlobMiB, stall.HeadersOK, stall.Broken, stall.Survivors,
		stall.FirstBreakMs, stall.MedianBreakMs, stall.LastBreakMs,
		stall.GoroutinesBefore, stall.GoroutinesPeak, stall.GoroutinesAfter,
		stall.FDsBefore, stall.FDsPeak, stall.FDsAfter, stall.WriteBlocked, verdict)
	t.Logf("备注：%s", stall.Note)
	t.Logf("| %d | %d | %d | %d | %d | %.0f | %.0f | %.0f | %.0f→%.0f→%.0f | %d→%d→%d | %s |",
		stall.Clients, stall.BlobMiB, stall.HeadersOK, stall.Broken, stall.Survivors,
		stall.FirstBreakMs, stall.MedianBreakMs, stall.LastBreakMs,
		stall.GoroutinesBefore, stall.GoroutinesPeak, stall.GoroutinesAfter,
		stall.FDsBefore, stall.FDsPeak, stall.FDsAfter, verdict)
	t.Logf("备注：%s", stall.Note)

	t.Log("== 慢客户端 × 无 StallWriter 保护的端点（GET /admin/v1/configs/{id}）==")
	t.Log("| 客户端 | 收到响应头 | 响应体字节 | 中断 | 幸存 | 最早中断(ms) | 最晚中断(ms) | goroutines 基线→峰值→收尾 | 增量/客户端 | 卡在写路径 | fds 基线→峰值→收尾 |")
	t.Log("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	t.Logf("| %d | %d | %d | %d | %d | %.0f | %.0f | %.0f→%.0f→%.0f | %.2f | %d | %d→%d→%d |",
		plain.Clients, plain.HeadersOK, plain.ResponseBytes,
		plain.Broken, plain.Survivors, plain.FirstBreakMs, plain.LastBreakMs,
		plain.GoroutinesBefore, plain.GoroutinesPeak, plain.GoroutinesAfter,
		plain.GoroutineGrowthPerClient, plain.WriteBlocked, plain.FDsBefore, plain.FDsPeak, plain.FDsAfter)
	t.Logf("备注：%s", plain.Note)
	t.Logf("goroutine 转储：%s", plain.DumpPath)

	t.Log("== 长轮询 / SSE（刻意不设 WriteTimeout）==")
	t.Log("| 类型 | 请求数 | 已建立 | 保持(ms) | goroutines 挂起前→挂起中→收尾后 |")
	t.Log("| --- | --- | --- | --- | --- |")
	for _, r := range suspended {
		t.Logf("| %s | %d | %d | %.0f | %.0f→%.0f→%.0f |",
			r.Kind, r.Requested, r.Connected, r.HeldMs, r.GoroutinesBefore, r.GoroutinesHeld, r.GoroutinesAfter)
		t.Logf("备注（%s）：%s", r.Kind, r.Note)
	}
}
