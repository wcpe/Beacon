//go:build e2e && loadtest

// 429 背压边界 e2e：在**真实生产链路**上确定性触发交付数据面的两类限流 429 并验证恢复。
//
// 覆盖（两个用例共用同一套真实链路的装配，各自独立进程 / 独立库）：
//   - TestBackpressure429DeliveryUploadBusyRealChain：PUT 上传并发满 → delivery_upload_busy；
//   - TestBackpressure429DeliveryDownloadBusyRealChain：GET 下载并发满 → delivery_download_busy。
//
// 与单元级证据的分工（service 包 backpressure_429_test.go，两者不可互替）：
//   - 单元级证明「限流语义正确」：flowLimiter 满即拒、额度不泄漏、恢复后可用——直接构造服务与假依赖；
//   - 本文件证明「生产链路上真实触发过」：真实构建的控制面进程 + 真实 HTTP 请求 + 真实 agent 双 header
//     鉴权（AuthenticateAgentReport）+ 真实 blob 归属校验（ListOrdersReferencingSHA 反查变更单）+ 真实 sqlite，
//     响应体里带出业务码，服务端访问日志留下带 traceId 的 429 行，且 /metrics 的 RED 指标里留下 429 时序。
//
// 为什么是确定性的（不靠并发压测碰运气）：
//   - 两条限流都经**运维热改设置**把上限压到 1（上传默认 4 / 下载默认 64）。flowLimiter 满即拒、**不排队**，
//     故「第二个在途请求」必然 429；上限是进程内即时生效的，不需要重启。

// 上传侧触发手法：
//  1. 第一个 PUT 用阻塞式请求体（io.Pipe）占住唯一额度：标准库在发送请求体前会 flush 请求头
//     （net/http transfer.go：ContentLength!=0 且 body 非内存 reader 时置 FlushHeaders），服务端
//     解析完头即进入 handler；handler 先占额度、再落 uploading 占位行、最后才在 io.Copy 上等请求体。
//  2. 同步点不看时钟只看事实：轮询库中 delivery_blob 出现该 sha 的 uploading 占位行
//     —— 该行写在 tryAcquire 之后，行在即额度已被占住。
//  3. 此后第二个 PUT 必然 429；放行第一个（补完请求体）后额度归还，第三个 PUT 恢复 204。
//
// 下载侧触发手法：
//  1. 上传一个足够大的 blob（64 MiB，远大于「服务端发送缓冲 + 客户端接收缓冲」的和），
//     再用 Range 请求占住唯一额度：客户端收到响应头即证明服务端已进入 ServeContent（此时额度已被占），
//     随后**停止读取**响应体，服务端写满内核缓冲后阻塞在 Write 上，额度保持占用。
//  2. 第二个 GET 必然 429（同一个 sha、同一个身份）。
//  3. 关闭第一个响应体释放占用，再轮询到 200 即为恢复证据（同 sha + Range 只取前 1 KiB，代价可忽略）。
//
// 运行（正式 / 冒烟同一入口，凭据缺失即 skip）：
//
//	export E2E_ADMIN_PASS='...' E2E_AUTH_SECRET='...'
//	go test -tags "e2e loadtest" -timeout=15m -v -run '^TestBackpressure429Delivery' ./apps/server/test/loadtest/
//
// 隔离（与主压测 / 跨午夜用例并存不互扰）：各自独立 sqlite 库（beacon-bp429-up.db / beacon-bp429-down.db）、
// 独立日志前缀、独立二进制输出路径、独立 v2 namespace code，且清理遗留库时只删自己那两个文件
// （刻意不复用 removeStaleDB：它会清目录下全部 *.db*，会误删并行运行的主压测库）。
package loadtest_e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	// bp429UploadConcurrency / bp429DownloadConcurrency 把对应并发上限压到 1：
	// flowLimiter 不排队，第二个在途请求必然被拒（默认值分别是 4 / 64）。
	bp429UploadConcurrency   = 1
	bp429DownloadConcurrency = 1

	// bp429SlotTimeout 是「额度已被占住」的观测上限（判据是库中占位行 / 客户端已收到响应头，不看固定时钟）。
	bp429SlotTimeout = 20 * time.Second
	// bp429HoldTimeout 是占位请求的整体上限：服务端 ReadTimeout(30s) / 写 stall(30s) 是更早的硬边界。
	bp429HoldTimeout = 45 * time.Second
	// bp429RequestTimeout 是普通请求（设置写入 / 429 请求 / 恢复请求 / 指标抓取）的上限。
	bp429RequestTimeout = 30 * time.Second
	// bp429PollInterval 是轮询间隔（本地 sqlite 读 / 本地 HTTP，取小值让触发尽快发生）。
	bp429PollInterval = 50 * time.Millisecond

	// bp429DeliveryRoute 是交付流式数据面的 chi 路由模板（HTTP RED 指标的 route 标签取值）。
	bp429DeliveryRoute = "/beacon/v2/stream/delivery/blobs/{sha256}"

	// bp429BigContentBytes 是下载侧占位用的 blob 大小。必须远大于「服务端发送缓冲 + 客户端接收缓冲」
	// 之和（本机 tcp_wmem / tcp_rmem 上限各 16 MiB），否则服务端可能在客户端停止读取前就把整个响应
	// 写进内核缓冲并释放额度，让第二个 GET 拿到 200 而不是 429。
	bp429BigContentBytes = 64 << 20
)

