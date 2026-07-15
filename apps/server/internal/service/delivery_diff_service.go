package service

import (
	"context"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
)

// DeliveryDiffService 承载变更单的差异面（FR-162，spec §4.2）：
// 同步差异扫描（diff-scan）、影响预览（impact）、变更项文件内容预览（file-diff）。
// 生命周期迁移归 DeliveryOrderService（职责分离，防上帝类）。
type DeliveryDiffService struct {
	db        *gorm.DB
	repo      *repository.ChangeOrderRepository
	assetRepo *repository.FileAssetRepository
	auditRepo *repository.AuditLogRepository
	preview   *AssetPreviewService
	health    *healthview.Store
}

// NewDeliveryDiffService 构造服务（preview 复用 P8 文件资产安全通道：敏感路径 / 在线校验 / 查看审计）。
func NewDeliveryDiffService(db *gorm.DB, repo *repository.ChangeOrderRepository,
	assetRepo *repository.FileAssetRepository, auditRepo *repository.AuditLogRepository,
	preview *AssetPreviewService, health *healthview.Store) *DeliveryDiffService {
	return &DeliveryDiffService{db: db, repo: repo, assetRepo: assetRepo, auditRepo: auditRepo,
		preview: preview, health: health}
}

// sourceFileStat 是模板源单文件的快照事实（差异计算的源侧输入）。
type sourceFileStat struct {
	sha  string
	size int64
}

// targetPathStat 是某 path 在目标集上的聚合事实（差异计算的目标侧输入）。
type targetPathStat struct {
	// 拥有该 path 的目标数
	present int
	// 是否存在与源 hash 不同的目标
	differs bool
}

// DiffScan 同步差异扫描（POST .../diff-scan，spec §4.2.1 决策：不下发重扫、不设异步任务态）：
// 读文件资产最新快照算模板源 vs 目标集差异，整组替换 file_diff 项（默认全部差异项入单）并更新 diff_snapshot_at。
func (s *DeliveryDiffService) DiffScan(orderID uint, operator, clientIP string) (*DiffScanView, error) {
	order, err := requireChangeOrder(s.repo, orderID)
	if err != nil {
		return nil, err
	}
	if order.Status != model.ChangeOrderStatusDraft {
		return nil, changeIllegalState(order.Status, "重扫差异")
	}
	if order.SourceServerID == "" {
		return nil, apperr.ErrChangeSourceMissing
	}
	srcFiles, snapshotAt, err := s.loadSourceFiles(order)
	if err != nil {
		return nil, err
	}
	targets, err := resolveChangeTargets(s.db, order.NamespaceID,
		diffComparisonSelector(decodeSelector(order.Selector)), order.SourceServerID)
	if err != nil {
		return nil, err
	}
	stats, err := s.loadTargetStats(targets, order.ScanDir, srcFiles)
	if err != nil {
		return nil, err
	}
	items := computeChangeDiffItems(order.ID, srcFiles, stats, len(targets))
	if err := s.persistDiffScan(order, items, snapshotAt, operator, clientIP); err != nil {
		return nil, err
	}
	return &DiffScanView{Status: "done", DiffSnapshotAt: &snapshotAt, Items: changeOrderItemViews(items)}, nil
}

// loadSourceFiles 取模板源在 scan_dir 范围内的快照文件表与快照时间。
// 模板源从未上报过清单（无扫描概要行）直接拒绝——防把「源未上报」误判成「源为空」而生成全删差异。
func (s *DeliveryDiffService) loadSourceFiles(order *model.ChangeOrder) (map[string]sourceFileStat, time.Time, error) {
	srcRow, err := findServerByRef(s.db, order.NamespaceID, order.SourceServerID)
	if err != nil {
		return nil, time.Time{}, err
	}
	if srcRow == nil || srcRow.Kind != model.ServerKindBackend {
		return nil, time.Time{}, apperr.ErrChangeSourceInvalid
	}
	var scan model.FileAssetScan
	if e := s.db.Where("server_id = ?", srcRow.ID).First(&scan).Error; e != nil {
		return nil, time.Time{}, apperr.ErrChangeSourceSnapshotMissing
	}
	rows, err := s.assetRepo.ListByServersWithPrefix([]uint{srcRow.ID}, order.ScanDir)
	if err != nil {
		return nil, time.Time{}, err
	}
	files := make(map[string]sourceFileStat, len(rows))
	for _, row := range rows {
		files[row.Path] = sourceFileStat{sha: row.SHA256, size: row.Size}
	}
	return files, scan.ScannedAt.UTC(), nil
}

