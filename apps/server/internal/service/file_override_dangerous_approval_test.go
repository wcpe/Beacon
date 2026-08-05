package service

import (
	"encoding/base64"
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

func newFileOverrideApprovalSuite(t *testing.T) (*FileService, *OverrideSetService, *ApprovalService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.FileObject{}, &model.FileRevision{}, &model.FileOverrideSet{}, &model.FileOverrideSetRevision{}, &model.FilePendingChange{}, &model.ApprovalRequest{}, &model.ApprovalExecutionReceipt{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移文件审批表失败: %v", err)
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
	files := NewFileService(db, repository.NewFileObjectRepository(db), repository.NewFileRevisionRepository(db), repository.NewAuditLogRepository(db))
	sets := NewOverrideSetService(db, repository.NewFileOverrideSetRepository(db), repository.NewFileOverrideSetRevisionRepository(db), repository.NewFileObjectRepository(db), repository.NewAuditLogRepository(db))
	registry := authz.NewApprovalRegistry()
	approval := NewApprovalService(db, repository.NewApprovalRequestRepository(db), repository.NewAuditLogRepository(db), registry)
	files.SetApprovalService(approval)
	files.SetPendingChangeCipher(cipher)
	sets.SetApprovalService(approval)
	sets.SetPendingChangeCipher(cipher)
	RegisterFileOverrideApprovalAdapters(registry, files, sets)
	return files, sets, approval, db
}

func TestFilePublishApprovalFailsClosedAndEncryptsPendingContent(t *testing.T) {
	files, _, approval, db := newFileOverrideApprovalSuite(t)
	item, err := files.applyCreate(CreateFileParams{Namespace: "prod", Path: "plugins/demo.yml", ScopeLevel: model.ScopeGlobal, Content: "enabled: false\n", Operator: "alice"})
	if err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}
	if _, err := files.Publish(item.ID, "token: 不得泄露\n", "alice", "", ""); err != apperr.ErrForbidden {
		t.Fatalf("公开发布必须拒绝：%v", err)
	}
	content := "token: 不得泄露\n"
	ticket, err := files.RequestPublish(item.ID, content, "更新文件", "file-publish-approval", "alice", "", "127.0.0.1", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("申请文件发布审批失败: %v", err)
	}
	assertPendingFileContentEncrypted(t, db, ticket.ApprovalRequestID, content)
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("审批 worker 应发布一次：processed=%d err=%v", processed, err)
	}
	updated, err := files.Get(item.ID)
	if err != nil || updated.Version != 2 || updated.Content != content {
		t.Fatalf("批准后应发布文件：item=%+v err=%v", updated, err)
	}
}

func TestFileRollbackAndDeleteRequireApproval(t *testing.T) {
	files, _, approval, _ := newFileOverrideApprovalSuite(t)
	item, err := files.applyCreate(CreateFileParams{Namespace: "prod", Path: "plugins/demo.yml", ScopeLevel: model.ScopeGlobal, Content: "v1\n", Operator: "alice"})
	if err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}
	if _, err := files.Rollback(item.ID, 1, "alice", "", ""); err != apperr.ErrForbidden {
		t.Fatalf("公开回滚必须拒绝：%v", err)
	}
	if err := files.Delete(item.ID, "alice", "", ""); err != apperr.ErrForbidden {
		t.Fatalf("公开删除必须拒绝：%v", err)
	}
	ticket, err := files.RequestRollback(item.ID, 1, "回滚文件", "file-rollback-approval", "alice", "", "127.0.0.1", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("申请文件回滚审批失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准回滚失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("审批 worker 应回滚一次：processed=%d err=%v", processed, err)
	}
	deleteTicket, err := files.RequestDelete(item.ID, "删除文件", "file-delete-approval", "alice", "", "127.0.0.1", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("申请文件删除审批失败: %v", err)
	}
	if _, err := approval.Approve(deleteTicket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准删除失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("审批 worker 应删除一次：processed=%d err=%v", processed, err)
	}
	if _, err := files.Get(item.ID); err != apperr.ErrFileNotFound {
		t.Fatalf("批准后文件应删除：%v", err)
	}
}

