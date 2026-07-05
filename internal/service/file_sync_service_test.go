package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime"
)

func TestNormalizeFileSyncDirectory(t *testing.T) {
	valid := map[string]string{
		"plugins/MyPlugin": "plugins/MyPlugin",
		"./plugins//demo/": "plugins/demo",
		".":                ".",
	}
	for input, want := range valid {
		got, err := NormalizeFileSyncDirectory(input)
		if err != nil {
			t.Fatalf("目录 %q 应合法，实际错误：%v", input, err)
		}
		if got != want {
			t.Fatalf("目录 %q 归一结果应为 %q，实际 %q", input, want, got)
		}
	}

	invalid := []string{"", "../plugins", "plugins/../demo", "/plugins", "C:/server", "//host/share", "plugins\\demo", "plugins:demo"}
	for _, input := range invalid {
		if _, err := NormalizeFileSyncDirectory(input); err == nil {
			t.Fatalf("目录 %q 应被拒绝", input)
		}
	}
}

func TestFileSyncCreateTaskValidatesSourceAndPersists(t *testing.T) {
	svc, db, reg := newFileSyncTestService(t)
	registerFileSyncInstance(t, reg, "prod", "source-1", "bukkit")

	task, err := svc.CreateTask(CreateFileSyncTaskParams{
		Namespace: "prod", SourceServerID: "source-1", Operator: "admin",
		Directory: "./plugins//demo", BatchSize: 2, IntervalSec: 10, FailureThresholdPercent: 50,
	})
	if err != nil {
		t.Fatalf("创建任务应成功，实际错误：%v", err)
	}
	if task.ID == 0 || task.Status != model.FileSyncTaskStatusScanning || task.Directory != "plugins/demo" {
		t.Fatalf("任务持久化结果不符合预期：%+v", task)
	}
	cmd := latestFileSyncCommand(t, db, task.ID, model.CommandTypeFileSyncSource)
	if cmd.ServerID != "source-1" || cmd.Status != model.CommandStatusPending {
		t.Fatalf("创建任务应下发源扫描命令，实际：%+v", cmd)
	}
	if countFileSyncLogs(t, db, task.ID) != 1 {
		t.Fatal("创建任务应写入一条持久化日志")
	}
	if countAuditActions(t, db, model.ActionFileSyncCreate) != 1 {
		t.Fatal("创建任务应写入专项审计")
	}
}

func TestFileSyncStartRequiresSourceManifest(t *testing.T) {
	svc, _, reg := newFileSyncTestService(t)
	for _, id := range []string{"source-1", "target-1"} {
		registerFileSyncInstance(t, reg, "prod", id, "bukkit")
	}
	task := mustCreateFileSyncTask(t, svc)
	if _, err := svc.PlanTargets(task.ID, []string{"target-1"}, "admin", "127.0.0.1"); err != nil {
		t.Fatalf("规划目标失败：%v", err)
	}
	if _, err := svc.Start(task.ID, "admin", "127.0.0.1"); !errors.Is(err, apperr.ErrFileSyncTaskState) {
		t.Fatalf("源清单未就绪时启动应被拒绝，实际：%v", err)
	}
}

func TestFileSyncSourceManifestAndStartDispatchTargetCommand(t *testing.T) {
	svc, db, reg := newFileSyncTestService(t)
	for _, id := range []string{"source-1", "target-1"} {
		registerFileSyncInstance(t, reg, "prod", id, "bukkit")
	}
	task := mustCreateFileSyncTask(t, svc)
	sourceCmd := latestFileSyncCommand(t, db, task.ID, model.CommandTypeFileSyncSource)
	markCommandFetched(t, db, sourceCmd.ID)
	if err := svc.ReceiveSourceManifest(sourceCmd.ID, []FileSyncManifestFile{
		{Path: "config.yml", Size: 12, Hash: testSHA256("config.yml")},
	}, "127.0.0.1"); err != nil {
		t.Fatalf("回传源清单失败：%v", err)
	}
	if _, err := svc.PlanTargets(task.ID, []string{"target-1"}, "admin", "127.0.0.1"); err != nil {
		t.Fatalf("规划目标失败：%v", err)
	}
	started, err := svc.Start(task.ID, "admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("启动任务失败：%v", err)
	}
	if started.Status != model.FileSyncTaskStatusRunning {
		t.Fatalf("启动后状态应为 running，实际：%s", started.Status)
	}
	targetCmd := latestFileSyncCommand(t, db, task.ID, model.CommandTypeFileSyncApply)
	if targetCmd.ServerID != "target-1" {
		t.Fatalf("应向目标下发 apply 命令，实际：%+v", targetCmd)
	}
	targets, _ := repository.NewFileSyncRepository(db).ListTargets(task.ID)
	if len(targets) != 1 || targets[0].Status != model.FileSyncTargetStatusManifesting || targets[0].CommandID != targetCmd.ID {
		t.Fatalf("目标应进入 manifesting 并记录命令 id，实际：%+v", targets)
	}
}

