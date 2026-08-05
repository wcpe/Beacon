package service

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/secret"
)

func newConfigApprovalTestSuite(t *testing.T) (*ConfigService, *ApprovalService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.ConfigItem{}, &model.ConfigRevision{}, &model.ConfigGray{}, &model.ConfigPendingChange{}, &model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移配置审批表失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	rawKey := make([]byte, secret.KeyBytes)
	for i := range rawKey {
		rawKey[i] = byte(i + 1)
	}
	cipher, err := secret.NewCipher(base64.StdEncoding.EncodeToString(rawKey))
	if err != nil {
		t.Fatalf("构造测试加密器失败: %v", err)
	}
	configService := NewConfigService(db, repository.NewConfigItemRepository(db, cipher), repository.NewConfigRevisionRepository(db, cipher), repository.NewAuditLogRepository(db))
	registry := authz.NewApprovalRegistry()
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	configService.SetApprovalService(approval)
	configService.SetPendingChangeCipher(cipher)
	configService.SetGrayService(NewConfigGrayService(db, configService, repository.NewConfigItemRepository(db, cipher), repository.NewConfigGrayRepository(db, cipher), repository.NewAuditLogRepository(db)))
	RegisterConfigApprovalAdapters(registry, configService)
	return configService, approval, db
}

func TestConfigGrayPublishApprovalFailsClosedThenAppliesWithReceipt(t *testing.T) {
	configs, approval, db := newConfigApprovalTestSuite(t)
	item, err := configs.Create(CreateConfigParams{Namespace: "prod", DataID: "app.yml", ScopeLevel: model.ScopeGlobal, Format: "yaml", Content: "enabled: false\n", Operator: "alice"})
	if err != nil {
		t.Fatalf("创建配置失败: %v", err)
	}
	if _, err := configs.gray.Publish(item.ID, "enabled: true\n", []string{"s1"}, "alice", "灰度", ""); err != apperr.ErrForbidden {
		t.Fatalf("公开灰度发布必须拒绝：%v", err)
	}
	content := "enabled: true\n"
	ticket, err := configs.RequestGrayPublish(item.ID, content, []string{"s1"}, "灰度开关", "gray-publish-approval", "alice", "灰度", "127.0.0.1", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("申请灰度发布审批失败: %v", err)
	}
	var req model.ApprovalRequest
	if err := db.Where("request_id = ?", ticket.ApprovalRequestID).First(&req).Error; err != nil || strings.Contains(req.Payload, content) {
		t.Fatalf("审批载荷不得泄露灰度内容：err=%v payload=%s", err, req.Payload)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("灰度审批 worker 应应用一次：processed=%d err=%v", processed, err)
	}
	gray, err := configs.gray.grayRepo.FindActiveByItem(item.ID)
	if err != nil || gray == nil || gray.Content != content {
		t.Fatalf("批准后应存在灰度：gray=%+v err=%v", gray, err)
	}
	var receipt model.ApprovalExecutionReceipt
	if err := db.Where("request_id = ?", ticket.ApprovalRequestID).First(&receipt).Error; err != nil {
		t.Fatalf("批准执行必须同事务写回执: %v", err)
	}
}
func TestConfigGrayPublishWithdrawalInvalidatesPendingChange(t *testing.T) {
	configs, approval, db := newConfigApprovalTestSuite(t)
	item, err := configs.Create(CreateConfigParams{Namespace: "prod", DataID: "app.yml", ScopeLevel: model.ScopeGlobal, Format: "yaml", Content: "enabled: false\n", Operator: "alice"})
	if err != nil {
		t.Fatalf("创建配置失败: %v", err)
	}
	ticket, err := configs.RequestGrayPublish(item.ID, "enabled: true\n", []string{"s1"}, "灰度开关", "gray-withdraw", "alice", "灰度", "127.0.0.1", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("申请灰度发布审批失败: %v", err)
	}
	if _, err := approval.Withdraw(ticket.ApprovalRequestID, auth.HumanPrincipal("alice"), "127.0.0.1"); err != nil {
		t.Fatalf("撤回失败: %v", err)
	}
	var change model.ConfigPendingChange
	if err := db.Where("approval_request_id = ?", ticket.ApprovalRequestID).First(&change).Error; err != nil || change.Status != model.ConfigPendingChangeInvalidated {
		t.Fatalf("撤回应使灰度冻结变更失效：status=%s err=%v", change.Status, err)
	}
	gray, err := configs.gray.grayRepo.FindActiveByItem(item.ID)
	if err != nil || gray != nil {
		t.Fatalf("撤回不得创建灰度：gray=%+v err=%v", gray, err)
	}
}

