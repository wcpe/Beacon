package service

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/metricwindow"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/roster"
)

// 本文件是「429 背压边界」的服务层证据集（单元级）：四类 429 的触发语义与额度归还在真实装配
// （真实 AsyncDailyWriter / flowLimiter + 真实仓储接口）下逐条可复现，不依赖 MySQL、不依赖外部压测。
//
// 与 e2e 的分工（两者不可互相替代，报告须分开陈述）：
//   - 本文件证明「链路正确」：队列满 / 并发满 → 领域 429 错误 → 上层渲染 429，且额度不泄漏、恢复后可用。
//   - e2e（apps/server/test/loadtest/backpressure_429_test.go）证明「生产链路上真实触发过」：
//     真实 HTTP 请求经真实鉴权 / 归属校验打到真实进程，响应体里带出业务码。

// blockingReader 是「占住额度」的读端：首次 Read 发出 started 信号后一直阻塞，直到 release 关闭。
// 用于让一次上传确定性地停在 Store 的临界区内（额度已被占用），从而让第二个请求必然撞上限流。
type blockingReader struct {
	payload []byte
	offset  int
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	if r.offset >= len(r.payload) {
		return 0, io.EOF
	}
	n := copy(p, r.payload[r.offset:])
	r.offset += n
	return n, nil
}

// mustSha 返回内容摘要（复用既有测试助手的语义，单独命名避免与其它测试文件冲突）。
func mustSha(content []byte) string { return shaOf(content) }

// TestBackpressureDeliveryUploadBusyAndRecover 上传并发上限设为 1 时：
// 第一个上传占住额度 → 第二个必然 429（delivery_upload_busy）→ 第一个完成后额度归还、第三个恢复成功。
// 这是「429 是瞬时背压、不是永久损坏」的直接证明，且顺带证明失败路径不泄漏额度。
func TestBackpressureDeliveryUploadBusyAndRecover(t *testing.T) {
	settings := looseBlobSettings()
	settings.upload = 1 // 上限 1：任何第二个在途上传必然被拒（flowLimiter 不排队）
	svc, _ := newBlobTestSvc(t, settings)

	firstContent := []byte("first upload occupies the single slot")
	secondContent := []byte("second upload must be rejected")
	thirdContent := []byte("third upload must succeed after release")
	reader := &blockingReader{
		payload: firstContent, started: make(chan struct{}), release: make(chan struct{}),
	}

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- svc.Store(mustSha(firstContent), int64(len(firstContent)), reader)
	}()
	// 同步点：第一个上传已经进入临界区（额度已占），此时再发第二个。
	select {
	case <-reader.started:
	case <-time.After(5 * time.Second):
		t.Fatal("第一个上传迟迟未进入读体阶段")
	}
	if got := svc.uploads.inflight; got != 1 {
		t.Fatalf("占住额度后应在途数为 1，实际 %d", got)
	}

	err := svc.Store(mustSha(secondContent), int64(len(secondContent)), bytes.NewReader(secondContent))
	if !errors.Is(err, apperr.ErrDeliveryUploadBusy) {
		t.Fatalf("并发满时应返回 delivery_upload_busy（HTTP 429），实际 %v", err)
	}
	// 被拒的请求不应在磁盘 / 元数据上留下任何痕迹（预检在占额之后、落盘之前）。
	if _, headErr := svc.Head(mustSha(secondContent)); !errors.Is(headErr, apperr.ErrDeliveryBlobNotFound) {
		t.Fatalf("被 429 拒绝的上传不应落任何 blob，实际 %v", headErr)
	}
	if got := svc.uploads.inflight; got != 1 {
		t.Fatalf("被拒请求不应改变在途数，实际 %d", got)
	}

	// 放行第一个：应成功落盘并归还额度。
	close(reader.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("第一个上传应成功，实际 %v", err)
	}
	if got := svc.uploads.inflight; got != 0 {
		t.Fatalf("第一个上传完成后额度应归还为 0，实际 %d", got)
	}

	// 恢复证据：额度归还后再传一个新 sha 应成功。
	if err := svc.Store(mustSha(thirdContent), int64(len(thirdContent)), bytes.NewReader(thirdContent)); err != nil {
		t.Fatalf("额度归还后上传应恢复成功，实际 %v", err)
	}
	if _, err := svc.Head(mustSha(thirdContent)); err != nil {
		t.Fatalf("恢复后的上传应就绪，实际 %v", err)
	}
}

