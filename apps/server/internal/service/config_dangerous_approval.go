package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/merge"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// ConfigApprovalTicket 是危险配置变更的最小审批申请响应。
type ConfigApprovalTicket struct {
	ApprovalRequestID string `json:"approvalRequestId"`
	ChangeID          string `json:"changeId"`
	Status            string `json:"status"`
}

type configApprovalPayload struct {
	SchemaVersion       int    `json:"schemaVersion"`
	ChangeID            string `json:"changeId"`
	ConfigItemID        uint   `json:"configItemId"`
	ExpectedVersion     int64  `json:"expectedVersion"`
	ContentSHA256       string `json:"sha256"`
	ExpectedGrayVersion int64  `json:"expectedGrayVersion"`
}

type configPendingPayload struct {
	Content         string                  `json:"content"`
	RollbackVersion int64                   `json:"rollbackVersion"`
	Comment         string                  `json:"comment"`
	Operator        string                  `json:"operator"`
	ClientIP        string                  `json:"clientIp"`
	Cohort          string                  `json:"cohort"`
	IDs             []uint                  `json:"ids,omitempty"`
	Enabled         *bool                   `json:"enabled,omitempty"`
	Expected        []configPendingExpected `json:"expected,omitempty"`
}

type configPendingExpected struct {
	ID      uint   `json:"id"`
	Version int64  `json:"version"`
	Enabled bool   `json:"enabled"`
	Hash    string `json:"hash"`
}

// RegisterConfigApprovalAdapters 注册配置发布和回滚的唯一执行入口。
func RegisterConfigApprovalAdapters(registry *authz.ApprovalRegistry, svc *ConfigService) {
	if registry == nil || svc == nil {
		return
	}
	adapter := configApprovalAdapter{svc: svc}
	for _, kind := range []string{authz.OperationConfigPublish, authz.OperationConfigRollback, authz.OperationConfigGrayPublish, authz.OperationConfigGrayPromote, authz.OperationConfigDelete, authz.OperationConfigBatchDelete, authz.OperationConfigBatchDisable, authz.OperationConfigBatchEnable} {
		registry.RegisterDescriptor(authz.OperationDescriptor{Key: kind, SchemaVersion: approvalSchemaVersion, Capability: auth.CapabilityApprovalRequest, RiskLevel: "high", RequiresTerminalCallback: true}, authz.RequireExecutionReceipt(adapter))
	}
}

// RequestDelete 冻结当前配置版本与摘要，待审批后再软删。
func (s *ConfigService) RequestDelete(id uint, reason, idempotencyKey, operator, comment, clientIP string, principal auth.Principal) (ConfigApprovalTicket, error) {
	item, err := s.Get(id)
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	pending := configPendingPayload{Operator: operator, Comment: comment, ClientIP: clientIP}
	return s.requestConfigChange(authz.OperationConfigDelete, id, item.Version, configItemHash(item), model.ConfigPendingChangeDelete, pending, reason, idempotencyKey, principal, clientIP)
}

// RequestBatchDelete 冻结所有目标的 id、版本与状态摘要，待审批后原子软删。
func (s *ConfigService) RequestBatchDelete(ids []uint, reason, idempotencyKey, operator, clientIP string, principal auth.Principal) (ConfigApprovalTicket, error) {
	return s.requestConfigBatchChange(authz.OperationConfigBatchDelete, ids, nil, reason, idempotencyKey, operator, clientIP, principal)
}

// RequestBatchSetEnabled 冻结所有目标的 id、版本与状态摘要，待审批后原子置启用态。
func (s *ConfigService) RequestBatchSetEnabled(ids []uint, enabled bool, reason, idempotencyKey, operator, clientIP string, principal auth.Principal) (ConfigApprovalTicket, error) {
	kind := authz.OperationConfigBatchDisable
	if enabled {
		kind = authz.OperationConfigBatchEnable
	}
	return s.requestConfigBatchChange(kind, ids, &enabled, reason, idempotencyKey, operator, clientIP, principal)
}

