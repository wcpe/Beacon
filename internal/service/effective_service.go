package service

import (
	"sort"

	"beacon/internal/merge"
	"beacon/internal/model"
	"beacon/internal/repository"
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

// EffectiveService 按 agent 身份解析有效配置（scope 覆盖链键级深合并）。
type EffectiveService struct {
	configRepo *repository.ConfigItemRepository
	assignRepo *repository.ZoneAssignmentRepository
}

// NewEffectiveService 构造服务。
func NewEffectiveService(configRepo *repository.ConfigItemRepository, assignRepo *repository.ZoneAssignmentRepository) *EffectiveService {
	return &EffectiveService{configRepo: configRepo, assignRepo: assignRepo}
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

// Resolve 解析某 (namespace, serverId) 的有效配置：
// 按 zone_assignment 回填 (group, zone) → 拉四层候选 → 按 dataId 分桶深合并 → 算整体 md5。
func (s *EffectiveService) Resolve(ns, serverID string) (Effective, error) {
	group, zone := "", ""
	assign, err := s.assignRepo.FindByServer(ns, serverID)
	if err != nil {
		return Effective{}, err
	}
	if assign != nil {
		group, zone = assign.GroupCode, assign.ZoneCode
	}

	candidates, err := s.configRepo.FindEffectiveCandidates(ns, group, zone, serverID)
	if err != nil {
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
		Namespace: ns,
		ServerID:  serverID,
		Group:     group,
		Zone:      zone,
		MD5:       merge.OverallMD5(dataIDToMD5),
		Items:     items,
	}, nil
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
