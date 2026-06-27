package service

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime"
)

// newImpactTestStack 装配影响面预览测试栈（内存 sqlite 存 zone_assignment + 共享注册表），不依赖 MySQL。
func newImpactTestStack(t *testing.T) (*ImpactService, *runtime.Registry, *repository.ZoneAssignmentRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.ZoneAssignment{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.Exec("DELETE FROM zone_assignment").Error; err != nil {
		t.Fatalf("清表失败: %v", err)
	}
	assignRepo := repository.NewZoneAssignmentRepository(db)
	reg := runtime.NewRegistry()
	return NewImpactService(reg, assignRepo), reg, assignRepo
}

// regInstance 注册一个在线实例（指定角色无关，仅看归属与状态）。
func regImpactInst(t *testing.T, reg *runtime.Registry, ns, serverID, groupHint string) {
	t.Helper()
	in := &runtime.Instance{Namespace: ns, ServerID: serverID, Role: "bukkit", GroupHint: groupHint, Address: serverID + ":1"}
	if _, err := reg.Register(in, 30*time.Second, time.Now().UTC()); err != nil {
		t.Fatalf("注册 %s 失败: %v", serverID, err)
	}
}

// TestScopeCovers 穷举四层覆盖判定（纯函数）。
func TestScopeCovers(t *testing.T) {
	cases := []struct {
		name                            string
		level, group, scopeTarget       string
		instGroup, instZone, instServer string
		want                            bool
	}{
		{"global 覆盖任意", model.ScopeGlobal, "", "", "g1", "z1", "s1", true},
		{"group 同大区命中", model.ScopeGroup, "g1", "", "g1", "z1", "s1", true},
		{"group 异大区不中", model.ScopeGroup, "g1", "", "g2", "z1", "s1", false},
		{"zone 大区+小区都中", model.ScopeZone, "g1", "z1", "g1", "z1", "s1", true},
		{"zone 大区中小区不中", model.ScopeZone, "g1", "z1", "g1", "z2", "s1", false},
		{"zone 大区不中", model.ScopeZone, "g1", "z1", "g2", "z1", "s1", false},
		{"server 同 serverId 命中", model.ScopeServer, "g1", "s1", "g1", "z1", "s1", true},
		{"server 异 serverId 不中", model.ScopeServer, "g1", "s2", "g1", "z1", "s1", false},
	}
	for _, c := range cases {
		if got := scopeCovers(c.level, c.group, c.scopeTarget, c.instGroup, c.instZone, c.instServer); got != c.want {
			t.Errorf("%s: scopeCovers=%v 期望 %v", c.name, got, c.want)
		}
	}
}

// TestImpactGlobalCoversAllAvailable global 覆盖该环境全部可用实例。
func TestImpactGlobalCoversAllAvailable(t *testing.T) {
	svc, reg, _ := newImpactTestStack(t)
	regImpactInst(t, reg, "prod", "s1", "g1")
	regImpactInst(t, reg, "prod", "s2", "g2")
	regImpactInst(t, reg, "other", "s3", "g1") // 异环境不计

	imp, err := svc.Resolve("prod", model.ScopeGlobal, "", "")
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if imp.Total != 2 || imp.Affected[0] != "s1" || imp.Affected[1] != "s2" {
		t.Fatalf("global 应命中 [s1 s2]，实际 total=%d affected=%v", imp.Total, imp.Affected)
	}
}

// TestImpactGroupByAssignment group 层按 DB 归属解析大区命中。
func TestImpactGroupByAssignment(t *testing.T) {
	svc, reg, assignRepo := newImpactTestStack(t)
	regImpactInst(t, reg, "prod", "s1", "hintX") // 已指派 → 用 DB 大区 g1
	regImpactInst(t, reg, "prod", "s2", "g1")    // 未指派 → 回退 GroupHint=g1
	regImpactInst(t, reg, "prod", "s3", "g2")    // 未指派 → GroupHint=g2，不中
	if _, err := assignRepo.Upsert("prod", "s1", "g1", "z1", ""); err != nil {
		t.Fatalf("指派失败: %v", err)
	}

	imp, err := svc.Resolve("prod", model.ScopeGroup, "g1", "")
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if imp.Total != 2 || imp.Affected[0] != "s1" || imp.Affected[1] != "s2" {
		t.Fatalf("group=g1 应命中 [s1 s2]，实际 %v", imp.Affected)
	}
}

// TestImpactZone zone 层须大区+小区都匹配（归属来自 DB）。
func TestImpactZone(t *testing.T) {
	svc, reg, assignRepo := newImpactTestStack(t)
	regImpactInst(t, reg, "prod", "s1", "g1")
	regImpactInst(t, reg, "prod", "s2", "g1")
	regImpactInst(t, reg, "prod", "s3", "g1")
	mustAssign(t, assignRepo, "prod", "s1", "g1", "z1")
	mustAssign(t, assignRepo, "prod", "s2", "g1", "z2") // 小区不同
	mustAssign(t, assignRepo, "prod", "s3", "g1", "z1")

	imp, err := svc.Resolve("prod", model.ScopeZone, "g1", "z1")
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if imp.Total != 2 || imp.Affected[0] != "s1" || imp.Affected[1] != "s3" {
		t.Fatalf("zone g1/z1 应命中 [s1 s3]，实际 %v", imp.Affected)
	}
}

// TestImpactServerOnlineHitOfflineEmpty server 层目标在线则命中、不在线返回空集。
func TestImpactServerOnlineHitOfflineEmpty(t *testing.T) {
	svc, reg, _ := newImpactTestStack(t)
	regImpactInst(t, reg, "prod", "s1", "g1")

	hit, err := svc.Resolve("prod", model.ScopeServer, "g1", "s1")
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if hit.Total != 1 || hit.Affected[0] != "s1" {
		t.Fatalf("server s1 在线应命中自身，实际 %v", hit.Affected)
	}
	miss, err := svc.Resolve("prod", model.ScopeServer, "g1", "s404")
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if miss.Total != 0 {
		t.Fatalf("server s404 不在线应返回空集，实际 %v", miss.Affected)
	}
}

// TestImpactDegradedCountedLostExcluded degraded 计入「将影响」、lost 排除（可用集合口径）。
func TestImpactDegradedCountedLostExcluded(t *testing.T) {
	svc, reg, _ := newImpactTestStack(t)
	base := time.Now().UTC()
	// 三台实例分别以不同的注册（=初始心跳）时刻注册，再统一在 now 扫描，制造不同心跳年龄分档。
	_, _ = reg.Register(&runtime.Instance{Namespace: "prod", ServerID: "online-1", Role: "bukkit", GroupHint: "g1", Address: "online-1:1"}, 30*time.Second, base)
	_, _ = reg.Register(&runtime.Instance{Namespace: "prod", ServerID: "degraded-1", Role: "bukkit", GroupHint: "g1", Address: "degraded-1:1"}, 30*time.Second, base.Add(-20*time.Second)) // age 20s → degraded（>10 <30）
	_, _ = reg.Register(&runtime.Instance{Namespace: "prod", ServerID: "lost-1", Role: "bukkit", GroupHint: "g1", Address: "lost-1:1"}, 30*time.Second, base.Add(-40*time.Second))         // age 40s → lost（>30 <60）

	now := base
	reg.SweepExpired(now, 10*time.Second, 30*time.Second, 60*time.Second)

	imp, err := svc.Resolve("prod", model.ScopeGlobal, "", "")
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if imp.Total != 2 || imp.Affected[0] != "degraded-1" || imp.Affected[1] != "online-1" {
		t.Fatalf("可用集合应为 [degraded-1 online-1]（lost 排除），实际 %v", imp.Affected)
	}
}

func mustAssign(t *testing.T, repo *repository.ZoneAssignmentRepository, ns, serverID, group, zone string) {
	t.Helper()
	if _, err := repo.Upsert(ns, serverID, group, zone, ""); err != nil {
		t.Fatalf("指派 %s 失败: %v", serverID, err)
	}
}
