//go:build integration

package service_test

import (
	"errors"
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime"
	"github.com/wcpe/Beacon/internal/service"
)

// schedStack 装配调度服务与共享注册表（drain 落 DB、落位读内存 + DB）。
func schedStack(t *testing.T) (*service.SchedulingService, *runtime.Registry) {
	db := testDB(t)
	reg := runtime.NewRegistry()
	svc := service.NewSchedulingService(db,
		repository.NewServerDrainRepository(db),
		repository.NewAuditLogRepository(db), reg)
	return svc, reg
}

// regOnline 注册一台在线实例到注册表（指定 zone/weight/capacity）。
func regOnline(t *testing.T, reg *runtime.Registry, serverID, zone string, weight, capacity int) {
	t.Helper()
	_, err := reg.Register(&runtime.Instance{
		Namespace: "prod", ServerID: serverID, Role: "bukkit",
		ResolvedGroup: "area1", ResolvedZone: zone, Assigned: true,
		Address: serverID + ":25565", Weight: weight, Capacity: capacity,
	}, 30*time.Second, time.Now().UTC())
	if err != nil {
		t.Fatalf("注册 %s 失败: %v", serverID, err)
	}
}

// TestSchedulingDrainAffectsPlacement 集成验证：drain 后落位剔除、取消后回到候选、跨实例新建仍生效。
func TestSchedulingDrainAffectsPlacement(t *testing.T) {
	svc, reg := schedStack(t)
	regOnline(t, reg, "lobby-1", "zoneA", 100, 200)
	regOnline(t, reg, "lobby-2", "zoneA", 100, 100)

	// 初始：两台都在候选，lobby-1 容量大居前
	cands, err := svc.Placement("prod", "area1", "zoneA")
	if err != nil {
		t.Fatalf("落位查询失败: %v", err)
	}
	if len(cands) != 2 || cands[0].ServerID != "lobby-1" {
		t.Fatalf("初始候选错误: %+v", cands)
	}

	// drain lobby-1 → 落位仅剩 lobby-2
	if _, err := svc.Drain("prod", "lobby-1", "滚动维护", "admin", "127.0.0.1"); err != nil {
		t.Fatalf("drain 失败: %v", err)
	}
	cands, _ = svc.Placement("prod", "area1", "zoneA")
	if len(cands) != 1 || cands[0].ServerID != "lobby-2" {
		t.Fatalf("drain 后应仅剩 lobby-2，实际 %+v", cands)
	}

	// drain 列表含 lobby-1
	drains, _ := svc.ListDrains("prod")
	if len(drains) != 1 || drains[0].ServerID != "lobby-1" || drains[0].Reason != "滚动维护" {
		t.Fatalf("drain 列表错误: %+v", drains)
	}

	// 取消 drain → lobby-1 回到候选并复居首
	if err := svc.Undrain("prod", "lobby-1", "admin", "127.0.0.1"); err != nil {
		t.Fatalf("取消 drain 失败: %v", err)
	}
	cands, _ = svc.Placement("prod", "area1", "zoneA")
	if len(cands) != 2 || cands[0].ServerID != "lobby-1" {
		t.Fatalf("取消 drain 后应恢复两候选且 lobby-1 居首，实际 %+v", cands)
	}
}

// TestUndrainNotFound 取消不存在的 drain → DRAIN_NOT_FOUND。
func TestUndrainNotFound(t *testing.T) {
	svc, _ := schedStack(t)
	err := svc.Undrain("prod", "ghost", "admin", "")
	if !errors.Is(err, apperr.ErrDrainNotFound) {
		t.Fatalf("应返回 DRAIN_NOT_FOUND，实际 %v", err)
	}
}

// TestPlacementEmptyWhenAllDrained 全部 drain → 落位空候选（200、不报错）。
func TestPlacementEmptyWhenAllDrained(t *testing.T) {
	svc, reg := schedStack(t)
	regOnline(t, reg, "lobby-1", "zoneA", 100, 100)
	regOnline(t, reg, "lobby-2", "zoneA", 100, 100)
	if _, err := svc.Drain("prod", "lobby-1", "", "admin", ""); err != nil {
		t.Fatalf("drain lobby-1 失败: %v", err)
	}
	if _, err := svc.Drain("prod", "lobby-2", "", "admin", ""); err != nil {
		t.Fatalf("drain lobby-2 失败: %v", err)
	}
	cands, err := svc.Placement("prod", "area1", "zoneA")
	if err != nil {
		t.Fatalf("落位查询失败: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("全部 drain 应空候选，实际 %+v", cands)
	}
}

// TestPlacementInvalidParam namespace/zone 缺失 → INVALID_PARAM。
func TestPlacementInvalidParam(t *testing.T) {
	svc, _ := schedStack(t)
	if _, err := svc.Placement("", "area1", "zoneA"); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("缺 namespace 应 INVALID_PARAM，实际 %v", err)
	}
	if _, err := svc.Placement("prod", "area1", ""); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("缺 zone 应 INVALID_PARAM，实际 %v", err)
	}
}
