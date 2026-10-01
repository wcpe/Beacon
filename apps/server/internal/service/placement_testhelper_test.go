package service

import (
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// seedServerPlacement 按新真源（server.zone_id）落一条区服归属：
// 建/取 namespace → bc_cluster → region（大区 code）→ zone（小区 code），再把 server.zone_id 指向该小区。
// 幂等：同 (namespace, serverId) 重复调用只更新 zone_id；同 code 的 region / zone 复用。
func seedServerPlacement(t *testing.T, db *gorm.DB, namespaceCode, serverID, regionCode, zoneCode string) {
	t.Helper()
	ns := seedPlacementNamespace(t, db, namespaceCode)
	cluster := seedBCCluster(t, db, ns.ID)
	region := seedRegion(t, db, cluster.ID, regionCode)
	zone := seedZone(t, db, region.ID, zoneCode)

	server := upsertSeedServer(t, db, ns.ID, serverID, model.ServerKindBackend)
	zoneID := zone.ID
	server.ZoneID = &zoneID
	if err := db.Save(server).Error; err != nil {
		t.Fatalf("写入 server.zone_id 失败: %v", err)
	}
}

// seedBCClusterOnlyPlacement 只把 server.bc_cluster_id 指向 BC 集群（代理归属），不落 zone，
// 用于验证「仅分到 BC 集群」按无区服归属处理。
func seedBCClusterOnlyPlacement(t *testing.T, db *gorm.DB, namespaceCode, serverID string) {
	t.Helper()
	ns := seedPlacementNamespace(t, db, namespaceCode)
	cluster := seedBCCluster(t, db, ns.ID)

	server := upsertSeedServer(t, db, ns.ID, serverID, model.ServerKindProxy)
	clusterID := cluster.ID
	server.BCClusterID = &clusterID
	if err := db.Save(server).Error; err != nil {
		t.Fatalf("写入 server.bc_cluster_id 失败: %v", err)
	}
}

// seedServerRow 只建 server 行（无任何归属），用于验证「无归属」回退语义。
func seedServerRow(t *testing.T, db *gorm.DB, namespaceCode, serverID string) {
	t.Helper()
	ns := seedPlacementNamespace(t, db, namespaceCode)
	upsertSeedServer(t, db, ns.ID, serverID, model.ServerKindBackend)
}

// upsertSeedServer 取或建 server 行（保留既有 kind 与归属列，由调用方按需覆盖）。
func upsertSeedServer(t *testing.T, db *gorm.DB, namespaceID uint, serverID, kind string) *model.Server {
	t.Helper()
	var server model.Server
	err := db.Where("namespace_id = ? AND server_id = ?", namespaceID, serverID).First(&server).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		server = model.Server{
			NamespaceID: namespaceID, ServerID: serverID, Kind: kind,
			Lifecycle: model.ServerLifecycleActive,
		}
		if err := db.Create(&server).Error; err != nil {
			t.Fatalf("建 server 行失败: %v", err)
		}
		return &server
	}
	if err != nil {
		t.Fatalf("读取 server 行失败: %v", err)
	}
	return &server
}

// seedPlacementNamespace 取或建 namespace。
func seedPlacementNamespace(t *testing.T, db *gorm.DB, code string) *model.Namespace {
	t.Helper()
	var ns model.Namespace
	err := db.Where("code = ?", code).First(&ns).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		ns = model.Namespace{Code: code, Name: code, Lifecycle: model.NamespaceLifecycleActive}
		if err := db.Create(&ns).Error; err != nil {
			t.Fatalf("建 namespace 失败: %v", err)
		}
		return &ns
	}
	if err != nil {
		t.Fatalf("读取 namespace 失败: %v", err)
	}
	return &ns
}

// seedBCCluster 取或建该 namespace 下的 BC 集群。
func seedBCCluster(t *testing.T, db *gorm.DB, namespaceID uint) *model.BCCluster {
	t.Helper()
	var cluster model.BCCluster
	err := db.Where("namespace_id = ? AND code = ?", namespaceID, "bc1").First(&cluster).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		cluster = model.BCCluster{NamespaceID: namespaceID, Code: "bc1", Name: "bc1"}
		if err := db.Create(&cluster).Error; err != nil {
			t.Fatalf("建 BC 集群失败: %v", err)
		}
		return &cluster
	}
	if err != nil {
		t.Fatalf("读取 BC 集群失败: %v", err)
	}
	return &cluster
}

// seedRegion 取或建大区。
func seedRegion(t *testing.T, db *gorm.DB, clusterID uint, code string) *model.Region {
	t.Helper()
	var region model.Region
	err := db.Where("bc_cluster_id = ? AND code = ?", clusterID, code).First(&region).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		region = model.Region{BCClusterID: clusterID, Code: code, Name: code}
		if err := db.Create(&region).Error; err != nil {
			t.Fatalf("建大区失败: %v", err)
		}
		return &region
	}
	if err != nil {
		t.Fatalf("读取大区失败: %v", err)
	}
	return &region
}

// seedZone 取或建小区。
func seedZone(t *testing.T, db *gorm.DB, regionID uint, code string) *model.Zone {
	t.Helper()
	var zone model.Zone
	err := db.Where("region_id = ? AND code = ?", regionID, code).First(&zone).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		zone = model.Zone{RegionID: regionID, Code: code, Name: code}
		if err := db.Create(&zone).Error; err != nil {
			t.Fatalf("建小区失败: %v", err)
		}
		return &zone
	}
	if err != nil {
		t.Fatalf("读取小区失败: %v", err)
	}
	return &zone
}
