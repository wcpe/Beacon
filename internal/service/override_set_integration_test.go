//go:build integration

package service_test

import (
	"context"
	"testing"
	"time"

	"beacon/internal/apperr"
	"beacon/internal/model"
	"beacon/internal/repository"
	"beacon/internal/runtime"
	"beacon/internal/runtime/longpoll"
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

// overrideDeliveryStack 聚合覆盖集服务 + 文件服务 + 投递服务 + 注册表（验证投递解析与长轮询唤醒）。
type overrideDeliveryStack struct {
	sets     *service.OverrideSetService
	files    *service.FileService
	ovrEff   *service.OverrideEffectiveService
	fileRepo *repository.FileObjectRepository
	reg      *runtime.Registry
}

// newOverrideDeliveryStack 装配覆盖集服务 + 文件服务（编辑成员内容）+ 投递服务（复用 fileHub）+ 注册表 + 唤醒器。
func newOverrideDeliveryStack(t *testing.T) overrideDeliveryStack {
	db := testDB(t)
	setRepo := repository.NewFileOverrideSetRepository(db)
	revRepo := repository.NewFileOverrideSetRevisionRepository(db)
	fileRepo := repository.NewFileObjectRepository(db)
	fileRevRepo := repository.NewFileRevisionRepository(db)
	auditRepo := repository.NewAuditLogRepository(db)
	assignRepo := repository.NewZoneAssignmentRepository(db)
	reg := runtime.NewRegistry()
	hub := longpoll.NewHub()
	fileHub := longpoll.NewHub()
	topologyHub := longpoll.NewHub()
	ovrEff := service.NewOverrideEffectiveService(setRepo, fileRepo, assignRepo, fileHub)
	notifier := service.NewChangeNotifier(hub, fileHub, topologyHub, reg, assignRepo)
	setSvc := service.NewOverrideSetService(db, setRepo, revRepo, fileRepo, auditRepo)
	setSvc.SetNotifier(notifier)
	// 文件服务共享同一 notifier：编辑成员文件内容（成员是 override_set_id>0 的 FileObject）走通道B 发布路径，
	// 提交后经 fileHub 唤醒 override 长轮询（与覆盖集投递复用同一唤醒集合）。
	fileSvc := service.NewFileService(db, fileRepo, fileRevRepo, auditRepo)
	fileSvc.SetNotifier(notifier)
	return overrideDeliveryStack{sets: setSvc, files: fileSvc, ovrEff: ovrEff, fileRepo: fileRepo, reg: reg}
}

// registerOverrideS1 把 s1 注册进内存（供 group 反查唤醒）。
func registerOverrideS1(t *testing.T, reg *runtime.Registry) {
	t.Helper()
	if _, err := reg.Register(&runtime.Instance{
		Namespace: "prod", ServerID: "s1", GroupHint: model.GlobalGroupCode, ResolvedGroup: model.GlobalGroupCode, Address: "10.0.0.1:1",
	}, 30*time.Second, time.Now().UTC()); err != nil {
		t.Fatalf("注册实例失败: %v", err)
	}
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

// TestOverrideDeliveryResolveAndContent 投递解析：建集 + 关联成员 → 解析出适用集（含目标根/命令/成员）+ 取成员内容。
func TestOverrideDeliveryResolveAndContent(t *testing.T) {
	s := newOverrideDeliveryStack(t)
	set, err := s.sets.Create(service.CreateOverrideSetParams{
		Namespace: "prod", Group: model.GlobalGroupCode, Name: "AllinCore",
		ScopeLevel: model.ScopeGlobal, TargetRoot: "plugins/AllinCore",
		ReloadCommand: "allin reload", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("建覆盖集失败: %v", err)
	}
	// 关联一个成员文件（path 相对 targetRoot）。
	member := &model.FileObject{
		NamespaceCode: "prod", GroupCode: model.GlobalGroupCode, Path: "config.yml",
		ScopeLevel: model.ScopeGlobal, Content: "members-content\n", ContentMD5: "m1",
		Version: 1, Enabled: true, OverrideSetID: set.ID,
	}
	if err := s.fileRepo.Create(member); err != nil {
		t.Fatalf("建成员失败: %v", err)
	}

	eff, err := s.ovrEff.Resolve("prod", "s1", model.GlobalGroupCode)
	if err != nil {
		t.Fatalf("投递解析失败: %v", err)
	}
	if len(eff.Sets) != 1 {
		t.Fatalf("应解析出 1 个适用集，得 %d", len(eff.Sets))
	}
	got := eff.Sets[0]
	if got.TargetRoot != "plugins/AllinCore" || got.ReloadCommand != "allin reload" {
		t.Fatalf("目标根/命令不符：%+v", got)
	}
	if len(got.MemberPaths) != 1 || got.MemberPaths[0] != "config.yml" {
		t.Fatalf("成员清单不符：%+v", got.MemberPaths)
	}
	if eff.OverrideMD5 == "" {
		t.Fatal("overrideMd5 不应为空")
	}

	// 取成员内容（按 setName + 相对 path）。
	file, err := s.ovrEff.MemberContent("prod", "s1", model.GlobalGroupCode, "AllinCore", "config.yml")
	if err != nil || file == nil {
		t.Fatalf("取成员内容失败 err=%v file=%v", err, file)
	}
	if file.Content != "members-content\n" {
		t.Fatalf("成员内容不符：%q", file.Content)
	}

	// 不适用本 server 的集名 / 不存在的成员 → nil（不越权读）。
	if f, _ := s.ovrEff.MemberContent("prod", "s1", model.GlobalGroupCode, "NotApplicable", "config.yml"); f != nil {
		t.Fatal("不适用集名应取不到成员")
	}
}

// TestOverrideMembersExcludedFromFileTree 覆盖集成员不出现在通用文件树（避免双写到错误根）。
func TestOverrideMembersExcludedFromFileTree(t *testing.T) {
	s := newOverrideDeliveryStack(t)
	set, err := s.sets.Create(service.CreateOverrideSetParams{
		Namespace: "prod", Group: model.GlobalGroupCode, Name: "AllinCore",
		ScopeLevel: model.ScopeGlobal, TargetRoot: "plugins/AllinCore", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("建覆盖集失败: %v", err)
	}
	member := &model.FileObject{
		NamespaceCode: "prod", GroupCode: model.GlobalGroupCode, Path: "config.yml",
		ScopeLevel: model.ScopeGlobal, Content: "x\n", ContentMD5: "m1",
		Version: 1, Enabled: true, OverrideSetID: set.ID,
	}
	if err := s.fileRepo.Create(member); err != nil {
		t.Fatalf("建成员失败: %v", err)
	}
	// 通用文件树候选不应含成员（override_set_id>0 被排除）。
	cands, err := s.fileRepo.FindEffectiveCandidates("prod", model.GlobalGroupCode, "", "s1")
	if err != nil {
		t.Fatalf("拉通用文件树候选失败: %v", err)
	}
	for _, c := range cands {
		if c.OverrideSetID != 0 {
			t.Fatalf("通用文件树不应含覆盖集成员，得 %+v", c)
		}
	}
}

// TestOverrideDeliveryWakesOnPublish override 长轮询：发布覆盖集后被唤醒并返回新 overrideMd5。
func TestOverrideDeliveryWakesOnPublish(t *testing.T) {
	s := newOverrideDeliveryStack(t)
	registerOverrideS1(t, s.reg)
	// 初始无任何集时的 overrideMd5（agent 首拉基准）。
	base, err := s.ovrEff.Resolve("prod", "s1", model.GlobalGroupCode)
	if err != nil {
		t.Fatalf("初始解析失败: %v", err)
	}

	ch := make(chan struct {
		md5     string
		changed bool
	}, 1)
	go func() {
		eff, changed, _ := s.ovrEff.WaitOverride(context.Background(), "prod", "s1", model.GlobalGroupCode, base.OverrideMD5, 3*time.Second)
		ch <- struct {
			md5     string
			changed bool
		}{eff.OverrideMD5, changed}
	}()

	time.Sleep(100 * time.Millisecond) // 让 waiter 先挂起
	if _, err := s.sets.Create(service.CreateOverrideSetParams{
		Namespace: "prod", Group: model.GlobalGroupCode, Name: "AllinCore",
		ScopeLevel: model.ScopeGlobal, TargetRoot: "plugins/AllinCore",
		ReloadCommand: "allin reload", Operator: "alice",
	}); err != nil {
		t.Fatalf("建覆盖集失败: %v", err)
	}

	select {
	case r := <-ch:
		if !r.changed || r.md5 == base.OverrideMD5 {
			t.Fatalf("发布后应被唤醒并得新 md5，实际 changed=%v md5=%s", r.changed, r.md5)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("override 长轮询未在发布后被唤醒")
	}
}

// newOverrideMemberSet 建一个覆盖集并关联一个成员文件（path/targetRoot/命令固定），返回 (set, 成员 FileObject)。
// 成员是 override_set_id>0 的 FileObject，内容经通道B 文件服务编辑（FR-15 内容热更复现的前置）。
func newOverrideMemberSet(t *testing.T, s overrideDeliveryStack) (uint, *model.FileObject) {
	t.Helper()
	set, err := s.sets.Create(service.CreateOverrideSetParams{
		Namespace: "prod", Group: model.GlobalGroupCode, Name: "AllinCore",
		ScopeLevel: model.ScopeGlobal, TargetRoot: "plugins/AllinCore",
		ReloadCommand: "allin reload", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("建覆盖集失败: %v", err)
	}
	member := &model.FileObject{
		NamespaceCode: "prod", GroupCode: model.GlobalGroupCode, Path: "config.yml",
		ScopeLevel: model.ScopeGlobal, Content: "v1\n", ContentMD5: "init-md5",
		Version: 1, Enabled: true, OverrideSetID: set.ID,
	}
	if err := s.fileRepo.Create(member); err != nil {
		t.Fatalf("建成员失败: %v", err)
	}
	return set.ID, member
}

// TestOverrideDeliveryContentEditChangesMd5 FR-15 内容热更缺口复现（黑盒）：
// 成员「内容只改不变 path」时 overrideMd5 必须变（修复前因公式不含成员内容指纹而不变 → agent 不重取）；
// 且内容未变时 overrideMd5 幂等（不无谓重推）。
func TestOverrideDeliveryContentEditChangesMd5(t *testing.T) {
	s := newOverrideDeliveryStack(t)
	_, member := newOverrideMemberSet(t, s)

	base, err := s.ovrEff.Resolve("prod", "s1", model.GlobalGroupCode)
	if err != nil {
		t.Fatalf("初始解析失败: %v", err)
	}
	// 幂等：内容未变，再解析 overrideMd5 不应变（避免无谓重推 / 重复 reload）。
	again, err := s.ovrEff.Resolve("prod", "s1", model.GlobalGroupCode)
	if err != nil {
		t.Fatalf("再解析失败: %v", err)
	}
	if again.OverrideMD5 != base.OverrideMD5 {
		t.Fatalf("内容未变 overrideMd5 应幂等，base=%s again=%s", base.OverrideMD5, again.OverrideMD5)
	}

	// 编辑成员文件内容（path 不变），经通道B 发布路径更新 content + 按字节算的 content_md5。
	if _, err := s.files.Publish(member.ID, "v2-changed\n", "alice", "改成员内容", ""); err != nil {
		t.Fatalf("编辑成员内容失败: %v", err)
	}

	after, err := s.ovrEff.Resolve("prod", "s1", model.GlobalGroupCode)
	if err != nil {
		t.Fatalf("内容编辑后解析失败: %v", err)
	}
	if after.OverrideMD5 == base.OverrideMD5 {
		t.Fatalf("成员内容改变（path 不变）overrideMd5 必须变，base==after=%s", base.OverrideMD5)
	}
}

// TestOverrideDeliveryWakesOnMemberContentEdit FR-15 内容热更缺口复现（长轮询端到端）：
// 编辑覆盖集成员内容（path 不变）→ 经 fileHub 唤醒 override 长轮询 → 返回 changed + 新 overrideMd5。
func TestOverrideDeliveryWakesOnMemberContentEdit(t *testing.T) {
	s := newOverrideDeliveryStack(t)
	registerOverrideS1(t, s.reg)
	_, member := newOverrideMemberSet(t, s)

	base, err := s.ovrEff.Resolve("prod", "s1", model.GlobalGroupCode)
	if err != nil {
		t.Fatalf("初始解析失败: %v", err)
	}

	ch := make(chan struct {
		md5     string
		changed bool
	}, 1)
	go func() {
		eff, changed, _ := s.ovrEff.WaitOverride(context.Background(), "prod", "s1", model.GlobalGroupCode, base.OverrideMD5, 3*time.Second)
		ch <- struct {
			md5     string
			changed bool
		}{eff.OverrideMD5, changed}
	}()

	time.Sleep(100 * time.Millisecond) // 让 waiter 先挂起
	if _, err := s.files.Publish(member.ID, "v2-changed\n", "alice", "改成员内容", ""); err != nil {
		t.Fatalf("编辑成员内容失败: %v", err)
	}

	select {
	case r := <-ch:
		if !r.changed || r.md5 == base.OverrideMD5 {
			t.Fatalf("成员内容编辑后应被唤醒并得新 md5，实际 changed=%v md5=%s", r.changed, r.md5)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("成员内容编辑后 override 长轮询未被唤醒")
	}
}
