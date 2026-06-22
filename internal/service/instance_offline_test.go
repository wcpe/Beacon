package service

import (
	"errors"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime"
)

// newOfflineTestStack 装配主动下线态测试栈（内存 sqlite + 共享注册表），不依赖 MySQL/DSN（FR-49）。
func newOfflineTestStack(t *testing.T) (*InstanceService, *runtime.Registry, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.ServerOffline{}, &model.ServerDrain{}, &model.ZoneAssignment{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	for _, tbl := range []string{"server_offline", "server_drain", "zone_assignment", "audit_log"} {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	reg := runtime.NewRegistry()
	svc := NewInstanceService(db,
		reg,
		repository.NewZoneAssignmentRepository(db),
		repository.NewServerOfflineRepository(db),
		repository.NewAuditLogRepository(db),
		10*time.Second, 30*time.Second)
	return svc, reg, db
}

func regParams(serverID string) RegisterParams {
	return RegisterParams{
		Namespace: "prod", ServerID: serverID, Role: "bukkit", GroupHint: "area1",
		Address: serverID + ":25565", ClientIP: "127.0.0.1",
	}
}

// TestOfflineRejectsReregister 主动下线后该实例重注册被专门错误码拒绝，取消下线后可再注册。
func TestOfflineRejectsReregister(t *testing.T) {
	svc, reg, _ := newOfflineTestStack(t)

	// 首次注册成功并在册
	if _, err := svc.Register(regParams("lobby-1")); err != nil {
		t.Fatalf("首次注册应成功: %v", err)
	}
	if reg.Get("prod", "lobby-1") == nil {
		t.Fatalf("首次注册后应在册")
	}

	// 主动下线：落库 + 移出内存
	if err := svc.Offline("prod", "lobby-1", "故障下架", "admin", "127.0.0.1"); err != nil {
		t.Fatalf("下线应成功: %v", err)
	}
	if reg.Get("prod", "lobby-1") != nil {
		t.Fatalf("下线后应已移出内存可用集")
	}

	// 重注册被拒：专门错误码 INSTANCE_OFFLINE_REJECTED（区别于 NOT_REGISTERED / DUPLICATE_SERVER_ID）
	_, err := svc.Register(regParams("lobby-1"))
	if !errors.Is(err, apperr.ErrInstanceOfflineRejected) {
		t.Fatalf("下线后重注册应 INSTANCE_OFFLINE_REJECTED，实际 %v", err)
	}
	if reg.Get("prod", "lobby-1") != nil {
		t.Fatalf("被拒注册不得写入内存")
	}

	// 下线标记可列出
	offs, err := svc.ListOffline("prod")
	if err != nil {
		t.Fatalf("列出下线标记失败: %v", err)
	}
	if len(offs) != 1 || offs[0].ServerID != "lobby-1" || offs[0].Reason != "故障下架" {
		t.Fatalf("下线标记错误: %+v", offs)
	}

	// 取消下线后可重新注册
	if err := svc.Online("prod", "lobby-1", "admin", "127.0.0.1"); err != nil {
		t.Fatalf("取消下线应成功: %v", err)
	}
	if _, err := svc.Register(regParams("lobby-1")); err != nil {
		t.Fatalf("取消下线后应可重新注册: %v", err)
	}
	if reg.Get("prod", "lobby-1") == nil {
		t.Fatalf("取消下线后重注册应在册")
	}
}

// TestOnlineNotFound 取消未下线的实例 → OFFLINE_NOT_FOUND。
func TestOnlineNotFound(t *testing.T) {
	svc, _, _ := newOfflineTestStack(t)
	if err := svc.Online("prod", "ghost", "admin", ""); !errors.Is(err, apperr.ErrOfflineNotFound) {
		t.Fatalf("取消不存在下线应 OFFLINE_NOT_FOUND，实际 %v", err)
	}
}

// TestOfflineAllowsNotInRegistry 允许对不在内存的实例预先下线（移除内存不是前置条件）。
func TestOfflineAllowsNotInRegistry(t *testing.T) {
	svc, _, _ := newOfflineTestStack(t)
	if err := svc.Offline("prod", "never-seen", "", "admin", ""); err != nil {
		t.Fatalf("对未注册实例预先下线应成功: %v", err)
	}
	if _, err := svc.Register(regParams("never-seen")); !errors.Is(err, apperr.ErrInstanceOfflineRejected) {
		t.Fatalf("预先下线后首次注册即应被拒，实际 %v", err)
	}
}

// TestOfflineDistinctFromHealthAndDrain 下线不写 drain；TTL 衰退不写 server_offline；三者互不串扰。
func TestOfflineDistinctFromHealthAndDrain(t *testing.T) {
	svc, reg, db := newOfflineTestStack(t)

	// 注册两台，仅对 lobby-1 主动下线
	if _, err := svc.Register(regParams("lobby-1")); err != nil {
		t.Fatalf("注册 lobby-1 失败: %v", err)
	}
	if _, err := svc.Register(regParams("lobby-2")); err != nil {
		t.Fatalf("注册 lobby-2 失败: %v", err)
	}
	if err := svc.Offline("prod", "lobby-1", "", "admin", ""); err != nil {
		t.Fatalf("下线 lobby-1 失败: %v", err)
	}

	// server_drain 不应被下线写入（职责分离）
	var drainCount int64
	if err := db.Model(&model.ServerDrain{}).Where("deleted_at = ?", model.SoftDeleteSentinel).Count(&drainCount).Error; err != nil {
		t.Fatalf("统计 drain 失败: %v", err)
	}
	if drainCount != 0 {
		t.Fatalf("主动下线不得写 server_drain，实际 %d 条", drainCount)
	}

	// 把 lobby-2 心跳推到 TTL 之外触发健康衰退；衰退不得写 server_offline
	reg.SweepExpired(time.Now().UTC().Add(10*time.Minute), 15*time.Second, 30*time.Second, 120*time.Second)
	offs, err := svc.ListOffline("prod")
	if err != nil {
		t.Fatalf("列出下线标记失败: %v", err)
	}
	if len(offs) != 1 || offs[0].ServerID != "lobby-1" {
		t.Fatalf("健康衰退不得写 server_offline，下线标记应仅 lobby-1，实际 %+v", offs)
	}
}