func TestFileSyncTargetResultCircuitBreaksWhenBatchFailureRateExceeded(t *testing.T) {
	svc, db, reg := newFileSyncTestService(t)
	for _, id := range []string{"source-1", "target-1", "target-2", "target-3"} {
		registerFileSyncInstance(t, reg, "prod", id, "bukkit")
	}
	task, err := svc.CreateTask(CreateFileSyncTaskParams{
		Namespace: "prod", SourceServerID: "source-1", Directory: "plugins/demo",
		BatchSize: 2, IntervalSec: 0, FailureThresholdPercent: 40, Operator: "admin",
	})
	if err != nil {
		t.Fatalf("创建任务失败：%v", err)
	}
	sourceCmd := latestFileSyncCommand(t, db, task.ID, model.CommandTypeFileSyncSource)
	markCommandFetched(t, db, sourceCmd.ID)
	if err := svc.ReceiveSourceManifest(sourceCmd.ID, []FileSyncManifestFile{
		{Path: "config.yml", Size: 12, Hash: testSHA256("config.yml")},
	}, "127.0.0.1"); err != nil {
		t.Fatalf("回传源清单失败：%v", err)
	}
	if _, err := svc.PlanTargets(task.ID, []string{"target-1", "target-2", "target-3"}, "admin", "127.0.0.1"); err != nil {
		t.Fatalf("规划目标失败：%v", err)
	}
	if _, err := svc.Start(task.ID, "admin", "127.0.0.1"); err != nil {
		t.Fatalf("启动任务失败：%v", err)
	}
	cmds := applyCommands(t, db, task.ID)
	if len(cmds) != 2 {
		t.Fatalf("第一批应只下发 2 条目标命令，实际 %d 条：%+v", len(cmds), cmds)
	}
	for _, cmd := range cmds {
		markCommandFetched(t, db, cmd.ID)
	}
	if err := svc.ReceiveTargetResult(cmds[0].ID, FileSyncTargetResult{OK: true, BackupPath: ".beacon/a", CurrentFileCount: 1, ChangedFileCount: 1}); err != nil {
		t.Fatalf("回传成功目标失败：%v", err)
	}
	if err := svc.ReceiveTargetResult(cmds[1].ID, FileSyncTargetResult{OK: false, Reason: "磁盘不可写"}); err != nil {
		t.Fatalf("回传失败目标失败：%v", err)
	}
	got, err := repository.NewFileSyncRepository(db).GetTask(task.ID)
	if err != nil {
		t.Fatalf("查询任务失败：%v", err)
	}
	if got.Status != model.FileSyncTaskStatusCircuitBroken {
		t.Fatalf("失败率超过阈值后任务应熔断，实际：%s", got.Status)
	}
}

func TestFileSyncCreateTaskRejectsInvalidSource(t *testing.T) {
	svc, _, reg := newFileSyncTestService(t)
	registerFileSyncInstance(t, reg, "prod", "bc-1", "bungee")

	_, err := svc.CreateTask(CreateFileSyncTaskParams{
		Namespace: "prod", SourceServerID: "bc-1", Operator: "admin",
		Directory: "plugins/demo", BatchSize: 2, IntervalSec: 10, FailureThresholdPercent: 50,
	})
	if !errors.Is(err, apperr.ErrFileSyncSourceInvalid) {
		t.Fatalf("非 bukkit 源应返回 ErrFileSyncSourceInvalid，实际：%v", err)
	}

	_, err = svc.CreateTask(CreateFileSyncTaskParams{
		Namespace: "prod", SourceServerID: "missing", Operator: "admin",
		Directory: "plugins/demo", BatchSize: 2, IntervalSec: 10, FailureThresholdPercent: 50,
	})
	if !errors.Is(err, apperr.ErrFileSyncSourceInvalid) {
		t.Fatalf("离线源应返回 ErrFileSyncSourceInvalid，实际：%v", err)
	}
}