func (s *ConfigService) requestConfigBatchChange(kind string, ids []uint, enabled *bool, reason, idempotencyKey, operator, clientIP string, principal auth.Principal) (ConfigApprovalTicket, error) {
	if operator == "" || len(ids) == 0 {
		return ConfigApprovalTicket{}, apperr.ErrInvalidParam
	}
	uniqueIDs := normalizeConfigBatchIDs(ids)
	items, err := s.configRepo.FindByIDs(uniqueIDs)
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	if len(items) != len(uniqueIDs) {
		return ConfigApprovalTicket{}, apperr.ErrConfigNotFound
	}
	expected := make([]configPendingExpected, 0, len(uniqueIDs))
	byID := make(map[uint]*model.ConfigItem, len(items))
	for i := range items {
		byID[items[i].ID] = &items[i]
	}
	for _, id := range uniqueIDs {
		item := byID[id]
		expected = append(expected, configPendingExpected{ID: item.ID, Version: item.Version, Enabled: item.Enabled, Hash: configItemHash(item)})
	}
	namespaceCode := items[0].NamespaceCode
	for _, item := range items[1:] {
		if item.NamespaceCode != namespaceCode {
			return ConfigApprovalTicket{}, apperr.ErrApprovalCrossNamespace
		}
	}
	pending := configPendingPayload{IDs: uniqueIDs, Enabled: enabled, Expected: expected, Operator: operator, ClientIP: clientIP}
	changeType := map[string]string{authz.OperationConfigBatchDelete: model.ConfigPendingChangeBatchDelete, authz.OperationConfigBatchDisable: model.ConfigPendingChangeBatchDisable, authz.OperationConfigBatchEnable: model.ConfigPendingChangeBatchEnable}[kind]
	return s.requestConfigBatch(kind, namespaceCode, uniqueIDs, expected, configPendingHash(pending), changeType, pending, reason, idempotencyKey, principal, clientIP)
}

// normalizeConfigBatchIDs 将批量目标去重并按稳定主键升序冻结，确保同一集合有同一审批哈希。
func normalizeConfigBatchIDs(ids []uint) []uint {
	unique := dedupIDs(ids)
	sort.Slice(unique, func(i, j int) bool { return unique[i] < unique[j] })
	return unique
}

func (s *ConfigService) requestConfigBatch(kind, namespaceCode string, ids []uint, expected []configPendingExpected, hash, changeType string, pending configPendingPayload, reason, idempotencyKey string, principal auth.Principal, clientIP string) (ConfigApprovalTicket, error) {
	if pending.Operator == "" || s.approval == nil || s.pending == nil || s.cipher == nil || !s.cipher.IsEnabled() {
		return ConfigApprovalTicket{}, apperr.ErrForbidden
	}
	changeID := configApprovalChangeID(principal, kind, 0, idempotencyKey)
	ciphertext, err := s.encryptPendingPayload(pending)
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	resourceID := configBatchResourceID(ids)
	payload := configApprovalPayload{SchemaVersion: approvalSchemaVersion, ChangeID: changeID, ContentSHA256: hash}
	request, err := s.approval.request(authz.Operation{
		Kind: kind, Resource: "config_item_batch", ResourceID: resourceID, IdempotencyKey: idempotencyKey, Reason: reason,
		PreconditionSummary: fmt.Sprintf("命名空间 %s 的 %d 个配置项版本与状态保持冻结值", namespaceCode, len(ids)),
		ImpactSummary:       fmt.Sprintf("%s命名空间 %s 的 %d 个配置项", configBatchImpactVerb(kind), namespaceCode, len(ids)),
		EvidenceSnapshot:    configBatchEvidence(namespaceCode, ids, expected),
	}, configApprovalPayloadMap(payload), principal, clientIP, func(tx *gorm.DB, req authz.ApprovalRequest) error {
		return s.pending.WithTx(tx).Create(&model.ConfigPendingChange{ChangeID: changeID, ApprovalRequestID: req.RequestID, ChangeType: changeType, ContentSHA256: hash, Ciphertext: ciphertext, Status: model.ConfigPendingChangePending})
	})
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	return ConfigApprovalTicket{ApprovalRequestID: request.RequestID, ChangeID: changeID, Status: request.Status}, nil
}

func configBatchResourceID(ids []uint) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d", id)
	}
	return "batch:" + strings.Join(parts, ",")
}

func configBatchImpactVerb(kind string) string {
	return map[string]string{
		authz.OperationConfigBatchDelete:  "删除",
		authz.OperationConfigBatchDisable: "禁用",
		authz.OperationConfigBatchEnable:  "启用",
	}[kind]
}

