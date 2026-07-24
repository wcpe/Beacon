package service

import (
	"encoding/json"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// 本文件是交付编排 V2（变更单）的对外响应视图（camelCase，逐字对齐 packages/contracts/src/delivery.ts
// 与 devmock 形状——前端已按此消费，不可漂移）。绝不直出 GORM 模型（PascalCase 会破契约），handler 直出视图。

// ChangeSelector 是目标筛选器（spec §4.3.1）：存库为 TEXT JSON、视图为对象，字段名即存储与响应契约。
type ChangeSelector struct {
	// 是否取 namespace 内全部合格目标
	All bool `json:"all"`
	// 大区 id 集合（展开为其下全部小区）
	Regions []uint `json:"regions"`
	// 小区 id 集合
	Zones []uint `json:"zones"`
	// 显式点名的 serverId 集合
	Servers []string `json:"servers"`
	// 从并集中剔除的 serverId 集合
	Excludes []string `json:"excludes"`
}

// normalized 返回数组字段非 nil 的副本（响应恒为数组不为 null，与 devmock emptySelector 口径一致）。
func (s ChangeSelector) normalized() ChangeSelector {
	if s.Regions == nil {
		s.Regions = []uint{}
	}
	if s.Zones == nil {
		s.Zones = []uint{}
	}
	if s.Servers == nil {
		s.Servers = []string{}
	}
	if s.Excludes == nil {
		s.Excludes = []string{}
	}
	return s
}

// encodeSelector 把筛选器序列化为存库 TEXT。
func encodeSelector(s ChangeSelector) string {
	raw, _ := json.Marshal(s.normalized())
	return string(raw)
}

// decodeSelector 解析存库 TEXT 为筛选器；空串 / 损坏回退空筛选器（数组恒非 nil）。
func decodeSelector(raw string) ChangeSelector {
	var s ChangeSelector
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &s)
	}
	return s.normalized()
}

// encodeBatchSizes 把批次规划数组序列化为存库文本。
func encodeBatchSizes(sizes []int) string {
	if sizes == nil {
		sizes = []int{}
	}
	raw, _ := json.Marshal(sizes)
	return string(raw)
}

// decodeBatchSizes 解析存库批次规划文本；空串 / 损坏回退空数组（响应恒为数组不为 null）。
func decodeBatchSizes(raw string) []int {
	var sizes []int
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &sizes)
	}
	if sizes == nil {
		sizes = []int{}
	}
	return sizes
}

// ChangeOrderSummaryView 对齐 contracts ChangeOrderSummary（列表项）。
type ChangeOrderSummaryView struct {
	ID                            uint       `json:"id"`
	NamespaceID                   uint       `json:"namespaceId"`
	Title                         string     `json:"title"`
	Description                   string     `json:"description"`
	SourceServerID                *string    `json:"sourceServerId"`
	ScanDir                       string     `json:"scanDir"`
	Status                        string     `json:"status"`
	PauseKind                     *string    `json:"pauseKind"`
	PauseReason                   *string    `json:"pauseReason"`
	BatchMode                     string     `json:"batchMode"`
	BatchSizes                    []int      `json:"batchSizes"`
	ActivationMethod              string     `json:"activationMethod"`
	ObserveWindowSec              int        `json:"observeWindowSec"`
	ActivateTimeoutSec            int        `json:"activateTimeoutSec"`
	FailureRateThresholdPercent   int        `json:"failureRateThresholdPercent"`
	UnhealthyRateThresholdPercent int        `json:"unhealthyRateThresholdPercent"`
	PayloadState                  string     `json:"payloadState"`
	DiffSnapshotAt                *time.Time `json:"diffSnapshotAt"`
	CreatedBy                     string     `json:"createdBy"`
	SubmittedAt                   *time.Time `json:"submittedAt"`
	ApprovedBy                    *string    `json:"approvedBy"`
	ApprovedAt                    *time.Time `json:"approvedAt"`
	RejectReason                  *string    `json:"rejectReason"`
	StartedAt                     *time.Time `json:"startedAt"`
	FinishedAt                    *time.Time `json:"finishedAt"`
	CancelReason                  *string    `json:"cancelReason"`
	RollbackBy                    *string    `json:"rollbackBy"`
	RollbackReason                *string    `json:"rollbackReason"`
	RollbackAt                    *time.Time `json:"rollbackAt"`
	CreatedAt                     time.Time  `json:"createdAt"`
	UpdatedAt                     time.Time  `json:"updatedAt"`
}

