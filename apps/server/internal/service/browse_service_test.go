package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// TestRequestBrowseRejectsPublicBypass 验证旧公开 service 不能直接下发 fs-browse 命令。
func TestRequestBrowseRejectsPublicBypass(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	if _, err := svc.RequestBrowse(context.Background(), BrowseParams{Namespace: "prod", ServerID: "lobby-1", Op: model.BrowseOpList, Operator: "alice"}); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("公开浏览下发应拒绝，实际 %v", err)
	}
	var count int64
	if err := db.Model(&model.AgentCommand{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("拒绝旁路后不得创建命令，count=%d err=%v", count, err)
	}
}

// TestRequestBrowseApprovalGuards 验证申请参数不完整或非法 op 时不创建审批和命令。
func TestRequestBrowseApprovalGuards(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), authz.NewApprovalRegistry())
	svc.SetApprovalService(approval)
	for _, params := range []BrowseParams{
		{Namespace: "prod", Op: model.BrowseOpList, Operator: "alice"},
		{Namespace: "prod", ServerID: "lobby-1", Op: "shell", Operator: "alice"},
	} {
		if _, err := svc.RequestBrowseApproval(params, "核对文件", "browse-invalid", auth.HumanPrincipal("alice")); !errors.Is(err, apperr.ErrInvalidParam) {
			t.Fatalf("非法浏览申请应拒绝，params=%+v err=%v", params, err)
		}
	}
	var requests int64
	if err := db.Model(&model.ApprovalRequest{}).Count(&requests).Error; err != nil || requests != 0 {
		t.Fatalf("非法参数不得创建审批，count=%d err=%v", requests, err)
	}
}

// TestReceiveBrowseResultFailureRevokesGrant 验证 Agent 拒绝读取时待激活授权随命令失败原子撤销。
func TestReceiveBrowseResultFailureRevokesGrant(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	registry := authz.NewApprovalRegistry()
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	grants := NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	svc.SetApprovalService(approval)
	svc.SetSensitiveAccessGrants(grants)
	RegisterAgentCommandApprovalAdapters(registry, svc, grants)
	ticket, err := svc.RequestBrowseApproval(BrowseParams{Namespace: "prod", ServerID: "lobby-1", Op: model.BrowseOpFile, Path: "Demo/config.yml", Operator: "alice"}, "核对文件", "browse-failed", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建浏览审批失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准浏览申请失败: %v", err)
	}
	if _, err := NewApprovalWorker(approval).RunOnce(); err != nil {
		t.Fatalf("worker 下发浏览命令失败: %v", err)
	}
	cmd, err := repository.NewAgentCommandRepository(db).FindOldestPending("prod", "lobby-1")
	if err != nil || cmd == nil {
		t.Fatalf("读取待执行浏览命令失败: cmd=%+v err=%v", cmd, err)
	}
	if ok, err := repository.NewAgentCommandRepository(db).UpdateStatus(cmd.ID, model.CommandStatusPending, model.CommandStatusFetched, ""); err != nil || !ok {
		t.Fatalf("模拟 agent 领取浏览命令失败: ok=%v err=%v", ok, err)
	}
	if err := svc.ReceiveBrowseResult(agentauth.Identity{Namespace: "prod", ServerID: "lobby-1", IdentityID: "identity"}, cmd.ID, false, "", "目标路径不可读取"); err != nil {
		t.Fatalf("回传浏览失败结果失败: %v", err)
	}
	var grant model.SensitiveAccessGrant
	if err := db.Where("approval_request_id = ?", ticket.ApprovalRequestID).First(&grant).Error; err != nil {
		t.Fatalf("读取浏览授权失败: %v", err)
	}
	if grant.Status != model.SensitiveAccessGrantStatusRevoked {
		t.Fatalf("失败命令的授权应撤销，实际 %s", grant.Status)
	}
	stored, _ := repository.NewAgentCommandRepository(db).FindByID(cmd.ID)
	if stored.ResultDetail != `{"error":"agent_browse_failed"}` || strings.Contains(stored.ResultDetail, "目标路径不可读取") {
		t.Fatalf("失败原因必须使用安全枚举摘要，实际 %q", stored.ResultDetail)
	}
}

