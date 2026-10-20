package service

import (
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// 本文件锁定 v1 回传类端点的**命令归属校验**（issue #146）：
// commandId 是自增小整数、完全可枚举，而 v1 数据面的 agent 共用同一把共享 token——
// 只按 commandId 处理，任一 agent 都能用猜到的 id 推进 / 污染别人的命令与任务。
// 因此归属不符必须按「命令不存在」拒，且**命令与任务不得有任何改写**（只断言返回错误不足）。

// TestCommandBelongsToAgent 直测归属判定的四个分支（含零值身份必须拒）。
func TestCommandBelongsToAgent(t *testing.T) {
	cmd := &model.AgentCommand{NamespaceCode: "prod", ServerID: "lobby-1"}
	cases := []struct {
		name     string
		cmd      *model.AgentCommand
		identity agentauth.Identity
		want     bool
	}{
		{"归属一致", cmd, testAgentIdentityOf("prod", "lobby-1"), true},
		{"跨实例", cmd, testAgentIdentityOf("prod", "lobby-2"), false},
		{"跨环境同名实例", cmd, testAgentIdentityOf("staging", "lobby-1"), false},
		{"空身份", cmd, agentauth.Identity{}, false},
		{"只给 namespace", cmd, agentauth.Identity{Namespace: "prod"}, false},
		{"只给 serverId", cmd, agentauth.Identity{ServerID: "lobby-1"}, false},
		{"空命令", nil, testAgentIdentityOf("prod", "lobby-1"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandBelongsToAgent(tc.cmd, tc.identity); got != tc.want {
				t.Fatalf("归属判定应为 %v，实际 %v", tc.want, got)
			}
		})
	}
}

// TestReceiveResyncResultRejectsForeignAgent 归属不符的重同步结果回传必须拒绝，
// 且命令仍是 fetched、结果摘要（失败成因）保持原样。
func TestReceiveResyncResultRejectsForeignAgent(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	cmdRepo := repository.NewAgentCommandRepository(db)

	if _, err := svc.applyRequestResyncForTest("prod", "lobby-1", "alice", ""); err != nil {
		t.Fatalf("构造重同步命令失败: %v", err)
	}
	cmd, err := svc.FetchPending("prod", "lobby-1")
	if err != nil || cmd == nil {
		t.Fatalf("领取重同步命令失败: cmd=%v err=%v", cmd, err)
	}
	// 失败回传本会写结果摘要，这里用它验证「拒绝时一行都不写」。
	for name, identity := range map[string]agentauth.Identity{
		"跨实例":   testAgentIdentityOf("prod", "lobby-2"),
		"跨环境":   testAgentIdentityOf("staging", "lobby-1"),
		"未解析身份": {},
	} {
		t.Run(name, func(t *testing.T) {
			if err := svc.ReceiveResyncResult(identity, cmd.ID, false, "冒充他服写入的成因"); !errors.Is(err, apperr.ErrCommandNotFound) {
				t.Fatalf("非归属回传必须按命令不存在拒绝，实际 %v", err)
			}
			got, err := cmdRepo.FindByID(cmd.ID)
			if err != nil {
				t.Fatalf("读取命令失败: %v", err)
			}
			if got.Status != model.CommandStatusFetched {
				t.Fatalf("拒绝后命令状态不得改写，期望 fetched，实际 %s", got.Status)
			}
			if got.ResultDetail != "" {
				t.Fatalf("拒绝后不得写入结果摘要，实际 %q", got.ResultDetail)
			}
		})
	}
}

// TestReceiveIngestRejectsForeignAgent 归属不符的 ingest 回传必须拒绝，
// 且不落任何文件、不记 file.import 审计、命令仍 fetched。
func TestReceiveIngestRejectsForeignAgent(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	cmdRepo := repository.NewAgentCommandRepository(db)

	if _, err := applyRequestReverseFetchForTest(svc, "prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", ""); err != nil {
		t.Fatalf("构造反向抓取命令失败: %v", err)
	}
	cmd, err := svc.FetchPending("prod", "lobby-1")
	if err != nil || cmd == nil {
		t.Fatalf("领取反向抓取命令失败: cmd=%v err=%v", cmd, err)
	}
	files := []ImportFile{{Path: "AllinCore/config.yml", Content: "a: 1\n"}}

	for name, identity := range map[string]agentauth.Identity{
		"跨实例":   testAgentIdentityOf("prod", "lobby-2"),
		"跨环境":   testAgentIdentityOf("staging", "lobby-1"),
		"未解析身份": {},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := svc.ReceiveIngest(identity, cmd.ID, files, "10.0.0.9")
			if !errors.Is(err, apperr.ErrCommandNotFound) {
				t.Fatalf("非归属回传必须按命令不存在拒绝，实际 %v", err)
			}
			if res != nil {
				t.Fatalf("拒绝时不得返回入库结果，实际 %+v", res)
			}
			// 未写入真源：文件对象一个都没落
			var objects int64
			if e := db.Model(&model.FileObject{}).Count(&objects).Error; e != nil {
				t.Fatalf("统计文件对象失败: %v", e)
			}
			if objects != 0 {
				t.Fatalf("拒绝时不得落任何文件，实际 %d 个", objects)
			}
			if n := countAudit(t, db, model.ActionFileImport); n != 0 {
				t.Fatalf("拒绝时不得记 file.import 审计，实际 %d 条", n)
			}
			got, e := cmdRepo.FindByID(cmd.ID)
			if e != nil {
				t.Fatalf("读取命令失败: %v", e)
			}
			if got.Status != model.CommandStatusFetched || got.ResultDetail != "" {
				t.Fatalf("拒绝后命令不得改写，期望 fetched/空摘要，实际 %s/%q", got.Status, got.ResultDetail)
			}
		})
	}
}

