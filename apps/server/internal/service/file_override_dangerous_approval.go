package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/filetree"
	"github.com/wcpe/Beacon/apps/server/internal/gitexport"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/secret"
)

const (
	pendingTargetFile        = "file"
	pendingTargetFileChange  = "file_change"
	pendingTargetOverrideSet = "override_set"
)

// FileApprovalTicket 是文件与覆盖集危险变更的审批申请响应。
type FileApprovalTicket struct {
	ApprovalRequestID string `json:"approvalRequestId"`
	ChangeID          string `json:"changeId"`
	Status            string `json:"status"`
}

type fileApprovalPayload struct {
	SchemaVersion int    `json:"schemaVersion"`
	ChangeID      string `json:"changeId"`
	TargetType    string `json:"targetType"`
	TargetID      uint   `json:"targetId"`
	Expected      int64  `json:"expectedVersion"`
	SHA256        string `json:"sha256"`
}

type filePendingPayload struct {
	Content         string                `json:"content,omitempty"`
	RollbackVersion int64                 `json:"rollbackVersion,omitempty"`
	TargetRoot      string                `json:"targetRoot,omitempty"`
	ReloadCommand   string                `json:"reloadCommand,omitempty"`
	Comment         string                `json:"comment"`
	Operator        string                `json:"operator"`
	ClientIP        string                `json:"clientIp"`
	Create          *CreateFileParams     `json:"create,omitempty"`
	Import          *ImportFilesParams    `json:"import,omitempty"`
	IDs             []uint                `json:"ids,omitempty"`
	Enabled         *bool                 `json:"enabled,omitempty"`
	Expected        []filePendingExpected `json:"expected,omitempty"`
}

type filePendingExpected struct {
	Path    string `json:"path,omitempty"`
	ID      uint   `json:"id,omitempty"`
	Version int64  `json:"version"`
}

func (s *FileService) SetApprovalService(approval *ApprovalService)        { s.approval = approval }
func (s *FileService) SetPendingChangeCipher(cipher *secret.Cipher)        { s.cipher = cipher }
func (s *OverrideSetService) SetApprovalService(approval *ApprovalService) { s.approval = approval }
func (s *OverrideSetService) SetPendingChangeCipher(cipher *secret.Cipher) { s.cipher = cipher }

// RegisterFileOverrideApprovalAdapters 注册文件与覆盖集危险动作的唯一执行入口。
func RegisterFileOverrideApprovalAdapters(registry *authz.ApprovalRegistry, files *FileService, sets *OverrideSetService) {
	if registry == nil || files == nil || sets == nil {
		return
	}
	adapter := fileOverrideApprovalAdapter{files: files, sets: sets}
	for _, kind := range []string{authz.OperationFilePublish, authz.OperationFileRollback, authz.OperationFileDelete, authz.OperationFileCreate, authz.OperationFileImport, authz.OperationFileBatchDelete, authz.OperationFileBatchDisable, authz.OperationFileBatchEnable, authz.OperationOverrideSetPublish, authz.OperationOverrideSetRollback, authz.OperationOverrideSetDelete} {
		registry.RegisterDescriptor(authz.OperationDescriptor{Key: kind, SchemaVersion: approvalSchemaVersion, Capability: auth.CapabilityApprovalRequest, RiskLevel: "high", RequiresTerminalCallback: true}, authz.RequireExecutionReceipt(adapter))
	}
}

func (s *FileService) RequestCreate(p CreateFileParams, reason, key string, principal auth.Principal) (FileApprovalTicket, error) {
	if p.Namespace == "" || p.Operator == "" {
		return FileApprovalTicket{}, apperr.ErrInvalidParam
	}
	cleanPath, err := normalizePath(p.Path)
	if err != nil {
		return FileApprovalTicket{}, err
	}
	group, target, err := normalizeScope(p.ScopeLevel, p.Group, p.ScopeTarget)
	if err != nil {
		return FileApprovalTicket{}, err
	}
	p.Path, p.Group, p.ScopeTarget = cleanPath, group, target
	if err := validateFileContent(p.Path, p.Content); err != nil {
		return FileApprovalTicket{}, err
	}
	if existing, err := s.fileRepo.FindByIdentity(p.Namespace, p.Group, p.Path, p.ScopeLevel, p.ScopeTarget); err != nil || existing != nil {
		if err != nil {
			return FileApprovalTicket{}, err
		}
		return FileApprovalTicket{}, apperr.ErrFileConflict
	}
	pending := filePendingPayload{Create: &p, Operator: p.Operator, Comment: p.Comment, ClientIP: p.ClientIP}
	return s.requestChange(authz.OperationFileCreate, pendingTargetFileChange, 0, 0, pendingHash(pending), pending, reason, key, principal, p.ClientIP)
}

