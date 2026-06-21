package service

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// newCommandSvcTestDB 打开内存 sqlite 并迁移命令 + 文件树 + 审计表（不依赖 MySQL/DSN）。
func newCommandSvcTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AgentCommand{}, &model.FileObject{}, &model.FileRevision{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	for _, tbl := range []string{"agent_command", "file_object", "file_revision", "audit_log"} {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	return db
}

func newCommandSvc(db *gorm.DB) *AgentCommandService {
	cmdRepo := repository.NewAgentCommandRepository(db)
	auditRepo := repository.NewAuditLogRepository(db)
	fileSvc := NewFileService(db, repository.NewFileObjectRepository(db), repository.NewFileRevisionRepository(db), auditRepo)
	return NewAgentCommandService(db, cmdRepo, fileSvc, auditRepo)
}

func countAudit(t *testing.T, db *gorm.DB, action string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.AuditLog{}).Where("action = ?", action).Count(&n).Error; err != nil {
		t.Fatalf("计数审计失败: %v", err)
	}
	return n
}

// TestRequestReverseFetch 触发即建 pending 命令并记一条 file.reverse-fetch 审计（target=命令）。
func TestRequestReverseFetch(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	cmd, err := svc.RequestReverseFetch("prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("触发反向抓取失败: %v", err)
	}
	if cmd.ID == 0 || cmd.Status != model.CommandStatusPending || cmd.Type != model.CommandTypeIngestPlugins {
		t.Fatalf("命令应为 pending/ingest-plugins，实际 %+v", cmd)
	}
	if countAudit(t, db, model.ActionFileReverseFetch) != 1 {
		t.Fatal("应记一条 file.reverse-fetch 审计")
	}
	// 缺参一律拒
	if _, err := svc.RequestReverseFetch("prod", "", model.ScopeGroup, "area1", "", "alice", ""); err == nil {
		t.Fatal("缺 serverId 应拒")
	}
	// server 层缺目标 serverId 应拒
	if _, err := svc.RequestReverseFetch("prod", "lobby-1", model.ScopeServer, "area1", "", "alice", ""); err == nil {
		t.Fatal("server 层缺 target 应拒")
	}
}

