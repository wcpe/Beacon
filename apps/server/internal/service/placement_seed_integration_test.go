//go:build integration

package service_test

import (
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// seedServerZonePlacement 按新真源（server.zone_id）落一条区服归属：
// 建/取 namespace → bc_cluster → region（大区 code）→ zone（小区 code），并把该 (namespace, serverId)
// 的 server 行 zone_id 指过去（server 行不存在则新建 backend 行）。
//
// 供「归属驱动的读取」集成用例构造权威状态：旧 zone_assignment 已退役、生产恒 0 行，
// 靠 V1 指派接口喂数据已不再驱动任何读方。幂等：同 code 的各级复用，重复调用只改 zone_id。
func seedServerZonePlacement(t *testing.T, db *gorm.DB, nsCode, serverID, regionCode, zoneCode string) {
	t.Helper()
	ns := ensureNamespaceRow(t, db, nsCode)
	cluster := ensureBCClusterRow(t, db, ns.ID)
	region := ensureRegionRow(t, db, cluster.ID, regionCode)
	zone := ensureZoneRow(t, db, region.ID, zoneCode)

	var server model.Server
	err := db.Where("namespace_id = ? AND server_id = ?", ns.ID, serverID).First(&server).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		server = model.Server{NamespaceID: ns.ID, ServerID: serverID, Kind: model.ServerKindBackend}
		if err := db.Create(&server).Error; err != nil {
			t.Fatalf("建 server 行失败: %v", err)
		}
	} else if err != nil {
		t.Fatalf("读取 server 行失败: %v", err)
	}
	zoneID := zone.ID
	server.ZoneID = &zoneID
	if err := db.Save(&server).Error; err != nil {
		t.Fatalf("写入 server.zone_id 失败: %v", err)
	}
}

// ensureNamespaceRow 取或建 namespace 行（归属解析经 namespace.code → id 关联）。
func ensureNamespaceRow(t *testing.T, db *gorm.DB, code string) *model.Namespace {
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

// ensureBCClusterRow 取或建该 namespace 下的 BC 集群行。
func ensureBCClusterRow(t *testing.T, db *gorm.DB, namespaceID uint) *model.BCCluster {
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

// ensureRegionRow 取或建大区行。
func ensureRegionRow(t *testing.T, db *gorm.DB, clusterID uint, code string) *model.Region {
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

// ensureZoneRow 取或建小区行。
func ensureZoneRow(t *testing.T, db *gorm.DB, regionID uint, code string) *model.Zone {
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
