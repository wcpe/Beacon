package service

import (
	"errors"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/gitexport"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/secret"
)

// captureExporter 是 GitExporter 桩：线程安全记录每次 ExportAsync 的 meta，供断言触发与元数据。
type captureExporter struct {
	mu    sync.Mutex
	metas []gitexport.ExportMeta
}

func (c *captureExporter) ExportAsync(meta gitexport.ExportMeta) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.metas = append(c.metas, meta)
}

func (c *captureExporter) calls() []gitexport.ExportMeta {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]gitexport.ExportMeta, len(c.metas))
	copy(out, c.metas)
	return out
}

// newGitExportTestDB 打开内存 sqlite 并迁移配置 + 文件树 + 审计表（git 导出触发链路用）。
func newGitExportTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(
		&model.ConfigItem{}, &model.ConfigRevision{},
		&model.FileObject{}, &model.FileRevision{}, &model.AuditLog{},
	); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	for _, tbl := range []string{"config_item", "config_revision", "file_object", "file_revision", "audit_log"} {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	return db
}

// TestFileServiceTriggersExportOnWrite FR-47：文件创建 / 发布提交后触发 git 导出，元数据带动作 / 对象 / 版本。
func TestFileServiceTriggersExportOnWrite(t *testing.T) {
	db := newGitExportTestDB(t)
	fileRepo := repository.NewFileObjectRepository(db)
	svc := NewFileService(db, fileRepo, repository.NewFileRevisionRepository(db), repository.NewAuditLogRepository(db))
	exp := &captureExporter{}
	svc.SetGitExporter(exp)

	obj, err := svc.Create(CreateFileParams{
		Namespace: "prod", Group: "area1", Path: "Demo/config.yml",
		ScopeLevel: model.ScopeGroup, Content: "a: 1\n", Operator: "alice",
	})
	if err != nil {
		t.Fatalf("Create 应成功：%v", err)
	}
	if _, err := svc.Publish(obj.ID, "a: 2\n", "bob", "改值", ""); err != nil {
		t.Fatalf("Publish 应成功：%v", err)
	}

	calls := exp.calls()
	if len(calls) != 2 {
		t.Fatalf("应触发 2 次导出（建 + 发布），实际 %d", len(calls))
	}
	if calls[0].Action != model.ActionFileCreate || calls[0].Operator != "alice" {
		t.Fatalf("首次导出元数据应为 file.create/alice，实际 %+v", calls[0])
	}
	if calls[1].Action != model.ActionFilePublish || calls[1].Version != 2 {
		t.Fatalf("二次导出应为 file.publish/版本 2，实际 %+v", calls[1])
	}
}

// TestConfigServiceTriggersExportOnPublish FR-47：配置发布提交后触发 git 导出。
func TestConfigServiceTriggersExportOnPublish(t *testing.T) {
	db := newGitExportTestDB(t)
	cipher, _ := secret.NewCipher("")
	configRepo := repository.NewConfigItemRepository(db, cipher)
	revRepo := repository.NewConfigRevisionRepository(db, cipher)
	svc := NewConfigService(db, configRepo, revRepo, repository.NewAuditLogRepository(db))
	exp := &captureExporter{}
	svc.SetGitExporter(exp)

	if _, err := svc.Create(CreateConfigParams{
		Namespace: "prod", Group: model.GlobalGroupCode, DataID: "app.yml",
		ScopeLevel: model.ScopeGlobal, Format: "yaml", Content: "a: 1\n", Operator: "alice",
	}); err != nil {
		t.Fatalf("Create 应成功：%v", err)
	}
	calls := exp.calls()
	if len(calls) != 1 || calls[0].Action != model.ActionConfigCreate {
		t.Fatalf("配置创建应触发 1 次 config.create 导出，实际 %+v", calls)
	}
	if calls[0].Target == "" {
		t.Fatal("导出元数据应带可读对象引用")
	}
}

// failingGitRepo 是必失败的 GitRepo 桩，验证导出失败绝不阻断发布主流程（best-effort）。
type failingGitRepo struct{}

func (failingGitRepo) Commit(_ gitexport.Snapshot, _ string) error {
	return errors.New("模拟 git 仓损坏")
}