// loadTargetStats 分块（≤100 服 / 次）取目标集在 scan_dir 范围内的清单并聚合逐 path 事实（防 N+1 与超大 IN）。
func (s *DeliveryDiffService) loadTargetStats(targets []model.Server, scanDir string,
	srcFiles map[string]sourceFileStat) (map[string]*targetPathStat, error) {
	stats := map[string]*targetPathStat{}
	for start := 0; start < len(targets); start += changeOrderDiffChunkServers {
		end := min(start+changeOrderDiffChunkServers, len(targets))
		ids := make([]uint, 0, end-start)
		for _, srv := range targets[start:end] {
			ids = append(ids, srv.ID)
		}
		rows, err := s.assetRepo.ListByServersWithPrefix(ids, scanDir)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			stat := stats[row.Path]
			if stat == nil {
				stat = &targetPathStat{}
				stats[row.Path] = stat
			}
			stat.present++
			if src, ok := srcFiles[row.Path]; ok && src.sha != row.SHA256 {
				stat.differs = true
			}
		}
	}
	return stats, nil
}

// computeChangeDiffItems 按目标集并集语义算差异项（已拍板决策 2）：
// add = 源有且 ≥1 目标缺；update = 双方有且 ≥1 目标 hash 异（同 path 命中 add/update 时取 update）；
// delete = 源无且 ≥1 目标有。全部目标同 hash 的 path 不入单；结果按 path 字典序稳定排序。
func computeChangeDiffItems(orderID uint, srcFiles map[string]sourceFileStat,
	stats map[string]*targetPathStat, targetCount int) []model.ChangeOrderItem {
	items := make([]model.ChangeOrderItem, 0, len(srcFiles))
	for _, path := range sortedPathKeys(srcFiles) {
		src := srcFiles[path]
		stat := stats[path]
		present, differs := 0, false
		if stat != nil {
			present, differs = stat.present, stat.differs
		}
		action := ""
		switch {
		case differs:
			action = model.ChangeItemActionUpdate
		case present < targetCount:
			action = model.ChangeItemActionAdd
		default:
			continue // 全部目标与源同 hash：相同跳过，不入单
		}
		items = append(items, newFileDiffItem(orderID, path, action, &src))
	}
	for _, path := range sortedPathKeys(stats) {
		if _, inSource := srcFiles[path]; !inSource {
			items = append(items, newFileDiffItem(orderID, path, model.ChangeItemActionDelete, nil))
		}
	}
	return items
}

// newFileDiffItem 构造文件差异项；delete 项无源侧哈希 / 字节数（spec §3.2）。
func newFileDiffItem(orderID uint, path, action string, src *sourceFileStat) model.ChangeOrderItem {
	item := model.ChangeOrderItem{
		OrderID: orderID, Kind: model.ChangeItemKindFileDiff,
		Path: &path, Action: &action,
	}
	if src != nil {
		sha := src.sha
		size := src.size
		item.SHA256 = &sha
		item.SizeBytes = &size
	}
	return item
}

