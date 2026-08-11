package service

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// TestAssetPreviewApprovalEnqueuesGrantAndConsumesOnce 验证批准才原子入队命令、签发 pending grant，
// Agent 回传后仅原申请人可一次消费瞬态正文。
func TestAssetPreviewApprovalEnqueuesGrantAndConsumesOnce(t *testing.T) {
	db := newAssetSvcTestDB(t)
	if err := db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}, &model.SensitiveAccessGrant{}); err != nil {
		t.Fatalf("迁移审批表失败: %v", err)
	}
	svc := newAssetSvc(db, true)
	registry := authz.NewApprovalRegistry()
	grants := NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	svc.SetSensitiveAccessApproval(approval, grants)
	RegisterAssetPreviewApprovalAdapter(registry, svc, grants)

	row := seedPreviewServer(t, db, "prod", "lobby-1")
	seedAsset(t, db, row, "plugins/Beacon/config.yml", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", 12, true)
	request, err := svc.RequestAccess("lobby-1", "plugins/Beacon/config.yml", "排查配置", "asset-preview-1", auth.HumanPrincipal("alice"), "")
	if err != nil {
		t.Fatalf("创建审批申请失败: %v", err)
	}
	if len(allCommands(t, db)) != 0 {
		t.Fatal("批准前不得创建 Agent 命令")
	}
	if _, err := approval.Approve(request.RequestID, auth.HumanPrincipal("alice"), ""); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if _, err := NewApprovalWorker(approval).RunOnce(context.Background()); err != nil {
		t.Fatalf("执行审批 worker 失败: %v", err)
	}
	cmds := allCommands(t, db)
	if len(cmds) != 1 || cmds[0].Status != model.CommandStatusPending {
		t.Fatalf("批准后应仅入队一条读取命令，实际 %+v", cmds)
	}
	grant, err := repository.NewSensitiveAccessGrantRepository(db).FindByApprovalRequestID(request.RequestID)
	if err != nil || grant == nil || grant.Status != model.SensitiveAccessGrantStatusPending {
		t.Fatalf("批准后应同事务创建 pending grant: grant=%+v err=%v", grant, err)
	}
	if ok, err := repository.NewAgentCommandRepository(db).UpdateStatus(cmds[0].ID, model.CommandStatusPending, model.CommandStatusFetched, ""); err != nil || !ok {
		t.Fatalf("模拟 Agent 拉取命令失败: ok=%v err=%v", ok, err)
	}
	if err := svc.ReceiveContent("prod", "lobby-1", cmds[0].ID, AssetContentPayload{Content: "敏感正文"}); err != nil {
		t.Fatalf("Agent 回传失败: %v", err)
	}
	if _, err := svc.ConsumeApproved(grant.GrantID, cmds[0].ID, auth.HumanPrincipal("bob")); !errors.Is(err, apperr.ErrSensitiveAccessWrongPrincipal) {
		t.Fatalf("其他主体不得消费: %v", err)
	}
	result, err := svc.ConsumeApproved(grant.GrantID, cmds[0].ID, auth.HumanPrincipal("alice"))
	if err != nil || result.Content == nil || *result.Content != "敏感正文" {
		t.Fatalf("原申请人应一次消费正文: result=%+v err=%v", result, err)
	}
	if _, err := svc.ConsumeApproved(grant.GrantID, cmds[0].ID, auth.HumanPrincipal("alice")); err == nil {
		t.Fatal("已消费授权不得重放")
	}
}

