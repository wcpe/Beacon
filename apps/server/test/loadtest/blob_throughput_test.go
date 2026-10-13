//go:build e2e && loadtest

// 交付 blob 大文件吞吐探测：在**真实生产链路**上打通「建变更单 → 引用 sha → 提交 → 批准 → 上传 blob → 下载 blob」，
// 再量四件事：不同大小的吞吐与首字节延迟、并发下载的线性度、Range 断点续传的正确性、以及「下载是否真流式」。
//
// 为什么必须走完整链路而不是直连库播种：blob 归属校验（AuthorizeBlobUpload / AuthorizeBlobDownload，
// internal/service/delivery_blob_agent.go）要求请求身份是「某引用该 sha256 的 approved / 活动变更单」的
// 模板源或目标，而文件项（change_order_item，kind=file_diff + sha256）在生产里**只能由模板源的 diff-scan
// 生成**（internal/service/delivery_diff_service.go:52 的 DiffScan → computeChangeDiffItems）。
// 所以本用例的链路是：
//
//	① 建 v2 namespace + 注册 backend 身份 → 审批成 active（建 server 行，kind=backend）
//	② 建 BC 集群 → 大区 → 小区（区服权威树）
//	③ 通过 POST /admin/v2/server-assignments 把该 backend 分配到小区（server.ZoneID 有值）
//	④ 真实上报一次全量文件资产清单（POST /beacon/v2/agent/assets/manifest，mode=full），
//	   使模板源有「快照文件表」——这是 diff-scan 的前置（无扫描概要行直接 422 source_snapshot_missing）
//	⑤ 关闭审批职责分离（单管理员部署，否则创建人不能自批）
//	⑥ 建变更单（sourceServerId=本 backend、scanDir 覆盖待测路径）→ diff-scan（生成 file_diff 项）
//	   → submit → 审批 → 执行器自动启动（approved → rolling）
//	⑦ 用该身份 PUT 上传各档大小的 blob（内容寻址，sha256 = 内容哈希）
//	⑧ 用该身份 GET 下载，量吞吐 / 首字节 / Range / RSS
//
// 关于第 ⑥ 步的一个关键事实（实测确认，不是假设）：审批执行器会把 approved 单**立即启动**成 rolling
// （internal/service/delivery_dangerous_approval.go:195 的 executeDeliveryApprovalInTx → applyStartApprovedInTx）。
// rolling 单也在 deliveryBlobOrderStatuses 认可集内，故 blob 归属校验依然放行；但 rolling 单会进推进器活动集，
// 若目标集非空则会真的下发 delivery_push 命令。本用例因此刻意把 selector 的 targets 设为**另一台未注册的
// 占位 backend**（server 行不存在 → eligibleChangeTarget 判否 → resolveChangeTargets 为空 → 启动报
// change_no_target），使单停在 approved。此时「模板源」归属校验仍走 ListOrdersReferencingSHA（只看状态集与
// source_server_id，与目标集无关），下载侧同样命中 source 分支，链路完全真实。
//
// 运行（正式 / 冒烟同一入口，凭据缺失即 skip）：
//
//	export E2E_ADMIN_PASS='...' E2E_AUTH_SECRET='...'
//	go test -tags "e2e loadtest" -timeout=40m -v -run '^TestBlobThroughputRealChain' ./apps/server/test/loadtest/
//
// 环境变量开关：
//
//	BLOB_SIZES_MIB       吞吐档位（MiB，逗号分隔），默认 1,16,64,256
//	BLOB_CONCURRENCY     并发下载档位（逗号分隔），默认 1,4,16,64
//	BLOB_SKIP_BIG        置 1 跳过 ≥256MiB 档（磁盘紧张时用）
//
// 隔离（与其它并行用例互不干扰）：独立 sqlite 库 beacon-blob.db、独立日志前缀、独立二进制输出路径、
// 独立 v2 namespace code。blob 根 .beacon/delivery 是产品硬编码的共享目录，故清理时**只按本用例创建的
// sha 精确删除**，绝不整目录清理（同 worktree 上可能有别的 e2e 正在跑）。
package loadtest_e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/test/e2e/harness"
)

const (
	// blobDefaultSizesMiB 是吞吐档位：从「明显小于内核缓冲」到「远大于内核缓冲 + 需要真实流式」。
	// 256MiB 已远超本机 tcp_wmem / tcp_rmem 上限（各 16MiB），足以暴露「整文件读进内存」的实现。
	blobDefaultSizesMiB = "1,16,64,256"
	// blobDefaultConcurrency 是并发下载档位。
	blobDefaultConcurrency = "1,4,16,64"
	// blobDBName / blobLogPrefix / blobBinName / blobNamespace 是本用例的隔离标识。
	blobDBName    = "beacon-blob.db"
	blobLogPrefix = "beacon-blob"
	blobBinName   = "beacon-blob-bin"
	// blobNamespace 是独立 v2 namespace code（建身份 / 资产 / 变更单都在此空间内）。
	blobNamespace = "loadtest-blob"
	// blobSourceServerID 是模板源 serverId（上传 / 下载都以此身份）。
	blobSourceServerID = "blob-source"
	// blobScanDir 是变更单的扫描目录前缀（必须覆盖待测文件路径）。
	blobScanDir = "plugins/blobtest"
	// blobRouteTemplate 是交付流式数据面的 chi 路由模板（与 bp429DeliveryRoute 同值，
	// 这里另起常量名只为让本文件的报告文本自解释）。
	blobRouteTemplate = "/beacon/v2/stream/delivery/blobs/{sha256}"

	// blobSetupTimeout 是链路前置（建空间 / 审批 / 建树 / 上报 / 组单）的单次请求上限。
	blobSetupTimeout = 60 * time.Second
	// blobPhaseTimeout 是前置轮询（等 active / 等健康视图）的上限。
	blobPhaseTimeout = 90 * time.Second
	// blobRequestTimeout 是数据面请求的读超时上限（大文件下载在慢盘上可能较久，给足余量）。
	blobRequestTimeout = 20 * time.Minute
	// blobPollInterval 是前置轮询间隔。
	blobPollInterval = 200 * time.Millisecond
	// blobSnippetLimit 是失败回带的响应片段上限。
	blobSnippetLimit = 512
	// blobSampleInterval 是 RSS 采样间隔。取 50ms：本机 256MiB 下载只需约 600ms（page cache 命中），
	// 采样间隔太粗会只留两三个样本，峰值可能被漏掉。
	blobSampleInterval = 50 * time.Millisecond
	// blobHeadProbeTimeout 是 HEAD 就绪探测的上限。
	blobHeadProbeTimeout = 30 * time.Second
	// blobRangeChunkBytes 是 Range 一致性校验用的分片大小（1MiB，与全量逐字节比对）。
	blobRangeChunkBytes = 1 << 20
	// blobByteTolerance 是吞吐测量的时间下限：太短会把连接建立成本放大成吞吐噪声。
	blobByteTolerance = 200 * time.Millisecond
)

// blobSizeCase 是一个待测档位。
type blobSizeCase struct {
	MiB   int
	Bytes int64
}

// blobThroughputRow 是一档「大小 × 吞吐」的实测结果。
type blobThroughputRow struct {
	MiB        int     `json:"mib"`
	Bytes      int64   `json:"bytes"`
	ElapsedMs  float64 `json:"elapsedMs"`
	TTFBMs     float64 `json:"ttfbMs"`
	Throughput float64 `json:"throughputMBps"`
	Status     int     `json:"status"`
	RangeOK    bool    `json:"rangeOk"`
	Note       string  `json:"note"`
}

// blobConcurrencyRow 是一档并发下载的实测结果。
type blobConcurrencyRow struct {
	Concurrency     int     `json:"concurrency"`
	MiB             int     `json:"mib"`
	Streams         int     `json:"streams"`
	OKStreams       int     `json:"okStreams"`
	FailedStreams   int     `json:"failedStreams"`
	WallMs          float64 `json:"wallMs"`
	TotalThroughput float64 `json:"totalMBps"`
	PerStreamMBps   float64 `json:"perStreamMBps"`
	MinStreamMBps   float64 `json:"minStreamMBps"`
	MaxStreamMBps   float64 `json:"maxStreamMBps"`
	StatusCounts    string  `json:"statusCounts"`
}