func (s *FileService) RequestImport(p ImportFilesParams, reason, key string, principal auth.Principal) (FileApprovalTicket, error) {
	if p.Namespace == "" || p.Operator == "" || len(p.Files) == 0 {
		return FileApprovalTicket{}, apperr.ErrInvalidParam
	}
	if p.ScopeLevel == "" {
		p.ScopeLevel = model.ScopeGroup
	}
	group, target, err := normalizeScope(p.ScopeLevel, p.Group, p.ScopeTarget)
	if err != nil {
		return FileApprovalTicket{}, err
	}
	p.Group, p.ScopeTarget = group, target
	for i := range p.Files {
		cleanPath, err := normalizePath(p.Files[i].Path)
		if err != nil {
			return FileApprovalTicket{}, err
		}
		if err := validateFileContent(cleanPath, p.Files[i].Content); err != nil {
			return FileApprovalTicket{}, err
		}
		p.Files[i].Path = cleanPath
	}
	expected := make([]filePendingExpected, 0, len(p.Files))
	for _, file := range p.Files {
		existing, err := s.fileRepo.FindByIdentity(p.Namespace, p.Group, file.Path, p.ScopeLevel, p.ScopeTarget)
		if err != nil {
			return FileApprovalTicket{}, err
		}
		item := filePendingExpected{Path: file.Path}
		if existing != nil {
			item.ID, item.Version = existing.ID, existing.Version
		}
		expected = append(expected, item)
	}
	pending := filePendingPayload{Import: &p, Expected: expected, Operator: p.Operator, Comment: p.Comment, ClientIP: p.ClientIP}
	return s.requestChange(authz.OperationFileImport, pendingTargetFileChange, 0, 0, pendingHash(pending), pending, reason, key, principal, p.ClientIP)
}

func (s *FileService) RequestBatchDelete(ids []uint, reason, key, operator, clientIP string, principal auth.Principal) (FileApprovalTicket, error) {
	return s.requestBatchChange(authz.OperationFileBatchDelete, ids, nil, reason, key, operator, clientIP, principal)
}

func (s *FileService) RequestBatchSetEnabled(ids []uint, enabled bool, reason, key, operator, clientIP string, principal auth.Principal) (FileApprovalTicket, error) {
	return s.requestBatchChange(map[bool]string{true: authz.OperationFileBatchEnable, false: authz.OperationFileBatchDisable}[enabled], ids, &enabled, reason, key, operator, clientIP, principal)
}

func (s *FileService) requestBatchChange(kind string, ids []uint, enabled *bool, reason, key, operator, clientIP string, principal auth.Principal) (FileApprovalTicket, error) {
	if operator == "" || len(ids) == 0 {
		return FileApprovalTicket{}, apperr.ErrInvalidParam
	}
	unique := dedupIDs(ids)
	objects, err := s.fileRepo.FindByIDs(unique)
	if err != nil {
		return FileApprovalTicket{}, err
	}
	if len(objects) != len(unique) {
		return FileApprovalTicket{}, apperr.ErrFileNotFound
	}
	expected := make([]filePendingExpected, 0, len(objects))
	for _, item := range objects {
		expected = append(expected, filePendingExpected{ID: item.ID, Version: item.Version})
	}
	pending := filePendingPayload{IDs: unique, Enabled: enabled, Expected: expected, Operator: operator, ClientIP: clientIP}
	return s.requestChange(kind, pendingTargetFileChange, 0, 0, pendingHash(pending), pending, reason, key, principal, clientIP)
}

