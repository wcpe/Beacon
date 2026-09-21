package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

func newNamespaceRuntimeGateDB(t *testing.T, lifecycle string) (*gorm.DB, *model.Namespace) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:namespace_runtime_gate?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Namespace{}, &model.Server{}, &model.ServerTag{}, &model.AgentIdentity{}, &model.AgentCommand{}); err != nil {
		t.Fatalf("迁移运行资格测试表失败: %v", err)
	}
	for _, table := range []string{"agent_command", "agent_identity", "server", "namespace"} {
		if err := db.Exec("DELETE FROM " + table).Error; err != nil {
			t.Fatalf("清理 %s 失败: %v", table, err)
		}
	}
	namespace := &model.Namespace{Code: "runtime-gate", Name: "运行资格", Lifecycle: lifecycle}
	if err := db.Create(namespace).Error; err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	server := &model.Server{NamespaceID: namespace.ID, ServerID: "game-1", Kind: model.ServerKindBackend}
	if err := db.Create(server).Error; err != nil {
		t.Fatalf("创建 server 失败: %v", err)
	}
	return db, namespace
}

func TestNamespaceRuntimeGateRejectsInactiveLifecycle(t *testing.T) {
	for _, lifecycle := range []string{model.NamespaceLifecycleArchived, model.NamespaceLifecycleTombstoned} {
		t.Run(lifecycle, func(t *testing.T) {
			db, namespace := newNamespaceRuntimeGateDB(t, lifecycle)
			assertNamespaceRuntimeDenied(t, ensureNamespaceRuntimeActiveByID(db, namespace.ID))
			assertNamespaceRuntimeDenied(t, ensureNamespaceRuntimeActiveByCode(db, namespace.Code))
			assertNamespaceRuntimeDenied(t, ensureServerActiveForNamespace(db, namespace.Code, "game-1"))
			assertNamespaceRuntimeDenied(t, ensureRegistrationServerActive(db, namespace.ID, nil, "game-1"))
			assertNamespaceRuntimeDenied(t, repository.NewAgentCommandRepository(db).Create(&model.AgentCommand{
				NamespaceCode: namespace.Code, ServerID: "game-1", Type: model.CommandTypeAssetRescan, Status: model.CommandStatusPending,
			}))
			late := &model.AgentCommand{NamespaceCode: namespace.Code, ServerID: "game-1", Type: model.CommandTypeAssetRescan, Status: model.CommandStatusFetched}
			if err := db.Create(late).Error; err != nil {
				t.Fatalf("写入迟到回传命令失败: %v", err)
			}
			_, err := repository.NewAgentCommandRepository(db).UpdateStatus(late.ID, model.CommandStatusFetched, model.CommandStatusDone, "")
			assertNamespaceRuntimeDenied(t, err)
			_, err = NewAssetService(db, nil, nil, nil, nil).ApplyManifest(ManifestReportParams{
				Identity: agentauth.Identity{NamespaceID: namespace.ID, Namespace: namespace.Code, ServerID: "game-1"}, Mode: manifestModeDelta,
			})
			assertNamespaceRuntimeDenied(t, err)
			assertNamespaceRuntimeDenied(t, (&DeliveryBlobService{db: db}).AuthorizeBlobUpload(
				agentauth.Identity{NamespaceID: namespace.ID, Namespace: namespace.Code, ServerID: "game-1"}, strings.Repeat("a", 64)))
			assertNamespaceRuntimeDenied(t, ensureFileSyncTaskRuntimeActive(db, &model.FileSyncTask{NamespaceCode: namespace.Code}))
			if _, err := (&FileSyncService{db: db}).CreateTask(CreateFileSyncTaskParams{
				Namespace: namespace.Code, SourceServerID: "game-1", Directory: "plugins/demo", BatchSize: 1, Operator: "admin",
			}); err != nil {
				assertNamespaceRuntimeDenied(t, err)
			} else {
				t.Fatal("归档 namespace 不得创建文件同步投递")
			}
			if _, err := changeNamespaceCode(db, namespace.ID); err != nil {
				assertNamespaceRuntimeDenied(t, err)
			} else {
				t.Fatal("归档 namespace 不得进入交付状态迁移")
			}
		})
	}
}

func TestNamespaceRuntimeGateRejectsSelectionPaths(t *testing.T) {
	db, namespace := newNamespaceRuntimeGateDB(t, model.NamespaceLifecycleArchived)
	v2 := NewV2ControlPlaneService(db)
	if _, err := v2.DefaultEntryServerIDs(namespace.Code); err == nil {
		t.Fatal("归档 namespace 不得参与默认入口选择")
	} else {
		assertNamespaceRuntimeDenied(t, err)
	}
	v2.SetDirectoryResyncCommandPort(repository.NewAgentCommandRepository(db), nil)
	if _, err := v2.RequestNamespaceDirectoryResync(namespace.ID, "admin", ""); err == nil {
		t.Fatal("归档 namespace 不得创建目录重同步命令")
	} else {
		assertNamespaceRuntimeDenied(t, err)
	}
	scheduling := NewSchedulingService(db, nil, nil, runtime.NewRegistry())
	if _, err := scheduling.Placement(namespace.Code, "", "zone-a"); err == nil {
		t.Fatal("归档 namespace 不得参与调度候选选择")
	} else {
		assertNamespaceRuntimeDenied(t, err)
	}
}

func TestNamespaceRuntimeGateRejectsClosedIdentityBinding(t *testing.T) {
	now := time.Now().UTC()
	err := ensureIdentityRuntimeBindingOpen(&model.AgentIdentity{AssetBindingClosedAt: &now})
	if !errors.Is(err, apperr.ErrServerArchived) {
		t.Fatalf("已关闭资产绑定必须被拒绝，实际 %v", err)
	}
}

func assertNamespaceRuntimeDenied(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, apperr.ErrNamespaceArchived) {
		t.Fatalf("运行路径应因 namespace 非 active 被拒绝，实际 %v", err)
	}
}
