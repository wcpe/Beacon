package service

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// configScopeRef 唯一定位一条覆盖链所在的层（作用域层 + 层实体 id）。
type configScopeRef struct {
	Level string
	RefID uint
}

// ConfigEffectiveTarget 是有效解析目标（四选一；全空 = 仅 namespace 基线，spec §4.3）。
// ServerRef 兼容行数字 id 与业务 serverId 两种写法（与 devmock 口径一致）。
type ConfigEffectiveTarget struct {
	ServerRef   string
	ZoneID      uint
	RegionID    uint
	BCClusterID uint
}

// scopeMismatch 构造带具体原因的 CONFIG_SCOPE_MISMATCH 错误（跨 namespace 强隔离，spec §4.8）。
func scopeMismatch(reason string) *apperr.Error {
	return apperr.New(http.StatusBadRequest, "CONFIG_SCOPE_MISMATCH", reason)
}

// validateScopeBelongs 校验 scope 实体存在且归属链最终落在文件 namespace（spec §3.3/§4.8）。
func validateScopeBelongs(db *gorm.DB, file *model.ConfigFile, level string, refID uint) error {
	if !model.IsValidConfigScopeLevel(level) || refID == 0 {
		return scopeMismatch("作用域层不合法")
	}
	ownerNamespaceID, err := scopeNamespaceID(db, level, refID)
	if err != nil {
		return err
	}
	if ownerNamespaceID == 0 {
		return scopeMismatch(fmt.Sprintf("作用域 %s/%d 不存在", level, refID))
	}
	if ownerNamespaceID != file.NamespaceID {
		return scopeMismatch(fmt.Sprintf("作用域 %s/%d 不属于文件所在环境", level, refID))
	}
	return nil
}

// scopeNamespaceID 沿归属链解出某层实体最终归属的 namespace id；实体不存在返回 0。
func scopeNamespaceID(db *gorm.DB, level string, refID uint) (uint, error) {
	switch level {
	case model.ConfigScopeNamespace:
		var ns model.Namespace
		if err := db.Select("id").First(&ns, refID).Error; err != nil {
			return zeroIfNotFound(err)
		}
		return ns.ID, nil
	case model.ConfigScopeBCCluster:
		var cluster model.BCCluster
		if err := db.First(&cluster, refID).Error; err != nil {
			return zeroIfNotFound(err)
		}
		return cluster.NamespaceID, nil
	case model.ConfigScopeRegion:
		var region model.Region
		if err := db.First(&region, refID).Error; err != nil {
			return zeroIfNotFound(err)
		}
		return scopeNamespaceID(db, model.ConfigScopeBCCluster, region.BCClusterID)
	case model.ConfigScopeZone:
		var zone model.Zone
		if err := db.First(&zone, refID).Error; err != nil {
			return zeroIfNotFound(err)
		}
		return scopeNamespaceID(db, model.ConfigScopeRegion, zone.RegionID)
	case model.ConfigScopeServer:
		var server model.Server
		if err := db.First(&server, refID).Error; err != nil {
			return zeroIfNotFound(err)
		}
		return server.NamespaceID, nil
	default:
		return 0, nil
	}
}

// zeroIfNotFound 把记录缺失归一为 (0, nil)，其余错误原样上抛。
func zeroIfNotFound(err error) (uint, error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return 0, err
}

// resolveTargetChain 解析目标的有序作用域链（低 → 高，spec §4.1/§4.3）：
//   - server：经 zone→region→bc_cluster 解出完整五层；未分配 zone 的 server 只有 namespace + server 两层；
//   - zone / region / bc_cluster：假想目标，链到该层为止；
//   - 全空：仅 namespace 基线。
//
// 目标必须属于文件 namespace，否则 CONFIG_SCOPE_MISMATCH。
func resolveTargetChain(db *gorm.DB, namespaceID uint, target ConfigEffectiveTarget) ([]configScopeRef, error) {
	base := []configScopeRef{{Level: model.ConfigScopeNamespace, RefID: namespaceID}}
	switch {
	case target.ServerRef != "":
		return resolveServerChain(db, namespaceID, base, target.ServerRef)
	case target.ZoneID != 0:
		return resolveZoneChain(db, namespaceID, base, target.ZoneID)
	case target.RegionID != 0:
		return resolveRegionChain(db, namespaceID, base, target.RegionID)
	case target.BCClusterID != 0:
		return resolveBCClusterChain(db, namespaceID, base, target.BCClusterID)
	default:
		return base, nil
	}
}

// resolveServerChain 解析 server 目标：接受行数字 id 或业务 serverId。
func resolveServerChain(db *gorm.DB, namespaceID uint, base []configScopeRef, serverRef string) ([]configScopeRef, error) {
	server, err := findServerByRef(db, namespaceID, serverRef)
	if err != nil {
		return nil, err
	}
	if server == nil {
		return nil, scopeMismatch("目标 server 不存在或不属于文件所在环境")
	}
	if server.ZoneID != nil {
		chain, err := resolveZoneChain(db, namespaceID, base, *server.ZoneID)
		if err != nil {
			return nil, err
		}
		return append(chain, configScopeRef{Level: model.ConfigScopeServer, RefID: server.ID}), nil
	}
	// 未分配 zone：只有 namespace + server 两层参与合并（spec §4.1）
	return append(base, configScopeRef{Level: model.ConfigScopeServer, RefID: server.ID}), nil
}