// TestAssetPreviewApprovalRejectsChangedAsset 验证审批后清单哈希漂移不会创建命令或 grant。
func TestAssetPreviewApprovalRejectsChangedAsset(t *testing.T) {
	db := newAssetSvcTestDB(t)
	if err := db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}, &model.SensitiveAccessGrant{}); err != nil {
		t.Fatalf("迁移审批表失败: %v", err)
	}
	svc := newAssetSvc(db, true)
	registry := authz.NewApprovalRegistry()
	grants := NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	svc.SetSensitiveAccessApproval(approval, grants)
	RegisterAssetPreviewApprovalAdapter(registry, svc, grants)
	row := seedPreviewServer(t, db, "prod", "lobby-1")
	seedAsset(t, db, row, "a.yml", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", 1, true)
	request, err := svc.RequestAccess("lobby-1", "a.yml", "核验", "asset-preview-2", auth.HumanPrincipal("alice"), "")
	if err != nil {
		t.Fatalf("创建申请失败: %v", err)
	}
	if err := db.Model(&model.FileAsset{}).Where("server_id = ? AND path = ?", row, "a.yml").Update("sha256", "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd").Error; err != nil {
		t.Fatalf("制造版本漂移失败: %v", err)
	}
	if _, err := approval.Approve(request.RequestID, auth.HumanPrincipal("alice"), ""); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if _, err := NewApprovalWorker(approval).RunOnce(context.Background()); err != nil {
		t.Fatalf("执行 worker 失败: %v", err)
	}
	if len(allCommands(t, db)) != 0 {
		t.Fatal("版本漂移不得创建命令")
	}
	var stored model.ApprovalRequest
	if err := db.Where("request_id = ?", request.RequestID).First(&stored).Error; err != nil || stored.Status != model.ApprovalStatusFailed {
		t.Fatalf("版本漂移应使审批失败: status=%s err=%v", stored.Status, err)
	}
}

// TestAssetPreviewApprovalRevokesGrantWhenAgentFails 验证失败回传不能留下未来可激活的授权。
func TestAssetPreviewApprovalRevokesGrantWhenAgentFails(t *testing.T) {
	db := newAssetSvcTestDB(t)
	if err := db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}, &model.SensitiveAccessGrant{}); err != nil {
		t.Fatalf("迁移审批表失败: %v", err)
	}
	svc := newAssetSvc(db, true)
	registry := authz.NewApprovalRegistry()
	grants := NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	svc.SetSensitiveAccessApproval(approval, grants)
	RegisterAssetPreviewApprovalAdapter(registry, svc, grants)
	row := seedPreviewServer(t, db, "prod", "lobby-1")
	seedAsset(t, db, row, "a.yml", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", 1, true)
	request, err := svc.RequestAccess("lobby-1", "a.yml", "核验", "asset-preview-3", auth.HumanPrincipal("alice"), "")
	if err != nil {
		t.Fatalf("创建申请失败: %v", err)
	}
	if _, err := approval.Approve(request.RequestID, auth.HumanPrincipal("alice"), ""); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if _, err := NewApprovalWorker(approval).RunOnce(context.Background()); err != nil {
		t.Fatalf("执行 worker 失败: %v", err)
	}
	cmd := allCommands(t, db)[0]
	if ok, err := repository.NewAgentCommandRepository(db).UpdateStatus(cmd.ID, model.CommandStatusPending, model.CommandStatusFetched, ""); err != nil || !ok {
		t.Fatalf("模拟 Agent 拉取命令失败: ok=%v err=%v", ok, err)
	}
	if err := svc.ReceiveContent("prod", "lobby-1", cmd.ID, AssetContentPayload{Error: "读取失败"}); err != nil {
		t.Fatalf("Agent 失败回传处理失败: %v", err)
	}
	grant, err := repository.NewSensitiveAccessGrantRepository(db).FindByApprovalRequestID(request.RequestID)
	if err != nil || grant == nil || grant.Status != model.SensitiveAccessGrantStatusRevoked {
		t.Fatalf("失败回传应撤销 grant: grant=%+v err=%v", grant, err)
	}
}

