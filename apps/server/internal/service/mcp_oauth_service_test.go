package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

func newMCPOAuthServiceTest(t *testing.T) (*MCPOAuthService, *repository.MCPOAuthRepository, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.MCPOAuthClient{}, &model.MCPAccessToken{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移 MCP OAuth 测试表失败: %v", err)
	}
	repo := repository.NewMCPOAuthRepository(db)
	return NewMCPOAuthService(db, repo, repository.NewAuditLogRepository(db)), repo, db
}

func newMCPOAuthApprovalTest(t *testing.T) (*MCPOAuthService, *ApprovalService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开审批测试数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.MCPOAuthClient{}, &model.MCPOAuthClientChange{}, &model.MCPAccessToken{}, &model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移 MCP 审批测试表失败: %v", err)
	}
	registry := authz.NewApprovalRegistry()
	oauth := NewMCPOAuthService(db, repository.NewMCPOAuthRepository(db), repository.NewAuditLogRepository(db))
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	oauth.SetApprovalService(approval)
	RegisterMCPOAuthApprovalAdapters(registry, oauth)
	return oauth, approval, db
}

func TestMCPOAuthCreateApprovalAppliesClientAndNeverPersistsPlaintext(t *testing.T) {
	oauth, approval, db := newMCPOAuthApprovalTest(t)
	created, err := oauth.RequestCreate("部署机器人", model.MCPClientProfileAutomation, "需要连接控制面", "mcp-create-1", auth.HumanPrincipal("alice"), "127.0.0.1")
	if err != nil {
		t.Fatalf("创建 MCP 客户端审批失败: %v", err)
	}
	if !strings.HasPrefix(created.ClientSecret, mcpSecretPrefix) {
		t.Fatalf("首次响应应含一次性 secret，实际 %q", created.ClientSecret)
	}
	var request model.ApprovalRequest
	if err := db.Where("request_id = ?", created.ApprovalRequestID).First(&request).Error; err != nil {
		t.Fatalf("查询审批请求失败: %v", err)
	}
	if strings.Contains(request.Payload, created.ClientSecret) || strings.Contains(request.Payload, mcpHash(created.ClientSecret)) {
		t.Fatalf("审批载荷泄露 secret 或 hash: %s", request.Payload)
	}
	repeated, err := oauth.RequestCreate("部署机器人", model.MCPClientProfileAutomation, "需要连接控制面", "mcp-create-1", auth.HumanPrincipal("alice"), "127.0.0.1")
	if err != nil || repeated.ApprovalRequestID != created.ApprovalRequestID || repeated.ClientSecret != "" {
		t.Fatalf("幂等重试只能返回原 request ID 且不能重放 secret: ticket=%+v err=%v", repeated, err)
	}
	if _, err := approval.Approve(created.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准 MCP 客户端审批失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("worker 应执行一次，processed=%d err=%v", processed, err)
	}
	var client model.MCPOAuthClient
	if err := db.Where("client_id = ?", created.ClientID).First(&client).Error; err != nil || client.Status != model.MCPClientStatusActive || client.SecretVersion != 1 {
		t.Fatalf("已批准客户端状态不符: client=%+v err=%v", client, err)
	}
	if _, _, _, err := oauth.IssueAccessToken(created.ClientID, created.ClientSecret, "https://beacon.example/admin/v2/mcp", "observer"); err != nil {
		t.Fatalf("批准后 secret 应可换取 token: %v", err)
	}
	var receipt model.ApprovalExecutionReceipt
	if err := db.Where("request_id = ?", created.ApprovalRequestID).First(&receipt).Error; err != nil || receipt.ResultRef != created.ClientID {
		t.Fatalf("领域写入应与 receipt 同事务完成: receipt=%+v err=%v", receipt, err)
	}
}

func TestMCPOAuthWithdrawInvalidatesPendingChange(t *testing.T) {
	oauth, approval, db := newMCPOAuthApprovalTest(t)
	created, err := oauth.RequestCreate("观察者", model.MCPClientProfileObserver, "需要只读访问", "mcp-withdraw-1", auth.HumanPrincipal("alice"), "127.0.0.1")
	if err != nil {
		t.Fatalf("创建审批失败: %v", err)
	}
	requester := auth.HumanPrincipal("alice")
	if _, err := approval.Withdraw(created.ApprovalRequestID, requester, "127.0.0.1"); err != nil {
		t.Fatalf("撤回审批失败: %v", err)
	}
	var change model.MCPOAuthClientChange
	if err := db.Where("approval_request_id = ?", created.ApprovalRequestID).First(&change).Error; err != nil || change.Status != model.MCPClientChangeInvalidated {
		t.Fatalf("撤回应使待应用变更失效: change=%+v err=%v", change, err)
	}
	if _, err := oauth.RequestCreate("不同名称", model.MCPClientProfileObserver, "需要只读访问", "mcp-withdraw-1", auth.HumanPrincipal("alice"), "127.0.0.1"); !errors.Is(err, apperr.ErrIdempotencyKeyReused) {
		t.Fatalf("同幂等键改变语义应拒绝，实际 %v", err)
	}
}