// findServerByRef 在 namespace 内按行 id 或业务 serverId 找 server；不存在返回 (nil, nil)。
func findServerByRef(db *gorm.DB, namespaceID uint, serverRef string) (*model.Server, error) {
	q := db.Where("namespace_id = ?", namespaceID)
	if id, err := strconv.ParseUint(serverRef, 10, 64); err == nil {
		q = q.Where("id = ? OR server_id = ?", uint(id), serverRef)
	} else {
		q = q.Where("server_id = ?", serverRef)
	}
	var server model.Server
	err := q.First(&server).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &server, nil
}

// resolveZoneChain 解析 zone 目标：namespace → bc_cluster → region → zone。
func resolveZoneChain(db *gorm.DB, namespaceID uint, base []configScopeRef, zoneID uint) ([]configScopeRef, error) {
	var zone model.Zone
	if err := db.First(&zone, zoneID).Error; err != nil {
		return nil, notFoundAsScopeMismatch(err, "目标 zone 不存在")
	}
	chain, err := resolveRegionChain(db, namespaceID, base, zone.RegionID)
	if err != nil {
		return nil, err
	}
	return append(chain, configScopeRef{Level: model.ConfigScopeZone, RefID: zone.ID}), nil
}

// resolveRegionChain 解析 region 目标：namespace → bc_cluster → region。
func resolveRegionChain(db *gorm.DB, namespaceID uint, base []configScopeRef, regionID uint) ([]configScopeRef, error) {
	var region model.Region
	if err := db.First(&region, regionID).Error; err != nil {
		return nil, notFoundAsScopeMismatch(err, "目标 region 不存在")
	}
	chain, err := resolveBCClusterChain(db, namespaceID, base, region.BCClusterID)
	if err != nil {
		return nil, err
	}
	return append(chain, configScopeRef{Level: model.ConfigScopeRegion, RefID: region.ID}), nil
}

// resolveBCClusterChain 解析 bc_cluster 目标：namespace → bc_cluster（此处校验归属 namespace）。
func resolveBCClusterChain(db *gorm.DB, namespaceID uint, base []configScopeRef, clusterID uint) ([]configScopeRef, error) {
	var cluster model.BCCluster
	if err := db.First(&cluster, clusterID).Error; err != nil {
		return nil, notFoundAsScopeMismatch(err, "目标 bc_cluster 不存在")
	}
	if cluster.NamespaceID != namespaceID {
		return nil, scopeMismatch("目标不属于文件所在环境")
	}
	return append(base, configScopeRef{Level: model.ConfigScopeBCCluster, RefID: cluster.ID}), nil
}

// notFoundAsScopeMismatch 把记录缺失归一为 CONFIG_SCOPE_MISMATCH，其余错误原样上抛。
func notFoundAsScopeMismatch(err error, reason string) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return scopeMismatch(reason)
	}
	return err
}

// resolveScopeNames 批量解析各层实体显示名（namespace 取 code、server 取业务 serverId、其余取 name）。
// 按层各发一次 IN 查询（至多 5 次），禁循环内逐个查库（N+1）。
func resolveScopeNames(db *gorm.DB, refs []configScopeRef) (map[configScopeRef]string, error) {
	byLevel := map[string][]uint{}
	for _, ref := range refs {
		byLevel[ref.Level] = append(byLevel[ref.Level], ref.RefID)
	}
	names := make(map[configScopeRef]string, len(refs))
	for level, ids := range byLevel {
		if err := loadScopeLevelNames(db, level, ids, names); err != nil {
			return nil, err
		}
	}
	// 兜底：实体已被删除等取不到名时给可读占位，避免空白
	for _, ref := range refs {
		if _, ok := names[ref]; !ok {
			names[ref] = fmt.Sprintf("%s-%d", ref.Level, ref.RefID)
		}
	}
	return names, nil
}

// loadScopeLevelNames 按单一层批量取名并写入结果映射。
func loadScopeLevelNames(db *gorm.DB, level string, ids []uint, out map[configScopeRef]string) error {
	type idName struct {
		ID   uint
		Name string
	}
	var rows []idName
	var err error
	switch level {
	case model.ConfigScopeNamespace:
		err = db.Model(&model.Namespace{}).Select("id, code AS name").Where("id IN ?", ids).Scan(&rows).Error
	case model.ConfigScopeBCCluster:
		err = db.Model(&model.BCCluster{}).Select("id, name").Where("id IN ?", ids).Scan(&rows).Error
	case model.ConfigScopeRegion:
		err = db.Model(&model.Region{}).Select("id, name").Where("id IN ?", ids).Scan(&rows).Error
	case model.ConfigScopeZone:
		err = db.Model(&model.Zone{}).Select("id, name").Where("id IN ?", ids).Scan(&rows).Error
	case model.ConfigScopeServer:
		err = db.Model(&model.Server{}).Select("id, server_id AS name").Where("id IN ?", ids).Scan(&rows).Error
	}
	if err != nil {
		return err
	}
	for _, row := range rows {
		out[configScopeRef{Level: level, RefID: row.ID}] = row.Name
	}
	return nil
}