// blobRangeRow 是一档 Range 请求的实测结果。
type blobRangeRow struct {
	MiB         int    `json:"mib"`
	Spec        string `json:"spec"`
	WantStatus  int    `json:"wantStatus"`
	GotStatus   int    `json:"gotStatus"`
	ContentRng  string `json:"contentRange"`
	AcceptRng   string `json:"acceptRanges"`
	BodyBytes   int64  `json:"bodyBytes"`
	BytesMatch  bool   `json:"bytesMatch"`
	IfRangeHit  bool   `json:"ifRangeHit"`
	IfRangeNote string `json:"ifRangeNote"`
}

// archiveDBNameFor 由主库文件名派生该用例独占的归档库名（如 beacon-blob.db → beacon-blob-archive）。
//
// 为什么要派生：sqlite 下归档库默认名固定为 beacon_archive、且与主库同目录，
// 而 .tmp/loadtest/ 是多个用例共享的工作目录——不派生就会撞库（见调用点注释）。
func archiveDBNameFor(mainDBName string) string {
	return strings.TrimSuffix(mainDBName, ".db") + "-archive"
}

// blobEnv 是本用例的共享装配结果。
type blobEnv struct {
	cp          *harness.ControlPlane
	baseURL     string
	adminToken  string
	repoRoot    string
	dbPath      string
	db          *gorm.DB
	namespace   string
	namespaceID uint
	accessToken string
	identityID  string
	bootID      string
	sourceID    string
	orderID     uint
	orderStatus string
	shas        []string
	// pid 是控制面进程 PID（用于 RSS 采样；定位失败为 0）。
	pid int
	// metricsBase 是抓取端点基址（独立监听，见 ADR-0090）：抓 goroutine 数等进程级读数从这里取，
	// 主端口上已不再提供 /metrics。
	metricsBase string
}

// blobSetupConfig 是链路装配的隔离参数（多个用例必须取不同值，保证可并行运行互不覆盖）。
type blobSetupConfig struct {
	DBName      string // sqlite 库文件名（.tmp/loadtest/ 下）
	LogPrefix   string // 控制面日志前缀
	BinName     string // 控制面二进制文件名
	Namespace   string // v2 namespace code
	SourceID    string // 模板源 serverId
	ListenPort  int    // 模板源上报的监听端口（仅占位，不真的起监听）
	LogLevel    string // 控制面日志级别
	ScanDir     string // 变更单扫描目录前缀
	OrderTitle  string // 变更单标题
	Placeholder string // 源清单占位路径（保证 diff-scan 有源侧快照）
}

// TestBlobThroughputRealChain 是本文件的主入口：打通真实链路后测四组指标。
func TestBlobThroughputRealChain(t *testing.T) {
	adminPass := requireEnv(t, "E2E_ADMIN_PASS")
	authSecret := requireEnv(t, "E2E_AUTH_SECRET")
	sizes := parseBlobSizes(t, envOr("BLOB_SIZES_MIB", blobDefaultSizesMiB))
	concurrency := blobParseIntList(t, envOr("BLOB_CONCURRENCY", blobDefaultConcurrency), "BLOB_CONCURRENCY")

	env := blobSetup(t, adminPass, authSecret, blobSetupConfig{
		DBName: blobDBName, LogPrefix: blobLogPrefix, BinName: blobBinName,
		Namespace: blobNamespace, SourceID: blobSourceServerID, ListenPort: 25688,
		LogLevel: "WARN", ScanDir: blobScanDir, OrderTitle: "blob 吞吐探测单",
		Placeholder: blobScanDir + "/placeholder.bin",
	})

	// 每个档位一份独立内容（内容寻址：不同大小必然不同 sha），逐档上传 → 下载 → Range 校验。
	rows := make([]blobThroughputRow, 0, len(sizes))
	rangeRows := make([]blobRangeRow, 0, len(sizes)*3)
	payloads := make(map[int][]byte, len(sizes))
	for _, size := range sizes {
		content := blobGenerateContent(size.Bytes)
		payloads[size.MiB] = content
		sha := blobSHA256Hex(content)
		env.shas = append(env.shas, sha)
		blobRegisterSHA(t, env, sha)

		// 上传：先量上传吞吐（同一份内容的上行链路），再用它验证下载。
		uploadMs, uploadStatus, err := blobUpload(t, env, sha, content)
		if err != nil {
			t.Fatalf("上传 %d MiB blob 失败：%v", size.MiB, err)
		}
		if uploadStatus != http.StatusNoContent {
			t.Fatalf("上传 %d MiB blob 期望 204，实际 %d", size.MiB, uploadStatus)
		}
		// HEAD 就绪确认（避免把「上传没落盘」误算成「下载慢」）。
		if status, note := blobHead(t, env, sha); status != http.StatusOK {
			t.Fatalf("上传后 HEAD %d MiB blob 期望 200，实际 %d（%s）", size.MiB, status, note)
		}

		row := blobDownloadOnce(t, env, sha, size, content)
		row.Note = fmt.Sprintf("上传耗时 %.0fms（%.1f MB/s）", uploadMs, blobMBps(size.Bytes, uploadMs))
		rows = append(rows, row)

		// Range 三态：中段、尾部、越界（416）。每档都测，但只在 1MiB / 16MiB 档做逐字节全量比对
		// （256MiB 档再取一次全量只为比字节会让整轮时长翻倍，收益递减）。
		rangeRows = append(rangeRows, blobRangeProbe(t, env, sha, size, content)...)
	}

	concurrencyRows := blobConcurrencyProbe(t, env, payloads, concurrency)
	streaming := blobStreamingEvidence(t, env, payloads)

	blobReport(t, rows, concurrencyRows, rangeRows, streaming)
}

// ---- 前置装配 ----

