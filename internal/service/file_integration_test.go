//go:build integration

package service_test

import (
	"context"
	"testing"
	"time"

	"beacon/internal/apperr"
	"beacon/internal/filetree"
	"beacon/internal/model"
	"beacon/internal/repository"
	"beacon/internal/runtime"
	"beacon/internal/runtime/longpoll"
	"beacon/internal/service"
)

// fileStack 聚合文件通道与配置通道服务，便于验证两通道唤醒集合独立。
type fileStack struct {
	files   *service.FileService
	fileEff *service.FileEffectiveService
	cfg     *service.ConfigService
	cfgEff  *service.EffectiveService
	reg     *runtime.Registry
}

// newFileStack 装配文件 + 配置服务（共享 registry，但 hub 与 fileHub 独立）。
func newFileStack(t *testing.T) fileStack {
	db := testDB(t)
	fr := repository.NewFileObjectRepository(db)
	frr := repository.NewFileRevisionRepository(db)
	cr := repository.NewConfigItemRepository(db, noEncryptCipher())
	ar := repository.NewAuditLogRepository(db)
	asg := repository.NewZoneAssignmentRepository(db)
	reg := runtime.NewRegistry()
	hub := longpoll.NewHub()
	fileHub := longpoll.NewHub()
	fileEff := service.NewFileEffectiveService(fr, asg, fileHub)
	cfgEff := service.NewEffectiveService(cr, asg, hub)
	notifier := service.NewChangeNotifier(hub, fileHub, reg, asg)
	fileSvc := service.NewFileService(db, fr, frr, ar)
	fileSvc.SetNotifier(notifier)
	cfgSvc := service.NewConfigService(db, cr, repository.NewConfigRevisionRepository(db, noEncryptCipher()), ar)
	cfgSvc.SetNotifier(notifier)
	return fileStack{files: fileSvc, fileEff: fileEff, cfg: cfgSvc, cfgEff: cfgEff, reg: reg}
}

// registerS1 把 s1 注册进内存（供 group 反查唤醒）。
func registerS1(t *testing.T, reg *runtime.Registry) {
	t.Helper()
	if _, err := reg.Register(&runtime.Instance{
		Namespace: "prod", ServerID: "s1", GroupHint: "bw", ResolvedGroup: "bw", Address: "10.0.0.1:1",
	}, 30*time.Second, time.Now().UTC()); err != nil {
		t.Fatalf("注册实例失败: %v", err)
	}
}

// TestFileLifecycle 集成验证：建→发布→历史→回滚→软删。
func TestFileLifecycle(t *testing.T) {
	s := newFileStack(t)

	obj, err := s.files.Create(service.CreateFileParams{
		Namespace: "prod", Group: model.GlobalGroupCode, Path: "ui-components/main.allin",
		ScopeLevel: model.ScopeGlobal, Content: "v1\n", Operator: "alice", Comment: "首次",
	})
	if err != nil {
		t.Fatalf("建文件失败: %v", err)
	}
	if obj.Version != 1 || obj.ContentMD5 != filetree.ContentMD5("v1\n") {
		t.Fatalf("首发版本/ md5 错误：version=%d md5=%s", obj.Version, obj.ContentMD5)
	}

	pub, err := s.files.Publish(obj.ID, "v2\n", "bob", "改内容", "")
	if err != nil || pub.Version != 2 {
		t.Fatalf("发布失败 version=%d err=%v", pub.Version, err)
	}

	revs, err := s.files.ListRevisions(obj.ID)
	if err != nil || len(revs) != 2 {
		t.Fatalf("历史应有 2 条，实际 %d err=%v", len(revs), err)
	}

	rb, err := s.files.Rollback(obj.ID, 1, "carol", "回滚", "")
	if err != nil || rb.Version != 3 || rb.Content != "v1\n" {
		t.Fatalf("回滚错误 version=%d content=%q err=%v", rb.Version, rb.Content, err)
	}

	if err := s.files.Delete(obj.ID, "dave", "", ""); err != nil {
		t.Fatalf("软删失败: %v", err)
	}
	if _, err := s.files.Get(obj.ID); err != apperr.ErrFileNotFound {
		t.Fatalf("软删后应 FILE_NOT_FOUND，实际 %v", err)
	}
}