func (s *FileService) RequestPublish(id uint, content, reason, key, operator, comment, clientIP string, principal auth.Principal) (FileApprovalTicket, error) {
	item, err := s.Get(id)
	if err != nil {
		return FileApprovalTicket{}, err
	}
	if err = validateFileContent(item.Path, content); err != nil {
		return FileApprovalTicket{}, err
	}
	return s.requestChange(authz.OperationFilePublish, pendingTargetFile, id, item.Version, fileHash(content), filePendingPayload{Content: content, Comment: comment, Operator: operator, ClientIP: clientIP}, reason, key, principal, clientIP)
}
func (s *FileService) RequestRollback(id uint, version int64, reason, key, operator, comment, clientIP string, principal auth.Principal) (FileApprovalTicket, error) {
	item, err := s.Get(id)
	if err != nil {
		return FileApprovalTicket{}, err
	}
	rev, err := s.revRepo.FindByObjectAndVersion(id, version)
	if err != nil {
		return FileApprovalTicket{}, err
	}
	if rev == nil {
		return FileApprovalTicket{}, apperr.ErrRevisionNotFound
	}
	return s.requestChange(authz.OperationFileRollback, pendingTargetFile, id, item.Version, fileHash(rev.Content), filePendingPayload{Content: rev.Content, RollbackVersion: version, Comment: comment, Operator: operator, ClientIP: clientIP}, reason, key, principal, clientIP)
}
func (s *FileService) RequestDelete(id uint, reason, key, operator, comment, clientIP string, principal auth.Principal) (FileApprovalTicket, error) {
	item, err := s.Get(id)
	if err != nil {
		return FileApprovalTicket{}, err
	}
	return s.requestChange(authz.OperationFileDelete, pendingTargetFile, id, item.Version, fileHash(fmt.Sprintf("%d:%d", id, item.Version)), filePendingPayload{Comment: comment, Operator: operator, ClientIP: clientIP}, reason, key, principal, clientIP)
}
func (s *OverrideSetService) RequestPublish(id uint, p PublishOverrideSetParams, reason, key string, principal auth.Principal) (FileApprovalTicket, error) {
	set, err := s.Get(id)
	if err != nil {
		return FileApprovalTicket{}, err
	}
	if _, err = filetree.ValidateTargetRoot(p.TargetRoot); err != nil {
		return FileApprovalTicket{}, err
	}
	if _, err = normalizeReloadCommand(p.ReloadCommand); err != nil {
		return FileApprovalTicket{}, err
	}
	return s.requestChange(authz.OperationOverrideSetPublish, id, set.Version, overrideHash(p.TargetRoot, p.ReloadCommand), filePendingPayload{TargetRoot: p.TargetRoot, ReloadCommand: p.ReloadCommand, Comment: p.Comment, Operator: p.Operator, ClientIP: p.ClientIP}, reason, key, principal)
}
func (s *OverrideSetService) RequestRollback(id uint, version int64, reason, key, operator, comment, clientIP string, principal auth.Principal) (FileApprovalTicket, error) {
	set, err := s.Get(id)
	if err != nil {
		return FileApprovalTicket{}, err
	}
	rev, err := s.revRepo.FindBySetAndVersion(id, version)
	if err != nil {
		return FileApprovalTicket{}, err
	}
	if rev == nil {
		return FileApprovalTicket{}, apperr.ErrRevisionNotFound
	}
	return s.requestChange(authz.OperationOverrideSetRollback, id, set.Version, overrideHash(rev.TargetRoot, rev.ReloadCommand), filePendingPayload{TargetRoot: rev.TargetRoot, ReloadCommand: rev.ReloadCommand, RollbackVersion: version, Comment: comment, Operator: operator, ClientIP: clientIP}, reason, key, principal)
}
func (s *OverrideSetService) RequestDelete(id uint, reason, key, operator, comment, clientIP string, principal auth.Principal) (FileApprovalTicket, error) {
	set, err := s.Get(id)
	if err != nil {
		return FileApprovalTicket{}, err
	}
	return s.requestChange(authz.OperationOverrideSetDelete, id, set.Version, fileHash(fmt.Sprintf("%d:%d", id, set.Version)), filePendingPayload{Comment: comment, Operator: operator, ClientIP: clientIP}, reason, key, principal)
}

