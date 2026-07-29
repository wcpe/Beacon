package service

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

func newServerLifecycleTestSuite(t *testing.T) (*ApprovalService, *gorm.DB, model.Server) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:server_lifecycle?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Namespace{}, &model.BCCluster{}, &model.Region{}, &model.Zone{}, &model.LobbyCluster{}, &model.Server{}, &model.AgentIdentity{}, &model.ApprovalRequest{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移生命周期测试表失败: %v", err)
	}
	for _, table := range []string{"namespace", "bc_cluster", "region", "zone", "lobby_cluster", "server", "agent_identity", "approval_request", "audit_log"} {
		if err := db.Exec("DELETE FROM " + table).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", table, err)
		}
	}
	server := seedLifecycleServer(t, db)
	registry := authz.NewApprovalRegistry()
	v2 := NewV2ControlPlaneService(db)
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	v2.SetApprovalService(approval)
	RegisterV2ControlPlaneApprovalAdapters(registry, v2)
	return approval, db, server
}

func seedLifecycleServer(t *testing.T, db *gorm.DB) model.Server {
	t.Helper()
	namespace := model.Namespace{Code: "prod", Name: "生产"}
	if err := db.Create(&namespace).Error; err != nil {
		t.Fatalf("写入 namespace 测试数据失败: %v", err)
	}
	cluster, zone := seedLifecycleTopology(t, db, namespace.ID)
	lobby := model.LobbyCluster{NamespaceID: namespace.ID}
	if err := db.Create(&lobby).Error; err != nil {
		t.Fatalf("写入大厅集群测试数据失败: %v", err)
	}
	server := model.Server{NamespaceID: namespace.ID, ServerID: "game-1", DisplayName: "游戏一服", Kind: model.ServerKindBackend, BCClusterID: &cluster.ID, ZoneID: &zone.ID, LobbyClusterID: &lobby.ID, PendingZoneID: &zone.ID, PendingBCClusterID: &cluster.ID, IsDefaultEntry: true, Draining: true}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("写入 server 测试数据失败: %v", err)
	}
	seedLifecycleIdentity(t, db, namespace.ID, server)
	return server
}

func seedLifecycleTopology(t *testing.T, db *gorm.DB, namespaceID uint) (model.BCCluster, model.Zone) {
	t.Helper()
	cluster := model.BCCluster{NamespaceID: namespaceID, Code: "bc-1", Name: "BC 1"}
	if err := db.Create(&cluster).Error; err != nil {
		t.Fatalf("写入 BC 集群测试数据失败: %v", err)
	}
	region := model.Region{BCClusterID: cluster.ID, Code: "region-1", Name: "大区 1"}
	if err := db.Create(&region).Error; err != nil {
		t.Fatalf("写入大区测试数据失败: %v", err)
	}
	zone := model.Zone{RegionID: region.ID, Code: "zone-1", Name: "小区 1"}
	if err := db.Create(&zone).Error; err != nil {
		t.Fatalf("写入小区测试数据失败: %v", err)
	}
	return cluster, zone
}

func seedLifecycleIdentity(t *testing.T, db *gorm.DB, namespaceID uint, server model.Server) {
	t.Helper()
	identity := model.AgentIdentity{IdentityID: "identity-game-1", NamespaceID: namespaceID, ServerID: model.NullableServerID(server.ServerID), Kind: server.Kind, Status: model.AgentIdentityStatusActive}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatalf("写入身份测试数据失败: %v", err)
	}
}

func lifecycleRequest(kind, key string, _ uint) authz.Operation {
	return authz.Operation{Kind: kind, Resource: "client-forged", ResourceID: "forged", IdempotencyKey: key, RiskLevel: "low", Reason: "生命周期变更原因"}
}

func lifecyclePayload(serverID uint) map[string]any {
	return map[string]any{
		"parameters": map[string]any{"serverRowId": float64(serverID)},
		"operator":   "client-forged",
		"clientIP":   "198.51.100.7",
		"snapshot":   map[string]any{"lifecycle": "archived"},
	}
}