// persistDiffScan 在事务内整组替换 file_diff 项 + 更新快照时间 + 专项审计（config_change 项保留）。
func (s *DeliveryDiffService) persistDiffScan(order *model.ChangeOrder, items []model.ChangeOrderItem,
	snapshotAt time.Time, operator, clientIP string) error {
	nsCode, err := changeNamespaceCode(s.db, order.NamespaceID)
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		repoTx := s.repo.WithTx(tx)
		if e := repoTx.DeleteItemsByKind(order.ID, model.ChangeItemKindFileDiff); e != nil {
			return e
		}
		if e := repoTx.CreateItems(items); e != nil {
			return e
		}
		ok, e := repoTx.UpdateStatusCAS(order.ID, []string{model.ChangeOrderStatusDraft},
			map[string]any{"status": model.ChangeOrderStatusDraft, "diff_snapshot_at": snapshotAt})
		if e != nil {
			return e
		}
		if !ok {
			return changeIllegalState(order.Status, "重扫差异")
		}
		return writeChangeOrderAudit(tx, s.auditRepo, nsCode, operator, clientIP,
			model.ActionDeliveryOrderUpdate, order.ID,
			map[string]any{"orderId": order.ID, "diffScan": true, "fileItems": len(items)})
	})
}

// Impact 影响预览（GET .../impact，spec §4.2.2）：汇总（批次划分 / 传输量估算）+ 逐目标分页
// （在线 / 健康、新增 / 覆盖 / 删除 / 相同跳过现算、命中的配置作用域 from→to）。仅现算当前页目标，防全量扫。
func (s *DeliveryDiffService) Impact(orderID uint, page, size int) (*ChangeImpactView, error) {
	order, err := requireChangeOrder(s.repo, orderID)
	if err != nil {
		return nil, err
	}
	items, err := s.repo.ListItems(order.ID)
	if err != nil {
		return nil, err
	}
	targetIDs, err := s.impactTargetIDs(order)
	if err != nil {
		return nil, err
	}
	page, size = normalizeChangePage(page, size, changeOrderListPageDefault)
	pageServers, err := s.pageTargetServers(order.NamespaceID, targetIDs, page, size)
	if err != nil {
		return nil, err
	}
	rows, err := s.impactTargetRows(order, items, pageServers)
	if err != nil {
		return nil, err
	}
	return &ChangeImpactView{
		Summary: impactSummary(order, items, targetIDs),
		Targets: ChangeImpactTargetsPageView{Items: rows, Total: int64(len(targetIDs))},
	}, nil
}

// impactTargetIDs 取影响目标集：启动后以固化 change_target 快照为准，未启动按 selector 现解析（字典序）。
func (s *DeliveryDiffService) impactTargetIDs(order *model.ChangeOrder) ([]string, error) {
	frozen, err := s.repo.ListTargetServerIDs(order.ID)
	if err != nil {
		return nil, err
	}
	if len(frozen) > 0 {
		return frozen, nil
	}
	targets, err := resolveChangeTargets(s.db, order.NamespaceID, decodeSelector(order.Selector), order.SourceServerID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(targets))
	for _, srv := range targets {
		ids = append(ids, srv.ServerID)
	}
	return ids, nil
}

// pageTargetServers 切当前页并取页内目标的 server 行（字典序稳定分页）。
func (s *DeliveryDiffService) pageTargetServers(namespaceID uint, targetIDs []string, page, size int) ([]model.Server, error) {
	start := (page - 1) * size
	if start >= len(targetIDs) {
		return []model.Server{}, nil
	}
	pageIDs := targetIDs[start:min(start+size, len(targetIDs))]
	var servers []model.Server
	if err := s.db.Where("namespace_id = ? AND server_id IN ?", namespaceID, pageIDs).
		Order("server_id ASC").Find(&servers).Error; err != nil {
		return nil, err
	}
	return servers, nil
}