func configBatchEvidence(namespaceCode string, ids []uint, expected []configPendingExpected) []authz.ApprovalEvidenceLine {
	lines := []authz.ApprovalEvidenceLine{
		{Label: "命名空间", Value: namespaceCode},
		{Label: "配置项 ID", Value: strings.TrimPrefix(configBatchResourceID(ids), "batch:")},
	}
	for _, item := range expected {
		state := "已禁用"
		if item.Enabled {
			state = "已启用"
		}
		lines = append(lines, authz.ApprovalEvidenceLine{Label: fmt.Sprintf("配置项 %d", item.ID), Value: fmt.Sprintf("版本=%d，%s，摘要=%s", item.Version, state, item.Hash[:12])})
	}
	return lines
}

func (s *ConfigService) requestConfigChange(kind string, itemID uint, expectedVersion int64, hash, changeType string, pending configPendingPayload, reason, idempotencyKey string, principal auth.Principal, clientIP string) (ConfigApprovalTicket, error) {
	if pending.Operator == "" || s.approval == nil || s.pending == nil || s.cipher == nil || !s.cipher.IsEnabled() {
		return ConfigApprovalTicket{}, apperr.ErrForbidden
	}
	changeID := configApprovalChangeID(principal, kind, itemID, idempotencyKey)
	ciphertext, err := s.encryptPendingPayload(pending)
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	payload := configApprovalPayload{SchemaVersion: approvalSchemaVersion, ChangeID: changeID, ConfigItemID: itemID, ExpectedVersion: expectedVersion, ContentSHA256: hash}
	request, err := s.approval.request(authz.Operation{Kind: kind, Resource: "config_item", ResourceID: fmt.Sprintf("%d", itemID), IdempotencyKey: idempotencyKey, Reason: reason, PreconditionSummary: "执行前校验冻结配置目标", ImpactSummary: "变更线上配置状态"}, configApprovalPayloadMap(payload), principal, clientIP, func(tx *gorm.DB, req authz.ApprovalRequest) error {
		return s.pending.WithTx(tx).Create(&model.ConfigPendingChange{ChangeID: changeID, ApprovalRequestID: req.RequestID, ConfigItemID: itemID, ChangeType: changeType, ExpectedVersion: expectedVersion, ContentSHA256: hash, Ciphertext: ciphertext, Status: model.ConfigPendingChangePending})
	})
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	return ConfigApprovalTicket{ApprovalRequestID: request.RequestID, ChangeID: changeID, Status: request.Status}, nil
}

// RequestGrayPublish 冻结加密的灰度内容和目标名单，等待审批执行。
func (s *ConfigService) RequestGrayPublish(id uint, content string, cohort []string, reason, idempotencyKey, operator, comment, clientIP string, principal auth.Principal) (ConfigApprovalTicket, error) {
	item, err := s.Get(id)
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	if operator == "" || s.approval == nil || s.pending == nil || s.cipher == nil || !s.cipher.IsEnabled() || s.gray == nil {
		return ConfigApprovalTicket{}, apperr.ErrForbidden
	}
	if err := validateContent(item.Format, content); err != nil {
		return ConfigApprovalTicket{}, err
	}
	encodedCohort, err := encodeCohort(cohort)
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	contentHash := configContentHash(content)
	changeID := configApprovalChangeID(principal, authz.OperationConfigGrayPublish, id, idempotencyKey)
	ciphertext, err := s.encryptPendingPayload(configPendingPayload{Content: content, Cohort: encodedCohort, Comment: comment, Operator: operator, ClientIP: clientIP})
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	payload := configApprovalPayload{SchemaVersion: approvalSchemaVersion, ChangeID: changeID, ConfigItemID: id, ExpectedVersion: item.Version, ExpectedGrayVersion: item.GrayVersion, ContentSHA256: contentHash}
	request, err := s.approval.request(authz.Operation{Kind: authz.OperationConfigGrayPublish, Resource: "config_item", ResourceID: fmt.Sprintf("%d", id), IdempotencyKey: idempotencyKey, Reason: reason, PreconditionSummary: fmt.Sprintf("配置版本为 %d，灰度版本为 %d", item.Version, item.GrayVersion), ImpactSummary: fmt.Sprintf("向 %d 台服务器发布灰度配置", len(decodeMembers(encodedCohort)))}, configApprovalPayloadMap(payload), principal, clientIP, func(tx *gorm.DB, req authz.ApprovalRequest) error {
		return s.pending.WithTx(tx).Create(&model.ConfigPendingChange{ChangeID: changeID, ApprovalRequestID: req.RequestID, ConfigItemID: id, ChangeType: model.ConfigPendingChangeGrayPublish, ExpectedVersion: item.Version, ExpectedGrayVersion: item.GrayVersion, ContentSHA256: contentHash, Ciphertext: ciphertext, Status: model.ConfigPendingChangePending})
	})
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	return ConfigApprovalTicket{ApprovalRequestID: request.RequestID, ChangeID: changeID, Status: request.Status}, nil
}

