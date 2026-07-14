package service

import (
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// 上报模式（规格 §4.3）。
const (
	manifestModeDelta = "delta"
	manifestModeFull  = "full"
)

const (
	// defaultAssetPageSize 是搜索 / 概要分页默认页大小。
	defaultAssetPageSize = 50
	// maxAssetPageSize 是分页上限（规格 §5.2 pageSize≤200）。
	maxAssetPageSize = 200
	// maxRescanServers 是单次重扫目标服上限（规格 §5.2 serverIds≤100）。
	maxRescanServers = 100
	// fullUploadTTL 是全量分片暂存保活窗口（规格 §4.3）。
	fullUploadTTL = 5 * time.Minute
)

// AssetService 是文件资产索引域服务（FR-163，规格 v2-file-assets.md）：
// agent 面清单上报（增量 / 全量，摘要校准）+ 管理面搜索 / 概要 / 跨服比对 / 批量重扫下发。
// 分层 handler → service → repository 单向依赖；多表写在事务内原子完成。本域对目标文件系统零写入（只读索引）。
type AssetService struct {
	db          *gorm.DB
	repo        *repository.FileAssetRepository
	scanRepo    *repository.FileAssetScanRepository
	commandRepo *repository.AgentCommandRepository
	auditRepo   *repository.AuditLogRepository
	notifier    CommandNotifier // 重扫命令待办唤醒器（可选注入；未注入则建命令后不主动唤醒）
	staging     *fullUploadStaging
	now         func() time.Time
}

// NewAssetService 构造服务。
func NewAssetService(db *gorm.DB, repo *repository.FileAssetRepository, scanRepo *repository.FileAssetScanRepository,
	commandRepo *repository.AgentCommandRepository, auditRepo *repository.AuditLogRepository) *AssetService {
	now := func() time.Time { return time.Now().UTC() }
	return &AssetService{
		db: db, repo: repo, scanRepo: scanRepo, commandRepo: commandRepo, auditRepo: auditRepo,
		staging: newFullUploadStaging(fullUploadTTL, now), now: now,
	}
}

// SetNotifier 注入重扫命令待办唤醒器（启动时装配；未注入则建命令后不主动唤醒，留待 agent 重连拉取）。
func (s *AssetService) SetNotifier(n CommandNotifier) { s.notifier = n }

// ---- agent 面：清单上报 ----

// ManifestUpsert 是一条上报的 upsert 文件（agent 面 upserts[] 项，规格 §5.1）。
type ManifestUpsert struct {
	Path    string
	SHA256  string
	Size    int64
	MtimeMs int64
	IsText  bool
}

// ManifestReportParams 是一次清单上报入参（身份为中间件注入的权威绑定，非请求体自报）。
type ManifestReportParams struct {
	Identity       agentauth.Identity
	Mode           string
	ScannedAt      time.Time
	ScanDurationMs int
	Truncated      bool
	// delta
	BaseDigest string
	Upserts    []ManifestUpsert
	Deleted    []string
	// full 分片
	UploadID string
	Seq      int
	EOF      bool
}

// ManifestReportResult 是上报处理结果（对齐 agent 面 200 响应 digest / fileCount，规格 §5.1）。
// 全量非末批（未收齐）时 Digest 为空，agent 继续发下一分片。
type ManifestReportResult struct {
	Digest    string
	FileCount int
}

// ApplyManifest 处理一次清单上报：解析权威身份 → 定位 server 行 → 校验条目 → 增量 / 全量应用。
// 增量基线失配 / 全量分片乱序返回 ErrAssetManifestOutOfSync（409），agent 收到即改发全量自愈。
func (s *AssetService) ApplyManifest(p ManifestReportParams) (ManifestReportResult, error) {
	server, err := findServerRow(s.db, p.Identity.NamespaceID, p.Identity.ServerID)
	if err != nil {
		return ManifestReportResult{}, err
	}
	if server == nil {
		// 已确认身份理应有 server 行（确认时建）；缺失属异常，按实例不存在兜底。
		return ManifestReportResult{}, apperr.ErrInstanceNotFound
	}
	if err := validateUpserts(p.Upserts); err != nil {
		return ManifestReportResult{}, err
	}
	scannedAt := p.ScannedAt
	if scannedAt.IsZero() {
		scannedAt = s.now()
	}
	p.ScannedAt = scannedAt
	switch p.Mode {
	case manifestModeDelta:
		return s.applyDelta(server, p)
	case manifestModeFull:
		return s.applyFull(server, p)
	default:
		return ManifestReportResult{}, apperr.ErrInvalidParam
	}
}

// applyDelta 单事务应用增量：校验 baseDigest == 库内摘要 → upsert + delete → 重算摘要 → 刷新概要。
// 零变更空 delta（无 upserts / deleted）仅刷新 scanned_at 并回当前摘要（保活，规格 §4.3）。
func (s *AssetService) applyDelta(server *model.Server, p ManifestReportParams) (ManifestReportResult, error) {
	var result ManifestReportResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		r := s.repo.WithTx(tx)
		sr := s.scanRepo.WithTx(tx)
		scan, err := sr.FindByServer(server.ID)
		if err != nil {
			return err
		}
		current := ""
		if scan != nil {
			current = scan.ManifestDigest
		}
		if p.BaseDigest != current {
			return apperr.ErrAssetManifestOutOfSync
		}
		if err := r.UpsertAssets(entriesToFileAssets(server, upsertsToEntries(p.Upserts), p.ScannedAt)); err != nil {
			return err
		}
		if err := r.DeleteByServerPaths(server.ID, p.Deleted); err != nil {
			return err
		}
		all, err := r.ListByServer(server.ID)
		if err != nil {
			return err
		}
		digest, count, totalSize := summarize(all)
		if err := sr.Upsert(buildScanRow(server, digest, count, totalSize, p)); err != nil {
			return err
		}
		result = ManifestReportResult{Digest: digest, FileCount: count}
		return nil
	})
	if err != nil {
		return ManifestReportResult{}, err
	}
	return result, nil
}

