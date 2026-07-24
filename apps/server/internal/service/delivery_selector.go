package service

import (
	"fmt"
	"net/http"
	"sort"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// selectorCrossNamespace 构造带具体冲突实体的 selector 校验错误（FR-162 跨 namespace 拒绝，code 对齐同族）。
func selectorCrossNamespace(reason string) *apperr.Error {
	return apperr.New(http.StatusBadRequest, "selector_cross_namespace", reason)
}

// deliveryTopology 是解析 selector 所需的 namespace 内拓扑快照（3 次批量查询取齐，禁循环查库）。
type deliveryTopology struct {
	// namespace 内全部 server（含 proxy / 未分配，合格性在解析时过滤）
	servers []model.Server
	// namespace 内合法 zone id 集合
	zoneIDs map[uint]struct{}
	// namespace 内合法 region id → 其下 zone id 列表
	zonesByRegion map[uint][]uint
	// 已确认绑定（status=active）身份的 serverId 集合
	activeServerIDs map[string]struct{}
}

// loadDeliveryTopology 取 namespace 内的区服拓扑与身份绑定事实：
// bc_cluster → region → zone 三级各一次 IN 查询 + server / 身份各一次，共 5 次批量查询。
func loadDeliveryTopology(db *gorm.DB, namespaceID uint) (*deliveryTopology, error) {
	topo := &deliveryTopology{
		zoneIDs:         map[uint]struct{}{},
		zonesByRegion:   map[uint][]uint{},
		activeServerIDs: map[string]struct{}{},
	}
	if err := db.Where("namespace_id = ?", namespaceID).Find(&topo.servers).Error; err != nil {
		return nil, err
	}

	var clusterIDs []uint
	if err := db.Model(&model.BCCluster{}).Where("namespace_id = ?", namespaceID).
		Pluck("id", &clusterIDs).Error; err != nil {
		return nil, err
	}
	var regions []model.Region
	if len(clusterIDs) > 0 {
		if err := db.Where("bc_cluster_id IN ?", clusterIDs).Find(&regions).Error; err != nil {
			return nil, err
		}
	}
	regionIDs := make([]uint, 0, len(regions))
	for _, region := range regions {
		regionIDs = append(regionIDs, region.ID)
		topo.zonesByRegion[region.ID] = nil
	}
	var zones []model.Zone
	if len(regionIDs) > 0 {
		if err := db.Where("region_id IN ?", regionIDs).Find(&zones).Error; err != nil {
			return nil, err
		}
	}
	for _, zone := range zones {
		topo.zoneIDs[zone.ID] = struct{}{}
		topo.zonesByRegion[zone.RegionID] = append(topo.zonesByRegion[zone.RegionID], zone.ID)
	}

	var activeIDs []string
	if err := db.Model(&model.AgentIdentity{}).
		Where("namespace_id = ? AND status = ?", namespaceID, model.AgentIdentityStatusActive).
		Pluck("server_id", &activeIDs).Error; err != nil {
		return nil, err
	}
	for _, id := range activeIDs {
		topo.activeServerIDs[id] = struct{}{}
	}
	return topo, nil
}

// resolveChangeTargets 把 selector 解析为合格目标集（spec §4.3.1，字典序稳定排序）：
//   - all=true 取 namespace 内全部合格目标；否则 regions（展开为小区）∪ zones ∪ servers 的并集，最后减 excludes；
//   - 合格目标 = kind=backend + 身份已确认绑定（active）+ 已分配 zone；模板源自身自动排除；
//   - 引用异 namespace / 不存在的 region / zone / server 直接校验失败（FR-162 跨 namespace 拒绝）。
func resolveChangeTargets(db *gorm.DB, namespaceID uint, selector ChangeSelector, sourceServerID string) ([]model.Server, error) {
	topo, err := loadDeliveryTopology(db, namespaceID)
	if err != nil {
		return nil, err
	}
	pickedZones, err := validateSelectorRefs(topo, selector)
	if err != nil {
		return nil, err
	}
	named := toSet(selector.Servers)
	excluded := toSet(selector.Excludes)

	targets := make([]model.Server, 0, len(topo.servers))
	for _, srv := range topo.servers {
		if !selectorMatches(selector, pickedZones, named, &srv) {
			continue
		}
		if !eligibleChangeTarget(topo, &srv) {
			continue
		}
		if _, skip := excluded[srv.ServerID]; skip || srv.ServerID == sourceServerID {
			continue
		}
		targets = append(targets, srv)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ServerID < targets[j].ServerID })
	return targets, nil
}

