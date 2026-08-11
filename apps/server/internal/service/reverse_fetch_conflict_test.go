package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/filetree"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

func newResolveApprovalService(t *testing.T, db *gorm.DB, svc *ReverseFetchTaskService) *ApprovalService {
	t.Helper()
	if err := db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}, &model.SensitiveAccessGrant{}); err != nil {
		t.Fatalf("迁移审批表失败: %v", err)
	}
	registry := authz.NewApprovalRegistry()
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	svc.SetApprovalService(approval)
	svc.SetSensitiveAccessGrants(NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db)))
	RegisterReverseFetchTaskApprovalAdapters(registry, svc)
	return approval
}

// gormShared 持有共享内存 sqlite 的 *gorm.DB（辅助把 helper 签名收敛，避免到处传 db）。
type gormShared struct{ db *gorm.DB }

func nowMinus1h() time.Time { return time.Now().Add(-1 * time.Hour) }
func nowMinus2h() time.Time { return time.Now().Add(-2 * time.Hour) }

func TestConflictDiffBundleJSONUsesDocumentedLowerCamelCase(t *testing.T) {
	body, err := json.Marshal(ConflictDiffBundle{Items: []ConflictDiffResult{{
		Path:            "A/config.yml",
		FetchedContent:  "new: 2\n",
		FetchedMD5:      "fetched-md5",
		ExistingContent: "old: 1\n",
		ExistingMD5:     "existing-md5",
		Version:         2,
	}}})
	if err != nil {
		t.Fatalf("序列化冲突正文包失败: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("解析冲突正文包失败: %v", err)
	}
	if _, ok := result["items"]; !ok {
		t.Fatalf("缺少文档约定的 items 字段: %s", body)
	}
	if _, ok := result["Items"]; ok {
		t.Fatalf("不得暴露 Go 字段名: %s", body)
	}
}

// seedExistingFile 在目标 group 层预置一个已发布文件（制造冲突）。
func seedExistingFile(t *testing.T, svc *ReverseFetchTaskService, path, content string) {
	t.Helper()
	if _, err := applyFileCreateForTest(svc.fileSvc, CreateFileParams{
		Namespace: "prod", Group: "area1", Path: path, ScopeLevel: model.ScopeGroup,
		Content: content, Operator: "seed", Comment: "预置",
	}); err != nil {
		t.Fatalf("预置已有文件 %s 失败: %v", path, err)
	}
}

// TestNoConflictGoesStraightToDone 提交后目标无已有版本 → 直接落库 done（不进 conflict-review）。
func TestNoConflictGoesStraightToDone(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	task := scanToPendingReview(t, db, svc, []ScanFile{
		{Path: "A/config.yml", Size: 1, IsText: true},
		{Path: "B/lang.yml", Size: 1, IsText: true},
	})
	got, _ := applySubmitForTest(svc, task.ID, []string{"A/config.yml", "B/lang.yml"}, false, "alice", "")
	fetchCmd(t, db, got.SubmitCommandID)
	res, err := svc.ReceiveSubmitIngest(got.SubmitCommandID, []ImportFile{
		{Path: "A/config.yml", Content: "k: 1\n"},
		{Path: "B/lang.yml", Content: "hi: hello\n"},
	}, "")
	if err != nil {
		t.Fatalf("无冲突应直接落库: %v", err)
	}
	if res.Created != 2 {
		t.Fatalf("应落 2 个文件，实际 created=%d updated=%d", res.Created, res.Updated)
	}
	done, _ := svc.Get(task.ID)
	if done.Status != model.ReverseFetchTaskDone {
		t.Fatalf("无冲突落库后应 done，实际 %s", done.Status)
	}
	if done.SubmitContent != "" {
		t.Fatal("无冲突路径不应暂存 submit_content")
	}
}

// TestConflictEntersReviewAndStashesNotLanded 有冲突 → 进 conflict-review、暂存全部回传内容、不落库覆盖已有版本。
func TestConflictEntersReviewAndStashesNotLanded(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	// 目标 group 层已有 A/config.yml（制造冲突）
	seedExistingFile(t, svc, "A/config.yml", "old: 1\n")
	existing, _ := repository.NewFileObjectRepository(db).FindByIdentity("prod", "area1", "A/config.yml", model.ScopeGroup, "")
	oldVersion := existing.Version

	task := scanToPendingReview(t, db, svc, []ScanFile{
		{Path: "A/config.yml", Size: 1, IsText: true},
		{Path: "B/new.yml", Size: 1, IsText: true},
	})
	got, _ := applySubmitForTest(svc, task.ID, []string{"A/config.yml", "B/new.yml"}, false, "alice", "")
	fetchCmd(t, db, got.SubmitCommandID)

	// 回传含冲突文件 A/config.yml（新内容）+ 非冲突 B/new.yml → 应进 conflict-review、不落库
	res, err := svc.ReceiveSubmitIngest(got.SubmitCommandID, []ImportFile{
		{Path: "A/config.yml", Content: "new: 2\n"},
		{Path: "B/new.yml", Content: "added: yes\n"},
	}, "")
	if err != nil {
		t.Fatalf("有冲突应静默进 conflict-review（无错），实际 %v", err)
	}
	if res != nil {
		t.Fatalf("进冲突审核不应有落库结果，实际 %+v", res)
	}
	cr, _ := svc.Get(task.ID)
	if cr.Status != model.ReverseFetchTaskConflictReview {
		t.Fatalf("有冲突应进 conflict-review，实际 %s", cr.Status)
	}
	if cr.SubmitContent == "" {
		t.Fatal("conflict-review 应暂存 submit 回传内容")
	}
	// 暂存信封含全部回传文件 + 冲突集
	var env submitContentEnvelope
	_ = json.Unmarshal([]byte(cr.SubmitContent), &env)
	if len(env.Files) != 2 || len(env.Conflicts) != 1 || env.Conflicts[0] != "A/config.yml" {
		t.Fatalf("信封应含 2 文件 + 1 冲突，实际 %+v", env)
	}
	// 不落库：已有版本不变、非冲突文件也未落（待 resolve）
	stillOld, _ := repository.NewFileObjectRepository(db).FindByIdentity("prod", "area1", "A/config.yml", model.ScopeGroup, "")
	if stillOld.Version != oldVersion || stillOld.Content != "old: 1\n" {
		t.Fatalf("进冲突审核不应覆盖已有版本，实际 v=%d content=%q", stillOld.Version, stillOld.Content)
	}
	if obj, _ := repository.NewFileObjectRepository(db).FindByIdentity("prod", "area1", "B/new.yml", model.ScopeGroup, ""); obj != nil {
		t.Fatal("非冲突文件也应待 resolve 才落库，进冲突审核期不落")
	}
	// 互斥：conflict-review 仍占活跃，同实例不可再建
	if _, err := applyCreateScanTaskForTest(svc, "prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", ""); err == nil {
		t.Fatal("conflict-review 应仍占活跃，互斥应拒新建")
	}
}

// TestConflictReviewRollsBackWhenSubmitCommandCASMisses 命令状态已被并发推进时，冲突暂存与正文授权都不得提交。
func TestConflictReviewRollsBackWhenSubmitCommandCASMisses(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	seedExistingFile(t, svc, "A/config.yml", "old: 1\n")
	task := scanToPendingReview(t, db, svc, []ScanFile{{Path: "A/config.yml", Size: 1, IsText: true}})
	submitted, err := applySubmitForTest(svc, task.ID, []string{"A/config.yml"}, false, "alice", "")
	if err != nil {
		t.Fatalf("下发提交命令失败: %v", err)
	}
	cmd := fetchCmd(t, db, submitted.SubmitCommandID)
	completed, err := repository.NewAgentCommandRepository(db).UpdateStatus(cmd.ID,
		model.CommandStatusFetched, model.CommandStatusDone, "")
	if err != nil || !completed {
		t.Fatalf("模拟并发完成命令失败: completed=%v err=%v", completed, err)
	}

	err = svc.enterConflictReview(submitted, cmd, []ImportFile{{Path: "A/config.yml", Content: "new: 2\n"}}, []string{"A/config.yml"})
	if err != apperr.ErrReverseFetchTaskState {
		t.Fatalf("命令 CAS 未命中必须拒绝任务变更，实际 %v", err)
	}
	still, err := svc.Get(task.ID)
	if err != nil || still.Status != model.ReverseFetchTaskFetching || still.SubmitContent != "" {
		t.Fatalf("事务回滚后任务应保持 fetching 且无正文，task=%+v err=%v", still, err)
	}
	grant, err := svc.grants.repo.FindByTargetRef("agent-command/" + fmt.Sprint(cmd.ID))
	if err != nil || grant == nil || grant.Status != model.SensitiveAccessGrantStatusPending {
		t.Fatalf("事务回滚后授权应保持 pending，grant=%+v err=%v", grant, err)
	}
}

// setupConflictReview 预置一个含 1 冲突 + 1 非冲突的 conflict-review 任务，返回任务与冲突文件抓取 md5。
func setupConflictReview(t *testing.T, db *gormShared, svc *ReverseFetchTaskService) (*model.ReverseFetchTask, string) {
	t.Helper()
	seedExistingFile(t, svc, "A/config.yml", "old: 1\n")
	task := scanToPendingReview(t, db.db, svc, []ScanFile{
		{Path: "A/config.yml", Size: 1, IsText: true},
		{Path: "B/new.yml", Size: 1, IsText: true},
	})
	got, _ := applySubmitForTest(svc, task.ID, []string{"A/config.yml", "B/new.yml"}, false, "alice", "")
	fetchCmd(t, db.db, got.SubmitCommandID)
	if _, err := svc.ReceiveSubmitIngest(got.SubmitCommandID, []ImportFile{
		{Path: "A/config.yml", Content: "new: 2\n"},
		{Path: "B/new.yml", Content: "added: yes\n"},
	}, ""); err != nil {
		t.Fatalf("进冲突审核失败: %v", err)
	}
	cr, _ := svc.Get(task.ID)
	if svc.grants != nil {
		grant, err := svc.grants.repo.FindByTargetRef("agent-command/" + fmt.Sprint(got.SubmitCommandID))
		if err != nil || grant == nil {
			t.Fatalf("读取预置授权失败: grant=%+v err=%v", grant, err)
		}
		if err := svc.grants.Consume(grant.GrantID, auth.HumanPrincipal("alice"), authz.OperationAgentCommandReverseSubmit,
			grant.TargetRef, reverseFetchSHA256(cr.SubmitContent), time.Now().UTC()); err != nil {
			t.Fatalf("预置消费提交正文授权失败: %v", err)
		}
	}
	return cr, filetree.ContentMD5("new: 2\n")
}

// TestResolveRequiresApprovalAndWorkerCommit 验证公开 resolve 无法直执，批准后由执行器在同一事务落库并写回执。
func TestResolveRequiresApprovalAndWorkerCommit(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	approval := newResolveApprovalService(t, shared.db, svc)
	cr, fetchedMD5 := setupConflictReview(t, shared, svc)

	if _, err := svc.Resolve(cr.ID, []ResolveDecision{{Path: "A/config.yml", Action: ResolveActionOverwrite, ReviewedMD5: fetchedMD5}}, "alice", ""); err != apperr.ErrForbidden {
		t.Fatalf("公开 resolve 必须拒绝旁路，实际 %v", err)
	}
	ticket, err := svc.RequestResolveApproval(cr.ID, []ResolveDecision{{Path: "A/config.yml", Action: ResolveActionOverwrite, ReviewedMD5: fetchedMD5}},
		"确认覆盖冲突文件", "resolve-approval", "alice", "", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建 resolve 审批失败: %v", err)
	}
	var req model.ApprovalRequest
	if err := shared.db.Where("request_id = ?", ticket.ApprovalRequestID).First(&req).Error; err != nil {
		t.Fatalf("读取 resolve 审批失败: %v", err)
	}
	if req.OperationKey != authz.OperationAgentCommandReverseResolve || strings.Contains(req.Payload, "new: 2") || strings.Contains(req.Payload, "old: 1") {
		t.Fatalf("审批冻结不得含文件正文，实际 operation=%s payload=%s", req.OperationKey, req.Payload)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), ""); err != nil {
		t.Fatalf("批准 resolve 失败: %v", err)
	}
	if _, err := NewApprovalWorker(approval).RunOnce(); err != nil {
		t.Fatalf("worker 执行 resolve 失败: %v", err)
	}
	done, _ := svc.Get(cr.ID)
	if done.Status != model.ReverseFetchTaskDone || done.SubmitContent != "" {
		t.Fatalf("worker 执行后任务应 done 且清瞬态，实际 %+v", done)
	}
	var receipt model.ApprovalExecutionReceipt
	if err := shared.db.Where("request_id = ?", ticket.ApprovalRequestID).First(&receipt).Error; err != nil {
		t.Fatalf("同事务执行应写回执: %v", err)
	}
	obj, _ := repository.NewFileObjectRepository(shared.db).FindByIdentity("prod", "area1", "A/config.yml", model.ScopeGroup, "")
	if obj == nil || obj.Content != "new: 2\n" || obj.Version != 2 {
		t.Fatalf("批准后的 worker 应覆盖冲突文件，实际 %+v", obj)
	}
}

