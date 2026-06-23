package service

import (
	"encoding/json"
	"testing"

	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// TestReceiveScanRecomputesOverThresholdFromSetting 守护 FR-61：ReceiveScan 用设置 store 的
// reverse-fetch.max-file-bytes + agent 上报 size 重算 overThreshold，不信 agent 自报的 overThreshold 标记。
// agent 故意报错标记（小文件标 true、大文件标 false），控制面应按 size 与设置阈值纠正。
func TestReceiveScanRecomputesOverThresholdFromSetting(t *testing.T) {
	db := newRFTaskTestDB(t)
	taskRepo := repository.NewReverseFetchTaskRepository(db)
	cmdRepo := repository.NewAgentCommandRepository(db)
	auditRepo := repository.NewAuditLogRepository(db)
	fileSvc := NewFileService(db, repository.NewFileObjectRepository(db), repository.NewFileRevisionRepository(db), auditRepo)
	// 把上限设为 2000 字节：>2000 才算超阈值。
	settings := settingsWith(t, map[string]string{SettingReverseFetchMaxFileBytes: "2000"})
	svc := NewReverseFetchTaskService(db, taskRepo, cmdRepo, fileSvc, auditRepo, settings)
	cmdSvc := NewAgentCommandService(db, cmdRepo, fileSvc, auditRepo)
	cmdSvc.SetSubmitIngestReceiver(svc)

	task, err := svc.CreateScanTask("prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "")
	if err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	fetchCmd(t, db, task.ScanCommandID)

	// agent 上报：小文件(100)谎标 over=true；大文件(5000)谎标 over=false。
	files := []ScanFile{
		{Path: "A/small.yml", Size: 100, IsText: true, OverThreshold: true},
		{Path: "A/big.yml", Size: 5000, IsText: true, OverThreshold: false},
	}
	if err := svc.ReceiveScan(task.ScanCommandID, files, ""); err != nil {
		t.Fatalf("scan 回传应成功，实际 %v", err)
	}

	got, _ := svc.Get(task.ID)
	if got.OverThresholdCount != 1 {
		t.Fatalf("按设置阈值 2000 重算应仅 1 个超阈值（5000 的 big.yml），实际 %d", got.OverThresholdCount)
	}
	var m scanManifest
	if err := json.Unmarshal([]byte(got.Manifest), &m); err != nil {
		t.Fatalf("解析 manifest 失败: %v", err)
	}
	byPath := map[string]ScanFile{}
	for _, f := range m.Files {
		byPath[f.Path] = f
	}
	if byPath["A/small.yml"].OverThreshold {
		t.Fatal("100 字节文件不应超阈值（控制面应纠正 agent 谎标的 over=true）")
	}
	if !byPath["A/big.yml"].OverThreshold {
		t.Fatal("5000 字节文件应超阈值（控制面应纠正 agent 谎标的 over=false）")
	}
}

// TestSubmitRejectsUnconfirmedOverThreshold 守护 FR-61：submit 超阈值确认门读设置重算后的 manifest 标记，
// 未确认超阈值文件 → 拒（须显式确认才纳入）。
func TestSubmitRejectsUnconfirmedOverThreshold(t *testing.T) {
	db := newRFTaskTestDB(t)
	taskRepo := repository.NewReverseFetchTaskRepository(db)
	cmdRepo := repository.NewAgentCommandRepository(db)
	auditRepo := repository.NewAuditLogRepository(db)
	fileSvc := NewFileService(db, repository.NewFileObjectRepository(db), repository.NewFileRevisionRepository(db), auditRepo)
	settings := settingsWith(t, map[string]string{SettingReverseFetchMaxFileBytes: "2000"})
	svc := NewReverseFetchTaskService(db, taskRepo, cmdRepo, fileSvc, auditRepo, settings)
	cmdSvc := NewAgentCommandService(db, cmdRepo, fileSvc, auditRepo)
	cmdSvc.SetSubmitIngestReceiver(svc)

	task, err := svc.CreateScanTask("prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "")
	if err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	fetchCmd(t, db, task.ScanCommandID)
	files := []ScanFile{{Path: "A/big.yml", Size: 5000, IsText: true, OverThreshold: false}}
	if err := svc.ReceiveScan(task.ScanCommandID, files, ""); err != nil {
		t.Fatalf("scan 回传失败: %v", err)
	}

	// 未确认提交超阈值文件 → 拒。
	if _, err := svc.Submit(task.ID, []string{"A/big.yml"}, false, "alice", ""); err == nil {
		t.Fatal("未确认提交超阈值文件应被拒")
	}
	// 显式确认 → 通过（任务进 fetching）。
	out, err := svc.Submit(task.ID, []string{"A/big.yml"}, true, "alice", "")
	if err != nil {
		t.Fatalf("确认后提交应成功，实际 %v", err)
	}
	if out.Status != model.ReverseFetchTaskFetching {
		t.Fatalf("确认提交后任务应 fetching，实际 %s", out.Status)
	}
}