// RequestGrayPromote 冻结当前灰度内容和版本，等待审批后晋升为稳定版本。
func (s *ConfigService) RequestGrayPromote(id uint, reason, idempotencyKey, operator, comment, clientIP string, principal auth.Principal) (ConfigApprovalTicket, error) {
	item, err := s.Get(id)
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	if operator == "" || s.approval == nil || s.pending == nil || s.cipher == nil || !s.cipher.IsEnabled() || s.gray == nil {
		return ConfigApprovalTicket{}, apperr.ErrForbidden
	}
	gray, err := s.gray.grayRepo.FindActiveByItem(id)
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	if gray == nil {
		return ConfigApprovalTicket{}, apperr.ErrGrayNotFound
	}
	contentHash := configContentHash(gray.Content)
	changeID := configApprovalChangeID(principal, authz.OperationConfigGrayPromote, id, idempotencyKey)
	ciphertext, err := s.encryptPendingPayload(configPendingPayload{Content: gray.Content, Cohort: gray.Cohort, Comment: comment, Operator: operator, ClientIP: clientIP})
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	payload := configApprovalPayload{SchemaVersion: approvalSchemaVersion, ChangeID: changeID, ConfigItemID: id, ExpectedVersion: item.Version, ExpectedGrayVersion: item.GrayVersion, ContentSHA256: contentHash}
	request, err := s.approval.request(authz.Operation{Kind: authz.OperationConfigGrayPromote, Resource: "config_item", ResourceID: fmt.Sprintf("%d", id), IdempotencyKey: idempotencyKey, Reason: reason, PreconditionSummary: fmt.Sprintf("配置版本为 %d，灰度版本为 %d", item.Version, item.GrayVersion), ImpactSummary: "将当前灰度配置晋升为稳定版本"}, configApprovalPayloadMap(payload), principal, clientIP, func(tx *gorm.DB, req authz.ApprovalRequest) error {
		return s.pending.WithTx(tx).Create(&model.ConfigPendingChange{ChangeID: changeID, ApprovalRequestID: req.RequestID, ConfigItemID: id, ChangeType: model.ConfigPendingChangeGrayPromote, ExpectedVersion: item.Version, ExpectedGrayVersion: item.GrayVersion, ContentSHA256: contentHash, Ciphertext: ciphertext, Status: model.ConfigPendingChangePending})
	})
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	return ConfigApprovalTicket{ApprovalRequestID: request.RequestID, ChangeID: changeID, Status: request.Status}, nil
}

// RequestPublish 冻结加密的新内容并创建审批；内容不写入审批载荷、摘要或审计。
func (s *ConfigService) RequestPublish(id uint, content, reason, idempotencyKey, operator, clientIP string, principal auth.Principal) (ConfigApprovalTicket, error) {
	item, err := s.Get(id)
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	if operator == "" || s.approval == nil || s.pending == nil || s.cipher == nil || !s.cipher.IsEnabled() {
		return ConfigApprovalTicket{}, apperr.ErrForbidden
	}
	if err := validateContent(item.Format, content); err != nil {
		return ConfigApprovalTicket{}, err
	}
	contentHash := configContentHash(content)
	changeID := configApprovalChangeID(principal, authz.OperationConfigPublish, id, idempotencyKey)
	ciphertext, err := s.encryptPendingPayload(configPendingPayload{Content: content, Operator: operator, ClientIP: clientIP})
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	payload := configApprovalPayload{SchemaVersion: approvalSchemaVersion, ChangeID: changeID, ConfigItemID: id, ExpectedVersion: item.Version, ContentSHA256: contentHash}
	request, err := s.approval.request(authz.Operation{Kind: authz.OperationConfigPublish, Resource: "config_item", ResourceID: fmt.Sprintf("%d", id), IdempotencyKey: idempotencyKey, Reason: reason, PreconditionSummary: fmt.Sprintf("配置版本为 %d", item.Version), ImpactSummary: "发布配置新版本"}, configApprovalPayloadMap(payload), principal, clientIP, func(tx *gorm.DB, req authz.ApprovalRequest) error {
		return s.pending.WithTx(tx).Create(&model.ConfigPendingChange{ChangeID: changeID, ApprovalRequestID: req.RequestID, ConfigItemID: id, ChangeType: model.ConfigPendingChangePublish, ExpectedVersion: item.Version, ContentSHA256: contentHash, Ciphertext: ciphertext, Status: model.ConfigPendingChangePending})
	})
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	return ConfigApprovalTicket{ApprovalRequestID: request.RequestID, ChangeID: changeID, Status: request.Status}, nil
}