// bp429EnvConfig 是一次 e2e 的隔离参数（两条用例必须取不同值，保证可并行运行互不覆盖）。
type bp429EnvConfig struct {
	DBName    string // sqlite 库文件名（.tmp/loadtest/ 下）
	LogPrefix string // 控制面日志前缀（.tmp/<prefix>.{out,err}.log）
	BinName   string // 控制面二进制文件名（.tmp/loadtest/ 下，避免与他人共用输出路径被覆盖）
	Namespace string // v2 namespace code
}

// bp429Identity 是一次真实 agent 身份的凭据（双 header 鉴权：token + identity，bootId 参与陈旧判定）。
type bp429Identity struct {
	AccessToken string
	IdentityID  string
	BootID      string
	ServerID    string
}

// bp429Env 是一次真实链路 e2e 的共享装配结果。
type bp429Env struct {
	cp         *harness.ControlPlane
	baseURL    string
	adminToken string
	stderrPath string
	db         *gorm.DB
	namespace  string
	orderID    uint
	ident      bp429Identity
	contents   [][]byte
	shas       []string
}

// bp429Outcome 是占位 / 后台请求 goroutine 的收尾结果（err 非空表示请求层失败）。
type bp429Outcome struct {
	status int
	body   string
	err    error
}

// TestBackpressure429DeliveryUploadBusyRealChain 真实链路确定性触发 delivery_upload_busy 429 并验证恢复。
func TestBackpressure429DeliveryUploadBusyRealChain(t *testing.T) {
	env := bp429Setup(t, bp429EnvConfig{
		DBName: "beacon-bp429-up.db", LogPrefix: "beacon-bp429-up",
		BinName: "beacon-bp429-up", Namespace: "loadtest-bp429-up",
	}, [][]byte{
		bytes.Repeat([]byte("A"), 4096),
		bytes.Repeat([]byte("B"), 2048),
		bytes.Repeat([]byte("C"), 1024),
	})
	contentA, contentB, contentC := env.contents[0], env.contents[1], env.contents[2]
	shaA, shaB, shaC := env.shas[0], env.shas[1], env.shas[2]

	// —— 前置：把上传并发压到 1（非高影响键，管理面直接写、进程内即时生效）——
	bp429SetIntSetting(t, env, "delivery.upload-concurrency", bp429UploadConcurrency)

	// —— 第 1 步：真实 PUT 占住唯一额度 ——
	releaseHolder, holderDone := bp429HoldUpload(t, env, shaA, contentA)

	bp429WaitUploadingRow(t, env.db, shaA, env.cp)
	t.Logf("占位就绪：库中已出现 sha256=%s 的 uploading 占位行 → 上传额度已被真实请求占住", shaA)

	// —— 第 2 步：第二个真实 PUT 必然 429（核心证据）——
	secondResp, secondRaw := bp429Do(t, env.cp, bp429NewPutRequest(t, env, shaB, bytes.NewReader(contentB), int64(len(contentB))))
	if secondResp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("并发满时第二个上传应回 HTTP 429，实际 %d；响应片段：%s", secondResp.StatusCode, bp429Snippet(secondRaw))
	}
	if code := bp429ErrorCode(t, secondRaw); code != "delivery_upload_busy" {
		t.Fatalf("429 响应业务码应为 delivery_upload_busy，实际 %q；响应片段：%s", code, bp429Snippet(secondRaw))
	}
	t.Logf("【真实触发证据·上传】PUT %s → HTTP 429 code=delivery_upload_busy（占位 sha=%s）", bp429DeliveryRoute, shaA)
	t.Logf("  429 响应体：%s", bp429Snippet(secondRaw))
	bp429LogServerAccess(t, env, http.MethodPut, shaB, http.StatusTooManyRequests)

	// 被拒请求不应留下任何痕迹（预检在占额之后、落盘之前）：该 sha 既无元数据行、也无内容可下载。
	if n := bp429BlobRowCount(t, env.db, shaB); n != 0 {
		t.Fatalf("被 429 拒绝的上传不应落任何 delivery_blob 元数据行（sha=%s，实际 %d 行）", shaB, n)
	}
	bp429AssertGETStatus(t, env, shaB, http.StatusNotFound, "blob_not_found")

	// —— 第 3 步：放行占位请求，额度归还 ——
	releaseHolder()
	bp429AwaitOutcome(t, holderDone, http.StatusNoContent, "占位上传应成功落盘（204）")
	t.Log("占位上传完成：HTTP 204（blob 已就绪落盘）")

	// —— 第 4 步：恢复证据 —— 额度归还后第三个真实 PUT 成功，且内容可真实读回 ——
	thirdResp, thirdRaw := bp429Do(t, env.cp, bp429NewPutRequest(t, env, shaC, bytes.NewReader(contentC), int64(len(contentC))))
	if thirdResp.StatusCode != http.StatusNoContent {
		t.Fatalf("额度归还后上传应恢复成功（204），实际 %d；响应片段：%s", thirdResp.StatusCode, bp429Snippet(thirdRaw))
	}
	got := bp429GETBody(t, env, shaC, 0, http.StatusOK)
	if !bytes.Equal(got, contentC) {
		t.Fatalf("恢复后下载内容与上传内容不一致：期望 %d 字节，实际 %d 字节", len(contentC), len(got))
	}
	t.Logf("【恢复证据·上传】额度归还后第三个 PUT 成功（HTTP 204），GET 回 200 且读回的 %d 字节与上传内容逐字节一致", len(got))

	// 就绪查询（HEAD）同步恢复：agent 侧断点 / 去重判断依赖它，故一并断言。
	headResp := bp429HeadBlob(t, env, shaC)
	if headResp.StatusCode != http.StatusOK {
		t.Fatalf("恢复后的 blob 应可 HEAD（200），实际 %d", headResp.StatusCode)
	}
	if cl := headResp.Header.Get("Content-Length"); cl != fmt.Sprint(len(contentC)) {
		t.Fatalf("HEAD 返回的 Content-Length 应为 %d，实际 %q", len(contentC), cl)
	}

	// —— 第 5 步：RED 指标里留下 429 时序（旁证：429 经真实 HTTP 栈返回，而非仅响应体文案）——
	bp429AssertREDSeries(t, bp429ScrapeMetrics(t, env.baseURL), http.MethodPut, http.StatusTooManyRequests)

	t.Log("== 429 背压边界 e2e（上传）结论 ==")
	t.Log("1) delivery_upload_busy 429 在真实生产链路（真实进程 / 真实鉴权 / 真实归属校验 / 真实限流）上确定性触发；")
	t.Log("2) 429 是瞬时背压而非永久损坏：额度归还后同一身份同一端点立即恢复 204，且被拒请求零残留；")
	t.Log("3) 429 同时存在于三处独立证据：响应体业务码、服务端访问日志（带 traceId）、/metrics 的 HTTP RED 指标。")
}

