package service

import (
	"encoding/json"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// newRFTaskTestDB 打开内存 sqlite 并迁移受管任务 + 命令 + 文件树 + 审计表（不依赖 MySQL/DSN）。
func newRFTaskTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.ReverseFetchTask{}, &model.AgentCommand{},
		&model.FileObject{}, &model.FileRevision{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	for _, tbl := range []string{"reverse_fetch_task", "agent_command", "file_object", "file_revision", "audit_log"} {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	return db
}

func newRFTaskSvc(db *gorm.DB) *ReverseFetchTaskService {
	taskRepo := repository.NewReverseFetchTaskRepository(db)
	cmdRepo := repository.NewAgentCommandRepository(db)
	auditRepo := repository.NewAuditLogRepository(db)
	fileSvc := NewFileService(db, repository.NewFileObjectRepository(db), repository.NewFileRevisionRepository(db), auditRepo)
	svc := NewReverseFetchTaskService(db, taskRepo, cmdRepo, fileSvc, auditRepo)
	// submit 回传由命令服务据 mode 转交受管任务（与生产装配一致）。
	cmdSvc := NewAgentCommandService(db, cmdRepo, fileSvc, auditRepo)
	cmdSvc.SetSubmitIngestReceiver(svc)
	return svc
}

// fetchCmd 把指定命令从 pending CAS 迁移 fetched（模拟 agent 拉取），返回命令。
func fetchCmd(t *testing.T, db *gorm.DB, id uint) *model.AgentCommand {
	t.Helper()
	cmdRepo := repository.NewAgentCommandRepository(db)
	ok, err := cmdRepo.UpdateStatus(id, model.CommandStatusPending, model.CommandStatusFetched, "")
	if err != nil || !ok {
		t.Fatalf("拉取命令 %d 失败: ok=%v err=%v", id, ok, err)
	}
	c, _ := cmdRepo.FindByID(id)
	return c
}

func rfCountAudit(t *testing.T, db *gorm.DB, action string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.AuditLog{}).Where("action = ?", action).Count(&n).Error; err != nil {
		t.Fatalf("计数审计失败: %v", err)
	}
	return n
}

// TestCreateScanTaskAndMutex 建任务即 scanning + 下发 scan 命令 + 审计；同实例已有活跃任务再建 → 409。
func TestCreateScanTaskAndMutex(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)

	task, err := svc.CreateScanTask("prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("建任务应成功: %v", err)
	}
	if task.Status != model.ReverseFetchTaskScanning || task.ScanCommandID == 0 {
		t.Fatalf("应 scanning + 已下发 scan 命令，实际 %+v", task)
	}
	// scan 命令已落库为 pending，mode=scan
	cmd, _ := repository.NewAgentCommandRepository(db).FindByID(task.ScanCommandID)
	if cmd == nil || cmd.Status != model.CommandStatusPending {
		t.Fatalf("scan 命令应为 pending，实际 %+v", cmd)
	}
	var p ingestPayload
	_ = json.Unmarshal([]byte(cmd.Payload), &p)
	if p.Mode != model.IngestModeScan || p.Scope != model.ScopeGroup || p.Group != "area1" {
		t.Fatalf("scan 命令 payload 应含 mode=scan/scope/group，实际 %+v", p)
	}
	if rfCountAudit(t, db, model.ActionFileReverseFetchScan) != 1 {
		t.Fatal("应记一条 file.reverse-fetch-scan 审计")
	}

	// 互斥：同实例再建 → 409 REVERSE_FETCH_TASK_ACTIVE
	_, err = svc.CreateScanTask("prod", "lobby-1", model.ScopeGroup, "area1", "", "bob", "")
	ae, ok := err.(*apperr.Error)
	if !ok || ae.Code != apperr.ErrReverseFetchTaskActive.Code {
		t.Fatalf("已有活跃任务应 409 REVERSE_FETCH_TASK_ACTIVE，实际 %v", err)
	}

	// 另一实例不受互斥影响
	if _, err := svc.CreateScanTask("prod", "lobby-2", model.ScopeGroup, "area1", "", "alice", ""); err != nil {
		t.Fatalf("另一实例建任务应成功: %v", err)
	}
	// 缺参 / 非法 scope 拒
	if _, err := svc.CreateScanTask("prod", "", model.ScopeGroup, "area1", "", "alice", ""); err == nil {
		t.Fatal("缺 serverId 应拒")
	}
	if _, err := svc.CreateScanTask("prod", "x", model.ScopeServer, "area1", "", "alice", ""); err == nil {
		t.Fatal("server 层缺 target 应拒")
	}
}

