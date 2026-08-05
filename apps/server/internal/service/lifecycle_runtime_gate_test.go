package service

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

func TestNamespaceLifecycleEvictsRuntimeAndRequiresReregister(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	registry, instances := newLifecycleRuntimeInstanceService(t, db)
	approval.preparer.(*V2ControlPlaneService).SetRuntimeRegistry(registry)
	registerLifecycleRuntimeInstance(t, instances, server)

	namespace := lifecycleNamespace(t, db, server.NamespaceID)
	requestAndApproveNamespaceLifecycle(t, approval, namespace.ID, NamespaceLifecycleParams{Reason: "归档环境", Operator: "alice", ClientIP: "127.0.0.1"}, auth.HumanPrincipal("alice"), auth.HumanPrincipal("bob"), false)
	assertLifecycleRuntimeDisabled(t, instances, registry, namespace.Code, server.ServerID, apperr.ErrNamespaceArchived)

	requestAndApproveNamespaceRestore(t, approval, namespace.ID, NamespaceLifecycleParams{Reason: "恢复环境", Operator: "alice", ClientIP: "127.0.0.1"}, auth.HumanPrincipal("alice"), auth.HumanPrincipal("bob"))
	if _, err := instances.Heartbeat(namespace.Code, server.ServerID); !errors.Is(err, apperr.ErrNotRegistered) {
		t.Fatalf("恢复不得自动复活旧运行实例，实际 %v", err)
	}
	registerLifecycleRuntimeInstance(t, instances, server)
	if got := instances.Discover(runtime.Filter{Namespace: namespace.Code}); len(got) != 1 || got[0].ServerID != server.ServerID {
		t.Fatalf("恢复后仅允许重新注册进入发现，实际 %+v", got)
	}
}

func TestNamespacePermanentDeleteEvictsStaleRuntime(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	registry, instances := newLifecycleRuntimeInstanceService(t, db)
	approval.preparer.(*V2ControlPlaneService).SetRuntimeRegistry(registry)
	registerLifecycleRuntimeInstance(t, instances, server)
	namespace := lifecycleNamespace(t, db, server.NamespaceID)

	requestAndApproveNamespaceLifecycle(t, approval, namespace.ID, NamespaceLifecycleParams{Reason: "归档环境", Operator: "alice", ClientIP: "127.0.0.1"}, auth.HumanPrincipal("alice"), auth.HumanPrincipal("bob"), false)
	// 模拟归档与驱逐之间遗留的运行条目；永久墓碑也必须将其摘除。
	if _, err := registry.Register(&runtime.Instance{Namespace: namespace.Code, ServerID: server.ServerID, Role: "bukkit", Address: "127.0.0.1:25565"}, time.Minute, time.Now().UTC()); err != nil {
		t.Fatalf("写入遗留运行实例失败: %v", err)
	}
	requestAndApproveNamespaceLifecycle(t, approval, namespace.ID, NamespaceLifecycleParams{Reason: "永久删除环境", Operator: "alice", ClientIP: "127.0.0.1", Confirmation: namespace.Code}, auth.HumanPrincipal("alice"), auth.HumanPrincipal("bob"), true)
	assertLifecycleRuntimeDisabled(t, instances, registry, namespace.Code, server.ServerID, apperr.ErrNamespaceArchived)
}

func TestServerArchiveEvictsRuntime(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	registry, instances := newLifecycleRuntimeInstanceService(t, db)
	approval.preparer.(*V2ControlPlaneService).SetRuntimeRegistry(registry)
	registerLifecycleRuntimeInstance(t, instances, server)

	requestAndApproveLifecycle(t, approval, "server.archive", "runtime-archive", server.ID, auth.HumanPrincipal("alice"), auth.HumanPrincipal("bob"))
	assertLifecycleRuntimeDisabled(t, instances, registry, "prod", server.ServerID, apperr.ErrServerArchived)
}