// applyFull 累积全量分片；收齐（eof）后单事务整体替换该服全部行并刷新概要。
// 未收齐返回空摘要（agent 续发下一片）；分片乱序 / 暂存过期返回 ErrAssetManifestOutOfSync。
func (s *AssetService) applyFull(server *model.Server, p ManifestReportParams) (ManifestReportResult, error) {
	assembled, done, err := s.staging.append(stagingKey(server.ID, p.UploadID), p.Seq, p.EOF, upsertsToEntries(p.Upserts))
	if err != nil {
		if errors.Is(err, errStagingOutOfSync) {
			return ManifestReportResult{}, apperr.ErrAssetManifestOutOfSync
		}
		return ManifestReportResult{}, err
	}
	if !done {
		return ManifestReportResult{Digest: "", FileCount: len(p.Upserts)}, nil
	}
	digest := computeManifestDigest(assembled)
	rows := entriesToFileAssets(server, assembled, p.ScannedAt)
	var totalSize int64
	for i := range rows {
		totalSize += rows[i].Size
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		r := s.repo.WithTx(tx)
		sr := s.scanRepo.WithTx(tx)
		if err := r.DeleteAllByServer(server.ID); err != nil {
			return err
		}
		if err := r.UpsertAssets(rows); err != nil {
			return err
		}
		return sr.Upsert(buildScanRow(server, digest, len(rows), totalSize, p))
	})
	if err != nil {
		return ManifestReportResult{}, err
	}
	slog.Info("文件资产全量上报入库", "namespace", p.Identity.Namespace, "serverId", p.Identity.ServerID,
		"fileCount", len(rows), "truncated", p.Truncated)
	return ManifestReportResult{Digest: digest, FileCount: len(rows)}, nil
}

// ---- 管理面：搜索 ----

