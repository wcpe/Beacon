package service

import (
	"errors"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/longpoll"
	"gorm.io/gorm"
)

func TestFR199TransferServerPlacementAtomicallyMovesZoneAndLobby(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	server := approveLobbyBackend(t, svc, token, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "lobby-1")
	zone := createLobbyTestZone(t, svc, ns.ID)
	if _, err := svc.AssignServers(AssignServersParams{ServerIDs: []uint{server.ID}, TargetKind: model.AssignmentTargetZone, TargetID: zone.ID, IsDefaultEntry: true, Reason: "首次分配", Operator: "admin"}); err != nil {
		t.Fatalf("首次分配小区失败: %v", err)
	}
	lobby := lobbyForNamespace(t, db, ns.ID)

	view, err := svc.TransferServerPlacement(ServerPlacementTransferParams{
		ServerID: server.ServerID, TargetKind: LobbyPlacementKind, TargetID: lobby.ID,
		Reason: "调整为全局大厅", Operator: "admin",
	})
	if err != nil {
		t.Fatalf("小区迁入大厅失败: %v", err)
	}
	if view.PlacementKind != LobbyPlacementKind || view.LobbyClusterID == nil || *view.LobbyClusterID != lobby.ID || view.ZoneID != nil || view.IsDefaultEntry {
		t.Fatalf("迁入大厅后归属应互斥且清默认入口，实际 %+v", view)
	}

	view, err = svc.TransferServerPlacement(ServerPlacementTransferParams{
		ServerID: server.ServerID, TargetKind: model.AssignmentTargetZone, TargetID: zone.ID,
		Reason: "恢复业务小区", Operator: "admin",
	})
	if err != nil {
		t.Fatalf("大厅迁回小区失败: %v", err)
	}
	if view.PlacementKind != model.AssignmentTargetZone || view.ZoneID == nil || *view.ZoneID != zone.ID || view.LobbyClusterID != nil || view.IsDefaultEntry {
		t.Fatalf("迁回小区后归属应互斥且默认入口保持清除，实际 %+v", view)
	}

	var count int64
	if err := db.Model(&model.AuditLog{}).Where("action IN ?", []string{
		actionLobbyMemberMoveIn, actionLobbyMemberMoveOut,
	}).Count(&count).Error; err != nil {
		t.Fatalf("查询大厅迁移审计失败: %v", err)
	}
	if count != 2 {
		t.Fatalf("两次真实迁移必须各记一次专项审计，实际 %d", count)
	}
}

func TestFR199TransferRejectsNonemptyAndInvalidPlacements(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	server := approveLobbyBackend(t, svc, token, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "lobby-2")
	lobby := lobbyForNamespace(t, db, ns.ID)
	registry := runtime.NewRegistry()
	if _, err := registry.Register(&runtime.Instance{Namespace: ns.Code, ServerID: server.ServerID, Status: runtime.StatusOnline, PlayerCount: 3}, time.Minute, time.Now().UTC()); err != nil {
		t.Fatalf("写入运行态失败: %v", err)
	}
	svc.SetRuntimeRegistry(registry)

	_, err = svc.TransferServerPlacement(ServerPlacementTransferParams{
		ServerID: server.ServerID, TargetKind: LobbyPlacementKind, TargetID: lobby.ID,
		Reason: "在线有玩家", Operator: "admin",
	})
	if !errors.Is(err, apperr.ErrZoneServerOnlineNonempty) {
		t.Fatalf("在线且有玩家应被排空门拒绝，实际 %v", err)
	}
	var stored model.Server
	if err := db.First(&stored, server.ID).Error; err != nil {
		t.Fatalf("读取 server 失败: %v", err)
	}
	if stored.LobbyClusterID != nil {
		t.Fatalf("排空拒绝后不得改变归属，实际 %+v", stored)
	}

	proxy := approveLobbyProxy(t, svc, token, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "bc-1")
	_, err = svc.TransferServerPlacement(ServerPlacementTransferParams{
		ServerID: proxy.ServerID, TargetKind: LobbyPlacementKind, TargetID: lobby.ID,
		Reason: "错误迁入", Operator: "admin",
	})
	if !errors.Is(err, errLobbyMemberRoleInvalid) {
		t.Fatalf("代理加入大厅应返回角色错误，实际 %v", err)
	}
	zone := createLobbyTestZone(t, svc, ns.ID)
	if _, err := svc.TransferServerPlacement(ServerPlacementTransferParams{
		ServerID: server.ServerID, TargetKind: model.AssignmentTargetZone, TargetID: zone.ID,
		Reason: "未分配直入小区", Operator: "admin",
	}); !errors.Is(err, errServerPlacementConflict) {
		t.Fatalf("未分配直入小区必须继续走首次分配，实际 %v", err)
	}
}