// TestResolveApprovalNormalizesDecisionOrder 验证同一冲突处置集合的提交顺序不影响审批冻结载荷或幂等重放。
func TestResolveApprovalNormalizesDecisionOrder(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	newResolveApprovalService(t, shared.db, svc)
	task, fetchedMD5 := setupConflictReview(t, shared, svc)
	seedExistingFile(t, svc, "B/new.yml", "old: yes\n")

	var env submitContentEnvelope
	if err := json.Unmarshal([]byte(task.SubmitContent), &env); err != nil {
		t.Fatalf("解析暂存内容失败: %v", err)
	}
	env.Conflicts = []string{"A/config.yml", "B/new.yml"}
	content, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("序列化暂存内容失败: %v", err)
	}
	if err := shared.db.Model(&model.ReverseFetchTask{}).Where("id = ?", task.ID).Update("submit_content", string(content)).Error; err != nil {
		t.Fatalf("更新双冲突暂存内容失败: %v", err)
	}
	if err := shared.db.Model(&model.SensitiveAccessGrant{}).
		Where("target_ref = ?", "agent-command/"+fmt.Sprint(task.SubmitCommandID)).
		Update("content_version_hash", reverseFetchSHA256(string(content))).Error; err != nil {
		t.Fatalf("更新测试授权正文哈希失败: %v", err)
	}

	first := []ResolveDecision{
		{Path: "B/new.yml", Action: ResolveActionKeep},
		{Path: "A/config.yml", Action: ResolveActionOverwrite, ReviewedMD5: fetchedMD5},
	}
	second := []ResolveDecision{
		{Path: "A/config.yml", Action: ResolveActionOverwrite, ReviewedMD5: fetchedMD5},
		{Path: "B/new.yml", Action: ResolveActionKeep},
	}
	firstTicket, err := svc.RequestResolveApproval(task.ID, first, "确认冲突处置", "resolve-order", "alice", "", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("首次创建审批失败: %v", err)
	}
	secondTicket, err := svc.RequestResolveApproval(task.ID, second, "确认冲突处置", "resolve-order", "alice", "", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("同集合乱序重放必须复用审批，实际 %v", err)
	}
	if secondTicket.ApprovalRequestID != firstTicket.ApprovalRequestID {
		t.Fatalf("同集合乱序应复用同一审批，首次=%s 重放=%s", firstTicket.ApprovalRequestID, secondTicket.ApprovalRequestID)
	}
}