func TestMCPOAuthIssueAndVerifyAudienceBoundToken(t *testing.T) {
	svc, repo, _ := newMCPOAuthServiceTest(t)
	secret, hash, err := NewMCPClientSecret()
	if err != nil {
		t.Fatalf("生成客户端 secret 失败: %v", err)
	}
	if err := repo.CreateClient(&model.MCPOAuthClient{ClientID: secret.ClientID, DisplayName: "自动化", SecretHash: hash, SecretPrefix: secret.Prefix, Profile: model.MCPClientProfileAutomation, Status: model.MCPClientStatusActive, SecretVersion: 1, CreatedBy: "human:admin"}); err != nil {
		t.Fatalf("创建测试客户端失败: %v", err)
	}
	const audience = "https://beacon.example/admin/v2/mcp"
	token, ttl, principal, err := svc.IssueAccessToken(secret.ClientID, secret.Secret, audience, "observer")
	if err != nil {
		t.Fatalf("签发 token 失败: %v", err)
	}
	if ttl != mcpAccessTokenTTL || principal.Kind != auth.PrincipalKindMCP || principal.Role != model.MCPClientProfileAutomation {
		t.Fatalf("签发主体或有效期不符: ttl=%s principal=%+v", ttl, principal)
	}
	if _, err := svc.VerifyAccessToken(token, audience); err != nil {
		t.Fatalf("合法 audience 的 token 应通过: %v", err)
	}
	if _, err := svc.VerifyAccessToken(token, "https://beacon.example/admin/v2/other"); err == nil {
		t.Fatal("错误 audience 不应通过 token 校验")
	}
}

func TestMCPOAuthRotationVersionImmediatelyInvalidatesOldToken(t *testing.T) {
	svc, repo, db := newMCPOAuthServiceTest(t)
	secret, hash, err := NewMCPClientSecret()
	if err != nil {
		t.Fatalf("生成客户端 secret 失败: %v", err)
	}
	client := &model.MCPOAuthClient{ClientID: secret.ClientID, DisplayName: "只读", SecretHash: hash, SecretPrefix: secret.Prefix, Profile: model.MCPClientProfileObserver, Status: model.MCPClientStatusActive, SecretVersion: 1, CreatedBy: "human:admin"}
	if err := repo.CreateClient(client); err != nil {
		t.Fatalf("创建测试客户端失败: %v", err)
	}
	const audience = "https://beacon.example/admin/v2/mcp"
	token, _, _, err := svc.IssueAccessToken(secret.ClientID, secret.Secret, audience, "")
	if err != nil {
		t.Fatalf("签发 token 失败: %v", err)
	}
	if err := db.Model(&model.MCPOAuthClient{}).Where("client_id = ?", secret.ClientID).Update("secret_version", 2).Error; err != nil {
		t.Fatalf("模拟轮换失败: %v", err)
	}
	if _, err := svc.VerifyAccessToken(token, audience); err == nil {
		t.Fatal("轮换后的旧 token 不应通过")
	}
}

func TestMCPOAuthRejectsExpiredToken(t *testing.T) {
	svc, repo, db := newMCPOAuthServiceTest(t)
	secret, hash, err := NewMCPClientSecret()
	if err != nil {
		t.Fatalf("生成客户端 secret 失败: %v", err)
	}
	if err := repo.CreateClient(&model.MCPOAuthClient{ClientID: secret.ClientID, DisplayName: "只读", SecretHash: hash, SecretPrefix: secret.Prefix, Profile: model.MCPClientProfileObserver, Status: model.MCPClientStatusActive, SecretVersion: 1, CreatedBy: "human:admin"}); err != nil {
		t.Fatalf("创建测试客户端失败: %v", err)
	}
	const audience = "https://beacon.example/admin/v2/mcp"
	token, _, _, err := svc.IssueAccessToken(secret.ClientID, secret.Secret, audience, "")
	if err != nil {
		t.Fatalf("签发 token 失败: %v", err)
	}
	if err := db.Model(&model.MCPAccessToken{}).Where("client_id = ?", secret.ClientID).Update("expires_at", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatalf("模拟过期失败: %v", err)
	}
	if _, err := svc.VerifyAccessToken(token, audience); err == nil {
		t.Fatal("过期 token 不应通过")
	}
}