func TestFR199LobbyReadUsesHealthViewAndNoopDoesNotAudit(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	server := approveLobbyBackend(t, svc, token, "dddddddd-dddd-4ddd-8ddd-dddddddddddd", "lobby-3")
	lobby := lobbyForNamespace(t, db, ns.ID)
	if _, err := svc.TransferServerPlacement(ServerPlacementTransferParams{
		ServerID: server.ServerID, TargetKind: LobbyPlacementKind, TargetID: lobby.ID,
		Reason: "配置大厅", Operator: "admin",
	}); err != nil {
		t.Fatalf("迁入大厅失败: %v", err)
	}
	views := healthview.NewStore()
	views.ReplaceAll([]healthview.View{{
		NamespaceID: ns.ID, ServerID: server.ServerID, Kind: model.ServerKindBackend,
		Score: 91, Level: healthview.LevelHealthy, Schedulable: true, OnlineCount: 7, MaxOnline: 100,
	}})
	svc.SetHealthViews(views)

	summary, err := svc.ListLobbyClusters(ListLobbyClustersParams{NamespaceID: ns.ID})
	if err != nil || summary.Total != 1 || !summary.Items[0].Ready || summary.Items[0].SchedulableCount != 1 {
		t.Fatalf("大厅摘要应复用健康可调度事实，实际 %+v err=%v", summary, err)
	}
	detail, err := svc.GetLobbyCluster(lobby.ID, LobbyClusterDetailParams{})
	if err != nil || len(detail.Members) != 1 {
		t.Fatalf("大厅详情应返回唯一成员，实际 %+v err=%v", detail, err)
	}
	member := detail.Members[0]
	if !member.Schedulable || member.Score != 91 || member.PlayerCount != 7 || member.MaxOnline != 100 {
		t.Fatalf("成员状态必须来自健康视图，实际 %+v", member)
	}

	if _, err := svc.TransferServerPlacement(ServerPlacementTransferParams{
		ServerID: server.ServerID, TargetKind: LobbyPlacementKind, TargetID: lobby.ID,
		Reason: "重复请求", Operator: "admin",
	}); err != nil {
		t.Fatalf("同值迁入应幂等成功: %v", err)
	}
	var count int64
	if err := db.Model(&model.AuditLog{}).Where("action = ?", actionLobbyMemberAssign).Count(&count).Error; err != nil {
		t.Fatalf("查询幂等审计失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("同值请求不得新增审计，实际 %d", count)
	}
}

func TestFR199TransferNotifiesTopologyOnlyAfterRealCommit(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, token, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	server := approveLobbyBackend(t, svc, token, "ffffffff-ffff-4fff-8fff-ffffffffffff", "lobby-notify")
	lobby := lobbyForNamespace(t, db, ns.ID)
	pushes := &lobbyPushRecorder{}
	notifier := NewChangeNotifier(longpoll.NewHub(), longpoll.NewHub(), longpoll.NewHub(), longpoll.NewHub(), runtime.NewRegistry(), nil)
	notifier.SetMetrics(pushes)
	svc.SetChangeNotifier(notifier)

	if _, err := svc.TransferServerPlacement(ServerPlacementTransferParams{
		ServerID: server.ServerID, TargetKind: LobbyPlacementKind, TargetID: lobby.ID,
		Reason: "设置大厅", Operator: "admin",
	}); err != nil {
		t.Fatalf("真实迁移失败: %v", err)
	}
	if pushes.count != 1 {
		t.Fatalf("提交成功后应唤醒一次拓扑订阅，实际 %d", pushes.count)
	}
	if _, err := svc.TransferServerPlacement(ServerPlacementTransferParams{
		ServerID: server.ServerID, TargetKind: LobbyPlacementKind, TargetID: lobby.ID,
		Reason: "同值请求", Operator: "admin",
	}); err != nil {
		t.Fatalf("同值请求失败: %v", err)
	}
	if pushes.count != 1 {
		t.Fatalf("同值请求不得发送拓扑通知，实际 %d", pushes.count)
	}
}

type lobbyPushRecorder struct{ count int }

func (r *lobbyPushRecorder) IncPushNotify() { r.count++ }

func approveLobbyBackend(t *testing.T, svc *V2ControlPlaneService, token, identityID, serverID string) *model.Server {
	t.Helper()
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{Token: token, IdentityID: identityID, ServerID: serverID, Kind: model.ServerKindBackend, BootID: "boot-" + serverID}); err != nil {
		t.Fatalf("注册 backend 失败: %v", err)
	}
	if _, err := svc.ApproveAgentIdentity(identityID, ApproveAgentIdentityParams{ServerID: serverID, Operator: "admin"}); err != nil {
		t.Fatalf("确认 backend 失败: %v", err)
	}
	var server model.Server
	if err := svc.db.Where("server_id = ?", serverID).First(&server).Error; err != nil {
		t.Fatalf("读取 backend 失败: %v", err)
	}
	return &server
}