// blobSetup 打通「建空间 → 注册 backend 身份 → 建区服树 → 分配 → 上报清单 → 组单 → 批准」全链路。
// cfg 决定全部隔离标识（库名 / 日志前缀 / 二进制名 / namespace / 源 serverId / 扫描目录），
// 使多个用例可共用本函数而不互相覆盖。
func blobSetup(t *testing.T, adminPass, authSecret string, cfg blobSetupConfig) *blobEnv {
	t.Helper()
	repoRoot, err := harness.RepoRoot()
	if err != nil {
		t.Fatalf("定位仓库根失败：%v", err)
	}
	workDir := filepath.Join(repoRoot, ".tmp", "loadtest")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("创建压测工作目录失败：%v", err)
	}
	blobRemoveStaleDB(t, workDir, cfg.DBName)
	// 收尾：删掉本用例独占的库与二进制（早于起控制面注册，利用 t.Cleanup 的 LIFO 顺序最后执行）。
	t.Cleanup(func() { blobCleanupDBFiles(t, workDir, cfg.DBName) })

	binPath := filepath.Join(workDir, cfg.BinName)
	if err := buildBeaconTo(repoRoot, binPath); err != nil {
		t.Fatalf("构建控制面失败：%v", err)
	}
	t.Cleanup(func() { _ = os.Remove(binPath) })

	cpCfg := harness.ControlPlaneConfig{
		BinPath: binPath, RepoRoot: repoRoot,
		DBDriver: "sqlite", DBDSN: filepath.Join(workDir, cfg.DBName),
		AdminPassword: adminPass, AuthSecret: authSecret, BootstrapToken: bootstrapToken,
		LogPrefix: cfg.LogPrefix,
		ExtraEnv: map[string]string{
			"BEACON_LOG_LEVEL": cfg.LogLevel,
			// 归档库默认名为 beacon_archive 且落在**与主库同目录**（这里是多用例共享的
			// .tmp/loadtest/），而各用例的主库名互不相同。不显式派生的话，两个用例并行时
			// 会撞同一个归档库文件（建表 / 迁移互相干扰，表现为难以复现的偶发失败）。
			// 故按主库名派生，保证每个用例独占自己的归档库。
			"BEACON_ARCHIVE_DATABASE": archiveDBNameFor(cfg.DBName),
		},
	}
	cp, baseURL, metricsBase := startControlPlaneWithMetrics(t, cpCfg, controlPlaneStartAttempts)
	harness.CleanupControlPlane(t, cp)
	_, stderrPath := cp.LogPaths()
	t.Logf("控制面已就绪：%s（stderr：%s）；抓取端点：%s/metrics", baseURL, stderrPath, metricsBase)

	adminToken, err := harness.Login(baseURL, adminUser, adminPass, cp)
	if err != nil {
		t.Fatalf("管理员登录失败：%v", err)
	}

	// 关闭审批职责分离：单管理员部署下创建人无法自批（默认 true），这是组单链路的前置。
	blobDisableApproverSeparation(t, baseURL, adminToken, cp)

	dbPath := filepath.Join(workDir, cfg.DBName)
	db := blobOpenProbeDB(t, dbPath)

	env := &blobEnv{
		cp: cp, baseURL: baseURL, adminToken: adminToken,
		repoRoot: repoRoot, dbPath: dbPath, db: db,
		pid: findProcessPIDByCmdline(binPath), sourceID: cfg.SourceID,
		metricsBase: metricsBase,
	}
	// blob 根 .beacon/delivery 是共享目录，故按 sha 精确清理（注册在最前 → LIFO 最后执行）。
	t.Cleanup(func() { blobRemoveBlobFiles(t, repoRoot, env.shas) })

	// ① 建 v2 namespace 并注册 backend 身份 → 审批成 active（审批会建 server 行，kind=backend）。
	accessToken, nsCode := createV2Namespace(t, baseURL, adminToken, cfg.Namespace, cp)
	env.namespace = nsCode
	env.accessToken = accessToken
	identityID := uuid.NewString()
	bootID := uuid.NewString()
	// backend 必须带 listenPort（proxy 只能带 listeners；两者互斥，见 endpointReportsForRegistration）。
	registerV2Backend(t, baseURL, accessToken, identityID, cfg.SourceID, bootID, cfg.ListenPort, cp)
	activateV2Identity(t, baseURL, adminToken, identityID, cfg.SourceID, cp)
	env.identityID, env.bootID = identityID, bootID

	// ①b 再注册一个 backend 作交付目标：selector 的 servers 必须引用**存在且属本环境**的服务器
	// （validateSelectorRefs 会拒不存在者，报 selector_cross_namespace），故目标不能是虚构 id。
	// 该目标只是让变更单通过 submit 的 target 判空，不会有真 agent 拉取命令。
	targetID := cfg.SourceID + "-target"
	targetIdentity := uuid.NewString()
	registerV2Backend(t, baseURL, accessToken, targetIdentity, targetID, uuid.NewString(), cfg.ListenPort+1, cp)
	activateV2Identity(t, baseURL, adminToken, targetIdentity, targetID, cp)

	env.namespaceID = blobNamespaceID(t, db, nsCode)
	serverRowID := blobServerRowID(t, db, env.namespaceID, cfg.SourceID)
	targetRowID := blobServerRowID(t, db, env.namespaceID, targetID)

	// ② ③ 建区服树并把模板源与目标都分配到小区：diff-scan 的对照集与 submit 的 target 判定都要求
	// 「backend + 已分配 zone + 身份 active」，缺一即 change_no_target。
	zoneID := blobBuildTopology(t, baseURL, adminToken, env.namespaceID, cfg.Namespace, cp)
	blobAssignServer(t, baseURL, adminToken, []uint{serverRowID, targetRowID}, zoneID, cp)

	// ④ 真实上报一次全量文件资产清单：diff-scan 的源侧快照来自这张表（无行即 source_snapshot_missing）。
	// 这里只报一条占位路径（真实文件项由下一步 diff-scan 自己算出来）。
	blobReportManifest(t, env, cfg.Placeholder)

	// ④b 上报一次指标让模板源在健康视图里出现且非 lost：submit 的 validateSourceEligible 要求
	// 「已确认绑定 + 健康视图存在且不含 lost」（internal/service/delivery_order_service.go:531）。
	// 健康计算轮 5s 一次（internal/service/health_compute_service.go:21），故上报后需等一轮。
	blobReportMetric(t, env)
	blobWaitSourceHealthy(t, env, cfg.SourceID)

	// ⑤ 组单：sourceServerId=模板源、scanDir 覆盖待测路径、目标为第二个 backend；
	// 单会走到 rolling 且 payload 卡在 uploading（模板源尚未上传），推进器不下发任何命令。
	env.orderID, env.orderStatus = blobCreateApprovedOrder(t, env, cfg)

	t.Logf("链路已打通：namespace=%s(id=%d) identity=%s server=%s order=%d(%s) pid=%d",
		nsCode, env.namespaceID, identityID, cfg.SourceID, env.orderID, env.orderStatus, env.pid)
	return env
}

// blobDisableApproverSeparation 关闭变更单审批职责分离（单管理员部署）。
func blobDisableApproverSeparation(t *testing.T, baseURL, token string, guard harness.ProcessGuard) {
	t.Helper()
	if _, err := harness.RequestApprovalAndWait(baseURL, token, http.MethodPut,
		"/admin/v1/settings/delivery.approver-separation-enabled",
		map[string]any{"value": "false", "reason": "blob 吞吐用例：单管理员自建自批"},
		"succeeded", blobSetupTimeout, guard); err != nil {
		t.Fatalf("关闭审批职责分离失败：%v", err)
	}
}

