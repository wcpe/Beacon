package service

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// ServerView 是 server 资产的富化视图（camelCase + 计算字段），对齐前端 zone-authority mock 的 ServerItem。
// 名称字段沿 zone→region→bc_cluster 链取；online 为占位口径：存在 active 身份即视为在线
// （P4 健康域接入前无注册表 / 心跳真源，故以身份 active 近似「已接入」）。
type ServerView struct {
	ID              uint      `json:"id"`
	NamespaceID     uint      `json:"namespaceId"`
	ServerID        string    `json:"serverId"`
	Kind            string    `json:"kind"`
	BCClusterID     *uint     `json:"bcClusterId"`
	BCClusterName   *string   `json:"bcClusterName"`
	ZoneID          *uint     `json:"zoneId"`
	ZoneName        *string   `json:"zoneName"`
	RegionName      *string   `json:"regionName"`
	PendingZoneID   *uint     `json:"pendingZoneId"`
	PendingZoneName *string   `json:"pendingZoneName"`
	IsDefaultEntry  bool      `json:"isDefaultEntry"`
	Draining        bool      `json:"draining"`
	Online          bool      `json:"online"`
	Assigned        bool      `json:"assigned"`
	CreatedAt       time.Time `json:"createdAt"`
}

// onlineKey 唯一定位一台 server（namespace 内 serverId 唯一）。
type onlineKey struct {
	namespaceID uint
	serverID    string
}

// isServerAssigned 判断 server 是否已分配（backend 看 zone_id、proxy 看 bc_cluster_id）。
func isServerAssigned(s *model.Server) bool {
	if s.Kind == model.ServerKindBackend {
		return s.ZoneID != nil
	}
	return s.BCClusterID != nil
}

// enrichServers 把 server 行批量富化为视图：一次性批量取名与在线态，禁循环内查库（N+1）。
func enrichServers(db *gorm.DB, servers []model.Server) ([]ServerView, error) {
	if len(servers) == 0 {
		return []ServerView{}, nil
	}
	zoneIDs, bcIDs, serverIDs := collectServerRefs(servers)
	zoneByID, regionNameByID, err := loadZoneRegionNames(db, zoneIDs)
	if err != nil {
		return nil, err
	}
	bcNameByID, err := loadBCClusterNames(db, bcIDs)
	if err != nil {
		return nil, err
	}
	onlineKeys, err := loadOnlineServerKeys(db, serverIDs)
	if err != nil {
		return nil, err
	}
	views := make([]ServerView, 0, len(servers))
	for i := range servers {
		views = append(views, buildServerView(&servers[i], zoneByID, regionNameByID, bcNameByID, onlineKeys))
	}
	return views, nil
}

// collectServerRefs 收集需批量查名的 zone / bc_cluster id 与 serverId 去重集合。
func collectServerRefs(servers []model.Server) (zoneIDs, bcIDs []uint, serverIDs []string) {
	zoneSet := map[uint]struct{}{}
	bcSet := map[uint]struct{}{}
	serverSet := map[string]struct{}{}
	for i := range servers {
		addUintPtr(zoneSet, servers[i].ZoneID)
		addUintPtr(zoneSet, servers[i].PendingZoneID)
		addUintPtr(bcSet, servers[i].BCClusterID)
		serverSet[servers[i].ServerID] = struct{}{}
	}
	return uintKeys(zoneSet), uintKeys(bcSet), stringKeys(serverSet)
}

