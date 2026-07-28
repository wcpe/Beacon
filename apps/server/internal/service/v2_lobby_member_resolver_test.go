package service

import (
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestLobbyClusterMemberServerIDsProjectsOnlySameNamespaceLobbyMembers 锁定 discovery 的大厅成员投影：
// 同 namespace 的小区服和未分配服均不标记，其他 namespace 成员也不可泄露。
func TestLobbyClusterMemberServerIDsProjectsOnlySameNamespaceLobbyMembers(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	nsA, _, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 prod namespace 失败: %v", err)
	}
	nsB, _, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "stage", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 stage namespace 失败: %v", err)
	}
	var lobbyA model.LobbyCluster
	if err := db.Where("namespace_id = ?", nsA.ID).First(&lobbyA).Error; err != nil {
		t.Fatalf("读取 prod 大厅失败: %v", err)
	}
	var lobbyB model.LobbyCluster
	if err := db.Where("namespace_id = ?", nsB.ID).First(&lobbyB).Error; err != nil {
		t.Fatalf("读取 stage 大厅失败: %v", err)
	}
	zoneID := uint(999)
	if err := db.Create([]model.Server{
		{NamespaceID: nsA.ID, ServerID: "lobby-a", Kind: model.ServerKindBackend, LobbyClusterID: &lobbyA.ID},
		{NamespaceID: nsA.ID, ServerID: "zone-a", Kind: model.ServerKindBackend, ZoneID: &zoneID},
		{NamespaceID: nsA.ID, ServerID: "unassigned-a", Kind: model.ServerKindBackend},
		{NamespaceID: nsB.ID, ServerID: "lobby-b", Kind: model.ServerKindBackend, LobbyClusterID: &lobbyB.ID},
	}).Error; err != nil {
		t.Fatalf("创建服务器归属失败: %v", err)
	}

	members, err := svc.LobbyClusterMemberServerIDs(nsA.Code)
	if err != nil {
		t.Fatalf("解析大厅成员失败: %v", err)
	}
	if !members["lobby-a"] || members["zone-a"] || members["unassigned-a"] || members["lobby-b"] {
		t.Fatalf("大厅成员投影泄露或误标，实际 %v", members)
	}
}