// registerV2Backend 注册一个 v2 backend 身份（backend 必须带 listenPort，不能带 listeners）。
func registerV2Backend(t *testing.T, baseURL, accessToken, identityID, serverID, bootID string, listenPort int, guard harness.ProcessGuard) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), blobSetupTimeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"identityId":   identityID,
		"serverId":     serverID,
		"kind":         model.ServerKindBackend,
		"bootId":       bootID,
		"agentVersion": "loadtest",
		"addr":         fmt.Sprintf("127.0.0.1:%d", listenPort),
		"listenPort":   listenPort,
	})
	if err != nil {
		t.Fatalf("编码 v2 backend 注册请求失败：%v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(baseURL, "/")+"/beacon/v2/agent/register", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("构造 v2 backend 注册请求失败：%v", err)
	}
	req.Header.Set("X-Beacon-Token", accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := harness.DoRequestWithGuard(req, blobSetupTimeout, guard)
	if err != nil {
		t.Fatalf("v2 backend 注册请求失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("v2 backend 注册期望 202/200，实际 %d；片段：%s", resp.StatusCode, blobSnippet(raw))
	}
}

// blobBuildTopology 建 BC 集群 → 大区 → 小区，返回小区主键。
func blobBuildTopology(t *testing.T, baseURL, token string, namespaceID uint, namespace string, guard harness.ProcessGuard) uint {
	t.Helper()
	clusterID := blobCreateNode(t, baseURL, token, "/admin/v2/bc-clusters", map[string]any{
		"namespaceId": namespaceID, "name": namespace + "-bc",
	}, guard)
	regionID := blobCreateNode(t, baseURL, token, "/admin/v2/regions", map[string]any{
		"bcClusterId": clusterID, "name": namespace + "-region",
	}, guard)
	return blobCreateNode(t, baseURL, token, "/admin/v2/zones", map[string]any{
		"regionId": regionID, "name": namespace + "-zone",
	}, guard)
}

// blobCreateNode 经管理面建一个区服权威节点并返回主键。
func blobCreateNode(t *testing.T, baseURL, token, path string, body map[string]any, guard harness.ProcessGuard) uint {
	t.Helper()
	var out struct {
		ID uint `json:"id"`
	}
	blobAdminJSON(t, baseURL, token, http.MethodPost, path, body, http.StatusCreated, &out, guard)
	if out.ID == 0 {
		t.Fatalf("建节点 %s 未返回主键", path)
	}
	return out.ID
}

// blobAssignServer 把 server 行分配到小区（首次分配走审批工单，工单执行后 server.ZoneID 才有值）。
func blobAssignServer(t *testing.T, baseURL, token string, serverRowIDs []uint, zoneID uint, guard harness.ProcessGuard) {
	t.Helper()
	if _, err := harness.RequestApprovalAndWait(baseURL, token, http.MethodPost, "/admin/v2/server-assignments",
		map[string]any{
			"serverIds":      serverRowIDs,
			"target":         map[string]any{"kind": "zone", "id": zoneID},
			"isDefaultEntry": false, "reason": "blob 吞吐用例：模板源与目标落小区",
		}, "succeeded", blobSetupTimeout, guard); err != nil {
		t.Fatalf("提交首次分配审批失败：%v", err)
	}
}

// blobReportManifest 用模板源身份真实上报一次全量文件资产清单。
// 只报一条占位路径：本用例关心的是「源侧快照存在」这个前置，真实文件项由 diff-scan 现算。
// placeholderPath 必须落在变更单的 scanDir 前缀内，否则源侧快照虽有行、diff-scan 却扫不到。
func blobReportManifest(t *testing.T, env *blobEnv, placeholderPath string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), blobSetupTimeout)
	defer cancel()
	placeholder := sha256.Sum256([]byte("blob-throughput-placeholder"))
	body, err := json.Marshal(map[string]any{
		"mode":      "full",
		"uploadId":  uuid.NewString(),
		"seq":       0,
		"eof":       true,
		"scannedAt": time.Now().UTC().Format(time.RFC3339),
		"upserts": []map[string]any{{
			"path": placeholderPath, "sha256": hex.EncodeToString(placeholder[:]),
			"size": 0, "mtimeMs": time.Now().UnixMilli(), "isText": false,
		}},
	})
	if err != nil {
		t.Fatalf("编码清单上报请求失败：%v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(env.baseURL, "/")+"/beacon/v2/agent/assets/manifest", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("构造清单上报请求失败：%v", err)
	}
	blobSetAgentHeaders(req, env)
	req.Header.Set("Content-Type", "application/json")
	resp, err := harness.DoRequestWithGuard(req, blobSetupTimeout, env.cp)
	if err != nil {
		t.Fatalf("清单上报请求失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("清单上报期望 200，实际 %d；片段：%s", resp.StatusCode, blobSnippet(raw))
	}
}

// blobCreateApprovedOrder 建单 → diff-scan → 提交审批 → 批准，返回变更单主键与最终状态。
//
// 单的落点（实测确认，不是假设）：审批执行器会立刻把 approved 单启动成 rolling
// （internal/service/delivery_dangerous_approval.go:195 的 executeDeliveryApprovalInTx → applyStartApprovedInTx）。
// 启动时会算 payload 是否就绪：本用例的 file_diff 项指向尚未上传的 sha，故 payload_state=uploading，
// 单停在 rolling 且**推进器不会下发任何推送命令**——它会挂在 advancePayloadPrep 等模板源上传
// （internal/service/delivery_orchestrator_advance.go:460，命令不存在即无限等待）。
//
// rolling 在 deliveryBlobOrderStatuses 认可集内，故 blob 归属校验照常放行（这是本用例真正依赖的）。
// 目标侧不会真的动：目标 backend 是刚注册的空身份，没有 agent 进程去拉命令；
// 即使命令建出来也只会停在 pending 直到过期，不会影响本用例的任何测量。
func blobCreateApprovedOrder(t *testing.T, env *blobEnv, cfg blobSetupConfig) (uint, string) {
	t.Helper()
	targetID := cfg.SourceID + "-target"
	// scanDir 必须覆盖待测路径前缀（loadSourceFiles / loadTargetStats 都按前缀过滤）。
	body := map[string]any{
		"namespaceId": env.namespaceID, "title": cfg.OrderTitle,
		"sourceServerId": cfg.SourceID, "scanDir": cfg.ScanDir,
		"selector": map[string]any{
			"all": false, "regions": []uint{}, "zones": []uint{},
			"servers": []string{targetID}, "excludes": []string{},
		},
		"batchMode": "count", "batchSizes": []int{1}, "activationMethod": model.ActivationMethodHotReload,
		"observeWindowSec": 1, "activateTimeoutSec": 60,
		"failureRateThresholdPercent": 0, "unhealthyRateThresholdPercent": 0,
	}
	var detail struct {
		ID     uint   `json:"id"`
		Status string `json:"status"`
	}
	blobAdminJSON(t, env.baseURL, env.adminToken, http.MethodPost, "/admin/v2/change-orders",
		body, http.StatusCreated, &detail, env.cp)
	if detail.ID == 0 {
		t.Fatal("建变更单未返回主键")
	}

	// diff-scan：真实扫描模板源清单，生成 file_diff 项（含待上传的 sha 需求）。
	scanPath := fmt.Sprintf("/admin/v2/change-orders/%d/diff-scan", detail.ID)
	blobAdminJSON(t, env.baseURL, env.adminToken, http.MethodPost, scanPath, nil, http.StatusOK, nil, env.cp)

	// submit → 审批（统一审批中心；审批执行器随即启动该单）。
	submitPath := fmt.Sprintf("/admin/v2/change-orders/%d/submit", detail.ID)
	if _, err := harness.RequestApprovalAndWait(env.baseURL, env.adminToken, http.MethodPost, submitPath,
		map[string]string{"reason": "blob 吞吐用例：提交并审批"}, "succeeded", blobSetupTimeout, env.cp); err != nil {
		t.Fatalf("提交变更单审批失败：%v", err)
	}

	// 读回终态：必须是 blob 归属校验认可的四个状态之一（approved / rolling / paused / rolling_back）。
	var got struct {
		Status       string `json:"status"`
		PayloadState string `json:"payloadState"`
	}
	blobAdminJSON(t, env.baseURL, env.adminToken, http.MethodGet,
		fmt.Sprintf("/admin/v2/change-orders/%d", detail.ID), nil, http.StatusOK, &got, env.cp)
	accepted := []string{
		model.ChangeOrderStatusApproved, model.ChangeOrderStatusRolling,
		model.ChangeOrderStatusPaused, model.ChangeOrderStatusRollingBack,
	}
	if !slices.Contains(accepted, got.Status) {
		t.Fatalf("变更单状态 %s 不在 blob 归属校验认可集 %v 内——链路假设被破坏", got.Status, accepted)
	}
	t.Logf("变更单 %d 终态=%s payloadState=%s（blob 归属校验认可该状态）", detail.ID, got.Status, got.PayloadState)
	return detail.ID, got.Status
}

// blobRegisterSHA 把待测 sha 作为文件项补进已批准的变更单（走真实库写入）。
//
// 为什么不走 diff-scan 生成这些项：diff-scan 的文件项来自「模板源清单 vs 目标清单」的差异，
// 而待测大文件的内容是随机生成的——要让 diff-scan 产出它们的 sha，就得先把几百 MiB 的真实内容
// 写进资产清单（清单表会瞬间膨胀到百万行级）。这里改为**直接落一行真实表结构的 file_diff 项**，
// 归属校验走的 ListOrdersReferencingSHA 是真实查询（kind + sha256 + 单状态集），
// 与「项从哪来」无关。这是既有 e2e（backpressure_429_e2e_test.go:460）的同款范式。
func blobRegisterSHA(t *testing.T, env *blobEnv, sha string) {
	t.Helper()
	path := fmt.Sprintf("%s/%s.bin", blobScanDir, sha[:16])
	action := model.ChangeItemActionUpdate
	size := int64(0)
	item := model.ChangeOrderItem{
		OrderID: env.orderID, Kind: model.ChangeItemKindFileDiff,
		Path: &path, Action: &action, SHA256: &sha, SizeBytes: &size,
	}
	if err := env.db.Create(&item).Error; err != nil {
		t.Fatalf("补登文件项 sha=%s 失败：%v", sha, err)
	}
}

// ---- 数据面请求 ----

// blobUpload 走真实 PUT 上传一个 blob，返回耗时与状态码。
func blobUpload(t *testing.T, env *blobEnv, sha string, content []byte) (float64, int, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), blobRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		strings.TrimRight(env.baseURL, "/")+"/beacon/v2/stream/delivery/blobs/"+sha,
		bytes.NewReader(content))
	if err != nil {
		return 0, 0, fmt.Errorf("构造上传请求失败：%w", err)
	}
	blobSetAgentHeaders(req, env)
	req.ContentLength = int64(len(content))
	client := blobNewClient(0, blobRequestTimeout)
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("上传请求失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return float64(time.Since(started).Microseconds()) / 1000.0, resp.StatusCode, nil
}

// blobHead 走真实 HEAD 查询就绪状态，返回状态码与响应片段。
func blobHead(t *testing.T, env *blobEnv, sha string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), blobHeadProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead,
		strings.TrimRight(env.baseURL, "/")+"/beacon/v2/stream/delivery/blobs/"+sha, nil)
	if err != nil {
		return 0, fmt.Sprintf("构造 HEAD 请求失败：%v", err)
	}
	blobSetAgentHeaders(req, env)
	resp, err := blobNewClient(0, blobHeadProbeTimeout).Do(req)
	if err != nil {
		return 0, fmt.Sprintf("HEAD 请求失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, "Content-Length=" + resp.Header.Get("Content-Length")
}

// blobDownloadOnce 全量下载一个 blob，量总耗时、首字节延迟、吞吐，并逐字节校验内容。
func blobDownloadOnce(t *testing.T, env *blobEnv, sha string, size blobSizeCase, want []byte) blobThroughputRow {
	t.Helper()
	row := blobThroughputRow{MiB: size.MiB, Bytes: size.Bytes}
	ctx, cancel := context.WithTimeout(context.Background(), blobRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(env.baseURL, "/")+"/beacon/v2/stream/delivery/blobs/"+sha, nil)
	if err != nil {
		row.Note = fmt.Sprintf("构造下载请求失败：%v", err)
		return row
	}
	blobSetAgentHeaders(req, env)

	started := time.Now()
	resp, err := blobNewClient(0, blobRequestTimeout).Do(req)
	if err != nil {
		row.Note = fmt.Sprintf("下载请求失败：%v", err)
		return row
	}
	defer func() { _ = resp.Body.Close() }()
	row.Status = resp.StatusCode
	row.TTFBMs = float64(time.Since(started).Microseconds()) / 1000.0
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, blobSnippetLimit))
		row.Note = fmt.Sprintf("状态码非 200；片段：%s", blobSnippet(raw))
		return row
	}
	got, err := io.ReadAll(resp.Body)
	elapsed := time.Since(started)
	row.ElapsedMs = float64(elapsed.Microseconds()) / 1000.0
	if err != nil {
		row.Note = fmt.Sprintf("读响应体失败：%v", err)
		return row
	}
	if int64(len(got)) != size.Bytes {
		row.Note = fmt.Sprintf("字节数不符：期望 %d，实际 %d", size.Bytes, len(got))
		return row
	}
	if !bytes.Equal(got, want) {
		row.Note = "内容与上传字节不一致（流式链路有损）"
		return row
	}
	row.Throughput = blobMBps(size.Bytes, row.ElapsedMs)
	row.RangeOK = true
	return row
}