// impactSummary 组装汇总：目标总数、批次划分预览、文件总数 / 总字节、去重传输字节（唯一 sha256）、配置作用域数、快照时间。
func impactSummary(order *model.ChangeOrder, items []model.ChangeOrderItem, targetIDs []string) ChangeImpactSummaryView {
	summary := ChangeImpactSummaryView{
		TargetTotal: len(targetIDs),
		Batches:     planImpactBatches(order.BatchMode, decodeBatchSizes(order.BatchSizes), len(targetIDs)),
		SnapshotAt:  order.DiffSnapshotAt,
	}
	seenSHA := map[string]struct{}{}
	for i := range items {
		item := &items[i]
		if item.Kind == model.ChangeItemKindConfigChange {
			summary.ConfigScopeCount++
			continue
		}
		summary.FileTotal++
		if item.SizeBytes != nil {
			summary.TotalBytes += *item.SizeBytes
		}
		if item.SHA256 != nil && item.SizeBytes != nil {
			if _, dup := seenSHA[*item.SHA256]; !dup {
				seenSHA[*item.SHA256] = struct{}{}
				summary.TransferBytes += *item.SizeBytes
			}
		}
	}
	return summary
}

// planImpactBatches 批次划分预览（spec §4.4.1）：percent 逐批向上取整、count 逐批固定台数，
// 均不超过剩余；剩余全部进末批。同输入必同输出（可复现）。
func planImpactBatches(mode string, sizes []int, targetTotal int) []ChangeImpactBatchView {
	result := make([]ChangeImpactBatchView, 0, len(sizes)+1)
	remaining := targetTotal
	for _, size := range sizes {
		if remaining <= 0 {
			break
		}
		raw := size
		if mode == model.BatchModePercent {
			raw = (targetTotal*size + 99) / 100
		}
		count := min(raw, remaining)
		if count <= 0 {
			continue
		}
		remaining -= count
		result = append(result, ChangeImpactBatchView{BatchNo: len(result) + 1, Count: count})
	}
	if remaining > 0 {
		result = append(result, ChangeImpactBatchView{BatchNo: len(result) + 1, Count: remaining})
	}
	return result
}

// impactTargetRows 组装当前页逐目标行：在线 / 健康读内存真源，文件四计数按「源 vs 该目标」现算，配置命中按作用域链判定。
func (s *DeliveryDiffService) impactTargetRows(order *model.ChangeOrder, items []model.ChangeOrderItem,
	pageServers []model.Server) ([]ChangeImpactTargetView, error) {
	srcFiles, targetFiles, err := s.impactFileFacts(order, items, pageServers)
	if err != nil {
		return nil, err
	}
	configScopes, err := s.impactConfigScopes(order, items, pageServers)
	if err != nil {
		return nil, err
	}
	rows := make([]ChangeImpactTargetView, 0, len(pageServers))
	for i := range pageServers {
		srv := &pageServers[i]
		row := ChangeImpactTargetView{ServerID: srv.ServerID, ConfigScopes: configScopes[srv.ServerID]}
		if row.ConfigScopes == nil {
			row.ConfigScopes = []ChangeImpactConfigScopeView{}
		}
		row.Online, row.Level = s.targetHealth(order.NamespaceID, srv.ServerID)
		fillImpactFileCounts(&row, srcFiles, targetFiles[srv.ID])
		rows = append(rows, row)
	}
	return rows, nil
}

// targetHealth 读健康视图内存真源：在线 = 视图存在且未失联；无视图时等级标 unknown。
func (s *DeliveryDiffService) targetHealth(namespaceID uint, serverID string) (bool, string) {
	view, ok := s.health.Get(namespaceID, serverID)
	if !ok {
		return false, "unknown"
	}
	return !containsReason(view.Reasons, healthview.ReasonLost), view.Level
}