// TestBackpressure429DeliveryDownloadBusyRealChain 真实链路确定性触发 delivery_download_busy 429 并验证恢复。
func TestBackpressure429DeliveryDownloadBusyRealChain(t *testing.T) {
	env := bp429Setup(t, bp429EnvConfig{
		DBName: "beacon-bp429-down.db", LogPrefix: "beacon-bp429-down",
		BinName: "beacon-bp429-down", Namespace: "loadtest-bp429-down",
	}, [][]byte{
		bytes.Repeat([]byte{0x5a}, bp429BigContentBytes), // 占位用大 blob
		bytes.Repeat([]byte("R"), 1024),                  // 无关小 blob（证明恢复只针对限流、不是全局放行）
	})
	bigContent, smallContent := env.contents[0], env.contents[1]
	shaBig, shaSmall := env.shas[0], env.shas[1]

	// —— 前置 1：把下载并发压到 1 ——
	bp429SetIntSetting(t, env, "delivery.download-concurrency", bp429DownloadConcurrency)

	// —— 前置 2：把两个 blob 真实上传就绪（走真实 PUT；上传并发此时为默认 4，单请求不受限）——
	// 大 blob 用来占住下载额度；小 blob 用来在恢复后验证「内容仍可完整读回」而不必再传 64 MiB。
	bp429PutBlob(t, env, shaBig, bigContent)
	bp429PutBlob(t, env, shaSmall, smallContent)
	t.Logf("前置完成：已上传 %d 字节的大 blob（sha=%s）与 %d 字节的小 blob（sha=%s）",
		len(bigContent), shaBig, len(smallContent), shaSmall)

	// —— 第 1 步：真实 GET 占住唯一下载额度 ——
	// 占位客户端：读到响应头 + 首字节即证明服务端已进入 ServeContent（额度已被占），随后停读。
	holderBody := bp429HoldDownload(t, env, shaBig)
	t.Logf("占位就绪：客户端已收到 GET 响应头并读到首字节 → 服务端已进入 ServeContent 且未释放额度（还有 %d 字节未写出）", len(bigContent))

	// —— 第 2 步：第二个真实 GET 必然 429（核心证据）——
	secondResp, secondRaw := bp429Do(t, env.cp, bp429NewGETRequest(t, env, shaBig, 0))
	if secondResp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("下载并发满时第二个 GET 应回 HTTP 429，实际 %d；响应片段：%s", secondResp.StatusCode, bp429Snippet(secondRaw))
	}
	if code := bp429ErrorCode(t, secondRaw); code != "delivery_download_busy" {
		t.Fatalf("429 响应业务码应为 delivery_download_busy，实际 %q；响应片段：%s", code, bp429Snippet(secondRaw))
	}
	t.Logf("【真实触发证据·下载】GET %s → HTTP 429 code=delivery_download_busy（占位 sha=%s）", bp429DeliveryRoute, shaBig)
	t.Logf("  429 响应体：%s", bp429Snippet(secondRaw))
	bp429LogServerAccess(t, env, http.MethodGet, shaBig, http.StatusTooManyRequests)

	// —— 第 3 步：结束占位下载，额度归还 ——
	// 客户端提前关闭未读完的响应体即释放连接；服务端随写失败返回、defer 归还额度。
	if err := holderBody.Close(); err != nil {
		t.Fatalf("关闭占位下载响应体失败：%v", err)
	}

	// —— 第 4 步：恢复证据 —— 轮询到 GET 重新可用（同 sha，Range 只取前 1 KiB）——
	// 不用「关掉后立刻请求」：服务端写失败与 defer 归还额度之间有一个极短的窗口，
	// 固定 sleep 要么不够稳要么白等；轮询「取到 200 即恢复」既确定性又不引入假等待。
	recovered := bp429PollDownloadRecovered(t, env, shaBig)
	if !recovered {
		t.Fatal("额度归还后下载应恢复成功（同 sha 的 Range 请求应回 200/206），但轮询超时仍为 429")
	}
	small := bp429GETBody(t, env, shaSmall, 0, http.StatusOK)
	if !bytes.Equal(small, smallContent) {
		t.Fatalf("恢复后另一个 blob 的下载内容不一致：期望 %d 字节，实际 %d 字节", len(smallContent), len(small))
	}
	t.Logf("【恢复证据·下载】额度归还后同 sha 的 Range 请求立即恢复成功，且另一 blob 的 %d 字节可完整读回", len(small))

	// —— 第 5 步：RED 指标里留下 429 时序 ——
	bp429AssertREDSeries(t, bp429ScrapeMetrics(t, env.baseURL), http.MethodGet, http.StatusTooManyRequests)

	t.Log("== 429 背压边界 e2e（下载）结论 ==")
	t.Log("1) delivery_download_busy 429 在真实生产链路（真实进程 / 真实鉴权 / 真实归属校验 / 真实慢客户端）上确定性触发；")
	t.Log("2) 慢客户端占住下载额度时新下载被明确拒绝而不是排队挂起（与 flowLimiter「满即拒」口径一致）；")
	t.Log("3) 客户端断开后额度归还，同 sha 下载立即恢复——429 是瞬时背压，不是连接或文件层面的损坏。")
}