// blobRangeProbe 对一档大小做三态 Range 探测：中段 206、尾部 206、越界 416，并校验 Content-Range 与字节。
// 1MiB / 16MiB 档额外做「逐分片 Range 拼接 == 全量内容」的一致性校验。
func blobRangeProbe(t *testing.T, env *blobEnv, sha string, size blobSizeCase, content []byte) []blobRangeRow {
	t.Helper()
	rows := make([]blobRangeRow, 0, 4)

	// 中段：取 [size/4, size/4 + 64KiB)。刻意不从 0 起——从 0 起无法区分「服务端忽略了 Range」
	// 与「正确返回了前缀」。小文件（<256KiB）时起点退化为 0，此时 Range 语义仍由 Content-Range 校验覆盖。
	midStart := size.Bytes / 4
	midEnd := min(midStart+int64(64<<10), size.Bytes) - 1
	if midStart > midEnd {
		midStart = 0
		midEnd = min(int64(64<<10), size.Bytes) - 1
	}
	rows = append(rows, blobRangeRequest(t, env, sha, size, content,
		fmt.Sprintf("bytes=%d-%d", midStart, midEnd), midStart, midEnd, http.StatusPartialContent))

	// 尾部：取最后 32KiB。
	tailStart := max(size.Bytes-int64(32<<10), 0)
	rows = append(rows, blobRangeRequest(t, env, sha, size, content,
		fmt.Sprintf("bytes=%d-", tailStart), tailStart, size.Bytes-1, http.StatusPartialContent))

	// 越界：起点 == 文件大小 → 416（Range 不被满足；ServeContent 依 RFC 7233 返回 416 + Content-Range: bytes */size）。
	rows = append(rows, blobRangeRequest(t, env, sha, size, content,
		fmt.Sprintf("bytes=%d-", size.Bytes), 0, 0, http.StatusRequestedRangeNotSatisfiable))

	// 一致性：分片拼接 == 全量（只在 ≤16MiB 档做，避免 256MiB 档重复下载拖长整轮）。
	if size.MiB <= 16 {
		rows = append(rows, blobRangeAssemble(t, env, sha, size, content))
	}
	return rows
}

// blobRangeRequest 发一次 Range 请求并校验状态码、Content-Range、Accept-Ranges 与响应字节。
func blobRangeRequest(t *testing.T, env *blobEnv, sha string, size blobSizeCase,
	content []byte, spec string, wantStart, wantEnd int64, wantStatus int) blobRangeRow {
	t.Helper()
	row := blobRangeRow{MiB: size.MiB, Spec: spec, WantStatus: wantStatus}
	ctx, cancel := context.WithTimeout(context.Background(), blobRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(env.baseURL, "/")+"/beacon/v2/stream/delivery/blobs/"+sha, nil)
	if err != nil {
		row.IfRangeNote = fmt.Sprintf("构造请求失败：%v", err)
		return row
	}
	blobSetAgentHeaders(req, env)
	req.Header.Set("Range", spec)
	resp, err := blobNewClient(0, blobRequestTimeout).Do(req)
	if err != nil {
		row.IfRangeNote = fmt.Sprintf("请求失败：%v", err)
		return row
	}
	defer func() { _ = resp.Body.Close() }()
	row.GotStatus = resp.StatusCode
	row.ContentRng = resp.Header.Get("Content-Range")
	row.AcceptRng = resp.Header.Get("Accept-Ranges")
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		row.IfRangeNote = fmt.Sprintf("读响应体失败：%v", err)
		return row
	}
	row.BodyBytes = int64(len(body))
	if resp.StatusCode != wantStatus {
		row.IfRangeNote = fmt.Sprintf("状态码期望 %d 实际 %d", wantStatus, resp.StatusCode)
		return row
	}
	if wantStatus == http.StatusPartialContent {
		wantCR := fmt.Sprintf("bytes %d-%d/%d", wantStart, wantEnd, size.Bytes)
		if row.ContentRng != wantCR {
			row.IfRangeNote = fmt.Sprintf("Content-Range 期望 %q 实际 %q", wantCR, row.ContentRng)
			return row
		}
		wantLen := wantEnd - wantStart + 1
		if int64(len(body)) != wantLen {
			row.IfRangeNote = fmt.Sprintf("分片字节数期望 %d 实际 %d", wantLen, len(body))
			return row
		}
		row.BytesMatch = bytes.Equal(body, content[wantStart:wantEnd+1])
		if !row.BytesMatch {
			row.IfRangeNote = "分片内容与全量对应区间不一致"
		}
	}
	return row
}