// RequestRollback 冻结目标历史版本的加密内容并创建回滚审批。
func (s *ConfigService) RequestRollback(id uint, toVersion int64, reason, idempotencyKey, operator, comment, clientIP string, principal auth.Principal) (ConfigApprovalTicket, error) {
	item, err := s.Get(id)
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	if operator == "" || s.approval == nil || s.pending == nil || s.cipher == nil || !s.cipher.IsEnabled() {
		return ConfigApprovalTicket{}, apperr.ErrForbidden
	}
	target, err := s.revRepo.FindByItemAndVersion(id, toVersion)
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	if target == nil {
		return ConfigApprovalTicket{}, apperr.ErrRevisionNotFound
	}
	if err := validateContent(item.Format, target.Content); err != nil {
		return ConfigApprovalTicket{}, err
	}
	contentHash := configContentHash(target.Content)
	changeID := configApprovalChangeID(principal, authz.OperationConfigRollback, id, idempotencyKey)
	ciphertext, err := s.encryptPendingPayload(configPendingPayload{Content: target.Content, RollbackVersion: toVersion, Comment: comment, Operator: operator, ClientIP: clientIP})
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	payload := configApprovalPayload{SchemaVersion: approvalSchemaVersion, ChangeID: changeID, ConfigItemID: id, ExpectedVersion: item.Version, ContentSHA256: contentHash}
	request, err := s.approval.request(authz.Operation{Kind: authz.OperationConfigRollback, Resource: "config_item", ResourceID: fmt.Sprintf("%d", id), IdempotencyKey: idempotencyKey, Reason: reason, PreconditionSummary: fmt.Sprintf("配置版本为 %d，目标历史版本为 %d", item.Version, toVersion), ImpactSummary: "回滚配置到历史版本"}, configApprovalPayloadMap(payload), principal, clientIP, func(tx *gorm.DB, req authz.ApprovalRequest) error {
		return s.pending.WithTx(tx).Create(&model.ConfigPendingChange{ChangeID: changeID, ApprovalRequestID: req.RequestID, ConfigItemID: id, ChangeType: model.ConfigPendingChangeRollback, ExpectedVersion: item.Version, ContentSHA256: contentHash, Ciphertext: ciphertext, Status: model.ConfigPendingChangePending})
	})
	if err != nil {
		return ConfigApprovalTicket{}, err
	}
	return ConfigApprovalTicket{ApprovalRequestID: request.RequestID, ChangeID: changeID, Status: request.Status}, nil
}

func (s *ConfigService) encryptPendingPayload(payload configPendingPayload) (string, error) {
	plain, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return s.cipher.Encrypt(string(plain))
}

func configApprovalPayloadMap(payload configApprovalPayload) map[string]any {
	return map[string]any{"schemaVersion": payload.SchemaVersion, "changeId": payload.ChangeID, "configItemId": payload.ConfigItemID, "expectedVersion": payload.ExpectedVersion, "expectedGrayVersion": payload.ExpectedGrayVersion, "sha256": payload.ContentSHA256}
}

func configContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func configPendingHash(pending configPendingPayload) string {
	raw, _ := json.Marshal(pending)
	return configContentHash(string(raw))
}

func configItemHash(item *model.ConfigItem) string {
	return configContentHash(fmt.Sprintf("%d:%d:%s:%t", item.ID, item.Version, item.ContentMD5, item.Enabled))
}

func configApprovalChangeID(principal auth.Principal, operation string, itemID uint, idempotencyKey string) string {
	p := auth.NormalizePrincipal(principal)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d|%s", p.StableKind(), p.StableID(), operation, itemID, idempotencyKey)))
	return "cfgchg_" + hex.EncodeToString(sum[:])
}

type configApprovalAdapter struct{ svc *ConfigService }

func (configApprovalAdapter) Execute(authz.ApprovalRequest, authz.Permit) error {
	return apperr.ErrForbidden
}

