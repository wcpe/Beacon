package service

import (
	"testing"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestFR199CreateNamespaceHasExactlyOneEmptyLobbyCluster 锁定 FR-199：namespace 创建成功时，同一事务必须创建唯一的空大厅集群。
func TestFR199CreateNamespaceHasExactlyOneEmptyLobbyCluster(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, _, err := svc.CreateV2Namespace(CreateV2NamespaceParams{
		Name:     "prod",
		Operator: "admin",
	})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}

	var count int64
	if err := db.Table("lobby_cluster").Where("namespace_id = ?", ns.ID).Count(&count).Error; err != nil {
		t.Fatalf("查询 namespace 的大厅集群失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("新建 namespace 必须恰有一个空大厅集群，实际 %d 个", count)
	}
}

// TestFR199LobbyClusterNamespaceUnique 锁定 namespace 与大厅集群的一对一数据库约束。
func TestFR199LobbyClusterNamespaceUnique(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	ns, _, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "prod", Operator: "admin"})
	if err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	if err := db.Create(&model.LobbyCluster{NamespaceID: ns.ID}).Error; err == nil {
		t.Fatal("同一 namespace 创建第二个大厅集群必须被唯一约束拒绝")
	}
}

// TestFR199LobbyClusterFailureRollsBackNamespace 锁定大厅写入失败时 namespace 不得残留。
func TestFR199LobbyClusterFailureRollsBackNamespace(t *testing.T) {
	db, svc := newV2ControlPlaneTestService(t)
	if err := db.Migrator().DropTable(&model.LobbyCluster{}); err != nil {
		t.Fatalf("删除大厅集群表以模拟写入失败: %v", err)
	}
	if _, _, err := svc.CreateV2Namespace(CreateV2NamespaceParams{Name: "rollback", Operator: "admin"}); err == nil {
		t.Fatal("大厅集群写入失败时 namespace 创建必须失败")
	}
	var count int64
	if err := db.Model(&model.Namespace{}).Where("code = ?", "rollback").Count(&count).Error; err != nil {
		t.Fatalf("统计回滚 namespace 失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("事务失败不得残留 namespace，实际 %d 条", count)
	}
}