func (s *FileService) requestChange(kind, target string, id uint, expected int64, hash string, pending filePendingPayload, reason, key string, principal auth.Principal, clientIP string) (FileApprovalTicket, error) {
	if s.approval == nil || s.pending == nil || s.cipher == nil || !s.cipher.IsEnabled() || pending.Operator == "" {
		return FileApprovalTicket{}, apperr.ErrForbidden
	}
	changeID := fileChangeID(principal, kind, target, id, key)
	ciphertext, err := encryptFilePending(s.cipher, pending)
	if err != nil {
		return FileApprovalTicket{}, err
	}
	payload := fileApprovalPayload{SchemaVersion: approvalSchemaVersion, ChangeID: changeID, TargetType: target, TargetID: id, Expected: expected, SHA256: hash}
	req, err := s.approval.request(authz.Operation{Kind: kind, Resource: target, ResourceID: fmt.Sprintf("%d", id), IdempotencyKey: key, Reason: reason, PreconditionSummary: fmt.Sprintf("当前版本为 %d", expected), ImpactSummary: "将变更线上文件状态"}, payloadMap(payload), principal, clientIP, func(tx *gorm.DB, req authz.ApprovalRequest) error {
		return s.pending.WithTx(tx).Create(&model.FilePendingChange{ChangeID: changeID, ApprovalRequestID: req.RequestID, TargetType: target, TargetID: id, ChangeType: kind, ExpectedVersion: expected, ContentSHA256: hash, Ciphertext: ciphertext, Status: model.FilePendingChangePending})
	})
	if err != nil {
		return FileApprovalTicket{}, err
	}
	return FileApprovalTicket{ApprovalRequestID: req.RequestID, ChangeID: changeID, Status: req.Status}, nil
}
func (s *OverrideSetService) requestChange(kind string, id uint, expected int64, hash string, pending filePendingPayload, reason, key string, principal auth.Principal) (FileApprovalTicket, error) {
	if s.approval == nil || s.pending == nil || s.cipher == nil || !s.cipher.IsEnabled() || pending.Operator == "" {
		return FileApprovalTicket{}, apperr.ErrForbidden
	}
	changeID := fileChangeID(principal, kind, pendingTargetOverrideSet, id, key)
	ciphertext, err := encryptFilePending(s.cipher, pending)
	if err != nil {
		return FileApprovalTicket{}, err
	}
	payload := fileApprovalPayload{SchemaVersion: approvalSchemaVersion, ChangeID: changeID, TargetType: pendingTargetOverrideSet, TargetID: id, Expected: expected, SHA256: hash}
	req, err := s.approval.request(authz.Operation{Kind: kind, Resource: pendingTargetOverrideSet, ResourceID: fmt.Sprintf("%d", id), IdempotencyKey: key, Reason: reason, PreconditionSummary: fmt.Sprintf("当前版本为 %d", expected), ImpactSummary: "将变更线上覆盖集状态"}, payloadMap(payload), principal, pending.ClientIP, func(tx *gorm.DB, req authz.ApprovalRequest) error {
		return s.pending.WithTx(tx).Create(&model.FilePendingChange{ChangeID: changeID, ApprovalRequestID: req.RequestID, TargetType: pendingTargetOverrideSet, TargetID: id, ChangeType: kind, ExpectedVersion: expected, ContentSHA256: hash, Ciphertext: ciphertext, Status: model.FilePendingChangePending})
	})
	if err != nil {
		return FileApprovalTicket{}, err
	}
	return FileApprovalTicket{ApprovalRequestID: req.RequestID, ChangeID: changeID, Status: req.Status}, nil
}

type fileOverrideApprovalAdapter struct {
	files *FileService
	sets  *OverrideSetService
}

func (fileOverrideApprovalAdapter) Execute(authz.ApprovalRequest, authz.Permit) error {
	return apperr.ErrForbidden
}
func (a fileOverrideApprovalAdapter) ExecuteInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
	p, change, pending, err := a.load(tx, req, permit)
	if err != nil {
		return nil, err
	}
	fileBefore, _ := a.files.GetInTx(tx, p.TargetID)
	setBefore, _ := a.sets.setRepo.WithTx(tx).FindByID(p.TargetID)
	var result string
	var after func()
	switch req.Operation.Kind {
	case authz.OperationFileCreate:
		var item *model.FileObject
		item, err = a.applyFileCreate(tx, pending)
		if item != nil {
			result = fmt.Sprintf("%d", item.ID)
			after = func() { a.files.notify(item); a.files.exportGit(item, model.ActionFileCreate, pending.Operator) }
		}
	case authz.OperationFileImport:
		var items []model.FileObject
		items, err = a.applyFileImport(tx, pending)
		result = fmt.Sprintf("%d", len(items))
		if pending.Import != nil {
			imported := *pending.Import
			after = func() {
				if a.files.notifier != nil {
					a.files.notifier.NotifyFileChange(imported.Namespace, imported.ScopeLevel, imported.Group, imported.ScopeTarget)
				}
				if a.files.exporter != nil {
					a.files.exporter.ExportAsync(gitexport.ExportMeta{Operator: imported.Operator, Action: model.ActionFileImport, Target: fmt.Sprintf("%s/%s@%s:%s", imported.Namespace, imported.Group, imported.ScopeLevel, imported.ScopeTarget)})
				}
			}
		}
	case authz.OperationFileBatchDelete, authz.OperationFileBatchDisable, authz.OperationFileBatchEnable:
		var items []model.FileObject
		items, err = a.applyFileBatch(tx, req.Operation.Kind, pending)
		result = fmt.Sprintf("%d", len(items))
		if err == nil {
			action := map[string]string{authz.OperationFileBatchDelete: model.ActionFileDelete, authz.OperationFileBatchDisable: model.ActionFileDisable, authz.OperationFileBatchEnable: model.ActionFileEnable}[req.Operation.Kind]
			after = func() {
				for i := range items {
					a.files.notify(&items[i])
					a.files.exportGit(&items[i], action, pending.Operator)
				}
			}
		}
	case authz.OperationFilePublish:
		result, err = a.applyFilePublish(tx, p, pending)
	case authz.OperationFileRollback:
		result, err = a.applyFileRollback(tx, p, pending)
	case authz.OperationFileDelete:
		result, err = a.applyFileDelete(tx, p, pending)
	case authz.OperationOverrideSetPublish:
		result, err = a.applyOverridePublish(tx, p, pending)
	case authz.OperationOverrideSetRollback:
		result, err = a.applyOverrideRollback(tx, p, pending)
	case authz.OperationOverrideSetDelete:
		result, err = a.applyOverrideDelete(tx, p, pending)
	default:
		return nil, apperr.ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	ok, err := a.files.pending.WithTx(tx).ApplyCAS(change.ChangeID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.ErrApprovalTargetChanged
	}
	if err := tx.Create(&model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash, ResultRef: result}).Error; err != nil {
		return nil, err
	}
	if after == nil {
		after = a.afterCommit(req.Operation.Kind, p.TargetID, pending.Operator, fileBefore, setBefore)
	}
	return after, nil
}