// impactFileFacts 取源快照文件表与当前页目标的清单（无文件项 / 纯配置单直接空表，不查资产）。
func (s *DeliveryDiffService) impactFileFacts(order *model.ChangeOrder, items []model.ChangeOrderItem,
	pageServers []model.Server) (map[string]sourceFileStat, map[uint]map[string]string, error) {
	if order.SourceServerID == "" || !hasFileDiffItem(items) {
		return map[string]sourceFileStat{}, map[uint]map[string]string{}, nil
	}
	srcFiles, _, err := s.loadSourceFiles(order)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]uint, 0, len(pageServers))
	for _, srv := range pageServers {
		ids = append(ids, srv.ID)
	}
	rows, err := s.assetRepo.ListByServersWithPrefix(ids, order.ScanDir)
	if err != nil {
		return nil, nil, err
	}
	targetFiles := make(map[uint]map[string]string, len(pageServers))
	for _, row := range rows {
		if targetFiles[row.ServerID] == nil {
			targetFiles[row.ServerID] = map[string]string{}
		}
		targetFiles[row.ServerID][row.Path] = row.SHA256
	}
	return srcFiles, targetFiles, nil
}

// fillImpactFileCounts 按「源 vs 该目标」现算四计数：新增 / 覆盖 / 删除 / 相同跳过（spec §4.2.2）。
func fillImpactFileCounts(row *ChangeImpactTargetView, srcFiles map[string]sourceFileStat, targetShaByPath map[string]string) {
	for path, src := range srcFiles {
		sha, ok := targetShaByPath[path]
		switch {
		case !ok:
			row.AddCount++
		case sha != src.sha:
			row.UpdateCount++
		default:
			row.SkipCount++
		}
	}
	for path := range targetShaByPath {
		if _, ok := srcFiles[path]; !ok {
			row.DeleteCount++
		}
	}
}

// hasFileDiffItem 判断项集内是否存在文件差异项。
func hasFileDiffItem(items []model.ChangeOrderItem) bool {
	for i := range items {
		if items[i].Kind == model.ChangeItemKindFileDiff {
			return true
		}
	}
	return false
}

// impactConfigScopes 计算当前页各目标命中的配置作用域（spec §4.2.2）：
// 命中 = 目标在该作用域覆盖范围内（作用域链语义与配置域 resolveTargetChain 一致，按页批量建链防 N+1）。
func (s *DeliveryDiffService) impactConfigScopes(order *model.ChangeOrder, items []model.ChangeOrderItem,
	pageServers []model.Server) (map[string][]ChangeImpactConfigScopeView, error) {
	configItems := make([]model.ChangeOrderItem, 0, len(items))
	for i := range items {
		if items[i].Kind == model.ChangeItemKindConfigChange {
			configItems = append(configItems, items[i])
		}
	}
	result := map[string][]ChangeImpactConfigScopeView{}
	if len(configItems) == 0 || len(pageServers) == 0 {
		return result, nil
	}
	chains, err := buildServerScopeChains(s.db, order.NamespaceID, pageServers)
	if err != nil {
		return nil, err
	}
	for i := range pageServers {
		srv := &pageServers[i]
		for j := range configItems {
			item := &configItems[j]
			ref := configScopeRef{Level: *item.ConfigScopeKind, RefID: *item.ConfigScopeID}
			if _, hit := chains[srv.ServerID][ref]; hit {
				result[srv.ServerID] = append(result[srv.ServerID], ChangeImpactConfigScopeView{
					ScopeKind: *item.ConfigScopeKind, ScopeID: *item.ConfigScopeID,
					FromVersionID: item.ConfigFromVersionID, ToVersionID: item.ConfigToVersionID,
				})
			}
		}
	}
	return result, nil
}