// bp429Setup 装配一次真实链路 e2e：清库 → 构建 → 起控制面 → 登录 → 建 namespace → 注册并激活身份 →
// 直连库播种 approved 变更单 + 文件项（sha 与 contents 一一对应）。
func bp429Setup(t *testing.T, cfg bp429EnvConfig, contents [][]byte) *bp429Env {
	t.Helper()
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")

	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	workDir := filepath.Join(repoRoot, ".tmp", "loadtest")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("创建压测工作目录失败：%v", err)
	}
	bp429RemoveStaleDB(t, workDir, cfg.DBName)
	// 收尾：删掉本用例独占的库文件（含 WAL / SHM；队列实测会写到近 800 MB）。
	// 在此处（早于起控制面与开探针连接）注册，利用 t.Cleanup 的 LIFO 顺序让它最后执行：
	// 先停控制面、再关探针连接，最后删库——避免把文件从仍持有它的进程脚下抽走。
	// 也刻意不复用 removeStaleDB：它会清目录下全部 *.db*，会误删并行运行的主压测库。
	t.Cleanup(func() { bp429CleanupDBFiles(t, workDir, cfg.DBName) })

	t.Log("== 构建控制面二进制（独立输出路径，不与他人共用）==")
	binPath := filepath.Join(workDir, cfg.BinName)
	if err := buildBeaconTo(repoRoot, binPath); err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}
	// 收尾：删掉本用例独占的控制面二进制（体积大；不与他人共用输出路径，故可安全删）。
	t.Cleanup(func() { bp429RemoveFile(binPath) })

	t.Log("== 起控制面（独立 sqlite + 动态端口）==")
	dbPath := filepath.Join(workDir, cfg.DBName)
	cp, baseURL := startControlPlane(t, harness.ControlPlaneConfig{
		BinPath: binPath, RepoRoot: repoRoot,
		DBDriver: "sqlite", DBDSN: dbPath,
		AdminPassword: adminPass, AuthSecret: authSecret, BootstrapToken: bootstrapToken,
		LogPrefix: cfg.LogPrefix,
		// 保留访问日志（默认 WARN 会压掉）：429 在控制面日志里留痕是证据链的一环。
		ExtraEnv: map[string]string{"BEACON_LOG_LEVEL": "INFO"},
	}, controlPlaneStartAttempts)
	harness.CleanupControlPlane(t, cp)
	stdoutPath, stderrPath := cp.LogPaths()
	t.Logf("控制面已就绪：%s（日志：%s / %s）", baseURL, stdoutPath, stderrPath)

	adminToken, err := harness.Login(baseURL, adminUser, adminPass, cp)
	if err != nil {
		t.Fatalf("管理员登录失败：%v", err)
	}

	// 独立 v2 namespace（复用既有引导 helper：建空间 → 注册身份 → 审批成 active）。
	accessToken, nsCode := createV2Namespace(t, baseURL, adminToken, cfg.Namespace, cp)

	// 直连同一个 sqlite 文件播种变更单 / 文件项并轮询占位行。
	// 与控制面并发读写靠 DSN 的 busy_timeout(5000) 兜底（与 hotreload e2e 同一范式：隔离库 + 直连播种）。
	db := bp429OpenProbeDB(t, dbPath)
	nsID := bp429NamespaceID(t, db, nsCode)
	ident := bp429RegisterIdentity(t, cp, baseURL, adminToken, accessToken)

	// 播种一张 approved 变更单：文件项指向本次要传的 sha。
	// 说明：文件项只能由真实模板源的 diff-scan 生成（需要有活着的 agent 上报文件资产），
	// 本用例焦点是**数据面并发限流**而非组单链路，故沿用 hotreload e2e 的「直连库播种既有事实」范式：
	// 单子与文件项是真实表结构的真实行，blob 归属校验 ListOrdersReferencingSHA 走的是真实查询。
	env := &bp429Env{
		cp: cp, baseURL: baseURL, adminToken: adminToken, stderrPath: stderrPath,
		db: db, namespace: nsCode, ident: ident, contents: contents,
	}
	files := make([]bp429SeedFile, 0, len(contents))
	for i := range contents {
		sha := bp429SHA256Hex(contents[i])
		env.shas = append(env.shas, sha)
		files = append(files, bp429SeedFile{
			Path: fmt.Sprintf("plugins/bp429/%02d.bin", i), SHA: sha, Size: int64(len(contents[i])),
		})
	}
	env.orderID = bp429SeedApprovedOrder(t, db, nsID, ident.ServerID, files)
	t.Logf("前置完成：namespace=%s(id=%d) order=%d source=%s identity=%s",
		nsCode, nsID, env.orderID, ident.ServerID, ident.IdentityID)

	// 收尾：清掉本用例落盘的 blob（下载用例会落 64 MiB）。
	// blob 存储根是产品硬编码的 .beacon/delivery（不可配置），故只能按**本用例的 sha** 精确删除；
	// 绝不整目录清理——同一 worktree 上可能还有别的 e2e 正在跑，扫全目录会误删它们的文件。
	// 库文件与二进制在 bp429Setup 里各自登记了清理（见该函数开头）。
	t.Cleanup(func() { bp429RemoveBlobFiles(t, repoRoot, env.shas) })
	return env
}