// TestFetchPending 取最早 pending 并 CAS fetched；取空返回 (nil,nil)。
func TestFetchPending(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	_, _ = svc.RequestReverseFetch("prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "")

	got, err := svc.FetchPending("prod", "lobby-1")
	if err != nil || got == nil {
		t.Fatalf("应取到 pending: %v / %v", got, err)
	}
	if got.Status != model.CommandStatusFetched {
		t.Fatalf("取后应为 fetched，实际 %s", got.Status)
	}
	// 已无 pending
	again, err := svc.FetchPending("prod", "lobby-1")
	if err != nil || again != nil {
		t.Fatalf("无 pending 应返回 (nil,nil)，实际 %v / %v", again, err)
	}
}

// TestReceiveIngestHappy 回传合法文件 → 落组覆盖、命令 done、记 file.import 审计。
func TestReceiveIngestHappy(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	_, _ = svc.RequestReverseFetch("prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "")
	cmd, _ := svc.FetchPending("prod", "lobby-1")

	files := []ImportFile{
		{Path: "AllinCore/config.yml", Content: "a: 1\n"},
		{Path: "AllinCore/lang.yml", Content: "hi: hello\n"},
	}
	res, err := svc.ReceiveIngest(cmd.ID, files, "10.0.0.2")
	if err != nil {
		t.Fatalf("ingest 应成功: %v", err)
	}
	if res.Created != 2 {
		t.Fatalf("应新建 2 个文件对象，实际 created=%d updated=%d", res.Created, res.Updated)
	}
	// 文件已落组覆盖
	fileRepo := repository.NewFileObjectRepository(db)
	obj, _ := fileRepo.FindByIdentity("prod", "area1", "AllinCore/config.yml", model.ScopeGroup, "")
	if obj == nil || obj.Content != "a: 1\n" {
		t.Fatalf("组覆盖文件应已落库，实际 %+v", obj)
	}
	// 命令 done
	got, _ := repository.NewAgentCommandRepository(db).FindByID(cmd.ID)
	if got.Status != model.CommandStatusDone {
		t.Fatalf("命令应为 done，实际 %s", got.Status)
	}
	// 触发 + ingest 各一条审计
	if countAudit(t, db, model.ActionFileReverseFetch) != 1 || countAudit(t, db, model.ActionFileImport) != 1 {
		t.Fatal("应各记一条 file.reverse-fetch 与 file.import 审计")
	}
}

// TestReceiveIngestRejectsJarAndState 排除 jar、状态/存在性守卫；失败命令转 failed。
func TestReceiveIngestRejectsJarAndState(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	cmdRepo := repository.NewAgentCommandRepository(db)

	// 不存在的命令 → COMMAND_NOT_FOUND
	if _, err := svc.ReceiveIngest(99999, []ImportFile{{Path: "a.yml", Content: "x"}}, ""); err != apperr.ErrCommandNotFound {
		t.Fatalf("不存在命令应 ErrCommandNotFound，实际 %v", err)
	}
	// pending（未拉取）状态回传 → 不可回传
	_, _ = svc.RequestReverseFetch("prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "")
	pend, _ := cmdRepo.FindOldestPending("prod", "lobby-1")
	if _, err := svc.ReceiveIngest(pend.ID, []ImportFile{{Path: "a.yml", Content: "x"}}, ""); err != apperr.ErrCommandNotFound {
		t.Fatalf("pending 状态回传应被拒，实际 %v", err)
	}
	// 拉取后回传含 .jar → ErrInvalidPath，命令转 failed
	cmd, _ := svc.FetchPending("prod", "lobby-1")
	if _, err := svc.ReceiveIngest(cmd.ID, []ImportFile{{Path: "Evil/plugin.jar", Content: "MZ"}}, ""); err != apperr.ErrInvalidPath {
		t.Fatalf("含 jar 应 ErrInvalidPath，实际 %v", err)
	}
	got, _ := cmdRepo.FindByID(cmd.ID)
	if got.Status != model.CommandStatusFailed {
		t.Fatalf("校验失败命令应转 failed，实际 %s", got.Status)
	}
}

// TestReceiveIngestServerScope 实例(server)级反向抓取 → 落单服覆盖、不落 group 层。
func TestReceiveIngestServerScope(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	_, _ = svc.RequestReverseFetch("prod", "lobby-1", model.ScopeServer, "area1", "lobby-1", "alice", "")
	cmd, _ := svc.FetchPending("prod", "lobby-1")
	if _, err := svc.ReceiveIngest(cmd.ID, []ImportFile{{Path: "AllinCore/config.yml", Content: "a: 2\n"}}, ""); err != nil {
		t.Fatalf("server 层 ingest 应成功: %v", err)
	}
	fileRepo := repository.NewFileObjectRepository(db)
	obj, _ := fileRepo.FindByIdentity("prod", "area1", "AllinCore/config.yml", model.ScopeServer, "lobby-1")
	if obj == nil || obj.Content != "a: 2\n" {
		t.Fatalf("应落单服覆盖 (scope=server, target=lobby-1)，实际 %+v", obj)
	}
	groupObj, _ := fileRepo.FindByIdentity("prod", "area1", "AllinCore/config.yml", model.ScopeGroup, "")
	if groupObj != nil {
		t.Fatal("不应落到 group 层")
	}
}

// TestValidateIngestFiles 直测再校验：空 / 超数 / jar / 超总量 / 合法。
func TestValidateIngestFiles(t *testing.T) {
	if validateIngestFiles(nil) != apperr.ErrInvalidParam {
		t.Fatal("空集应 ErrInvalidParam")
	}
	tooMany := make([]ImportFile, MaxImportFiles+1)
	for i := range tooMany {
		tooMany[i] = ImportFile{Path: "f.yml", Content: "x"}
	}
	if validateIngestFiles(tooMany) != apperr.ErrTooManyFiles {
		t.Fatal("超文件数应 ErrTooManyFiles")
	}
	if validateIngestFiles([]ImportFile{{Path: "a/b.JAR", Content: "x"}}) != apperr.ErrInvalidPath {
		t.Fatal("含 .JAR（大小写）应 ErrInvalidPath")
	}
	big := []ImportFile{{Path: "big.yml", Content: string(make([]byte, MaxImportTotalBytes+1))}}
	if validateIngestFiles(big) != apperr.ErrContentTooLarge {
		t.Fatal("超总量应 ErrContentTooLarge")
	}
	if err := validateIngestFiles([]ImportFile{{Path: "ok.yml", Content: "v: 1\n"}}); err != nil {
		t.Fatalf("合法文件集应通过，实际 %v", err)
	}
}

// TestExpireStale 陈旧 pending/fetched 转 expired。
func TestExpireStale(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	_, _ = svc.RequestReverseFetch("prod", "a", model.ScopeGroup, "g", "", "alice", "")
	if err := db.Model(&model.AgentCommand{}).Where("1 = 1").Update("created_at", time.Now().Add(-2*time.Hour)).Error; err != nil {
		t.Fatalf("改 created_at 失败: %v", err)
	}
	n, err := svc.ExpireStale(time.Now().Add(-1 * time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("应过期 1 条，实际 %d / %v", n, err)
	}
}
