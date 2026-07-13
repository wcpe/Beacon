package service

import (
	"errors"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// TestAssignRejectsBungee 验证 zone 指派对 role=bungee 的 BC 代理实例拒绝（FR-8/FR-35 纵深防御）。
// bungee 守卫在任何 DB 访问前返回，故无需真实库即可单测（repo/db 传 nil 不会被触达）。
func TestAssignRejectsBungee(t *testing.T) {
	reg := runtime.NewRegistry()
	if _, err := reg.Register(&runtime.Instance{
		Namespace: "prod", ServerID: "bc-1", Role: roleBungee, Address: "10.0.0.9:25577",
	}, 30*time.Second, time.Now().UTC()); err != nil {
		t.Fatalf("注册 bc 实例失败: %v", err)
	}
	// db/repo 传 nil：bungee 守卫先于事务与仓库访问返回，不会触达。
	svc := NewZoneService(nil, nil, nil, reg)

	_, err := svc.Assign("prod", "bc-1", "area1", "zoneA", "admin", "", "10.0.0.1")
	if !errors.Is(err, apperr.ErrZoneNotAssignableToBC) {
		t.Fatalf("对 bungee 实例应返回 ErrZoneNotAssignableToBC，实际 %v", err)
	}
}

// newZoneSvcWithRegistry 装配一个带指定 registry 的 ZoneService（内存 sqlite + 真实仓库），供排空门/同值 no-op 单测。
func newZoneSvcWithRegistry(t *testing.T, db *gorm.DB, reg *runtime.Registry) *ZoneService {
	t.Helper()
	return NewZoneService(db,
		repository.NewZoneAssignmentRepository(db),
		repository.NewAuditLogRepository(db),
		reg)
}

// registerOnlineWithPlayers 注册一个在线 bukkit 实例并上报指定玩家数（>0 即"在场有玩家"）。
func registerOnlineWithPlayers(t *testing.T, reg *runtime.Registry, ns, serverID string, players int) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := reg.Register(&runtime.Instance{
		Namespace: ns, ServerID: serverID, Role: roleBukkit, Address: "10.0.0.5:25565",
	}, 30*time.Second, now); err != nil {
		t.Fatalf("注册在线实例失败: %v", err)
	}
	if ok := reg.Report(ns, serverID, "", players, 0, 0, 0, runtime.CPULoadUnavailable, nil); !ok {
		t.Fatalf("上报玩家数失败")
	}
}

// TestAssignSameValueNoOp 验证同值指派 no-op：目标与现有完全相同 → 返回现有记录、不新增审计、不再 upsert。
func TestAssignSameValueNoOp(t *testing.T) {
	db := newDefaultEntrySvcDB(t)
	reg := runtime.NewRegistry()
	svc := newZoneSvcWithRegistry(t, db, reg)

	// 首次指派 lobby-1 → area1/zoneA（离线服，放行）
	first, err := svc.Assign("prod", "lobby-1", "area1", "zoneA", "admin", "首次", "1.1.1.1")
	if err != nil || first == nil {
		t.Fatalf("首次指派应成功，实际 a=%+v err=%v", first, err)
	}
	if got := auditCount(t, db, model.ActionZoneAssign); got != 1 {
		t.Fatalf("首次指派应有 1 条 zone.assign 审计，实际 %d", got)
	}

	// 即便此时该服在线且有玩家，同值指派仍应 no-op 放行（同值先于排空门）
	registerOnlineWithPlayers(t, reg, "prod", "lobby-1", 5)

	got, err := svc.Assign("prod", "lobby-1", "area1", "zoneA", "admin", "重复", "1.1.1.1")
	if err != nil {
		t.Fatalf("同值指派应 no-op 成功，实际 err=%v", err)
	}
	if got == nil || got.GroupCode != "area1" || got.ZoneCode != "zoneA" {
		t.Fatalf("同值指派应返回现有记录，实际 %+v", got)
	}
	// 不新增任何审计（assign 仍 1、move 0）
	if c := auditCount(t, db, model.ActionZoneAssign); c != 1 {
		t.Fatalf("同值 no-op 不应新增 zone.assign 审计，实际 %d", c)
	}
	if c := auditCount(t, db, model.ActionZoneMove); c != 0 {
		t.Fatalf("同值 no-op 不应产生 zone.move 审计，实际 %d", c)
	}
	// note 未被改写（未走 upsert）
	cur, err := repository.NewZoneAssignmentRepository(db).FindByServer("prod", "lobby-1")
	if err != nil || cur == nil {
		t.Fatalf("查现有指派失败: a=%+v err=%v", cur, err)
	}
	if cur.Note != "首次" {
		t.Fatalf("同值 no-op 不应改写 note，期望「首次」实际 %q", cur.Note)
	}
}