// bp429RemoveBlobFiles 按 sha 精确删除 blob 落盘文件（最终路径 + 可能残留的上传临时文件）。
// 删不掉只记日志：留下的是可被清理器回收的孤儿文件，不该让用例结果因此变红。
func bp429RemoveBlobFiles(t *testing.T, repoRoot string, shas []string) {
	t.Helper()
	root := filepath.Join(repoRoot, ".beacon", "delivery")
	for _, sha := range shas {
		if len(sha) < 2 {
			continue
		}
		final := filepath.Join(root, "blobs", sha[:2], sha)
		if err := os.Remove(final); err != nil && !os.IsNotExist(err) {
			t.Logf("收尾：删除 blob 文件 %s 失败（可由清理器回收）：%v", final, err)
		}
		// 上传中断会在 <root>/tmp 留下 <sha>.tmp-* 临时文件，一并按前缀清理。
		matches, _ := filepath.Glob(filepath.Join(root, "tmp", sha+".tmp-*"))
		for _, path := range matches {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Logf("收尾：删除上传临时文件 %s 失败：%v", path, err)
			}
		}
	}
}

// bp429RemoveStaleDB 只清理指定用例自己的库文件（含 WAL / SHM）。
// 刻意不复用 removeStaleDB：那个 helper 会清目录下全部 *.db*，会误删并行运行的主压测库。
func bp429RemoveStaleDB(t *testing.T, dir, dbName string) {
	t.Helper()
	if err := bp429RemoveDBFiles(dir, dbName); err != nil {
		t.Fatalf("清理库文件失败：%v", err)
	}
}

// bp429CleanupDBFiles 是收尾用的非致命版本：删不掉只记日志（收尾失败不该让用例结果变红）。
func bp429CleanupDBFiles(t *testing.T, dir, dbName string) {
	t.Helper()
	if err := bp429RemoveDBFiles(dir, dbName); err != nil {
		t.Logf("收尾：清理库文件失败（可手工删除）：%v", err)
	}
}

// bp429RemoveDBFiles 删除某用例的库文件三件套（主库 / WAL / SHM），已不存在视为成功。
func bp429RemoveDBFiles(dir, dbName string) error {
	base := filepath.Join(dir, dbName)
	for _, path := range []string{base, base + "-wal", base + "-shm"} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除 %s：%w", path, err)
		}
	}
	return nil
}

// bp429RemoveFile 删除单个文件（收尾用）；不存在或删不掉都不影响用例结果。
func bp429RemoveFile(path string) {
	_ = os.Remove(path)
}

// bp429OpenProbeDB 打开一个直连控制面同一 sqlite 文件的连接（库为 WAL，多读者不阻塞写者）。
// 与控制面并发读写靠 DSN 的 busy_timeout(5000) 兜底——只用单条 SELECT / INSERT，不起事务。
func bp429OpenProbeDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path+"?_busy_timeout=5000"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开核对用 sqlite 连接失败（%s）：%v", path, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取底层连接池失败：%v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// bp429NamespaceID 按 code 读回 namespace 主键（播种变更单需要）。
func bp429NamespaceID(t *testing.T, db *gorm.DB, code string) uint {
	t.Helper()
	var ns model.Namespace
	if err := db.Where("code = ?", code).First(&ns).Error; err != nil {
		t.Fatalf("按 code=%s 读 namespace 失败：%v", code, err)
	}
	if ns.ID == 0 {
		t.Fatalf("namespace %s 主键为空", code)
	}
	return ns.ID
}

// bp429RegisterIdentity 注册一个 v2 身份并审批成 active（复用既有引导 helper，走真实审批链路）。
func bp429RegisterIdentity(t *testing.T, cp *harness.ControlPlane, baseURL, adminToken, accessToken string) bp429Identity {
	t.Helper()
	serverID := "bp429-source"
	identityID := uuid.NewString() // v4，满足服务端 validUUID（UUIDv4 正则）
	bootID := uuid.NewString()
	registerV2Proxy(t, baseURL, accessToken, identityID, serverID, bootID, 25677, cp)
	activateV2Identity(t, baseURL, adminToken, identityID, serverID, cp)
	return bp429Identity{AccessToken: accessToken, IdentityID: identityID, BootID: bootID, ServerID: serverID}
}

// bp429SeedFile 是播种进变更单的一条文件项（sha 指向本用例要传的内容）。
type bp429SeedFile struct {
	Path string
	SHA  string
	Size int64
}