// TestReceiveScanNeverFails 扫描回传列全树（含超阈值文件）永不失败：任务→pending-review、清单与计数落库。
func TestReceiveScanNeverFails(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	task, _ := svc.CreateScanTask("prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "")
	fetchCmd(t, db, task.ScanCommandID)

	// 含一个远超 1MB 上限的运行时垃圾文件（overThreshold=true）——scan 不应因此失败
	files := []ScanFile{
		{Path: "AllinCore/config.yml", Size: 1234, IsText: true, OverThreshold: false},
		{Path: "ServerProbe/metrics.jsonl", Size: 5 * 1024 * 1024, IsText: true, OverThreshold: true},
	}
	if err := svc.ReceiveScan(task.ScanCommandID, files, ""); err != nil {
		t.Fatalf("scan 回传应永不失败（含超阈值文件），实际 %v", err)
	}
	got, _ := svc.Get(task.ID)
	if got.Status != model.ReverseFetchTaskPendingReview {
		t.Fatalf("回清单后应 pending-review，实际 %s", got.Status)
	}
	if got.TotalFiles != 2 || got.OverThresholdCount != 1 {
		t.Fatalf("计数应 total=2 overThreshold=1，实际 total=%d over=%d", got.TotalFiles, got.OverThresholdCount)
	}
	// manifest 含两文件元信息
	var m scanManifest
	_ = json.Unmarshal([]byte(got.Manifest), &m)
	if len(m.Files) != 2 || !m.Files[1].OverThreshold {
		t.Fatalf("manifest 应列出全树含超阈值红标，实际 %+v", m.Files)
	}
	// scan 命令转 done
	cmd, _ := repository.NewAgentCommandRepository(db).FindByID(task.ScanCommandID)
	if cmd.Status != model.CommandStatusDone {
		t.Fatalf("scan 命令应 done，实际 %s", cmd.Status)
	}
}

// scanToPendingReview 建任务 + 回扫描清单，把任务推到 pending-review，返回任务。
func scanToPendingReview(t *testing.T, db *gorm.DB, svc *ReverseFetchTaskService, files []ScanFile) *model.ReverseFetchTask {
	t.Helper()
	task, err := svc.CreateScanTask("prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "")
	if err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	fetchCmd(t, db, task.ScanCommandID)
	if err := svc.ReceiveScan(task.ScanCommandID, files, ""); err != nil {
		t.Fatalf("scan 回传失败: %v", err)
	}
	got, _ := svc.Get(task.ID)
	return got
}

// TestSubmitOnlySelectedLands 提交仅落选定集：submit 下发命令 + 任务→fetching；agent 回选定内容 → 仅选定落库、任务→done。
func TestSubmitOnlySelectedLands(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	task := scanToPendingReview(t, db, svc, []ScanFile{
		{Path: "AllinCore/config.yml", Size: 10, IsText: true},
		{Path: "AllinCore/data.db", Size: 99, IsText: false},
		{Path: "Other/lang.yml", Size: 20, IsText: true},
	})

	// 仅选定两个配置文件（不含 data.db）
	got, err := svc.Submit(task.ID, []string{"AllinCore/config.yml", "Other/lang.yml"}, false, "alice", "")
	if err != nil {
		t.Fatalf("提交应成功: %v", err)
	}
	if got.Status != model.ReverseFetchTaskFetching || got.SubmitCommandID == 0 {
		t.Fatalf("提交后应 fetching + 已下发 submit 命令，实际 %+v", got)
	}
	if rfCountAudit(t, db, model.ActionFileReverseFetchSubmit) != 1 {
		t.Fatal("应记一条 file.reverse-fetch-submit 审计")
	}
	// submit 命令 payload 含 mode=submit + selectedPaths
	cmd := fetchCmd(t, db, got.SubmitCommandID)
	var p ingestPayload
	_ = json.Unmarshal([]byte(cmd.Payload), &p)
	if p.Mode != model.IngestModeSubmit || len(p.SelectedPaths) != 2 {
		t.Fatalf("submit 命令应含 mode=submit + 2 选定 path，实际 %+v", p)
	}

	// agent 回传选定内容（仅选定集）→ 受管任务落库、任务→done
	res, err := svc.ReceiveSubmitIngest(got.SubmitCommandID, []ImportFile{
		{Path: "AllinCore/config.yml", Content: "k: 1\n"},
		{Path: "Other/lang.yml", Content: "hi: hello\n"},
	}, "")
	if err != nil {
		t.Fatalf("submit ingest 应成功: %v", err)
	}
	if res.Created != 2 {
		t.Fatalf("应落 2 个选定文件，实际 created=%d updated=%d", res.Created, res.Updated)
	}
	done, _ := svc.Get(task.ID)
	if done.Status != model.ReverseFetchTaskDone {
		t.Fatalf("入库后任务应 done，实际 %s", done.Status)
	}
	// 仅选定文件落库；未选定的 data.db 不落库
	fileRepo := repository.NewFileObjectRepository(db)
	if obj, _ := fileRepo.FindByIdentity("prod", "area1", "AllinCore/config.yml", model.ScopeGroup, ""); obj == nil {
		t.Fatal("选定文件应落库")
	}
	if obj, _ := fileRepo.FindByIdentity("prod", "area1", "AllinCore/data.db", model.ScopeGroup, ""); obj != nil {
		t.Fatal("未选定文件不应落库")
	}
	if rfCountAudit(t, db, model.ActionFileReverseFetchIngest) != 1 {
		t.Fatal("应记一条 file.reverse-fetch-ingest 审计")
	}
	// 任务终结后互斥解除：同实例可再建任务
	if _, err := svc.CreateScanTask("prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", ""); err != nil {
		t.Fatalf("任务终结后同实例应可再建，实际 %v", err)
	}
}

// TestSubmitOverThresholdNotConfirmed 选定集含超阈值文件但未确认 → 拒该文件（400），不下发命令、不拒整批可重提确认。
func TestSubmitOverThresholdNotConfirmed(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	task := scanToPendingReview(t, db, svc, []ScanFile{
		{Path: "AllinCore/config.yml", Size: 10, IsText: true, OverThreshold: false},
		{Path: "Big/dump.yml", Size: 2 * 1024 * 1024, IsText: true, OverThreshold: true},
	})

	// 选定含超阈值文件但未确认 → 400 OVER_THRESHOLD_NOT_CONFIRMED
	_, err := svc.Submit(task.ID, []string{"AllinCore/config.yml", "Big/dump.yml"}, false, "alice", "")
	ae, ok := err.(*apperr.Error)
	if !ok || ae.Code != apperr.ErrOverThresholdNotConfirmed.Code {
		t.Fatalf("超阈值未确认应 400 OVER_THRESHOLD_NOT_CONFIRMED，实际 %v", err)
	}
	// 被拒后任务仍 pending-review（未下发命令、未迁移），可重提
	still, _ := svc.Get(task.ID)
	if still.Status != model.ReverseFetchTaskPendingReview {
		t.Fatalf("被拒后任务应仍 pending-review，实际 %s", still.Status)
	}

	// 仅选非超阈值文件（不带确认）→ 成功（只拒超阈值那个、不拒整批）
	got, err := svc.Submit(task.ID, []string{"AllinCore/config.yml"}, false, "alice", "")
	if err != nil {
		t.Fatalf("仅选非超阈值文件应成功: %v", err)
	}
	if got.SelectedCount != 1 {
		t.Fatalf("应选定 1 个，实际 %d", got.SelectedCount)
	}
}

// TestSubmitOverThresholdConfirmed 带 confirmOverThreshold=true 时超阈值文件可纳入选定集。
func TestSubmitOverThresholdConfirmed(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	task := scanToPendingReview(t, db, svc, []ScanFile{
		{Path: "Big/dump.yml", Size: 2 * 1024 * 1024, IsText: true, OverThreshold: true},
	})
	got, err := svc.Submit(task.ID, []string{"Big/dump.yml"}, true, "alice", "")
	if err != nil {
		t.Fatalf("确认后超阈值文件应可纳入: %v", err)
	}
	if got.SelectedCount != 1 {
		t.Fatalf("确认后应选定 1 个，实际 %d", got.SelectedCount)
	}
}

// TestSubmitRejectsPathNotInManifest 选定 path 不在扫描清单内 → 拒，不下发命令。
func TestSubmitRejectsPathNotInManifest(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	task := scanToPendingReview(t, db, svc, []ScanFile{{Path: "A/config.yml", Size: 1, IsText: true}})
	if _, err := svc.Submit(task.ID, []string{"A/config.yml", "Ghost/x.yml"}, false, "alice", ""); err != apperr.ErrInvalidParam {
		t.Fatalf("含清单外 path 应 ErrInvalidParam，实际 %v", err)
	}
}

// TestStateMachineGuards 状态机守卫：非 pending-review 不可 submit；submit 回传须任务 fetching。
func TestStateMachineGuards(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	task, _ := svc.CreateScanTask("prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "")
	// scanning 态直接 submit → 状态不符
	if _, err := svc.Submit(task.ID, []string{"a.yml"}, false, "alice", ""); err != apperr.ErrReverseFetchTaskState {
		t.Fatalf("scanning 态 submit 应 REVERSE_FETCH_TASK_STATE，实际 %v", err)
	}
	// 不存在的任务
	if _, err := svc.Submit(99999, []string{"a.yml"}, false, "alice", ""); err != apperr.ErrReverseFetchTaskNotFound {
		t.Fatalf("不存在任务应 NOT_FOUND，实际 %v", err)
	}
}

// TestCancel 取消非终态任务 → cancelled + 审计、互斥解除；终态再取消 → 状态不符。
func TestCancel(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	task, _ := svc.CreateScanTask("prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "")

	got, err := svc.Cancel(task.ID, "alice", "")
	if err != nil {
		t.Fatalf("取消应成功: %v", err)
	}
	if got.Status != model.ReverseFetchTaskCancelled {
		t.Fatalf("取消后应 cancelled，实际 %s", got.Status)
	}
	if rfCountAudit(t, db, model.ActionFileReverseFetchCancel) != 1 {
		t.Fatal("应记一条 file.reverse-fetch-cancel 审计")
	}
	// 终态再取消 → 状态不符
	if _, err := svc.Cancel(task.ID, "alice", ""); err != apperr.ErrReverseFetchTaskState {
		t.Fatalf("终态再取消应 REVERSE_FETCH_TASK_STATE，实际 %v", err)
	}
	// 互斥解除：同实例可再建
	if _, err := svc.CreateScanTask("prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", ""); err != nil {
		t.Fatalf("取消后同实例应可再建，实际 %v", err)
	}
}

// TestExpireStale 陈旧非终态任务 → expired、清空清单、互斥解除。
func TestRFTaskExpireStale(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	task := scanToPendingReview(t, db, svc, []ScanFile{{Path: "A/config.yml", Size: 1, IsText: true}})
	// 把创建时间推到 2 小时前
	if err := db.Model(&model.ReverseFetchTask{}).Where("id = ?", task.ID).
		Update("created_at", time.Now().Add(-2*time.Hour)).Error; err != nil {
		t.Fatalf("改 created_at 失败: %v", err)
	}
	n, err := svc.ExpireStale(time.Now().Add(-1 * time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("应过期 1 条，实际 %d / %v", n, err)
	}
	got, _ := svc.Get(task.ID)
	if got.Status != model.ReverseFetchTaskExpired || got.Manifest != "" {
		t.Fatalf("过期后应 expired 且清单已清空，实际 status=%s manifestLen=%d", got.Status, len(got.Manifest))
	}
	// 互斥解除：同实例可再建
	if _, err := svc.CreateScanTask("prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", ""); err != nil {
		t.Fatalf("过期后同实例应可再建，实际 %v", err)
	}
}

// TestReceiveIngestDispatchesSubmitMode 守护线路：agent 复用 /files/ingest 回传 submit 内容时，
// AgentCommandService.ReceiveIngest 据命令 mode=submit 转交受管任务 ReceiveSubmitIngest 落库（而非误走 FR-39 land）。
func TestReceiveIngestDispatchesSubmitMode(t *testing.T) {
	db := newRFTaskTestDB(t)
	taskRepo := repository.NewReverseFetchTaskRepository(db)
	cmdRepo := repository.NewAgentCommandRepository(db)
	auditRepo := repository.NewAuditLogRepository(db)
	fileSvc := NewFileService(db, repository.NewFileObjectRepository(db), repository.NewFileRevisionRepository(db), auditRepo)
	taskSvc := NewReverseFetchTaskService(db, taskRepo, cmdRepo, fileSvc, auditRepo)
	cmdSvc := NewAgentCommandService(db, cmdRepo, fileSvc, auditRepo)
	cmdSvc.SetSubmitIngestReceiver(taskSvc)

	task := scanToPendingReview(t, db, taskSvc, []ScanFile{{Path: "A/config.yml", Size: 1, IsText: true}})
	got, _ := taskSvc.Submit(task.ID, []string{"A/config.yml"}, false, "alice", "")
	fetchCmd(t, db, got.SubmitCommandID)

	// 经命令服务的统一回传入口（生产路径）→ 应转交受管任务落库、任务→done
	res, err := cmdSvc.ReceiveIngest(got.SubmitCommandID, []ImportFile{{Path: "A/config.yml", Content: "k: 1\n"}}, "")
	if err != nil {
		t.Fatalf("经 ReceiveIngest 转交 submit 应成功: %v", err)
	}
	if res == nil || res.Created != 1 {
		t.Fatalf("应落 1 个选定文件，实际 %+v", res)
	}
	done, _ := taskSvc.Get(task.ID)
	if done.Status != model.ReverseFetchTaskDone {
		t.Fatalf("转交后任务应 done，实际 %s", done.Status)
	}
}

// TestSubmitIngestRejectsJar submit 回传含 jar → 任务 failed、命令 failed（双保险，不落库）。
func TestSubmitIngestRejectsJar(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	task := scanToPendingReview(t, db, svc, []ScanFile{{Path: "A/config.yml", Size: 1, IsText: true}})
	got, _ := svc.Submit(task.ID, []string{"A/config.yml"}, false, "alice", "")
	fetchCmd(t, db, got.SubmitCommandID)

	// agent 回传混入 jar（异常 / 越权）→ 400 INVALID_PATH，任务转 failed
	if _, err := svc.ReceiveSubmitIngest(got.SubmitCommandID, []ImportFile{{Path: "evil/plugin.jar", Content: "MZ"}}, ""); err != apperr.ErrInvalidPath {
		t.Fatalf("含 jar 应 ErrInvalidPath，实际 %v", err)
	}
	failed, _ := svc.Get(task.ID)
	if failed.Status != model.ReverseFetchTaskFailed {
		t.Fatalf("入库校验失败任务应 failed，实际 %s", failed.Status)
	}
}