// TestResolveReceiptFailureRollsBack 验证回执插入失败时，冲突文件、任务终态与审计必须一起回滚。
func TestResolveReceiptFailureRollsBack(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	approval := newResolveApprovalService(t, shared.db, svc)
	cr, fetchedMD5 := setupConflictReview(t, shared, svc)
	ticket, err := svc.RequestResolveApproval(cr.ID, []ResolveDecision{{Path: "A/config.yml", Action: ResolveActionOverwrite, ReviewedMD5: fetchedMD5}},
		"验证回执回滚", "resolve-receipt-rollback", "alice", "", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建 resolve 审批失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), ""); err != nil {
		t.Fatalf("批准 resolve 审批失败: %v", err)
	}
	if err := shared.db.Exec("CREATE TRIGGER reject_resolve_receipt BEFORE INSERT ON approval_execution_receipt BEGIN SELECT RAISE(ABORT, '拒绝回执'); END").Error; err != nil {
		t.Fatalf("安装回执失败触发器失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("回执失败后的 worker 应收敛审批，processed=%d err=%v", processed, err)
	}
	done, err := svc.Get(cr.ID)
	if err != nil || done.Status != model.ReverseFetchTaskConflictReview || done.SubmitContent == "" {
		t.Fatalf("回执失败必须回滚任务状态和暂存内容，task=%+v err=%v", done, err)
	}
	obj, err := repository.NewFileObjectRepository(shared.db).FindByIdentity("prod", "area1", "A/config.yml", model.ScopeGroup, "")
	if err != nil || obj == nil || obj.Content != "old: 1\n" || obj.Version != 1 {
		t.Fatalf("回执失败必须回滚冲突文件，obj=%+v err=%v", obj, err)
	}
	if added, err := repository.NewFileObjectRepository(shared.db).FindByIdentity("prod", "area1", "B/new.yml", model.ScopeGroup, ""); err != nil || added != nil {
		t.Fatalf("回执失败不得留下非冲突文件，obj=%+v err=%v", added, err)
	}
	var receipts int64
	if err := shared.db.Model(&model.ApprovalExecutionReceipt{}).Where("request_id = ?", ticket.ApprovalRequestID).Count(&receipts).Error; err != nil || receipts != 0 {
		t.Fatalf("回执失败不得留下执行回执，count=%d err=%v", receipts, err)
	}
}