// buildServerScopeChains 按页批量构造各目标的作用域链集合（namespace→bc_cluster→region→zone→server，
// 未分配 zone 仅 namespace+server 两层；与 config_center_scope.resolveTargetChain 语义一致，3 次 IN 查询建链）。
func buildServerScopeChains(db *gorm.DB, namespaceID uint, servers []model.Server) (map[string]map[configScopeRef]struct{}, error) {
	zoneIDs := make([]uint, 0, len(servers))
	for i := range servers {
		if servers[i].ZoneID != nil {
			zoneIDs = append(zoneIDs, *servers[i].ZoneID)
		}
	}
	zoneByID, regionByID, err := loadZoneRegionIndex(db, zoneIDs)
	if err != nil {
		return nil, err
	}
	chains := make(map[string]map[configScopeRef]struct{}, len(servers))
	for i := range servers {
		srv := &servers[i]
		chain := map[configScopeRef]struct{}{
			{Level: model.ConfigScopeNamespace, RefID: namespaceID}: {},
			{Level: model.ConfigScopeServer, RefID: srv.ID}:         {},
		}
		if srv.ZoneID != nil {
			if zone, ok := zoneByID[*srv.ZoneID]; ok {
				chain[configScopeRef{Level: model.ConfigScopeZone, RefID: zone.ID}] = struct{}{}
				if region, ok := regionByID[zone.RegionID]; ok {
					chain[configScopeRef{Level: model.ConfigScopeRegion, RefID: region.ID}] = struct{}{}
					chain[configScopeRef{Level: model.ConfigScopeBCCluster, RefID: region.BCClusterID}] = struct{}{}
				}
			}
		}
		chains[srv.ServerID] = chain
	}
	return chains, nil
}

// loadZoneRegionIndex 批量取 zone 与其 region 行的索引（2 次 IN 查询）。
func loadZoneRegionIndex(db *gorm.DB, zoneIDs []uint) (map[uint]model.Zone, map[uint]model.Region, error) {
	zoneByID := map[uint]model.Zone{}
	regionByID := map[uint]model.Region{}
	if len(zoneIDs) == 0 {
		return zoneByID, regionByID, nil
	}
	var zones []model.Zone
	if err := db.Where("id IN ?", zoneIDs).Find(&zones).Error; err != nil {
		return nil, nil, err
	}
	regionIDs := make([]uint, 0, len(zones))
	for _, zone := range zones {
		zoneByID[zone.ID] = zone
		regionIDs = append(regionIDs, zone.RegionID)
	}
	var regions []model.Region
	if len(regionIDs) > 0 {
		if err := db.Where("id IN ?", regionIDs).Find(&regions).Error; err != nil {
			return nil, nil, err
		}
	}
	for _, region := range regions {
		regionByID[region.ID] = region
	}
	return zoneByID, regionByID, nil
}

// FileDiff 变更项文件内容预览（GET .../items/{itemId}/file-diff，spec §5.1 正式契约）：
// after = 模板源内容、before = 目标内容；serverId 缺省取字典序第一个与源存在差异的目标；
// 复用文件资产安全通道（敏感路径 403 / agent 离线 504 / 查看审计）；is_text=false 直接 binary、不取内容。
func (s *DeliveryDiffService) FileDiff(ctx context.Context, orderID, itemID uint,
	serverIDParam, reason, operator, clientIP string) (*ChangeFileDiffView, error) {
	order, item, err := s.requireFileDiffItem(orderID, itemID)
	if err != nil {
		return nil, err
	}
	facts, err := s.loadFileDiffFacts(order, item, serverIDParam)
	if err != nil {
		return nil, err
	}
	view := &ChangeFileDiffView{Path: facts.path, ChangeType: facts.changeType, ServerID: facts.chosenServerID()}
	if facts.binaryByManifest() {
		view.Binary = true
		return view, nil
	}
	if err := s.fetchFileDiffContents(ctx, facts, view, reason, operator, clientIP); err != nil {
		return nil, err
	}
	return view, nil
}

// requireFileDiffItem 取单与文件差异项（非 file_diff / 缺 path / 缺 action 一律视为不存在）。
func (s *DeliveryDiffService) requireFileDiffItem(orderID, itemID uint) (*model.ChangeOrder, *model.ChangeOrderItem, error) {
	order, err := requireChangeOrder(s.repo, orderID)
	if err != nil {
		return nil, nil, err
	}
	item, err := s.repo.FindItem(orderID, itemID)
	if err != nil {
		return nil, nil, err
	}
	if item == nil || item.Kind != model.ChangeItemKindFileDiff || item.Path == nil || item.Action == nil {
		return nil, nil, apperr.ErrChangeItemNotFound
	}
	return order, item, nil
}