func TestRequireRegisteredRejectsArchivedRuntimeTarget(t *testing.T) {
	approval, db, server := newServerLifecycleTestSuite(t)
	registry, instances := newLifecycleRuntimeInstanceService(t, db)
	approval.preparer.(*V2ControlPlaneService).SetRuntimeRegistry(registry)
	registerLifecycleRuntimeInstance(t, instances, server)

	requestAndApproveLifecycle(t, approval, "server.archive", "runtime-require-registered", server.ID, auth.HumanPrincipal("alice"), auth.HumanPrincipal("bob"))
	if _, err := instances.RequireRegistered("prod", server.ServerID); !errors.Is(err, apperr.ErrServerArchived) {
		t.Fatalf("归档目标不得通过 RequireRegistered，实际 %v", err)
	}
}

func TestGlobalDiscoverExcludesInactiveNamespaces(t *testing.T) {
	_, db, server := newServerLifecycleTestSuite(t)
	registry, instances := newLifecycleRuntimeInstanceService(t, db)
	registerLifecycleRuntimeInstance(t, instances, server)
	for _, lifecycle := range []string{model.NamespaceLifecycleArchived, model.NamespaceLifecycleTombstoned} {
		namespace := &model.Namespace{Code: "inactive-" + lifecycle, Name: "失效环境", Lifecycle: lifecycle}
		if err := db.Create(namespace).Error; err != nil {
			t.Fatalf("创建失效 namespace 失败: %v", err)
		}
		if _, err := registry.Register(&runtime.Instance{Namespace: namespace.Code, ServerID: "game-" + lifecycle, Role: "bukkit", Address: "127.0.0.1:25565"}, time.Minute, time.Now().UTC()); err != nil {
			t.Fatalf("注册失效环境运行实例失败: %v", err)
		}
	}
	if got := instances.Discover(runtime.Filter{}); len(got) != 1 || got[0].Namespace != "prod" || got[0].ServerID != server.ServerID {
		t.Fatalf("全局发现必须排除归档与墓碑 namespace，实际 %+v", got)
	}
}

func newLifecycleRuntimeInstanceService(t *testing.T, db *gorm.DB) (*runtime.Registry, *InstanceService) {
	t.Helper()
	if err := db.AutoMigrate(&model.ServerOffline{}, &model.ZoneAssignment{}); err != nil {
		t.Fatalf("迁移运行实例依赖表失败: %v", err)
	}
	registry := runtime.NewRegistry()
	service := NewInstanceService(db, registry, repository.NewZoneAssignmentRepository(db), repository.NewServerOfflineRepository(db), repository.NewAuditLogRepository(db), time.Second, time.Minute)
	return registry, service
}

func lifecycleNamespace(t *testing.T, db *gorm.DB, id uint) model.Namespace {
	t.Helper()
	var namespace model.Namespace
	if err := db.First(&namespace, id).Error; err != nil {
		t.Fatalf("读取 namespace 失败: %v", err)
	}
	return namespace
}

func registerLifecycleRuntimeInstance(t *testing.T, instances *InstanceService, server model.Server) {
	t.Helper()
	if _, err := instances.Register(RegisterParams{Namespace: "prod", ServerID: server.ServerID, Role: "bukkit", Address: "127.0.0.1:25565"}); err != nil {
		t.Fatalf("注册运行实例失败: %v", err)
	}
}

func assertLifecycleRuntimeDisabled(t *testing.T, instances *InstanceService, registry *runtime.Registry, namespace, serverID string, want error) {
	t.Helper()
	if registry.Get(namespace, serverID) != nil {
		t.Fatal("生命周期变更后不得保留运行实例")
	}
	if _, err := instances.Heartbeat(namespace, serverID); !errors.Is(err, want) {
		t.Fatalf("失效实例心跳必须被拒绝，实际 %v", err)
	}
	backends := []string{"lobby-1"}
	if err := instances.Report(ReportParams{Namespace: namespace, ServerID: serverID, Backends: &backends}); !errors.Is(err, want) {
		t.Fatalf("失效实例上报必须被拒绝，实际 %v", err)
	}
	if got := instances.Discover(runtime.Filter{Namespace: namespace}); len(got) != 0 {
		t.Fatalf("失效环境不得返回发现结果，实际 %+v", got)
	}
}