func (a fileOverrideApprovalAdapter) afterCommit(operation string, targetID uint, operator string, fileBefore *model.FileObject, setBefore *model.FileOverrideSet) func() {
	return func() {
		switch operation {
		case authz.OperationFilePublish, authz.OperationFileRollback:
			action := model.ActionFilePublish
			if operation == authz.OperationFileRollback {
				action = model.ActionFileRollback
			}
			if item, err := a.files.Get(targetID); err == nil {
				a.files.notify(item)
				a.files.exportGit(item, action, operator)
			}
		case authz.OperationFileDelete:
			if fileBefore != nil {
				a.files.notify(fileBefore)
				a.files.exportGit(fileBefore, model.ActionFileDelete, operator)
			}
		case authz.OperationOverrideSetPublish, authz.OperationOverrideSetRollback:
			if set, err := a.sets.Get(targetID); err == nil {
				a.sets.notify(set)
			}
		case authz.OperationOverrideSetDelete:
			if setBefore != nil {
				a.sets.notify(setBefore)
			}
		}
	}
}
func (a fileOverrideApprovalAdapter) CompleteTerminalInTx(tx *gorm.DB, req authz.ApprovalRequest, _ string) error {
	return a.files.pending.WithTx(tx).InvalidateByApprovalRequest(req.RequestID)
}
func (a fileOverrideApprovalAdapter) load(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (fileApprovalPayload, *model.FilePendingChange, filePendingPayload, error) {
	var p fileApprovalPayload
	if json.Unmarshal(req.Payload, &p) != nil || p.SchemaVersion != approvalSchemaVersion || p.ChangeID == "" || len(p.SHA256) != 64 || ensurePermit(permit, req.Operation.Kind) != nil {
		return p, nil, filePendingPayload{}, apperr.ErrApprovalTargetChanged
	}
	change, err := a.files.pending.WithTx(tx).FindByApprovalRequest(req.RequestID)
	if err != nil || change == nil {
		if err != nil {
			return p, nil, filePendingPayload{}, err
		}
		return p, nil, filePendingPayload{}, apperr.ErrApprovalTargetChanged
	}
	if change.Status != model.FilePendingChangePending || change.ChangeID != p.ChangeID || change.TargetType != p.TargetType || change.TargetID != p.TargetID || change.ChangeType != req.Operation.Kind || change.ExpectedVersion != p.Expected || change.ContentSHA256 != p.SHA256 {
		return p, nil, filePendingPayload{}, apperr.ErrApprovalTargetChanged
	}
	plain, err := a.files.cipher.Decrypt(change.Ciphertext)
	if err != nil {
		return p, nil, filePendingPayload{}, err
	}
	var pending filePendingPayload
	if json.Unmarshal([]byte(plain), &pending) != nil || pending.Operator == "" || (requiresPendingHash(req.Operation.Kind) && pendingHash(pending) != p.SHA256) {
		return p, nil, filePendingPayload{}, apperr.ErrApprovalTargetChanged
	}
	return p, change, pending, nil
}

func (a fileOverrideApprovalAdapter) applyFileCreate(tx *gorm.DB, pending filePendingPayload) (*model.FileObject, error) {
	if pending.Create == nil {
		return nil, apperr.ErrApprovalTargetChanged
	}
	p := *pending.Create
	if p.Operator != pending.Operator || p.ClientIP != pending.ClientIP {
		return nil, apperr.ErrApprovalTargetChanged
	}
	if err := validateFileContent(p.Path, p.Content); err != nil {
		return nil, err
	}
	existing, err := a.files.fileRepo.WithTx(tx).FindByIdentity(p.Namespace, p.Group, p.Path, p.ScopeLevel, p.ScopeTarget)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, apperr.ErrApprovalTargetChanged
	}
	md5 := filetree.ContentMD5(p.Content)
	item := &model.FileObject{NamespaceCode: p.Namespace, GroupCode: p.Group, Path: p.Path, ScopeLevel: p.ScopeLevel, ScopeTarget: p.ScopeTarget, Content: p.Content, ContentMD5: md5, Version: 1, Enabled: true, WholeFileOverride: p.WholeFileOverride, SensitiveExcluded: p.SensitiveExcluded}
	if err := a.files.fileRepo.WithTx(tx).Create(item); err != nil {
		return nil, err
	}
	rev, err := a.files.appendRevision(tx, item.ID, 1, item.Content, md5, nil, p.Operator, p.Comment)
	if err != nil {
		return nil, err
	}
	item.CurrentRevision = rev.ID
	if err := a.files.fileRepo.WithTx(tx).Save(item); err != nil {
		return nil, err
	}
	if err := a.files.writeAudit(tx, item, p.Operator, model.ActionFileCreate, fmt.Sprintf(`{"version":1,"md5":"%s"}`, md5), p.ClientIP); err != nil {
		return nil, err
	}
	return item, nil
}