func TestFileSyncPlanTargetsDeduplicatesExcludesSourceAndBatches(t *testing.T) {
	svc, db, reg := newFileSyncTestService(t)
	for _, id := range []string{"source-1", "target-1", "target-2", "target-3"} {
		registerFileSyncInstance(t, reg, "prod", id, "bukkit")
	}
	task := mustCreateFileSyncTask(t, svc)

	planned, err := svc.PlanTargets(task.ID, []string{"target-1", "target-2", "target-1", "source-1", "target-3"}, "admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("规划目标应成功，实际错误：%v", err)
	}
	if planned.Status != model.FileSyncTaskStatusPlanned || planned.TargetCount != 3 || planned.BatchCount != 2 {
		t.Fatalf("规划后任务统计不符合预期：%+v", planned)
	}

	batches, err := repository.NewFileSyncRepository(db).ListBatches(task.ID)
	if err != nil {
		t.Fatalf("查询批次失败：%v", err)
	}
	if len(batches) != 2 || batches[0].PlannedCount != 2 || batches[1].PlannedCount != 1 {
		t.Fatalf("批次规划不符合预期：%+v", batches)
	}
	targets, err := repository.NewFileSyncRepository(db).ListTargets(task.ID)
	if err != nil {
		t.Fatalf("查询目标失败：%v", err)
	}
	got := make([]string, 0, len(targets))
	for _, target := range targets {
		got = append(got, target.ServerID)
	}
	want := []string{"target-1", "target-2", "target-3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("目标顺序应稳定去重并排除源，实际 %v", got)
		}
	}
}

func TestFileSyncPlanTargetsRejectsEmptyAndInvalidTarget(t *testing.T) {
	svc, _, reg := newFileSyncTestService(t)
	registerFileSyncInstance(t, reg, "prod", "source-1", "bukkit")
	registerFileSyncInstance(t, reg, "prod", "bc-1", "bungee")
	task := mustCreateFileSyncTask(t, svc)

	_, err := svc.PlanTargets(task.ID, []string{"source-1", "source-1"}, "admin", "127.0.0.1")
	if !errors.Is(err, apperr.ErrFileSyncNoTargets) {
		t.Fatalf("空目标应返回 ErrFileSyncNoTargets，实际：%v", err)
	}

	_, err = svc.PlanTargets(task.ID, []string{"bc-1"}, "admin", "127.0.0.1")
	if !errors.Is(err, apperr.ErrFileSyncTargetInvalid) {
		t.Fatalf("非 bukkit 目标应返回 ErrFileSyncTargetInvalid，实际：%v", err)
	}
}

func TestFileSyncControlActionsUseStateMachineAndAudit(t *testing.T) {
	svc, db, reg := newFileSyncTestService(t)
	for _, id := range []string{"source-1", "target-1"} {
		registerFileSyncInstance(t, reg, "prod", id, "bukkit")
	}
	task := mustCreateFileSyncTask(t, svc)

	if _, err := svc.Pause(task.ID, "admin", "127.0.0.1"); !errors.Is(err, apperr.ErrFileSyncTaskState) {
		t.Fatalf("planned 前暂停应被状态机拒绝，实际：%v", err)
	}
	if _, err := svc.PlanTargets(task.ID, []string{"target-1"}, "admin", "127.0.0.1"); err != nil {
		t.Fatalf("规划失败：%v", err)
	}
	var err error
	sourceCmd := latestFileSyncCommand(t, db, task.ID, model.CommandTypeFileSyncSource)
	markCommandFetched(t, db, sourceCmd.ID)
	if err := svc.ReceiveSourceManifest(sourceCmd.ID, []FileSyncManifestFile{
		{Path: "config.yml", Size: 12, Hash: testSHA256("config.yml")},
	}, "127.0.0.1"); err != nil {
		t.Fatalf("回传源清单失败：%v", err)
	}
	if task, err = svc.Start(task.ID, "admin", "127.0.0.1"); err != nil || task.Status != model.FileSyncTaskStatusRunning {
		t.Fatalf("启动后应为 running，task=%+v err=%v", task, err)
	}
	if task, err = svc.Pause(task.ID, "admin", "127.0.0.1"); err != nil || task.Status != model.FileSyncTaskStatusPaused {
		t.Fatalf("暂停后应为 paused，task=%+v err=%v", task, err)
	}
	if task, err = svc.Resume(task.ID, "admin", "127.0.0.1"); err != nil || task.Status != model.FileSyncTaskStatusRunning {
		t.Fatalf("继续后应为 running，task=%+v err=%v", task, err)
	}
	if task, err = svc.Terminate(task.ID, "admin", "127.0.0.1"); err != nil || task.Status != model.FileSyncTaskStatusTerminated {
		t.Fatalf("终止后应为 terminated，task=%+v err=%v", task, err)
	}
	if countAuditActions(t, db, model.ActionFileSyncTerminate) != 1 {
		t.Fatal("控制动作应写入专项审计")
	}
}