func TestConfigPublishApprovalStoresOnlyEncryptedPendingContent(t *testing.T) {
	configs, approval, db := newConfigApprovalTestSuite(t)
	item, err := configs.Create(CreateConfigParams{Namespace: "prod", DataID: "app.yml", ScopeLevel: model.ScopeGlobal, Format: "yaml", Content: "enabled: false\n", Operator: "alice"})
	if err != nil {
		t.Fatalf("创建配置失败: %v", err)
	}
	content := "token: not-for-approval-payload\n"
	ticket, err := configs.RequestPublish(item.ID, content, "变更令牌", "publish-approval", "alice", "127.0.0.1", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("申请发布审批失败: %v", err)
	}
	var req model.ApprovalRequest
	if err := db.Where("request_id = ?", ticket.ApprovalRequestID).First(&req).Error; err != nil {
		t.Fatalf("查询审批失败: %v", err)
	}
	if strings.Contains(req.Payload, content) || strings.Contains(req.SafeSummary, content) || strings.Contains(req.EvidenceSnapshot, content) {
		t.Fatalf("审批记录泄露配置内容")
	}
	var change model.ConfigPendingChange
	if err := db.Where("approval_request_id = ?", ticket.ApprovalRequestID).First(&change).Error; err != nil {
		t.Fatalf("查询冻结变更失败: %v", err)
	}
	if !secret.IsEncrypted(change.Ciphertext) || strings.Contains(change.Ciphertext, content) {
		t.Fatalf("冻结内容必须仅以密文保存：%q", change.Ciphertext)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("审批 worker 应应用一次：processed=%d err=%v", processed, err)
	}
	updated, err := configs.Get(item.ID)
	if err != nil || updated.Content != content || updated.Version != 2 {
		t.Fatalf("批准后应发布新版本：item=%+v err=%v", updated, err)
	}
	if err := db.Where("request_id = ?", ticket.ApprovalRequestID).First(&req).Error; err != nil || req.Status != model.ApprovalStatusSucceeded {
		t.Fatalf("审批应成功：status=%s err=%v", req.Status, err)
	}
}

func TestConfigPublishApprovalWithdrawInvalidatesPendingChange(t *testing.T) {
	configs, approval, db := newConfigApprovalTestSuite(t)
	item, err := configs.Create(CreateConfigParams{Namespace: "prod", DataID: "app.yml", ScopeLevel: model.ScopeGlobal, Format: "yaml", Content: "enabled: false\n", Operator: "alice"})
	if err != nil {
		t.Fatalf("创建配置失败: %v", err)
	}
	ticket, err := configs.RequestPublish(item.ID, "enabled: true\n", "变更开关", "publish-withdraw", "alice", "127.0.0.1", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("申请发布审批失败: %v", err)
	}
	if _, err := approval.Withdraw(ticket.ApprovalRequestID, auth.HumanPrincipal("alice"), "127.0.0.1"); err != nil {
		t.Fatalf("撤回失败: %v", err)
	}
	var change model.ConfigPendingChange
	if err := db.Where("approval_request_id = ?", ticket.ApprovalRequestID).First(&change).Error; err != nil || change.Status != model.ConfigPendingChangeInvalidated {
		t.Fatalf("撤回应使冻结变更失效：status=%s err=%v", change.Status, err)
	}
	unchanged, err := configs.Get(item.ID)
	if err != nil || unchanged.Version != 1 || unchanged.Content != "enabled: false\n" {
		t.Fatalf("撤回不得发布配置：item=%+v err=%v", unchanged, err)
	}
}