// bp429SeedApprovedOrder 在隔离库中播种一张 approved 变更单 + 若干 file_diff 文件项。
// 只填 NOT NULL 必需列：本用例只关心数据面 blob 归属校验，不驱动编排状态机（approved 单不进推进器活动集）。
func bp429SeedApprovedOrder(t *testing.T, db *gorm.DB, namespaceID uint, sourceServerID string, files []bp429SeedFile) uint {
	t.Helper()
	order := model.ChangeOrder{
		NamespaceID: namespaceID, Title: "BP429 交付并发限流验证单",
		SourceServerID: sourceServerID, Status: model.ChangeOrderStatusApproved,
		BatchMode: "count", BatchSizes: "[1]", ActivationMethod: model.ActivationMethodHotReload,
		ObserveWindowSec: 0, ActivateTimeoutSec: 60,
		PayloadState: model.PayloadStateReady, CreatedBy: adminUser,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("播种变更单失败：%v", err)
	}
	action := model.ChangeItemActionUpdate
	for i := range files {
		path := files[i].Path
		sha := files[i].SHA
		size := files[i].Size
		item := model.ChangeOrderItem{
			OrderID: order.ID, Kind: model.ChangeItemKindFileDiff,
			Path: &path, Action: &action, SHA256: &sha, SizeBytes: &size,
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("播种文件项 %s 失败：%v", files[i].Path, err)
		}
	}
	return order.ID
}

// bp429SetIntSetting 经管理面写一个 int 型热改设置（非高影响键，200 即时生效；高影响键会回 202 工单，故断言 200）。
func bp429SetIntSetting(t *testing.T, env *bp429Env, key string, value int) {
	t.Helper()
	body, err := json.Marshal(map[string]string{
		"value":  fmt.Sprint(value),
		"reason": "429 背压边界 e2e：把并发上限压到 1 以确定性触发限流",
	})
	if err != nil {
		t.Fatalf("编码设置请求失败：%v", err)
	}
	req, err := http.NewRequest(http.MethodPut, env.baseURL+"/admin/v1/settings/"+key, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("构建设置请求失败：%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+env.adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, raw := bp429Do(t, env.cp, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("写设置 %s=%d 应回 200（非高影响键直写生效），实际 %d；响应片段：%s",
			key, value, resp.StatusCode, bp429Snippet(raw))
	}
	t.Logf("已热改设置 %s=%d（进程内即时生效，无需重启）", key, value)
}

// bp429PutBlob 走真实 PUT 把一个 blob 传到就绪（204）；失败即 Fatal。
func bp429PutBlob(t *testing.T, env *bp429Env, sha string, content []byte) {
	t.Helper()
	resp, raw := bp429Do(t, env.cp, bp429NewPutRequest(t, env, sha, bytes.NewReader(content), int64(len(content))))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("前置上传 blob（sha=%s，%d 字节）应回 204，实际 %d；响应片段：%s",
			sha, len(content), resp.StatusCode, bp429Snippet(raw))
	}
}

// bp429HoldUpload 起一个真实 PUT 占住唯一上传额度，返回放行函数与收尾通道。
// 占位原理：请求体用 io.Pipe 挂在发送前段——请求头先到服务端，handler 先占额度再等请求体。
func bp429HoldUpload(t *testing.T, env *bp429Env, sha string, content []byte) (func(), <-chan bp429Outcome) {
	t.Helper()
	pr, pw := io.Pipe()
	releaseCh := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	// 失败路径也要放行，避免 goroutine 泄漏把测试进程拖住。
	t.Cleanup(release)

	go func() {
		// 先送 1 字节（不依赖它到达服务端）：真正让服务端进入 handler 的是「请求头在体之前被 flush」。
		_, _ = pw.Write(content[:1])
		<-releaseCh
		_, _ = pw.Write(content[1:])
		_ = pw.Close()
	}()

	req := bp429NewPutRequest(t, env, sha, pr, int64(len(content)))
	done := make(chan bp429Outcome, 1)
	go func() {
		resp, err := harness.DoRequestWithGuard(req, bp429HoldTimeout, env.cp)
		if err != nil {
			done <- bp429Outcome{err: err}
			return
		}
		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		done <- bp429Outcome{status: resp.StatusCode, body: string(raw), err: readErr}
	}()
	return release, done
}

// bp429HoldDownload 起一个真实 GET 占住唯一下载额度，返回占位响应体（调用方 Close 即释放额度）。
func bp429HoldDownload(t *testing.T, env *bp429Env, sha string) io.ReadCloser {
	t.Helper()
	// 独立客户端：占位连接必须一直挂着（不进连接池、不设超时），与其它断言用的请求互不干扰。
	holderClient := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	t.Cleanup(holderClient.CloseIdleConnections)

	req := bp429NewGETRequest(t, env, sha, 0)
	// 占位请求挂在一个有界 context 上（整体上限远大于实际占用时长）：既是安全网，也让控制面早退能中断它。
	ctx, cancel, err := harness.RequestContext(req.Context(), bp429HoldTimeout, env.cp)
	if err != nil {
		t.Fatalf("构造占位下载 context 失败：%v", err)
	}
	t.Cleanup(cancel)
	resp, err := holderClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("占位下载请求失败：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("占位下载应回 200（真实字节流），实际 %d；响应片段：%s", resp.StatusCode, bp429Snippet(raw))
	}
	// 读到首字节：确认服务端已在 ServeContent 内持续写（额度已被占），且远未写完（还有 64 MiB 未出）。
	head := make([]byte, 1)
	if _, err := io.ReadFull(resp.Body, head); err != nil {
		t.Fatalf("读占位下载首字节失败：%v", err)
	}
	return resp.Body
}