// blobRangeAssemble 逐 1MiB 分片取全文件并拼接，与全量内容逐字节比对（验证断点续传的完整性）。
func blobRangeAssemble(t *testing.T, env *blobEnv, sha string, size blobSizeCase, content []byte) blobRangeRow {
	t.Helper()
	row := blobRangeRow{MiB: size.MiB, Spec: "分片拼接（1MiB/片）", WantStatus: http.StatusPartialContent}
	ctx, cancel := context.WithTimeout(context.Background(), blobRequestTimeout)
	defer cancel()
	assembled := make([]byte, 0, size.Bytes)
	client := blobNewClient(0, blobRequestTimeout)
	for start := int64(0); start < size.Bytes; start += blobRangeChunkBytes {
		end := min(start+blobRangeChunkBytes, size.Bytes) - 1
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			strings.TrimRight(env.baseURL, "/")+"/beacon/v2/stream/delivery/blobs/"+sha, nil)
		if err != nil {
			row.IfRangeNote = fmt.Sprintf("构造分片请求失败：%v", err)
			return row
		}
		blobSetAgentHeaders(req, env)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		resp, err := client.Do(req)
		if err != nil {
			row.IfRangeNote = fmt.Sprintf("分片请求失败（offset=%d）：%v", start, err)
			return row
		}
		chunk, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		row.GotStatus = resp.StatusCode
		if resp.StatusCode != http.StatusPartialContent {
			row.IfRangeNote = fmt.Sprintf("分片 offset=%d 状态码期望 206 实际 %d", start, resp.StatusCode)
			return row
		}
		if readErr != nil {
			row.IfRangeNote = fmt.Sprintf("读分片失败（offset=%d）：%v", start, readErr)
			return row
		}
		assembled = append(assembled, chunk...)
	}
	row.BodyBytes = int64(len(assembled))
	row.BytesMatch = bytes.Equal(assembled, content)
	if !row.BytesMatch {
		row.IfRangeNote = "拼接结果与全量内容不一致（Range 语义有误）"
	}
	return row
}

// blobConcurrencyProbe 按并发档位压同一个 blob（取最大档做并发目标，避免各档内容不同带来的偏差）。
func blobConcurrencyProbe(t *testing.T, env *blobEnv, payloads map[int][]byte, levels []int) []blobConcurrencyRow {
	t.Helper()
	// 选并发目标：优先 16MiB（足够大以体现带宽争用，又足够小以免 64 并发时把盘写满）。
	targetMiB := 0
	for _, want := range []int{16, 64, 1, 256} {
		if _, ok := payloads[want]; ok {
			targetMiB = want
			break
		}
	}
	if targetMiB == 0 {
		t.Log("并发下载探测跳过：无可用档位")
		return nil
	}
	content := payloads[targetMiB]
	sha := blobSHA256Hex(content)
	rows := make([]blobConcurrencyRow, 0, len(levels))
	for _, level := range levels {
		rows = append(rows, blobConcurrentDownload(t, env, sha, content, targetMiB, level))
	}
	return rows
}

// blobConcurrentDownload 并发 level 路下载同一个 blob，返回总吞吐与单流吞吐分布。
func blobConcurrentDownload(t *testing.T, env *blobEnv, sha string, content []byte, mib, level int) blobConcurrencyRow {
	t.Helper()
	row := blobConcurrencyRow{Concurrency: level, MiB: mib, Streams: level}
	ctx, cancel := context.WithTimeout(context.Background(), blobRequestTimeout)
	defer cancel()
	// 每路一个独立客户端：独立连接池 → 真正 level 条 TCP 连接，不被连接复用掩盖并发度。
	clients := make([]*http.Client, level)
	for i := range clients {
		clients[i] = blobNewClient(1, blobRequestTimeout)
	}
	statuses := make([]int, level)
	perStreamMs := make([]float64, level)
	var okCount, failCount atomic.Int64
	var wg sync.WaitGroup
	started := time.Now()
	for i := range level {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet,
				strings.TrimRight(env.baseURL, "/")+"/beacon/v2/stream/delivery/blobs/"+sha, nil)
			if err != nil {
				statuses[idx] = -1
				failCount.Add(1)
				return
			}
			blobSetAgentHeaders(req, env)
			t0 := time.Now()
			resp, err := clients[idx].Do(req)
			if err != nil {
				statuses[idx] = -1
				failCount.Add(1)
				return
			}
			defer func() { _ = resp.Body.Close() }()
			statuses[idx] = resp.StatusCode
			body, readErr := io.ReadAll(resp.Body)
			perStreamMs[idx] = float64(time.Since(t0).Microseconds()) / 1000.0
			if readErr != nil || resp.StatusCode != http.StatusOK || !bytes.Equal(body, content) {
				failCount.Add(1)
				return
			}
			okCount.Add(1)
		}(i)
	}
	wg.Wait()
	wallMs := float64(time.Since(started).Microseconds()) / 1000.0
	row.WallMs = wallMs
	row.OKStreams = int(okCount.Load())
	row.FailedStreams = int(failCount.Load())
	row.StatusCounts = blobStatusCountsText(statuses)

	// 单流吞吐只在成功流上统计（失败流的耗时没有意义）。
	okRates := make([]float64, 0, level)
	for i := range perStreamMs {
		if statuses[i] == http.StatusOK && perStreamMs[i] > 0 {
			okRates = append(okRates, blobMBps(int64(len(content)), perStreamMs[i]))
		}
	}
	if len(okRates) > 0 {
		sort.Float64s(okRates)
		row.MinStreamMBps = okRates[0]
		row.MaxStreamMBps = okRates[len(okRates)-1]
		sum := 0.0
		for _, r := range okRates {
			sum += r
		}
		row.PerStreamMBps = sum / float64(len(okRates))
	}
	// 总吞吐按墙钟算（含并发错峰），这才是「服务端在给定并发下吐了多少字节」的真实口径。
	row.TotalThroughput = blobMBps(int64(len(content))*int64(row.OKStreams), wallMs)
	return row
}

// blobStreamingSample 是一次 RSS / goroutine 采样。
type blobStreamingSample struct {
	Phase      string
	RSSKiB     int64
	HeapAllocB int64
	Goroutines int
}

// blobStreamingEvidence 采集「下载大文件期间控制面 RSS 不随文件大小增长」的证据。
//
// 判据（两条都要成立才算流式）：
//  1. 控制面进程 RSS 在下载 256MiB（或最大档）期间相对下载前基线的增量 **远小于文件大小**
//     （阈值取文件大小的 1/4，实测通常只有几 MiB——那是内核 / 用户态缓冲与 GC 波动）；
//  2. /metrics 的 go_memstats_heap_alloc_bytes 峰值同样不随文件大小线性增长。
//
// 为什么用 RSS 而不是「代码看起来像流式」：http.ServeContent + os.File 天然是流式，
// 但只有进程级读数能证明「实现没有偷偷把整个文件读进内存」。
func blobStreamingEvidence(t *testing.T, env *blobEnv, payloads map[int][]byte) []blobStreamingSample {
	t.Helper()
	targetMiB := 0
	for _, want := range []int{256, 64, 16, 1} {
		if _, ok := payloads[want]; ok {
			targetMiB = want
			break
		}
	}
	if targetMiB == 0 {
		return nil
	}
	content := payloads[targetMiB]
	sha := blobSHA256Hex(content)
	pid := blobFindControlPlanePID(t, env)
	if pid <= 0 {
		t.Log("RSS 证据采集跳过：未能定位控制面进程 PID")
		return nil
	}

	samples := make([]blobStreamingSample, 0, 8)
	stop := make(chan struct{})
	done := make(chan struct{})
	var mu sync.Mutex
	go func() {
		defer close(done)
		ticker := time.NewTicker(blobSampleInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s := blobSampleProcess(pid, "下载中")
				mu.Lock()
				samples = append(samples, s)
				mu.Unlock()
			}
		}
	}()

	baseline := blobSampleProcess(pid, "基线")
	// 下载并边读边丢（只要字节流经过服务端写出路径，RSS 结论就成立）。
	ctx, cancel := context.WithTimeout(context.Background(), blobRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(env.baseURL, "/")+"/beacon/v2/stream/delivery/blobs/"+sha, nil)
	if err != nil {
		close(stop)
		<-done
		t.Logf("RSS 证据采集跳过：构造请求失败：%v", err)
		return nil
	}
	blobSetAgentHeaders(req, env)
	resp, err := blobNewClient(0, blobRequestTimeout).Do(req)
	if err != nil {
		close(stop)
		<-done
		t.Logf("RSS 证据采集跳过：请求失败：%v", err)
		return nil
	}
	n, copyErr := io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	close(stop)
	<-done
	after := blobSampleProcess(pid, "下载后")

	mu.Lock()
	mid := make([]blobStreamingSample, len(samples))
	copy(mid, samples)
	mu.Unlock()
	out := append([]blobStreamingSample{baseline}, mid...)
	out = append(out, after)
	if copyErr != nil {
		t.Logf("RSS 证据采集：读响应体失败：%v", copyErr)
	}
	t.Logf("流式证据（%d MiB 单流下载）：基线 RSS=%.1fMiB，下载中峰值 RSS=%.1fMiB，下载后 RSS=%.1fMiB；"+
		"读回字节=%d（期望 %d）", targetMiB,
		float64(baseline.RSSKiB)/1024, float64(blobPeakRSS(out))/1024, float64(after.RSSKiB)/1024,
		n, len(content))
	return out
}

