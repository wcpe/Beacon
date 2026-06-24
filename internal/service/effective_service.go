package service

import (
	"context"
	"sort"
	"time"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/merge"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime/longpoll"
)

// EffectiveItem 是某 dataId 按覆盖链合并后的有效配置。
type EffectiveItem struct {
	DataID  string
	Format  string
	MD5     string
	Content string
}

// Effective 是某 agent 身份的有效配置解析结果（收敛只看 md5，无代际号）。
type Effective struct {
	Namespace string
	ServerID  string
	Group     string
	Zone      string
	MD5       string
	Items     []EffectiveItem
}

// EffectiveService 按 agent 身份解析有效配置（scope 覆盖链键级深合并）+ 长轮询挂起。
type EffectiveService struct {
	configRepo *repository.ConfigItemRepository
	assignRepo *repository.ZoneAssignmentRepository
	grayRepo   *repository.ConfigGrayRepository     // 可选，灰度叠加（FR-9）；nil 即无灰度行为
	revRepo    *repository.ConfigRevisionRepository // 可选，per-server 变更时间线聚合历史版本（FR-80）；纯解析/长轮询路径可传 nil
	hub        *longpoll.Hub
}

// NewEffectiveService 构造服务。hub 仅长轮询用，纯解析场景可传 nil。
// grayRepo 可选：注入则在版本选择层叠加灰度（cohort 内 serverId 换灰度内容，FR-9，见 ADR-0021）；
// 传 nil 则解析行为与无灰度时完全一致（向后兼容）。
// revRepo 可选：注入则支持 per-server 变更时间线（FR-80）；agent 热路径不需要、可传 nil。
func NewEffectiveService(configRepo *repository.ConfigItemRepository, assignRepo *repository.ZoneAssignmentRepository, grayRepo *repository.ConfigGrayRepository, revRepo *repository.ConfigRevisionRepository, hub *longpoll.Hub) *EffectiveService {
	return &EffectiveService{configRepo: configRepo, assignRepo: assignRepo, grayRepo: grayRepo, revRepo: revRepo, hub: hub}
}

// Resolve 解析某 (namespace, serverId) 的有效配置：
// 先按 zone_assignment 得 (group, zone)，未分配则 group=groupHint、zone 为空；再拉四层候选合并。
func (s *EffectiveService) Resolve(ns, serverID, groupHint string) (Effective, error) {
	group, zone := groupHint, ""
	assign, err := s.assignRepo.FindByServer(ns, serverID)
	if err != nil {
		return Effective{}, err
	}
	if assign != nil {
		group, zone = assign.GroupCode, assign.ZoneCode
	}
	return s.resolveLayers(ns, serverID, group, zone)
}

// WaitEffective 长轮询：先注册 waiter 再算 md5（消除注册前发布丢唤醒窗口）。
// md5 与 agentMD5 不同 → 立即返回 (eff, true)；相同 → 挂起，被唤醒后重算比对；超时/断连返回 (_, false)。
func (s *EffectiveService) WaitEffective(ctx context.Context, ns, serverID, groupHint, agentMD5 string, timeout time.Duration) (Effective, bool, error) {
	w := s.hub.Register(ns, serverID)
	defer s.hub.Deregister(w)
	deadline := time.Now().Add(timeout)
	for {
		eff, err := s.Resolve(ns, serverID, groupHint)
		if err != nil {
			return Effective{}, false, err
		}
		if eff.MD5 != agentMD5 {
			return eff, true, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return Effective{}, false, nil
		}
		if !w.Wait(ctx, remaining) {
			return Effective{}, false, nil // 超时或客户端断连
		}
		// 被唤醒 → 循环重跑 Resolve 比对（唤醒即重算）
	}
}

// applyGrayOverlay 在版本选择层叠加灰度（FR-9，见 ADR-0021）：
// 按 ns + 候选项集合一次性取活跃灰度（无 N+1），对"存在灰度且 serverID 在 cohort 内"的候选项，
// 把参与合并的 content 替换为灰度 content。其余层、合并算法、md5 计算全不变——
// 名单外 serverID 解析结果与无灰度时逐字节相同。grayRepo 未注入则直接返回（无灰度行为）。
func (s *EffectiveService) applyGrayOverlay(ns, serverID string, candidates []model.ConfigItem) error {
	if s.grayRepo == nil || len(candidates) == 0 {
		return nil
	}
	itemIDs := make([]uint, 0, len(candidates))
	for i := range candidates {
		itemIDs = append(itemIDs, candidates[i].ID)
	}
	grays, err := s.grayRepo.ListActiveByItemIDs(ns, itemIDs)
	if err != nil {
		return err
	}
	if len(grays) == 0 {
		return nil
	}
	for i := range candidates {
		gray, ok := grays[candidates[i].ID]
		if !ok {
			continue
		}
		cohort, err := decodeCohort(gray.Cohort)
		if err != nil {
			return err
		}
		if cohort[serverID] {
			candidates[i].Content = gray.Content
		}
	}
	return nil
}

