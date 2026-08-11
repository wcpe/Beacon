package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/filetree"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// newCommandSvcTestDB 打开内存 sqlite 并迁移命令 + 文件树 + zone 指派 + 审计表（不依赖 MySQL/DSN）。
// zone_assignment 供 FR-46 拓印 diff 经 FileEffectiveService 解期望合并值用（按拓印源 server 的 zone 归属解析）。
func newCommandSvcTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AgentCommand{}, &model.FileObject{}, &model.FileRevision{}, &model.ZoneAssignment{}, &model.AuditLog{},
		&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}, &model.SensitiveAccessGrant{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	for _, tbl := range []string{"agent_command", "file_object", "file_revision", "zone_assignment", "audit_log", "approval_request", "approval_execution_receipt", "sensitive_access_grant"} {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	return db
}

// TestRequestResyncApprovalOnlyExecutesAfterWorker 验证 FR-209 的申请、批准和异步命令下发闭环。
func TestRequestResyncApprovalOnlyExecutesAfterWorker(t *testing.T) {
	db := newCommandSvcTestDB(t)
	commands := newCommandSvc(db)
	registry := authz.NewApprovalRegistry()
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	commands.SetApprovalService(approval)
	RegisterAgentCommandApprovalAdapters(registry, commands, NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db)))

	if _, err := commands.RequestResync("prod", "lobby-1", "alice", "127.0.0.1"); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("公开 RequestResync 必须拒绝旁路，实际 %v", err)
	}
	if err := (agentCommandApprovalAdapter{}).Execute(authz.ApprovalRequest{}, authz.Permit{}); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("公开适配器 Execute 必须拒绝旁路，实际 %v", err)
	}

	ticket, err := commands.RequestResyncApproval("prod", "lobby-1", "需要重拉权威配置", "resync-approval-1", "alice", "127.0.0.1", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建重同步审批失败: %v", err)
	}
	var before int64
	if err := db.Model(&model.AgentCommand{}).Count(&before).Error; err != nil || before != 0 {
		t.Fatalf("批准前不得创建命令，count=%d err=%v", before, err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准重同步申请失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("worker 应执行一条已批准申请，processed=%d err=%v", processed, err)
	}

	var command model.AgentCommand
	if err := db.Where("namespace = ? AND server_id = ?", "prod", "lobby-1").First(&command).Error; err != nil {
		t.Fatalf("读取 worker 下发命令失败: %v", err)
	}
	if command.Type != model.CommandTypeResyncConfig || command.Status != model.CommandStatusPending {
		t.Fatalf("worker 应下发 pending resync-config，实际 %+v", command)
	}
	var grant model.SensitiveAccessGrant
	if err := db.Where("approval_request_id = ?", ticket.ApprovalRequestID).First(&grant).Error; err != nil {
		t.Fatalf("读取审批授权失败: %v", err)
	}
	if grant.Status != model.SensitiveAccessGrantStatusActive {
		t.Fatalf("批准后授权应激活，实际 %s", grant.Status)
	}
	var receipt model.ApprovalExecutionReceipt
	if err := db.Where("request_id = ?", ticket.ApprovalRequestID).First(&receipt).Error; err != nil {
		t.Fatalf("读取执行回执失败: %v", err)
	}
	if receipt.ResultRef != "agent-command-"+fmt.Sprint(command.ID) {
		t.Fatalf("回执命令引用不符: %s", receipt.ResultRef)
	}
}