// TestResolveRejectsTamperedExecutionBinding 验证执行租约或审批绑定遭篡改时，冲突落库不会开始任何领域写入。
func TestResolveRejectsTamperedExecutionBinding(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*gorm.DB, *model.ApprovalRequest)
	}{
		{name: "错误申请标识", mutate: func(_ *gorm.DB, req *model.ApprovalRequest) { req.RequestID = "forged-request" }},
		{name: "错误载荷哈希", mutate: func(_ *gorm.DB, req *model.ApprovalRequest) { req.FrozenPayloadSHA256 = "forged-hash" }},
		{name: "错误模式版本", mutate: func(db *gorm.DB, req *model.ApprovalRequest) {
			_ = db.Model(&model.ApprovalRequest{}).Where("id = ?", req.ID).Update("schema_version", 99).Error
		}},
		{name: "错误租约", mutate: func(db *gorm.DB, req *model.ApprovalRequest) {
			_ = db.Model(&model.ApprovalRequest{}).Where("id = ?", req.ID).Update("lease_owner", "forged-lease").Error
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shared, svc, task, worker, claimed := prepareResolveExecution(t)
			tc.mutate(shared.db, claimed)
			_ = worker.execute(claimed)
			still, err := svc.Get(task.ID)
			if err != nil || still.Status != model.ReverseFetchTaskConflictReview || still.SubmitContent == "" {
				t.Fatalf("拒绝后任务必须保持 conflict-review 与暂存内容，task=%+v err=%v", still, err)
			}
			obj, err := repository.NewFileObjectRepository(shared.db).FindByIdentity("prod", "area1", "A/config.yml", model.ScopeGroup, "")
			if err != nil || obj == nil || obj.Content != "old: 1\n" || obj.Version != 1 {
				t.Fatalf("拒绝后不得覆盖冲突文件，obj=%+v err=%v", obj, err)
			}
			var receipts int64
			if err := shared.db.Model(&model.ApprovalExecutionReceipt{}).Count(&receipts).Error; err != nil || receipts != 0 {
				t.Fatalf("拒绝后不得写入执行回执，count=%d err=%v", receipts, err)
			}
			var succeeded int64
			if err := shared.db.Model(&model.ApprovalRequest{}).Where("status = ?", model.ApprovalStatusSucceeded).Count(&succeeded).Error; err != nil || succeeded != 0 {
				t.Fatalf("错误审批绑定不得标记成功，count=%d err=%v", succeeded, err)
			}
		})
	}
}