// AssetSearchParams 是搜索入参（serverId 为业务串，service 内解析为 server.id 数字行 id）。
type AssetSearchParams struct {
	NamespaceID uint
	ServerID    string
	PathPrefix  string
	Name        string
	Ext         string
	SHA256      string
	Page        int
	PageSize    int
}

// AssetItemView 是资产行对外视图（camelCase 由 handler 映射，serverId 为业务串）。
type AssetItemView struct {
	ServerID    string
	NamespaceID uint
	Path        string
	Ext         string
	SHA256      string
	Size        int64
	MtimeMs     int64
	IsText      bool
	ScannedAt   time.Time
}

// AssetListResult 是搜索分页结果。
type AssetListResult struct {
	Items []AssetItemView
	Total int64
}

// Search 组合条件分页搜索（规格 §4.4）：namespaceId 必填；name 兜底子串须与至少一个索引条件组合，否则拒绝。
func (s *AssetService) Search(p AssetSearchParams) (AssetListResult, error) {
	if p.NamespaceID == 0 {
		return AssetListResult{}, apperr.ErrInvalidParam
	}
	if p.Name != "" && p.ServerID == "" && p.PathPrefix == "" && p.Ext == "" && p.SHA256 == "" {
		return AssetListResult{}, apperr.ErrInvalidParam // 无索引条件的裸 name 查询拒绝（防全表扫描）
	}
	var serverRowID uint
	if p.ServerID != "" {
		server, err := findServerRow(s.db, p.NamespaceID, p.ServerID)
		if err != nil {
			return AssetListResult{}, err
		}
		if server == nil {
			return AssetListResult{Items: []AssetItemView{}, Total: 0}, nil // serverId 不存在 → 空结果
		}
		serverRowID = server.ID
	}
	offset, limit := paginate(p.Page, p.PageSize)
	rows, total, err := s.repo.Search(repository.AssetSearchQuery{
		NamespaceID: p.NamespaceID, ServerID: serverRowID,
		PathPrefix: p.PathPrefix, Name: p.Name, Ext: p.Ext, SHA256: p.SHA256,
		Offset: offset, Limit: limit,
	})
	if err != nil {
		return AssetListResult{}, err
	}
	idToServerID, err := s.serverIDNames(collectServerIDs(rows))
	if err != nil {
		return AssetListResult{}, err
	}
	items := make([]AssetItemView, 0, len(rows))
	for i := range rows {
		items = append(items, toAssetItemView(&rows[i], idToServerID))
	}
	return AssetListResult{Items: items, Total: total}, nil
}

// ---- 管理面：扫描概要 ----

// ScanStatusParams 是每服扫描概要查询入参。
type ScanStatusParams struct {
	NamespaceID uint
	ServerID    string
	Page        int
	PageSize    int
}

// ScanStatusView 是概要对外视图（serverId 为业务串）。
type ScanStatusView struct {
	ServerID       string
	NamespaceID    uint
	ManifestDigest string
	FileCount      int
	TotalSize      int64
	Truncated      bool
	ScannedAt      time.Time
	ScanDurationMs int
}

// ScanStatusResult 是概要分页结果。
type ScanStatusResult struct {
	Items []ScanStatusView
	Total int64
}

// ScanStatus 分页列某 namespace 的每服扫描概要（规格 §5.2）。
func (s *AssetService) ScanStatus(p ScanStatusParams) (ScanStatusResult, error) {
	if p.NamespaceID == 0 {
		return ScanStatusResult{}, apperr.ErrInvalidParam
	}
	var serverRowID uint
	if p.ServerID != "" {
		server, err := findServerRow(s.db, p.NamespaceID, p.ServerID)
		if err != nil {
			return ScanStatusResult{}, err
		}
		if server == nil {
			return ScanStatusResult{Items: []ScanStatusView{}, Total: 0}, nil
		}
		serverRowID = server.ID
	}
	offset, limit := paginate(p.Page, p.PageSize)
	rows, total, err := s.scanRepo.ListByNamespace(p.NamespaceID, serverRowID, offset, limit)
	if err != nil {
		return ScanStatusResult{}, err
	}
	idToServerID, err := s.serverIDNames(collectScanServerIDs(rows))
	if err != nil {
		return ScanStatusResult{}, err
	}
	items := make([]ScanStatusView, 0, len(rows))
	for i := range rows {
		items = append(items, toScanStatusView(&rows[i], idToServerID))
	}
	return ScanStatusResult{Items: items, Total: total}, nil
}