func (a fileOverrideApprovalAdapter) applyFileImport(tx *gorm.DB, pending filePendingPayload) ([]model.FileObject, error) {
	if pending.Import == nil || len(pending.Import.Files) == 0 || len(pending.Expected) != len(pending.Import.Files) {
		return nil, apperr.ErrApprovalTargetChanged
	}
	p := *pending.Import
	items := make([]model.FileObject, 0, len(p.Files))
	for index, file := range p.Files {
		existing, err := a.files.fileRepo.WithTx(tx).FindByIdentity(p.Namespace, p.Group, file.Path, p.ScopeLevel, p.ScopeTarget)
		if err != nil {
			return nil, err
		}
		expected := pending.Expected[index]
		if expected.Path != file.Path || (existing == nil && expected.ID != 0) || (existing != nil && (existing.ID != expected.ID || existing.Version != expected.Version)) {
			return nil, apperr.ErrApprovalTargetChanged
		}
		md5 := filetree.ContentMD5(file.Content)
		if existing == nil {
			item := model.FileObject{NamespaceCode: p.Namespace, GroupCode: p.Group, Path: file.Path, ScopeLevel: p.ScopeLevel, ScopeTarget: p.ScopeTarget, Content: file.Content, ContentMD5: md5, Version: 1, Enabled: true}
			if err := a.files.fileRepo.WithTx(tx).Create(&item); err != nil {
				return nil, err
			}
			rev, err := a.files.appendRevision(tx, item.ID, 1, item.Content, md5, nil, p.Operator, p.Comment)
			if err != nil {
				return nil, err
			}
			item.CurrentRevision = rev.ID
			if err := a.files.fileRepo.WithTx(tx).Save(&item); err != nil {
				return nil, err
			}
			items = append(items, item)
			continue
		}
		rev, err := a.files.appendRevision(tx, existing.ID, existing.Version+1, file.Content, md5, nil, p.Operator, p.Comment)
		if err != nil {
			return nil, err
		}
		existing.Content, existing.ContentMD5, existing.Version, existing.CurrentRevision = file.Content, md5, existing.Version+1, rev.ID
		if err := a.files.fileRepo.WithTx(tx).Save(existing); err != nil {
			return nil, err
		}
		items = append(items, *existing)
	}
	if err := a.files.writeImportAudit(tx, p.Namespace, p.Group, p.Operator, fmt.Sprintf(`{"scope":%q,"target":%q,"files":%d}`, p.ScopeLevel, p.ScopeTarget, len(p.Files)), p.ClientIP); err != nil {
		return nil, err
	}
	return items, nil
}