// TestTailLogsApprovalGrantFlow 验证日志正文只在 Agent 回传后向原申请主体一次性开放。
func TestTailLogsApprovalGrantFlow(t *testing.T) {
	db := newCommandSvcTestDB(t)
	registry := authz.NewApprovalRegistry()
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	grants := NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	logs := NewAgentLogService(db, repository.NewAgentCommandRepository(db), repository.NewAuditLogRepository(db))
	logs.SetApprovalService(approval)
	logs.SetSensitiveAccessGrants(grants)
	RegisterAgentLogApprovalAdapter(registry, logs, grants)

	if _, err := logs.RequestTailLogs("prod", "lobby-1", "alice", "127.0.0.1"); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("公开日志下发必须拒绝旁路，实际 %v", err)
	}
	ticket, err := logs.RequestTailLogsApproval("prod", "lobby-1", "排查启动异常", "tail-approval-1", "alice", "127.0.0.1", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建日志审批失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准日志审批失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("worker 应执行日志命令，processed=%d err=%v", processed, err)
	}
	var cmd model.AgentCommand
	if err := db.Where("namespace = ? AND server_id = ?", "prod", "lobby-1").First(&cmd).Error; err != nil {
		t.Fatalf("读取日志命令失败: %v", err)
	}
	if ok, err := repository.NewAgentCommandRepository(db).UpdateStatus(cmd.ID, model.CommandStatusPending, model.CommandStatusFetched, ""); err != nil || !ok {
		t.Fatalf("模拟 agent 领取日志命令失败: ok=%v err=%v", ok, err)
	}
	if err := logs.ReceiveLogs(cmd.ID, []AgentLogLine{{Level: "INFO", Text: "已脱敏日志"}}, "127.0.0.1"); err != nil {
		t.Fatalf("回传日志失败: %v", err)
	}
	var grant model.SensitiveAccessGrant
	if err := db.Where("approval_request_id = ?", ticket.ApprovalRequestID).First(&grant).Error; err != nil {
		t.Fatalf("读取日志授权失败: %v", err)
	}
	if _, err := logs.ConsumeApprovedLogs(grant.GrantID, cmd.ID, auth.HumanPrincipal("mallory")); !errors.Is(err, apperr.ErrSensitiveAccessWrongPrincipal) {
		t.Fatalf("非原申请主体不得消费，实际 %v", err)
	}
	result, err := logs.ConsumeApprovedLogs(grant.GrantID, cmd.ID, auth.HumanPrincipal("alice"))
	if err != nil || len(result.Lines) != 1 || result.Lines[0].Text != "已脱敏日志" {
		t.Fatalf("原申请主体应一次消费日志，result=%+v err=%v", result, err)
	}
	if _, err := logs.ConsumeApprovedLogs(grant.GrantID, cmd.ID, auth.HumanPrincipal("alice")); !errors.Is(err, apperr.ErrSensitiveAccessConsumed) {
		t.Fatalf("日志授权必须一次性消费，实际 %v", err)
	}
}

// TestBrowseApprovalGrantFlow 验证文件浏览正文只能在审批 worker 入队、Agent 回传后由原申请主体一次性消费。
func TestBrowseApprovalGrantFlow(t *testing.T) {
	db := newCommandSvcTestDB(t)
	commands := newCommandSvc(db)
	registry := authz.NewApprovalRegistry()
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	grants := NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	commands.SetApprovalService(approval)
	commands.SetSensitiveAccessGrants(grants)
	RegisterAgentCommandApprovalAdapters(registry, commands, grants)

	if _, err := commands.RequestBrowse(context.Background(), BrowseParams{Namespace: "prod", ServerID: "lobby-1", Op: model.BrowseOpFile, Path: "Demo/config.yml", Operator: "alice"}); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("公开浏览下发必须拒绝旁路，实际 %v", err)
	}
	ticket, err := commands.RequestBrowseApproval(BrowseParams{Namespace: "prod", ServerID: "lobby-1", Op: model.BrowseOpFile, Path: "Demo/config.yml", Operator: "alice", ClientIP: "127.0.0.1"}, "核对线上文件", "browse-approval-1", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建浏览审批失败: %v", err)
	}
	var before int64
	if err := db.Model(&model.AgentCommand{}).Count(&before).Error; err != nil || before != 0 {
		t.Fatalf("批准前不得创建浏览命令，count=%d err=%v", before, err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准浏览审批失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("worker 应执行浏览命令，processed=%d err=%v", processed, err)
	}

	var cmd model.AgentCommand
	if err := db.Where("namespace = ? AND server_id = ?", "prod", "lobby-1").First(&cmd).Error; err != nil {
		t.Fatalf("读取浏览命令失败: %v", err)
	}
	if cmd.Type != model.CommandTypeFsBrowse || cmd.Status != model.CommandStatusPending {
		t.Fatalf("worker 应下发 pending fs-browse，实际 %+v", cmd)
	}
	if ok, err := repository.NewAgentCommandRepository(db).UpdateStatus(cmd.ID, model.CommandStatusPending, model.CommandStatusFetched, ""); err != nil || !ok {
		t.Fatalf("模拟 agent 领取浏览命令失败: ok=%v err=%v", ok, err)
	}
	const content = `{"path":"Demo/config.yml","content":"enabled: true\\n"}`
	if err := commands.ReceiveBrowseResult(agentauth.Identity{Namespace: "prod", ServerID: "lobby-1", IdentityID: "identity"}, cmd.ID, true, content, ""); err != nil {
		t.Fatalf("回传浏览结果失败: %v", err)
	}
	var grant model.SensitiveAccessGrant
	if err := db.Where("approval_request_id = ?", ticket.ApprovalRequestID).First(&grant).Error; err != nil {
		t.Fatalf("读取浏览授权失败: %v", err)
	}
	if grant.Status != model.SensitiveAccessGrantStatusActive {
		t.Fatalf("Agent 回传后授权应激活，实际 %s", grant.Status)
	}
	var audit model.AuditLog
	if err := db.Where("action = ?", model.ActionFileBrowse).First(&audit).Error; err != nil || strings.Contains(audit.Detail, "enabled: true") {
		t.Fatalf("浏览审计不得含回传正文，detail=%q err=%v", audit.Detail, err)
	}
	if _, err := commands.ConsumeApprovedBrowse(grant.GrantID, cmd.ID, auth.HumanPrincipal("mallory")); !errors.Is(err, apperr.ErrSensitiveAccessWrongPrincipal) {
		t.Fatalf("非原申请主体不得消费，实际 %v", err)
	}
	result, err := commands.ConsumeApprovedBrowse(grant.GrantID, cmd.ID, auth.HumanPrincipal("alice"))
	if err != nil || result != content {
		t.Fatalf("原申请主体应一次消费浏览结果，result=%q err=%v", result, err)
	}
	if _, err := commands.ConsumeApprovedBrowse(grant.GrantID, cmd.ID, auth.HumanPrincipal("alice")); !errors.Is(err, apperr.ErrSensitiveAccessConsumed) {
		t.Fatalf("浏览授权必须一次性消费，实际 %v", err)
	}
}