// TestResolveApplyRequiresPermit 验证私有领域入口在任何副作用前校验审批许可。
func TestResolveApplyRequiresPermit(t *testing.T) {
	shared, svc, task, _, _ := prepareResolveExecution(t)
	if err := shared.db.Transaction(func(tx *gorm.DB) error {
		_, _, err := svc.applyResolveInTx(tx, authz.ApprovalRequest{}, authz.Permit{}, reverseFetchResolveApprovalPayload{TaskID: task.ID, Operator: "alice"})
		return err
	}); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("缺失审批许可必须拒绝，实际 %v", err)
	}
	still, err := svc.Get(task.ID)
	if err != nil || still.Status != model.ReverseFetchTaskConflictReview || still.SubmitContent == "" {
		t.Fatalf("缺失许可不得变更任务，task=%+v err=%v", still, err)
	}
}

func prepareResolveExecution(t *testing.T) (*gormShared, *ReverseFetchTaskService, *model.ReverseFetchTask, *ApprovalWorker, *model.ApprovalRequest) {
	t.Helper()
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	approval := newResolveApprovalService(t, shared.db, svc)
	task, fetchedMD5 := setupConflictReview(t, shared, svc)
	ticket, err := svc.RequestResolveApproval(task.ID, []ResolveDecision{{Path: "A/config.yml", Action: ResolveActionOverwrite, ReviewedMD5: fetchedMD5}},
		"验证执行绑定", "resolve-binding", "alice", "", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建 resolve 审批失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), ""); err != nil {
		t.Fatalf("批准 resolve 审批失败: %v", err)
	}
	worker := NewApprovalWorker(approval)
	claimed, found, err := worker.claimNext()
	if err != nil || !found {
		t.Fatalf("认领 resolve 审批失败: found=%v err=%v", found, err)
	}
	return shared, svc, task, worker, claimed
}