// TestReceiveBrowseResultGuards 回传守卫：不存在或非 fs-browse 命令一律拒绝。
func TestReceiveBrowseResultGuards(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	svc.SetSensitiveAccessGrants(NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db)))
	if err := svc.ReceiveBrowseResult(agentauth.Identity{Namespace: "prod", ServerID: "s", IdentityID: "identity"}, 99999, true, "{}", ""); !errors.Is(err, apperr.ErrCommandNotFound) {
		t.Fatalf("不存在命令应 ErrCommandNotFound，实际 %v", err)
	}
	_, _ = applyRequestReverseFetchForTest(svc, "prod", "lobby-1", model.ScopeGroup, "g", "", "alice", "")
	ingest, _ := svc.FetchPending("prod", "lobby-1")
	if err := svc.ReceiveBrowseResult(agentauth.Identity{Namespace: "prod", ServerID: "lobby-1", IdentityID: "identity"}, ingest.ID, true, "{}", ""); !errors.Is(err, apperr.ErrCommandNotFound) {
		t.Fatalf("非 fs-browse 类型回传应被拒，实际 %v", err)
	}
}

func seedActiveBrowseResult(t *testing.T, db *gorm.DB, grants *SensitiveAccessGrantService) (*model.AgentCommand, *model.SensitiveAccessGrant, string) {
	t.Helper()
	const result = `{"path":"Demo/config.yml","content":"enabled: true\\n"}`
	cmd := &model.AgentCommand{NamespaceCode: "prod", ServerID: "lobby-1", Type: model.CommandTypeFsBrowse, Status: model.CommandStatusDone, BrowseResult: result, Operator: "alice"}
	if err := db.Create(cmd).Error; err != nil {
		t.Fatalf("创建已完成浏览命令失败: %v", err)
	}
	grant, err := grants.CreatePending("apr_browse_consume", "human", "alice", authz.OperationAgentCommandFSBrowse, "agent-command/"+strconv.FormatUint(uint64(cmd.ID), 10), "pending")
	if err != nil {
		t.Fatalf("创建待激活授权失败: %v", err)
	}
	if err := grants.BindAndActivatePendingCommandFromAgent(cmd.ID, authz.OperationAgentCommandFSBrowse, fmt.Sprintf("%x", sha256.Sum256([]byte(result))), time.Now().UTC()); err != nil {
		t.Fatalf("激活浏览授权失败: %v", err)
	}
	return cmd, grant, result
}

// TestConsumeApprovedBrowseAtomicallyClearsResult 确保返回正文、消费 grant 与清空瞬态结果在同一事务提交。
func TestConsumeApprovedBrowseAtomicallyClearsResult(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	grants := NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	svc.SetSensitiveAccessGrants(grants)
	cmd, grant, want := seedActiveBrowseResult(t, db, grants)

	got, err := svc.ConsumeApprovedBrowse(grant.GrantID, cmd.ID, auth.HumanPrincipal("alice"))
	if err != nil || got != want {
		t.Fatalf("首次消费应返回原结果: got=%q err=%v", got, err)
	}
	stored, _ := repository.NewAgentCommandRepository(db).FindByID(cmd.ID)
	if stored.BrowseResult != "" {
		t.Fatalf("消费成功后必须清空瞬态浏览结果，实际 %q", stored.BrowseResult)
	}
	var consumed model.SensitiveAccessGrant
	if err := db.Where("grant_id = ?", grant.GrantID).First(&consumed).Error; err != nil || consumed.Status != model.SensitiveAccessGrantStatusConsumed {
		t.Fatalf("消费成功后授权应为 consumed: %+v err=%v", consumed, err)
	}
}