func TestConfigRollbackApprovalAppliesSourceRevision(t *testing.T) {
	configs, approval, db := newConfigApprovalTestSuite(t)
	item, err := configs.Create(CreateConfigParams{Namespace: "prod", DataID: "app.yml", ScopeLevel: model.ScopeGlobal, Format: "yaml", Content: "enabled: false\n", Operator: "alice"})
	if err != nil {
		t.Fatalf("创建配置失败: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, e := configs.applyPublishInTx(tx, item.ID, "enabled: true\n", "alice", "测试历史版本", "", 1)
		return e
	}); err != nil {
		t.Fatalf("准备历史版本失败: %v", err)
	}
	ticket, err := configs.RequestRollback(item.ID, 1, "回滚开关", "rollback-approval", "alice", "测试回滚", "127.0.0.1", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("申请回滚审批失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("审批 worker 应应用一次：processed=%d err=%v", processed, err)
	}
	updated, err := configs.Get(item.ID)
	if err != nil || updated.Version != 3 || updated.Content != "enabled: false\n" {
		t.Fatalf("批准后应回滚为新版本：item=%+v err=%v", updated, err)
	}
	revision, err := configs.GetRevision(item.ID, 3)
	if err != nil || revision.SourceRevision == nil {
		t.Fatalf("回滚版本应记录来源：revision=%+v err=%v", revision, err)
	}
}

func TestConfigDeleteAndBatchRequireApproval(t *testing.T) {
	configs, approval, db := newConfigApprovalTestSuite(t)
	first, err := configs.Create(CreateConfigParams{Namespace: "prod", DataID: "first.yml", ScopeLevel: model.ScopeGlobal, Format: "yaml", Content: "enabled: false\n", Operator: "alice"})
	if err != nil {
		t.Fatalf("创建第一条配置失败: %v", err)
	}
	second, err := configs.Create(CreateConfigParams{Namespace: "prod", DataID: "second.yml", ScopeLevel: model.ScopeGlobal, Format: "yaml", Content: "enabled: false\n", Operator: "alice"})
	if err != nil {
		t.Fatalf("创建第二条配置失败: %v", err)
	}
	if err := configs.Delete(first.ID, "alice", "", ""); err != apperr.ErrForbidden {
		t.Fatalf("公开删除必须拒绝: %v", err)
	}
	if err := configs.BatchDelete([]uint{first.ID, second.ID}, "alice", ""); err != apperr.ErrForbidden {
		t.Fatalf("公开批量删除必须拒绝: %v", err)
	}
	if err := configs.BatchSetEnabled([]uint{first.ID, second.ID}, false, "alice", ""); err != apperr.ErrForbidden {
		t.Fatalf("公开批量置态必须拒绝: %v", err)
	}
	ticket, err := configs.RequestBatchSetEnabled([]uint{first.ID, second.ID}, false, "批量禁用", "config-batch-disable", "alice", "", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建批量审批失败: %v", err)
	}
	var req model.ApprovalRequest
	if err := db.Where("request_id = ?", ticket.ApprovalRequestID).First(&req).Error; err != nil {
		t.Fatalf("查询审批申请失败: %v", err)
	}
	if req.Status != model.ApprovalStatusPending || req.OperationKey != authz.OperationConfigBatchDisable {
		t.Fatalf("处理器/服务只应创建待审批 ticket: %+v", req)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准批量禁用失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("审批 worker 应执行批量禁用: processed=%d err=%v", processed, err)
	}
	for _, id := range []uint{first.ID, second.ID} {
		item, err := configs.Get(id)
		if err != nil || item.Enabled {
			t.Fatalf("批准后目标应禁用: id=%d item=%+v err=%v", id, item, err)
		}
	}
	var receipt model.ApprovalExecutionReceipt
	if err := db.Where("request_id = ?", ticket.ApprovalRequestID).First(&receipt).Error; err != nil {
		t.Fatalf("批量审批执行必须同事务写回执: %v", err)
	}
	var auditCount int64
	if err := db.Model(&model.AuditLog{}).Where("action = ?", model.ActionConfigDisable).Count(&auditCount).Error; err != nil || auditCount != 2 {
		t.Fatalf("批量执行必须逐项写领域审计：count=%d err=%v", auditCount, err)
	}
}

func TestConfigBatchApprovalNormalizesTargetsAndExposesSafeEvidence(t *testing.T) {
	configs, _, db := newConfigApprovalTestSuite(t)
	first, err := configs.Create(CreateConfigParams{Namespace: "prod", DataID: "first.yml", ScopeLevel: model.ScopeGlobal, Format: "yaml", Content: "enabled: false\n", Operator: "alice"})
	if err != nil {
		t.Fatalf("创建第一条配置失败: %v", err)
	}
	second, err := configs.Create(CreateConfigParams{Namespace: "prod", DataID: "second.yml", ScopeLevel: model.ScopeGlobal, Format: "yaml", Content: "enabled: true\n", Operator: "alice"})
	if err != nil {
		t.Fatalf("创建第二条配置失败: %v", err)
	}
	firstTicket, err := configs.RequestBatchSetEnabled([]uint{second.ID, first.ID, second.ID}, false, "批量禁用", "config-batch-order-a", "alice", "", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("乱序批量提审失败: %v", err)
	}
	secondTicket, err := configs.RequestBatchSetEnabled([]uint{first.ID, second.ID}, false, "批量禁用", "config-batch-order-b", "alice", "", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("有序批量提审失败: %v", err)
	}
	var firstRequest, secondRequest model.ApprovalRequest
	if err := db.Where("request_id = ?", firstTicket.ApprovalRequestID).First(&firstRequest).Error; err != nil {
		t.Fatalf("读取乱序审批失败: %v", err)
	}
	if err := db.Where("request_id = ?", secondTicket.ApprovalRequestID).First(&secondRequest).Error; err != nil {
		t.Fatalf("读取有序审批失败: %v", err)
	}
	var firstChange, secondChange model.ConfigPendingChange
	if err := db.Where("approval_request_id = ?", firstTicket.ApprovalRequestID).First(&firstChange).Error; err != nil {
		t.Fatalf("读取乱序冻结变更失败: %v", err)
	}
	if err := db.Where("approval_request_id = ?", secondTicket.ApprovalRequestID).First(&secondChange).Error; err != nil {
		t.Fatalf("读取有序冻结变更失败: %v", err)
	}
	if firstChange.ContentSHA256 != secondChange.ContentSHA256 {
		t.Fatalf("同一目标集合乱序提交必须冻结同一目标哈希：%s != %s", firstChange.ContentSHA256, secondChange.ContentSHA256)
	}
	wantTarget := "batch:" + strings.Join([]string{configBatchIDText(first.ID), configBatchIDText(second.ID)}, ",")
	if firstRequest.ResourceID != wantTarget || !strings.Contains(firstRequest.SafeSummary, wantTarget) {
		t.Fatalf("审批目标必须使用稳定批量引用：resourceId=%q summary=%q", firstRequest.ResourceID, firstRequest.SafeSummary)
	}
	var evidence []authz.ApprovalEvidenceLine
	if err := json.Unmarshal([]byte(firstRequest.EvidenceSnapshot), &evidence); err != nil {
		t.Fatalf("解析冻结安全证据失败: %v", err)
	}
	joined := evidenceText(evidence)
	for _, want := range []string{"命名空间=prod", "配置项 ID=" + strings.TrimPrefix(wantTarget, "batch:"), "配置项 " + configBatchIDText(first.ID) + "=版本=1，已启用", "配置项 " + configBatchIDText(second.ID) + "=版本=1，已启用"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("审批人必须可审查冻结批量影响，缺少 %q：%s", want, joined)
		}
	}
}