// TestResolveApprovalRejectsChangedConflict 验证批准后冲突目标版本漂移会失败关闭，不写入抓取内容。
func TestResolveApprovalRejectsChangedConflict(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	approval := newResolveApprovalService(t, shared.db, svc)
	cr, fetchedMD5 := setupConflictReview(t, shared, svc)
	ticket, err := svc.RequestResolveApproval(cr.ID, []ResolveDecision{{Path: "A/config.yml", Action: ResolveActionOverwrite, ReviewedMD5: fetchedMD5}},
		"确认覆盖冲突文件", "resolve-drift", "alice", "", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建 resolve 审批失败: %v", err)
	}
	obj, _ := repository.NewFileObjectRepository(shared.db).FindByIdentity("prod", "area1", "A/config.yml", model.ScopeGroup, "")
	if err := shared.db.Model(obj).Updates(map[string]any{"content": "changed: 3\n", "content_md5": filetree.ContentMD5("changed: 3\n"), "version": obj.Version + 1}).Error; err != nil {
		t.Fatalf("制造冲突目标漂移失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), ""); err != nil {
		t.Fatalf("批准 resolve 失败: %v", err)
	}
	if _, err := NewApprovalWorker(approval).RunOnce(); err != nil {
		t.Fatalf("worker 处理漂移审批失败: %v", err)
	}
	var req model.ApprovalRequest
	if err := shared.db.Where("request_id = ?", ticket.ApprovalRequestID).First(&req).Error; err != nil {
		t.Fatalf("读取失败审批失败: %v", err)
	}
	if req.Status != model.ApprovalStatusFailed {
		t.Fatalf("冲突目标漂移应使审批失败，实际 %s", req.Status)
	}
	still, _ := svc.Get(cr.ID)
	if still.Status != model.ReverseFetchTaskConflictReview {
		t.Fatalf("漂移拒绝后任务应保留审核态，实际 %s", still.Status)
	}
}

// TestResolveApprovalRejectsChangedOutput 验证批准后的暂存输出变化会失败关闭，不能借旧审批落库新内容。
func TestResolveApprovalRejectsChangedOutput(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	approval := newResolveApprovalService(t, shared.db, svc)
	cr, fetchedMD5 := setupConflictReview(t, shared, svc)
	ticket, err := svc.RequestResolveApproval(cr.ID, []ResolveDecision{{Path: "A/config.yml", Action: ResolveActionOverwrite, ReviewedMD5: fetchedMD5}},
		"确认覆盖冲突文件", "resolve-output-drift", "alice", "", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建 resolve 审批失败: %v", err)
	}
	var env submitContentEnvelope
	if err := json.Unmarshal([]byte(cr.SubmitContent), &env); err != nil {
		t.Fatalf("解析暂存输出失败: %v", err)
	}
	env.Files["A/config.yml"] = "changed: 3\n"
	changed, _ := json.Marshal(env)
	if err := shared.db.Model(&model.ReverseFetchTask{}).Where("id = ?", cr.ID).Update("submit_content", string(changed)).Error; err != nil {
		t.Fatalf("制造暂存输出漂移失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), ""); err != nil {
		t.Fatalf("批准 resolve 失败: %v", err)
	}
	if _, err := NewApprovalWorker(approval).RunOnce(); err != nil {
		t.Fatalf("worker 处理输出漂移审批失败: %v", err)
	}
	var req model.ApprovalRequest
	if err := shared.db.Where("request_id = ?", ticket.ApprovalRequestID).First(&req).Error; err != nil {
		t.Fatalf("读取失败审批失败: %v", err)
	}
	if req.Status != model.ApprovalStatusFailed {
		t.Fatalf("暂存输出漂移应使审批失败，实际 %s", req.Status)
	}
	obj, _ := repository.NewFileObjectRepository(shared.db).FindByIdentity("prod", "area1", "A/config.yml", model.ScopeGroup, "")
	if obj == nil || obj.Content != "old: 1\n" || obj.Version != 1 {
		t.Fatalf("输出漂移不得覆盖已有文件，实际 %+v", obj)
	}
}

// TestConflictDiffRequiresGrant 验证旧服务入口不得绕过授权返回正文。
func TestConflictDiffRequiresGrant(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	cr, _ := setupConflictReview(t, shared, svc)
	if _, err := svc.ConflictDiff(cr.ID, "A/config.yml"); err != apperr.ErrForbidden {
		t.Fatalf("旧服务入口必须拒绝正文旁路，实际 %v", err)
	}
}

// TestReverseSubmitGrantConsumesConflictBundle 验证提交审批执行器原子签发待激活授权，回传后绑定正文哈希，
// 原申请主体一次读取全部冲突正文且不返回非冲突文件。
func TestReverseSubmitGrantConsumesConflictBundle(t *testing.T) {
	db := newRFTaskTestDB(t)
	svc := newRFTaskSvc(db)
	approval := newResolveApprovalService(t, db, svc)
	seedExistingFile(t, svc, "A/config.yml", "old: 1\n")
	seedExistingFile(t, svc, "C/other.yml", "old: 3\n")
	task := scanToPendingReview(t, db, svc, []ScanFile{
		{Path: "A/config.yml", Size: 1, IsText: true}, {Path: "B/new.yml", Size: 1, IsText: true}, {Path: "C/other.yml", Size: 1, IsText: true},
	})
	ticket, err := svc.RequestSubmitApproval(task.ID, []string{"A/config.yml", "B/new.yml", "C/other.yml"}, false,
		"读取待审核反向抓取内容", "reverse-submit-grant", "alice", "", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建提交审批失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), ""); err != nil {
		t.Fatalf("批准提交失败: %v", err)
	}
	if _, err := NewApprovalWorker(approval).RunOnce(); err != nil {
		t.Fatalf("执行提交审批失败: %v", err)
	}
	var submitted model.ReverseFetchTask
	if err := db.First(&submitted, task.ID).Error; err != nil {
		t.Fatalf("读取已提交任务失败: %v", err)
	}
	var grant model.SensitiveAccessGrant
	if err := db.Where("approval_request_id = ?", ticket.ApprovalRequestID).First(&grant).Error; err != nil {
		t.Fatalf("提交 worker 应创建 pending grant: %v", err)
	}
	if grant.Status != model.SensitiveAccessGrantStatusPending || grant.TargetRef != "agent-command/"+fmt.Sprint(submitted.SubmitCommandID) {
		t.Fatalf("pending grant 绑定不符: %+v", grant)
	}
	var receipt model.ApprovalExecutionReceipt
	if err := db.Where("request_id = ?", ticket.ApprovalRequestID).First(&receipt).Error; err != nil || !strings.Contains(receipt.ResultRef, grant.GrantID) {
		t.Fatalf("receipt 必须关联 grant：receipt=%+v err=%v", receipt, err)
	}
	fetchCmd(t, db, submitted.SubmitCommandID)
	if _, err := svc.ReceiveSubmitIngest(submitted.SubmitCommandID, []ImportFile{
		{Path: "A/config.yml", Content: "new: 2\n"}, {Path: "B/new.yml", Content: "private: no\n"}, {Path: "C/other.yml", Content: "new: 4\n"},
	}, ""); err != nil {
		t.Fatalf("回传冲突内容失败: %v", err)
	}
	if err := db.First(&grant, grant.ID).Error; err != nil || grant.Status != model.SensitiveAccessGrantStatusActive {
		t.Fatalf("回传后 grant 应按正文哈希激活：grant=%+v err=%v", grant, err)
	}
	reviewedMD5 := filetree.ContentMD5("new: 2\n")
	if _, err := svc.RequestResolveApproval(task.ID, []ResolveDecision{{Path: "A/config.yml", Action: ResolveActionOverwrite, ReviewedMD5: reviewedMD5}, {Path: "C/other.yml", Action: ResolveActionKeep}},
		"未消费正文不得处置", "reverse-resolve-before-consume", "alice", "", auth.HumanPrincipal("alice")); err != apperr.ErrForbidden {
		t.Fatalf("未消费正文不得提交 resolve 审批，实际 %v", err)
	}
	if _, err := svc.ConsumeApprovedConflictDiff(grant.GrantID, task.ID, auth.HumanPrincipal("mallory")); err != apperr.ErrSensitiveAccessWrongPrincipal {
		t.Fatalf("非原申请主体不得消费，实际 %v", err)
	}
	bundle, err := svc.ConsumeApprovedConflictDiff(grant.GrantID, task.ID, auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("原申请主体应可消费冲突正文: %v", err)
	}
	if len(bundle.Items) != 2 || bundle.Items[0].Path != "A/config.yml" || bundle.Items[1].Path != "C/other.yml" {
		t.Fatalf("一次消费只应返回全部冲突路径，实际 %+v", bundle.Items)
	}
	if bundle.Items[0].FetchedContent != "new: 2\n" || bundle.Items[0].ExistingContent != "old: 1\n" {
		t.Fatalf("A 冲突正文不符：%+v", bundle.Items[0])
	}
	if _, err := svc.ConsumeApprovedConflictDiff(grant.GrantID, task.ID, auth.HumanPrincipal("alice")); err != apperr.ErrSensitiveAccessConsumed {
		t.Fatalf("重复消费必须失败关闭，实际 %v", err)
	}
}

// TestResolveOverwriteRequiresReviewedMD5 overwrite 须带正确 reviewedMd5（盲确认 / 漂移 412）。
func TestResolveOverwriteRequiresReviewedMD5(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	cr, fetchedMD5 := setupConflictReview(t, shared, svc)

	// 盲确认（无 reviewedMd5）→ 412
	_, err := applyResolveForTest(svc, cr.ID, []ResolveDecision{{Path: "A/config.yml", Action: ResolveActionOverwrite}}, "alice", "")
	if err != apperr.ErrReverseFetchReviewMismatch {
		t.Fatalf("盲确认应 412 REVERSE_FETCH_REVIEW_MISMATCH，实际 %v", err)
	}
	// 错 md5 → 412
	_, err = applyResolveForTest(svc, cr.ID, []ResolveDecision{{Path: "A/config.yml", Action: ResolveActionOverwrite, ReviewedMD5: "deadbeef"}}, "alice", "")
	if err != apperr.ErrReverseFetchReviewMismatch {
		t.Fatalf("错 md5 应 412，实际 %v", err)
	}
	// 被拒后任务仍 conflict-review（未认领、可重 resolve）
	still, _ := svc.Get(cr.ID)
	if still.Status != model.ReverseFetchTaskConflictReview {
		t.Fatalf("自审失败后任务应仍 conflict-review，实际 %s", still.Status)
	}
	_ = fetchedMD5
}

// TestResolveOverwriteAndNonConflictLand overwrite 自审通过 + 非冲突文件一并落库、任务 done。
func TestResolveOverwriteAndNonConflictLand(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	cr, fetchedMD5 := setupConflictReview(t, shared, svc)

	res, err := applyResolveForTest(svc, cr.ID, []ResolveDecision{
		{Path: "A/config.yml", Action: ResolveActionOverwrite, ReviewedMD5: fetchedMD5},
	}, "alice", "")
	if err != nil {
		t.Fatalf("overwrite 自审通过应落库: %v", err)
	}
	// A/config.yml 覆盖（已有→新版本，Updated）+ B/new.yml 非冲突首发（Created）
	if res.Created != 1 || res.Updated != 1 {
		t.Fatalf("应 created=1（非冲突首发）updated=1（冲突覆盖），实际 created=%d updated=%d", res.Created, res.Updated)
	}
	done, _ := svc.Get(cr.ID)
	if done.Status != model.ReverseFetchTaskDone || done.SubmitContent != "" {
		t.Fatalf("resolve 后应 done 且清空 submit_content，实际 status=%s contentLen=%d", done.Status, len(done.SubmitContent))
	}
	// 冲突文件被覆盖为抓取内容
	repo := repository.NewFileObjectRepository(shared.db)
	overwritten, _ := repo.FindByIdentity("prod", "area1", "A/config.yml", model.ScopeGroup, "")
	if overwritten.Content != "new: 2\n" || overwritten.Version != 2 {
		t.Fatalf("冲突文件应被覆盖为抓取内容（v2），实际 v=%d content=%q", overwritten.Version, overwritten.Content)
	}
	// 非冲突文件落库
	if added, _ := repo.FindByIdentity("prod", "area1", "B/new.yml", model.ScopeGroup, ""); added == nil {
		t.Fatal("非冲突文件应在 resolve 时落库")
	}
	if rfCountAudit(t, shared.db, model.ActionFileReverseFetchIngest) != 1 {
		t.Fatal("resolve 应记一条 file.reverse-fetch-ingest 审计")
	}
}

// TestResolveKeepSkipsConflict keep 保留已有：冲突文件不覆盖，非冲突文件仍落库、任务 done。
func TestResolveKeepSkipsConflict(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	cr, _ := setupConflictReview(t, shared, svc)

	res, err := applyResolveForTest(svc, cr.ID, []ResolveDecision{
		{Path: "A/config.yml", Action: ResolveActionKeep},
	}, "alice", "")
	if err != nil {
		t.Fatalf("keep 应成功: %v", err)
	}
	// 只有非冲突 B/new.yml 首发；冲突 A/config.yml 保留已有、不落
	if res.Created != 1 || res.Updated != 0 {
		t.Fatalf("keep 应 created=1 updated=0，实际 created=%d updated=%d", res.Created, res.Updated)
	}
	repo := repository.NewFileObjectRepository(shared.db)
	kept, _ := repo.FindByIdentity("prod", "area1", "A/config.yml", model.ScopeGroup, "")
	if kept.Content != "old: 1\n" || kept.Version != 1 {
		t.Fatalf("keep 应保留已有版本不变，实际 v=%d content=%q", kept.Version, kept.Content)
	}
	done, _ := svc.Get(cr.ID)
	if done.Status != model.ReverseFetchTaskDone {
		t.Fatalf("resolve 后应 done，实际 %s", done.Status)
	}
}

// TestResolveRequiresDecisionForEachConflict 冲突集每项须有决定（缺决定 → 拒，不落库）。
func TestResolveRequiresDecisionForEachConflict(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	cr, _ := setupConflictReview(t, shared, svc)

	// 空决定（漏掉唯一冲突）→ 拒
	if _, err := applyResolveForTest(svc, cr.ID, nil, "alice", ""); err != apperr.ErrInvalidParam {
		t.Fatalf("漏掉冲突决定应 INVALID_PARAM，实际 %v", err)
	}
	// 决定指向非冲突 path → 冲突不存在
	if _, err := applyResolveForTest(svc, cr.ID, []ResolveDecision{{Path: "B/new.yml", Action: ResolveActionKeep}}, "alice", ""); err != apperr.ErrReverseFetchConflictNotFound {
		t.Fatalf("决定指向非冲突 path 应 CONFLICT_NOT_FOUND，实际 %v", err)
	}
	// 任务仍 conflict-review
	still, _ := svc.Get(cr.ID)
	if still.Status != model.ReverseFetchTaskConflictReview {
		t.Fatalf("拒后应仍 conflict-review，实际 %s", still.Status)
	}
}

// TestResolveConcurrentClaimOnce 并发双 resolve 只有一个认领成功（CAS conflict-review→ingesting）。
func TestResolveConcurrentClaimOnce(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	cr, fetchedMD5 := setupConflictReview(t, shared, svc)

	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount, stateErrCount := 0, 0
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := applyResolveForTest(svc, cr.ID, []ResolveDecision{
				{Path: "A/config.yml", Action: ResolveActionOverwrite, ReviewedMD5: fetchedMD5},
			}, "alice", "")
			mu.Lock()
			defer mu.Unlock()
			switch err {
			case nil:
				okCount++
			case apperr.ErrReverseFetchTaskState:
				stateErrCount++
			}
		}()
	}
	wg.Wait()
	if okCount != 1 || stateErrCount != 1 {
		t.Fatalf("并发双 resolve 应恰一个成功一个 STATE，实际 ok=%d state=%d", okCount, stateErrCount)
	}
	done, _ := svc.Get(cr.ID)
	if done.Status != model.ReverseFetchTaskDone {
		t.Fatalf("认领者落库后应 done，实际 %s", done.Status)
	}
}