// ---- 管理面：跨服比对 ----

// CompareParams 是跨服哈希比对入参（范围二选一：zoneId / serverIds；均空即整 namespace）。
type CompareParams struct {
	NamespaceID uint
	Path        string
	ZoneID      uint
	ServerIDs   []string
}

// CompareMemberView 是分组内一台成员服。
type CompareMemberView struct {
	ServerID  string
	MtimeMs   int64
	ScannedAt time.Time
}

// CompareGroupView 是一个哈希分组。
type CompareGroupView struct {
	SHA256  string
	Size    int64
	Servers []CompareMemberView
}

// CompareResult 是比对结果：按 sha256 分组 + 范围内无该文件的服列表。
type CompareResult struct {
	Path    string
	Groups  []CompareGroupView
	Missing []string
}

// Compare 跨服同路径哈希分组比对 + 缺失服列表（规格 §4.4）：范围内命中行按 sha256 分组，未命中服归 missing。
func (s *AssetService) Compare(p CompareParams) (CompareResult, error) {
	if p.NamespaceID == 0 || p.Path == "" {
		return CompareResult{}, apperr.ErrInvalidParam
	}
	candidates, err := s.resolveCompareServers(p)
	if err != nil {
		return CompareResult{}, err
	}
	if len(candidates) == 0 {
		return CompareResult{Path: p.Path, Groups: []CompareGroupView{}, Missing: []string{}}, nil
	}
	rows, err := s.repo.FindByNamespacePathServers(p.NamespaceID, p.Path, serverRowIDs(candidates))
	if err != nil {
		return CompareResult{}, err
	}
	return buildCompareResult(p.Path, candidates, rows), nil
}

// resolveCompareServers 解析比对范围内的候选服（规格 §4.4）：
// 显式 serverIds 优先（逐个按 namespace 解析、丢弃不存在的）；否则 zoneId 限定；否则整 namespace。
func (s *AssetService) resolveCompareServers(p CompareParams) ([]model.Server, error) {
	if len(p.ServerIDs) > 0 {
		servers := make([]model.Server, 0, len(p.ServerIDs))
		seen := map[uint]struct{}{}
		for _, sid := range p.ServerIDs {
			server, err := findServerRow(s.db, p.NamespaceID, sid)
			if err != nil {
				return nil, err
			}
			if server == nil {
				continue
			}
			if _, dup := seen[server.ID]; dup {
				continue
			}
			seen[server.ID] = struct{}{}
			servers = append(servers, *server)
		}
		return servers, nil
	}
	query := s.db.Where("namespace_id = ?", p.NamespaceID)
	if p.ZoneID != 0 {
		query = query.Where("zone_id = ?", p.ZoneID)
	}
	var servers []model.Server
	if err := query.Order("id ASC").Find(&servers).Error; err != nil {
		return nil, err
	}
	return servers, nil
}

// ---- 管理面：批量重扫下发 ----

// RescanParams 是批量重扫入参。
type RescanParams struct {
	NamespaceID uint
	ServerIDs   []string
	Force       bool
	Operator    string
	ClientIP    string
}

// RescanServerResult 是单服重扫下发结果（离线服 CommandID 为 nil、Offline=true、不建命令）。
type RescanServerResult struct {
	ServerID  string
	CommandID *uint
	Offline   bool
}

// RescanResult 是批量重扫结果。
type RescanResult struct {
	Results []RescanServerResult
}

