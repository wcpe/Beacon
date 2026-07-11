package service

import (
	"log/slog"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/metricwindow"
)

const (
	// 单批上报样本数上限（spec §4.2：agent 单批 ≤120）。
	maxSamplesPerReport = 120
	// 时钟偏移阈值（spec §4.2 待定 9）：agent 上报时钟与控制面偏移超此值整批拒绝，倒逼校时。
	defaultClockSkewLimit = 5 * time.Minute
	// RTT 不可用哨兵（与 agent 约定：取不到为 -1）。
	rttUnavailable = -1.0
)

// RouteKindMetricSample 是指标批在异步日表写入通道中的路由键（FR-144，见 §4.3）。
const RouteKindMetricSample = "metric_sample"

// metricEnqueuer 是接收服务对异步写入池的窄依赖：非阻塞投递一批聚合行，队列满返回 false。
// 由 MetricSampleEnqueuer 实现，抽成接口便于单测注入「队列满」替身验证 429 背压。
type metricEnqueuer interface {
	Enqueue(rows []model.MetricSampleV2) bool
}

// MetricSampleEnqueuer 把泛化异步日表写入通道绑定到 metric_sample 路由（装配用）。
type MetricSampleEnqueuer struct {
	// Writer 泛化异步日表写入通道（须已注册 RouteKindMetricSample 路由）。
	Writer *AsyncDailyWriter
}

// Enqueue 非阻塞投递一批指标聚合行；队列满返回 false（上层据此回 429 背压）。
func (e MetricSampleEnqueuer) Enqueue(rows []model.MetricSampleV2) bool {
	return EnqueueRows(e.Writer, RouteKindMetricSample, rows)
}

// MetricReportSample 是一条 5s 批聚合样本（agent 端已按 5s 桶聚合，控制面不再重聚合，见 §4.3）。
type MetricReportSample struct {
	BucketStartMs   int64
	SampleCount     int
	CPUPctAvg       float64
	CPUPctMax       float64
	MemUsedMbAvg    float64
	MemMaxMb        int
	TPSAvg          float64
	TPSMin          float64
	OnlineAvg       int
	OnlineMax       int
	MaxOnline       int
	ConnAvg         int
	ConnMax         int
	BackendUp       int
	BackendTotal    int
	BackendRttMsAvg float64
	ReportRttMs     int
}

// MetricReportParams 是一次指标上报的入参（handler 解码后交服务；身份为中间件注入的权威绑定）。
type MetricReportParams struct {
	Identity         agentauth.Identity   // 权威身份（token + identity 鉴权后注入，非请求体自报）
	BodyServerID     string               // 请求体自报 serverId（仅作一致性校验，防错配 agent 串报）
	BodyKind         string               // 请求体自报 kind（同上）
	AgentTimeMs      int64                // agent 本地时钟（毫秒），供时钟偏移校验
	DroppedSinceLast int                  // 上次成功上报以来 agent 环形缓冲被覆盖丢弃的样本数（可见性 WARN）
	Samples          []MetricReportSample // 5s 批聚合样本，单批 ≤120
}

// MetricReportResult 是一次上报的处理结果（对齐 spec §5.1 的 202 响应 accepted / deduplicated）。
type MetricReportResult struct {
	Accepted     int // 本次新接收（窗口新增桶）的批数
	Deduplicated int // 被判为重放（窗口内已存在，含批内重复）的批数
}

// MetricIngestService 是控制面指标接收端（FR-144，见 §4.2/§4.3）：
// 鉴权由中间件完成，本服务只做校验 → 更 60s 内存窗口 → 非阻塞入队 → 立即返回，请求 goroutine 绝不碰 DB。
type MetricIngestService struct {
	window    *metricwindow.Store
	enqueue   metricEnqueuer
	now       func() time.Time
	skewLimit time.Duration
}

// NewMetricIngestService 构造接收服务（window 为 60s 内存窗口，enqueue 为异步写入池）。
func NewMetricIngestService(window *metricwindow.Store, enqueue metricEnqueuer) *MetricIngestService {
	return &MetricIngestService{
		window:    window,
		enqueue:   enqueue,
		now:       func() time.Time { return time.Now().UTC() },
		skewLimit: defaultClockSkewLimit,
	}
}

// Ingest 处理一次指标上报：一致性校验 → 时钟偏移校验 → 分类新桶 vs 重放 → 入队 → 提交窗口。
//
// 顺序要点（防 429 丢数据）：先只读窗口分类、构造新行，入队成功后才提交窗口——
// 若入队满回 429，窗口未变，agent 重发时不会被误判重放而丢数据（同服上报由 agent 串行，无交错）。
func (s *MetricIngestService) Ingest(p MetricReportParams) (MetricReportResult, error) {
	if err := s.validateBinding(p); err != nil {
		return MetricReportResult{}, err
	}
	if err := s.validateClockSkew(p.AgentTimeMs); err != nil {
		return MetricReportResult{}, err
	}
	if p.DroppedSinceLast > 0 {
		// 断连超 10 分钟环形容量导致样本被覆盖丢弃——记 WARN 让「丢了多少」可见（错误不静默，ADR-0057）。
		slog.Warn("agent 指标缓冲有样本被覆盖丢弃",
			"namespace", p.Identity.Namespace, "serverId", p.Identity.ServerID, "丢弃样本数", p.DroppedSinceLast)
	}
	if len(p.Samples) == 0 {
		return MetricReportResult{}, nil
	}
	if len(p.Samples) > maxSamplesPerReport {
		return MetricReportResult{}, apperr.ErrInvalidParam
	}

	recvMs := s.now().UnixMilli()
	newRows := make([]model.MetricSampleV2, 0, len(p.Samples))
	newWindow := make([]metricwindow.Sample, 0, len(p.Samples))
	seen := make(map[int64]struct{}, len(p.Samples))
	deduplicated := 0
	for _, raw := range p.Samples {
		if raw.BucketStartMs <= 0 || raw.SampleCount < 0 {
			return MetricReportResult{}, apperr.ErrInvalidParam
		}
		// 批内重复 或 窗口内已存在（60s 内重放）→ 计重放、不重复入队。
		if _, dup := seen[raw.BucketStartMs]; dup || s.window.Contains(p.Identity.NamespaceID, p.Identity.ServerID, raw.BucketStartMs) {
			deduplicated++
			continue
		}
		seen[raw.BucketStartMs] = struct{}{}
		norm := normalizeSampleByKind(raw, p.Identity.Kind)
		newRows = append(newRows, toMetricRow(norm, p.Identity))
		newWindow = append(newWindow, toWindowSample(norm, p.Identity, recvMs))
	}

	if len(newRows) > 0 {
		if !s.enqueue.Enqueue(newRows) {
			// 队列满：不改窗口、回 429 背压，agent 保留缓冲重试（数据不丢在 agent 侧）。
			return MetricReportResult{}, apperr.ErrMetricsIngestBusy
		}
		for _, ws := range newWindow {
			s.window.Upsert(ws)
		}
	}
	return MetricReportResult{Accepted: len(newRows), Deduplicated: deduplicated}, nil
}