// TestExpireClearsSubmitContent conflict-review 任务过期 → expired 且清空 submit_content、互斥解除。
func TestExpireClearsSubmitContent(t *testing.T) {
	shared := &gormShared{db: newRFTaskTestDB(t)}
	svc := newRFTaskSvc(shared.db)
	cr, _ := setupConflictReview(t, shared, svc)
	// 把创建时间推到 2 小时前
	if err := shared.db.Model(&model.ReverseFetchTask{}).Where("id = ?", cr.ID).
		Update("created_at", nowMinus2h()).Error; err != nil {
		t.Fatalf("改 created_at 失败: %v", err)
	}
	n, err := svc.ExpireStale(nowMinus1h())
	if err != nil || n != 1 {
		t.Fatalf("应过期 1 条，实际 %d / %v", n, err)
	}
	got, _ := svc.Get(cr.ID)
	if got.Status != model.ReverseFetchTaskExpired || got.SubmitContent != "" || got.Manifest != "" {
		t.Fatalf("过期后应 expired 且清空瞬态，实际 status=%s submitLen=%d manifestLen=%d",
			got.Status, len(got.SubmitContent), len(got.Manifest))
	}
	// 互斥解除：同实例可再建
	if _, err := applyCreateScanTaskForTest(svc, "prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", ""); err != nil {
		t.Fatalf("过期后同实例应可再建，实际 %v", err)
	}
}