// ChangeOrderListView 是变更单分页列表响应（对齐 contracts Paged<ChangeOrderSummary>）。
type ChangeOrderListView struct {
	Items []ChangeOrderSummaryView `json:"items"`
	Total int64                    `json:"total"`
}

// ChangeOrderItemView 对齐 contracts ChangeOrderItem（两种载荷共形，另一半字段为 null）。
type ChangeOrderItemView struct {
	ID                  uint    `json:"id"`
	Kind                string  `json:"kind"`
	Path                *string `json:"path"`
	Action              *string `json:"action"`
	SHA256              *string `json:"sha256"`
	SizeBytes           *int64  `json:"sizeBytes"`
	ConfigScopeKind     *string `json:"configScopeKind"`
	ConfigScopeID       *uint   `json:"configScopeId"`
	ConfigFromVersionID *uint   `json:"configFromVersionId"`
	ConfigToVersionID   *uint   `json:"configToVersionId"`
}

// ChangeBatchView 对齐 contracts ChangeBatch。
type ChangeBatchView struct {
	BatchNo          int        `json:"batchNo"`
	Status           string     `json:"status"`
	PlannedCount     int        `json:"plannedCount"`
	SuccessCount     int        `json:"successCount"`
	FailedCount      int        `json:"failedCount"`
	SkippedCount     int        `json:"skippedCount"`
	StartedAt        *time.Time `json:"startedAt"`
	ObserveStartedAt *time.Time `json:"observeStartedAt"`
	FinishedAt       *time.Time `json:"finishedAt"`
	GateConfirmedBy  *string    `json:"gateConfirmedBy"`
	GateConfirmedAt  *time.Time `json:"gateConfirmedAt"`
	BreakReason      *string    `json:"breakReason"`
}

// ChangeTargetView 对齐 contracts ChangeTarget。
type ChangeTargetView struct {
	ServerID         string     `json:"serverId"`
	BatchNo          int        `json:"batchNo"`
	Status           string     `json:"status"`
	PushedAt         *time.Time `json:"pushedAt"`
	ActivatedAt      *time.Time `json:"activatedAt"`
	ChangedFileCount int        `json:"changedFileCount"`
	SkippedFileCount int        `json:"skippedFileCount"`
	BackupPresent    bool       `json:"backupPresent"`
	Error            *string    `json:"error"`
	RollbackStatus   *string    `json:"rollbackStatus"`
	RollbackError    *string    `json:"rollbackError"`
}

// ChangeTargetPageView 是目标分页响应（对齐 contracts Paged<ChangeTarget>）。
type ChangeTargetPageView struct {
	Items []ChangeTargetView `json:"items"`
	Total int64              `json:"total"`
}

// ChangeOrderDetailView 对齐 contracts ChangeOrderDetail（Summary + selector + items + 批次 + 计数）。
type ChangeOrderDetailView struct {
	ChangeOrderSummaryView
	Selector       ChangeSelector        `json:"selector"`
	Items          []ChangeOrderItemView `json:"items"`
	Batches        []ChangeBatchView     `json:"batches"`
	TargetCounts   map[string]int64      `json:"targetCounts"`
	RollbackCounts map[string]int64      `json:"rollbackCounts"`
}

// DiffScanView 是差异扫描响应（对齐 apps/web DiffScanResponse）。
type DiffScanView struct {
	Status         string                `json:"status"`
	DiffSnapshotAt *time.Time            `json:"diffSnapshotAt"`
	Items          []ChangeOrderItemView `json:"items"`
}

// ChangeImpactBatchView 是影响预览的批次划分预览项。
type ChangeImpactBatchView struct {
	BatchNo int `json:"batchNo"`
	Count   int `json:"count"`
}

// ChangeImpactConfigScopeView 是影响预览逐目标行命中的配置作用域（spec §4.2.2「该目标命中的配置作用域与 from→to 版本」）。
type ChangeImpactConfigScopeView struct {
	ScopeKind     string `json:"scopeKind"`
	ScopeID       uint   `json:"scopeId"`
	FromVersionID *uint  `json:"fromVersionId"`
	ToVersionID   *uint  `json:"toVersionId"`
}