// TestImprintApprovalActivatesGrantAfterAgentResult 验证拓印正文不在批准前开放。
func TestImprintApprovalActivatesGrantAfterAgentResult(t *testing.T) {
	db := newCommandSvcTestDB(t)
	commands := newCommandSvc(db)
	registry := authz.NewApprovalRegistry()
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	grants := NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	commands.SetApprovalService(approval)
	commands.SetSensitiveAccessGrants(grants)
	RegisterAgentCommandApprovalAdapters(registry, commands, grants)

	if _, err := commands.RequestImprint("prod", "lobby-1", "Demo/config.yml", "alice", ""); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("公开拓印下发必须拒绝旁路，实际 %v", err)
	}
	ticket, err := commands.RequestImprintApproval("prod", "lobby-1", "Demo/config.yml", "核对线上配置", "imprint-approval-1", "alice", "127.0.0.1", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建拓印审批失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), ""); err != nil {
		t.Fatalf("批准拓印申请失败: %v", err)
	}
	if _, err := NewApprovalWorker(approval).RunOnce(); err != nil {
		t.Fatalf("执行拓印审批失败: %v", err)
	}
	var cmd model.AgentCommand
	if err := db.Where("namespace = ? AND server_id = ?", "prod", "lobby-1").First(&cmd).Error; err != nil {
		t.Fatalf("读取拓印命令失败: %v", err)
	}
	if ok, err := repository.NewAgentCommandRepository(db).UpdateStatus(cmd.ID, model.CommandStatusPending, model.CommandStatusFetched, ""); err != nil || !ok {
		t.Fatalf("模拟 agent 领取拓印命令失败: ok=%v err=%v", ok, err)
	}
	if _, err := commands.ReceiveIngest(cmd.ID, []ImportFile{{Path: "Demo/config.yml", Content: "safe: true"}}, ""); err != nil {
		t.Fatalf("接收拓印内容失败: %v", err)
	}
	var grant model.SensitiveAccessGrant
	if err := db.Where("approval_request_id = ?", ticket.ApprovalRequestID).First(&grant).Error; err != nil {
		t.Fatalf("读取拓印授权失败: %v", err)
	}
	if grant.Status != model.SensitiveAccessGrantStatusActive || grant.TargetRef != fmt.Sprintf("agent-command/%d", cmd.ID) {
		t.Fatalf("拓印回传后授权应绑定命令并激活，实际 %+v", grant)
	}
}