// resolveLayers 拉四层候选、叠加灰度、按 dataId 分桶深合并、算单 md5 与整体 md5。
func (s *EffectiveService) resolveLayers(ns, serverID, group, zone string) (Effective, error) {
	candidates, err := s.configRepo.FindEffectiveCandidates(ns, group, zone, serverID)
	if err != nil {
		return Effective{}, err
	}
	if err := s.applyGrayOverlay(ns, serverID, candidates); err != nil {
		return Effective{}, err
	}

	buckets := map[string][]model.ConfigItem{}
	for _, c := range candidates {
		buckets[c.DataID] = append(buckets[c.DataID], c)
	}

	items := make([]EffectiveItem, 0, len(buckets))
	dataIDToMD5 := make(map[string]string, len(buckets))
	for dataID, layers := range buckets {
		merged, format, err := mergeBucket(layers)
		if err != nil {
			return Effective{}, err
		}
		if merged == "" {
			continue // 全空，不贡献
		}
		sum := merge.MD5Hex(merged)
		items = append(items, EffectiveItem{DataID: dataID, Format: format, MD5: sum, Content: merged})
		dataIDToMD5[dataID] = sum
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DataID < items[j].DataID })

	return Effective{
		Namespace: ns, ServerID: serverID, Group: group, Zone: zone,
		MD5: merge.OverallMD5(dataIDToMD5), Items: items,
	}, nil
}

// scopePriority 覆盖层优先级（低→高，高覆盖低）。
func scopePriority(level string) int {
	switch level {
	case model.ScopeGlobal:
		return 0
	case model.ScopeGroup:
		return 1
	case model.ScopeZone:
		return 2
	case model.ScopeServer:
		return 3
	default:
		return -1
	}
}

// mergeBucket 把同一 dataId 的多层按优先级低→高合并，返回有效文本与格式。
func mergeBucket(layers []model.ConfigItem) (string, string, error) {
	sort.SliceStable(layers, func(i, j int) bool {
		return scopePriority(layers[i].ScopeLevel) < scopePriority(layers[j].ScopeLevel)
	})
	contents := make([]string, len(layers))
	for i, l := range layers {
		contents[i] = l.Content
	}
	format := layers[len(layers)-1].Format // 跨层格式一致，取任一
	merged, err := merge.MergeDataID(format, contents)
	if err != nil {
		return "", "", err
	}
	return merged, format, nil
}

// ProvenancedItem 是某 dataId 的有效配置 + 逐键来源（admin 只读预览用）。
type ProvenancedItem struct {
	DataID    string
	Format    string
	MD5       string
	Content   string
	Sources   []merge.KeyProvenance // 每个叶子键的最终来源层
	Deletions []merge.KeyProvenance // 被减量（写 null）删除且最终确实不存在的键
}

// ProvenancedEffective 是某目标的 admin 只读有效配置预览结果（含逐键来源）。
type ProvenancedEffective struct {
	Namespace string
	ServerID  string
	Group     string
	Zone      string
	MD5       string
	Items     []ProvenancedItem
}

// ResolveWithProvenance 解析某目标的有效配置并附逐键来源（admin 只读预览，见 ADR-0013）。
// serverID 非空时优先按 zone_assignment 解出 (group,zone)；未指派则用传入的 groupHint/zoneHint。
// 对同一解析出的 (group,zone)，合并内容与 md5 与 Resolve 一致（provenance 经平行纯函数计算，不改 agent 热路径）。
func (s *EffectiveService) ResolveWithProvenance(ns, serverID, groupHint, zoneHint string) (ProvenancedEffective, error) {
	group, zone := groupHint, zoneHint
	if serverID != "" {
		assign, err := s.assignRepo.FindByServer(ns, serverID)
		if err != nil {
			return ProvenancedEffective{}, err
		}
		if assign != nil {
			group, zone = assign.GroupCode, assign.ZoneCode
		}
	}

	candidates, err := s.configRepo.FindEffectiveCandidates(ns, group, zone, serverID)
	if err != nil {
		return ProvenancedEffective{}, err
	}
	// admin 预览与 agent 热路径共用同一灰度叠加逻辑，保证 cohort 内预览结果与下发一致
	if err := s.applyGrayOverlay(ns, serverID, candidates); err != nil {
		return ProvenancedEffective{}, err
	}

	buckets := map[string][]model.ConfigItem{}
	for _, c := range candidates {
		buckets[c.DataID] = append(buckets[c.DataID], c)
	}

	items := make([]ProvenancedItem, 0, len(buckets))
	dataIDToMD5 := make(map[string]string, len(buckets))
	for dataID, layers := range buckets {
		sort.SliceStable(layers, func(i, j int) bool {
			return scopePriority(layers[i].ScopeLevel) < scopePriority(layers[j].ScopeLevel)
		})
		provLayers := make([]merge.ProvLayer, len(layers))
		for i, l := range layers {
			provLayers[i] = merge.ProvLayer{Scope: l.ScopeLevel, Content: l.Content}
		}
		format := layers[len(layers)-1].Format // 跨层格式一致，取任一
		content, sources, deletions, err := merge.MergeDataIDWithProvenance(format, provLayers)
		if err != nil {
			return ProvenancedEffective{}, err
		}
		if content == "" {
			continue // 全空，不贡献
		}
		sum := merge.MD5Hex(content)
		items = append(items, ProvenancedItem{
			DataID: dataID, Format: format, MD5: sum, Content: content,
			Sources: sources, Deletions: deletions,
		})
		dataIDToMD5[dataID] = sum
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DataID < items[j].DataID })

	return ProvenancedEffective{
		Namespace: ns, ServerID: serverID, Group: group, Zone: zone,
		MD5: merge.OverallMD5(dataIDToMD5), Items: items,
	}, nil
}