func (a fileOverrideApprovalAdapter) applyFileBatch(tx *gorm.DB, kind string, pending filePendingPayload) ([]model.FileObject, error) {
	if len(pending.IDs) == 0 || len(pending.IDs) != len(pending.Expected) {
		return nil, apperr.ErrApprovalTargetChanged
	}
	items := make([]model.FileObject, 0, len(pending.IDs))
	for index, id := range pending.IDs {
		item, err := a.files.GetInTx(tx, id)
		if err != nil {
			return nil, err
		}
		expected := pending.Expected[index]
		if expected.ID != item.ID || expected.Version != item.Version {
			return nil, apperr.ErrApprovalTargetChanged
		}
		action, detail := model.ActionFileDelete, `{"deleted":true}`
		switch kind {
		case authz.OperationFileBatchDelete:
			err = a.files.fileRepo.WithTx(tx).SoftDelete(item.ID, time.Now().UTC())
		case authz.OperationFileBatchDisable, authz.OperationFileBatchEnable:
			if pending.Enabled == nil || *pending.Enabled != (kind == authz.OperationFileBatchEnable) {
				return nil, apperr.ErrApprovalTargetChanged
			}
			action, detail = model.ActionFileDisable, `{"enabled":false}`
			if *pending.Enabled {
				action, detail = model.ActionFileEnable, `{"enabled":true}`
			}
			err = a.files.fileRepo.WithTx(tx).SetEnabled(item.ID, *pending.Enabled)
		}
		if err != nil {
			return nil, err
		}
		if err := a.files.writeAudit(tx, item, pending.Operator, action, detail, pending.ClientIP); err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, nil
}

func (a fileOverrideApprovalAdapter) applyFilePublish(tx *gorm.DB, p fileApprovalPayload, v filePendingPayload) (string, error) {
	obj, err := a.files.GetInTx(tx, p.TargetID)
	if err != nil || obj.Version != p.Expected || fileHash(v.Content) != p.SHA256 {
		return "", apperr.ErrApprovalTargetChanged
	}
	if err = validateFileContent(obj.Path, v.Content); err != nil {
		return "", err
	}
	rev, err := a.files.appendRevision(tx, obj.ID, obj.Version+1, v.Content, filetree.ContentMD5(v.Content), nil, v.Operator, v.Comment)
	if err != nil {
		return "", err
	}
	obj.Content, obj.ContentMD5, obj.Version, obj.CurrentRevision = v.Content, filetree.ContentMD5(v.Content), obj.Version+1, rev.ID
	if err = a.files.fileRepo.WithTx(tx).Save(obj); err != nil {
		return "", err
	}
	if err = a.files.writeAudit(tx, obj, v.Operator, model.ActionFilePublish, fmt.Sprintf(`{"version":%d,"md5":"%s"}`, obj.Version, obj.ContentMD5), v.ClientIP); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", obj.ID), nil
}
func (a fileOverrideApprovalAdapter) applyFileRollback(tx *gorm.DB, p fileApprovalPayload, v filePendingPayload) (string, error) {
	obj, err := a.files.GetInTx(tx, p.TargetID)
	if err != nil || obj.Version != p.Expected {
		return "", apperr.ErrApprovalTargetChanged
	}
	target, err := a.files.revRepo.WithTx(tx).FindByObjectAndVersion(obj.ID, v.RollbackVersion)
	if err != nil || target == nil || target.Content != v.Content || fileHash(v.Content) != p.SHA256 {
		return "", apperr.ErrApprovalTargetChanged
	}
	src := target.ID
	rev, err := a.files.appendRevision(tx, obj.ID, obj.Version+1, v.Content, target.ContentMD5, &src, v.Operator, v.Comment)
	if err != nil {
		return "", err
	}
	obj.Content, obj.ContentMD5, obj.Version, obj.CurrentRevision = v.Content, target.ContentMD5, obj.Version+1, rev.ID
	if err = a.files.fileRepo.WithTx(tx).Save(obj); err != nil {
		return "", err
	}
	if err = a.files.writeAudit(tx, obj, v.Operator, model.ActionFileRollback, fmt.Sprintf(`{"version":%d,"fromVersion":%d,"md5":"%s"}`, obj.Version, v.RollbackVersion, obj.ContentMD5), v.ClientIP); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", obj.ID), nil
}
func (a fileOverrideApprovalAdapter) applyFileDelete(tx *gorm.DB, p fileApprovalPayload, v filePendingPayload) (string, error) {
	obj, err := a.files.GetInTx(tx, p.TargetID)
	if err != nil || obj.Version != p.Expected || fileHash(fmt.Sprintf("%d:%d", obj.ID, obj.Version)) != p.SHA256 {
		return "", apperr.ErrApprovalTargetChanged
	}
	if err = a.files.fileRepo.WithTx(tx).SoftDelete(obj.ID, time.Now().UTC()); err != nil {
		return "", err
	}
	if err = a.files.writeAudit(tx, obj, v.Operator, model.ActionFileDelete, `{"deleted":true}`, v.ClientIP); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", obj.ID), nil
}
func (a fileOverrideApprovalAdapter) applyOverridePublish(tx *gorm.DB, p fileApprovalPayload, v filePendingPayload) (string, error) {
	set, err := a.sets.setRepo.WithTx(tx).FindByID(p.TargetID)
	if err != nil || set == nil || set.Version != p.Expected || overrideHash(v.TargetRoot, v.ReloadCommand) != p.SHA256 {
		return "", apperr.ErrApprovalTargetChanged
	}
	root, err := filetree.ValidateTargetRoot(v.TargetRoot)
	if err != nil {
		return "", err
	}
	cmd, err := normalizeReloadCommand(v.ReloadCommand)
	if err != nil {
		return "", err
	}
	paths, err := a.sets.memberPathList(set)
	if err != nil {
		return "", err
	}
	rev, err := a.sets.appendRevision(tx, set.ID, set.Version+1, root, cmd, strings.Join(paths, "\n"), nil, v.Operator, v.Comment)
	if err != nil {
		return "", err
	}
	set.TargetRoot, set.ReloadCommand, set.Version, set.CurrentRevision = root, cmd, set.Version+1, rev.ID
	if err = a.sets.setRepo.WithTx(tx).Save(set); err != nil {
		return "", err
	}
	if err = a.sets.writeAudit(tx, set, v.Operator, model.ActionOverrideSetPublish, fmt.Sprintf(`{"version":%d,"targetRoot":%q,"hasCommand":%t}`, set.Version, root, cmd != ""), v.ClientIP); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", set.ID), nil
}
func (a fileOverrideApprovalAdapter) applyOverrideRollback(tx *gorm.DB, p fileApprovalPayload, v filePendingPayload) (string, error) {
	set, err := a.sets.setRepo.WithTx(tx).FindByID(p.TargetID)
	if err != nil || set == nil || set.Version != p.Expected {
		return "", apperr.ErrApprovalTargetChanged
	}
	target, err := a.sets.revRepo.WithTx(tx).FindBySetAndVersion(set.ID, v.RollbackVersion)
	if err != nil || target == nil || target.TargetRoot != v.TargetRoot || target.ReloadCommand != v.ReloadCommand || overrideHash(v.TargetRoot, v.ReloadCommand) != p.SHA256 {
		return "", apperr.ErrApprovalTargetChanged
	}
	src := target.ID
	rev, err := a.sets.appendRevision(tx, set.ID, set.Version+1, target.TargetRoot, target.ReloadCommand, target.MemberPaths, &src, v.Operator, v.Comment)
	if err != nil {
		return "", err
	}
	set.TargetRoot, set.ReloadCommand, set.Version, set.CurrentRevision = target.TargetRoot, target.ReloadCommand, set.Version+1, rev.ID
	if err = a.sets.setRepo.WithTx(tx).Save(set); err != nil {
		return "", err
	}
	if err = a.sets.writeAudit(tx, set, v.Operator, model.ActionOverrideSetRollback, fmt.Sprintf(`{"version":%d,"fromVersion":%d,"targetRoot":%q}`, set.Version, v.RollbackVersion, set.TargetRoot), v.ClientIP); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", set.ID), nil
}
func (a fileOverrideApprovalAdapter) applyOverrideDelete(tx *gorm.DB, p fileApprovalPayload, v filePendingPayload) (string, error) {
	set, err := a.sets.setRepo.WithTx(tx).FindByID(p.TargetID)
	if err != nil || set == nil || set.Version != p.Expected || fileHash(fmt.Sprintf("%d:%d", set.ID, set.Version)) != p.SHA256 {
		return "", apperr.ErrApprovalTargetChanged
	}
	if err = a.sets.setRepo.WithTx(tx).SoftDelete(set.ID, time.Now().UTC()); err != nil {
		return "", err
	}
	if err = a.sets.writeAudit(tx, set, v.Operator, model.ActionOverrideSetDelete, `{"deleted":true}`, v.ClientIP); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", set.ID), nil
}

func encryptFilePending(cipher *secret.Cipher, p filePendingPayload) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return cipher.Encrypt(string(raw))
}
func payloadMap(p fileApprovalPayload) map[string]any {
	return map[string]any{"schemaVersion": p.SchemaVersion, "changeId": p.ChangeID, "targetType": p.TargetType, "targetId": p.TargetID, "expectedVersion": p.Expected, "sha256": p.SHA256}
}
func fileHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func pendingHash(p filePendingPayload) string {
	raw, _ := json.Marshal(p)
	return fileHash(string(raw))
}

func requiresPendingHash(kind string) bool {
	switch kind {
	case authz.OperationFileCreate, authz.OperationFileImport, authz.OperationFileBatchDelete, authz.OperationFileBatchDisable, authz.OperationFileBatchEnable:
		return true
	default:
		return false
	}
}
func overrideHash(root, command string) string { return fileHash(root + "\x00" + command) }
func fileChangeID(principal auth.Principal, kind, target string, id uint, key string) string {
	p := auth.NormalizePrincipal(principal)
	return "filechg_" + fileHash(fmt.Sprintf("%s|%s|%s|%s|%d|%s", p.StableKind(), p.StableID(), kind, target, id, key))
}