func TestConfigBatchApprovalRejectsCrossNamespaceBeforeCreatingRequest(t *testing.T) {
	configs, approval, db := newConfigApprovalTestSuite(t)
	prod, err := configs.Create(CreateConfigParams{Namespace: "prod", DataID: "prod.yml", ScopeLevel: model.ScopeGlobal, Format: "yaml", Content: "enabled: true\n", Operator: "alice"})
	if err != nil {
		t.Fatalf("创建 prod 配置失败: %v", err)
	}
	test, err := configs.Create(CreateConfigParams{Namespace: "test", DataID: "test.yml", ScopeLevel: model.ScopeGlobal, Format: "yaml", Content: "enabled: true\n", Operator: "alice"})
	if err != nil {
		t.Fatalf("创建 test 配置失败: %v", err)
	}
	if _, err := configs.RequestBatchDelete([]uint{prod.ID, test.ID}, "跨环境删除", "config-batch-cross-namespace", "alice", "", auth.HumanPrincipal("alice")); !errors.Is(err, apperr.ErrApprovalCrossNamespace) {
		t.Fatalf("跨 namespace 批量提审必须失败关闭，实际 %v", err)
	}
	requests, err := approval.List(ApprovalListFilter{}, auth.HumanPrincipal("alice"))
	if err != nil || len(requests) != 0 {
		t.Fatalf("跨 namespace 拒绝前不得创建审批：requests=%d err=%v", len(requests), err)
	}
	var pendingCount int64
	if err := db.Model(&model.ConfigPendingChange{}).Count(&pendingCount).Error; err != nil || pendingCount != 0 {
		t.Fatalf("跨 namespace 拒绝前不得创建冻结变更：count=%d err=%v", pendingCount, err)
	}
}