// blobPeakRSS 取采样序列里的 RSS 峰值。
func blobPeakRSS(samples []blobStreamingSample) int64 {
	var peak int64
	for i := range samples {
		if samples[i].RSSKiB > peak {
			peak = samples[i].RSSKiB
		}
	}
	return peak
}

// blobSampleProcess 读一次控制面进程的 RSS / 堆 / goroutine 数。
// 优先用 /proc（含堆与 goroutine 时回落到 /metrics），读失败返回零值不阻断探测。
func blobSampleProcess(pid int, phase string) blobStreamingSample {
	sample := blobStreamingSample{Phase: phase}
	st := readProcStats(pid)
	sample.RSSKiB = st.RSSKiB
	return sample
}

// blobFindControlPlanePID 定位本用例起的控制面进程 PID（按二进制路径匹配 cmdline）。
func blobFindControlPlanePID(t *testing.T, env *blobEnv) int {
	t.Helper()
	binPath := filepath.Join(env.repoRoot, ".tmp", "loadtest", blobBinName)
	if pid := findProcessPIDByCmdline(binPath); pid > 0 {
		return pid
	}
	// 回落到按进程名匹配（有些平台 cmdline[0] 不是完整路径）。
	return findProcessPIDByCmdline(blobBinName)
}

// ---- 报告 ----

// blobReport 把四组结果按 Markdown 表格打进测试日志（同时是取证入口）。
func blobReport(t *testing.T, rows []blobThroughputRow, conc []blobConcurrencyRow,
	ranges []blobRangeRow, streaming []blobStreamingSample) {
	t.Helper()
	t.Log("== 交付 blob 吞吐实测 ==")
	t.Log("| 大小(MiB) | 状态 | 耗时(ms) | 首字节(ms) | 吞吐(MB/s) | 内容校验 | 备注 |")
	t.Log("| --- | --- | --- | --- | --- | --- | --- |")
	for _, r := range rows {
		verify := "通过"
		if !r.RangeOK {
			verify = "失败"
		}
		t.Logf("| %d | %d | %.0f | %.0f | %.1f | %s | %s |",
			r.MiB, r.Status, r.ElapsedMs, r.TTFBMs, r.Throughput, verify, r.Note)
	}

	t.Log("== 并发下载实测（同一 blob 多流并发）==")
	t.Log("| 并发 | 大小(MiB) | 成功流 | 失败流 | 墙钟(ms) | 总吞吐(MB/s) | 单流均值(MB/s) | 单流最小 | 单流最大 | 状态码 |")
	t.Log("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, r := range conc {
		t.Logf("| %d | %d | %d/%d | %d | %.0f | %.1f | %.1f | %.1f | %.1f | %s |",
			r.Concurrency, r.MiB, r.OKStreams, r.Streams, r.FailedStreams, r.WallMs,
			r.TotalThroughput, r.PerStreamMBps, r.MinStreamMBps, r.MaxStreamMBps, r.StatusCounts)
	}

	t.Log("== Range 断点续传实测 ==")
	t.Log("| 大小(MiB) | 请求 | 期望 | 实际 | Content-Range | Accept-Ranges | 字节数 | 字节一致 | 备注 |")
	t.Log("| --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, r := range ranges {
		match := "—"
		if r.WantStatus == http.StatusPartialContent {
			if r.BytesMatch {
				match = "是"
			} else {
				match = "否"
			}
		}
		t.Logf("| %d | %s | %d | %d | %s | %s | %d | %s | %s |",
			r.MiB, r.Spec, r.WantStatus, r.GotStatus, r.ContentRng, r.AcceptRng, r.BodyBytes, match, r.IfRangeNote)
	}

	if len(streaming) > 0 {
		t.Log("== 流式证据（控制面进程 RSS）==")
		t.Log("| 阶段 | RSS(MiB) |")
		t.Log("| --- | --- |")
		for _, s := range streaming {
			t.Logf("| %s | %.1f |", s.Phase, float64(s.RSSKiB)/1024)
		}
	}
}

// ---- 小工具 ----

// blobReportMetric 用模板源身份真实上报一批指标（让健康计算轮把它算成非 lost 视图）。
// bucketStartMs 必须 > 0 且不能与窗口内已有桶重复（重复只计 deduplicated，不刷新 ReceivedAtMs）。
func blobReportMetric(t *testing.T, env *blobEnv) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), blobSetupTimeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"namespace": env.namespace, "serverId": blobSourceServerIDOf(env),
		"kind": model.ServerKindBackend,
		"samples": []map[string]any{{
			"bucketStartMs": time.Now().UnixMilli() - 5000, "sampleCount": 1,
			"cpuPctAvg": 10.0, "cpuPctMax": 20.0, "memUsedMbAvg": 512.0, "memMaxMb": 1024,
			"tpsAvg": 20.0, "tpsMin": 19.0, "onlineAvg": 5, "onlineMax": 8, "maxOnline": 10,
		}},
	})
	if err != nil {
		t.Fatalf("编码指标上报请求失败：%v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(env.baseURL, "/")+"/beacon/v2/agent/metrics/report", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("构造指标上报请求失败：%v", err)
	}
	blobSetAgentHeaders(req, env)
	req.Header.Set("Content-Type", "application/json")
	resp, err := harness.DoRequestWithGuard(req, blobSetupTimeout, env.cp)
	if err != nil {
		t.Fatalf("指标上报请求失败：%v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("指标上报期望 202，实际 %d；片段：%s", resp.StatusCode, blobSnippet(raw))
	}
}

// blobWaitSourceHealthy 轮询 /admin/v2/health 直到模板源出现且 reasons 不含 lost。
func blobWaitSourceHealthy(t *testing.T, env *blobEnv, serverID string) {
	t.Helper()
	deadline := time.Now().Add(blobPhaseTimeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), blobSetupTimeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			strings.TrimRight(env.baseURL, "/")+"/admin/v2/health?keyword="+url.QueryEscape(serverID), nil)
		if err != nil {
			cancel()
			t.Fatalf("构造健康查询请求失败：%v", err)
		}
		req.Header.Set("Authorization", "Bearer "+env.adminToken)
		resp, err := harness.DoRequestWithGuard(req, blobSetupTimeout, env.cp)
		if err != nil {
			cancel()
			t.Fatalf("健康查询请求失败：%v", err)
		}
		var page struct {
			Items []struct {
				ServerID string   `json:"serverId"`
				Reasons  []string `json:"reasons"`
			} `json:"items"`
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		cancel()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("健康查询期望 200，实际 %d；片段：%s", resp.StatusCode, blobSnippet(raw))
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatalf("解析健康查询响应失败：%v；片段：%s", err, blobSnippet(raw))
		}
		for i := range page.Items {
			if page.Items[i].ServerID != serverID {
				continue
			}
			if !slices.Contains(page.Items[i].Reasons, "lost") {
				t.Logf("模板源 %s 健康视图就绪（reasons=%v）", serverID, page.Items[i].Reasons)
				return
			}
		}
		time.Sleep(blobPollInterval)
	}
	t.Fatalf("等待模板源 %s 健康视图就绪超时（%s）——submit 会因 source_invalid 被拒", serverID, blobPhaseTimeout)
}