// TimelineEntry 是某子服覆盖链上一次配置发布（含首发 / 发布 / 回滚）的元信息（不含内容，FR-80）。
type TimelineEntry struct {
	ConfigItemID uint
	DataID       string
	ScopeLevel   string
	ScopeTarget  string
	Version      int64
	MD5          string
	Operator     string
	Comment      string
	CreatedAt    time.Time
}

// ConfigTimeline 是某子服「有效配置变更时间线」的解析结果（按时间倒序的发布记录，FR-80）。
type ConfigTimeline struct {
	Namespace string
	ServerID  string
	Group     string
	Zone      string
	Entries   []TimelineEntry
}

// ConfigTimeline 解析某 (namespace, serverId) 当前覆盖链涉及的全部 config 项的发布历史，按时间倒序汇总（FR-80）。
// 覆盖链解析与有效配置 Resolve 同口径：按 zone_assignment（DB 权威，ADR-0004）解出 (group, zone)，未指派回退 groupHint / 空。
// 取该链四层候选 config 项后，一次按 itemID 集合拉全部 config_revision（避免 N+1），每条标注其所属项的 scope 元信息。
// 仅给元信息不含 content（要看内容走既有版本历史 / diff）。revRepo 未注入时返回错误（装配缺漏，不静默吞）。
func (s *EffectiveService) ConfigTimeline(ns, serverID, groupHint string) (ConfigTimeline, error) {
	if s.revRepo == nil {
		return ConfigTimeline{}, apperr.ErrInternal
	}
	group, zone := groupHint, ""
	assign, err := s.assignRepo.FindByServer(ns, serverID)
	if err != nil {
		return ConfigTimeline{}, err
	}
	if assign != nil {
		group, zone = assign.GroupCode, assign.ZoneCode
	}

	candidates, err := s.configRepo.FindEffectiveCandidates(ns, group, zone, serverID)
	if err != nil {
		return ConfigTimeline{}, err
	}
	// 建 itemID → scope 元信息映射，并收集 itemID 集合一次性拉历史（无 N+1）。
	type scopeMeta struct {
		dataID, scopeLevel, scopeTarget string
	}
	metaByItem := make(map[uint]scopeMeta, len(candidates))
	itemIDs := make([]uint, 0, len(candidates))
	for i := range candidates {
		c := &candidates[i]
		metaByItem[c.ID] = scopeMeta{dataID: c.DataID, scopeLevel: c.ScopeLevel, scopeTarget: c.ScopeTarget}
		itemIDs = append(itemIDs, c.ID)
	}

	revs, err := s.revRepo.ListByItemIDs(itemIDs)
	if err != nil {
		return ConfigTimeline{}, err
	}
	// revs 已由仓库按 created_at desc 排序；逐条关联其 config 项的 scope 元信息组装条目。
	entries := make([]TimelineEntry, 0, len(revs))
	for i := range revs {
		rev := &revs[i]
		meta := metaByItem[rev.ConfigItemID] // 必命中：revs 由 itemIDs 派生
		entries = append(entries, TimelineEntry{
			ConfigItemID: rev.ConfigItemID,
			DataID:       meta.dataID,
			ScopeLevel:   meta.scopeLevel,
			ScopeTarget:  meta.scopeTarget,
			Version:      rev.Version,
			MD5:          rev.ContentMD5,
			Operator:     rev.Operator,
			Comment:      rev.Comment,
			CreatedAt:    rev.CreatedAt,
		})
	}

	return ConfigTimeline{
		Namespace: ns, ServerID: serverID, Group: group, Zone: zone, Entries: entries,
	}, nil
}