// assetRescanPayload 是 asset-rescan 命令载荷（force=true 令 agent 忽略本地 mtime 缓存全部重哈希，规格 §4.2）。
type assetRescanPayload struct {
	Force bool `json:"force"`
}

// Rescan 批量对目标服下发 asset-rescan 命令（规格 §5.2）：在线服建 pending 命令、离线服标记入响应不阻断整批；
// 事务内建命令 + 记一条 asset.rescan 审计，提交成功后唤醒各在线 agent。
func (s *AssetService) Rescan(p RescanParams) (RescanResult, error) {
	if p.NamespaceID == 0 || len(p.ServerIDs) == 0 || len(p.ServerIDs) > maxRescanServers {
		return RescanResult{}, apperr.ErrInvalidParam
	}
	nsCode, err := s.namespaceCode(p.NamespaceID)
	if err != nil {
		return RescanResult{}, err
	}
	onlineKeys, err := loadOnlineServerKeys(s.db, p.ServerIDs)
	if err != nil {
		return RescanResult{}, err
	}
	payload, _ := json.Marshal(assetRescanPayload{Force: p.Force})
	results := make([]RescanServerResult, 0, len(p.ServerIDs))
	dispatched := make([]string, 0, len(p.ServerIDs))
	err = s.db.Transaction(func(tx *gorm.DB) error {
		cr := s.commandRepo.WithTx(tx)
		results = results[:0]
		dispatched = dispatched[:0]
		for _, sid := range p.ServerIDs {
			if _, online := onlineKeys[onlineKey{namespaceID: p.NamespaceID, serverID: sid}]; !online {
				results = append(results, RescanServerResult{ServerID: sid, CommandID: nil, Offline: true})
				continue
			}
			cmd := &model.AgentCommand{
				NamespaceCode: nsCode, ServerID: sid,
				Type: model.CommandTypeAssetRescan, Payload: string(payload),
				Status: model.CommandStatusPending, Operator: p.Operator,
			}
			if e := cr.Create(cmd); e != nil {
				return e
			}
			id := cmd.ID
			results = append(results, RescanServerResult{ServerID: sid, CommandID: &id, Offline: false})
			dispatched = append(dispatched, sid)
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: nsCode, Operator: p.Operator, Action: model.ActionAssetRescan,
			TargetType: model.TargetTypeServer, TargetRef: rescanTargetRef(dispatched),
			Detail: rescanAuditDetail(p.ServerIDs, dispatched, p.Force),
			Result: model.ResultOK, ClientIP: p.ClientIP,
		})
	})
	if err != nil {
		return RescanResult{}, err
	}
	if s.notifier != nil {
		for _, sid := range dispatched {
			s.notifier.NotifyCommand(nsCode, sid)
		}
	}
	slog.Info("文件资产批量重扫下发", "namespace", nsCode, "目标数", len(p.ServerIDs),
		"下发数", len(dispatched), "force", p.Force, "operator", p.Operator)
	return RescanResult{Results: results}, nil
}

// ---- 内部辅助 ----

// namespaceCode 按 namespace 数字 id 取 code；不存在返回 ErrNamespaceNotFound。
func (s *AssetService) namespaceCode(id uint) (string, error) {
	var ns model.Namespace
	err := s.db.Select("id", "code").First(&ns, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", apperr.ErrNamespaceNotFound
	}
	if err != nil {
		return "", err
	}
	return ns.Code, nil
}

// serverIDNames 批量取一组 server.id 的业务 serverId 串（numeric→string 映射，禁循环内查库）。
func (s *AssetService) serverIDNames(ids []uint) (map[uint]string, error) {
	m := map[uint]string{}
	if len(ids) == 0 {
		return m, nil
	}
	var servers []model.Server
	if err := s.db.Select("id", "server_id").Where("id IN ?", ids).Find(&servers).Error; err != nil {
		return nil, err
	}
	for i := range servers {
		m[servers[i].ID] = servers[i].ServerID
	}
	return m, nil
}