func (a configApprovalAdapter) ExecuteInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
	payload, change, pending, err := a.loadPending(tx, req, permit)
	if err != nil {
		return nil, err
	}
	var item *model.ConfigItem
	var gray *model.ConfigGray
	switch req.Operation.Kind {
	case authz.OperationConfigPublish:
		if change.ChangeType != model.ConfigPendingChangePublish {
			return nil, apperr.ErrApprovalTargetChanged
		}
		item, err = a.svc.applyPublishInTx(tx, payload.ConfigItemID, pending.Content, pending.Operator, pending.Comment, pending.ClientIP, payload.ExpectedVersion)
	case authz.OperationConfigRollback:
		if change.ChangeType != model.ConfigPendingChangeRollback {
			return nil, apperr.ErrApprovalTargetChanged
		}
		item, err = a.svc.applyRollbackInTx(tx, payload.ConfigItemID, pending, payload.ExpectedVersion)
	case authz.OperationConfigGrayPublish:
		if change.ChangeType != model.ConfigPendingChangeGrayPublish || a.svc.gray == nil {
			return nil, apperr.ErrApprovalTargetChanged
		}
		gray, item, err = a.svc.gray.applyPublishInTx(tx, payload, pending)
	case authz.OperationConfigGrayPromote:
		if change.ChangeType != model.ConfigPendingChangeGrayPromote || a.svc.gray == nil {
			return nil, apperr.ErrApprovalTargetChanged
		}
		item, gray, err = a.svc.gray.applyPromoteInTx(tx, payload, pending)
	case authz.OperationConfigDelete:
		if change.ChangeType != model.ConfigPendingChangeDelete {
			return nil, apperr.ErrApprovalTargetChanged
		}
		item, err = a.applyDelete(tx, payload, pending)
	case authz.OperationConfigBatchDelete, authz.OperationConfigBatchDisable, authz.OperationConfigBatchEnable:
		if change.ChangeType != map[string]string{authz.OperationConfigBatchDelete: model.ConfigPendingChangeBatchDelete, authz.OperationConfigBatchDisable: model.ConfigPendingChangeBatchDisable, authz.OperationConfigBatchEnable: model.ConfigPendingChangeBatchEnable}[req.Operation.Kind] {
			return nil, apperr.ErrApprovalTargetChanged
		}
		items, applyErr := a.applyBatch(tx, req.Operation.Kind, pending)
		if applyErr != nil {
			return nil, applyErr
		}
		item = &model.ConfigItem{ID: uint(len(items))}
		ok, err := a.svc.pending.WithTx(tx).ApplyCAS(change.ChangeID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, apperr.ErrApprovalTargetChanged
		}
		if err := tx.Create(&model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash, ResultRef: fmt.Sprintf("%d", len(items))}).Error; err != nil {
			return nil, err
		}
		return a.afterBatchCommit(req.Operation.Kind, items, pending.Operator), nil
	default:
		return nil, apperr.ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	ok, err := a.svc.pending.WithTx(tx).ApplyCAS(change.ChangeID)
	if err != nil || !ok {
		if err != nil {
			return nil, err
		}
		return nil, apperr.ErrApprovalTargetChanged
	}
	if err := tx.Create(&model.ApprovalExecutionReceipt{RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash, ResultRef: fmt.Sprintf("%d", item.ID)}).Error; err != nil {
		return nil, err
	}
	return a.afterCommit(req.Operation.Kind, item, gray, pending), nil
}

func (a configApprovalAdapter) afterCommit(operation string, item *model.ConfigItem, gray *model.ConfigGray, pending configPendingPayload) func() {
	return func() {
		switch operation {
		case authz.OperationConfigGrayPublish:
			a.svc.gray.notifyServers(item.NamespaceCode, pending.Cohort)
		case authz.OperationConfigGrayPromote:
			if a.svc.gray.metrics != nil {
				a.svc.gray.metrics.IncConfigPublish()
			}
			a.svc.gray.notifyPromote(item, gray.Cohort)
		case authz.OperationConfigDelete:
			a.svc.notify(item)
			a.svc.exportGit(item, model.ActionConfigDelete, pending.Operator)
		default:
			a.svc.recordPublish()
			a.svc.notify(item)
			a.svc.exportGit(item, operation, pending.Operator)
		}
	}
}

func (a configApprovalAdapter) afterBatchCommit(operation string, items []model.ConfigItem, operator string) func() {
	return func() {
		action := map[string]string{authz.OperationConfigBatchDelete: model.ActionConfigDelete, authz.OperationConfigBatchDisable: model.ActionConfigDisable, authz.OperationConfigBatchEnable: model.ActionConfigEnable}[operation]
		for i := range items {
			a.svc.notify(&items[i])
			a.svc.exportGit(&items[i], action, operator)
		}
	}
}