// loadZoneRegionNames 批量取 zone 行与其所属 region 名。
func loadZoneRegionNames(db *gorm.DB, zoneIDs []uint) (map[uint]model.Zone, map[uint]string, error) {
	zoneByID := map[uint]model.Zone{}
	regionNameByID := map[uint]string{}
	if len(zoneIDs) == 0 {
		return zoneByID, regionNameByID, nil
	}
	var zones []model.Zone
	if err := db.Where("id IN ?", zoneIDs).Find(&zones).Error; err != nil {
		return nil, nil, err
	}
	regionSet := map[uint]struct{}{}
	for i := range zones {
		zoneByID[zones[i].ID] = zones[i]
		regionSet[zones[i].RegionID] = struct{}{}
	}
	if len(regionSet) == 0 {
		return zoneByID, regionNameByID, nil
	}
	var regions []model.Region
	if err := db.Where("id IN ?", uintKeys(regionSet)).Find(&regions).Error; err != nil {
		return nil, nil, err
	}
	for i := range regions {
		regionNameByID[regions[i].ID] = regions[i].Name
	}
	return zoneByID, regionNameByID, nil
}

// loadBCClusterNames 批量取 bc_cluster 名。
func loadBCClusterNames(db *gorm.DB, bcIDs []uint) (map[uint]string, error) {
	byID := map[uint]string{}
	if len(bcIDs) == 0 {
		return byID, nil
	}
	var clusters []model.BCCluster
	if err := db.Where("id IN ?", bcIDs).Find(&clusters).Error; err != nil {
		return nil, err
	}
	for i := range clusters {
		byID[clusters[i].ID] = clusters[i].Name
	}
	return byID, nil
}

// loadOnlineServerKeys 批量取 active 身份对应的 (namespace, serverId) 键集合，作为在线占位口径。
func loadOnlineServerKeys(db *gorm.DB, serverIDs []string) (map[onlineKey]struct{}, error) {
	keys := map[onlineKey]struct{}{}
	if len(serverIDs) == 0 {
		return keys, nil
	}
	var idents []model.AgentIdentity
	if err := db.Select("namespace_id", "server_id").
		Where("server_id IN ? AND status = ?", serverIDs, model.AgentIdentityStatusActive).
		Find(&idents).Error; err != nil {
		return nil, err
	}
	for i := range idents {
		keys[onlineKey{namespaceID: idents[i].NamespaceID, serverID: idents[i].ServerID}] = struct{}{}
	}
	return keys, nil
}

// buildServerView 组装单台 server 视图（纯内存，名称来自已批量取的映射）。
func buildServerView(s *model.Server, zoneByID map[uint]model.Zone, regionNameByID, bcNameByID map[uint]string, online map[onlineKey]struct{}) ServerView {
	view := ServerView{
		ID: s.ID, NamespaceID: s.NamespaceID, ServerID: s.ServerID, Kind: s.Kind,
		BCClusterID: s.BCClusterID, ZoneID: s.ZoneID, PendingZoneID: s.PendingZoneID,
		IsDefaultEntry: s.IsDefaultEntry, Draining: s.Draining,
		Assigned: isServerAssigned(s), CreatedAt: s.CreatedAt,
	}
	if s.BCClusterID != nil {
		if name, ok := bcNameByID[*s.BCClusterID]; ok {
			view.BCClusterName = &name
		}
	}
	if s.ZoneID != nil {
		if zone, ok := zoneByID[*s.ZoneID]; ok {
			zoneName := zone.Name
			view.ZoneName = &zoneName
			if regionName, ok := regionNameByID[zone.RegionID]; ok {
				view.RegionName = &regionName
			}
		}
	}
	if s.PendingZoneID != nil {
		if zone, ok := zoneByID[*s.PendingZoneID]; ok {
			pendingName := zone.Name
			view.PendingZoneName = &pendingName
		}
	}
	_, view.Online = online[onlineKey{namespaceID: s.NamespaceID, serverID: s.ServerID}]
	return view
}

// ZoneTreeZone 是 zone-tree 的小区节点。
type ZoneTreeZone struct {
	ID                uint   `json:"id"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	ServerCount       int    `json:"serverCount"`
	DefaultEntryCount int    `json:"defaultEntryCount"`
}

// ZoneTreeRegion 是 zone-tree 的大区节点。
type ZoneTreeRegion struct {
	ID          uint           `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Zones       []ZoneTreeZone `json:"zones"`
}