// TestImprintConfirmApprovalUsesWorkerReceipt 验证确认拓印不能直写，且文件、命令终态与回执同事务提交。
func TestImprintConfirmApprovalUsesWorkerReceipt(t *testing.T) {
	db := newCommandSvcTestDB(t)
	commands := newImprintSvc(db)
	registry := authz.NewApprovalRegistry()
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	commands.SetApprovalService(approval)
	RegisterAgentCommandApprovalAdapters(registry, commands, NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db)))

	cmd, err := applyRequestImprintForTest(commands, "prod", "lobby-1", "Demo/config.yml", "alice", "")
	if err != nil {
		t.Fatalf("构造拓印命令失败: %v", err)
	}
	if _, err := commands.FetchPending("prod", "lobby-1"); err != nil {
		t.Fatalf("领取拓印命令失败: %v", err)
	}
	if _, err := commands.ReceiveIngest(cmd.ID, []ImportFile{{Path: "Demo/config.yml", Content: "safe: true\n"}}, ""); err != nil {
		t.Fatalf("回传拓印内容失败: %v", err)
	}
	md5 := filetree.ContentMD5("safe: true\n")
	if _, err := commands.ConfirmImprint(cmd.ID, model.ScopeServer, "area1", "", "lobby-1", md5, "alice", ""); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("公开确认入口必须拒绝旁路，实际 %v", err)
	}

	ticket, err := commands.RequestImprintConfirmApproval(cmd.ID, model.ScopeServer, "area1", "", "lobby-1", md5,
		"确认并入线上覆盖", "imprint-confirm-approval-1", "alice", "127.0.0.1", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建确认审批失败: %v", err)
	}
	var request model.ApprovalRequest
	if err := db.Where("request_id = ?", ticket.ApprovalRequestID).First(&request).Error; err != nil {
		t.Fatalf("读取确认审批失败: %v", err)
	}
	if request.OperationKind != authz.OperationAgentCommandImprintConfirm || strings.Contains(request.Payload, "safe: true") {
		t.Fatalf("确认审批应冻结专用 operation 且不得含正文，request=%+v", request)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准确认审批失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("worker 应执行确认审批，processed=%d err=%v", processed, err)
	}
	obj, err := repository.NewFileObjectRepository(db).FindByIdentity("prod", "area1", "Demo/config.yml", model.ScopeServer, "lobby-1")
	if err != nil || obj == nil || obj.Content != "safe: true\n" {
		t.Fatalf("worker 应落库拓印内容，obj=%+v err=%v", obj, err)
	}
	stored, err := repository.NewAgentCommandRepository(db).FindByID(cmd.ID)
	if err != nil || stored.Status != model.CommandStatusDone || stored.ImprintContent != "" {
		t.Fatalf("成功后命令应 done 且清空瞬态，command=%+v err=%v", stored, err)
	}
	var receipt model.ApprovalExecutionReceipt
	if err := db.Where("request_id = ?", ticket.ApprovalRequestID).First(&receipt).Error; err != nil || receipt.ResultRef != fmt.Sprintf("file-%d", obj.ID) {
		t.Fatalf("确认审批必须写同事务回执，receipt=%+v err=%v", receipt, err)
	}
}