func TestServerLifecycleDefaultsToActive(t *testing.T) {
	_, db, server := newServerLifecycleTestSuite(t)
	var saved model.Server
	if err := db.First(&saved, server.ID).Error; err != nil {
		t.Fatalf("读取 server 失败: %v", err)
	}
	if saved.Lifecycle != model.ServerLifecycleActive || saved.ArchivedAt != nil || saved.ArchivedBy != "" || saved.ArchiveReason != "" {
		t.Fatalf("新建 server 应为 active 且无归档元数据，实际 %+v", saved)
	}
}

func TestServerLifecycleRequestFreezesTrustedSnapshot(t *testing.T) {
	approval, _, server := newServerLifecycleTestSuite(t)
	created, err := approval.Request(lifecycleRequest(authz.OperationServerArchive, "freeze", server.ID), lifecyclePayload(server.ID), auth.HumanPrincipal("alice"), "127.0.0.1")
	if err != nil {
		t.Fatalf("提审失败: %v", err)
	}
	if created.ResourceType != model.TargetTypeServer || created.ResourceID != strconv.FormatUint(uint64(server.ID), 10) || created.RiskLevel != "high" {
		t.Fatalf("审批目标与风险等级应由服务端冻结，实际 %+v", created)
	}
	var payload serverLifecyclePayload
	if err := json.Unmarshal([]byte(created.Payload), &payload); err != nil {
		t.Fatalf("解析冻结载荷失败: %v", err)
	}
	if payload.SchemaVersion != approvalSchemaVersion || payload.OperationKey != authz.OperationServerArchive || payload.Operator != "human:alice" || payload.ClientIP != "127.0.0.1" || payload.Server.ID != server.ID || payload.Server.NamespaceID != server.NamespaceID || payload.Server.DisplayName != server.DisplayName || payload.Server.Lifecycle != model.ServerLifecycleActive || len(payload.Server.Identities) != 1 {
		t.Fatalf("客户端伪造的快照或操作者不应进入冻结载荷，实际 %+v", payload)
	}
}

func TestServerLifecycleRequestAcceptsRESTParameters(t *testing.T) {
	approval, _, server := newServerLifecycleTestSuite(t)
	created, err := approval.Request(
		lifecycleRequest(authz.OperationServerArchive, "rest-parameters", server.ID),
		map[string]any{"serverRowId": float64(server.ID)},
		auth.HumanPrincipal("alice"),
		"127.0.0.1",
	)
	if err != nil {
		t.Fatalf("REST 参数形态提审失败: %v", err)
	}
	if created.ResourceID != strconv.FormatUint(uint64(server.ID), 10) {
		t.Fatalf("REST 参数未冻结为目标 server，实际 %+v", created)
	}
}

func TestServerLifecycleArchiveRestorePreservesBindings(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	requester := auth.HumanPrincipal("alice")
	approver := auth.HumanPrincipal("bob")
	archived := requestAndApproveLifecycle(t, approval, authz.OperationServerArchive, "archive", server.ID, requester, approver)
	assertArchivedServer(t, archived, server)
	restored := requestAndApproveLifecycle(t, approval, authz.OperationServerRestore, "restore", server.ID, requester, approver)
	if restored.Lifecycle != model.ServerLifecycleActive || restored.ArchivedAt != nil || restored.ArchivedBy != "" || restored.ArchiveReason != "" {
		t.Fatalf("恢复后应清除归档元数据，实际 %+v", restored)
	}
	assertServerBindings(t, restored, server)
	var identity model.AgentIdentity
	if err := db.Where("identity_id = ?", "identity-game-1").First(&identity).Error; err != nil {
		t.Fatalf("读取身份绑定失败: %v", err)
	}
	if identity.NamespaceID != server.NamespaceID || identity.ServerID != model.NullableServerID(server.ServerID) {
		t.Fatalf("归档与恢复不得改变身份归属，实际 %+v", identity)
	}
	assertServerLifecycleAudit(t, db, model.ActionServerArchive)
	assertServerLifecycleAudit(t, db, model.ActionServerRestore)
}

func assertServerLifecycleAudit(t *testing.T, db *gorm.DB, action string) {
	t.Helper()
	var count int64
	if err := db.Model(&model.AuditLog{}).Where("action = ?", action).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("应写入一条 %s 业务审计，count=%d err=%v", action, count, err)
	}
}