// validateUpserts 控制面入库前校验上报条目（双保险）：path 安全、sha256 合法、size 非负。
func validateUpserts(upserts []ManifestUpsert) error {
	for i := range upserts {
		u := &upserts[i]
		if !validAssetPath(u.Path) || !validSHA256Hex(u.SHA256) || u.Size < 0 {
			return apperr.ErrInvalidParam
		}
	}
	return nil
}

// upsertsToEntries 把上报 upserts 转为清单条目。
func upsertsToEntries(upserts []ManifestUpsert) []ManifestEntry {
	entries := make([]ManifestEntry, len(upserts))
	for i := range upserts {
		entries[i] = ManifestEntry{
			Path: upserts[i].Path, SHA256: upserts[i].SHA256,
			Size: upserts[i].Size, MtimeMs: upserts[i].MtimeMs, IsText: upserts[i].IsText,
		}
	}
	return entries
}

// entriesToFileAssets 把清单条目映射为落库行（ext 由 path 在控制面推导；namespace/server 取权威 server 行）。
func entriesToFileAssets(server *model.Server, entries []ManifestEntry, scannedAt time.Time) []model.FileAsset {
	rows := make([]model.FileAsset, len(entries))
	for i := range entries {
		e := &entries[i]
		rows[i] = model.FileAsset{
			NamespaceID: server.NamespaceID, ServerID: server.ID,
			Path: e.Path, Ext: deriveExt(e.Path), SHA256: e.SHA256,
			Size: e.Size, MtimeMs: e.MtimeMs, IsText: e.IsText, ScannedAt: scannedAt,
		}
	}
	return rows
}

// summarize 从落库行重算清单摘要 + 文件数 + 总字节（增量应用后的权威摘要，规格 §4.3）。
func summarize(rows []model.FileAsset) (digest string, count int, totalSize int64) {
	entries := make([]ManifestEntry, len(rows))
	for i := range rows {
		entries[i] = ManifestEntry{Path: rows[i].Path, SHA256: rows[i].SHA256, Size: rows[i].Size, MtimeMs: rows[i].MtimeMs}
		totalSize += rows[i].Size
	}
	return computeManifestDigest(entries), len(rows), totalSize
}

// buildScanRow 组装概要行（一服一行，随上报整体刷新）。
func buildScanRow(server *model.Server, digest string, count int, totalSize int64, p ManifestReportParams) *model.FileAssetScan {
	return &model.FileAssetScan{
		NamespaceID: server.NamespaceID, ServerID: server.ID,
		ManifestDigest: digest, FileCount: count, TotalSize: totalSize,
		Truncated: p.Truncated, ScannedAt: p.ScannedAt, ScanDurationMs: p.ScanDurationMs,
	}
}

// paginate 归一化分页为 (offset, limit)：pageSize 缺省 50、上限 200；page 从 1 起。
func paginate(page, pageSize int) (offset, limit int) {
	if pageSize <= 0 {
		pageSize = defaultAssetPageSize
	}
	if pageSize > maxAssetPageSize {
		pageSize = maxAssetPageSize
	}
	if page <= 0 {
		page = 1
	}
	return (page - 1) * pageSize, pageSize
}

// collectServerIDs 收集资产行去重的 server.id 集合。
func collectServerIDs(rows []model.FileAsset) []uint {
	set := map[uint]struct{}{}
	for i := range rows {
		set[rows[i].ServerID] = struct{}{}
	}
	return uintKeys(set)
}

// collectScanServerIDs 收集概要行去重的 server.id 集合。
func collectScanServerIDs(rows []model.FileAssetScan) []uint {
	set := map[uint]struct{}{}
	for i := range rows {
		set[rows[i].ServerID] = struct{}{}
	}
	return uintKeys(set)
}