// TestImprintConfirmReceiptFailureRollsBack 验证回执失败时不得留下文件写入或命令终态。
func TestImprintConfirmReceiptFailureRollsBack(t *testing.T) {
	db := newCommandSvcTestDB(t)
	commands := newImprintSvc(db)
	registry := authz.NewApprovalRegistry()
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	commands.SetApprovalService(approval)
	RegisterAgentCommandApprovalAdapters(registry, commands, NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db)))

	cmd, err := applyRequestImprintForTest(commands, "prod", "lobby-1", "Demo/config.yml", "alice", "")
	if err != nil {
		t.Fatalf("构造拓印命令失败: %v", err)
	}
	if _, err := commands.FetchPending("prod", "lobby-1"); err != nil {
		t.Fatalf("领取拓印命令失败: %v", err)
	}
	content := "safe: rollback\n"
	if _, err := commands.ReceiveIngest(cmd.ID, []ImportFile{{Path: "Demo/config.yml", Content: content}}, ""); err != nil {
		t.Fatalf("回传拓印内容失败: %v", err)
	}
	ticket, err := commands.RequestImprintConfirmApproval(cmd.ID, model.ScopeServer, "area1", "", "lobby-1", filetree.ContentMD5(content),
		"验证回执回滚", "imprint-confirm-receipt-rollback", "alice", "", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建确认审批失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), ""); err != nil {
		t.Fatalf("批准确认审批失败: %v", err)
	}
	if err := db.Exec("CREATE TRIGGER reject_imprint_receipt BEFORE INSERT ON approval_execution_receipt BEGIN SELECT RAISE(ABORT, '拒绝回执'); END").Error; err != nil {
		t.Fatalf("安装回执失败触发器失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("回执失败后的 worker 应收敛申请，processed=%d err=%v", processed, err)
	}
	obj, err := repository.NewFileObjectRepository(db).FindByIdentity("prod", "area1", "Demo/config.yml", model.ScopeServer, "lobby-1")
	if err != nil || obj != nil {
		t.Fatalf("回执失败必须回滚文件写入，obj=%+v err=%v", obj, err)
	}
	stored, err := repository.NewAgentCommandRepository(db).FindByID(cmd.ID)
	if err != nil || stored.Status != model.CommandStatusReady || stored.ImprintContent != content {
		t.Fatalf("回执失败必须回滚命令终态与瞬态，command=%+v err=%v", stored, err)
	}
	var receipts int64
	if err := db.Model(&model.ApprovalExecutionReceipt{}).Where("request_id = ?", ticket.ApprovalRequestID).Count(&receipts).Error; err != nil || receipts != 0 {
		t.Fatalf("回执失败不得留下收据，count=%d err=%v", receipts, err)
	}
}

// TestImprintConfirmRejectsTamperedExecutionBinding 验证执行租约或审批绑定遭篡改时，确认拓印不会开始任何领域写入。
func TestImprintConfirmRejectsTamperedExecutionBinding(t *testing.T) {
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
			db, _, cmd, worker, claimed := prepareImprintConfirmExecution(t)
			tc.mutate(db, claimed)
			_ = worker.execute(claimed)
			obj, err := repository.NewFileObjectRepository(db).FindByIdentity("prod", "area1", "Demo/config.yml", model.ScopeServer, "lobby-1")
			if err != nil || obj != nil {
				t.Fatalf("拒绝后不得写入拓印文件，obj=%+v err=%v", obj, err)
			}
			stored, err := repository.NewAgentCommandRepository(db).FindByID(cmd.ID)
			if err != nil || stored.Status != model.CommandStatusReady || stored.ImprintContent == "" {
				t.Fatalf("拒绝后命令必须保持 ready 与瞬态，command=%+v err=%v", stored, err)
			}
			var receipts int64
			if err := db.Model(&model.ApprovalExecutionReceipt{}).Count(&receipts).Error; err != nil || receipts != 0 {
				t.Fatalf("拒绝后不得写入执行回执，count=%d err=%v", receipts, err)
			}
			var succeeded int64
			if err := db.Model(&model.ApprovalRequest{}).Where("status = ?", model.ApprovalStatusSucceeded).Count(&succeeded).Error; err != nil || succeeded != 0 {
				t.Fatalf("错误审批绑定不得标记成功，count=%d err=%v", succeeded, err)
			}
		})
	}
}

// TestConfirmImprintApplyRequiresPermit 验证私有领域入口在任何副作用前校验审批许可。
func TestConfirmImprintApplyRequiresPermit(t *testing.T) {
	db, commands, cmd, _, _ := prepareImprintConfirmExecution(t)
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := commands.applyConfirmImprintInTx(tx, authz.ApprovalRequest{}, authz.Permit{}, imprintConfirmApprovalPayload{
			CommandID: cmd.ID, Namespace: "prod", ServerID: "lobby-1", Path: "Demo/config.yml",
			Scope: model.ScopeServer, Group: "area1", Target: "lobby-1", ReviewedMD5: filetree.ContentMD5("safe: true\n"), Operator: "alice",
		})
		return err
	}); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("缺失审批许可必须拒绝，实际 %v", err)
	}
	obj, err := repository.NewFileObjectRepository(db).FindByIdentity("prod", "area1", "Demo/config.yml", model.ScopeServer, "lobby-1")
	if err != nil || obj != nil {
		t.Fatalf("缺失许可不得写入拓印文件，obj=%+v err=%v", obj, err)
	}
}