func TestFileSyncEventsReplayPersistedLogsAndPushLive(t *testing.T) {
	svc, _, reg := newFileSyncTestService(t)
	for _, id := range []string{"source-1", "target-1"} {
		registerFileSyncInstance(t, reg, "prod", id, "bukkit")
	}
	task := mustCreateFileSyncTask(t, svc)
	if _, err := svc.PlanTargets(task.ID, []string{"target-1"}, "admin", "127.0.0.1"); err != nil {
		t.Fatalf("规划失败：%v", err)
	}
	sourceCmd := latestFileSyncCommand(t, svc.db, task.ID, model.CommandTypeFileSyncSource)
	markCommandFetched(t, svc.db, sourceCmd.ID)
	if err := svc.ReceiveSourceManifest(sourceCmd.ID, []FileSyncManifestFile{
		{Path: "config.yml", Size: 12, Hash: testSHA256("config.yml")},
	}, "127.0.0.1"); err != nil {
		t.Fatalf("回传源清单失败：%v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newRecordingFileSyncSink()
	go func() {
		_ = svc.RunEvents(ctx, task.ID, 0, sink)
	}()

	if evt := sink.wait(t); evt.Type != FileSyncEventTypeLog || evt.LogID == 0 {
		t.Fatalf("连接后应先回放持久化日志，实际 %+v", evt)
	}
	if _, err := svc.Start(task.ID, "admin", "127.0.0.1"); err != nil {
		t.Fatalf("启动失败：%v", err)
	}
	evt := sink.waitFor(t, func(evt FileSyncEvent) bool {
		return evt.Type == FileSyncEventTypeTask && evt.Status == model.FileSyncTaskStatusRunning
	})
	if evt.Type != FileSyncEventTypeTask {
		t.Fatalf("启动后应推送任务状态事件，实际 %+v", evt)
	}
}

func TestFileSyncGetReturnsNewestLogTail(t *testing.T) {
	svc, db, reg := newFileSyncTestService(t)
	registerFileSyncInstance(t, reg, "prod", "source-1", "bukkit")
	task := mustCreateFileSyncTask(t, svc)
	repo := repository.NewFileSyncRepository(db)
	for i := 1; i <= 205; i++ {
		err := repo.CreateLog(&model.FileSyncLog{
			TaskID: task.ID, Level: model.FileSyncLogLevelInfo, Message: fmt.Sprintf("日志 %03d", i),
		})
		if err != nil {
			t.Fatalf("追加任务日志失败：%v", err)
		}
	}

	_, _, _, logs, err := svc.Get(task.ID)
	if err != nil {
		t.Fatalf("查询任务详情失败：%v", err)
	}
	if len(logs) != fileSyncMaxLogTail {
		t.Fatalf("详情应只返回日志尾部 %d 条，实际 %d 条", fileSyncMaxLogTail, len(logs))
	}
	if logs[0].Message != "日志 006" || logs[len(logs)-1].Message != "日志 205" {
		t.Fatalf("日志尾部应保持升序且包含最新记录，首尾为 %q / %q", logs[0].Message, logs[len(logs)-1].Message)
	}
}

func newFileSyncTestService(t *testing.T) (*FileSyncService, *gorm.DB, *runtime.Registry) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("打开 sqlite 失败：%v", err)
	}
	if err := db.AutoMigrate(
		&model.FileSyncTask{}, &model.FileSyncFile{}, &model.FileSyncBatch{}, &model.FileSyncTarget{}, &model.FileSyncLog{},
		&model.AgentCommand{},
		&model.AuditLog{}, &model.ZoneAssignment{}, &model.ServerOffline{},
	); err != nil {
		t.Fatalf("迁移测试表失败：%v", err)
	}
	reg := runtime.NewRegistry()
	auditRepo := repository.NewAuditLogRepository(db)
	instSvc := NewInstanceService(db, reg, repository.NewZoneAssignmentRepository(db),
		repository.NewServerOfflineRepository(db), auditRepo, 10*time.Second, 30*time.Second)
	cmdRepo := repository.NewAgentCommandRepository(db)
	svc := NewFileSyncService(db, repository.NewFileSyncRepository(db), instSvc, auditRepo, NewFileSyncEventHub())
	svc.SetCommandQueue(cmdRepo, nil)
	svc.SetBlobRoot(t.TempDir())
	return svc, db, reg
}