func approveLobbyProxy(t *testing.T, svc *V2ControlPlaneService, token, identityID, serverID string) *model.Server {
	t.Helper()
	if _, err := svc.RegisterAgentV2(AgentRegisterV2Params{Token: token, IdentityID: identityID, ServerID: serverID, Kind: model.ServerKindProxy, BootID: "boot-" + serverID}); err != nil {
		t.Fatalf("注册 proxy 失败: %v", err)
	}
	if _, err := svc.ApproveAgentIdentity(identityID, ApproveAgentIdentityParams{ServerID: serverID, Operator: "admin"}); err != nil {
		t.Fatalf("确认 proxy 失败: %v", err)
	}
	var server model.Server
	if err := svc.db.Where("server_id = ?", serverID).First(&server).Error; err != nil {
		t.Fatalf("读取 proxy 失败: %v", err)
	}
	return &server
}

func lobbyForNamespace(t *testing.T, db *gorm.DB, namespaceID uint) model.LobbyCluster {
	t.Helper()
	var lobby model.LobbyCluster
	if err := db.Where("namespace_id = ?", namespaceID).First(&lobby).Error; err != nil {
		t.Fatalf("读取大厅集群失败: %v", err)
	}
	return lobby
}

func createLobbyTestZone(t *testing.T, svc *V2ControlPlaneService, namespaceID uint) *model.Zone {
	t.Helper()
	cluster, err := svc.CreateBCCluster(CreateBCClusterParams{NamespaceID: namespaceID, Name: "bc-lobby", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 BC 集群失败: %v", err)
	}
	region, err := svc.CreateRegion(CreateRegionParams{BCClusterID: cluster.ID, Name: "region-lobby", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建大区失败: %v", err)
	}
	zone, err := svc.CreateZone(CreateZoneParams{RegionID: region.ID, Name: "zone-lobby", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建小区失败: %v", err)
	}
	return zone
}