// TestMCPOAuthRequestCredentialChangeDistinguishesPreconditions 验证四类前置失败各自返回
// 可区分的错误码，而不是合并成一个泛化 403。
//
// 动机：这四类失败的正确处置完全不同——无审批服务要去查部署，非人类主体要换主体，
// 缺原因要补原因，缺幂等键要补请求头。此前统一回 ErrForbidden（文案「只读密钥无权执行写操作」），
// 会把调用方引向排查权限，方向完全错误。
func TestMCPOAuthRequestCredentialChangeDistinguishesPreconditions(t *testing.T) {
	const (
		reason = "需要连接控制面"
		key    = "mcp-precond-1"
	)
	cases := []struct {
		name      string
		reason    string
		key       string
		principal auth.Principal
		want      *apperr.Error
	}{
		{"缺原因", " ", key, auth.HumanPrincipal("alice"), apperr.ErrReasonRequired},
		{"缺幂等键", reason, "", auth.HumanPrincipal("alice"), apperr.ErrIdempotencyKeyRequired},
		{"非人类主体", reason, key, auth.MCPPrincipal("mcp_robot", "自动化", model.MCPClientProfileAutomation), apperr.ErrHumanOnlyOperation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oauth, _, _ := newMCPOAuthApprovalTest(t)
			_, err := oauth.RequestCreate("机器人", model.MCPClientProfileObserver, tc.reason, tc.key, tc.principal, "127.0.0.1")
			if err == nil {
				t.Fatal("应拒绝，实际通过")
			}
			var ae *apperr.Error
			if !errors.As(err, &ae) {
				t.Fatalf("应为 *apperr.Error，实际 %T", err)
			}
			if ae.Code != tc.want.Code {
				t.Fatalf("错误码应为 %q，实际 %q（文案 %q）", tc.want.Code, ae.Code, ae.Message)
			}
			// 明确不得退化为泛化 403 文案，否则等于回到修复前。
			if ae.Code == apperr.ErrForbidden.Code {
				t.Fatalf("不应复用泛化 %q", apperr.ErrForbidden.Code)
			}
		})
	}
}

// TestMCPOAuthRequestCredentialChangeWithoutApprovalIsInternal 覆盖第四类分支：审批服务未装配。
//
// 装配缺失是服务端问题（500 INTERNAL），不能与调用方的前置失败（400 / 403）混用同一个码，
// 否则调用方会去核对参数或权限，而真实原因是部署未接线。
func TestMCPOAuthRequestCredentialChangeWithoutApprovalIsInternal(t *testing.T) {
	// 刻意不调用 SetApprovalService：模拟装配缺失。
	oauth, _, _ := newMCPOAuthServiceTest(t)
	_, err := oauth.RequestCreate("机器人", model.MCPClientProfileObserver, "合法原因", "mcp-precond-no-approval", auth.HumanPrincipal("alice"), "127.0.0.1")
	if !errors.Is(err, apperr.ErrInternal) {
		t.Fatalf("审批服务未装配应回 500 INTERNAL，实际 %v", err)
	}
	if errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("装配缺失不应报成调用方越权（%q）", apperr.ErrForbidden.Code)
	}
}

// TestMCPOAuthRequestCredentialChangeHumanStillWorks 保证收敛错误码后正常人类提审不受影响。
func TestMCPOAuthRequestCredentialChangeHumanStillWorks(t *testing.T) {
	oauth, _, _ := newMCPOAuthApprovalTest(t)
	ticket, err := oauth.RequestCreate("正常机器人", model.MCPClientProfileObserver, "合法原因", "mcp-precond-ok", auth.HumanPrincipal("alice"), "127.0.0.1")
	if err != nil {
		t.Fatalf("人类主体带齐参数应成功，实际 %v", err)
	}
	if ticket.ApprovalRequestID == "" || ticket.ClientSecret == "" {
		t.Fatalf("应返回票据与一次性明文，实际 %+v", ticket)
	}
}