func registerFileSyncInstance(t *testing.T, reg *runtime.Registry, ns, serverID, role string) {
	t.Helper()
	_, err := reg.Register(&runtime.Instance{
		Namespace: ns, ServerID: serverID, Role: role, Address: serverID + ":25565",
	}, time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatalf("注册实例 %s 失败：%v", serverID, err)
	}
}

func mustCreateFileSyncTask(t *testing.T, svc *FileSyncService) *model.FileSyncTask {
	t.Helper()
	task, err := svc.CreateTask(CreateFileSyncTaskParams{
		Namespace: "prod", SourceServerID: "source-1", Directory: "plugins/demo",
		BatchSize: 2, IntervalSec: 10, FailureThresholdPercent: 50, Operator: "admin",
	})
	if err != nil {
		t.Fatalf("创建任务失败：%v", err)
	}
	return task
}

func countFileSyncLogs(t *testing.T, db *gorm.DB, taskID uint) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.FileSyncLog{}).Where("task_id = ?", taskID).Count(&n).Error; err != nil {
		t.Fatalf("统计任务日志失败：%v", err)
	}
	return n
}

func countAuditActions(t *testing.T, db *gorm.DB, action string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.AuditLog{}).Where("action = ?", action).Count(&n).Error; err != nil {
		t.Fatalf("统计审计失败：%v", err)
	}
	return n
}

func latestFileSyncCommand(t *testing.T, db *gorm.DB, taskID uint, cmdType string) *model.AgentCommand {
	t.Helper()
	var cmd model.AgentCommand
	needle := fmt.Sprintf(`"taskId":%d`, taskID)
	if err := db.Where("type = ? AND payload LIKE ?", cmdType, "%"+needle+"%").
		Order("id desc").First(&cmd).Error; err != nil {
		t.Fatalf("查询文件同步命令失败：%v", err)
	}
	return &cmd
}

func applyCommands(t *testing.T, db *gorm.DB, taskID uint) []model.AgentCommand {
	t.Helper()
	var cmds []model.AgentCommand
	needle := fmt.Sprintf(`"taskId":%d`, taskID)
	if err := db.Where("type = ? AND payload LIKE ?", model.CommandTypeFileSyncApply, "%"+needle+"%").
		Order("id asc").Find(&cmds).Error; err != nil {
		t.Fatalf("查询目标同步命令失败：%v", err)
	}
	return cmds
}

func markCommandFetched(t *testing.T, db *gorm.DB, commandID uint) {
	t.Helper()
	if err := db.Model(&model.AgentCommand{}).Where("id = ?", commandID).
		Update("status", model.CommandStatusFetched).Error; err != nil {
		t.Fatalf("标记命令 fetched 失败：%v", err)
	}
}

func testSHA256(seed string) string {
	switch seed {
	case "config.yml":
		return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	default:
		return "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	}
}

type recordingFileSyncSink struct {
	ch chan FileSyncEvent
}

func newRecordingFileSyncSink() *recordingFileSyncSink {
	return &recordingFileSyncSink{ch: make(chan FileSyncEvent, 16)}
}

func (s *recordingFileSyncSink) Send(evt FileSyncEvent) error {
	s.ch <- evt
	return nil
}

func (s *recordingFileSyncSink) wait(t *testing.T) FileSyncEvent {
	t.Helper()
	select {
	case evt := <-s.ch:
		return evt
	case <-time.After(2 * time.Second):
		t.Fatal("等待文件同步事件超时")
		return FileSyncEvent{}
	}
}

func (s *recordingFileSyncSink) waitFor(t *testing.T, pred func(FileSyncEvent) bool) FileSyncEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case evt := <-s.ch:
			if pred(evt) {
				return evt
			}
		case <-deadline:
			t.Fatal("等待指定文件同步事件超时")
			return FileSyncEvent{}
		}
	}
}
