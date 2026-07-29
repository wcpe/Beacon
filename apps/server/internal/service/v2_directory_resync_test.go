package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

func TestFR201DirectoryResyncOnlyAcceptsOnlineConfirmedBC(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	if err := db.AutoMigrate(&model.AgentCommand{}); err != nil {
		t.Fatal(err)
	}
	ns := &model.Namespace{Code: "prod", Name: "prod"}
	if err := db.Create(ns).Error; err != nil {
		t.Fatal(err)
	}
	cluster := &model.BCCluster{NamespaceID: ns.ID, Name: "bc"}
	if err := db.Create(cluster).Error; err != nil {
		t.Fatal(err)
	}
	online := &model.Server{NamespaceID: ns.ID, ServerID: "bc-1", Kind: model.ServerKindProxy, BCClusterID: &cluster.ID}
	offline := &model.Server{NamespaceID: ns.ID, ServerID: "bc-2", Kind: model.ServerKindProxy, BCClusterID: &cluster.ID}
	if err := db.Create([]*model.Server{online, offline}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create([]model.AgentIdentity{
		{IdentityID: "20111111-1111-4111-8111-111111111111", NamespaceID: ns.ID, ServerID: model.NullableServerID("bc-1"), Kind: model.ServerKindProxy, Status: model.AgentIdentityStatusActive},
		{IdentityID: "20111111-1111-4111-8111-111111111112", NamespaceID: ns.ID, ServerID: model.NullableServerID("bc-2"), Kind: model.ServerKindProxy, Status: model.AgentIdentityStatusActive},
	}).Error; err != nil {
		t.Fatal(err)
	}
	reg := runtime.NewRegistry()
	if _, err := reg.Register(&runtime.Instance{Namespace: ns.Code, ServerID: "bc-1"}, 0, nowForFR201()); err != nil {
		t.Fatal(err)
	}
	svc.SetRuntimeRegistry(reg)
	svc.SetDirectoryResyncCommandPort(repository.NewAgentCommandRepository(db), nil)
	if !svc.isConfirmedOnlineBC(ns, online) {
		t.Fatalf("测试前置：bc-1 应为已确认在线 BC")
	}

	result, err := svc.RequestNamespaceDirectoryResync(ns.ID, "admin", "")
	if err != nil {
		t.Fatalf("namespace 重同步应部分接受: %v", err)
	}
	if result.Accepted != 1 || result.Rejected != 1 || len(result.Results) != 2 {
		t.Fatalf("接受/拒绝结果错误: %+v", result)
	}
	if result.Results[0].ServerID != "bc-1" || !result.Results[0].Accepted || result.Results[0].CommandID == 0 {
		t.Fatalf("在线 BC 应创建命令: %+v", result.Results[0])
	}
	if result.Results[1].Code != "BC_OFFLINE" {
		t.Fatalf("离线 BC 应明确拒绝: %+v", result.Results[1])
	}
}

func TestFR201DirectoryResyncSingleFlightAndReportBinding(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	if err := db.AutoMigrate(&model.AgentCommand{}); err != nil {
		t.Fatal(err)
	}
	ns := &model.Namespace{Code: "prod", Name: "prod"}
	if err := db.Create(ns).Error; err != nil {
		t.Fatal(err)
	}
	cluster := &model.BCCluster{NamespaceID: ns.ID, Name: "bc"}
	if err := db.Create(cluster).Error; err != nil {
		t.Fatal(err)
	}
	server := &model.Server{NamespaceID: ns.ID, ServerID: "bc-1", Kind: model.ServerKindProxy, BCClusterID: &cluster.ID}
	if err := db.Create(server).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentIdentity{IdentityID: "20122222-2222-4222-8222-222222222222", NamespaceID: ns.ID, ServerID: model.NullableServerID("bc-1"), Kind: model.ServerKindProxy, Status: model.AgentIdentityStatusActive}).Error; err != nil {
		t.Fatal(err)
	}
	reg := runtime.NewRegistry()
	if _, err := reg.Register(&runtime.Instance{Namespace: ns.Code, ServerID: "bc-1"}, 0, nowForFR201()); err != nil {
		t.Fatal(err)
	}
	svc.SetRuntimeRegistry(reg)
	repo := repository.NewAgentCommandRepository(db)
	svc.SetDirectoryResyncCommandPort(repo, nil)
	if !svc.isConfirmedOnlineBC(ns, server) {
		t.Fatalf("测试前置：bc-1 应为已确认在线 BC")
	}

	first, err := svc.RequestServerDirectoryResync(server.ID, "admin", "")
	if err != nil {
		t.Fatalf("首次下发失败: %v", err)
	}
	if _, err := svc.RequestServerDirectoryResync(server.ID, "admin", ""); err == nil {
		t.Fatal("活跃命令不应重复创建")
	}
	cmd, err := repo.FindByID(first.CommandID)
	if err != nil || cmd == nil {
		t.Fatalf("读取命令失败: %v", err)
	}
	if _, err := repo.UpdateStatus(cmd.ID, model.CommandStatusPending, model.CommandStatusFetched, ""); err != nil {
		t.Fatal(err)
	}
	wrong := agentauth.Identity{NamespaceID: ns.ID, Namespace: ns.Code, ServerID: "other", Kind: model.ServerKindProxy}
	if err := svc.ReceiveDirectoryResyncResult(wrong, cmd.ID, false, "\x00不安全"); err == nil {
		t.Fatal("错目标回执必须按不存在拒绝")
	}
	right := agentauth.Identity{NamespaceID: ns.ID, Namespace: ns.Code, ServerID: "bc-1", Kind: model.ServerKindProxy}
	if err := svc.ReceiveDirectoryResyncResult(right, cmd.ID, false, strings.Repeat("\x00失败", 300)); err != nil {
		t.Fatalf("正确回执失败: %v", err)
	}
	got, err := repo.FindByID(cmd.ID)
	if err != nil || got == nil || got.Status != model.CommandStatusFailed {
		t.Fatalf("失败回执应终结命令: %+v err=%v", got, err)
	}
	if strings.ContainsRune(got.ResultDetail, '\x00') || len([]rune(got.ResultDetail)) > 512 {
		t.Fatalf("失败原因必须清洗并截断: %q", got.ResultDetail)
	}
}

