package service

import (
	"errors"
	"net/url"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

func TestV1ZoneAssignmentRequiresApprovalAndExecutesInWorker(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+url.QueryEscape(t.Name())+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存数据库失败：%v", err)
	}
	if err := db.AutoMigrate(&model.Namespace{}, &model.ZoneAssignment{}, &model.ServerDrain{}, &model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移测试表失败：%v", err)
	}
	if err := db.Create(&model.Namespace{Code: "prod", Name: "生产"}).Error; err != nil {
		t.Fatalf("创建命名空间失败：%v", err)
	}
	registry := authz.NewApprovalRegistry()
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	v2 := NewV2ControlPlaneService(db)
	zone := NewZoneService(db, repository.NewZoneAssignmentRepository(db), repository.NewAuditLogRepository(db), runtime.NewRegistry())
	scheduling := NewSchedulingService(db, repository.NewServerDrainRepository(db), repository.NewAuditLogRepository(db), runtime.NewRegistry())
	v2.SetLegacyZoneService(zone)
	v2.SetLegacySchedulingService(scheduling)
	v2.SetApprovalService(approval)
	RegisterV2ControlPlaneApprovalAdapters(registry, v2)
	if _, err := zone.Assign("prod", "lobby-1", "area-a", "zone-a", "admin", "首次分配", ""); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("V1 公开指派入口必须拒绝，实际 %v", err)
	}
	if err := zone.Unassign("prod", "lobby-1", "admin", ""); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("V1 公开取消指派入口必须拒绝，实际 %v", err)
	}
	if err := scheduling.Undrain("prod", "lobby-1", "admin", ""); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("V1 公开恢复调度入口必须拒绝，实际 %v", err)
	}

	ticket, err := v2.RequestLegacyZoneAssignment("prod", "lobby-1", "area-a", "zone-a", "首次分配", "admin", "", "v1-assignment", auth.HumanPrincipal("admin"))
	if err != nil {
		t.Fatalf("V1 指派应仅创建审批申请：%v", err)
	}
	if ticket.Status != model.ApprovalStatusPending || ticket.OperationKey != authz.OperationTopologyServerAssign {
		t.Fatalf("申请状态或操作错误：%+v", ticket)
	}
	if current, err := repository.NewZoneAssignmentRepository(db).FindByServer("prod", "lobby-1"); err != nil || current != nil {
		t.Fatalf("批准前不得写入 V1 指派，当前=%+v err=%v", current, err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("admin"), ""); err != nil {
		t.Fatalf("批准申请失败：%v", err)
	}
	if _, err := NewApprovalWorker(approval).RunOnce(); err != nil {
		t.Fatalf("审批 worker 执行失败：%v", err)
	}
	current, err := repository.NewZoneAssignmentRepository(db).FindByServer("prod", "lobby-1")
	if err != nil || current == nil || current.GroupCode != "area-a" || current.ZoneCode != "zone-a" {
		t.Fatalf("批准后应写入冻结的 V1 指派，当前=%+v err=%v", current, err)
	}

	unassign, err := v2.RequestLegacyZoneUnassign("prod", "lobby-1", "取消归属", "admin", "", "v1-unassign", auth.HumanPrincipal("admin"))
	if err != nil {
		t.Fatalf("V1 取消指派应创建审批申请：%v", err)
	}
	if _, err := approval.Approve(unassign.ApprovalRequestID, auth.HumanPrincipal("admin"), ""); err != nil {
		t.Fatalf("批准取消指派失败：%v", err)
	}
	if _, err := NewApprovalWorker(approval).RunOnce(); err != nil {
		t.Fatalf("执行取消指派失败：%v", err)
	}
	if current, err := repository.NewZoneAssignmentRepository(db).FindByServer("prod", "lobby-1"); err != nil || current != nil {
		t.Fatalf("批准后应删除 V1 指派，当前=%+v err=%v", current, err)
	}

	if _, err := scheduling.Drain("prod", "lobby-1", "维护", "admin", ""); err != nil {
		t.Fatalf("V1 开启排空应直接止损成功：%v", err)
	}
	undrain, err := v2.RequestLegacyUndrain("prod", "lobby-1", "恢复调度", "admin", "", "v1-undrain", auth.HumanPrincipal("admin"))
	if err != nil {
		t.Fatalf("V1 取消排空应创建审批申请：%v", err)
	}
	if _, err := approval.Approve(undrain.ApprovalRequestID, auth.HumanPrincipal("admin"), ""); err != nil {
		t.Fatalf("批准取消排空失败：%v", err)
	}
	if _, err := NewApprovalWorker(approval).RunOnce(); err != nil {
		t.Fatalf("执行取消排空失败：%v", err)
	}
	if current, err := repository.NewServerDrainRepository(db).FindByServer("prod", "lobby-1"); err != nil || current != nil {
		t.Fatalf("批准后应取消 V1 排空，当前=%+v err=%v", current, err)
	}
}