// ZoneTreeCluster 是 zone-tree 的 BC 集群节点。
type ZoneTreeCluster struct {
	ID          uint             `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	ProxyCount  int              `json:"proxyCount"`
	Regions     []ZoneTreeRegion `json:"regions"`
}

// ZoneTreeResponse 是区服结构树只读聚合响应。
type ZoneTreeResponse struct {
	NamespaceID     uint              `json:"namespaceId"`
	Clusters        []ZoneTreeCluster `json:"clusters"`
	UnassignedCount int               `json:"unassignedCount"`
}

// ZoneTree 只读聚合 bc_cluster / region / zone / server 拼结构树：一次拉四表内存拼装，禁循环内查库（N+1）。
func (s *V2ControlPlaneService) ZoneTree(namespaceID uint) (*ZoneTreeResponse, error) {
	clusterQ := s.db.Model(&model.BCCluster{})
	if namespaceID != 0 {
		clusterQ = clusterQ.Where("namespace_id = ?", namespaceID)
	}
	var clusters []model.BCCluster
	if err := clusterQ.Order("id ASC").Find(&clusters).Error; err != nil {
		return nil, err
	}
	regions, err := loadRegionsForClusters(s.db, clusters)
	if err != nil {
		return nil, err
	}
	zones, err := loadZonesForRegions(s.db, regions)
	if err != nil {
		return nil, err
	}
	serverQ := s.db.Model(&model.Server{})
	if namespaceID != 0 {
		serverQ = serverQ.Where("namespace_id = ?", namespaceID)
	}
	var servers []model.Server
	if err := serverQ.Find(&servers).Error; err != nil {
		return nil, err
	}
	return buildZoneTree(namespaceID, clusters, regions, zones, servers), nil
}

// loadRegionsForClusters 批量取给定 BC 集群下的全部大区。
func loadRegionsForClusters(db *gorm.DB, clusters []model.BCCluster) ([]model.Region, error) {
	if len(clusters) == 0 {
		return nil, nil
	}
	ids := make([]uint, 0, len(clusters))
	for i := range clusters {
		ids = append(ids, clusters[i].ID)
	}
	var regions []model.Region
	err := db.Where("bc_cluster_id IN ?", ids).Order("id ASC").Find(&regions).Error
	return regions, err
}

// loadZonesForRegions 批量取给定大区下的全部小区。
func loadZonesForRegions(db *gorm.DB, regions []model.Region) ([]model.Zone, error) {
	if len(regions) == 0 {
		return nil, nil
	}
	ids := make([]uint, 0, len(regions))
	for i := range regions {
		ids = append(ids, regions[i].ID)
	}
	var zones []model.Zone
	err := db.Where("region_id IN ?", ids).Order("id ASC").Find(&zones).Error
	return zones, err
}

// buildZoneTree 纯内存拼装结构树：先按 server 统计各维度计数，再逐层组装。
func buildZoneTree(namespaceID uint, clusters []model.BCCluster, regions []model.Region, zones []model.Zone, servers []model.Server) *ZoneTreeResponse {
	proxyByCluster, serverByZone, defaultEntryByZone, unassigned := countServersForTree(servers)
	zonesByRegion := map[uint][]ZoneTreeZone{}
	for i := range zones {
		zone := &zones[i]
		zonesByRegion[zone.RegionID] = append(zonesByRegion[zone.RegionID], ZoneTreeZone{
			ID: zone.ID, Name: zone.Name, Description: zone.Description,
			ServerCount: serverByZone[zone.ID], DefaultEntryCount: defaultEntryByZone[zone.ID],
		})
	}
	regionsByCluster := map[uint][]ZoneTreeRegion{}
	for i := range regions {
		region := &regions[i]
		regionsByCluster[region.BCClusterID] = append(regionsByCluster[region.BCClusterID], ZoneTreeRegion{
			ID: region.ID, Name: region.Name, Description: region.Description,
			Zones: orEmptyZones(zonesByRegion[region.ID]),
		})
	}
	out := &ZoneTreeResponse{NamespaceID: namespaceID, Clusters: make([]ZoneTreeCluster, 0, len(clusters)), UnassignedCount: unassigned}
	for i := range clusters {
		cluster := &clusters[i]
		out.Clusters = append(out.Clusters, ZoneTreeCluster{
			ID: cluster.ID, Name: cluster.Name, Description: cluster.Description,
			ProxyCount: proxyByCluster[cluster.ID], Regions: orEmptyRegions(regionsByCluster[cluster.ID]),
		})
	}
	return out
}

// countServersForTree 按 server 行统计集群 proxy 数、各 zone 服务器数 / 默认入口数与全局未分配数。
func countServersForTree(servers []model.Server) (proxyByCluster, serverByZone, defaultEntryByZone map[uint]int, unassigned int) {
	proxyByCluster = map[uint]int{}
	serverByZone = map[uint]int{}
	defaultEntryByZone = map[uint]int{}
	for i := range servers {
		server := &servers[i]
		if server.Kind == model.ServerKindProxy && server.BCClusterID != nil {
			proxyByCluster[*server.BCClusterID]++
		}
		if server.ZoneID != nil {
			serverByZone[*server.ZoneID]++
			if server.IsDefaultEntry {
				defaultEntryByZone[*server.ZoneID]++
			}
		}
		if !isServerAssigned(server) {
			unassigned++
		}
	}
	return proxyByCluster, serverByZone, defaultEntryByZone, unassigned
}

// NamespaceStat 是 namespace 列表项及其统计摘要（已分配 server 数 / BC 集群数 / 生效信任数）。
type NamespaceStat struct {
	Namespace        model.Namespace
	ServerCount      int64
	BCClusterCount   int64
	ActiveTrustCount int64
}

// ListNamespacesWithStats 列出全部 namespace 并附统计摘要，计数按 namespace 分组聚合、禁逐个查库。
func (s *V2ControlPlaneService) ListNamespacesWithStats() ([]NamespaceStat, error) {
	var namespaces []model.Namespace
	if err := s.db.Order("id ASC").Find(&namespaces).Error; err != nil {
		return nil, err
	}
	serverCounts, err := s.assignedServerCountsByNamespace()
	if err != nil {
		return nil, err
	}
	clusterCounts, err := s.bcClusterCountsByNamespace()
	if err != nil {
		return nil, err
	}
	trustCounts, err := s.activeTrustCountsByNamespace()
	if err != nil {
		return nil, err
	}
	stats := make([]NamespaceStat, 0, len(namespaces))
	for i := range namespaces {
		stats = append(stats, NamespaceStat{
			Namespace:        namespaces[i],
			ServerCount:      serverCounts[namespaces[i].ID],
			BCClusterCount:   clusterCounts[namespaces[i].ID],
			ActiveTrustCount: trustCounts[namespaces[i].ID],
		})
	}
	return stats, nil
}

// nsCountRow 承接按 namespace 分组的计数结果。
type nsCountRow struct {
	NamespaceID uint
	Count       int64
}

// assignedServerCountsByNamespace 按 namespace 统计已分配 server 数。
func (s *V2ControlPlaneService) assignedServerCountsByNamespace() (map[uint]int64, error) {
	var rows []nsCountRow
	if err := s.db.Model(&model.Server{}).
		Select("namespace_id, COUNT(*) AS count").
		Where("zone_id IS NOT NULL OR bc_cluster_id IS NOT NULL").
		Group("namespace_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return countRowsToMap(rows), nil
}

// bcClusterCountsByNamespace 按 namespace 统计 BC 集群数。
func (s *V2ControlPlaneService) bcClusterCountsByNamespace() (map[uint]int64, error) {
	var rows []nsCountRow
	if err := s.db.Model(&model.BCCluster{}).
		Select("namespace_id, COUNT(*) AS count").
		Group("namespace_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return countRowsToMap(rows), nil
}

// activeTrustCountsByNamespace 按 namespace 统计生效信任数（一条信任对 from / to 双方各计一次）。
func (s *V2ControlPlaneService) activeTrustCountsByNamespace() (map[uint]int64, error) {
	var trusts []model.NamespaceTrust
	if err := s.db.Select("from_namespace_id", "to_namespace_id").
		Where("status = ?", model.NamespaceTrustStatusActive).Find(&trusts).Error; err != nil {
		return nil, err
	}
	counts := map[uint]int64{}
	for i := range trusts {
		counts[trusts[i].FromNamespaceID]++
		counts[trusts[i].ToNamespaceID]++
	}
	return counts, nil
}

// RezonePrefillView 是换区工单重确认的预填目标（源自 server 的 pending 归属列）。
type RezonePrefillView struct {
	TargetKind string `json:"targetKind"`
	TargetID   uint   `json:"targetId"`
	TargetName string `json:"targetName"`
}

// GetAgentIdentity 只读取单条身份详情，附换区预填目标（若该 server 正处于换区中）。
func (s *V2ControlPlaneService) GetAgentIdentity(identityID string) (*model.AgentIdentity, *RezonePrefillView, error) {
	if !validUUID(identityID) {
		return nil, nil, apperr.ErrInvalidParam
	}
	ident, err := findIdentityByID(s.db, identityID)
	if err != nil {
		return nil, nil, err
	}
	if ident == nil {
		return nil, nil, apperr.ErrInstanceNotFound
	}
	prefill, err := s.rezonePrefillFor(ident)
	if err != nil {
		return nil, nil, err
	}
	return ident, prefill, nil
}

// rezonePrefillFor 若身份对应 server 处于换区中（pending 归属非空），返回其预填目标名称，否则返回 nil。
func (s *V2ControlPlaneService) rezonePrefillFor(ident *model.AgentIdentity) (*RezonePrefillView, error) {
	server, err := findServerRow(s.db, ident.NamespaceID, ident.ServerID)
	if err != nil || server == nil {
		return nil, err
	}
	if server.PendingZoneID != nil {
		var zone model.Zone
		if err := s.db.First(&zone, *server.PendingZoneID).Error; err != nil {
			return nil, err
		}
		return &RezonePrefillView{TargetKind: model.AssignmentTargetZone, TargetID: zone.ID, TargetName: zone.Name}, nil
	}
	if server.PendingBCClusterID != nil {
		var cluster model.BCCluster
		if err := s.db.First(&cluster, *server.PendingBCClusterID).Error; err != nil {
			return nil, err
		}
		return &RezonePrefillView{TargetKind: model.AssignmentTargetBCCluster, TargetID: cluster.ID, TargetName: cluster.Name}, nil
	}
	return nil, nil
}

// findServerRow 按 (namespace, serverId) 取 server 行；不存在返回 (nil, nil)。
func findServerRow(db *gorm.DB, namespaceID uint, serverID string) (*model.Server, error) {
	var server model.Server
	err := db.Where("namespace_id = ? AND server_id = ?", namespaceID, serverID).First(&server).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &server, nil
}

// ---- 小工具：集合与切片（避免散落重复代码）----

func addUintPtr(set map[uint]struct{}, v *uint) {
	if v != nil {
		set[*v] = struct{}{}
	}
}

func uintKeys(set map[uint]struct{}) []uint {
	keys := make([]uint, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	return keys
}

func stringKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	return keys
}

func countRowsToMap(rows []nsCountRow) map[uint]int64 {
	m := make(map[uint]int64, len(rows))
	for i := range rows {
		m[rows[i].NamespaceID] = rows[i].Count
	}
	return m
}

func orEmptyZones(zones []ZoneTreeZone) []ZoneTreeZone {
	if zones == nil {
		return []ZoneTreeZone{}
	}
	return zones
}

func orEmptyRegions(regions []ZoneTreeRegion) []ZoneTreeRegion {
	if regions == nil {
		return []ZoneTreeRegion{}
	}
	return regions
}