// validateBinding 校验请求体自报 serverId / kind 与权威身份一致（防错配 agent 用他人身份串报）。
func (s *MetricIngestService) validateBinding(p MetricReportParams) error {
	if p.BodyServerID != "" && p.BodyServerID != p.Identity.ServerID {
		return apperr.ErrIdentityBindingMismatch
	}
	if p.BodyKind != "" && p.BodyKind != p.Identity.Kind {
		return apperr.ErrIdentityBindingMismatch
	}
	return nil
}

// validateClockSkew 校验 agent 时钟与控制面偏移；超阈值整批拒绝（400）。agentTimeMs<=0 视为未提供、跳过。
func (s *MetricIngestService) validateClockSkew(agentTimeMs int64) error {
	if agentTimeMs <= 0 {
		return nil
	}
	skew := s.now().Sub(time.UnixMilli(agentTimeMs))
	if skew < 0 {
		skew = -skew
	}
	if skew > s.skewLimit {
		return apperr.ErrClockSkewTooLarge
	}
	return nil
}

// normalizeSampleByKind 按角色清除不适用列（写缺省值 0 / -1，不特判存 NULL，见 §3.1）：
// backend 无连接 / 后端探测（清 0，rtt 置 -1）；proxy 无 tps / 在线 / 容量（清 0）。report_rtt_ms 两者皆适用。
func normalizeSampleByKind(s MetricReportSample, kind string) MetricReportSample {
	switch kind {
	case model.ServerKindBackend:
		s.ConnAvg, s.ConnMax = 0, 0
		s.BackendUp, s.BackendTotal = 0, 0
		s.BackendRttMsAvg = rttUnavailable
	case model.ServerKindProxy:
		s.TPSAvg, s.TPSMin = 0, 0
		s.OnlineAvg, s.OnlineMax, s.MaxOnline = 0, 0, 0
	}
	return s
}

// toMetricRow 把归一化后的上报样本映射为日表行模型（namespace/serverId/kind 取权威身份）。
func toMetricRow(s MetricReportSample, id agentauth.Identity) model.MetricSampleV2 {
	return model.MetricSampleV2{
		NamespaceID:     id.NamespaceID,
		ServerID:        id.ServerID,
		Kind:            id.Kind,
		BucketStartMs:   s.BucketStartMs,
		SampleCount:     s.SampleCount,
		CPUPctAvg:       s.CPUPctAvg,
		CPUPctMax:       s.CPUPctMax,
		MemUsedMbAvg:    s.MemUsedMbAvg,
		MemMaxMb:        s.MemMaxMb,
		TPSAvg:          s.TPSAvg,
		TPSMin:          s.TPSMin,
		OnlineAvg:       s.OnlineAvg,
		OnlineMax:       s.OnlineMax,
		MaxOnline:       s.MaxOnline,
		ConnAvg:         s.ConnAvg,
		ConnMax:         s.ConnMax,
		BackendUp:       s.BackendUp,
		BackendTotal:    s.BackendTotal,
		BackendRttMsAvg: s.BackendRttMsAvg,
		ReportRttMs:     s.ReportRttMs,
	}
}

// toWindowSample 把归一化后的上报样本映射为 60s 窗口内存样本。
func toWindowSample(s MetricReportSample, id agentauth.Identity, recvMs int64) metricwindow.Sample {
	return metricwindow.Sample{
		NamespaceID:     id.NamespaceID,
		ServerID:        id.ServerID,
		Kind:            id.Kind,
		BucketStartMs:   s.BucketStartMs,
		SampleCount:     s.SampleCount,
		CPUPctAvg:       s.CPUPctAvg,
		CPUPctMax:       s.CPUPctMax,
		MemUsedMbAvg:    s.MemUsedMbAvg,
		MemMaxMb:        s.MemMaxMb,
		TPSAvg:          s.TPSAvg,
		TPSMin:          s.TPSMin,
		OnlineAvg:       s.OnlineAvg,
		OnlineMax:       s.OnlineMax,
		MaxOnline:       s.MaxOnline,
		ConnAvg:         s.ConnAvg,
		ConnMax:         s.ConnMax,
		BackendUp:       s.BackendUp,
		BackendTotal:    s.BackendTotal,
		BackendRttMsAvg: s.BackendRttMsAvg,
		ReportRttMs:     s.ReportRttMs,
		ReceivedAtMs:    recvMs,
	}
}