// fileDiffFacts 是一次 file-diff 的定位事实：源 / 目标清单行与选定目标。
type fileDiffFacts struct {
	path        string
	action      string
	changeType  string
	sourceID    string           // 模板源 serverId（文件项必有源）
	srcAsset    *model.FileAsset // 源侧清单行（源已无此文件为 nil）
	chosen      string           // 实际所用目标 serverId（无可用目标为空串）
	chosenAsset *model.FileAsset // 选定目标的清单行（目标无此文件为 nil）
}

// chosenServerID 返回响应用的目标 serverId（空串 → null）。
func (f *fileDiffFacts) chosenServerID() *string { return nilIfEmpty(f.chosen) }

// binaryByManifest 按清单 is_text 提示判二进制：涉及内容的任一侧非文本即不取内容（决策 4）。
func (f *fileDiffFacts) binaryByManifest() bool {
	if f.action != model.ChangeItemActionDelete && f.srcAsset != nil && !f.srcAsset.IsText {
		return true
	}
	if f.action != model.ChangeItemActionAdd && f.chosenAsset != nil && !f.chosenAsset.IsText {
		return true
	}
	return false
}

// loadFileDiffFacts 解析目标集、定位源 / 目标清单行并选定实际所用目标。
func (s *DeliveryDiffService) loadFileDiffFacts(order *model.ChangeOrder, item *model.ChangeOrderItem,
	serverIDParam string) (*fileDiffFacts, error) {
	if order.SourceServerID == "" {
		return nil, apperr.ErrChangeSourceMissing
	}
	facts := &fileDiffFacts{
		path: *item.Path, action: *item.Action,
		changeType: changeTypeOfAction(*item.Action), sourceID: order.SourceServerID,
	}
	srcRow, err := findServerByRef(s.db, order.NamespaceID, order.SourceServerID)
	if err != nil {
		return nil, err
	}
	if srcRow == nil {
		return nil, apperr.ErrChangeSourceInvalid
	}
	if facts.srcAsset, err = s.assetRepo.FindByServerPath(srcRow.ID, facts.path); err != nil {
		return nil, err
	}
	targets, err := s.fileDiffTargets(order)
	if err != nil {
		return nil, err
	}
	return facts, s.chooseFileDiffTarget(order, facts, targets, serverIDParam)
}

// fileDiffTargets 取 file-diff 的候选目标行：启动后用固化目标快照，未启动按 selector 现解析（字典序）。
func (s *DeliveryDiffService) fileDiffTargets(order *model.ChangeOrder) ([]model.Server, error) {
	frozen, err := s.repo.ListTargetServerIDs(order.ID)
	if err != nil {
		return nil, err
	}
	if len(frozen) == 0 {
		return resolveChangeTargets(s.db, order.NamespaceID,
			diffComparisonSelector(decodeSelector(order.Selector)), order.SourceServerID)
	}
	var servers []model.Server
	if err := s.db.Where("namespace_id = ? AND server_id IN ?", order.NamespaceID, frozen).
		Order("server_id ASC").Find(&servers).Error; err != nil {
		return nil, err
	}
	return servers, nil
}

// chooseFileDiffTarget 选定对比目标：显式 serverId 须在目标集内；缺省按字典序取第一个与源存在差异的目标
// （add=缺该文件 / update=hash 异 / delete=仍有该文件），无差异目标退回首个目标（决策 4）。
func (s *DeliveryDiffService) chooseFileDiffTarget(order *model.ChangeOrder, facts *fileDiffFacts,
	targets []model.Server, serverIDParam string) error {
	assetByRow, err := s.targetAssetsForPath(order.NamespaceID, facts.path, targets)
	if err != nil {
		return err
	}
	if serverIDParam != "" {
		for i := range targets {
			if targets[i].ServerID == serverIDParam {
				facts.chosen, facts.chosenAsset = serverIDParam, assetByRow[targets[i].ID]
				return nil
			}
		}
		return changeInvalidParam("serverId 不在本单目标集内")
	}
	for i := range targets {
		asset := assetByRow[targets[i].ID]
		if fileDiffTargetDiffers(facts, asset) {
			facts.chosen, facts.chosenAsset = targets[i].ServerID, asset
			return nil
		}
	}
	if len(targets) > 0 {
		facts.chosen, facts.chosenAsset = targets[0].ServerID, assetByRow[targets[0].ID]
	}
	return nil
}