// TestBackpressureDeliveryUploadQuotaNotLeakedOnFailure 失败路径（哈希不符 422）也必须归还额度：
// 否则一次坏上传就会把上传面永久打成 429（额度泄漏是背压类缺陷最典型的慢性失效）。
func TestBackpressureDeliveryUploadQuotaNotLeakedOnFailure(t *testing.T) {
	settings := looseBlobSettings()
	settings.upload = 1
	svc, _ := newBlobTestSvc(t, settings)

	content := []byte("content whose declared sha does not match")
	err := svc.Store(mustSha([]byte("some other content")), int64(len(content)), bytes.NewReader(content))
	if !errors.Is(err, apperr.ErrDeliveryBlobHashMismatch) {
		t.Fatalf("哈希不符应返回 blob_hash_mismatch，实际 %v", err)
	}
	if got := svc.uploads.inflight; got != 0 {
		t.Fatalf("失败上传必须归还额度，在途数应为 0，实际 %d", got)
	}
	// 额度未泄漏的正面证据：紧接着的第二次上传能正常进行。
	if err := svc.Store(mustSha(content), int64(len(content)), bytes.NewReader(content)); err != nil {
		t.Fatalf("失败后额度应已归还、下次上传应成功，实际 %v", err)
	}
}

// TestBackpressureDeliveryDownloadBusyAndRecover 下载并发上限设为 1 时：
// 第一个 Open 占住额度 → 第二个必然 429（delivery_download_busy）→ 归还后第三个恢复成功。
func TestBackpressureDeliveryDownloadBusyAndRecover(t *testing.T) {
	settings := looseBlobSettings()
	settings.download = 1
	svc, _ := newBlobTestSvc(t, settings)

	content := []byte("downloadable blob content")
	sha := mustSha(content)
	mustStore(t, svc, sha, content)

	file, size, release, err := svc.Open(sha)
	if err != nil {
		t.Fatalf("首次下载应成功，实际 %v", err)
	}
	if size != int64(len(content)) {
		t.Fatalf("下载大小应为 %d，实际 %d", len(content), size)
	}
	defer func() { _ = file.Close() }()

	if _, _, _, err := svc.Open(sha); !errors.Is(err, apperr.ErrDeliveryDownloadBusy) {
		t.Fatalf("下载并发满时应返回 delivery_download_busy（HTTP 429），实际 %v", err)
	}
	release()
	file2, _, release2, err := svc.Open(sha)
	if err != nil {
		t.Fatalf("额度归还后下载应恢复成功，实际 %v", err)
	}
	_ = file2.Close()
	release2()
	if got := svc.downloads.inflight; got != 0 {
		t.Fatalf("全部归还后在途数应为 0，实际 %d", got)
	}
}

// TestBackpressureQueueFullDrives429AtRealCapacity 用**生产默认队列容量**（4096，编译期常量）
// 真实填满队列，证明「第 4097 个批被拒」：这不是构造出的小队列，而是线上真正生效的那个容量。
// 不启动 worker（Start 未调用）故队列不会被消费——正是「消费者被拖慢/停住」时的等价形态。
func TestBackpressureQueueFullDrives429AtRealCapacity(t *testing.T) {
	writer := NewAsyncDailyWriter()
	if writer.queueCapacity != defaultDailyWriteQueueCapacity {
		t.Fatalf("本用例依赖生产默认队列容量 %d，实际 %d", defaultDailyWriteQueueCapacity, writer.queueCapacity)
	}
	RegisterFlusher(writer, RouteKindConnEvent, func(_ []model.ConnEvent) (int, error) { return 0, nil })

	for i := 0; i < defaultDailyWriteQueueCapacity; i++ {
		if !EnqueueRows(writer, RouteKindConnEvent, []model.ConnEvent{{ConnID: uuid.NewString()}}) {
			t.Fatalf("第 %d 个批不应被拒（容量 %d）", i+1, defaultDailyWriteQueueCapacity)
		}
	}
	if EnqueueRows(writer, RouteKindConnEvent, []model.ConnEvent{{ConnID: uuid.NewString()}}) {
		t.Fatalf("队列已达容量 %d，第 %d 个批必须被拒", defaultDailyWriteQueueCapacity, defaultDailyWriteQueueCapacity+1)
	}
}