func prepareImprintConfirmExecution(t *testing.T) (*gorm.DB, *AgentCommandService, *model.AgentCommand, *ApprovalWorker, *model.ApprovalRequest) {
	t.Helper()
	db := newCommandSvcTestDB(t)
	commands := newImprintSvc(db)
	registry := authz.NewApprovalRegistry()
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	commands.SetApprovalService(approval)
	RegisterAgentCommandApprovalAdapters(registry, commands, NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db)))
	cmd, err := applyRequestImprintForTest(commands, "prod", "lobby-1", "Demo/config.yml", "alice", "")
	if err != nil {
		t.Fatalf("构造拓印命令失败: %v", err)
	}
	if _, err := commands.FetchPending("prod", "lobby-1"); err != nil {
		t.Fatalf("领取拓印命令失败: %v", err)
	}
	if _, err := commands.ReceiveIngest(cmd.ID, []ImportFile{{Path: "Demo/config.yml", Content: "safe: true\n"}}, ""); err != nil {
		t.Fatalf("回传拓印内容失败: %v", err)
	}
	ticket, err := commands.RequestImprintConfirmApproval(cmd.ID, model.ScopeServer, "area1", "", "lobby-1", filetree.ContentMD5("safe: true\n"),
		"验证执行绑定", "imprint-binding", "alice", "", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建确认审批失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), ""); err != nil {
		t.Fatalf("批准确认审批失败: %v", err)
	}
	worker := NewApprovalWorker(approval)
	claimed, found, err := worker.claimNext()
	if err != nil || !found {
		t.Fatalf("认领确认审批失败: found=%v err=%v", found, err)
	}
	return db, commands, cmd, worker, claimed
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
	cmd, err := applyRequestReverseFetchForTest(svc, "prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "10.0.0.1")
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
	if _, err := applyRequestReverseFetchForTest(svc, "prod", "", model.ScopeGroup, "area1", "", "alice", ""); err == nil {
		t.Fatal("缺 serverId 应拒")
	}
	// server 层缺目标 serverId 应拒
	if _, err := applyRequestReverseFetchForTest(svc, "prod", "lobby-1", model.ScopeServer, "area1", "", "alice", ""); err == nil {
		t.Fatal("server 层缺 target 应拒")
	}
}

