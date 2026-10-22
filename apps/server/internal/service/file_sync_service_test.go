package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// fileSyncIdentityOf 由命令反推一个归属正确的权威身份，供正常路径用例使用。
// file-sync 数据面要求调用者是命令所属实例（blob 侧另有源/目标约束）。
func fileSyncIdentityOf(cmd *model.AgentCommand) agentauth.Identity {
	return agentauth.Identity{Namespace: cmd.NamespaceCode, ServerID: cmd.ServerID, IdentityID: "test-" + cmd.ServerID}
}

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
	if err := svc.ReceiveSourceManifest(fileSyncIdentityOf(sourceCmd), task.ID, sourceCmd.ID, []FileSyncManifestFile{
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

// TestFileSyncStoreBlobRejectsTerminalTask 覆盖终态任务不再接受源 agent 上传 blob。
//
// 为什么必须拒：blob 目录按 taskId 分片且没有任务级清理路径，终态任务继续落盘只会留下
// 无人回收的垃圾；同时用非终态用例反向确认拦截条件没有过宽。
func TestFileSyncStoreBlobRejectsTerminalTask(t *testing.T) {
	const content = "config.yml"
	// 用真实 sha256：非终态用例会真的走完落盘并按内容校验，占位哈希会被判为非法参数。
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	identity := agentauth.Identity{Namespace: "prod", ServerID: "source-1", IdentityID: "test-source-1"}

	cases := []struct {
		name    string
		status  string
		wantErr error
	}{
		{name: "已完成任务拒写", status: model.FileSyncTaskStatusSucceeded, wantErr: apperr.ErrFileSyncTaskState},
		{name: "已失败任务拒写", status: model.FileSyncTaskStatusFailed, wantErr: apperr.ErrFileSyncTaskState},
		{name: "已终止任务拒写", status: model.FileSyncTaskStatusTerminated, wantErr: apperr.ErrFileSyncTaskState},
		{name: "熔断任务拒写", status: model.FileSyncTaskStatusCircuitBroken, wantErr: apperr.ErrFileSyncTaskState},
		{name: "非终态任务仍可写入", status: model.FileSyncTaskStatusCached},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, db, reg := newFileSyncTestService(t)
			registerFileSyncInstance(t, reg, "prod", "source-1", "bukkit")
			task := mustCreateFileSyncTask(t, svc)
			if err := db.Model(&model.FileSyncTask{}).Where("id = ?", task.ID).
				Update("status", tc.status).Error; err != nil {
				t.Fatalf("把任务置为 %s 失败：%v", tc.status, err)
			}

			err := svc.StoreBlob(identity, task.ID, hash, strings.NewReader(content))
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("任务状态 %s 时应允许写入 blob，实际错误：%v", tc.status, err)
				}
				if _, statErr := os.Stat(svc.blobPath(task.ID, hash)); statErr != nil {
					t.Fatalf("任务状态 %s 时应已落盘 blob，实际 stat 失败：%v", tc.status, statErr)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("任务状态 %s 时应返回 %v，实际：%v", tc.status, tc.wantErr, err)
			}
			if _, statErr := os.Stat(svc.blobPath(task.ID, hash)); !os.IsNotExist(statErr) {
				t.Fatalf("任务状态 %s 时不应落盘 blob，实际 stat 结果：%v", tc.status, statErr)
			}
		})
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
	if err := svc.ReceiveSourceManifest(fileSyncIdentityOf(sourceCmd), task.ID, sourceCmd.ID, []FileSyncManifestFile{
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
	if err := svc.ReceiveTargetResult(fileSyncIdentityOf(&cmds[0]), task.ID, cmds[0].ID, FileSyncTargetResult{OK: true, BackupPath: ".beacon/a", CurrentFileCount: 1, ChangedFileCount: 1}); err != nil {
		t.Fatalf("回传成功目标失败：%v", err)
	}
	if err := svc.ReceiveTargetResult(fileSyncIdentityOf(&cmds[1]), task.ID, cmds[1].ID, FileSyncTargetResult{OK: false, Reason: "磁盘不可写"}); err != nil {
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

func TestFileSyncTargetResultDispatchesNextBatchAfterFirstBatchSucceeds(t *testing.T) {
	svc, db, reg := newFileSyncTestService(t)
	for _, id := range []string{"source-1", "target-1", "target-2", "target-3"} {
		registerFileSyncInstance(t, reg, "prod", id, "bukkit")
	}
	task := mustCreateReadyFileSyncTask(t, svc, db, 2, 0)
	if _, err := svc.PlanTargets(task.ID, []string{"target-1", "target-2", "target-3"}, "admin", "127.0.0.1"); err != nil {
		t.Fatalf("规划目标失败：%v", err)
	}
	if _, err := svc.Start(task.ID, "admin", "127.0.0.1"); err != nil {
		t.Fatalf("启动任务失败：%v", err)
	}
	cmds := applyCommands(t, db, task.ID)
	if len(cmds) != 2 {
		t.Fatalf("首批应下发 2 条目标命令，实际 %d 条：%+v", len(cmds), cmds)
	}
	for _, cmd := range cmds {
		markCommandFetched(t, db, cmd.ID)
		if err := svc.ReceiveTargetResult(fileSyncIdentityOf(&cmd), task.ID, cmd.ID, FileSyncTargetResult{OK: true, BackupPath: ".beacon/a"}); err != nil {
			t.Fatalf("回传首批目标结果失败：%v", err)
		}
	}

	cmds = applyCommands(t, db, task.ID)
	if len(cmds) != 3 || cmds[2].ServerID != "target-3" {
		t.Fatalf("首批完成后应立即调度第二批目标，实际：%+v", cmds)
	}
	batches, err := repository.NewFileSyncRepository(db).ListBatches(task.ID)
	if err != nil {
		t.Fatalf("查询批次失败：%v", err)
	}
	if batches[0].Status != model.FileSyncBatchStatusSucceeded || batches[1].Status != model.FileSyncBatchStatusRunning {
		t.Fatalf("首批完成后批次状态不符合预期：%+v", batches)
	}
}

func TestFileSyncTargetResultCompletesTaskAfterLastBatchSucceeds(t *testing.T) {
	svc, db, reg := newFileSyncTestService(t)
	for _, id := range []string{"source-1", "target-1", "target-2", "target-3"} {
		registerFileSyncInstance(t, reg, "prod", id, "bukkit")
	}
	task := mustCreateReadyFileSyncTask(t, svc, db, 2, 0)
	if _, err := svc.PlanTargets(task.ID, []string{"target-1", "target-2", "target-3"}, "admin", "127.0.0.1"); err != nil {
		t.Fatalf("规划目标失败：%v", err)
	}
	if _, err := svc.Start(task.ID, "admin", "127.0.0.1"); err != nil {
		t.Fatalf("启动任务失败：%v", err)
	}
	for _, cmd := range applyCommands(t, db, task.ID) {
		markCommandFetched(t, db, cmd.ID)
		if err := svc.ReceiveTargetResult(fileSyncIdentityOf(&cmd), task.ID, cmd.ID, FileSyncTargetResult{OK: true, BackupPath: ".beacon/a"}); err != nil {
			t.Fatalf("回传目标结果失败：%v", err)
		}
	}
	cmds := applyCommands(t, db, task.ID)
	markCommandFetched(t, db, cmds[len(cmds)-1].ID)
	if err := svc.ReceiveTargetResult(fileSyncIdentityOf(&cmds[len(cmds)-1]), task.ID, cmds[len(cmds)-1].ID, FileSyncTargetResult{OK: true, BackupPath: ".beacon/b"}); err != nil {
		t.Fatalf("回传最后一批目标结果失败：%v", err)
	}

	got, err := repository.NewFileSyncRepository(db).GetTask(task.ID)
	if err != nil {
		t.Fatalf("查询任务失败：%v", err)
	}
	if got.Status != model.FileSyncTaskStatusSucceeded || got.FinishedAt == nil {
		t.Fatalf("最后一批成功后任务应完成，实际：%+v", got)
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
	if err := svc.ReceiveSourceManifest(fileSyncIdentityOf(sourceCmd), task.ID, sourceCmd.ID, []FileSyncManifestFile{
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
	if err := svc.ReceiveSourceManifest(fileSyncIdentityOf(sourceCmd), task.ID, sourceCmd.ID, []FileSyncManifestFile{
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

func TestFileSyncEventsPushSourceReadyTaskPatch(t *testing.T) {
	svc, _, reg := newFileSyncTestService(t)
	for _, id := range []string{"source-1", "target-1"} {
		registerFileSyncInstance(t, reg, "prod", id, "bukkit")
	}
	task := mustCreateFileSyncTask(t, svc)
	if _, err := svc.PlanTargets(task.ID, []string{"target-1"}, "admin", "127.0.0.1"); err != nil {
		t.Fatalf("规划失败：%v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newRecordingFileSyncSink()
	go func() {
		_ = svc.RunEvents(ctx, task.ID, 0, sink)
	}()
	if evt := sink.wait(t); evt.Type != FileSyncEventTypeLog {
		t.Fatalf("连接后应先回放持久化日志，实际 %+v", evt)
	}

	sourceCmd := latestFileSyncCommand(t, svc.db, task.ID, model.CommandTypeFileSyncSource)
	markCommandFetched(t, svc.db, sourceCmd.ID)
	if err := svc.ReceiveSourceManifest(fileSyncIdentityOf(sourceCmd), task.ID, sourceCmd.ID, []FileSyncManifestFile{
		{Path: "config.yml", Size: 12, Hash: testSHA256("config.yml")},
	}, "127.0.0.1"); err != nil {
		t.Fatalf("回传源清单失败：%v", err)
	}

	evt := sink.waitFor(t, func(evt FileSyncEvent) bool {
		return evt.Type == FileSyncEventTypeTask && evt.Task != nil && evt.Task.SourceReady
	})
	if evt.Task.SourceFileCount != 1 || evt.Task.SourceTotalBytes != 12 || evt.Task.Status != model.FileSyncTaskStatusPlanned {
		t.Fatalf("源清单就绪事件应携带任务补丁，实际 %+v", evt.Task)
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
	instSvc := NewInstanceService(db, reg, repository.NewServerPlacementRepository(db),
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

func mustCreateReadyFileSyncTask(t *testing.T, svc *FileSyncService, db *gorm.DB, batchSize, intervalSec int) *model.FileSyncTask {
	t.Helper()
	task, err := svc.CreateTask(CreateFileSyncTaskParams{
		Namespace: "prod", SourceServerID: "source-1", Directory: "plugins/demo",
		BatchSize: batchSize, IntervalSec: intervalSec, FailureThresholdPercent: 100, Operator: "admin",
	})
	if err != nil {
		t.Fatalf("创建任务失败：%v", err)
	}
	sourceCmd := latestFileSyncCommand(t, db, task.ID, model.CommandTypeFileSyncSource)
	markCommandFetched(t, db, sourceCmd.ID)
	if err := svc.ReceiveSourceManifest(fileSyncIdentityOf(sourceCmd), task.ID, sourceCmd.ID, []FileSyncManifestFile{
		{Path: "config.yml", Size: 12, Hash: testSHA256("config.yml")},
	}, "127.0.0.1"); err != nil {
		t.Fatalf("回传源清单失败：%v", err)
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

// TestCapReasonRunesContract 锁死 capReasonRunes 的契约：返回值的 rune 数恒 <= max。
//
// 这条契约是「error 列不会超宽」的唯一保证。边界必须覆盖：
// 放不下截断标记的小 max、max<=0、恰好等于上限（不加标记）、以及中文（多字节）。
func TestCapReasonRunesContract(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		max   int
		want  int  // 期望的 rune 数（-1 表示「等于原串」）
		empty bool // 期望空串
	}{
		{name: "空串", text: "", max: 10, want: 0},
		{name: "未超限原样返回", text: "abc", max: 10, want: 3},
		{name: "恰好等于上限不加标记", text: "abcde", max: 5, want: 5},
		{name: "超限加截断标记", text: strings.Repeat("a", 20), max: 10, want: 10},
		{name: "中文超限不切半字符", text: strings.Repeat("配置失败", 10), max: 12, want: 12},
		{name: "max 为 0 返回空", text: "abc", max: 0, empty: true},
		{name: "max 为负返回空", text: "abc", max: -1, empty: true},
		// 放不下 6 rune 的截断标记时硬截断，不得返回超长标记。
		{name: "max 小于标记长度", text: strings.Repeat("a", 20), max: 3, want: 3},
		{name: "max 恰等于标记长度", text: strings.Repeat("a", 20), max: 6, want: 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := capReasonRunes(c.text, c.max)
			if c.empty {
				if got != "" {
					t.Fatalf("期望空串，实际 %q", got)
				}
				return
			}
			if n := len([]rune(got)); n != c.want {
				t.Fatalf("rune 数应为 %d，实际 %d（%q）", c.want, n, got)
			}
			if c.max > 0 && len([]rune(got)) > c.max {
				t.Fatalf("违反契约：rune 数 %d 超过 max %d", len([]rune(got)), c.max)
			}
			// 截断产物必须是合法 UTF-8（按字节切中文会在此暴露）。
			if !utf8.ValidString(got) {
				t.Fatalf("截断后不是合法 UTF-8：%q", got)
			}
		})
	}
}

// TestTargetLogMessageFitsColumn 锁死「拼接后仍不超 file_sync_log.message 列宽」。
//
// message 是 size:512，且带「目标 <serverID> 同步失败：」前缀。只按 reason 收口会让
// 长原因 + 长 serverID 拼完溢出——而落日志在事务内，严格模式下 1406 会让整个事务失败。
func TestTargetLogMessageFitsColumn(t *testing.T) {
	// 极端组合：最长的 serverID 量级 + 已按 reason 上限收口的中文原因。
	serverID := strings.Repeat("s", 128)
	reason := capReasonRunes(strings.Repeat("配置解析失败", 200), fileSyncReasonMaxRunes)

	msg := targetLogMessage(serverID, model.FileSyncTargetStatusFailed, reason)
	if n := len([]rune(msg)); n > fileSyncLogMessageMaxRunes {
		t.Fatalf("message rune 数 %d 超过上限 %d（列宽 512）", n, fileSyncLogMessageMaxRunes)
	}
	if n := len([]rune(msg)); n > 512 {
		t.Fatalf("message rune 数 %d 超过物理列宽 512", n)
	}
	if !utf8.ValidString(msg) {
		t.Fatal("message 必须是合法 UTF-8")
	}
	// 截断也要保留前缀语义，运维才看得出是哪台服的哪一步。
	if !strings.Contains(msg, "同步失败") {
		t.Fatalf("截断后须保留失败语义，实际 %q", msg)
	}
}
