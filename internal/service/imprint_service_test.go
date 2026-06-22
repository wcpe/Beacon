package service

import (
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/filetree"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// newImprintSvc 构造带 FileEffectiveService 注入的命令服务（FR-46 拓印需解期望合并值）。
func newImprintSvc(db *gorm.DB) *AgentCommandService {
	cmdRepo := repository.NewAgentCommandRepository(db)
	auditRepo := repository.NewAuditLogRepository(db)
	fileRepo := repository.NewFileObjectRepository(db)
	fileSvc := NewFileService(db, fileRepo, repository.NewFileRevisionRepository(db), auditRepo)
	effSvc := NewFileEffectiveService(fileRepo, repository.NewZoneAssignmentRepository(db), nil)
	svc := NewAgentCommandService(db, cmdRepo, fileSvc, auditRepo)
	svc.SetFileEffectiveService(effSvc)
	return svc
}

// seedGroupFile 直插一条组级文件覆盖（构造期望合并值的底层）。
func seedGroupFile(t *testing.T, db *gorm.DB, ns, group, path, content string) {
	t.Helper()
	obj := &model.FileObject{
		NamespaceCode: ns, GroupCode: group, Path: path,
		ScopeLevel: model.ScopeGroup, ScopeTarget: "",
		Content: content, ContentMD5: filetree.ContentMD5(content), Version: 1, Enabled: true,
	}
	if err := repository.NewFileObjectRepository(db).Create(obj); err != nil {
		t.Fatalf("插组级文件失败: %v", err)
	}
}

// TestRequestImprint 触发拓印即建 pending 命令（载荷 mode=imprint + path）并记 file.imprint-fetch 审计。
func TestRequestImprint(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newImprintSvc(db)
	cmd, err := svc.RequestImprint("prod", "lobby-1", "AllinCore/config.yml", "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("触发拓印失败: %v", err)
	}
	if cmd.Status != model.CommandStatusPending || cmd.Type != model.CommandTypeIngestPlugins {
		t.Fatalf("命令应 pending/ingest-plugins，实际 %+v", cmd)
	}
	if countAudit(t, db, model.ActionFileImprintFetch) != 1 {
		t.Fatal("应记一条 file.imprint-fetch 审计")
	}
	// 缺 path 应拒
	if _, err := svc.RequestImprint("prod", "lobby-1", "", "alice", ""); err == nil {
		t.Fatal("缺 path 应拒")
	}
	// 非法 path 应拒
	if _, err := svc.RequestImprint("prod", "lobby-1", "../escape.yml", "alice", ""); err != apperr.ErrInvalidPath {
		t.Fatalf("非法 path 应 ErrInvalidPath，实际 %v", err)
	}
}

// TestImprintReceiveTransfersNotLand 拓印回传：取指定 path 转存命令、命令转 ready，不落 file_object、不记 file.import。
func TestImprintReceiveTransfersNotLand(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newImprintSvc(db)
	cmd, _ := svc.RequestImprint("prod", "lobby-1", "AllinCore/config.yml", "alice", "")
	got, _ := svc.FetchPending("prod", "lobby-1")
	if got.ID != cmd.ID {
		t.Fatalf("应取到刚建命令")
	}
	// agent 回传整棵树（含目标 path 与其它文件）
	_, err := svc.ReceiveIngest(cmd.ID, []ImportFile{
		{Path: "AllinCore/config.yml", Content: "a: 99\n"},
		{Path: "AllinCore/lang.yml", Content: "hi: hello\n"},
	}, "10.0.0.2")
	if err != nil {
		t.Fatalf("拓印回传应成功: %v", err)
	}
	// 命令转 ready 且转存目标 path 内容
	reloaded, _ := repository.NewAgentCommandRepository(db).FindByID(cmd.ID)
	if reloaded.Status != model.CommandStatusReady {
		t.Fatalf("拓印回传后命令应 ready，实际 %s", reloaded.Status)
	}
	if reloaded.ImprintContent != "a: 99\n" {
		t.Fatalf("应转存目标 path 内容，实际 %q", reloaded.ImprintContent)
	}
	// 不落 file_object、不记 file.import
	fileRepo := repository.NewFileObjectRepository(db)
	if obj, _ := fileRepo.FindByIdentity("prod", "area1", "AllinCore/config.yml", model.ScopeGroup, ""); obj != nil {
		t.Fatal("拓印不应落 file_object")
	}
	if countAudit(t, db, model.ActionFileImport) != 0 {
		t.Fatal("拓印不应记 file.import 审计")
	}
}

// TestImprintReceiveMissingPathFails 指定 path 不在回传树中 → 命令 failed、不转存。
func TestImprintReceiveMissingPathFails(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newImprintSvc(db)
	cmd, _ := svc.RequestImprint("prod", "lobby-1", "AllinCore/missing.yml", "alice", "")
	_, _ = svc.FetchPending("prod", "lobby-1")
	_, err := svc.ReceiveIngest(cmd.ID, []ImportFile{{Path: "AllinCore/other.yml", Content: "x: 1\n"}}, "")
	if err == nil {
		t.Fatal("目标 path 缺失应报错")
	}
	reloaded, _ := repository.NewAgentCommandRepository(db).FindByID(cmd.ID)
	if reloaded.Status != model.CommandStatusFailed {
		t.Fatalf("目标 path 缺失命令应 failed，实际 %s", reloaded.Status)
	}
}

// TestImprintDiff 解期望合并值（FR-45）与本地实际值组装 diff；非 ready 拒。
func TestImprintDiff(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newImprintSvc(db)
	// 组级已有该文件 {a:1}，server 实际盘上是 {a:99}
	seedGroupFile(t, db, "prod", "area1", "AllinCore/config.yml", "a: 1\n")
	cmd, _ := svc.RequestImprint("prod", "lobby-1", "AllinCore/config.yml", "alice", "")
	_, _ = svc.FetchPending("prod", "lobby-1")

	// 未 ready（仍 fetched）→ 拒 diff
	if _, err := svc.ImprintDiff(cmd.ID, model.ScopeServer, "area1", "", "lobby-1"); err != apperr.ErrImprintNotReady {
		t.Fatalf("非 ready 应 ErrImprintNotReady，实际 %v", err)
	}
	_, _ = svc.ReceiveIngest(cmd.ID, []ImportFile{{Path: "AllinCore/config.yml", Content: "a: 99\n"}}, "")

	diff, err := svc.ImprintDiff(cmd.ID, model.ScopeServer, "area1", "", "lobby-1")
	if err != nil {
		t.Fatalf("diff 应成功: %v", err)
	}
	if diff.Path != "AllinCore/config.yml" {
		t.Fatalf("diff path 不符: %s", diff.Path)
	}
	if diff.ActualContent != "a: 99\n" {
		t.Fatalf("本地实际值应为 a:99，实际 %q", diff.ActualContent)
	}
	// 期望合并值来自组级 {a:1}（lobby-1 未指派，按 group=area1 解析）
	if diff.ExpectedContent != "a: 1\n" {
		t.Fatalf("期望合并值应为 a:1，实际 %q", diff.ExpectedContent)
	}
	if !diff.Differs {
		t.Fatal("a:99 与 a:1 应判为有差异")
	}
}

// TestConfirmImprintSelfReviewGate 自审门：reviewedMd5 匹配才落库，错误 md5 拒、不落库。
func TestConfirmImprintSelfReviewGate(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newImprintSvc(db)
	cmd, _ := svc.RequestImprint("prod", "lobby-1", "AllinCore/config.yml", "alice", "")
	_, _ = svc.FetchPending("prod", "lobby-1")
	_, _ = svc.ReceiveIngest(cmd.ID, []ImportFile{{Path: "AllinCore/config.yml", Content: "a: 99\n"}}, "")
	actualMD5 := filetree.ContentMD5("a: 99\n")

	// 错误 reviewedMd5 → 412，不落库
	if _, err := svc.ConfirmImprint(cmd.ID, model.ScopeServer, "area1", "", "lobby-1", "deadbeef", "alice", ""); err != apperr.ErrImprintReviewMismatch {
		t.Fatalf("错误 md5 应 ErrImprintReviewMismatch，实际 %v", err)
	}
	fileRepo := repository.NewFileObjectRepository(db)
	if obj, _ := fileRepo.FindByIdentity("prod", "area1", "AllinCore/config.yml", model.ScopeServer, "lobby-1"); obj != nil {
		t.Fatal("自审失败不应落库")
	}
	// 命令仍 ready（可重确认）
	if c, _ := repository.NewAgentCommandRepository(db).FindByID(cmd.ID); c.Status != model.CommandStatusReady {
		t.Fatalf("自审失败命令应仍 ready，实际 %s", c.Status)
	}

	// 正确 md5 → 落 server 层覆盖、命令 done、清空瞬态、记 file.imprint
	res, err := svc.ConfirmImprint(cmd.ID, model.ScopeServer, "area1", "", "lobby-1", actualMD5, "alice", "10.0.0.9")
	if err != nil {
		t.Fatalf("正确自审应落库: %v", err)
	}
	if res.ScopeLevel != model.ScopeServer || res.MD5 != actualMD5 {
		t.Fatalf("落库结果不符: %+v", res)
	}
	obj, _ := fileRepo.FindByIdentity("prod", "area1", "AllinCore/config.yml", model.ScopeServer, "lobby-1")
	if obj == nil || obj.Content != "a: 99\n" {
		t.Fatalf("应落 server 层覆盖，实际 %+v", obj)
	}
	c, _ := repository.NewAgentCommandRepository(db).FindByID(cmd.ID)
	if c.Status != model.CommandStatusDone {
		t.Fatalf("确认后命令应 done，实际 %s", c.Status)
	}
	if c.ImprintContent != "" {
		t.Fatalf("确认后瞬态内容应清空，实际 %q", c.ImprintContent)
	}
	if countAudit(t, db, model.ActionFileImprint) != 1 {
		t.Fatal("确认落库应记一条 file.imprint 审计")
	}
}

// TestConfirmImprintPublishesWhenExists 该层 path 已存在 → confirm 发布新版本（非 create 冲突）。
func TestConfirmImprintPublishesWhenExists(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newImprintSvc(db)
	// 组级已存在该 path
	seedGroupFile(t, db, "prod", "area1", "AllinCore/config.yml", "a: 1\n")
	cmd, _ := svc.RequestImprint("prod", "lobby-1", "AllinCore/config.yml", "alice", "")
	_, _ = svc.FetchPending("prod", "lobby-1")
	_, _ = svc.ReceiveIngest(cmd.ID, []ImportFile{{Path: "AllinCore/config.yml", Content: "a: 2\n"}}, "")
	md5 := filetree.ContentMD5("a: 2\n")

	// 并入 group 层（已存在）→ 应发布新版本（version 2）
	res, err := svc.ConfirmImprint(cmd.ID, model.ScopeGroup, "area1", "", "", md5, "alice", "")
	if err != nil {
		t.Fatalf("confirm 应成功（发布新版本）: %v", err)
	}
	if res.Version != 2 {
		t.Fatalf("已存在 path 应发版本 2，实际 %d", res.Version)
	}
	obj, _ := repository.NewFileObjectRepository(db).FindByIdentity("prod", "area1", "AllinCore/config.yml", model.ScopeGroup, "")
	if obj == nil || obj.Content != "a: 2\n" || obj.Version != 2 {
		t.Fatalf("组层应被发布为新版本 a:2，实际 %+v", obj)
	}
}