// TestGitExportServiceBestEffortNeverBlocks FR-47：导出 best-effort——
// git 失败仅吞为内部 WARN、exportOnce 不 panic、ExportAsync 非阻塞、读源拿到源层与密文 / 排除。
func TestGitExportServiceBestEffortNeverBlocks(t *testing.T) {
	db := newGitExportTestDB(t)
	exportSrc := repository.NewExportSourceRepository(db)

	// 真服务 + 必失败 GitRepo：exportOnce 应安全返回（不 panic、不抛）
	svc := NewGitExportService(exportSrc, failingGitRepo{})
	// 直接驱动一次同步导出，失败被吞（无 panic 即通过）
	svc.exportOnce(gitexport.ExportMeta{Action: model.ActionConfigPublish})

	// ExportAsync 非阻塞：worker 未启动时也应立即返回（队列缓冲 1，满即丢，不死等）
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			svc.ExportAsync(gitexport.ExportMeta{Action: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ExportAsync 应非阻塞，100 次调用 2s 内必返回")
	}
}

// TestExportSourceRepoPreservesCiphertextAndExclusion FR-47：
// 导出源读取保留敏感配置项密文（不解密）、文件 SensitiveExcluded 透传、敏感排除文件不进快照。
func TestExportSourceRepoPreservesCiphertextAndExclusion(t *testing.T) {
	db := newGitExportTestDB(t)
	// 启用 cipher：敏感配置项落库为密文
	cipher, err := secret.NewCipher("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=") // 32 字节 base64
	if err != nil {
		t.Fatalf("构造 cipher 失败：%v", err)
	}
	configRepo := repository.NewConfigItemRepository(db, cipher)
	revRepo := repository.NewConfigRevisionRepository(db, cipher)
	cfgSvc := NewConfigService(db, configRepo, revRepo, repository.NewAuditLogRepository(db))

	// 敏感配置项：明文 "password: s3cr3t"
	if _, err := cfgSvc.Create(CreateConfigParams{
		Namespace: "prod", Group: model.GlobalGroupCode, DataID: "redis.yml",
		ScopeLevel: model.ScopeGlobal, Format: "yaml", Content: "password: s3cr3t\n",
		Operator: "alice", Sensitive: true,
	}); err != nil {
		t.Fatalf("敏感配置 Create 应成功：%v", err)
	}

	// 文件树：一个普通文件 + 一个标敏感排除文件（含明文密码）
	fileRepo := repository.NewFileObjectRepository(db)
	fileSvc := NewFileService(db, fileRepo, repository.NewFileRevisionRepository(db), repository.NewAuditLogRepository(db))
	if _, err := fileSvc.Create(CreateFileParams{
		Namespace: "prod", Group: "area1", Path: "Demo/messages.yml",
		ScopeLevel: model.ScopeGroup, Content: "hi: hello\n", Operator: "alice",
	}); err != nil {
		t.Fatalf("普通文件 Create 应成功：%v", err)
	}
	excludedObj, err := fileSvc.Create(CreateFileParams{
		Namespace: "prod", Group: "area1", Path: "Demo/database.yml",
		ScopeLevel: model.ScopeGroup, Content: "password: dbpass123\n", Operator: "alice",
		SensitiveExcluded: true,
	})
	if err != nil {
		t.Fatalf("敏感排除文件 Create 应成功：%v", err)
	}
	// 持久化校验：SensitiveExcluded 落库
	reloaded, err := fileRepo.FindByID(excludedObj.ID)
	if err != nil || reloaded == nil || !reloaded.SensitiveExcluded {
		t.Fatalf("SensitiveExcluded 未持久化，err=%v obj=%+v", err, reloaded)
	}

	// 读源层
	layers, err := exportSourceLayersHelper(db)
	if err != nil {
		t.Fatalf("读源层失败：%v", err)
	}
	// 敏感配置项导密文原样、绝不含明文
	var sawCipher bool
	for _, l := range layers {
		if l.Kind == gitexport.KindConfig && l.Name == "redis.yml" {
			if !secret.IsEncrypted(l.Content) {
				t.Fatalf("敏感配置项源层应为密文（enc:v1:），实际 %q", l.Content)
			}
			if l.Content == "password: s3cr3t\n" {
				t.Fatal("敏感配置项源层绝不应为明文")
			}
			sawCipher = true
		}
	}
	if !sawCipher {
		t.Fatal("未读到敏感配置项源层")
	}

	// 组装快照：敏感排除文件不进、密文进、明文不泄
	snap := gitexport.BuildSnapshot(layers)
	for p, c := range snap.Files {
		if c == "password: s3cr3t\n" || c == "password: dbpass123\n" {
			t.Fatalf("快照路径 %q 不应含明文敏感内容", p)
		}
	}
	if _, ok := snap.Files["files/prod/area1/_group_/Demo/database.yml"]; ok {
		t.Fatal("敏感排除文件不应进快照")
	}
	if _, ok := snap.Files["files/prod/area1/_group_/Demo/messages.yml"]; !ok {
		t.Fatal("普通文件应进快照")
	}
	if _, ok := snap.Files["configs/prod/_global_/redis.yml"]; !ok {
		t.Fatal("敏感配置项（密文）应进快照")
	}
}

// exportSourceLayersHelper 用导出源仓库读全量源层（测试便捷封装）。
func exportSourceLayersHelper(db *gorm.DB) ([]gitexport.SourceLayer, error) {
	return repository.NewExportSourceRepository(db).LoadSourceLayers()
}
