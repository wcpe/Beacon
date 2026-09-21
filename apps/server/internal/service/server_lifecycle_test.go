package service

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
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
	if err := db.AutoMigrate(&model.Namespace{}, &model.BCCluster{}, &model.Region{}, &model.Zone{}, &model.LobbyCluster{}, &model.Server{}, &model.ServerTag{}, &model.AgentIdentity{}, &model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移生命周期测试表失败: %v", err)
	}
	for _, table := range []string{"namespace", "bc_cluster", "region", "zone", "lobby_cluster", "server", "agent_identity", "approval_request", "approval_execution_receipt", "audit_log"} {
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
	var receiptCount int64
	if err := db.Model(&model.ApprovalExecutionReceipt{}).Where("operation_key IN ?", []string{authz.OperationServerArchive, authz.OperationServerRestore}).Count(&receiptCount).Error; err != nil || receiptCount != 2 {
		t.Fatalf("生命周期事实与审批执行回执必须一同持久化，count=%d err=%v", receiptCount, err)
	}
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
	if _, err := approval.Approve(created.RequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("快照漂移批准不应在请求链执行，实际 %v", err)
	}
	if _, err := NewApprovalWorker(approval).RunOnce(); err != nil {
		t.Fatalf("快照漂移 worker 执行失败: %v", err)
	}
	finished, err := approval.Detail(created.RequestID, auth.HumanPrincipal("alice"))
	if err != nil || finished.Status != model.ApprovalStatusFailed || !strings.Contains(finished.FailureSummary, "审批目标已变化") {
		t.Fatalf("快照漂移应进入 failed，实际 request=%+v err=%v", finished, err)
	}
	var saved model.Server
	if err := db.First(&saved, server.ID).Error; err != nil {
		t.Fatalf("读取 server 失败: %v", err)
	}
	if saved.Lifecycle != model.ServerLifecycleActive {
		t.Fatalf("快照漂移后不得归档 server，实际 %q", saved.Lifecycle)
	}
}

func TestTopologyApprovalRejectsSnapshotDriftWithoutSideEffect(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	v2, ok := approval.preparer.(*V2ControlPlaneService)
	if !ok {
		t.Fatal("审批冻结器应为 V2 控制面服务")
	}
	ticket, err := v2.RequestSetServerDefaultEntry(SetServerDefaultEntryParams{
		ServerRowID: server.ID, Value: false, Reason: "取消默认入口",
	}, auth.HumanPrincipal("alice"), "topology-drift")
	if err != nil {
		t.Fatalf("拓扑提审失败: %v", err)
	}
	var created model.ApprovalRequest
	if err := db.Where("request_id = ?", ticket.ApprovalRequestID).First(&created).Error; err != nil {
		t.Fatalf("读取审批申请失败: %v", err)
	}
	var payload defaultEntryPayload
	if err := json.Unmarshal([]byte(created.Payload), &payload); err != nil {
		t.Fatalf("解析冻结载荷失败: %v", err)
	}
	if len(payload.ServerSnapshot) != 1 || payload.ServerSnapshot[0].ID != server.ID || !payload.ServerSnapshot[0].Draining {
		t.Fatalf("拓扑申请应冻结服务端 typed 快照，实际 %+v", payload.ServerSnapshot)
	}
	if err := db.Model(&model.Server{}).Where("id = ?", server.ID).Update("draining", false).Error; err != nil {
		t.Fatalf("制造拓扑漂移失败: %v", err)
	}
	if _, err := approval.Approve(created.RequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("执行漂移审批失败，processed=%d err=%v", processed, err)
	}
	finished, err := approval.Detail(created.RequestID, auth.HumanPrincipal("alice"))
	if err != nil || finished.Status != model.ApprovalStatusFailed || !strings.Contains(finished.FailureSummary, "审批目标已变化") {
		t.Fatalf("拓扑漂移应终止为 failed，request=%+v err=%v", finished, err)
	}
	var saved model.Server
	if err := db.First(&saved, server.ID).Error; err != nil {
		t.Fatalf("读取服务端状态失败: %v", err)
	}
	if !saved.IsDefaultEntry {
		t.Fatalf("拓扑漂移不得修改默认入口，实际 %+v", saved)
	}
}

func TestRequestDefaultEntryByServerIDKeepsDatabaseRowIDInsideService(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	v2, ok := approval.preparer.(*V2ControlPlaneService)
	if !ok {
		t.Fatal("审批冻结器应为 V2 控制面服务")
	}
	ticket, err := v2.RequestSetServerDefaultEntryByServerID(server.ServerID, false, "取消默认入口", "mcp:client", "mcp", "default-entry-by-server-id", auth.MCPPrincipal("client", "自动化", model.MCPClientProfileAutomation))
	if err != nil {
		t.Fatalf("按 serverId 提审失败: %v", err)
	}
	var request model.ApprovalRequest
	if err := db.Where("request_id = ?", ticket.ApprovalRequestID).First(&request).Error; err != nil {
		t.Fatalf("读取审批请求失败: %v", err)
	}
	if request.ResourceID != strconv.FormatUint(uint64(server.ID), 10) || request.OperationKind != authz.OperationTopologyDefaultEntryChange {
		t.Fatalf("服务端应解析并冻结行主键，实际 %+v", request)
	}
}