// ChangeImpactTargetView 是影响预览逐目标行（对齐 contracts ChangeImpactResponse.targets 元素 + configScopes 扩展）。
type ChangeImpactTargetView struct {
	ServerID     string                        `json:"serverId"`
	Online       bool                          `json:"online"`
	Level        string                        `json:"level"`
	AddCount     int                           `json:"addCount"`
	UpdateCount  int                           `json:"updateCount"`
	DeleteCount  int                           `json:"deleteCount"`
	SkipCount    int                           `json:"skipCount"`
	ConfigScopes []ChangeImpactConfigScopeView `json:"configScopes"`
}

// ChangeImpactSummaryView 是影响预览汇总。
type ChangeImpactSummaryView struct {
	TargetTotal      int                     `json:"targetTotal"`
	Batches          []ChangeImpactBatchView `json:"batches"`
	FileTotal        int                     `json:"fileTotal"`
	TotalBytes       int64                   `json:"totalBytes"`
	TransferBytes    int64                   `json:"transferBytes"`
	ConfigScopeCount int                     `json:"configScopeCount"`
	SnapshotAt       *time.Time              `json:"snapshotAt"`
}

// ChangeImpactTargetsPageView 是影响预览逐目标分页。
type ChangeImpactTargetsPageView struct {
	Items []ChangeImpactTargetView `json:"items"`
	Total int64                    `json:"total"`
}

// ChangeImpactView 是影响预览响应（对齐 contracts ChangeImpactResponse）。
type ChangeImpactView struct {
	Summary ChangeImpactSummaryView     `json:"summary"`
	Targets ChangeImpactTargetsPageView `json:"targets"`
}

// ChangeObserveView 是观察窗数据响应（对齐 contracts ChangeObserveResponse；M1 恒空形态，M3 接真实观察窗）。
type ChangeObserveView struct {
	BatchNo          *int                        `json:"batchNo"`
	ObserveStartedAt *time.Time                  `json:"observeStartedAt"`
	Targets          []ChangeObserveTargetSeries `json:"targets"`
}

// ChangeObserveTargetSeries 是观察窗逐目标健康序列（M1 不产出，仅占形状）。
type ChangeObserveTargetSeries struct {
	ServerID string                     `json:"serverId"`
	Series   []ChangeObserveSeriesPoint `json:"series"`
}

// ChangeObserveSeriesPoint 是观察窗单点（对齐 contracts ChangeObserveResponse.targets[].series 元素）。
type ChangeObserveSeriesPoint struct {
	TsMs   int64   `json:"tsMs"`
	Score  int     `json:"score"`
	Level  string  `json:"level"`
	TPS    float64 `json:"tps"`
	Alerts int     `json:"alerts"`
}

// ChangeOrderEventView 是进度事件（对齐 contracts ChangeOrderEvent；M1 由生命周期字段确定性派生）。
type ChangeOrderEventView struct {
	Seq      int       `json:"seq"`
	At       time.Time `json:"at"`
	Type     string    `json:"type"`
	OrderID  uint      `json:"orderId"`
	BatchNo  *int      `json:"batchNo"`
	ServerID *string   `json:"serverId"`
	Status   string    `json:"status"`
}

// ChangeEventsView 是事件端点响应（对齐 apps/web ChangeEventsResponse，SSE 的轮询替代形态）。
type ChangeEventsView struct {
	Events []ChangeOrderEventView `json:"events"`
}

// ChangeFileDiffView 是变更项文件内容预览响应（file-diff 端点正式契约，spec §5.1）：
// after=模板源内容、before=目标内容；serverId 回填实际所用目标（无可用目标为 null）；
// 二进制项不取内容（binary=true、前后皆 null）。
type ChangeFileDiffView struct {
	Path       string  `json:"path"`
	ChangeType string  `json:"changeType"`
	Before     *string `json:"before"`
	After      *string `json:"after"`
	Truncated  bool    `json:"truncated"`
	Binary     bool    `json:"binary"`
	ServerID   *string `json:"serverId"`
}

