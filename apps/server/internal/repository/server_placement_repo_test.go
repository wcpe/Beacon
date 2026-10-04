package repository

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// newPlacementTestDB 建含归属真源（namespace / bc_cluster / region / zone / server）的内存 sqlite。
func newPlacementTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Namespace{}, &model.BCCluster{}, &model.Region{}, &model.Zone{}, &model.Server{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// TestServerPlacementFindByServerResolvesCodes 验证新真源单查询解析：
// server.zone_id → 小区 code，再由 zone.region_id → 大区 code；并验证跨 namespace 不串号。
func TestServerPlacementFindByServerResolvesCodes(t *testing.T) {
	db := newPlacementTestDB(t)
	prod := mkPlacementFixture(t, db, "prod", "bc1", "area1", "zoneA")
	other := mkPlacementFixture(t, db, "test", "bc2", "area9", "zoneZ")
	mkServer(t, db, prod.namespaceID, "lobby-1", model.ServerKindBackend, &prod.zoneID, nil)
	mkServer(t, db, other.namespaceID, "lobby-1", model.ServerKindBackend, &other.zoneID, nil)

	repo := NewServerPlacementRepository(db)
	got, err := repo.FindByServer("prod", "lobby-1")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got == nil || got.GroupCode != "area1" || got.ZoneCode != "zoneA" {
		t.Fatalf("应解析出 area1/zoneA，实际 %+v", got)
	}
	// 同 serverId 在不同 namespace 归属不同，验证 namespace 作为查询维度未丢
	otherGot, err := repo.FindByServer("test", "lobby-1")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if otherGot == nil || otherGot.GroupCode != "area9" || otherGot.ZoneCode != "zoneZ" {
		t.Fatalf("应解析出 area9/zoneZ，实际 %+v", otherGot)
	}
}

// TestServerPlacementFindByServerNoPlacement 无区服归属时返回 (nil, nil)：
// 覆盖 server 行无归属 / 仅分到 BC 集群（代理）/ server 行不存在 / 空入参四种情形。
func TestServerPlacementFindByServerNoPlacement(t *testing.T) {
	db := newPlacementTestDB(t)
	fx := mkPlacementFixture(t, db, "prod", "bc1", "area1", "zoneA")
	clusterID := fx.clusterID
	mkServer(t, db, fx.namespaceID, "plain-1", model.ServerKindBackend, nil, nil)
	mkServer(t, db, fx.namespaceID, "bc-1", model.ServerKindProxy, nil, &clusterID)

	repo := NewServerPlacementRepository(db)
	cases := []struct {
		name     string
		ns       string
		serverID string
	}{
		{name: "server 行无任何归属", ns: "prod", serverID: "plain-1"},
		{name: "仅分到 BC 集群的代理", ns: "prod", serverID: "bc-1"},
		{name: "server 行不存在", ns: "prod", serverID: "ghost-1"},
		{name: "namespace 不存在", ns: "nope", serverID: "plain-1"},
		{name: "空 namespace", ns: "", serverID: "plain-1"},
		{name: "空 serverId", ns: "prod", serverID: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.FindByServer(tc.ns, tc.serverID)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if got != nil {
				t.Fatalf("无区服归属应返回 nil，实际 %+v", got)
			}
		})
	}
}

// TestServerPlacementFindByServerZoneRowMissing zone 行被并发删除时按无归属处理，不报错也不返回空 code 的伪归属。
func TestServerPlacementFindByServerZoneRowMissing(t *testing.T) {
	db := newPlacementTestDB(t)
	fx := mkPlacementFixture(t, db, "prod", "bc1", "area1", "zoneA")
	mkServer(t, db, fx.namespaceID, "lobby-1", model.ServerKindBackend, &fx.zoneID, nil)
	if err := db.Exec("DELETE FROM zone WHERE id = ?", fx.zoneID).Error; err != nil {
		t.Fatalf("删除 zone 行失败: %v", err)
	}

	got, err := NewServerPlacementRepository(db).FindByServer("prod", "lobby-1")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got != nil {
		t.Fatalf("zone 行缺失应按无归属返回 nil，实际 %+v", got)
	}
}