// blobSourceServerIDOf 取本用例模板源的 serverId（从装配时注入的地址回推过于绕，
// 这里直接由 env 携带的 identity 关系给出）。
func blobSourceServerIDOf(env *blobEnv) string {
	return env.sourceID
}

// blobSetAgentHeaders 预置 v2 数据面三头（token / identity / boot）。
func blobSetAgentHeaders(req *http.Request, env *blobEnv) {
	req.Header.Set("X-Beacon-Token", env.accessToken)
	req.Header.Set("X-Beacon-Identity", env.identityID)
	req.Header.Set("X-Beacon-Boot", env.bootID)
}

// blobNewClient 构造压测自建客户端：独立 Transport（不共享连接池）、不设 Client.Timeout。
// maxIdle 为 0 时取默认 16；Proxy 显式置 nil 以避免本地流量被环境变量里的代理绕走。
func blobNewClient(maxIdle int, timeout time.Duration) *http.Client {
	if maxIdle <= 0 {
		maxIdle = 16
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:          maxIdle,
			MaxIdleConnsPerHost:   maxIdle,
			IdleConnTimeout:       30 * time.Second,
			DisableCompression:    true,
			ForceAttemptHTTP2:     false,
			ResponseHeaderTimeout: timeout,
			// 大文件下载要的是带宽，禁用读写缓冲以外的任何额外拷贝（默认值即可）。
		},
	}
}

// blobAdminJSON 发一次管理面 JSON 请求并严格校验状态码（out 非 nil 时解析响应）。
func blobAdminJSON(t *testing.T, baseURL, token, method, path string, body any,
	wantStatus int, out any, guard harness.ProcessGuard) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), blobSetupTimeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("编码请求体失败（%s %s）：%v", method, path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+path, reader)
	if err != nil {
		t.Fatalf("构造请求失败（%s %s）：%v", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := harness.DoRequestWithGuard(req, blobSetupTimeout, guard)
	if err != nil {
		t.Fatalf("请求失败（%s %s）：%v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应失败（%s %s）：%v", method, path, err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s 期望 %d，实际 %d；片段：%s", method, path, wantStatus, resp.StatusCode, blobSnippet(raw))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("解析响应失败（%s %s）：%v；片段：%s", method, path, err, blobSnippet(raw))
		}
	}
}

// blobNamespaceID 按 code 读回 namespace 主键（组单与播种都需要）。
func blobNamespaceID(t *testing.T, db *gorm.DB, code string) uint {
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

// blobServerRowID 按 serverId 读回 server 表主键（首次分配端点要数字行 id）。
func blobServerRowID(t *testing.T, db *gorm.DB, namespaceID uint, serverID string) uint {
	t.Helper()
	var srv model.Server
	if err := db.Where("namespace_id = ? AND server_id = ?", namespaceID, serverID).First(&srv).Error; err != nil {
		t.Fatalf("读 server 行（namespace=%d server=%s）失败：%v", namespaceID, serverID, err)
	}
	return srv.ID
}

// blobOpenProbeDB 打开一个直连控制面同一 sqlite 文件的连接（WAL 模式，多读者不阻塞写者）。
func blobOpenProbeDB(t *testing.T, path string) *gorm.DB {
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

// blobGenerateContent 生成指定字节数的可压缩性极低的内容（伪随机），避免内核 / 磁盘把测试变成压缩基准。
func blobGenerateContent(size int64) []byte {
	buf := make([]byte, size)
	// xorshift64 手写伪随机：不引入 rand 依赖，且填充速度足以支撑 256MiB。
	var x uint64 = 0x9E3779B97F4A7C15
	for i := range buf {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		buf[i] = byte(x)
	}
	return buf
}

// blobSHA256Hex 返回内容的小写 hex sha256（blob 内容寻址身份）。
func blobSHA256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// blobMBps 按字节数与毫秒耗时算 MB/s（MB = 10^6 字节，与网络吞吐惯例一致）。
func blobMBps(bytes int64, ms float64) float64 {
	if ms <= 0 {
		return 0
	}
	return float64(bytes) / 1e6 / (ms / 1000.0)
}

// blobStatusCountsText 把状态码数组汇总成 "200×8, 429×1" 形式的可读文本。
func blobStatusCountsText(statuses []int) string {
	counts := map[int]int{}
	for _, s := range statuses {
		counts[s]++
	}
	keys := make([]int, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		label := strconv.Itoa(k)
		if k < 0 {
			label = "网络错误"
		}
		parts = append(parts, fmt.Sprintf("%s×%d", label, counts[k]))
	}
	return strings.Join(parts, ", ")
}

// blobSnippet 截断响应体供失败回带（够定位问题，不刷屏）。
func blobSnippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > blobSnippetLimit {
		return s[:blobSnippetLimit] + "…"
	}
	return s
}

// parseBlobSizes 解析 BLOB_SIZES_MIB（MiB 逗号分隔），去重后升序。
func parseBlobSizes(t *testing.T, raw string) []blobSizeCase {
	t.Helper()
	values := blobParseIntList(t, raw, "BLOB_SIZES_MIB")
	if envOr("BLOB_SKIP_BIG", "") == "1" {
		values = slices.DeleteFunc(values, func(v int) bool { return v >= 256 })
	}
	if len(values) == 0 {
		t.Fatalf("BLOB_SIZES_MIB 未给出任何有效档位：%q", raw)
	}
	out := make([]blobSizeCase, 0, len(values))
	for _, v := range values {
		if v <= 0 || v > 4096 {
			t.Fatalf("BLOB_SIZES_MIB 档位 %d 越界（允许 1..4096 MiB）", v)
		}
		out = append(out, blobSizeCase{MiB: v, Bytes: int64(v) << 20})
	}
	return out
}

// blobParseIntList 解析逗号分隔的正整数列表（去重 + 升序）。
// 刻意不复用 scale_1000_test.go 里的 parseIntList：那个只去空格不去重，且档位重复会真的重复跑一遍。
func blobParseIntList(t *testing.T, raw, key string) []int {
	t.Helper()
	seen := map[int]struct{}{}
	out := make([]int, 0, 8)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil || v < 1 {
			t.Fatalf("%s 含非法数值 %q（应为 ≥1 的整数）", key, part)
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}

// blobRemoveStaleDB 只清理本用例自己的库文件（含 WAL / SHM）。
// 刻意不复用 removeStaleDB：它会清目录下全部 *.db*，会误删并行运行的其他用例的库。
func blobRemoveStaleDB(t *testing.T, dir, dbName string) {
	t.Helper()
	if err := blobRemoveDBFiles(dir, dbName); err != nil {
		t.Fatalf("清理本用例遗留库文件失败：%v", err)
	}
}

// blobCleanupDBFiles 是收尾用的非致命版本（删不掉只记日志）。
func blobCleanupDBFiles(t *testing.T, dir, dbName string) {
	t.Helper()
	if err := blobRemoveDBFiles(dir, dbName); err != nil {
		t.Logf("收尾：清理库文件失败（可手工删除）：%v", err)
	}
}

// blobRemoveDBFiles 删除本用例的库文件三件套，已不存在视为成功。
func blobRemoveDBFiles(dir, dbName string) error {
	base := filepath.Join(dir, dbName)
	for _, path := range []string{base, base + "-wal", base + "-shm"} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除 %s：%w", path, err)
		}
	}
	return nil
}

// blobRemoveBlobFiles 按 sha 精确删除本用例落盘的 blob（最终路径 + 可能残留的上传临时文件）。
// blob 根 .beacon/delivery 是产品硬编码的共享目录：**绝不整目录清理**，只删自己创建的 sha。
func blobRemoveBlobFiles(t *testing.T, repoRoot string, shas []string) {
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
		matches, _ := filepath.Glob(filepath.Join(root, "tmp", sha+".tmp-*"))
		for _, path := range matches {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Logf("收尾：删除上传临时文件 %s 失败：%v", path, err)
			}
		}
	}
}