// TestBackpressureQueueFullToHTTP429Chain 走**真实装配**把队列满映射到 HTTP 429：
// 真实 AsyncDailyWriter（不启动 worker → 队列不被消费）+ 真实 Enqueuer 适配器 + 真实接收服务，
// 断言领域错误就是 429 业务码 metrics_ingest_busy / conn_ingest_busy。
func TestBackpressureQueueFullToHTTP429Chain(t *testing.T) {
	writer := NewAsyncDailyWriter()
	writer.queueCapacity = 1 // 缩短填满成本；容量本身已在上一用例用生产值验证
	RegisterFlusher(writer, RouteKindMetricSample, func([]model.MetricSampleV2) (int, error) { return 0, nil })
	RegisterFlusher(writer, RouteKindConnEvent, func([]model.ConnEvent) (int, error) { return 0, nil })

	// —— 指标上报：队列满 → metrics_ingest_busy（429）——
	now := time.Now().UTC()
	metricSvc := NewMetricIngestService(metricwindow.New(metricwindow.DefaultCapacity), MetricSampleEnqueuer{Writer: writer})
	metricSvc.now = func() time.Time { return now }
	if !EnqueueRows(writer, RouteKindMetricSample, []model.MetricSampleV2{{ServerID: "filler"}}) {
		t.Fatal("填满指标队列失败")
	}
	metricParams := MetricReportParams{
		Identity:    agentauth.Identity{NamespaceID: 1, Namespace: "prod", ServerID: "bp-s1", Kind: model.ServerKindBackend},
		AgentTimeMs: now.UnixMilli(),
		Samples:     []MetricReportSample{{BucketStartMs: now.UnixMilli() - 1000, SampleCount: 5}},
	}
	_, err := metricSvc.Ingest(metricParams)
	if !errors.Is(err, apperr.ErrMetricsIngestBusy) {
		t.Fatalf("指标队列满应返回 metrics_ingest_busy，实际 %v", err)
	}
	if ae := apperrStatusOf(t, err); ae != 429 {
		t.Fatalf("metrics_ingest_busy 的 HTTP 状态应为 429，实际 %d", ae)
	}

	// —— 连接上报：队列满 → conn_ingest_busy（429）——
	connSvc := NewConnIngestService(ConnEventEnqueuer{Writer: writer}, roster.NewStore(), &fakeReconciler{})
	if !EnqueueRows(writer, RouteKindConnEvent, []model.ConnEvent{{ConnID: uuid.NewString()}}) {
		t.Fatal("填满连接队列失败")
	}
	_, err = connSvc.Ingest(ConnBatchParams{
		Identity: proxyID(1, "bp-proxy"), BootID: "bp-boot",
		Events: []ConnEventInput{connInput(model.ConnEventKindOpen, uuid7(now.UnixMilli(), "c1"), "carol", "game-1")},
	})
	if !errors.Is(err, apperr.ErrConnIngestBusy) {
		t.Fatalf("连接队列满应返回 conn_ingest_busy，实际 %v", err)
	}
	if code := apperrStatusOf(t, err); code != 429 {
		t.Fatalf("conn_ingest_busy 的 HTTP 状态应为 429，实际 %d", code)
	}
}

// TestBackpressureQueueRecoversAfterDrain 队列被消费后恢复接收：证明 429 是瞬时背压而非永久拒绝。
func TestBackpressureQueueRecoversAfterDrain(t *testing.T) {
	writer := NewAsyncDailyWriter()
	writer.queueCapacity = 1
	RegisterFlusher(writer, RouteKindMetricSample, func([]model.MetricSampleV2) (int, error) { return 0, nil })

	now := time.Now().UTC()
	svc := NewMetricIngestService(metricwindow.New(metricwindow.DefaultCapacity), MetricSampleEnqueuer{Writer: writer})
	svc.now = func() time.Time { return now }
	if !EnqueueRows(writer, RouteKindMetricSample, []model.MetricSampleV2{{ServerID: "filler"}}) {
		t.Fatal("填满队列失败")
	}
	params := MetricReportParams{
		Identity:    agentauth.Identity{NamespaceID: 1, Namespace: "prod", ServerID: "bp-s2", Kind: model.ServerKindBackend},
		AgentTimeMs: now.UnixMilli(),
		Samples:     []MetricReportSample{{BucketStartMs: now.UnixMilli() - 1000, SampleCount: 5}},
	}
	if _, err := svc.Ingest(params); !errors.Is(err, apperr.ErrMetricsIngestBusy) {
		t.Fatalf("队列满应回 429，实际 %v", err)
	}
	// 排空一个批（等价于消费者取走一个批），队列腾出空位。
	if len(writer.routes[RouteKindMetricSample].queue) != 1 {
		t.Fatal("前置：队列应有 1 个批")
	}
	<-writer.routes[RouteKindMetricSample].queue
	res, err := svc.Ingest(params)
	if err != nil || res.Accepted != 1 || res.Deduplicated != 0 {
		t.Fatalf("队列腾空后应恢复接收且窗口未被此前 429 污染，实际 err=%v res=%+v", err, res)
	}
}

// apperrStatusOf 取出领域错误的 HTTP 状态码（非领域错误即失败）。
func apperrStatusOf(t *testing.T, err error) int {
	t.Helper()
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		t.Fatalf("期望领域错误（带 HTTP 状态与业务码），实际 %T: %v", err, err)
	}
	return ae.Status
}