// TestServerPlacementFindByNamespaceBatch 验证批量解析：一次取齐某环境全部「已分配到小区」的实例归属，
// 未分配 / 仅分到 BC 集群 / 异环境的行都不进入映射（调用方按无归属回退）。
func TestServerPlacementFindByNamespaceBatch(t *testing.T) {
	db := newPlacementTestDB(t)
	prod := mkPlacementFixture(t, db, "prod", "bc1", "area1", "zoneA")
	prodZoneB := mkZoneInSameFixture(t, db, prod.regionID, "zoneB")
	other := mkPlacementFixture(t, db, "test", "bc2", "area9", "zoneZ")
	clusterID := prod.clusterID

	mkServer(t, db, prod.namespaceID, "s1", model.ServerKindBackend, &prod.zoneID, nil)
	mkServer(t, db, prod.namespaceID, "s2", model.ServerKindBackend, &prodZoneB, nil)
	mkServer(t, db, prod.namespaceID, "plain-1", model.ServerKindBackend, nil, nil)
	mkServer(t, db, prod.namespaceID, "proxy-1", model.ServerKindProxy, nil, &clusterID)
	mkServer(t, db, other.namespaceID, "s1", model.ServerKindBackend, &other.zoneID, nil)

	got, err := NewServerPlacementRepository(db).FindByNamespace("prod")
	if err != nil {
		t.Fatalf("批量解析失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应只含两台已分配小区的实例，实际 %v", got)
	}
	if p := got["s1"]; p.GroupCode != "area1" || p.ZoneCode != "zoneA" {
		t.Fatalf("s1 应解析出 area1/zoneA，实际 %+v", p)
	}
	if p := got["s2"]; p.GroupCode != "area1" || p.ZoneCode != "zoneB" {
		t.Fatalf("s2 应解析出 area1/zoneB，实际 %+v", p)
	}
	if _, ok := got["plain-1"]; ok {
		t.Fatal("无归属实例不应进入映射")
	}
	if _, ok := got["proxy-1"]; ok {
		t.Fatal("仅分到 BC 集群的代理不应进入映射")
	}

	// 空环境 code 直接返回空映射，不查库
	empty, err := NewServerPlacementRepository(db).FindByNamespace("")
	if err != nil || len(empty) != 0 {
		t.Fatalf("空 namespace 应返回空映射，实际 %v err=%v", empty, err)
	}
}

// placementFixture 是归属测试用的结构骨架（namespace → bc_cluster → region → zone）。
type placementFixture struct {
	namespaceID uint
	clusterID   uint
	regionID    uint
	zoneID      uint
}

// mkPlacementFixture 按 code 建 namespace → bc_cluster → region → zone，返回各级 id。
func mkPlacementFixture(t *testing.T, db *gorm.DB, nsCode, clusterCode, regionCode, zoneCode string) placementFixture {
	t.Helper()
	ns := &model.Namespace{Code: nsCode, Name: nsCode, Lifecycle: model.NamespaceLifecycleActive}
	if err := db.Create(ns).Error; err != nil {
		t.Fatalf("建 namespace 失败: %v", err)
	}
	cluster := &model.BCCluster{NamespaceID: ns.ID, Code: clusterCode, Name: clusterCode}
	if err := db.Create(cluster).Error; err != nil {
		t.Fatalf("建 BC 集群失败: %v", err)
	}
	region := &model.Region{BCClusterID: cluster.ID, Code: regionCode, Name: regionCode}
	if err := db.Create(region).Error; err != nil {
		t.Fatalf("建大区失败: %v", err)
	}
	zone := &model.Zone{RegionID: region.ID, Code: zoneCode, Name: zoneCode}
	if err := db.Create(zone).Error; err != nil {
		t.Fatalf("建小区失败: %v", err)
	}
	return placementFixture{namespaceID: ns.ID, clusterID: cluster.ID, regionID: region.ID, zoneID: zone.ID}
}

// mkZoneInSameFixture 在既有大区下建一个额外小区（同环境第二台实例归属用），返回其 id。
func mkZoneInSameFixture(t *testing.T, db *gorm.DB, regionID uint, code string) uint {
	t.Helper()
	zone := &model.Zone{RegionID: regionID, Code: code, Name: code}
	if err := db.Create(zone).Error; err != nil {
		t.Fatalf("建小区 %s 失败: %v", code, err)
	}
	return zone.ID
}

// mkServer 建一台 server 行，按需带上 zone_id / bc_cluster_id 归属。
func mkServer(t *testing.T, db *gorm.DB, namespaceID uint, serverID, kind string, zoneID, bcClusterID *uint) {
	t.Helper()
	server := &model.Server{
		NamespaceID: namespaceID, ServerID: serverID, Kind: kind,
		Lifecycle: model.ServerLifecycleActive, ZoneID: zoneID, BCClusterID: bcClusterID,
	}
	if err := db.Create(server).Error; err != nil {
		t.Fatalf("建 server 行失败: %v", err)
	}
}
