package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// NamespaceLifecycleParams 是环境生命周期提审所需的操作者上下文。
type NamespaceLifecycleParams struct {
	Reason       string
	Operator     string
	ClientIP     string
	Confirmation string
}

type namespaceLifecyclePayload struct {
	SchemaVersion int                        `json:"schemaVersion"`
	OperationKey  string                     `json:"operationKey"`
	Operator      string                     `json:"operator"`
	ClientIP      string                     `json:"clientIP"`
	Reason        string                     `json:"reason"`
	ExpectedHash  string                     `json:"expectedHash"`
	Namespace     namespaceLifecycleSnapshot `json:"namespace"`
	Impact        namespaceLifecycleImpact   `json:"impact"`
}

type namespaceLifecycleSnapshot struct {
	ID          uint   `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Lifecycle   string `json:"lifecycle"`
}

type namespaceLifecycleImpact struct {
	Servers       []serverLifecycleSnapshot `json:"servers"`
	ServerCount   int                       `json:"serverCount"`
	IdentityCount int                       `json:"identityCount"`
	Summary       []string                  `json:"summary"`
}

// NamespaceLifecycleImpactView 是环境生命周期操作的只读、有界影响摘要。
type NamespaceLifecycleImpactView struct {
	NamespaceID       uint     `json:"namespaceId"`
	Code              string   `json:"code"`
	Action            string   `json:"action"`
	CurrentLifecycle  string   `json:"currentLifecycle"`
	TargetLifecycle   string   `json:"targetLifecycle"`
	ServerCount       int      `json:"serverCount"`
	IdentityCount     int      `json:"identityCount"`
	ActiveServerCount int      `json:"activeServerCount"`
	ImpactHash        string   `json:"impactHash"`
	Summary           []string `json:"summary"`
}

// GetNamespaceLifecycleImpact 返回环境归档、恢复或永久删除的脱敏影响预览。
func (s *V2ControlPlaneService) GetNamespaceLifecycleImpact(namespaceID uint, action string) (NamespaceLifecycleImpactView, error) {
	operation, ok := namespaceLifecycleImpactOperation(action)
	if !ok || namespaceID == 0 {
		return NamespaceLifecycleImpactView{}, apperr.ErrInvalidParam
	}
	namespace, impact, err := loadNamespaceLifecycleState(s.db, namespaceID)
	if err != nil {
		return NamespaceLifecycleImpactView{}, err
	}
	if err := validateNamespaceLifecycleRequest(operation, namespace.Lifecycle); err != nil {
		return NamespaceLifecycleImpactView{}, err
	}
	return NamespaceLifecycleImpactView{
		NamespaceID: namespace.ID, Code: namespace.Code, Action: action, CurrentLifecycle: namespace.Lifecycle,
		TargetLifecycle: namespaceTargetLifecycle(operation), ServerCount: impact.ServerCount, IdentityCount: impact.IdentityCount,
		ActiveServerCount: namespaceActiveServerCount(impact), ImpactHash: namespaceLifecycleHash(namespace, impact), Summary: impact.Summary,
	}, nil
}

// GetNamespacePermanentDeletionImpact 返回永久删除前的专用影响预览。
func (s *V2ControlPlaneService) GetNamespacePermanentDeletionImpact(namespaceID uint) (NamespaceLifecycleImpactView, error) {
	return s.GetNamespaceLifecycleImpact(namespaceID, authz.OperationNamespacePermanentDelete)
}

func namespaceLifecycleImpactOperation(action string) (string, bool) {
	switch action {
	case "archive", authz.OperationNamespaceArchive:
		return authz.OperationNamespaceArchive, true
	case "restore", authz.OperationNamespaceRestore:
		return authz.OperationNamespaceRestore, true
	case "permanent-delete", authz.OperationNamespacePermanentDelete:
		return authz.OperationNamespacePermanentDelete, true
	default:
		return "", false
	}
}

func namespaceTargetLifecycle(operation string) string {
	switch operation {
	case authz.OperationNamespaceArchive:
		return model.NamespaceLifecycleArchived
	case authz.OperationNamespaceRestore:
		return model.NamespaceLifecycleActive
	default:
		return model.NamespaceLifecycleTombstoned
	}
}

func namespaceActiveServerCount(impact namespaceLifecycleImpact) int {
	count := 0
	for _, server := range impact.Servers {
		if server.Lifecycle == model.ServerLifecycleActive {
			count++
		}
	}
	return count
}

// RequestArchiveNamespace 创建环境归档审批，不直接修改领域状态。
func (s *V2ControlPlaneService) RequestArchiveNamespace(namespaceID uint, p NamespaceLifecycleParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	return s.requestNamespaceLifecycle(authz.OperationNamespaceArchive, namespaceID, p, principal, idempotencyKey)
}

// RequestRestoreNamespace 创建环境恢复审批，不直接修改领域状态。
func (s *V2ControlPlaneService) RequestRestoreNamespace(namespaceID uint, p NamespaceLifecycleParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	return s.requestNamespaceLifecycle(authz.OperationNamespaceRestore, namespaceID, p, principal, idempotencyKey)
}

// RequestPermanentDeleteNamespace 创建环境及其子树永久墓碑审批。
func (s *V2ControlPlaneService) RequestPermanentDeleteNamespace(namespaceID uint, p NamespaceLifecycleParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	if err := s.confirmNamespacePermanentDelete(namespaceID, p.Confirmation); err != nil {
		return ApprovalTicketView{}, err
	}
	return s.requestNamespaceLifecycle(authz.OperationNamespacePermanentDelete, namespaceID, p, principal, idempotencyKey)
}

func (s *V2ControlPlaneService) confirmNamespacePermanentDelete(namespaceID uint, confirmation string) error {
	var namespace model.Namespace
	if err := s.db.First(&namespace, namespaceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.ErrNamespaceNotFound
		}
		return err
	}
	if strings.TrimSpace(confirmation) != namespace.Code {
		return apperr.ErrInvalidParam
	}
	return nil
}

func (s *V2ControlPlaneService) requestNamespaceLifecycle(operation string, namespaceID uint, p NamespaceLifecycleParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	if namespaceID == 0 {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	payload := map[string]any{"namespaceId": namespaceID}
	return s.requestApproval(operation, model.TargetTypeNamespace, strconv.FormatUint(uint64(namespaceID), 10), idempotencyKey, p.Reason, p.ClientIP, payload, principal)
}

// ArchiveNamespace 禁止绕过审批适配器直接归档。
func (s *V2ControlPlaneService) ArchiveNamespace(uint, NamespaceLifecycleParams) error {
	return apperr.ErrForbidden
}

// RestoreNamespace 禁止绕过审批适配器直接恢复。
func (s *V2ControlPlaneService) RestoreNamespace(uint, NamespaceLifecycleParams) error {
	return apperr.ErrForbidden
}

// PermanentDeleteNamespace 禁止绕过审批适配器直接永久删除。
func (s *V2ControlPlaneService) PermanentDeleteNamespace(uint, NamespaceLifecycleParams) error {
	return apperr.ErrForbidden
}

func isNamespaceLifecycleOperation(operation string) bool {
	return operation == authz.OperationNamespaceArchive || operation == authz.OperationNamespaceRestore || operation == authz.OperationNamespacePermanentDelete
}

func namespaceLifecycleID(payload map[string]any) (uint, error) {
	if parameters, ok := payload["parameters"].(map[string]any); ok {
		return lifecycleUint(parameters["namespaceId"])
	}
	return lifecycleUint(payload["namespaceId"])
}

func validateNamespaceLifecycleRequest(operation, lifecycle string) error {
	switch operation {
	case authz.OperationNamespaceArchive:
		if lifecycle != model.NamespaceLifecycleActive {
			return apperr.ErrNamespaceNotActive
		}
	case authz.OperationNamespaceRestore, authz.OperationNamespacePermanentDelete:
		if lifecycle != model.NamespaceLifecycleArchived {
			return apperr.ErrNamespaceNotArchived
		}
	default:
		return apperr.ErrInvalidParam
	}
	return nil
}

func (s *V2ControlPlaneService) prepareNamespaceLifecycleApproval(op authz.Operation, payload map[string]any, principal auth.Principal, clientIP string) (authz.Operation, map[string]any, error) {
	namespaceID, err := namespaceLifecycleID(payload)
	if err != nil {
		return op, nil, err
	}
	snapshot, impact, err := loadNamespaceLifecycleState(s.db, namespaceID)
	if err != nil {
		return op, nil, err
	}
	if err := validateNamespaceLifecycleRequest(op.Kind, snapshot.Lifecycle); err != nil {
		return op, nil, err
	}
	principal = auth.NormalizePrincipal(principal)
	op.Resource = model.TargetTypeNamespace
	op.ResourceID = strconv.FormatUint(uint64(namespaceID), 10)
	op.RiskLevel = "high"
	return op, namespaceLifecyclePayloadMap(op, snapshot, impact, principal.AuditRef(), clientIP), nil
}

func loadNamespaceLifecycleState(db *gorm.DB, namespaceID uint) (namespaceLifecycleSnapshot, namespaceLifecycleImpact, error) {
	var namespace model.Namespace
	if err := db.First(&namespace, namespaceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return namespaceLifecycleSnapshot{}, namespaceLifecycleImpact{}, apperr.ErrNamespaceNotFound
		}
		return namespaceLifecycleSnapshot{}, namespaceLifecycleImpact{}, err
	}
	var servers []model.Server
	if err := db.Where("namespace_id = ?", namespaceID).Order("server_id ASC, id ASC").Find(&servers).Error; err != nil {
		return namespaceLifecycleSnapshot{}, namespaceLifecycleImpact{}, err
	}
	impact := namespaceLifecycleImpact{Servers: make([]serverLifecycleSnapshot, 0, len(servers)), ServerCount: len(servers), Summary: make([]string, 0, minLifecycleSummary(len(servers), 20))}
	for _, server := range servers {
		current, err := loadServerLifecycleSnapshot(db, server.ID)
		if err != nil {
			return namespaceLifecycleSnapshot{}, namespaceLifecycleImpact{}, err
		}
		impact.IdentityCount += len(current.Identities)
		impact.Servers = append(impact.Servers, current)
		if len(impact.Summary) < 20 {
			impact.Summary = append(impact.Summary, fmt.Sprintf("%s:%s", current.ServerID, current.Lifecycle))
		}
	}
	return namespaceLifecycleSnapshot{ID: namespace.ID, Code: namespace.Code, Name: namespace.Name, Description: namespace.Description, Lifecycle: namespaceLifecycleValue(&namespace)}, impact, nil
}

func minLifecycleSummary(value, limit int) int {
	if value < limit {
		return value
	}
	return limit
}

func namespaceLifecyclePayloadMap(op authz.Operation, namespace namespaceLifecycleSnapshot, impact namespaceLifecycleImpact, operator, clientIP string) map[string]any {
	payload := namespaceLifecyclePayload{SchemaVersion: approvalSchemaVersion, OperationKey: op.Kind, Operator: operator, ClientIP: clientIP, Reason: strings.TrimSpace(op.Reason), Namespace: namespace, Impact: impact}
	payload.ExpectedHash = namespaceLifecycleHash(namespace, impact)
	return map[string]any{
		"schemaVersion": payload.SchemaVersion, "operationKey": payload.OperationKey, "operator": payload.Operator, "clientIP": payload.ClientIP,
		"reason": payload.Reason, "expectedHash": payload.ExpectedHash, "namespace": payload.Namespace, "impact": payload.Impact,
	}
}

func namespaceLifecycleHash(namespace namespaceLifecycleSnapshot, impact namespaceLifecycleImpact) string {
	body := auditJSON(struct {
		Namespace namespaceLifecycleSnapshot `json:"namespace"`
		Impact    namespaceLifecycleImpact   `json:"impact"`
	}{Namespace: namespace, Impact: impact})
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func namespaceLifecycleValue(namespace *model.Namespace) string {
	if namespace == nil || namespace.Lifecycle == "" {
		return model.NamespaceLifecycleActive
	}
	return namespace.Lifecycle
}

func isNamespaceActive(namespace *model.Namespace) bool {
	return namespace != nil && namespaceLifecycleValue(namespace) == model.NamespaceLifecycleActive
}

func (s *V2ControlPlaneService) applyNamespaceLifecycle(payload namespaceLifecyclePayload, permit authz.Permit, operation string) error {
	if err := ensurePermit(permit, operation); err != nil || payload.OperationKey != operation {
		return apperr.ErrForbidden
	}
	currentNamespace, currentImpact, err := loadNamespaceLifecycleState(s.db, payload.Namespace.ID)
	if err != nil || currentNamespace != payload.Namespace || namespaceLifecycleHash(currentNamespace, currentImpact) != payload.ExpectedHash {
		return lifecycleTargetChanged(err)
	}
	if err := validateNamespaceLifecycleRequest(operation, currentNamespace.Lifecycle); err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := updateNamespaceLifecycle(s.db, payload, permit, operation, now); err != nil {
		return err
	}
	if operation == authz.OperationNamespacePermanentDelete {
		if err := tombstoneNamespaceTree(s.db, payload, permit, now); err != nil {
			return err
		}
	}
	if err := createAudit(s.db, namespaceLifecycleAudit(payload, permit, operation)); err != nil {
		return err
	}
	if operation == authz.OperationNamespaceArchive || operation == authz.OperationNamespacePermanentDelete {
		s.scheduleRuntimeNamespaceEviction(currentNamespace.Code, currentImpact)
	}
	return nil
}

func updateNamespaceLifecycle(db *gorm.DB, payload namespaceLifecyclePayload, permit authz.Permit, operation string, now time.Time) error {
	updates := map[string]any{}
	switch operation {
	case authz.OperationNamespaceArchive:
		updates = map[string]any{"lifecycle": model.NamespaceLifecycleArchived, "archived_at": &now, "archived_by": payload.Operator, "archive_reason": payload.Reason}
	case authz.OperationNamespaceRestore:
		updates = map[string]any{"lifecycle": model.NamespaceLifecycleActive, "archived_at": nil, "archived_by": "", "archive_reason": ""}
	case authz.OperationNamespacePermanentDelete:
		updates = map[string]any{"lifecycle": model.NamespaceLifecycleTombstoned, "tombstoned_at": &now, "tombstoned_by": payload.Operator,
			"tombstone_reason": payload.Reason, "tombstone_approval_request_id": permit.RequestID(), "tombstone_impact_hash": payload.ExpectedHash}
	}
	result := db.Model(&model.Namespace{}).Where("id = ? AND lifecycle = ?", payload.Namespace.ID, payload.Namespace.Lifecycle).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return apperr.ErrApprovalTargetChanged
	}
	return nil
}

func tombstoneNamespaceTree(db *gorm.DB, payload namespaceLifecyclePayload, permit authz.Permit, now time.Time) error {
	if err := db.Model(&model.Server{}).Where("namespace_id = ? AND lifecycle IN ?", payload.Namespace.ID, []string{model.ServerLifecycleActive, model.ServerLifecycleArchived}).Updates(map[string]any{
		"lifecycle": model.ServerLifecycleTombstoned, "tombstoned_at": &now, "tombstoned_by": payload.Operator,
		"tombstone_reason": payload.Reason, "tombstone_approval_request_id": permit.RequestID(), "tombstone_impact_hash": payload.ExpectedHash,
	}).Error; err != nil {
		return err
	}
	if err := db.Model(&model.AgentIdentity{}).Where("namespace_id = ?", payload.Namespace.ID).Updates(map[string]any{"status": model.AgentIdentityStatusUnbound, "status_changed_at": now, "pending_expires_at": nil, "conflict_reason": "", "asset_binding_closed_at": &now, "asset_binding_close_reason": payload.Reason, "asset_binding_closed_approval_request_id": permit.RequestID()}).Error; err != nil {
		return err
	}
	return nil
}

func namespaceLifecycleAudit(payload namespaceLifecyclePayload, permit authz.Permit, operation string) model.AuditLog {
	return model.AuditLog{NamespaceCode: payload.Namespace.Code, Operator: payload.Operator, Action: operation, TargetType: model.TargetTypeNamespace, TargetRef: payload.Namespace.Code,
		Detail: auditJSON(map[string]any{"requestId": permit.RequestID(), "impactHash": payload.ExpectedHash, "serverCount": payload.Impact.ServerCount, "identityCount": payload.Impact.IdentityCount}), Result: model.ResultOK, ClientIP: payload.ClientIP}
}

// PermanentDeleteServer 禁止绕过审批适配器直接永久删除。
func (s *V2ControlPlaneService) PermanentDeleteServer(uint, NamespaceLifecycleParams) error {
	return apperr.ErrForbidden
}

// RequestArchiveServer 创建 server 归档审批，不直接修改领域状态。
func (s *V2ControlPlaneService) RequestArchiveServer(serverID uint, p NamespaceLifecycleParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	return s.requestServerLifecycle(authz.OperationServerArchive, serverID, p, principal, idempotencyKey)
}

// RequestRestoreServer 创建 server 恢复审批，不直接修改领域状态。
func (s *V2ControlPlaneService) RequestRestoreServer(serverID uint, p NamespaceLifecycleParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	return s.requestServerLifecycle(authz.OperationServerRestore, serverID, p, principal, idempotencyKey)
}

func (s *V2ControlPlaneService) requestServerLifecycle(operation string, serverID uint, p NamespaceLifecycleParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	if serverID == 0 {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	return s.requestApproval(operation, model.TargetTypeServer, strconv.FormatUint(uint64(serverID), 10), idempotencyKey, p.Reason, p.ClientIP, map[string]any{"serverRowId": serverID}, principal)
}

// RequestPermanentDeleteServer 创建已归档 server 的永久墓碑审批。
func (s *V2ControlPlaneService) RequestPermanentDeleteServer(serverID uint, p NamespaceLifecycleParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	if serverID == 0 {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	var server model.Server
	if err := s.db.First(&server, serverID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ApprovalTicketView{}, apperr.ErrInstanceNotFound
		}
		return ApprovalTicketView{}, err
	}
	if strings.TrimSpace(p.Confirmation) != server.ServerID {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	return s.requestApproval(authz.OperationServerPermanentDelete, model.TargetTypeServer, strconv.FormatUint(uint64(serverID), 10), idempotencyKey, p.Reason, p.ClientIP, map[string]any{"serverRowId": serverID}, principal)
}

func (s *V2ControlPlaneService) applyServerPermanentDelete(payload serverLifecyclePayload, permit authz.Permit) error {
	if err := ensurePermit(permit, authz.OperationServerPermanentDelete); err != nil || payload.OperationKey != authz.OperationServerPermanentDelete {
		return apperr.ErrForbidden
	}
	current, err := loadServerLifecycleSnapshot(s.db, payload.Server.ID)
	if err != nil || !sameServerLifecycleSnapshot(current, payload.Server) || current.Lifecycle != payload.ExpectedLifecycle {
		return lifecycleTargetChanged(err)
	}
	if current.Lifecycle != model.ServerLifecycleArchived {
		return apperr.ErrServerNotArchived
	}
	now := time.Now().UTC()
	if err := tombstoneServer(s.db, current, payload, permit, now); err != nil {
		return err
	}
	return createAudit(s.db, serverLifecycleAudit(payload, permit, authz.OperationServerPermanentDelete))
}

func tombstoneServer(db *gorm.DB, snapshot serverLifecycleSnapshot, payload serverLifecyclePayload, permit authz.Permit, now time.Time) error {
	result := db.Model(&model.Server{}).Where("id = ? AND lifecycle = ?", snapshot.ID, model.ServerLifecycleArchived).Updates(map[string]any{
		"lifecycle": model.ServerLifecycleTombstoned, "tombstoned_at": &now, "tombstoned_by": payload.Operator, "tombstone_reason": payload.ArchiveReason,
		"tombstone_approval_request_id": permit.RequestID(), "tombstone_impact_hash": permit.PayloadHash(),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return apperr.ErrApprovalTargetChanged
	}
	return db.Model(&model.AgentIdentity{}).Where("namespace_id = ? AND server_id = ?", snapshot.NamespaceID, snapshot.ServerID).Updates(map[string]any{
		"status": model.AgentIdentityStatusUnbound, "status_changed_at": now, "pending_expires_at": nil, "conflict_reason": "", "asset_binding_closed_at": &now, "asset_binding_close_reason": payload.ArchiveReason, "asset_binding_closed_approval_request_id": permit.RequestID(),
	}).Error
}

func sameServerLifecycleSnapshot(left, right serverLifecycleSnapshot) bool {
	return reflect.DeepEqual(left, right)
}
