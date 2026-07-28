package repository

import (
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestHealthFactsLobbyMemberIsAssigned 确保大厅成员不被健康计算误判为未分配 backend。
func TestHealthFactsLobbyMemberIsAssigned(t *testing.T) {
	db := openRepoSQLite(t, "health_facts_lobby_member")
	namespace := &model.Namespace{Code: "prod", Name: "生产"}
	if err := db.Create(namespace).Error; err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	lobby := &model.LobbyCluster{NamespaceID: namespace.ID}
	if err := db.Create(lobby).Error; err != nil {
		t.Fatalf("创建大厅集群失败: %v", err)
	}
	if err := db.Create(&model.Server{
		NamespaceID: namespace.ID, ServerID: "lobby-1", Kind: model.ServerKindBackend, LobbyClusterID: &lobby.ID,
	}).Error; err != nil {
		t.Fatalf("创建大厅成员失败: %v", err)
	}

	facts, err := NewHealthFactsRepository(db).ListAll()
	if err != nil {
		t.Fatalf("读取健康事实失败: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("应返回一条健康事实，实际 %d", len(facts))
	}
	if facts[0].Unassigned {
		t.Fatalf("大厅成员已归属 LobbyCluster，不应判为未分配：%+v", facts[0])
	}
	if facts[0].LobbyClusterID != lobby.ID || facts[0].NamespaceLobbyClusterID != lobby.ID {
		t.Fatalf("健康事实必须携带大厅归属及 namespace 大厅集群，实际 %+v", facts[0])
	}
}