// bp429PollDownloadRecovered 轮询直到同 sha 的 Range 下载恢复成功（200 / 206），返回是否恢复。
// 判据是「实际取到成功状态」，而不是固定 sleep——服务端写失败与 defer 归还额度之间的窗口长度不确定。
func bp429PollDownloadRecovered(t *testing.T, env *bp429Env, sha string) bool {
	t.Helper()
	deadline := time.Now().Add(bp429SlotTimeout)
	for {
		if err := env.cp.CheckEarlyExit(); err != nil {
			t.Fatalf("等下载额度归还期间控制面已早退：%v", err)
		}
		req := bp429NewGETRequest(t, env, sha, 1)
		resp, raw := bp429Do(t, env.cp, req)
		switch resp.StatusCode {
		case http.StatusOK, http.StatusPartialContent:
			return true
		case http.StatusTooManyRequests:
			// 仍在占用中：继续等（这是本用例刻意观测的中间态）。
		default:
			t.Fatalf("轮询下载恢复时遇到非预期状态 %d；响应片段：%s", resp.StatusCode, bp429Snippet(raw))
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(bp429PollInterval)
	}
}

// bp429AwaitOutcome 等待后台请求收尾并断言期望状态码。
func bp429AwaitOutcome(t *testing.T, done <-chan bp429Outcome, wantStatus int, label string) {
	t.Helper()
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("%s：请求层失败：%v", label, out.err)
		}
		if out.status != wantStatus {
			t.Fatalf("%s，实际 %d；响应片段：%s", label, out.status, bp429Snippet([]byte(out.body)))
		}
	case <-time.After(bp429HoldTimeout):
		t.Fatalf("%s：在 %s 内未收尾", label, bp429HoldTimeout)
	}
}

// bp429NewPutRequest 构造一次真实的流式上传请求（双 header 鉴权 + 显式 Content-Length）。
func bp429NewPutRequest(t *testing.T, env *bp429Env, sha string, body io.Reader, size int64) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, env.baseURL+"/beacon/v2/stream/delivery/blobs/"+sha, body)
	if err != nil {
		t.Fatalf("构造上传请求失败：%v", err)
	}
	// 必须显式声明长度：handler 对 ContentLength<0 回 411；也让标准库在发体前 flush 请求头
	// （这正是「服务端先占额度、请求体后到」能够成立的前提）。
	req.ContentLength = size
	bp429SetAgentHeaders(req, env.ident)
	return req
}

// bp429NewGETRequest 构造一次真实的流式下载请求；rangeBytes>0 时只取前 rangeBytes 字节（Range 断点语义）。
func bp429NewGETRequest(t *testing.T, env *bp429Env, sha string, rangeBytes int64) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, env.baseURL+"/beacon/v2/stream/delivery/blobs/"+sha, nil)
	if err != nil {
		t.Fatalf("构造下载请求失败：%v", err)
	}
	if rangeBytes > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", rangeBytes-1))
	}
	bp429SetAgentHeaders(req, env.ident)
	return req
}

// bp429HeadBlob 发一次真实 HEAD（就绪查询 / 断点判断路径），返回响应。
// HEAD 按 HTTP 规范不带响应体，故只断言状态码与响应头，不解析错误体。
func bp429HeadBlob(t *testing.T, env *bp429Env, sha string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodHead, env.baseURL+"/beacon/v2/stream/delivery/blobs/"+sha, nil)
	if err != nil {
		t.Fatalf("构造 HEAD 请求失败：%v", err)
	}
	bp429SetAgentHeaders(req, env.ident)
	resp, _ := bp429Do(t, env.cp, req)
	return resp
}

// bp429SetAgentHeaders 给请求挂上 v2 agent 数据面鉴权所需的双 header（+ bootId 供陈旧判定）。
func bp429SetAgentHeaders(req *http.Request, ident bp429Identity) {
	req.Header.Set("X-Beacon-Token", ident.AccessToken)
	req.Header.Set("X-Beacon-Identity", ident.IdentityID)
	req.Header.Set("X-Beacon-Boot", ident.BootID)
}

// bp429Do 执行一次受 guard 保护的请求并读全响应体（请求层失败即 Fatal，不静默）。
// 读完立刻关掉原始 Body 以归还连接，再用可重读副本顶替，调用方仍可自行读取。
func bp429Do(t *testing.T, cp *harness.ControlPlane, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	resp, err := harness.DoRequestWithGuard(req, bp429RequestTimeout, cp)
	if err != nil {
		t.Fatalf("%s %s 请求失败：%v", req.Method, req.URL.Path, err)
	}
	raw, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		t.Fatalf("读 %s %s 响应体失败：%v（HTTP %d）", req.Method, req.URL.Path, readErr, resp.StatusCode)
	}
	if closeErr != nil {
		t.Fatalf("关闭 %s %s 响应体失败：%v", req.Method, req.URL.Path, closeErr)
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	return resp, raw
}

// bp429GETBody 发一次 GET 并断言状态码，返回读回的字节（rangeBytes>0 时只取前 rangeBytes 字节）。
func bp429GETBody(t *testing.T, env *bp429Env, sha string, rangeBytes int64, wantStatus int) []byte {
	t.Helper()
	resp, raw := bp429Do(t, env.cp, bp429NewGETRequest(t, env, sha, rangeBytes))
	if resp.StatusCode != wantStatus {
		t.Fatalf("GET blobs/%s 应回 %d，实际 %d；响应片段：%s", sha, wantStatus, resp.StatusCode, bp429Snippet(raw))
	}
	return raw
}

// bp429AssertGETStatus 发一次 GET 并断言状态码与业务码。
func bp429AssertGETStatus(t *testing.T, env *bp429Env, sha string, wantStatus int, wantCode string) {
	t.Helper()
	resp, raw := bp429Do(t, env.cp, bp429NewGETRequest(t, env, sha, 0))
	if resp.StatusCode != wantStatus {
		t.Fatalf("GET blobs/%s 应回 %d，实际 %d；响应片段：%s", sha, wantStatus, resp.StatusCode, bp429Snippet(raw))
	}
	if code := bp429ErrorCode(t, raw); code != wantCode {
		t.Fatalf("GET blobs/%s 业务码应为 %s，实际 %q；响应片段：%s", sha, wantCode, code, bp429Snippet(raw))
	}
}