func TestServerLifecycleApprovalRejectsSnapshotDrift(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	created, err := approval.Request(lifecycleRequest(authz.OperationServerArchive, "drift", server.ID), lifecyclePayload(server.ID), auth.HumanPrincipal("alice"), "127.0.0.1")
	if err != nil {
		t.Fatalf("提审失败: %v", err)
	}
	if err := db.Model(&model.Server{}).Where("id = ?", server.ID).Update("draining", false).Error; err != nil {
		t.Fatalf("制造快照漂移失败: %v", err)
	}
	if _, err := approval.Approve(created.RequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); !errors.Is(err, apperr.ErrApprovalTargetChanged) {
		t.Fatalf("快照漂移应拒绝执行，实际 %v", err)
	}
	var saved model.Server
	if err := db.First(&saved, server.ID).Error; err != nil {
		t.Fatalf("读取 server 失败: %v", err)
	}
	if saved.Lifecycle != model.ServerLifecycleActive {
		t.Fatalf("快照漂移后不得归档 server，实际 %q", saved.Lifecycle)
	}
}

func TestServerLifecycleRejectsRepeatedState(t *testing.T) {
	approval, _, server := newServerLifecycleTestSuite(t)
	requester := auth.HumanPrincipal("alice")
	approver := auth.HumanPrincipal("bob")
	requestAndApproveLifecycle(t, approval, authz.OperationServerArchive, "archive-once", server.ID, requester, approver)
	if _, err := approval.Request(lifecycleRequest(authz.OperationServerArchive, "archive-twice", server.ID), lifecyclePayload(server.ID), requester, "127.0.0.1"); !errors.Is(err, apperr.ErrServerNotActive) {
		t.Fatalf("重复归档应拒绝非 active server，实际 %v", err)
	}
	requestAndApproveLifecycle(t, approval, authz.OperationServerRestore, "restore-once", server.ID, requester, approver)
	if _, err := approval.Request(lifecycleRequest(authz.OperationServerRestore, "restore-twice", server.ID), lifecyclePayload(server.ID), requester, "127.0.0.1"); !errors.Is(err, apperr.ErrServerNotArchived) {
		t.Fatalf("重复恢复应拒绝非 archived server，实际 %v", err)
	}
}

func requestAndApproveLifecycle(t *testing.T, approval *ApprovalService, kind, key string, serverID uint, requester, approver auth.Principal) model.Server {
	t.Helper()
	created, err := approval.Request(lifecycleRequest(kind, key, serverID), lifecyclePayload(serverID), requester, "127.0.0.1")
	if err != nil {
		t.Fatalf("提审 %s 失败: %v", kind, err)
	}
	if _, err := approval.Approve(created.RequestID, approver, "127.0.0.2"); err != nil {
		t.Fatalf("批准 %s 失败: %v", kind, err)
	}
	var server model.Server
	if err := approval.db.Where("id = ?", serverID).First(&server).Error; err != nil {
		t.Fatalf("读取执行结果失败: %v", err)
	}
	return server
}

func assertArchivedServer(t *testing.T, archived, original model.Server) {
	t.Helper()
	if archived.Lifecycle != model.ServerLifecycleArchived || archived.ArchivedAt == nil || archived.ArchivedBy != "human:alice" || archived.ArchiveReason != "生命周期变更原因" {
		t.Fatalf("归档元数据不完整，实际 %+v", archived)
	}
	assertServerBindings(t, archived, original)
}

func assertServerBindings(t *testing.T, actual, expected model.Server) {
	t.Helper()
	if actual.NamespaceID != expected.NamespaceID || actual.ServerID != expected.ServerID || actual.DisplayName != expected.DisplayName || actual.Kind != expected.Kind || actual.BCClusterID == nil || actual.ZoneID == nil || actual.LobbyClusterID == nil || actual.PendingZoneID == nil || actual.PendingBCClusterID == nil || *actual.BCClusterID != *expected.BCClusterID || *actual.ZoneID != *expected.ZoneID || *actual.LobbyClusterID != *expected.LobbyClusterID || *actual.PendingZoneID != *expected.PendingZoneID || *actual.PendingBCClusterID != *expected.PendingBCClusterID || actual.IsDefaultEntry != expected.IsDefaultEntry || actual.Draining != expected.Draining {
		t.Fatalf("生命周期切换不得改变绑定和归属，实际 %+v", actual)
	}
}