func (a configApprovalAdapter) loadPending(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (configApprovalPayload, *model.ConfigPendingChange, configPendingPayload, error) {
	var payload configApprovalPayload
	if err := json.Unmarshal(req.Payload, &payload); err != nil || payload.SchemaVersion != approvalSchemaVersion || payload.ChangeID == "" || len(payload.ContentSHA256) != 64 {
		return payload, nil, configPendingPayload{}, apperr.ErrInvalidParam
	}
	if err := ensurePermit(permit, req.Operation.Kind); err != nil {
		return payload, nil, configPendingPayload{}, err
	}
	change, err := a.svc.pending.WithTx(tx).FindByApprovalRequest(req.RequestID)
	if err != nil || change == nil {
		if err != nil {
			return payload, nil, configPendingPayload{}, err
		}
		return payload, nil, configPendingPayload{}, apperr.ErrApprovalTargetChanged
	}
	if change.Status != model.ConfigPendingChangePending || change.ChangeID != payload.ChangeID || change.ConfigItemID != payload.ConfigItemID || change.ChangeType != configChangeTypeForOperation(req.Operation.Kind) || change.ExpectedVersion != payload.ExpectedVersion || change.ExpectedGrayVersion != payload.ExpectedGrayVersion || change.ContentSHA256 != payload.ContentSHA256 {
		return payload, nil, configPendingPayload{}, apperr.ErrApprovalTargetChanged
	}
	plain, err := a.svc.cipher.Decrypt(change.Ciphertext)
	if err != nil {
		return payload, nil, configPendingPayload{}, err
	}
	var pending configPendingPayload
	if err := json.Unmarshal([]byte(plain), &pending); err != nil || pending.Operator == "" || (req.Operation.Kind != authz.OperationConfigDelete && configPendingPayloadHashForOperation(req.Operation.Kind, pending) != payload.ContentSHA256) {
		return payload, nil, configPendingPayload{}, apperr.ErrApprovalTargetChanged
	}
	return payload, change, pending, nil
}

func configChangeTypeForOperation(operation string) string {
	return map[string]string{authz.OperationConfigPublish: model.ConfigPendingChangePublish, authz.OperationConfigRollback: model.ConfigPendingChangeRollback, authz.OperationConfigGrayPublish: model.ConfigPendingChangeGrayPublish, authz.OperationConfigGrayPromote: model.ConfigPendingChangeGrayPromote, authz.OperationConfigDelete: model.ConfigPendingChangeDelete, authz.OperationConfigBatchDelete: model.ConfigPendingChangeBatchDelete, authz.OperationConfigBatchDisable: model.ConfigPendingChangeBatchDisable, authz.OperationConfigBatchEnable: model.ConfigPendingChangeBatchEnable}[operation]
}

func configPendingPayloadHashForOperation(operation string, pending configPendingPayload) string {
	switch operation {
	case authz.OperationConfigDelete:
		return ""
	case authz.OperationConfigBatchDelete, authz.OperationConfigBatchDisable, authz.OperationConfigBatchEnable:
		return configPendingHash(pending)
	default:
		return configContentHash(pending.Content)
	}
}

func (a configApprovalAdapter) applyDelete(tx *gorm.DB, payload configApprovalPayload, pending configPendingPayload) (*model.ConfigItem, error) {
	item, err := a.svc.GetInTx(tx, payload.ConfigItemID)
	if err != nil || item.Version != payload.ExpectedVersion || configItemHash(item) != payload.ContentSHA256 {
		return nil, apperr.ErrApprovalTargetChanged
	}
	if err := a.svc.configRepo.WithTx(tx).SoftDelete(item.ID, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := a.svc.writeAudit(tx, item, pending.Operator, model.ActionConfigDelete, `{"deleted":true}`, pending.ClientIP); err != nil {
		return nil, err
	}
	return item, nil
}

func (a configApprovalAdapter) applyBatch(tx *gorm.DB, operation string, pending configPendingPayload) ([]model.ConfigItem, error) {
	if len(pending.IDs) == 0 || len(pending.IDs) != len(pending.Expected) {
		return nil, apperr.ErrApprovalTargetChanged
	}
	items := make([]model.ConfigItem, 0, len(pending.IDs))
	for i, id := range pending.IDs {
		item, err := a.svc.GetInTx(tx, id)
		if err != nil {
			return nil, err
		}
		expected := pending.Expected[i]
		if expected.ID != item.ID || expected.Version != item.Version || expected.Enabled != item.Enabled || expected.Hash != configItemHash(item) {
			return nil, apperr.ErrApprovalTargetChanged
		}
		action, detail := model.ActionConfigDelete, `{"deleted":true}`
		switch operation {
		case authz.OperationConfigBatchDelete:
			err = a.svc.configRepo.WithTx(tx).SoftDelete(item.ID, time.Now().UTC())
		case authz.OperationConfigBatchDisable, authz.OperationConfigBatchEnable:
			if pending.Enabled == nil || *pending.Enabled != (operation == authz.OperationConfigBatchEnable) {
				return nil, apperr.ErrApprovalTargetChanged
			}
			action, detail = model.ActionConfigDisable, `{"enabled":false}`
			if *pending.Enabled {
				action, detail = model.ActionConfigEnable, `{"enabled":true}`
			}
			err = a.svc.configRepo.WithTx(tx).SetEnabled(item.ID, *pending.Enabled)
		}
		if err != nil {
			return nil, err
		}
		if err := a.svc.writeAudit(tx, item, pending.Operator, action, detail, pending.ClientIP); err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, nil
}

func (a configApprovalAdapter) CompleteTerminalInTx(tx *gorm.DB, req authz.ApprovalRequest, _ string) error {
	if a.svc == nil || a.svc.pending == nil || tx == nil {
		return apperr.ErrForbidden
	}
	return a.svc.pending.WithTx(tx).InvalidateByApprovalRequest(req.RequestID)
}

func (s *ConfigService) applyPublishInTx(tx *gorm.DB, id uint, content, operator, comment, clientIP string, expectedVersion int64) (*model.ConfigItem, error) {
	item, err := s.GetInTx(tx, id)
	if err != nil {
		return nil, err
	}
	if item.Version != expectedVersion {
		return nil, apperr.ErrApprovalTargetChanged
	}
	if err := validateContent(item.Format, content); err != nil {
		return nil, err
	}
	md5 := merge.MD5Hex(content)
	newVersion := item.Version + 1
	rev, err := s.appendRevisionContent(tx, item.ID, item.Format, newVersion, content, md5, item.Sensitive, nil, operator, comment)
	if err != nil {
		return nil, err
	}
	item.Content, item.ContentMD5, item.Version, item.CurrentRevision = content, md5, newVersion, rev.ID
	if err := s.configRepo.WithTx(tx).Save(item); err != nil {
		return nil, err
	}
	if err := s.writeAudit(tx, item, operator, model.ActionConfigPublish, fmt.Sprintf(`{"version":%d,"md5":"%s"}`, newVersion, md5), clientIP); err != nil {
		return nil, err
	}
	if err := s.recordReversible(tx, item, model.ReversibleOpPublish, expectedVersion, operator); err != nil {
		return nil, err
	}
	return item, nil
}
func (s *ConfigService) applyRollbackInTx(tx *gorm.DB, id uint, pending configPendingPayload, expectedVersion int64) (*model.ConfigItem, error) {
	item, err := s.GetInTx(tx, id)
	if err != nil {
		return nil, err
	}
	if item.Version != expectedVersion {
		return nil, apperr.ErrApprovalTargetChanged
	}
	target, err := s.revRepo.WithTx(tx).FindByItemAndVersion(id, pending.RollbackVersion)
	if err != nil {
		return nil, err
	}
	if target == nil || target.Content != pending.Content {
		return nil, apperr.ErrApprovalTargetChanged
	}
	if err := validateContent(item.Format, pending.Content); err != nil {
		return nil, err
	}
	newVersion := item.Version + 1
	source := target.ID
	rev, err := s.appendRevisionContent(tx, item.ID, item.Format, newVersion, pending.Content, target.ContentMD5, item.Sensitive, &source, pending.Operator, pending.Comment)
	if err != nil {
		return nil, err
	}
	item.Content, item.ContentMD5, item.Version, item.CurrentRevision = pending.Content, target.ContentMD5, newVersion, rev.ID
	if err := s.configRepo.WithTx(tx).Save(item); err != nil {
		return nil, err
	}
	if err := s.writeAudit(tx, item, pending.Operator, model.ActionConfigRollback, fmt.Sprintf(`{"version":%d,"fromVersion":%d,"md5":"%s"}`, newVersion, pending.RollbackVersion, target.ContentMD5), pending.ClientIP); err != nil {
		return nil, err
	}
	return item, nil
}