// scanToPendingReviewOn 在指定实例上建扫描任务并回传清单，把任务推到 pending-review。
// 与既有 scanToPendingReview 的差别只在可指定归属：同包多个用例会落在同一实例上，
// 而单活跃限流不允许同实例并存两条活跃任务。
func scanToPendingReviewOn(t *testing.T, db *gorm.DB, svc *ReverseFetchTaskService, namespace, serverID string, files []ScanFile) *model.ReverseFetchTask {
	t.Helper()
	task, err := applyCreateScanTaskForTest(svc, namespace, serverID, model.ScopeGroup, "area1", "", "alice", "")
	if err != nil {
		t.Fatalf("建扫描任务失败: %v", err)
	}
	fetchCmd(t, db, task.ScanCommandID)
	if err := svc.ReceiveScan(testAgentIdentityOf(namespace, serverID), task.ScanCommandID, files, ""); err != nil {
		t.Fatalf("scan 回传失败: %v", err)
	}
	got, err := svc.Get(task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	return got
}

// TestReceiveScanRejectsForeignAgent 归属不符的扫描清单回传必须拒绝，
// 且任务仍 scanning、清单瞬态为空、命令仍 fetched。
func TestReceiveScanRejectsForeignAgent(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	task, err := applyCreateScanTaskForTest(svc, "prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "")
	if err != nil {
		t.Fatalf("建扫描任务失败: %v", err)
	}
	fetchCmd(t, db, task.ScanCommandID)
	files := []ScanFile{{Path: "AllinCore/config.yml", Size: 1024, IsText: true}}

	for name, identity := range map[string]agentauth.Identity{
		"跨实例":   testAgentIdentityOf("prod", "lobby-2"),
		"跨环境":   testAgentIdentityOf("staging", "lobby-1"),
		"未解析身份": {},
	} {
		t.Run(name, func(t *testing.T) {
			if err := svc.ReceiveScan(identity, task.ScanCommandID, files, "10.0.0.9"); !errors.Is(err, apperr.ErrCommandNotFound) {
				t.Fatalf("非归属回传必须按命令不存在拒绝，实际 %v", err)
			}
			got, err := svc.Get(task.ID)
			if err != nil {
				t.Fatalf("读取任务失败: %v", err)
			}
			if got.Status != model.ReverseFetchTaskScanning {
				t.Fatalf("拒绝后任务状态不得改写，期望 scanning，实际 %s", got.Status)
			}
			if got.Manifest != "" || got.TotalFiles != 0 || got.OverThresholdCount != 0 {
				t.Fatalf("拒绝后不得写入扫描清单，实际 manifest=%q total=%d over=%d",
					got.Manifest, got.TotalFiles, got.OverThresholdCount)
			}
			cmd, e := repository.NewAgentCommandRepository(db).FindByID(task.ScanCommandID)
			if e != nil {
				t.Fatalf("读取命令失败: %v", e)
			}
			if cmd.Status != model.CommandStatusFetched {
				t.Fatalf("拒绝后命令状态不得改写，期望 fetched，实际 %s", cmd.Status)
			}
		})
	}
}

// TestReceiveErrorRejectsForeignAgent 归属不符的错误回传必须拒绝，
// 且任务未被打成 failed、lastError 为空、命令仍 fetched、无错误审计（防定向 DoS）。
func TestReceiveErrorRejectsForeignAgent(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	task, err := applyCreateScanTaskForTest(svc, "prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "")
	if err != nil {
		t.Fatalf("建扫描任务失败: %v", err)
	}
	fetchCmd(t, db, task.ScanCommandID)

	for name, identity := range map[string]agentauth.Identity{
		"跨实例":   testAgentIdentityOf("prod", "lobby-2"),
		"跨环境":   testAgentIdentityOf("staging", "lobby-1"),
		"未解析身份": {},
	} {
		t.Run(name, func(t *testing.T) {
			if err := svc.ReceiveError(identity, task.ScanCommandID, "冒充他服写入的失败成因", "10.0.0.9"); !errors.Is(err, apperr.ErrCommandNotFound) {
				t.Fatalf("非归属回传必须按命令不存在拒绝，实际 %v", err)
			}
			got, err := svc.Get(task.ID)
			if err != nil {
				t.Fatalf("读取任务失败: %v", err)
			}
			if got.Status != model.ReverseFetchTaskScanning {
				t.Fatalf("拒绝后任务不得转 failed，期望 scanning，实际 %s", got.Status)
			}
			if got.LastError != "" {
				t.Fatalf("拒绝后不得写入失败成因，实际 %q", got.LastError)
			}
			cmd, e := repository.NewAgentCommandRepository(db).FindByID(task.ScanCommandID)
			if e != nil {
				t.Fatalf("读取命令失败: %v", e)
			}
			if cmd.Status != model.CommandStatusFetched || cmd.ResultDetail != "" {
				t.Fatalf("拒绝后命令不得改写，期望 fetched/空摘要，实际 %s/%q", cmd.Status, cmd.ResultDetail)
			}
			if n := rfCountAudit(t, db, model.ActionFileReverseFetchError); n != 0 {
				t.Fatalf("拒绝时不得记错误审计，实际 %d 条", n)
			}
		})
	}

	// submit 模式同一条归属闸：不得终结 fetching 任务、不得撤销审批期授权（此处置失败态即证明未走到撤销分支）。
	// 用另一个实例建任务，避免与上面的 scanning 任务撞单活跃限流（同实例仅允许一条活跃任务）。
	submitTask := scanToPendingReviewOn(t, db, svc, "prod", "lobby-9", []ScanFile{{Path: "A/config.yml", Size: 1, IsText: true}})
	submitted, subErr := applySubmitForTest(svc, submitTask.ID, []string{"A/config.yml"}, false, "alice", "")
	if subErr != nil {
		t.Fatalf("提交选定集失败: %v", subErr)
	}
	fetchCmd(t, db, submitted.SubmitCommandID)
	if err := svc.ReceiveError(testAgentIdentityOf("prod", "lobby-8"), submitted.SubmitCommandID, "冒充", ""); !errors.Is(err, apperr.ErrCommandNotFound) {
		t.Fatalf("submit 模式非归属回传必须按命令不存在拒绝，实际 %v", err)
	}
	fetching, ferr := svc.Get(submitTask.ID)
	if ferr != nil {
		t.Fatalf("读取 submit 任务失败: %v", ferr)
	}
	if fetching.Status != model.ReverseFetchTaskFetching || fetching.LastError != "" {
		t.Fatalf("submit 模式拒绝后任务不得改写，期望 fetching/空成因，实际 %s/%q", fetching.Status, fetching.LastError)
	}
}

// TestReceiveScanAndErrorAcceptOwningAgent 是归属校验的正向锁：
// 命令归属的那台 agent 自己回传时必须正常处理（不被归属闸误伤）。
func TestReceiveScanAndErrorAcceptOwningAgent(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	task, err := applyCreateScanTaskForTest(svc, "prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "")
	if err != nil {
		t.Fatalf("建扫描任务失败: %v", err)
	}
	fetchCmd(t, db, task.ScanCommandID)

	// 正向：归属正确的扫描清单正常落库、任务转 pending-review。
	if err := svc.ReceiveScan(testAgentIdentityOf("prod", "lobby-1"), task.ScanCommandID,
		[]ScanFile{{Path: "AllinCore/config.yml", Size: 100, IsText: true}}, ""); err != nil {
		t.Fatalf("归属正确应受理扫描清单，实际 %v", err)
	}
	reviewed, err := svc.Get(task.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if reviewed.Status != model.ReverseFetchTaskPendingReview || reviewed.TotalFiles != 1 {
		t.Fatalf("应转 pending-review 且计数 1，实际 status=%s total=%d", reviewed.Status, reviewed.TotalFiles)
	}

	// 正向：归属正确的错误回传正常把新任务转 failed。
	errTask, err := applyCreateScanTaskForTest(svc, "prod", "lobby-2", model.ScopeGroup, "area1", "", "alice", "")
	if err != nil {
		t.Fatalf("建第二个扫描任务失败: %v", err)
	}
	fetchCmd(t, db, errTask.ScanCommandID)
	if err := svc.ReceiveError(testAgentIdentityOf("prod", "lobby-2"), errTask.ScanCommandID, "读盘失败：permission denied", ""); err != nil {
		t.Fatalf("归属正确应受理错误回传，实际 %v", err)
	}
	failed, err := svc.Get(errTask.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if failed.Status != model.ReverseFetchTaskFailed || failed.LastError == "" {
		t.Fatalf("应转 failed 并记成因，实际 status=%s lastError=%q", failed.Status, failed.LastError)
	}
}

// TestReceiveResyncResultAndIngestAcceptOwningAgent 是 ingest 分支的正向锁：
// 归属正确时重同步结果与 ingest 回传都按原语义落地（归属闸不得误伤正常回传）。
func TestReceiveResyncResultAndIngestAcceptOwningAgent(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	cmdRepo := repository.NewAgentCommandRepository(db)

	// 重同步：归属正确 → done。
	if _, err := svc.applyRequestResyncForTest("prod", "lobby-1", "alice", ""); err != nil {
		t.Fatalf("构造重同步命令失败: %v", err)
	}
	resyncCmd, err := svc.FetchPending("prod", "lobby-1")
	if err != nil || resyncCmd == nil {
		t.Fatalf("领取重同步命令失败: cmd=%v err=%v", resyncCmd, err)
	}
	if err := svc.ReceiveResyncResult(testAgentIdentity(resyncCmd), resyncCmd.ID, true, ""); err != nil {
		t.Fatalf("归属正确应受理重同步结果，实际 %v", err)
	}
	done, _ := cmdRepo.FindByID(resyncCmd.ID)
	if done.Status != model.CommandStatusDone {
		t.Fatalf("归属正确应推进 done，实际 %s", done.Status)
	}

	// 反向抓取 ingest：归属正确 → 文件落库、命令 done。
	if _, err := applyRequestReverseFetchForTest(svc, "prod", "lobby-2", model.ScopeGroup, "area1", "", "alice", ""); err != nil {
		t.Fatalf("构造反向抓取命令失败: %v", err)
	}
	ingestCmd, err := svc.FetchPending("prod", "lobby-2")
	if err != nil || ingestCmd == nil {
		t.Fatalf("领取反向抓取命令失败: cmd=%v err=%v", ingestCmd, err)
	}
	res, err := svc.ReceiveIngest(testAgentIdentity(ingestCmd), ingestCmd.ID,
		[]ImportFile{{Path: "AllinCore/config.yml", Content: "a: 1\n"}}, "")
	if err != nil {
		t.Fatalf("归属正确应受理 ingest 回传，实际 %v", err)
	}
	if res == nil || res.Created != 1 {
		t.Fatalf("应落 1 个文件，实际 %+v", res)
	}
	obj, _ := repository.NewFileObjectRepository(db).FindByIdentity("prod", "area1", "AllinCore/config.yml", model.ScopeGroup, "")
	if obj == nil || obj.Content != "a: 1\n" {
		t.Fatalf("文件应落组覆盖，实际 %+v", obj)
	}
}