// TestDirectoryResyncRejectsArchivedServer 验证归档 BC 即使仍残留 active identity 与运行态记录，也不可再创建命令。
func TestDirectoryResyncRejectsArchivedServer(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	if err := db.AutoMigrate(&model.AgentCommand{}); err != nil {
		t.Fatal(err)
	}
	ns := &model.Namespace{Code: "archived-resync", Name: "archived-resync"}
	if err := db.Create(ns).Error; err != nil {
		t.Fatal(err)
	}
	cluster := &model.BCCluster{NamespaceID: ns.ID, Name: "bc"}
	if err := db.Create(cluster).Error; err != nil {
		t.Fatal(err)
	}
	server := &model.Server{
		NamespaceID: ns.ID, ServerID: "bc-archived", Kind: model.ServerKindProxy,
		BCClusterID: &cluster.ID, Lifecycle: model.ServerLifecycleArchived,
	}
	if err := db.Create(server).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AgentIdentity{
		IdentityID: "20133333-3333-4333-8333-333333333333", NamespaceID: ns.ID,
		ServerID: model.NullableServerID(server.ServerID), Kind: model.ServerKindProxy, Status: model.AgentIdentityStatusActive,
	}).Error; err != nil {
		t.Fatal(err)
	}
	reg := runtime.NewRegistry()
	if _, err := reg.Register(&runtime.Instance{Namespace: ns.Code, ServerID: server.ServerID}, 0, nowForFR201()); err != nil {
		t.Fatal(err)
	}
	svc.SetRuntimeRegistry(reg)
	svc.SetDirectoryResyncCommandPort(repository.NewAgentCommandRepository(db), nil)

	_, err := svc.RequestServerDirectoryResync(server.ID, "admin", "")
	if !errors.Is(err, apperr.ErrServerArchived) {
		t.Fatalf("归档 BC 重同步应返回 SERVER_ARCHIVED，实际 %v", err)
	}
	var count int64
	if err := db.Model(&model.AgentCommand{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("归档 BC 不应创建重同步命令，实际 %d 条", count)
	}
}

func nowForFR201() time.Time { return time.Now().UTC() }