// validateSelectorRefs 校验 selector 引用的实体全部存在且属本 namespace，返回 zones ∪ regions 展开后的小区集合。
func validateSelectorRefs(topo *deliveryTopology, selector ChangeSelector) (map[uint]struct{}, error) {
	picked := map[uint]struct{}{}
	for _, regionID := range selector.Regions {
		zoneIDs, ok := topo.zonesByRegion[regionID]
		if !ok {
			return nil, selectorCrossNamespace(fmt.Sprintf("selector 引用的大区 %d 不存在或不属于本环境", regionID))
		}
		for _, zoneID := range zoneIDs {
			picked[zoneID] = struct{}{}
		}
	}
	for _, zoneID := range selector.Zones {
		if _, ok := topo.zoneIDs[zoneID]; !ok {
			return nil, selectorCrossNamespace(fmt.Sprintf("selector 引用的小区 %d 不存在或不属于本环境", zoneID))
		}
		picked[zoneID] = struct{}{}
	}
	known := map[string]struct{}{}
	for _, srv := range topo.servers {
		known[srv.ServerID] = struct{}{}
	}
	for _, serverID := range append(append([]string{}, selector.Servers...), selector.Excludes...) {
		if _, ok := known[serverID]; !ok {
			return nil, selectorCrossNamespace(fmt.Sprintf("selector 引用的服务器 %s 不存在或不属于本环境", serverID))
		}
	}
	return picked, nil
}

// selectorMatches 判断 server 是否落在 selector 的选取范围（all / 小区并集 / 点名）。
func selectorMatches(selector ChangeSelector, pickedZones map[uint]struct{}, named map[string]struct{}, srv *model.Server) bool {
	if selector.All {
		return true
	}
	if srv.ZoneID != nil {
		if _, ok := pickedZones[*srv.ZoneID]; ok {
			return true
		}
	}
	_, ok := named[srv.ServerID]
	return ok
}

// eligibleChangeTarget 判定合格目标：backend + 已分配 zone + 身份已确认绑定（spec §4.3.1）。
func eligibleChangeTarget(topo *deliveryTopology, srv *model.Server) bool {
	if srv.Kind != model.ServerKindBackend || srv.ZoneID == nil {
		return false
	}
	_, bound := topo.activeServerIDs[srv.ServerID]
	return bound
}

// diffComparisonSelector 给差异面（diff-scan / file-diff 预览）用的对照集 selector：
// 组单期 selector 未设（all=false 且无任何选取项）时回退为 all=true（保留 excludes），
// 使向导「先扫差异、后定范围」的既定步序能得到指示性差异（真机首验暴露：未设范围时扫描恒 0 项）；
// 执行期仍按目标本地清单逐服重判（spec §4.2.3）。submit / impact / 启动固化不走本回退，严格按用户所设 selector。
func diffComparisonSelector(selector ChangeSelector) ChangeSelector {
	if selector.All || len(selector.Regions) > 0 || len(selector.Zones) > 0 || len(selector.Servers) > 0 {
		return selector
	}
	return ChangeSelector{All: true, Excludes: selector.Excludes}
}

// toSet 把字符串切片转集合。
func toSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, v := range values {
		set[v] = struct{}{}
	}
	return set
}