// fileDiffTargetDiffers 判断某目标对该项是否呈现差异（按 action 语义）。
func fileDiffTargetDiffers(facts *fileDiffFacts, asset *model.FileAsset) bool {
	switch facts.action {
	case model.ChangeItemActionAdd:
		return asset == nil
	case model.ChangeItemActionDelete:
		return asset != nil
	default: // update：目标缺失或 hash 与源不同均算差异
		return asset == nil || facts.srcAsset == nil || asset.SHA256 != facts.srcAsset.SHA256
	}
}

// targetAssetsForPath 批量取目标集中该 path 的清单行（server 行 id → 行）。
func (s *DeliveryDiffService) targetAssetsForPath(namespaceID uint, path string,
	targets []model.Server) (map[uint]*model.FileAsset, error) {
	ids := make([]uint, 0, len(targets))
	for i := range targets {
		ids = append(ids, targets[i].ID)
	}
	rows, err := s.assetRepo.FindByNamespacePathServers(namespaceID, path, ids)
	if err != nil {
		return nil, err
	}
	byRow := make(map[uint]*model.FileAsset, len(rows))
	for i := range rows {
		byRow[rows[i].ServerID] = &rows[i]
	}
	return byRow, nil
}

// fetchFileDiffContents 经文件资产安全通道现取两侧内容（敏感 / 在线 / 审计由 Preview 统一执行）：
// after = 源内容（delete 项为 null）、before = 目标内容（add 项或目标已无此文件为 null）。
func (s *DeliveryDiffService) fetchFileDiffContents(ctx context.Context, facts *fileDiffFacts,
	view *ChangeFileDiffView, reason, operator, clientIP string) error {
	if facts.action != model.ChangeItemActionDelete {
		if facts.srcAsset == nil {
			return apperr.ErrAssetNotFound // 源快照已无此文件（组单后漂移），如实报缺
		}
		result, err := s.preview.Preview(ctx, PreviewParams{
			ServerID: facts.sourceID, Path: facts.path, Reason: reason, Operator: operator, ClientIP: clientIP,
		})
		if err != nil {
			return err
		}
		if result.Binary {
			view.Binary = true
			view.Before = nil
			return nil
		}
		view.After = result.Content
		view.Truncated = view.Truncated || result.Truncated
	}
	if facts.action != model.ChangeItemActionAdd && facts.chosen != "" && facts.chosenAsset != nil {
		result, err := s.preview.Preview(ctx, PreviewParams{
			ServerID: facts.chosen, Path: facts.path, Reason: reason, Operator: operator, ClientIP: clientIP,
		})
		if err != nil {
			return err
		}
		if result.Binary {
			view.Binary = true
			view.Before, view.After = nil, nil
			return nil
		}
		view.Before = result.Content
		view.Truncated = view.Truncated || result.Truncated
	}
	return nil
}

// changeTypeOfAction 把变更项 action 映射为 file-diff 契约的 changeType。
func changeTypeOfAction(action string) string {
	switch action {
	case model.ChangeItemActionAdd:
		return "added"
	case model.ChangeItemActionDelete:
		return "removed"
	default:
		return "modified"
	}
}

// sortedPathKeys 返回 map 键的字典序切片（差异结果稳定排序用；泛型以适配源表 / 目标聚合两种值型）。
func sortedPathKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