func TestFileCreateImportAndBatchRequireApproval(t *testing.T) {
	files, _, approval, _ := newFileOverrideApprovalSuite(t)
	create := CreateFileParams{Namespace: "prod", Path: "plugins/new.yml", ScopeLevel: model.ScopeGlobal, Content: "enabled: true\n", Operator: "alice"}
	if _, err := files.Create(create); err != apperr.ErrForbidden {
		t.Fatalf("公开新建必须拒绝：%v", err)
	}
	ticket, err := files.RequestCreate(create, "新建文件", "file-create-approval", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("申请新建审批失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准新建失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("审批 worker 应新建文件：processed=%d err=%v", processed, err)
	}
	created, err := files.Get(1)
	if err != nil || created.Content != create.Content {
		t.Fatalf("批准后应创建文件：item=%+v err=%v", created, err)
	}

	importParams := ImportFilesParams{Namespace: "prod", Group: "group-a", Files: []ImportFile{{Path: "plugins/import.yml", Content: "v1\n"}}, Operator: "alice"}
	if _, err := files.Import(importParams); err != apperr.ErrForbidden {
		t.Fatalf("公开导入必须拒绝：%v", err)
	}
	importTicket, err := files.RequestImport(importParams, "导入文件", "file-import-approval", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("申请导入审批失败: %v", err)
	}
	if _, err := approval.Approve(importTicket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准导入失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("审批 worker 应导入文件：processed=%d err=%v", processed, err)
	}
	imported, err := files.List(repository.FileFilter{Namespace: "prod", Group: "group-a"})
	if err != nil || len(imported) != 1 || imported[0].Content != "v1\n" {
		t.Fatalf("批准后应导入文件：items=%+v err=%v", imported, err)
	}

	if err := files.BatchSetEnabled([]uint{created.ID}, false, "alice", ""); err != apperr.ErrForbidden {
		t.Fatalf("公开批量禁用必须拒绝：%v", err)
	}
	batchTicket, err := files.RequestBatchSetEnabled([]uint{created.ID}, false, "批量禁用", "file-batch-disable-approval", "alice", "", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("申请批量禁用审批失败: %v", err)
	}
	if _, err := approval.Approve(batchTicket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准批量禁用失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("审批 worker 应批量禁用：processed=%d err=%v", processed, err)
	}
	disabled, err := files.Get(created.ID)
	if err != nil || disabled.Enabled {
		t.Fatalf("批准后应禁用文件：item=%+v err=%v", disabled, err)
	}
}

func TestOverrideSetDangerousActionsRequireApproval(t *testing.T) {
	_, sets, approval, _ := newFileOverrideApprovalSuite(t)
	set, err := sets.Create(CreateOverrideSetParams{Namespace: "prod", Name: "Demo", ScopeLevel: model.ScopeGlobal, TargetRoot: "plugins/Demo", ReloadCommand: "demo reload", Operator: "alice"})
	if err != nil {
		t.Fatalf("创建覆盖集失败: %v", err)
	}
	publish := PublishOverrideSetParams{TargetRoot: "plugins/Demo", ReloadCommand: "demo reload all", Operator: "alice"}
	if _, err := sets.Publish(set.ID, publish); err != apperr.ErrForbidden {
		t.Fatalf("公开覆盖集发布必须拒绝：%v", err)
	}
	if _, err := sets.Rollback(set.ID, 1, "alice", "", ""); err != apperr.ErrForbidden {
		t.Fatalf("公开覆盖集回滚必须拒绝：%v", err)
	}
	if err := sets.Delete(set.ID, "alice", "", ""); err != apperr.ErrForbidden {
		t.Fatalf("公开覆盖集删除必须拒绝：%v", err)
	}
	ticket, err := sets.RequestPublish(set.ID, publish, "发布覆盖集", "override-publish-approval", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("申请覆盖集发布审批失败: %v", err)
	}
	if _, err := approval.Approve(ticket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准发布失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("审批 worker 应发布覆盖集：processed=%d err=%v", processed, err)
	}
	updated, err := sets.Get(set.ID)
	if err != nil || updated.Version != 2 || updated.ReloadCommand != publish.ReloadCommand {
		t.Fatalf("批准后应更新覆盖集：set=%+v err=%v", updated, err)
	}
	rollback, err := sets.RequestRollback(set.ID, 1, "回滚覆盖集", "override-rollback-approval", "alice", "", "127.0.0.1", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("申请覆盖集回滚审批失败: %v", err)
	}
	if _, err := approval.Approve(rollback.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准回滚失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("审批 worker 应回滚覆盖集：processed=%d err=%v", processed, err)
	}
	deleteTicket, err := sets.RequestDelete(set.ID, "删除覆盖集", "override-delete-approval", "alice", "", "127.0.0.1", auth.HumanPrincipal("alice"))
	if err != nil {
		t.Fatalf("申请覆盖集删除审批失败: %v", err)
	}
	if _, err := approval.Approve(deleteTicket.ApprovalRequestID, auth.HumanPrincipal("bob"), "127.0.0.2"); err != nil {
		t.Fatalf("批准删除失败: %v", err)
	}
	if processed, err := NewApprovalWorker(approval).RunOnce(); err != nil || processed != 1 {
		t.Fatalf("审批 worker 应删除覆盖集：processed=%d err=%v", processed, err)
	}
	if _, err := sets.Get(set.ID); err != apperr.ErrOverrideSetNotFound {
		t.Fatalf("批准后覆盖集应删除：%v", err)
	}
}

func assertPendingFileContentEncrypted(t *testing.T, db *gorm.DB, requestID, content string) {
	t.Helper()
	var req model.ApprovalRequest
	if err := db.Where("request_id = ?", requestID).First(&req).Error; err != nil {
		t.Fatalf("查询审批申请失败: %v", err)
	}
	if strings.Contains(req.Payload, content) || strings.Contains(req.SafeSummary, content) || strings.Contains(req.EvidenceSnapshot, content) {
		t.Fatalf("审批记录泄露文件内容")
	}
	var change model.FilePendingChange
	if err := db.Where("approval_request_id = ?", requestID).First(&change).Error; err != nil {
		t.Fatalf("查询待审批文件变更失败: %v", err)
	}
	if !secret.IsEncrypted(change.Ciphertext) || strings.Contains(change.Ciphertext, content) {
		t.Fatalf("待审批文件内容必须仅以密文保存：%q", change.Ciphertext)
	}
}