// TestConsumeApprovedBrowseConcurrent 确保并发消费者至多一人取得正文，另一人只能收到已消费错误。
func TestConsumeApprovedBrowseConcurrent(t *testing.T) {
	db := newCommandSvcTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取得 sqlite 连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	svc := newCommandSvc(db)
	grants := NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	svc.SetSensitiveAccessGrants(grants)
	cmd, grant, want := seedActiveBrowseResult(t, db, grants)
	start := make(chan struct{})
	results := make(chan struct {
		result string
		err    error
	}, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, consumeErr := svc.ConsumeApprovedBrowse(grant.GrantID, cmd.ID, auth.HumanPrincipal("alice"))
			results <- struct {
				result string
				err    error
			}{result, consumeErr}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	successes := 0
	for item := range results {
		if item.err == nil {
			if item.result != want {
				t.Fatalf("成功消费者必须获得完整结果，实际 %q", item.result)
			}
			successes++
			continue
		}
		if !errors.Is(item.err, apperr.ErrSensitiveAccessConsumed) {
			t.Fatalf("并发第二消费者应收到已消费错误，实际 %v", item.err)
		}
	}
	if successes != 1 {
		t.Fatalf("并发消费必须恰有一人成功，实际 %d", successes)
	}
}

// TestConsumeApprovedBrowseRollsBackOnClearFailure 清空瞬态结果失败时必须回滚 grant 消费，不能丢结果或吞掉授权。
func TestConsumeApprovedBrowseRollsBackOnClearFailure(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	grants := NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	svc.SetSensitiveAccessGrants(grants)
	cmd, grant, want := seedActiveBrowseResult(t, db, grants)
	clearErr := errors.New("模拟清空浏览结果失败")
	if err := db.Callback().Update().Before("gorm:update").Register(t.Name(), func(tx *gorm.DB) {
		if _, ok := tx.Statement.Model.(*model.AgentCommand); ok {
			if err := tx.AddError(clearErr); !errors.Is(err, clearErr) {
				t.Errorf("注入清空错误失败: %v", err)
			}
		}
	}); err != nil {
		t.Fatalf("注册清空错误回调失败: %v", err)
	}

	if _, err := svc.ConsumeApprovedBrowse(grant.GrantID, cmd.ID, auth.HumanPrincipal("alice")); !errors.Is(err, clearErr) {
		t.Fatalf("清空失败应回传原错误，实际 %v", err)
	}
	stored, _ := repository.NewAgentCommandRepository(db).FindByID(cmd.ID)
	if stored.BrowseResult != want {
		t.Fatalf("事务回滚后结果必须保留，实际 %q", stored.BrowseResult)
	}
	var active model.SensitiveAccessGrant
	if err := db.Where("grant_id = ?", grant.GrantID).First(&active).Error; err != nil || active.Status != model.SensitiveAccessGrantStatusActive {
		t.Fatalf("事务回滚后授权必须保持 active: %+v err=%v", active, err)
	}
}

// TestClearExpiredBrowseResults 清理已到期且未消费授权对应的 done 浏览正文。
func TestClearExpiredBrowseResults(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	grants := NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	svc.SetSensitiveAccessGrants(grants)
	cmd, grant, _ := seedActiveBrowseResult(t, db, grants)
	if err := db.Model(&model.SensitiveAccessGrant{}).Where("grant_id = ?", grant.GrantID).Update("expires_at", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatalf("设置授权过期失败: %v", err)
	}
	if n, err := svc.ClearExpiredBrowseResults(time.Now().UTC()); err != nil || n != 1 {
		t.Fatalf("应清理一条到期浏览结果: n=%d err=%v", n, err)
	}
	stored, _ := repository.NewAgentCommandRepository(db).FindByID(cmd.ID)
	if stored.BrowseResult != "" {
		t.Fatalf("到期未消费正文必须清空，实际 %q", stored.BrowseResult)
	}
}

// TestReceiveBrowseResultRejectsMismatchedIdentity Agent 回传必须与命令的权威 namespace/server 绑定一致。
func TestReceiveBrowseResultRejectsMismatchedIdentity(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	svc.SetSensitiveAccessGrants(NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db)))
	cmd := &model.AgentCommand{NamespaceCode: "prod", ServerID: "lobby-1", Type: model.CommandTypeFsBrowse, Status: model.CommandStatusFetched, Operator: "alice"}
	if err := db.Create(cmd).Error; err != nil {
		t.Fatalf("创建浏览命令失败: %v", err)
	}
	identity := agentauth.Identity{Namespace: "prod", ServerID: "other", IdentityID: "identity"}
	if err := svc.ReceiveBrowseResult(identity, cmd.ID, true, `{}`, ""); !errors.Is(err, apperr.ErrCommandNotFound) {
		t.Fatalf("错身份回传必须拒绝，实际 %v", err)
	}
}

// allCommands 取全部命令行，供同包敏感命令测试断言生命周期。
func allCommands(t *testing.T, db *gorm.DB) []model.AgentCommand {
	t.Helper()
	var cmds []model.AgentCommand
	if err := db.Order("id asc").Find(&cmds).Error; err != nil {
		t.Fatalf("查命令失败: %v", err)
	}
	return cmds
}

// auditDetailContains 判断审计 detail 是否包含敏感正文片段。
func auditDetailContains(t *testing.T, db *gorm.DB, action, sub string) bool {
	t.Helper()
	var logs []model.AuditLog
	if err := db.Where("action = ?", action).Find(&logs).Error; err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	for _, log := range logs {
		if strings.Contains(log.Detail, sub) {
			return true
		}
	}
	return false
}