// TestAssignOnlineNonemptyRejectsReassign 验证在线非空服改派到不同区 → 409，且未落库未审计。
func TestAssignOnlineNonemptyRejectsReassign(t *testing.T) {
	db := newDefaultEntrySvcDB(t)
	reg := runtime.NewRegistry()
	svc := newZoneSvcWithRegistry(t, db, reg)

	// 先在离线态指派到 zoneA
	if _, err := svc.Assign("prod", "lobby-1", "area1", "zoneA", "admin", "", ""); err != nil {
		t.Fatalf("初始指派失败: %v", err)
	}
	// 该服在线且有玩家
	registerOnlineWithPlayers(t, reg, "prod", "lobby-1", 3)

	// 改派到 zoneB → 排空门 409
	_, err := svc.Assign("prod", "lobby-1", "area1", "zoneB", "admin", "", "")
	if !errors.Is(err, apperr.ErrZoneServerOnlineNonempty) {
		t.Fatalf("在线非空改派应返回 ZONE_SERVER_ONLINE_NONEMPTY，实际 %v", err)
	}
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Status != 409 || ae.Code != "ZONE_SERVER_ONLINE_NONEMPTY" {
		t.Fatalf("错误码/HTTP 状态应为 409 ZONE_SERVER_ONLINE_NONEMPTY，实际 %+v", ae)
	}
	// 库未变更（仍在 zoneA）、未新增 move 审计
	cur, _ := repository.NewZoneAssignmentRepository(db).FindByServer("prod", "lobby-1")
	if cur == nil || cur.ZoneCode != "zoneA" {
		t.Fatalf("被拒后库不应变更，应仍在 zoneA，实际 %+v", cur)
	}
	if c := auditCount(t, db, model.ActionZoneMove); c != 0 {
		t.Fatalf("被拒不应产生 zone.move 审计，实际 %d", c)
	}
}

// TestAssignOnlineNonemptyRejectsFirstAssign 验证在线非空服首次指派 → 409，未落库未审计。
func TestAssignOnlineNonemptyRejectsFirstAssign(t *testing.T) {
	db := newDefaultEntrySvcDB(t)
	reg := runtime.NewRegistry()
	svc := newZoneSvcWithRegistry(t, db, reg)

	// 未指派、但在线且有玩家
	registerOnlineWithPlayers(t, reg, "prod", "lobby-1", 8)

	_, err := svc.Assign("prod", "lobby-1", "area1", "zoneA", "admin", "", "")
	if !errors.Is(err, apperr.ErrZoneServerOnlineNonempty) {
		t.Fatalf("在线非空首次指派应返回 ZONE_SERVER_ONLINE_NONEMPTY，实际 %v", err)
	}
	cur, _ := repository.NewZoneAssignmentRepository(db).FindByServer("prod", "lobby-1")
	if cur != nil {
		t.Fatalf("被拒后不应落库，实际 %+v", cur)
	}
	if c := auditCount(t, db, model.ActionZoneAssign); c != 0 {
		t.Fatalf("被拒不应产生 zone.assign 审计，实际 %d", c)
	}
}

