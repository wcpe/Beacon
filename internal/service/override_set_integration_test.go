//go:build integration

package service_test

import (
	"testing"

	"beacon/internal/apperr"
	"beacon/internal/model"
	"beacon/internal/repository"
	"beacon/internal/service"
)

// newOverrideStack 装配覆盖集服务（FR-15）+ 文件仓库（用于成员关联与 dry-run）。
func newOverrideStack(t *testing.T) (*service.OverrideSetService, *repository.FileObjectRepository) {
	db := testDB(t)
	setRepo := repository.NewFileOverrideSetRepository(db)
	revRepo := repository.NewFileOverrideSetRevisionRepository(db)
	fileRepo := repository.NewFileObjectRepository(db)
	auditRepo := repository.NewAuditLogRepository(db)
	svc := service.NewOverrideSetService(db, setRepo, revRepo, fileRepo, auditRepo)
	return svc, fileRepo
}

// TestOverrideSetLifecycle 集成验证：建→发布→历史→回滚→软删，覆盖集事实正确流转。
func TestOverrideSetLifecycle(t *testing.T) {
	svc, _ := newOverrideStack(t)
	set, err := svc.Create(service.CreateOverrideSetParams{
		Namespace: "prod", Group: model.GlobalGroupCode, Name: "AllinCore",
		ScopeLevel: model.ScopeGlobal, TargetRoot: "plugins/AllinCore",
		ReloadCommand: "allin reload", Operator: "alice", Comment: "首次",
	})
	if err != nil {
		t.Fatalf("建覆盖集失败: %v", err)
	}
	if set.Version != 1 || set.Mode != model.OverrideModeFileOverride {
		t.Fatalf("期望 version=1 mode=file-override，得 v=%d mode=%s", set.Version, set.Mode)
	}

	// 发布新版本：改命令。
	pub, err := svc.Publish(set.ID, service.PublishOverrideSetParams{
		TargetRoot: "plugins/AllinCore", ReloadCommand: "allin reload all", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	if pub.Version != 2 || pub.ReloadCommand != "allin reload all" {
		t.Fatalf("期望 v2 且命令更新，得 v=%d cmd=%q", pub.Version, pub.ReloadCommand)
	}

	revs, err := svc.ListRevisions(set.ID)
	if err != nil || len(revs) != 2 {
		t.Fatalf("历史应 2 条，得 %d，err=%v", len(revs), err)
	}

	// 回滚到 v1：还原命令，version+1=3。回滚只还原事实，不重放命令（命令重放由 agent 侧禁止）。
	rb, err := svc.Rollback(set.ID, 1, "alice", "回滚", "")
	if err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	if rb.Version != 3 || rb.ReloadCommand != "allin reload" {
		t.Fatalf("回滚应还原 v1 命令、version=3，得 v=%d cmd=%q", rb.Version, rb.ReloadCommand)
	}

	if err := svc.Delete(set.ID, "alice", "下线", ""); err != nil {
		t.Fatalf("软删失败: %v", err)
	}
	if _, err := svc.Get(set.ID); err != apperr.ErrOverrideSetNotFound {
		t.Fatalf("软删后应找不到，得 %v", err)
	}
}

// TestOverrideSetDryRunNoPersist dry-run 只读预览：返回将覆盖的成员清单 + 命令，且绝不落任何东西。
func TestOverrideSetDryRunNoPersist(t *testing.T) {
	svc, fileRepo := newOverrideStack(t)
	set, err := svc.Create(service.CreateOverrideSetParams{
		Namespace: "prod", Group: model.GlobalGroupCode, Name: "AllinCore",
		ScopeLevel: model.ScopeGlobal, TargetRoot: "plugins/AllinCore",
		ReloadCommand: "allin reload", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("建覆盖集失败: %v", err)
	}
	// 关联两个成员文件。
	for _, p := range []string{"config.yml", "scripts/hello.js"} {
		obj := &model.FileObject{
			NamespaceCode: "prod", GroupCode: model.GlobalGroupCode, Path: p,
			ScopeLevel: model.ScopeGlobal, Content: "x", ContentMD5: "x",
			Version: 1, Enabled: true, OverrideSetID: set.ID,
		}
		if err := fileRepo.Create(obj); err != nil {
			t.Fatalf("建成员文件失败: %v", err)
		}
	}

	preview, err := svc.DryRun(set.ID)
	if err != nil {
		t.Fatalf("dry-run 失败: %v", err)
	}
	if len(preview.MemberPaths) != 2 {
		t.Fatalf("dry-run 应列出 2 个成员，得 %d", len(preview.MemberPaths))
	}
	if preview.CommandFirstToken != "allin" {
		t.Fatalf("命令首 token 应为 allin，得 %q", preview.CommandFirstToken)
	}

	// dry-run 不改版本、不产生新历史（不落任何东西）。
	after, _ := svc.Get(set.ID)
	if after.Version != 1 {
		t.Fatalf("dry-run 后版本不应变，得 %d", after.Version)
	}
	revs, _ := svc.ListRevisions(set.ID)
	if len(revs) != 1 {
		t.Fatalf("dry-run 后历史不应增，得 %d", len(revs))
	}
}

// TestOverrideSetRejectInvalid 控制面早校验：非法 target_root / 含注入字符的命令被拒。
func TestOverrideSetRejectInvalid(t *testing.T) {
	svc, _ := newOverrideStack(t)
	_, err := svc.Create(service.CreateOverrideSetParams{
		Namespace: "prod", Group: model.GlobalGroupCode, Name: "Bad",
		ScopeLevel: model.ScopeGlobal, TargetRoot: "plugins/../etc",
		ReloadCommand: "allin reload", Operator: "alice",
	})
	if err != apperr.ErrInvalidTargetRoot {
		t.Fatalf("非法目标根应拒，得 %v", err)
	}
	_, err = svc.Create(service.CreateOverrideSetParams{
		Namespace: "prod", Group: model.GlobalGroupCode, Name: "Bad2",
		ScopeLevel: model.ScopeGlobal, TargetRoot: "plugins/AllinCore",
		ReloadCommand: "allin reload; rm -rf /", Operator: "alice",
	})
	if err != apperr.ErrInvalidReloadCommand {
		t.Fatalf("注入命令应拒，得 %v", err)
	}
}
