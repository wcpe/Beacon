package service

import (
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

func TestNamespaceLifecycleArchiveRestoreKeepsChildrenUntouched(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	var namespace model.Namespace
	if err := db.First(&namespace, server.NamespaceID).Error; err != nil {
		t.Fatalf("读取 namespace 失败: %v", err)
	}
	archive := requestAndApproveNamespaceLifecycle(t, approval, namespace.ID, NamespaceLifecycleParams{Reason: "归档环境", Operator: "alice", ClientIP: "127.0.0.1"}, auth.HumanPrincipal("alice"), auth.HumanPrincipal("bob"), false)
	if archive.Lifecycle != model.NamespaceLifecycleArchived || archive.ArchivedAt == nil || archive.ArchivedBy != "human:alice" {
		t.Fatalf("环境归档状态不正确，实际 %+v", archive)
	}
	assertNamespaceChildrenUnchanged(t, db, server)
	restored := requestAndApproveNamespaceRestore(t, approval, namespace.ID, NamespaceLifecycleParams{Reason: "恢复环境", Operator: "alice", ClientIP: "127.0.0.1"}, auth.HumanPrincipal("alice"), auth.HumanPrincipal("bob"))
	if restored.Lifecycle != model.NamespaceLifecycleActive || restored.ArchivedAt != nil || restored.ArchivedBy != "" {
		t.Fatalf("环境恢复状态不正确，实际 %+v", restored)
	}
	assertNamespaceChildrenUnchanged(t, db, server)
}

func TestServerPermanentDeleteRequiresArchivedAndClosesBinding(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	params := NamespaceLifecycleParams{Reason: "永久删除服务", Operator: "alice", ClientIP: "127.0.0.1", Confirmation: server.ServerID}
	if _, err := approval.preparer.(*V2ControlPlaneService).RequestPermanentDeleteServer(server.ID, params, auth.HumanPrincipal("alice"), "server-tombstone-active"); !errors.Is(err, apperr.ErrServerNotArchived) {
		t.Fatalf("未归档服务不能永久删除，实际 %v", err)
	}
	requestAndApproveLifecycle(t, approval, "server.archive", "server-tombstone-archive", server.ID, auth.HumanPrincipal("alice"), auth.HumanPrincipal("bob"))
	v2 := approval.preparer.(*V2ControlPlaneService)
	created, err := v2.RequestPermanentDeleteServer(server.ID, params, auth.HumanPrincipal("alice"), "server-tombstone")
	if err != nil {
		t.Fatalf("提审永久删除服务失败: %v", err)
	}
	if _, err := approval.Approve(created.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准永久删除服务失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("执行永久删除服务失败，processed=%d err=%v", processed, err)
	}
	var saved model.Server
	if err := db.First(&saved, server.ID).Error; err != nil || saved.Lifecycle != model.ServerLifecycleTombstoned || saved.TombstonedAt == nil {
		t.Fatalf("服务应进入墓碑状态，server=%+v err=%v", saved, err)
	}
	var identity model.AgentIdentity
	if err := db.Where("identity_id = ?", "identity-game-1").First(&identity).Error; err != nil || identity.ServerID != model.NullableServerID(server.ServerID) || identity.Status != model.AgentIdentityStatusUnbound || identity.AssetBindingClosedAt == nil {
		t.Fatalf("永久删除必须关闭身份绑定，identity=%+v err=%v", identity, err)
	}
}

func TestNamespacePermanentDeleteTombstonesArchivedTree(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	v2 := approval.preparer.(*V2ControlPlaneService)
	var namespace model.Namespace
	if err := db.First(&namespace, server.NamespaceID).Error; err != nil {
		t.Fatalf("读取 namespace 失败: %v", err)
	}
	requestAndApproveNamespaceLifecycle(t, approval, namespace.ID, NamespaceLifecycleParams{Reason: "归档环境", Operator: "alice", ClientIP: "127.0.0.1"}, auth.HumanPrincipal("alice"), auth.HumanPrincipal("bob"), false)
	if _, err := v2.RequestPermanentDeleteNamespace(namespace.ID, NamespaceLifecycleParams{Reason: "永久删除环境", Operator: "alice", ClientIP: "127.0.0.1", Confirmation: "错误"}, auth.HumanPrincipal("alice"), "tree-bad-confirm"); !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("确认编码不匹配必须失败，实际 %v", err)
	}
	deleted := requestAndApproveNamespaceLifecycle(t, approval, namespace.ID, NamespaceLifecycleParams{Reason: "永久删除环境", Operator: "alice", ClientIP: "127.0.0.1", Confirmation: namespace.Code}, auth.HumanPrincipal("alice"), auth.HumanPrincipal("bob"), true)
	if deleted.Lifecycle != model.NamespaceLifecycleTombstoned || deleted.TombstonedAt == nil {
		t.Fatalf("环境应进入墓碑状态，实际 %+v", deleted)
	}
	var saved model.Server
	if err := db.First(&saved, server.ID).Error; err != nil || saved.Lifecycle != model.ServerLifecycleTombstoned {
		t.Fatalf("子 server 应原子进入墓碑状态，server=%+v err=%v", saved, err)
	}
	var identity model.AgentIdentity
	if err := db.Where("identity_id = ?", "identity-game-1").First(&identity).Error; err != nil || identity.ServerID != model.NullableServerID(server.ServerID) || identity.Status != model.AgentIdentityStatusUnbound || identity.AssetBindingClosedAt == nil {
		t.Fatalf("子树墓碑必须关闭身份绑定，identity=%+v err=%v", identity, err)
	}
}