// TestFetchPending 取最早 pending 并 CAS fetched；取空返回 (nil,nil)。
func TestFetchPending(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	_, _ = applyRequestReverseFetchForTest(svc, "prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "")

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

// TestFetchPendingRejectsArchivedTarget 归档后旧 pending 命令不可再领取。
func TestFetchPendingRejectsArchivedTarget(t *testing.T) {
	db := newCommandSvcTestDB(t)
	if err := db.AutoMigrate(&model.Namespace{}, &model.Server{}); err != nil {
		t.Fatalf("迁移生命周期表失败: %v", err)
	}
	ns := model.Namespace{Code: "prod", Name: "生产"}
	if err := db.Create(&ns).Error; err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	server := model.Server{NamespaceID: ns.ID, ServerID: "archived", Kind: model.ServerKindBackend}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("创建 server 失败: %v", err)
	}
	svc := newCommandSvc(db)
	cmd, err := applyRequestReverseFetchForTest(svc, "prod", "archived", model.ScopeGroup, "area1", "", "alice", "")
	if err != nil {
		t.Fatalf("active server 应允许创建命令: %v", err)
	}
	if err := db.Model(&model.Server{}).Where("id = ?", server.ID).Update("lifecycle", model.ServerLifecycleArchived).Error; err != nil {
		t.Fatalf("归档 server 失败: %v", err)
	}
	got, err := svc.FetchPending("prod", "archived")
	if err != apperr.ErrServerArchived || got != nil {
		t.Fatalf("归档 server 不应领取命令，实际 command=%v err=%v", got, err)
	}
	stored, err := repository.NewAgentCommandRepository(db).FindByID(cmd.ID)
	if err != nil || stored.Status != model.CommandStatusPending {
		t.Fatalf("领取失败不得改变命令状态，实际 command=%+v err=%v", stored, err)
	}
}

// TestReceiveResyncResultRejectsArchivedTarget 归档后迟到回传不得推进命令。
func TestReceiveResyncResultRejectsArchivedTarget(t *testing.T) {
	db := newCommandSvcTestDB(t)
	if err := db.AutoMigrate(&model.Namespace{}, &model.Server{}); err != nil {
		t.Fatalf("迁移生命周期表失败: %v", err)
	}
	ns := model.Namespace{Code: "prod", Name: "生产"}
	if err := db.Create(&ns).Error; err != nil {
		t.Fatalf("创建 namespace 失败: %v", err)
	}
	server := model.Server{NamespaceID: ns.ID, ServerID: "archived", Kind: model.ServerKindBackend}
	if err := db.Create(&server).Error; err != nil {
		t.Fatalf("创建 server 失败: %v", err)
	}
	svc := newCommandSvc(db)
	cmd, err := svc.applyRequestResyncForTest("prod", "archived", "alice", "")
	if err != nil {
		t.Fatalf("创建重同步命令失败: %v", err)
	}
	if _, err := svc.FetchPending("prod", "archived"); err != nil {
		t.Fatalf("领取命令失败: %v", err)
	}
	if err := db.Model(&model.Server{}).Where("id = ?", server.ID).Update("lifecycle", model.ServerLifecycleArchived).Error; err != nil {
		t.Fatalf("归档 server 失败: %v", err)
	}
	if err := svc.ReceiveResyncResult(cmd.ID, true, ""); err != apperr.ErrCommandNotFound {
		t.Fatalf("归档后的迟到回传应按命令失效处理，实际 %v", err)
	}
	stored, err := repository.NewAgentCommandRepository(db).FindByID(cmd.ID)
	if err != nil || stored.Status != model.CommandStatusFetched {
		t.Fatalf("迟到回传不得推进命令，实际 command=%+v err=%v", stored, err)
	}
}

// TestReceiveIngestHappy 回传合法文件 → 落组覆盖、命令 done、记 file.import 审计。
func TestReceiveIngestHappy(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	_, _ = applyRequestReverseFetchForTest(svc, "prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "")
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
	_, _ = applyRequestReverseFetchForTest(svc, "prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "")
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
	_, _ = applyRequestReverseFetchForTest(svc, "prod", "lobby-1", model.ScopeServer, "area1", "lobby-1", "alice", "")
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

// TestReceiveIngestAllowsAgentSelfDir 方案 D（FR-39 归真）回归：反向抓取回传含 agent 自身目录
// （BeaconAgent/*）的文本配置时，控制面不再整批拒绝，正常 ingest 落库为组级覆盖。
// 此前 normalizePath 命中 reservedAgentSelfDirs → ErrInvalidPath → 整次 ingest 400、命令 failed，
// 对任何装了 agent 的在线服反向抓取 100% 失效（agent 读盘必带自身 plugins/BeaconAgent/）。
func TestReceiveIngestAllowsAgentSelfDir(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	cmdRepo := repository.NewAgentCommandRepository(db)

	_, _ = applyRequestReverseFetchForTest(svc, "prod", "lobby-1", model.ScopeGroup, "area1", "", "alice", "")
	cmd, _ := svc.FetchPending("prod", "lobby-1")
	if _, err := svc.ReceiveIngest(cmd.ID, []ImportFile{
		{Path: "BeaconAgent/config.yml", Content: "endpoints: x\n"},
		{Path: "LuckPerms/config.yml", Content: "a: 1\n"},
	}, ""); err != nil {
		t.Fatalf("含 agent 自身目录的 ingest 应成功（方案 D 放开），实际 %v", err)
	}

	fileRepo := repository.NewFileObjectRepository(db)
	self, _ := fileRepo.FindByIdentity("prod", "area1", "BeaconAgent/config.yml", model.ScopeGroup, "")
	if self == nil || self.Content != "endpoints: x\n" {
		t.Fatalf("自身目录文件应落组级覆盖，实际 %+v", self)
	}
	other, _ := fileRepo.FindByIdentity("prod", "area1", "LuckPerms/config.yml", model.ScopeGroup, "")
	if other == nil {
		t.Fatal("同批合法文件也应落库")
	}

	got, _ := cmdRepo.FindByID(cmd.ID)
	if got.Status != model.CommandStatusDone {
		t.Fatalf("ingest 成功命令应转 done，实际 %s", got.Status)
	}
}

// TestRequestResync 触发即建 pending resync-config 命令并记一条 instance.resync 审计（target=实例）。
func TestRequestResync(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	cmd, err := svc.applyRequestResyncForTest("prod", "lobby-1", "alice", "10.0.0.1")
	if err != nil {
		t.Fatalf("触发重同步失败: %v", err)
	}
	if cmd.ID == 0 || cmd.Status != model.CommandStatusPending || cmd.Type != model.CommandTypeResyncConfig {
		t.Fatalf("命令应为 pending/resync-config，实际 %+v", cmd)
	}
	if cmd.Payload != "{}" {
		t.Fatalf("重同步命令无业务载荷，应为空 JSON，实际 %q", cmd.Payload)
	}
	if countAudit(t, db, model.ActionInstanceResync) != 1 {
		t.Fatal("应记一条 instance.resync 审计")
	}
	// 缺参一律拒
	if _, err := svc.applyRequestResyncForTest("prod", "", "alice", ""); err == nil {
		t.Fatal("缺 serverId 应拒")
	}
	if _, err := svc.applyRequestResyncForTest("prod", "lobby-1", "", ""); err == nil {
		t.Fatal("缺 operator 应拒")
	}
}

// TestReceiveResyncResultHappy 拉取后回传 ok=true → 命令 done。
func TestReceiveResyncResultHappy(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	cmdRepo := repository.NewAgentCommandRepository(db)
	_, _ = svc.applyRequestResyncForTest("prod", "lobby-1", "alice", "")
	cmd, _ := svc.FetchPending("prod", "lobby-1")

	if err := svc.ReceiveResyncResult(cmd.ID, true, ""); err != nil {
		t.Fatalf("回传成功应推进命令: %v", err)
	}
	got, _ := cmdRepo.FindByID(cmd.ID)
	if got.Status != model.CommandStatusDone {
		t.Fatalf("ok=true 命令应转 done，实际 %s", got.Status)
	}
}

// TestReceiveResyncResultFailedAndGuards ok=false 转 failed；存在性/状态/类型守卫。
func TestReceiveResyncResultFailedAndGuards(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	cmdRepo := repository.NewAgentCommandRepository(db)

	// 不存在的命令 → COMMAND_NOT_FOUND
	if err := svc.ReceiveResyncResult(99999, true, ""); err != apperr.ErrCommandNotFound {
		t.Fatalf("不存在命令应 ErrCommandNotFound，实际 %v", err)
	}
	// pending（未拉取）状态回传 → 不可回传
	_, _ = svc.applyRequestResyncForTest("prod", "lobby-1", "alice", "")
	pend, _ := cmdRepo.FindOldestPending("prod", "lobby-1")
	if err := svc.ReceiveResyncResult(pend.ID, true, ""); err != apperr.ErrCommandNotFound {
		t.Fatalf("pending 状态回传应被拒，实际 %v", err)
	}
	// 非 resync-config 类型命令（ingest-plugins）回传 → 类型不符拒
	_, _ = applyRequestReverseFetchForTest(svc, "prod", "other-1", model.ScopeGroup, "area1", "", "alice", "")
	ingestCmd, _ := svc.FetchPending("prod", "other-1")
	if err := svc.ReceiveResyncResult(ingestCmd.ID, true, ""); err != apperr.ErrCommandNotFound {
		t.Fatalf("非 resync-config 类型回传应被拒，实际 %v", err)
	}
	// 拉取后回传 ok=false → 命令转 failed
	cmd, _ := svc.FetchPending("prod", "lobby-1")
	if err := svc.ReceiveResyncResult(cmd.ID, false, "重拉失败"); err != nil {
		t.Fatalf("失败回传不应返回错误（仅推进命令）: %v", err)
	}
	got, _ := cmdRepo.FindByID(cmd.ID)
	if got.Status != model.CommandStatusFailed {
		t.Fatalf("ok=false 命令应转 failed，实际 %s", got.Status)
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
	_, _ = applyRequestReverseFetchForTest(svc, "prod", "a", model.ScopeGroup, "g", "", "alice", "")
	if err := db.Model(&model.AgentCommand{}).Where("1 = 1").Update("created_at", time.Now().Add(-2*time.Hour)).Error; err != nil {
		t.Fatalf("改 created_at 失败: %v", err)
	}
	n, err := svc.ExpireStale(time.Now().Add(-1 * time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("应过期 1 条，实际 %d / %v", n, err)
	}
}