func TestV2BatchApprovalRejectsCrossNamespaceTargets(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	v2, ok := approval.preparer.(*V2ControlPlaneService)
	if !ok {
		t.Fatal("审批冻结器应为 V2 控制面服务")
	}
	otherNamespace := model.Namespace{Code: "staging", Name: "预发布"}
	if err := db.Create(&otherNamespace).Error; err != nil {
		t.Fatalf("创建第二命名空间失败: %v", err)
	}
	otherServer := model.Server{NamespaceID: otherNamespace.ID, ServerID: "game-2", DisplayName: "游戏二服", Kind: model.ServerKindBackend}
	if err := db.Create(&otherServer).Error; err != nil {
		t.Fatalf("创建第二服务失败: %v", err)
	}
	_, err := v2.RequestAssignServers(AssignServersParams{
		ServerIDs: []uint{server.ID, otherServer.ID}, TargetKind: model.AssignmentTargetZone, TargetID: 1,
		Reason: "批量调整", Operator: "alice",
	}, auth.HumanPrincipal("alice"), "cross-namespace-batch")
	if !errors.Is(err, apperr.ErrSchedCrossNamespace) {
		t.Fatalf("跨命名空间批量提审必须失败关闭，实际 %v", err)
	}
	var count int64
	if err := db.Model(&model.ApprovalRequest{}).Count(&count).Error; err != nil {
		t.Fatalf("统计审批申请失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("跨命名空间批量提审不得创建审批，实际 %d 条", count)
	}
}

func TestV2BatchApprovalPersistsNamespaceAndEvidenceForEachServer(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	v2, ok := approval.preparer.(*V2ControlPlaneService)
	if !ok {
		t.Fatal("审批冻结器应为 V2 控制面服务")
	}
	second := model.Server{NamespaceID: server.NamespaceID, ServerID: "game-2", DisplayName: "游戏二服", Kind: model.ServerKindBackend}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("创建同命名空间服务失败: %v", err)
	}
	ticket, err := v2.RequestAssignServers(AssignServersParams{
		ServerIDs: []uint{server.ID, second.ID}, TargetKind: model.AssignmentTargetZone, TargetID: 1,
		Reason: "批量调整", Operator: "alice",
	}, auth.HumanPrincipal("alice"), "same-namespace-batch")
	if err != nil {
		t.Fatalf("同命名空间批量提审应成功: %v", err)
	}
	var created model.ApprovalRequest
	if err := db.Where("request_id = ?", ticket.ApprovalRequestID).First(&created).Error; err != nil {
		t.Fatalf("读取审批申请失败: %v", err)
	}
	if created.NamespaceID == nil || *created.NamespaceID != server.NamespaceID {
		t.Fatalf("批量审批应持久化唯一命名空间，实际 %+v", created.NamespaceID)
	}
	for _, serverID := range []string{server.ServerID, second.ServerID} {
		if !strings.Contains(created.EvidenceSnapshot, serverID) {
			t.Fatalf("审批快照应包含每个服务目标 %q，实际 %s", serverID, created.EvidenceSnapshot)
		}
	}
	_, evidence, err := approval.DetailEvidence(ticket.ApprovalRequestID, auth.HumanPrincipal("alice"))
	if err != nil || evidence.EvidenceStatus != "available" {
		t.Fatalf("批量审批应读取实时证据，evidence=%+v err=%v", evidence, err)
	}
	for _, serverID := range []string{server.ServerID, second.ServerID} {
		found := false
		for _, line := range evidence.CurrentFactsSummary {
			if line.Value == serverID {
				found = true
			}
		}
		if !found {
			t.Fatalf("实时证据应包含每个服务目标 %q，实际 %+v", serverID, evidence.CurrentFactsSummary)
		}
	}
}

func TestSetServerDrainingAllowsOnlyDirectDraining(t *testing.T) {
	approval, _, server := newServerLifecycleTestSuite(t)
	v2, ok := approval.preparer.(*V2ControlPlaneService)
	if !ok {
		t.Fatal("审批冻结器应为 V2 控制面服务")
	}
	if _, err := v2.SetServerDraining(SetServerDrainingParams{ServerID: server.ServerID, Draining: true, Reason: "开始排空"}); err != nil {
		t.Fatalf("设置排空应直接执行: %v", err)
	}
	if _, err := v2.SetServerDraining(SetServerDrainingParams{ServerID: server.ServerID, Draining: false, Reason: "停止排空"}); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("取消排空必须进入审批，实际 %v", err)
	}
}

func TestRuntimeEffectWaitsForOuterTransactionCommit(t *testing.T) {
	approval, _, _ := newServerLifecycleTestSuite(t)
	v2, ok := approval.preparer.(*V2ControlPlaneService)
	if !ok {
		t.Fatal("审批冻结器应为 V2 控制面服务")
	}
	callbacks := make([]func(), 0, 1)
	v2.afterCommit = func(callback func()) { callbacks = append(callbacks, callback) }
	called := false
	v2.scheduleAfterCommit(func() { called = true })
	if called || len(callbacks) != 1 {
		t.Fatalf("运行时副作用必须在外层事务提交前保持未执行，called=%t callbacks=%d", called, len(callbacks))
	}
	for _, callback := range callbacks {
		callback()
	}
	if !called {
		t.Fatal("外层事务提交后应执行运行时副作用")
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
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("执行 %s 失败，processed=%d err=%v", kind, processed, err)
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