// serverRowIDs 取候选服的数字行 id 列表。
func serverRowIDs(servers []model.Server) []uint {
	ids := make([]uint, 0, len(servers))
	for i := range servers {
		ids = append(ids, servers[i].ID)
	}
	return ids
}

// toAssetItemView 组装资产行视图（numeric→string serverId 由映射取）。
func toAssetItemView(row *model.FileAsset, idToServerID map[uint]string) AssetItemView {
	return AssetItemView{
		ServerID: idToServerID[row.ServerID], NamespaceID: row.NamespaceID,
		Path: row.Path, Ext: row.Ext, SHA256: row.SHA256, Size: row.Size,
		MtimeMs: row.MtimeMs, IsText: row.IsText, ScannedAt: row.ScannedAt,
	}
}

// toScanStatusView 组装概要视图。
func toScanStatusView(row *model.FileAssetScan, idToServerID map[uint]string) ScanStatusView {
	return ScanStatusView{
		ServerID: idToServerID[row.ServerID], NamespaceID: row.NamespaceID,
		ManifestDigest: row.ManifestDigest, FileCount: row.FileCount, TotalSize: row.TotalSize,
		Truncated: row.Truncated, ScannedAt: row.ScannedAt, ScanDurationMs: row.ScanDurationMs,
	}
}

// buildCompareResult 把比对命中行按 sha256 分组、算缺失服（纯内存，规格 §4.4）。
// 分组按成员数降序（最大组即多数派，前端据此标少数派差异）；组内成员按 serverId 升序稳定。
func buildCompareResult(path string, candidates []model.Server, rows []model.FileAsset) CompareResult {
	idToServerID := map[uint]string{}
	for i := range candidates {
		idToServerID[candidates[i].ID] = candidates[i].ServerID
	}
	present := map[uint]struct{}{}
	groupByHash := map[string]*CompareGroupView{}
	order := []string{}
	for i := range rows {
		row := &rows[i]
		present[row.ServerID] = struct{}{}
		g, ok := groupByHash[row.SHA256]
		if !ok {
			g = &CompareGroupView{SHA256: row.SHA256, Size: row.Size, Servers: []CompareMemberView{}}
			groupByHash[row.SHA256] = g
			order = append(order, row.SHA256)
		}
		g.Servers = append(g.Servers, CompareMemberView{
			ServerID: idToServerID[row.ServerID], MtimeMs: row.MtimeMs, ScannedAt: row.ScannedAt,
		})
	}
	groups := make([]CompareGroupView, 0, len(order))
	for _, h := range order {
		g := groupByHash[h]
		sort.Slice(g.Servers, func(i, j int) bool { return g.Servers[i].ServerID < g.Servers[j].ServerID })
		groups = append(groups, *g)
	}
	// 成员数降序（多数派在前）；同数按 sha256 升序稳定，避免顺序抖动。
	sort.SliceStable(groups, func(i, j int) bool {
		if len(groups[i].Servers) != len(groups[j].Servers) {
			return len(groups[i].Servers) > len(groups[j].Servers)
		}
		return groups[i].SHA256 < groups[j].SHA256
	})
	missing := make([]string, 0)
	for i := range candidates {
		if _, ok := present[candidates[i].ID]; !ok {
			missing = append(missing, candidates[i].ServerID)
		}
	}
	sort.Strings(missing)
	return CompareResult{Path: path, Groups: groups, Missing: missing}
}

// rescanTargetRef 组装重扫审计的 targetRef（下发服列表，逗号分隔；无在线服记占位）。
func rescanTargetRef(dispatched []string) string {
	if len(dispatched) == 0 {
		return "-"
	}
	return strings.Join(dispatched, ",")
}

// rescanAuditDetail 组装重扫审计 detail（目标 / 下发 / force，无文件内容，规格 §4.7）。
func rescanAuditDetail(targets, dispatched []string, force bool) string {
	detail, _ := json.Marshal(map[string]any{
		"serverIds":  targets,
		"dispatched": dispatched,
		"force":      force,
	})
	return string(detail)
}