func configBatchIDText(id uint) string { return stringifyConfigBatchUint(id) }

func stringifyConfigBatchUint(id uint) string {
	return fmt.Sprintf("%d", id)
}

func evidenceText(lines []authz.ApprovalEvidenceLine) string {
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		parts = append(parts, line.Label+"="+line.Value)
	}
	return strings.Join(parts, "\n")
}

func TestConfigBatchApprovalRejectsFrozenTargetDriftAndInvalidatesTerminal(t *testing.T) {
	configs, approval, db := newConfigApprovalTestSuite(t)
	first, err := configs.Create(CreateConfigParams{Namespace: "prod", DataID: "drift-first.yml", ScopeLevel: model.ScopeGlobal, Format: "yaml", Content: "enabled: false\n", Operator: "alice"})
	if err != nil {
		t.Fatalf("创建第一条配置失败: %v", err)
	}
	second, err := configs.Create(CreateConfigParams{Namespace: "prod", DataID: "drift-second.yml", ScopeLevel: model.ScopeGlobal, Format: "yaml", Content: "enabled: false\n", Operator: "alice"})
	if err != nil {
		t.Fatalf("创建第二条配置失败: %v", err)
	}
	ticket, err := configs.RequestBatchDelete([]uint{first.ID, second.ID}, "批量删除", "config-batch-drift", "alice", "", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建批量删除审批失败: %v", err)
	}
	if err := db.Model(&model.ConfigItem{}).Where("id = ?", second.ID).Update("enabled", false).Error; err != nil {
		t.Fatalf("制造目标状态漂移失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准批量删除失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("漂移审批 worker 应完成失败收敛: processed=%d err=%v", processed, err)
	}
	var req model.ApprovalRequest
	if err := db.Where("request_id = ?", ticket.ApprovalRequestID).First(&req).Error; err != nil || req.Status != model.ApprovalStatusFailed {
		t.Fatalf("目标 id/version/hash 漂移必须失败: req=%+v err=%v", req, err)
	}
	for _, id := range []uint{first.ID, second.ID} {
		if _, err := configs.Get(id); err != nil {
			t.Fatalf("漂移失败时整批不得部分删除: id=%d err=%v", id, err)
		}
	}
	withdrawn, err := configs.RequestDelete(first.ID, "删除配置", "config-delete-withdraw", "alice", "", "", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("创建删除审批失败: %v", err)
	}
	if _, err := approval.Withdraw(withdrawn.ApprovalRequestID, auth.HumanPrincipal("alice"), "127.0.0.1"); err != nil {
		t.Fatalf("撤回删除审批失败: %v", err)
	}
	var change model.ConfigPendingChange
	if err := db.Where("approval_request_id = ?", withdrawn.ApprovalRequestID).First(&change).Error; err != nil || change.Status != model.ConfigPendingChangeInvalidated {
		t.Fatalf("终态必须使冻结删除失效: change=%+v err=%v", change, err)
	}
}