// TestFileScopeOverride 集成验证 scope 整文件覆盖：server 层覆盖 global 层同 path，无覆盖者取 global。
func TestFileScopeOverride(t *testing.T) {
	s := newFileStack(t)

	if _, err := s.files.Create(service.CreateFileParams{
		Namespace: "prod", Group: model.GlobalGroupCode, Path: "conf.yml",
		ScopeLevel: model.ScopeGlobal, Content: "global\n", Operator: "a",
	}); err != nil {
		t.Fatalf("建 global 失败: %v", err)
	}
	if _, err := s.files.Create(service.CreateFileParams{
		Namespace: "prod", Group: "bw", Path: "conf.yml",
		ScopeLevel: model.ScopeServer, ScopeTarget: "s1", Content: "server-s1\n", Operator: "a",
	}); err != nil {
		t.Fatalf("建 server 失败: %v", err)
	}

	tree, err := s.fileEff.Resolve("prod", "s1", "bw")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(tree.Files) != 1 || tree.Files[0].Content != "server-s1\n" {
		t.Fatalf("s1 应取 server 覆盖整文件，实际 %+v", tree.Files)
	}

	tree2, err := s.fileEff.Resolve("prod", "s2", "bw")
	if err != nil {
		t.Fatalf("解析 s2 失败: %v", err)
	}
	if len(tree2.Files) != 1 || tree2.Files[0].Content != "global\n" {
		t.Fatalf("s2 应取 global，实际 %+v", tree2.Files)
	}
}

// TestFileLongPollWakesOnPublish 集成验证文件长轮询：发布后被唤醒并返回新 fileTreeMd5。
func TestFileLongPollWakesOnPublish(t *testing.T) {
	s := newFileStack(t)
	registerS1(t, s.reg)
	empty := filetree.FileTreeMD5(map[string]string{})

	ch := make(chan struct {
		md5     string
		changed bool
	}, 1)
	go func() {
		tree, changed, _ := s.fileEff.WaitFileTree(context.Background(), "prod", "s1", "bw", empty, 3*time.Second)
		ch <- struct {
			md5     string
			changed bool
		}{tree.FileTreeMD5, changed}
	}()

	time.Sleep(100 * time.Millisecond) // 让 waiter 先挂起
	if _, err := s.files.Create(service.CreateFileParams{
		Namespace: "prod", Group: model.GlobalGroupCode, Path: "a.yml",
		ScopeLevel: model.ScopeGlobal, Content: "x\n", Operator: "a",
	}); err != nil {
		t.Fatalf("建文件失败: %v", err)
	}

	select {
	case r := <-ch:
		if !r.changed || r.md5 == empty {
			t.Fatalf("发布后应被唤醒并得到新 md5，实际 changed=%v md5=%s", r.changed, r.md5)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("文件长轮询未在发布后被唤醒")
	}
}

// TestFilePublishDoesNotWakeConfigPoll 集成验证唤醒集合独立：文件发布不唤醒配置长轮询（独立 Hub）。
func TestFilePublishDoesNotWakeConfigPoll(t *testing.T) {
	s := newFileStack(t)
	registerS1(t, s.reg)

	// 先备好一份 global 配置，使配置 md5 非空、稳定
	if _, err := s.cfg.Create(service.CreateConfigParams{
		Namespace: "prod", Group: model.GlobalGroupCode, DataID: "app.yml",
		ScopeLevel: model.ScopeGlobal, Format: "yaml", Content: "k: 1\n", Operator: "a",
	}); err != nil {
		t.Fatalf("建配置失败: %v", err)
	}
	cur, err := s.cfgEff.Resolve("prod", "s1", "bw")
	if err != nil {
		t.Fatalf("解析配置失败: %v", err)
	}

	// 配置长轮询挂起，期间只发布文件 → 配置 waiter 不应被唤醒
	ch := make(chan bool, 1)
	go func() {
		_, changed, _ := s.cfgEff.WaitEffective(context.Background(), "prod", "s1", "bw", cur.MD5, 600*time.Millisecond)
		ch <- changed
	}()

	time.Sleep(100 * time.Millisecond)
	if _, err := s.files.Create(service.CreateFileParams{
		Namespace: "prod", Group: model.GlobalGroupCode, Path: "only-file.yml",
		ScopeLevel: model.ScopeGlobal, Content: "x\n", Operator: "a",
	}); err != nil {
		t.Fatalf("发布文件失败: %v", err)
	}

	select {
	case changed := <-ch:
		if changed {
			t.Fatal("文件发布不应唤醒配置长轮询（唤醒集合应独立）")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("配置 WaitEffective 未返回")
	}
}