// TestUnassignOnlineNonemptyRejected 验证在线非空服取消指派 → 409，未软删未审计。
func TestUnassignOnlineNonemptyRejected(t *testing.T) {
	db := newDefaultEntrySvcDB(t)
	reg := runtime.NewRegistry()
	svc := newZoneSvcWithRegistry(t, db, reg)

	if _, err := svc.Assign("prod", "lobby-1", "area1", "zoneA", "admin", "", ""); err != nil {
		t.Fatalf("初始指派失败: %v", err)
	}
	registerOnlineWithPlayers(t, reg, "prod", "lobby-1", 1)

	err := svc.Unassign("prod", "lobby-1", "admin", "")
	if !errors.Is(err, apperr.ErrZoneServerOnlineNonempty) {
		t.Fatalf("在线非空取消指派应返回 ZONE_SERVER_ONLINE_NONEMPTY，实际 %v", err)
	}
	// 指派仍在（未软删）、未产生 unassign 审计
	cur, _ := repository.NewZoneAssignmentRepository(db).FindByServer("prod", "lobby-1")
	if cur == nil || cur.ZoneCode != "zoneA" {
		t.Fatalf("被拒后指派应仍在，实际 %+v", cur)
	}
	if c := auditCount(t, db, model.ActionZoneUnassign); c != 0 {
		t.Fatalf("被拒不应产生 zone.unassign 审计，实际 %d", c)
	}
}

// TestAssignEmptyServerAllowed 验证空服（PlayerCount==0）改派放行、入审计（排空后可改）。
func TestAssignEmptyServerAllowed(t *testing.T) {
	db := newDefaultEntrySvcDB(t)
	reg := runtime.NewRegistry()
	svc := newZoneSvcWithRegistry(t, db, reg)

	if _, err := svc.Assign("prod", "lobby-1", "area1", "zoneA", "admin", "", ""); err != nil {
		t.Fatalf("初始指派失败: %v", err)
	}
	// 在线但已排空（0 玩家）
	registerOnlineWithPlayers(t, reg, "prod", "lobby-1", 0)

	got, err := svc.Assign("prod", "lobby-1", "area1", "zoneB", "admin", "", "")
	if err != nil || got == nil || got.ZoneCode != "zoneB" {
		t.Fatalf("空服改派应放行，实际 a=%+v err=%v", got, err)
	}
	if c := auditCount(t, db, model.ActionZoneMove); c != 1 {
		t.Fatalf("空服改派应有 1 条 zone.move 审计，实际 %d", c)
	}
}

// TestAssignOfflineServerAllowed 验证离线服（不在 registry）改派放行、入审计。
func TestAssignOfflineServerAllowed(t *testing.T) {
	db := newDefaultEntrySvcDB(t)
	reg := runtime.NewRegistry()
	svc := newZoneSvcWithRegistry(t, db, reg)

	if _, err := svc.Assign("prod", "lobby-1", "area1", "zoneA", "admin", "", ""); err != nil {
		t.Fatalf("初始指派失败: %v", err)
	}
	// registry 中无该实例（离线）→ 改派放行
	got, err := svc.Assign("prod", "lobby-1", "area1", "zoneB", "admin", "", "")
	if err != nil || got == nil || got.ZoneCode != "zoneB" {
		t.Fatalf("离线服改派应放行，实际 a=%+v err=%v", got, err)
	}
	if c := auditCount(t, db, model.ActionZoneMove); c != 1 {
		t.Fatalf("离线服改派应有 1 条 zone.move 审计，实际 %d", c)
	}
}

// TestUnassignEmptyServerAllowed 验证空服取消指派放行、入审计。
func TestUnassignEmptyServerAllowed(t *testing.T) {
	db := newDefaultEntrySvcDB(t)
	reg := runtime.NewRegistry()
	svc := newZoneSvcWithRegistry(t, db, reg)

	if _, err := svc.Assign("prod", "lobby-1", "area1", "zoneA", "admin", "", ""); err != nil {
		t.Fatalf("初始指派失败: %v", err)
	}
	registerOnlineWithPlayers(t, reg, "prod", "lobby-1", 0)

	if err := svc.Unassign("prod", "lobby-1", "admin", ""); err != nil {
		t.Fatalf("空服取消指派应放行，实际 err=%v", err)
	}
	if c := auditCount(t, db, model.ActionZoneUnassign); c != 1 {
		t.Fatalf("空服取消指派应有 1 条 zone.unassign 审计，实际 %d", c)
	}
}

// newDefaultEntrySvcDB 打开内存 sqlite 并迁移 zone 指派相关表（不依赖 MySQL/DSN）。
// 默认入口相关用例随真源收敛 v2 迁至 v2_default_entry_resolver_test.go（ADR-0067）。
func newDefaultEntrySvcDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.ZoneAssignment{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移 zone 指派相关表失败: %v", err)
	}
	for _, tbl := range []string{"zone_assignment", "audit_log"} {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	return db
}