func requestAndApproveNamespaceLifecycle(t *testing.T, approval *ApprovalService, namespaceID uint, params NamespaceLifecycleParams, requester, approver auth.Principal, permanent bool) model.Namespace {
	t.Helper()
	v2 := approval.preparer.(*V2ControlPlaneService)
	var ticket ApprovalTicketView
	var err error
	if permanent {
		ticket, err = v2.RequestPermanentDeleteNamespace(namespaceID, params, requester, "namespace-permanent")
	} else {
		ticket, err = v2.RequestArchiveNamespace(namespaceID, params, requester, "namespace-archive")
	}
	if err != nil {
		t.Fatalf("提审环境生命周期操作失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, approver, "127.0.0.2"); err != nil {
		t.Fatalf("批准环境生命周期操作失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("执行环境生命周期操作失败，processed=%d err=%v", processed, err)
	}
	var namespace model.Namespace
	if err := approval.db.First(&namespace, namespaceID).Error; err != nil {
		t.Fatalf("读取环境生命周期结果失败: %v", err)
	}
	return namespace
}

func requestAndApproveNamespaceRestore(t *testing.T, approval *ApprovalService, namespaceID uint, params NamespaceLifecycleParams, requester, approver auth.Principal) model.Namespace {
	t.Helper()
	v2 := approval.preparer.(*V2ControlPlaneService)
	ticket, err := v2.RequestRestoreNamespace(namespaceID, params, requester, "namespace-restore")
	if err != nil {
		t.Fatalf("提审环境恢复失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, approver, "127.0.0.2"); err != nil {
		t.Fatalf("批准环境恢复失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("执行环境恢复失败，processed=%d err=%v", processed, err)
	}
	var namespace model.Namespace
	if err := approval.db.First(&namespace, namespaceID).Error; err != nil {
		t.Fatalf("读取环境恢复结果失败: %v", err)
	}
	return namespace
}

func assertNamespaceChildrenUnchanged(t *testing.T, db *gorm.DB, expected model.Server) {
	t.Helper()
	var server model.Server
	if err := db.First(&server, expected.ID).Error; err != nil || server.Lifecycle != model.ServerLifecycleActive || server.ServerID != expected.ServerID || server.ZoneID == nil || server.BCClusterID == nil || server.LobbyClusterID == nil {
		t.Fatalf("环境归档或恢复不得修改子 server，server=%+v err=%v", server, err)
	}
	var identity model.AgentIdentity
	if err := db.Where("identity_id = ?", "identity-game-1").First(&identity).Error; err != nil || identity.ServerID != model.NullableServerID(expected.ServerID) || identity.Status != model.AgentIdentityStatusActive {
		t.Fatalf("环境归档或恢复不得关闭身份绑定，identity=%+v err=%v", identity, err)
	}
}