// changeOrderSummaryView 把变更单实体映射为列表视图（空串可空字段 → null，JSON 文本 → 结构）。
func changeOrderSummaryView(o *model.ChangeOrder) ChangeOrderSummaryView {
	return ChangeOrderSummaryView{
		ID: o.ID, NamespaceID: o.NamespaceID, Title: o.Title, Description: o.Description,
		SourceServerID: nilIfEmpty(o.SourceServerID), ScanDir: o.ScanDir, Status: o.Status,
		PauseKind: nilIfEmpty(o.PauseKind), PauseReason: nilIfEmpty(o.PauseReason),
		BatchMode: o.BatchMode, BatchSizes: decodeBatchSizes(o.BatchSizes),
		ActivationMethod: o.ActivationMethod, ObserveWindowSec: o.ObserveWindowSec,
		ActivateTimeoutSec: o.ActivateTimeoutSec, FailureRateThresholdPercent: o.FailureRateThresholdPercent,
		UnhealthyRateThresholdPercent: o.UnhealthyRateThresholdPercent, PayloadState: o.PayloadState,
		DiffSnapshotAt: o.DiffSnapshotAt, CreatedBy: o.CreatedBy, SubmittedAt: o.SubmittedAt,
		ApprovedBy: nilIfEmpty(o.ApprovedBy), ApprovedAt: o.ApprovedAt, RejectReason: nilIfEmpty(o.RejectReason),
		StartedAt: o.StartedAt, FinishedAt: o.FinishedAt, CancelReason: nilIfEmpty(o.CancelReason),
		RollbackBy: nilIfEmpty(o.RollbackBy), RollbackReason: nilIfEmpty(o.RollbackReason), RollbackAt: o.RollbackAt,
		CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}
}

// changeOrderItemViews 把变更项实体批量映射为视图（模型可空列已是指针，直透）。
func changeOrderItemViews(items []model.ChangeOrderItem) []ChangeOrderItemView {
	views := make([]ChangeOrderItemView, 0, len(items))
	for i := range items {
		views = append(views, changeOrderItemView(&items[i]))
	}
	return views
}

// changeOrderItemView 把单个变更项实体映射为视图。
func changeOrderItemView(item *model.ChangeOrderItem) ChangeOrderItemView {
	return ChangeOrderItemView{
		ID: item.ID, Kind: item.Kind, Path: item.Path, Action: item.Action,
		SHA256: item.SHA256, SizeBytes: item.SizeBytes,
		ConfigScopeKind: item.ConfigScopeKind, ConfigScopeID: item.ConfigScopeID,
		ConfigFromVersionID: item.ConfigFromVersionID, ConfigToVersionID: item.ConfigToVersionID,
	}
}

// changeBatchViews 把批次实体批量映射为视图。
func changeBatchViews(batches []model.ChangeBatch) []ChangeBatchView {
	views := make([]ChangeBatchView, 0, len(batches))
	for i := range batches {
		b := &batches[i]
		views = append(views, ChangeBatchView{
			BatchNo: b.BatchNo, Status: b.Status, PlannedCount: b.PlannedCount,
			SuccessCount: b.SuccessCount, FailedCount: b.FailedCount, SkippedCount: b.SkippedCount,
			StartedAt: b.StartedAt, ObserveStartedAt: b.ObserveStartedAt, FinishedAt: b.FinishedAt,
			GateConfirmedBy: nilIfEmpty(b.GateConfirmedBy), GateConfirmedAt: b.GateConfirmedAt,
			BreakReason: nilIfEmpty(b.BreakReason),
		})
	}
	return views
}

// changeTargetViews 把目标实体批量映射为视图（batch_id → batch_no 经批次映射换算）。
func changeTargetViews(targets []model.ChangeTarget, batchNoByID map[uint]int) []ChangeTargetView {
	views := make([]ChangeTargetView, 0, len(targets))
	for i := range targets {
		t := &targets[i]
		views = append(views, ChangeTargetView{
			ServerID: t.ServerID, BatchNo: batchNoByID[t.BatchID], Status: t.Status,
			PushedAt: t.PushedAt, ActivatedAt: t.ActivatedAt,
			ChangedFileCount: t.ChangedFileCount, SkippedFileCount: t.SkippedFileCount,
			BackupPresent: t.BackupPresent, Error: nilIfEmpty(t.Error),
			RollbackStatus: nilIfEmpty(t.RollbackStatus), RollbackError: nilIfEmpty(t.RollbackError),
		})
	}
	return views
}

// nilIfEmpty 把空串映射为 null（模型「空串=可空」列到视图 string|null 的统一换算）。
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