// TestAssetPairReadApprovalRequiresBothGrants 验证跨服读取分别审批、双侧原子消费且结果不返回正文。
func TestAssetPairReadApprovalRequiresBothGrants(t *testing.T) {
	db := newAssetSvcTestDB(t)
	if err := db.AutoMigrate(&model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}, &model.SensitiveAccessGrant{}); err != nil {
		t.Fatalf("迁移审批表失败: %v", err)
	}
	svc := newAssetSvc(db, true)
	registry := authz.NewApprovalRegistry()
	grants := NewSensitiveAccessGrantService(repository.NewSensitiveAccessGrantRepository(db))
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	svc.SetSensitiveAccessApproval(approval, grants)
	RegisterAssetPreviewApprovalAdapter(registry, svc, grants)
	leftServer := seedPreviewServer(t, db, "prod", "lobby-1")
	rightServer := seedPreviewServer(t, db, "prod", "lobby-2")
	leftHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	rightHash := "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"
	seedAsset(t, db, leftServer, "plugins/Beacon/config.yml", leftHash, 8, true)
	seedAsset(t, db, rightServer, "plugins/Beacon/config.yml", rightHash, 9, true)
	leftRequest, rightRequest, err := svc.RequestPairAccess(AssetRef{ServerID: "lobby-1", Path: "plugins/Beacon/config.yml"}, AssetRef{ServerID: "lobby-2", Path: "plugins/Beacon/config.yml"}, "核验跨服差异", "asset-pair-1", auth.HumanPrincipal("alice"), "")
	if err != nil {
		t.Fatalf("创建双侧审批失败: %v", err)
	}
	if leftRequest.RequestID == rightRequest.RequestID {
		t.Fatal("双侧必须是独立审批申请")
	}
	commands := approveAndFetchAssetPair(t, db, svc, approval, leftRequest, rightRequest)
	leftGrant, err := repository.NewSensitiveAccessGrantRepository(db).FindByApprovalRequestID(leftRequest.RequestID)
	if err != nil || leftGrant == nil || leftGrant.PairID == "" || leftGrant.PairSide != "left" {
		t.Fatalf("左侧应有成对授权: grant=%+v err=%v", leftGrant, err)
	}
	if _, err := svc.ConsumeApproved(leftGrant.GrantID, commands[0].ID, auth.HumanPrincipal("alice")); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("成对授权不得走单侧正文消费: %v", err)
	}
	leftCommand := commands[0]
	if leftCommand.ServerID != "lobby-1" {
		leftCommand = commands[1]
	}
	result, err := svc.ConsumePairApproved(leftGrant.GrantID, leftCommand.ID, auth.HumanPrincipal("alice"))
	if err != nil || !result.Changed || result.Identical || result.Unsupported || result.Left.SHA256 != leftHash || result.Right.SHA256 != rightHash {
		t.Fatalf("双侧授权应只返回安全差异摘要: result=%+v err=%v", result, err)
	}
	if result.Left.Path == "" || result.Right.Path == "" {
		t.Fatal("安全摘要应保留已直接可读的元数据")
	}
	if countAudit(t, db, model.ActionAssetDiff) != 1 || auditDetailContains(t, db, model.ActionAssetDiff, "左侧正文") || auditDetailContains(t, db, model.ActionAssetDiff, "右侧正文") {
		t.Fatal("双侧差异审计不得包含正文")
	}
}

func approveAndFetchAssetPair(t *testing.T, db *gorm.DB, svc *AssetPreviewService, approval *ApprovalService,
	leftRequest, rightRequest model.ApprovalRequest,
) []model.AgentCommand {
	t.Helper()
	approveAssetPairRequests(t, approval, leftRequest, rightRequest)
	commands := allCommands(t, db)
	if len(commands) != 2 {
		t.Fatalf("双侧批准后应各建一条命令，实际 %d", len(commands))
	}
	for _, command := range commands {
		fetchAssetPairContent(t, db, svc, command)
	}
	return commands
}

func approveAssetPairRequests(t *testing.T, approval *ApprovalService, leftRequest, rightRequest model.ApprovalRequest) {
	t.Helper()
	for _, request := range []model.ApprovalRequest{leftRequest, rightRequest} {
		if _, err := approval.Approve(request.RequestID, auth.HumanPrincipal("alice"), ""); err != nil {
			t.Fatalf("批准双侧申请失败: %v", err)
		}
		if _, err := NewApprovalWorker(approval).RunOnce(context.Background()); err != nil {
			t.Fatalf("执行审批 worker 失败: %v", err)
		}
	}
}

func fetchAssetPairContent(t *testing.T, db *gorm.DB, svc *AssetPreviewService, command model.AgentCommand) {
	t.Helper()
	if ok, err := repository.NewAgentCommandRepository(db).UpdateStatus(command.ID, model.CommandStatusPending, model.CommandStatusFetched, ""); err != nil || !ok {
		t.Fatalf("模拟 Agent 拉取命令失败: ok=%v err=%v", ok, err)
	}
	content := "左侧正文"
	if command.ServerID == "lobby-2" {
		content = "右侧正文"
	}
	if err := svc.ReceiveContent("prod", command.ServerID, command.ID, AssetContentPayload{Content: content}); err != nil {
		t.Fatalf("Agent 回传失败: %v", err)
	}
}