// bp429ErrorCode 从统一错误体 {code,message,traceId} 中取出业务码。
func bp429ErrorCode(t *testing.T, raw []byte) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("解析错误响应体失败（%q）：%v", bp429Snippet(raw), err)
	}
	return body.Code
}

// bp429WaitUploadingRow 轮询直到库中出现该 sha 的 uploading 占位行——该行写在 tryAcquire 之后，
// 故「行在」即「额度已被真实请求占住」。这是上传用例的同步点：不看时钟、不做固定 sleep。
func bp429WaitUploadingRow(t *testing.T, db *gorm.DB, sha string, guard harness.ProcessGuard) {
	t.Helper()
	var lastState string
	deadline := time.Now().Add(bp429SlotTimeout)
	for {
		// 库侧轮询同样要感知控制面早退：进程崩了必须立刻报错，而不是一路等到超时。
		if guard != nil {
			if err := guard.CheckEarlyExit(); err != nil {
				t.Fatalf("等上传占位行期间控制面已早退（sha=%s）：%v", sha, err)
			}
		}
		// 用 Find 而非 First/Take：未命中是正常的轮询中间态，不该被 GORM 记成 "record not found" 噪声。
		var rows []struct {
			State string
		}
		if e := db.Table("delivery_blob").Select("state").Where("sha256 = ?", sha).Limit(1).Find(&rows).Error; e == nil && len(rows) > 0 {
			lastState = rows[0].State
			if rows[0].State == model.DeliveryBlobStateUploading {
				return
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("等上传占位行超时（%s）：sha=%s 最近状态=%q——占位请求未进入 Store 临界区",
				bp429SlotTimeout, sha, lastState)
		}
		time.Sleep(bp429PollInterval)
	}
}

// bp429BlobRowCount 统计该 sha 的 delivery_blob 元数据行数（被拒上传应恒为 0）。
func bp429BlobRowCount(t *testing.T, db *gorm.DB, sha string) int64 {
	t.Helper()
	var count int64
	if err := db.Table("delivery_blob").Where("sha256 = ?", sha).Count(&count).Error; err != nil {
		t.Fatalf("统计 delivery_blob 行数失败（sha=%s）：%v", sha, err)
	}
	return count
}

// bp429ScrapeMetrics 抓一次 /metrics（Prometheus 文本，内网信任面不挂管理台鉴权）。
func bp429ScrapeMetrics(t *testing.T, baseURL string) string {
	t.Helper()
	client := &http.Client{Timeout: bp429RequestTimeout}
	resp, err := client.Get(baseURL + "/metrics")
	if err != nil {
		t.Fatalf("抓取 /metrics 失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		t.Fatalf("读取 /metrics 响应失败：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics 应回 200，实际 %d", resp.StatusCode)
	}
	return string(raw)
}

// bp429AssertREDSeries 断言交付路由上存在该 方法+状态码 的请求计数时序，且取值 ≥1。
func bp429AssertREDSeries(t *testing.T, metrics, method string, status int) {
	t.Helper()
	prefix := fmt.Sprintf(`beacon_http_requests_total{method="%s",route="%s",status="%d"} `, method, bp429DeliveryRoute, status)
	for _, line := range strings.Split(metrics, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		var value float64
		if _, err := fmt.Sscanf(strings.TrimPrefix(line, prefix), "%g", &value); err != nil {
			t.Fatalf("解析指标值失败（%q）：%v", line, err)
		}
		if value < 1 {
			t.Fatalf("指标 %s应 ≥1，实际 %v", prefix, value)
		}
		t.Logf("【指标旁证】/metrics: %s%v", prefix, value)
		return
	}
	t.Fatalf("/metrics 缺少时序：%s（该 429 未进入 HTTP RED 指标）", prefix)
}

// bp429LogServerAccess 在控制面 stderr 日志里找出这条 429 访问记录并打印。
// 这是第三方书证（由服务端自己写、不经测试的断言路径），且带 traceId，与响应体里的 traceId 可互校。
// 找不到不算失败：日志级别可能被环境压制，该项只是加厚证据链的旁证。
func bp429LogServerAccess(t *testing.T, env *bp429Env, method, sha string, status int) {
	t.Helper()
	raw, err := os.ReadFile(env.stderrPath)
	if err != nil {
		t.Logf("提示：读取控制面日志（%s）失败，跳过访问日志旁证：%v", env.stderrPath, err)
		return
	}
	needle := "delivery/blobs/" + sha
	wantStatus := fmt.Sprintf("状态=%d", status)
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "方法="+method) && strings.Contains(line, needle) && strings.Contains(line, wantStatus) {
			t.Logf("【服务端书证】控制面访问日志：%s", strings.TrimSpace(line))
			return
		}
	}
	t.Logf("提示：控制面访问日志（%s）未匹配到 %s %s 状态=%d 行，访问日志级别可能被压制",
		env.stderrPath, method, needle, status)
}

// bp429SHA256Hex 返回内容的小写 hex sha256（blob 内容寻址身份）。
func bp429SHA256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// bp429Snippet 截断响应片段便于日志 / 失败信息阅读（不打印超大响应体）。
func bp429Snippet(raw []byte) string {
	const limit = 300
	text := strings.TrimSpace(string(raw))
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
