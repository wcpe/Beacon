package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

const approvalSchemaVersion = 1

// ApprovalTicketView 是危险操作兼容入口返回的最小审批视图。
type ApprovalTicketView struct {
	ApprovalRequestID string `json:"approvalRequestId"`
	Status            string `json:"status"`
	OperationKey      string `json:"operationKey"`
	SecretReturned    bool   `json:"secretReturned"`
}

// SetApprovalService 注入 FR-207 统一审批核心。
func (s *V2ControlPlaneService) SetApprovalService(approval *ApprovalService) {
	s.approval = approval
	if approval != nil {
		approval.SetApprovalRequestPreparer(s)
	}
}

// RegisterV2ControlPlaneApprovalAdapters 注册 V2 控制面危险操作执行适配器。
func RegisterV2ControlPlaneApprovalAdapters(registry *authz.ApprovalRegistry, svc *V2ControlPlaneService) {
	if registry == nil || svc == nil {
		return
	}
	for _, kind := range []string{
		authz.OperationIdentityApprove, authz.OperationIdentityUnbind, authz.OperationIdentityEnable,
		authz.OperationIdentityAllowReapply, authz.OperationIdentityResolveConflict, authz.OperationNamespaceTrustGrant,
		authz.OperationTopologyServerAssign, authz.OperationTopologyServerRezone,
		authz.OperationTopologyDefaultEntryChange, authz.OperationTopologyLobbyMemberMove,
		authz.OperationTopologyDrainingDisable, authz.OperationServerArchive, authz.OperationServerRestore, authz.OperationServerPermanentDelete,
		authz.OperationNamespaceArchive, authz.OperationNamespaceRestore, authz.OperationNamespacePermanentDelete,
	} {
		registry.Register(kind, authz.RequireExecutionReceipt(v2ApprovalAdapter{svc: svc}))
	}
}

type v2ApprovalAdapter struct {
	svc *V2ControlPlaneService
}

func (a v2ApprovalAdapter) Execute(authz.ApprovalRequest, authz.Permit) error {
	return apperr.ErrForbidden
}

func (a v2ApprovalAdapter) ExecuteInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
	return a.svc.executeApprovedV2OperationInTx(tx, req, permit)
}

// ReadApprovalEvidence 只按持久化目标引用读取当前事实，绝不解析冻结载荷。
func (a v2ApprovalAdapter) ReadApprovalEvidence(req authz.ApprovalRequest) (authz.ApprovalEvidence, error) {
	if a.svc == nil || a.svc.db == nil {
		return authz.ApprovalEvidence{}, apperr.ErrInternal
	}
	if req.Operation.Resource == "legacy_topology" {
		return a.readLegacyTopologyEvidence(req.Operation.ResourceID)
	}
	switch req.Operation.Resource {
	case model.TargetTypeIdentity:
		identity, err := findIdentityByID(a.svc.db, req.Operation.ResourceID)
		if err != nil || identity == nil {
			return authz.ApprovalEvidence{}, apperr.ErrApprovalTargetChanged
		}
		return authz.ApprovalEvidence{
			EvidenceStatus: "available",
			CurrentFactsSummary: []authz.ApprovalEvidenceLine{
				{Label: "身份", Value: identity.IdentityID},
				{Label: "当前状态", Value: identity.Status},
				{Label: "命名空间", Value: strconv.FormatUint(uint64(identity.NamespaceID), 10)},
			},
		}, nil
	case model.TargetTypeServer:
		return a.readServerApprovalEvidence(req.Operation.ResourceID)
	case model.TargetTypeNamespace:
		return a.readNamespaceApprovalEvidence(req.Operation.ResourceID)
	case model.TargetTypeNamespaceTrust:
		return a.readNamespaceTrustApprovalEvidence(req.Operation.ResourceID)
	default:
		return authz.ApprovalEvidence{}, apperr.ErrInvalidParam
	}
}

func (a v2ApprovalAdapter) readNamespaceApprovalEvidence(resourceID string) (authz.ApprovalEvidence, error) {
	id, err := strconv.ParseUint(resourceID, 10, 64)
	if err != nil || id == 0 {
		return authz.ApprovalEvidence{}, apperr.ErrInvalidParam
	}
	var namespace model.Namespace
	if err := a.svc.db.First(&namespace, uint(id)).Error; err != nil {
		return authz.ApprovalEvidence{}, apperr.ErrApprovalTargetChanged
	}
	state, impact, err := loadNamespaceLifecycleState(a.svc.db, namespace.ID)
	if err != nil {
		return authz.ApprovalEvidence{}, err
	}
	lines := []authz.ApprovalEvidenceLine{
		{Label: "命名空间", Value: strconv.FormatUint(uint64(namespace.ID), 10)},
		{Label: "命名空间编码", Value: state.Code},
		{Label: "当前生命周期", Value: state.Lifecycle},
		{Label: "服务数量", Value: strconv.Itoa(impact.ServerCount)},
		{Label: "身份数量", Value: strconv.Itoa(impact.IdentityCount)},
	}
	return authz.ApprovalEvidence{EvidenceStatus: "available", CurrentFactsSummary: lines}, nil
}

func (a v2ApprovalAdapter) readLegacyTopologyEvidence(resourceID string) (authz.ApprovalEvidence, error) {
	parts := strings.Split(resourceID, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return authz.ApprovalEvidence{}, apperr.ErrInvalidParam
	}
	var assignment model.ZoneAssignment
	err := a.svc.db.Where("namespace_code = ? AND server_id = ? AND deleted_at = ?", parts[0], parts[1], model.SoftDeleteSentinel).First(&assignment).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return authz.ApprovalEvidence{}, err
	}
	lines := []authz.ApprovalEvidenceLine{{Label: "命名空间", Value: parts[0]}, {Label: "服务", Value: parts[1]}}
	if err == nil {
		lines = append(lines, authz.ApprovalEvidenceLine{Label: "当前大区", Value: assignment.GroupCode}, authz.ApprovalEvidenceLine{Label: "当前小区", Value: assignment.ZoneCode})
	} else {
		lines = append(lines, authz.ApprovalEvidenceLine{Label: "当前归属", Value: "未分配"})
	}
	return authz.ApprovalEvidence{EvidenceStatus: "available", CurrentFactsSummary: lines}, nil
}

func (a v2ApprovalAdapter) readServerApprovalEvidence(resourceID string) (authz.ApprovalEvidence, error) {
	ids, err := parseServerApprovalResourceIDs(resourceID)
	if err == nil && len(ids) > 0 {
		var servers []model.Server
		if err := a.svc.db.Where("id IN ?", ids).Order("id ASC").Find(&servers).Error; err != nil || len(servers) != len(ids) {
			return authz.ApprovalEvidence{}, apperr.ErrApprovalTargetChanged
		}
		if _, err := serverApprovalNamespaceID(servers); err != nil {
			return authz.ApprovalEvidence{}, err
		}
		return authz.ApprovalEvidence{EvidenceStatus: "available", CurrentFactsSummary: serverEvidenceLines(servers)}, nil
	}
	var server model.Server
	if err := a.svc.db.Where("server_id = ?", resourceID).First(&server).Error; err != nil {
		return authz.ApprovalEvidence{}, apperr.ErrApprovalTargetChanged
	}
	if _, err := serverApprovalNamespaceID([]model.Server{server}); err != nil {
		return authz.ApprovalEvidence{}, err
	}
	return authz.ApprovalEvidence{EvidenceStatus: "available", CurrentFactsSummary: serverEvidenceLines([]model.Server{server})}, nil
}

func parseServerApprovalResourceIDs(resourceID string) ([]uint, error) {
	parts := strings.Split(resourceID, ",")
	ids := make([]uint, 0, len(parts))
	for _, part := range parts {
		parsed, err := strconv.ParseUint(part, 10, 64)
		if err != nil || parsed == 0 {
			return nil, apperr.ErrInvalidParam
		}
		ids = append(ids, uint(parsed))
	}
	return ids, nil
}

func serverEvidenceLines(servers []model.Server) []authz.ApprovalEvidenceLine {
	namespaceID, err := serverApprovalNamespaceID(servers)
	if err != nil {
		return nil
	}
	lines := []authz.ApprovalEvidenceLine{
		{Label: "当前服务数", Value: strconv.Itoa(len(servers))},
		{Label: "命名空间", Value: strconv.FormatUint(uint64(namespaceID), 10)},
	}
	for _, server := range servers {
		rowID := strconv.FormatUint(uint64(server.ID), 10)
		lines = append(lines,
			authz.ApprovalEvidenceLine{Label: "服务 " + rowID, Value: server.ServerID},
			authz.ApprovalEvidenceLine{Label: "服务 " + rowID + " 当前生命周期", Value: server.Lifecycle},
		)
	}
	return lines
}

func serverApprovalNamespaceID(servers []model.Server) (uint, error) {
	if len(servers) == 0 || servers[0].NamespaceID == 0 {
		return 0, apperr.ErrApprovalTargetChanged
	}
	namespaceID := servers[0].NamespaceID
	for _, server := range servers[1:] {
		if server.NamespaceID != namespaceID {
			return 0, apperr.ErrSchedCrossNamespace
		}
	}
	return namespaceID, nil
}

func (a v2ApprovalAdapter) readNamespaceTrustApprovalEvidence(resourceID string) (authz.ApprovalEvidence, error) {
	parts := strings.Split(resourceID, ":")
	if len(parts) != 3 {
		return authz.ApprovalEvidence{}, apperr.ErrInvalidParam
	}
	from, fromErr := strconv.ParseUint(parts[0], 10, 64)
	to, toErr := strconv.ParseUint(parts[1], 10, 64)
	if fromErr != nil || toErr != nil || from == 0 || to == 0 || parts[2] == "" {
		return authz.ApprovalEvidence{}, apperr.ErrInvalidParam
	}
	var trust model.NamespaceTrust
	err := a.svc.db.Where("from_namespace_id = ? AND to_namespace_id = ? AND capability = ?", uint(from), uint(to), parts[2]).First(&trust).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return authz.ApprovalEvidence{EvidenceStatus: "available", CurrentFactsSummary: []authz.ApprovalEvidenceLine{
			{Label: "当前信任状态", Value: "不存在"},
		}}, nil
	}
	if err != nil {
		return authz.ApprovalEvidence{}, err
	}
	return authz.ApprovalEvidence{EvidenceStatus: "available", CurrentFactsSummary: []authz.ApprovalEvidenceLine{
		{Label: "当前信任状态", Value: trust.Status},
		{Label: "来源命名空间", Value: strconv.FormatUint(uint64(trust.FromNamespaceID), 10)},
		{Label: "目标命名空间", Value: strconv.FormatUint(uint64(trust.ToNamespaceID), 10)},
	}}, nil
}

func (s *V2ControlPlaneService) executeApprovedV2OperationInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
	if err := ensureRequestPermit(req, permit); err != nil {
		return nil, err
	}
	afterCommit := make([]func(), 0, 1)
	transactional := *s
	transactional.db = tx
	transactional.afterCommit = func(callback func()) { afterCommit = append(afterCommit, callback) }
	if err := transactional.executeApprovedV2Operation(req, permit); err != nil {
		return nil, err
	}
	if err := tx.Create(&model.ApprovalExecutionReceipt{
		RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash,
		ResultRef: "apr-result-" + req.RequestID,
	}).Error; err != nil {
		return nil, err
	}
	if len(afterCommit) == 0 {
		return nil, nil
	}
	return func() {
		for _, callback := range afterCommit {
			callback()
		}
	}, nil
}

func (s *V2ControlPlaneService) executeApprovedV2Operation(req authz.ApprovalRequest, permit authz.Permit) error {
	if approvalPayloadSource(req.Payload) == "v1" {
		return s.executeApprovedLegacyTopologyOperation(req, permit)
	}
	switch permit.Operation() {
	case authz.OperationIdentityApprove, authz.OperationIdentityUnbind, authz.OperationIdentityEnable,
		authz.OperationIdentityAllowReapply, authz.OperationIdentityResolveConflict, authz.OperationNamespaceTrustGrant:
		return s.executeApprovedIdentityOperation(req, permit)
	case authz.OperationTopologyServerAssign:
		var p serverAssignPayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		if err := s.ensureTopologySnapshot(p.ServerSnapshot); err != nil {
			return err
		}
		return s.assignServersApproved(p, permit)
	case authz.OperationTopologyServerRezone:
		var p serverRezonePayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		if err := s.ensureTopologySnapshot(p.ServerSnapshot); err != nil {
			return err
		}
		return s.rezoneServersApproved(p, permit)
	case authz.OperationTopologyDefaultEntryChange:
		var p defaultEntryPayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		if err := s.ensureTopologySnapshot(p.ServerSnapshot); err != nil {
			return err
		}
		return s.defaultEntryApproved(p, permit)
	case authz.OperationTopologyLobbyMemberMove:
		var p placementTransferPayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		if err := s.ensureTopologySnapshot([]serverTopologySnapshot{p.ServerSnapshot}); err != nil {
			return err
		}
		return s.placementTransferApproved(p, permit)
	case authz.OperationTopologyDrainingDisable:
		var p drainingPayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		if err := s.ensureTopologySnapshot([]serverTopologySnapshot{p.ServerSnapshot}); err != nil {
			return err
		}
		return s.drainingDisableApproved(p, permit)
	case authz.OperationServerArchive:
		var p serverLifecyclePayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		return s.archiveServerApproved(p, permit)
	case authz.OperationServerRestore:
		var p serverLifecyclePayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		return s.restoreServerApproved(p, permit)
	case authz.OperationServerPermanentDelete:
		var p serverLifecyclePayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		return s.applyServerPermanentDelete(p, permit)
	case authz.OperationNamespaceArchive, authz.OperationNamespaceRestore, authz.OperationNamespacePermanentDelete:
		var p namespaceLifecyclePayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		return s.applyNamespaceLifecycle(p, permit, permit.Operation())
	default:
		return apperr.ErrInvalidParam
	}
}

func (s *V2ControlPlaneService) executeApprovedIdentityOperation(req authz.ApprovalRequest, permit authz.Permit) error {
	switch permit.Operation() {
	case authz.OperationIdentityApprove:
		var p identityApprovePayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		return s.approveAgentIdentityApproved(p, permit)
	case authz.OperationIdentityUnbind, authz.OperationIdentityEnable, authz.OperationIdentityAllowReapply:
		var p identityTransitionPayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		return s.transitionIdentityApproved(p, permit.Operation(), permit)
	case authz.OperationIdentityResolveConflict:
		var p identityResolveConflictPayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		return s.resolveConflictApproved(p, permit)
	case authz.OperationNamespaceTrustGrant:
		var p namespaceTrustGrantPayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		return s.grantNamespaceTrustApproved(p, permit)
	default:
		return apperr.ErrInvalidParam
	}
}

func approvalPayloadSource(payload []byte) string {
	var envelope struct {
		Source string `json:"source"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return ""
	}
	return envelope.Source
}

type legacyZoneAssignmentPayload struct {
	Source        string `json:"source"`
	Action        string `json:"action"`
	Namespace     string `json:"namespace"`
	ServerID      string `json:"serverId"`
	Group         string `json:"group"`
	Zone          string `json:"zone"`
	Note          string `json:"note"`
	ExpectedGroup string `json:"expectedGroup"`
	ExpectedZone  string `json:"expectedZone"`
	ExpectedFound bool   `json:"expectedFound"`
	Operator      string `json:"operator"`
	ClientIP      string `json:"clientIP"`
}

func (s *V2ControlPlaneService) executeApprovedLegacyTopologyOperation(req authz.ApprovalRequest, permit authz.Permit) error {
	var payload legacyZoneAssignmentPayload
	if err := decodeApprovalPayload(req.Payload, &payload); err != nil || payload.Source != "v1" {
		return apperr.ErrInvalidParam
	}
	if err := ensurePermit(permit, req.Operation.Kind); err != nil {
		return err
	}
	if payload.Action == "undrain" {
		return s.executeApprovedLegacyUndrain(payload)
	}
	if s.legacyZone == nil || (permit.Operation() != authz.OperationTopologyServerAssign && permit.Operation() != authz.OperationTopologyServerRezone) {
		return apperr.ErrInvalidParam
	}
	current, err := s.legacyZone.assignRepo.WithTx(s.db).FindByServer(payload.Namespace, payload.ServerID)
	if err != nil {
		return err
	}
	if (current != nil) != payload.ExpectedFound || (current != nil && (current.GroupCode != payload.ExpectedGroup || current.ZoneCode != payload.ExpectedZone)) {
		return apperr.ErrApprovalTargetChanged
	}
	if payload.Action == "unassign" {
		if s.legacyZone.isOnlineNonempty(payload.Namespace, payload.ServerID) {
			return apperr.ErrZoneServerOnlineNonempty
		}
		if err := s.legacyZone.applyUnassignInTx(s.db, payload.Namespace, payload.ServerID, payload.Operator, payload.ClientIP); err != nil {
			return err
		}
		s.scheduleAfterCommit(func() {
			s.legacyZone.registry.ClearAssignment(payload.Namespace, payload.ServerID)
			s.legacyZone.notifyServer(payload.Namespace, payload.ServerID)
			s.legacyZone.exportGit(payload.Namespace, payload.ServerID, model.ActionZoneUnassign, payload.Operator)
		})
		return nil
	}
	if err := s.legacyZone.validateAssignForApproval(payload.Namespace, payload.ServerID, payload.Group, payload.Zone, payload.Operator); err != nil {
		return err
	}
	if _, err := s.legacyZone.applyAssignInTx(s.db, payload.Namespace, payload.ServerID, payload.Group, payload.Zone, payload.Operator, payload.Note, payload.ClientIP); err != nil {
		return err
	}
	s.scheduleAfterCommit(func() {
		s.legacyZone.registry.UpdateAssignment(payload.Namespace, payload.ServerID, payload.Group, payload.Zone)
		s.legacyZone.notifyServer(payload.Namespace, payload.ServerID)
		s.legacyZone.exportGit(payload.Namespace, payload.ServerID, model.ActionZoneAssign, payload.Operator)
	})
	return nil
}

func (s *V2ControlPlaneService) executeApprovedLegacyUndrain(payload legacyZoneAssignmentPayload) error {
	if s.legacyScheduling == nil {
		return apperr.ErrInternal
	}
	current, err := s.legacyScheduling.drainRepo.FindByServer(payload.Namespace, payload.ServerID)
	if err != nil {
		return err
	}
	if !payload.ExpectedFound || current == nil {
		return apperr.ErrApprovalTargetChanged
	}
	return s.legacyScheduling.applyUndrainInTx(s.db, payload.Namespace, payload.ServerID, payload.Operator, payload.ClientIP)
}

func decodeApprovalPayload(raw []byte, out any) error {
	if err := json.Unmarshal(raw, out); err != nil {
		return apperr.ErrInvalidParam
	}
	return nil
}

func (s *V2ControlPlaneService) requestApproval(kind, resource, resourceID, explicitKey, reason, clientIP string, payload map[string]any, principal auth.Principal) (ApprovalTicketView, error) {
	if s.approval == nil {
		return ApprovalTicketView{}, apperr.ErrInternal
	}
	if strings.TrimSpace(reason) == "" {
		return ApprovalTicketView{}, apperr.ErrApprovalReasonRequired
	}
	key := explicitKey
	if key == "" {
		key = approvalIdempotencyKey(kind, resourceID, payload)
	}
	namespaceID, evidence, err := s.approvalTargetEvidence(resource, resourceID)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	created, err := s.approval.Request(authz.Operation{
		Kind: kind, NamespaceID: namespaceID, Resource: resource, ResourceID: resourceID, EvidenceSnapshot: evidence,
		IdempotencyKey: key, RiskLevel: "high", Reason: reason,
	}, payload, principal, clientIP)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	return ApprovalTicketView{ApprovalRequestID: created.RequestID, Status: created.Status, OperationKey: created.OperationKey}, nil
}

func (s *V2ControlPlaneService) approvalTargetEvidence(resource, resourceID string) (*uint, []authz.ApprovalEvidenceLine, error) {
	evidence, err := (v2ApprovalAdapter{svc: s}).ReadApprovalEvidence(authz.ApprovalRequest{Operation: authz.Operation{Resource: resource, ResourceID: resourceID}})
	if err != nil {
		return nil, nil, err
	}
	if evidence.EvidenceStatus != "available" {
		return nil, nil, apperr.ErrApprovalTargetChanged
	}
	for _, line := range evidence.CurrentFactsSummary {
		if line.Label == "命名空间" {
			value, parseErr := strconv.ParseUint(line.Value, 10, 64)
			if parseErr == nil && value > 0 {
				namespaceID := uint(value)
				return &namespaceID, evidence.CurrentFactsSummary, nil
			}
		}
	}
	if resource == model.TargetTypeNamespaceTrust {
		parts := strings.Split(resourceID, ":")
		if len(parts) == 3 {
			value, parseErr := strconv.ParseUint(parts[0], 10, 64)
			if parseErr == nil && value > 0 {
				namespaceID := uint(value)
				return &namespaceID, evidence.CurrentFactsSummary, nil
			}
		}
	}
	return nil, evidence.CurrentFactsSummary, nil
}

func approvalIdempotencyKey(kind, resourceID string, payload map[string]any) string {
	body, _ := json.Marshal(payload)
	sum := sha256.Sum256([]byte(kind + ":" + resourceID + ":" + string(body)))
	return "auto-" + hex.EncodeToString(sum[:])[:24]
}

func baseApprovalPayload(kind, operator, clientIP string) map[string]any {
	return map[string]any{"schemaVersion": approvalSchemaVersion, "operationKey": kind, "operator": operator, "clientIP": clientIP}
}

func (s *V2ControlPlaneService) RequestApproveAgentIdentity(identityID string, p ApproveAgentIdentityParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	serverID, err := resolveApprovedServerID(p.ServerID)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	ident, err := findIdentityByID(s.db, identityID)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	if ident == nil {
		return ApprovalTicketView{}, apperr.ErrInstanceNotFound
	}
	if err := ensureIdentityRuntimeBindingOpen(ident); err != nil {
		return ApprovalTicketView{}, err
	}
	if err := ensureServerActive(s.db, ident.NamespaceID, serverID); err != nil {
		return ApprovalTicketView{}, err
	}
	payload := baseApprovalPayload(authz.OperationIdentityApprove, p.Operator, p.ClientIP)
	payload["identityId"] = identityID
	payload["expectedStatus"] = ident.Status
	payload["serverId"] = serverID
	payload["forceUnbindOccupier"] = p.ForceUnbindOccupier
	payload["targetExplicitNull"] = p.TargetExplicitNull
	payload["targetKind"] = p.TargetKind
	if p.TargetID != nil {
		payload["targetId"] = *p.TargetID
	}
	return s.requestApproval(authz.OperationIdentityApprove, model.TargetTypeIdentity, identityID, idempotencyKey, p.Reason, p.ClientIP, payload, principal)
}

func (s *V2ControlPlaneService) RequestUnbindAgentIdentity(identityID string, p IdentityTransitionParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	return s.requestIdentityTransitionApproval(authz.OperationIdentityUnbind, identityID, p, principal, idempotencyKey)
}

func (s *V2ControlPlaneService) RequestEnableAgentIdentity(identityID string, p IdentityTransitionParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	return s.requestIdentityTransitionApproval(authz.OperationIdentityEnable, identityID, p, principal, idempotencyKey)
}

// RequestAllowAgentIdentityReapply 为恢复重新申请资格创建审批请求。
func (s *V2ControlPlaneService) RequestAllowAgentIdentityReapply(identityID string, p IdentityTransitionParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	return s.requestIdentityTransitionApproval(authz.OperationIdentityAllowReapply, identityID, p, principal, idempotencyKey)
}

func (s *V2ControlPlaneService) requestIdentityTransitionApproval(kind, identityID string, p IdentityTransitionParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	identity, err := findIdentityByID(s.db, identityID)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	if identity == nil {
		return ApprovalTicketView{}, apperr.ErrInstanceNotFound
	}
	if err := ensureIdentityRuntimeBindingOpen(identity); err != nil {
		return ApprovalTicketView{}, err
	}
	payload := baseApprovalPayload(kind, p.Operator, p.ClientIP)
	payload["identityId"] = identityID
	payload["expectedStatus"] = identity.Status
	payload["reason"] = p.Reason
	return s.requestApproval(kind, model.TargetTypeIdentity, identityID, idempotencyKey, p.Reason, p.ClientIP, payload, principal)
}

func (s *V2ControlPlaneService) RequestResolveAgentIdentityConflict(identityID string, p ResolveConflictParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	ident, err := findIdentityByID(s.db, identityID)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	if ident == nil {
		return ApprovalTicketView{}, apperr.ErrInstanceNotFound
	}
	if err := ensureIdentityRuntimeBindingOpen(ident); err != nil {
		return ApprovalTicketView{}, err
	}
	if ident.Status != model.AgentIdentityStatusConflict {
		return ApprovalTicketView{}, apperr.ErrIllegalState
	}
	if p.KeepBootID == "" || !conflictPeersContain(ident.ConflictPeers, p.KeepBootID) {
		return ApprovalTicketView{}, apperr.ErrConflictKeepBootInvalid
	}
	status := ident.Status
	payload := baseApprovalPayload(authz.OperationIdentityResolveConflict, p.Operator, p.ClientIP)
	payload["identityId"] = identityID
	payload["expectedStatus"] = status
	payload["keepBootId"] = p.KeepBootID
	return s.requestApproval(authz.OperationIdentityResolveConflict, model.TargetTypeIdentity, identityID, idempotencyKey, p.Reason, p.ClientIP, payload, principal)
}

func (s *V2ControlPlaneService) RequestGrantNamespaceTrust(p GrantNamespaceTrustParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	payload := baseApprovalPayload(authz.OperationNamespaceTrustGrant, p.Operator, p.ClientIP)
	payload["fromNamespaceId"] = p.FromNamespaceID
	payload["toNamespaceId"] = p.ToNamespaceID
	payload["capability"] = p.Capability
	payload["note"] = p.Note
	payload["expectedActive"] = s.namespaceTrustActive(p.FromNamespaceID, p.ToNamespaceID, p.Capability)
	resourceID := fmt.Sprintf("%d:%d:%s", p.FromNamespaceID, p.ToNamespaceID, p.Capability)
	return s.requestApproval(authz.OperationNamespaceTrustGrant, model.TargetTypeNamespaceTrust, resourceID, idempotencyKey, p.Note, p.ClientIP, payload, principal)
}

func (s *V2ControlPlaneService) RequestAssignServers(p AssignServersParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	if !validAssignmentApprovalTarget(p.TargetKind, p.TargetID) {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	payload := baseApprovalPayload(authz.OperationTopologyServerAssign, p.Operator, p.ClientIP)
	payload["serverIds"] = sortedUintCopy(p.ServerIDs)
	payload["targetKind"] = p.TargetKind
	payload["targetId"] = p.TargetID
	payload["isDefaultEntry"] = p.IsDefaultEntry
	payload["serverSnapshot"] = s.serverSnapshot(p.ServerIDs)
	return s.requestApproval(authz.OperationTopologyServerAssign, model.TargetTypeServer, uintListKey(p.ServerIDs), idempotencyKey, p.Reason, p.ClientIP, payload, principal)
}

// RequestLegacyZoneAssignment 为 V1 zone 指派创建与 V2 同 operation 的审批请求。
func (s *V2ControlPlaneService) RequestLegacyZoneAssignment(namespace, serverID, group, zone, note, operator, clientIP, idempotencyKey string, principal auth.Principal) (ApprovalTicketView, error) {
	if s.legacyZone == nil || namespace == "" || serverID == "" || group == "" || zone == "" {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	if err := s.legacyZone.validateAssignForApproval(namespace, serverID, group, zone, operator); err != nil {
		return ApprovalTicketView{}, err
	}
	current, err := s.legacyZone.assignRepo.FindByServer(namespace, serverID)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	kind := authz.OperationTopologyServerAssign
	payload := map[string]any{"source": "v1", "action": "assign", "namespace": namespace, "serverId": serverID, "group": group, "zone": zone, "note": note, "operator": operator, "clientIP": clientIP, "expectedFound": current != nil}
	if current != nil {
		kind = authz.OperationTopologyServerRezone
		payload["expectedGroup"] = current.GroupCode
		payload["expectedZone"] = current.ZoneCode
	}
	return s.requestLegacyTopologyApproval(kind, namespace, serverID, idempotencyKey, note, clientIP, payload, principal)
}

// RequestLegacyZoneUnassign 为 V1 取消指派创建换区审批请求。
func (s *V2ControlPlaneService) RequestLegacyZoneUnassign(namespace, serverID, reason, operator, clientIP, idempotencyKey string, principal auth.Principal) (ApprovalTicketView, error) {
	if s.legacyZone == nil || namespace == "" || serverID == "" {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	current, err := s.legacyZone.assignRepo.FindByServer(namespace, serverID)
	if err != nil || current == nil {
		return ApprovalTicketView{}, apperr.ErrAssignmentNotFound
	}
	if s.legacyZone.isOnlineNonempty(namespace, serverID) {
		return ApprovalTicketView{}, apperr.ErrZoneServerOnlineNonempty
	}
	payload := map[string]any{"source": "v1", "action": "unassign", "namespace": namespace, "serverId": serverID, "expectedFound": true, "expectedGroup": current.GroupCode, "expectedZone": current.ZoneCode, "operator": operator, "clientIP": clientIP}
	return s.requestLegacyTopologyApproval(authz.OperationTopologyServerRezone, namespace, serverID, idempotencyKey, reason, clientIP, payload, principal)
}

// RequestLegacyUndrain 为 V1 取消排空创建统一审批请求。
func (s *V2ControlPlaneService) RequestLegacyUndrain(namespace, serverID, reason, operator, clientIP, idempotencyKey string, principal auth.Principal) (ApprovalTicketView, error) {
	if s.legacyScheduling == nil || namespace == "" || serverID == "" {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	current, err := s.legacyScheduling.drainRepo.FindByServer(namespace, serverID)
	if err != nil || current == nil {
		return ApprovalTicketView{}, apperr.ErrDrainNotFound
	}
	payload := map[string]any{"source": "v1", "action": "undrain", "namespace": namespace, "serverId": serverID, "expectedFound": true, "operator": operator, "clientIP": clientIP}
	return s.requestLegacyTopologyApproval(authz.OperationTopologyDrainingDisable, namespace, serverID, idempotencyKey, reason, clientIP, payload, principal)
}

func (s *V2ControlPlaneService) requestLegacyTopologyApproval(kind, namespace, serverID, idempotencyKey, reason, clientIP string, payload map[string]any, principal auth.Principal) (ApprovalTicketView, error) {
	if s.approval == nil || strings.TrimSpace(reason) == "" {
		return ApprovalTicketView{}, apperr.ErrApprovalReasonRequired
	}
	var ns model.Namespace
	if err := s.db.Where("code = ?", namespace).First(&ns).Error; err != nil {
		return ApprovalTicketView{}, apperr.ErrApprovalTargetChanged
	}
	key := idempotencyKey
	if key == "" {
		key = approvalIdempotencyKey(kind, namespace+"/"+serverID, payload)
	}
	created, err := s.approval.Request(authz.Operation{Kind: kind, NamespaceID: &ns.ID, Resource: "legacy_topology", ResourceID: namespace + "/" + serverID, IdempotencyKey: key, RiskLevel: "high", Reason: reason, EvidenceSnapshot: []authz.ApprovalEvidenceLine{{Label: "命名空间", Value: namespace}, {Label: "服务", Value: serverID}}}, payload, principal, clientIP)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	return ApprovalTicketView{ApprovalRequestID: created.RequestID, Status: created.Status, OperationKey: created.OperationKey}, nil
}

func (s *V2ControlPlaneService) RequestRezoneServers(p RezoneServersParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	if p.TargetKind == "" || !validAssignmentApprovalTarget(p.TargetKind, p.TargetID) {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	payload := baseApprovalPayload(authz.OperationTopologyServerRezone, p.Operator, p.ClientIP)
	payload["serverIds"] = sortedUintCopy(p.ServerIDs)
	payload["targetKind"] = p.TargetKind
	payload["targetId"] = p.TargetID
	payload["serverSnapshot"] = s.serverSnapshot(p.ServerIDs)
	return s.requestApproval(authz.OperationTopologyServerRezone, model.TargetTypeServer, uintListKey(p.ServerIDs), idempotencyKey, p.Reason, p.ClientIP, payload, principal)
}

func (s *V2ControlPlaneService) RequestSetServerDefaultEntry(p SetServerDefaultEntryParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	payload := baseApprovalPayload(authz.OperationTopologyDefaultEntryChange, p.Operator, p.ClientIP)
	payload["serverRowId"] = p.ServerRowID
	payload["value"] = p.Value
	payload["serverSnapshot"] = s.serverSnapshot([]uint{p.ServerRowID})
	return s.requestApproval(authz.OperationTopologyDefaultEntryChange, model.TargetTypeServer, strconv.FormatUint(uint64(p.ServerRowID), 10), idempotencyKey, p.Reason, p.ClientIP, payload, principal)
}

func (s *V2ControlPlaneService) RequestTransferServerPlacement(p ServerPlacementTransferParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	if !validPlacementTarget(p.TargetKind, p.TargetID) {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	payload := baseApprovalPayload(authz.OperationTopologyLobbyMemberMove, p.Operator, p.ClientIP)
	payload["serverId"] = p.ServerID
	payload["targetKind"] = p.TargetKind
	payload["targetId"] = p.TargetID
	payload["serverSnapshot"] = s.serverSnapshotByServerID(p.ServerID)
	return s.requestApproval(authz.OperationTopologyLobbyMemberMove, model.TargetTypeServer, p.ServerID, idempotencyKey, p.Reason, p.ClientIP, payload, principal)
}

func (s *V2ControlPlaneService) RequestDisableServerDraining(p SetServerDrainingParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	if p.Draining {
		return ApprovalTicketView{}, apperr.ErrInvalidParam
	}
	payload := baseApprovalPayload(authz.OperationTopologyDrainingDisable, p.Operator, p.ClientIP)
	payload["serverId"] = p.ServerID
	payload["draining"] = p.Draining
	payload["serverSnapshot"] = s.serverSnapshotByServerID(p.ServerID)
	return s.requestApproval(authz.OperationTopologyDrainingDisable, model.TargetTypeServer, p.ServerID, idempotencyKey, p.Reason, p.ClientIP, payload, principal)
}

func (s *V2ControlPlaneService) identityStatus(identityID string) (string, error) {
	ident, err := findIdentityByID(s.db, identityID)
	if err != nil {
		return "", err
	}
	if ident == nil {
		return "", apperr.ErrInstanceNotFound
	}
	return ident.Status, nil
}

func (s *V2ControlPlaneService) namespaceTrustActive(from, to uint, capability string) bool {
	if from == 0 || to == 0 || capability == "" {
		return false
	}
	var trust model.NamespaceTrust
	err := s.db.Where("from_namespace_id = ? AND to_namespace_id = ? AND capability = ? AND status = ?", from, to, capability, model.NamespaceTrustStatusActive).First(&trust).Error
	return err == nil
}

type serverTopologySnapshot struct {
	ID                 uint   `json:"id"`
	ServerID           string `json:"serverId"`
	NamespaceID        uint   `json:"namespaceId"`
	ZoneID             *uint  `json:"zoneId"`
	BCClusterID        *uint  `json:"bcClusterId"`
	LobbyClusterID     *uint  `json:"lobbyClusterId"`
	PendingZoneID      *uint  `json:"pendingZoneId"`
	PendingBCClusterID *uint  `json:"pendingBCClusterId"`
	IsDefaultEntry     bool   `json:"isDefaultEntry"`
	Draining           bool   `json:"draining"`
}

func (s *V2ControlPlaneService) serverSnapshot(ids []uint) []serverTopologySnapshot {
	if len(ids) == 0 {
		return nil
	}
	var servers []model.Server
	if err := s.db.Where("id IN ?", ids).Order("id").Find(&servers).Error; err != nil {
		return nil
	}
	out := make([]serverTopologySnapshot, 0, len(servers))
	for i := range servers {
		out = append(out, serverSnapshotValue(&servers[i]))
	}
	return out
}

func (s *V2ControlPlaneService) serverSnapshotByServerID(serverID string) serverTopologySnapshot {
	server, err := findServerByServerID(s.db, serverID)
	if err != nil {
		return serverTopologySnapshot{}
	}
	return serverSnapshotValue(server)
}

func serverSnapshotValue(server *model.Server) serverTopologySnapshot {
	return serverTopologySnapshot{ID: server.ID, ServerID: server.ServerID, NamespaceID: server.NamespaceID,
		ZoneID: copyUintPointer(server.ZoneID), BCClusterID: copyUintPointer(server.BCClusterID), LobbyClusterID: copyUintPointer(server.LobbyClusterID),
		PendingZoneID: copyUintPointer(server.PendingZoneID), PendingBCClusterID: copyUintPointer(server.PendingBCClusterID),
		IsDefaultEntry: server.IsDefaultEntry, Draining: server.Draining}
}

func copyUintPointer(v *uint) *uint {
	if v == nil {
		return nil
	}
	value := *v
	return &value
}

func (s *V2ControlPlaneService) ensureTopologySnapshot(expected []serverTopologySnapshot) error {
	if len(expected) == 0 {
		return apperr.ErrApprovalTargetChanged
	}
	ids := make([]uint, 0, len(expected))
	for _, snapshot := range expected {
		if snapshot.ID == 0 {
			return apperr.ErrApprovalTargetChanged
		}
		ids = append(ids, snapshot.ID)
	}
	actual := s.serverSnapshot(ids)
	if len(actual) != len(expected) || !reflect.DeepEqual(actual, expected) {
		return apperr.ErrApprovalTargetChanged
	}
	return nil
}

func sortedUintCopy(in []uint) []uint {
	out := append([]uint(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func uintListKey(ids []uint) string {
	ids = sortedUintCopy(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatUint(uint64(id), 10))
	}
	return strings.Join(parts, ",")
}

func validAssignmentApprovalTarget(kind string, id uint) bool {
	if kind == "" {
		return id == 0
	}
	return id != 0 && model.IsValidAssignmentTarget(kind)
}

type identityApprovePayload struct {
	IdentityID          string `json:"identityId"`
	ExpectedStatus      string `json:"expectedStatus"`
	ServerID            string `json:"serverId"`
	ForceUnbindOccupier bool   `json:"forceUnbindOccupier"`
	TargetExplicitNull  bool   `json:"targetExplicitNull"`
	TargetKind          string `json:"targetKind"`
	TargetID            *uint  `json:"targetId"`
	Operator            string `json:"operator"`
	ClientIP            string `json:"clientIP"`
}

type identityTransitionPayload struct {
	IdentityID     string `json:"identityId"`
	ExpectedStatus string `json:"expectedStatus"`
	Reason         string `json:"reason"`
	Operator       string `json:"operator"`
	ClientIP       string `json:"clientIP"`
}

type identityResolveConflictPayload struct {
	IdentityID     string `json:"identityId"`
	ExpectedStatus string `json:"expectedStatus"`
	KeepBootID     string `json:"keepBootId"`
	Operator       string `json:"operator"`
	ClientIP       string `json:"clientIP"`
}

type namespaceTrustGrantPayload struct {
	FromNamespaceID uint   `json:"fromNamespaceId"`
	ToNamespaceID   uint   `json:"toNamespaceId"`
	Capability      string `json:"capability"`
	Note            string `json:"note"`
	ExpectedActive  bool   `json:"expectedActive"`
	Operator        string `json:"operator"`
	ClientIP        string `json:"clientIP"`
}

type serverAssignPayload struct {
	ServerIDs      []uint                   `json:"serverIds"`
	TargetKind     string                   `json:"targetKind"`
	TargetID       uint                     `json:"targetId"`
	IsDefaultEntry bool                     `json:"isDefaultEntry"`
	ServerSnapshot []serverTopologySnapshot `json:"serverSnapshot"`
	Operator       string                   `json:"operator"`
	ClientIP       string                   `json:"clientIP"`
}

type serverRezonePayload struct {
	ServerIDs      []uint                   `json:"serverIds"`
	TargetKind     string                   `json:"targetKind"`
	TargetID       uint                     `json:"targetId"`
	ServerSnapshot []serverTopologySnapshot `json:"serverSnapshot"`
	Operator       string                   `json:"operator"`
	ClientIP       string                   `json:"clientIP"`
}

type defaultEntryPayload struct {
	ServerRowID    uint                     `json:"serverRowId"`
	Value          bool                     `json:"value"`
	ServerSnapshot []serverTopologySnapshot `json:"serverSnapshot"`
	Operator       string                   `json:"operator"`
	ClientIP       string                   `json:"clientIP"`
}

type placementTransferPayload struct {
	ServerID       string                 `json:"serverId"`
	TargetKind     string                 `json:"targetKind"`
	TargetID       uint                   `json:"targetId"`
	ServerSnapshot serverTopologySnapshot `json:"serverSnapshot"`
	Operator       string                 `json:"operator"`
	ClientIP       string                 `json:"clientIP"`
}

type drainingPayload struct {
	ServerID       string                 `json:"serverId"`
	Draining       bool                   `json:"draining"`
	ServerSnapshot serverTopologySnapshot `json:"serverSnapshot"`
	Operator       string                 `json:"operator"`
	ClientIP       string                 `json:"clientIP"`
}

func (s *V2ControlPlaneService) approveAgentIdentityApproved(p identityApprovePayload, permit authz.Permit) error {
	if err := s.ensureIdentityStatus(p.IdentityID, p.ExpectedStatus); err != nil {
		return err
	}
	_, err := s.ApproveAgentIdentityApproved(p.IdentityID, ApproveAgentIdentityParams{
		ServerID: p.ServerID, ForceUnbindOccupier: p.ForceUnbindOccupier,
		TargetExplicitNull: p.TargetExplicitNull, TargetKind: p.TargetKind, TargetID: p.TargetID,
		Operator: p.Operator, ClientIP: p.ClientIP,
	}, permit)
	return err
}

func (s *V2ControlPlaneService) transitionIdentityApproved(p identityTransitionPayload, kind string, permit authz.Permit) error {
	if err := s.ensureIdentityStatus(p.IdentityID, p.ExpectedStatus); err != nil {
		return err
	}
	params := IdentityTransitionParams{Reason: p.Reason, Operator: p.Operator, ClientIP: p.ClientIP}
	var err error
	switch kind {
	case authz.OperationIdentityUnbind:
		_, err = s.UnbindAgentIdentityApproved(p.IdentityID, params, permit)
	case authz.OperationIdentityAllowReapply:
		_, err = s.AllowAgentIdentityReapplyApproved(p.IdentityID, params, permit)
	default:
		_, err = s.EnableAgentIdentityApproved(p.IdentityID, params, permit)
	}
	return err
}

func (s *V2ControlPlaneService) resolveConflictApproved(p identityResolveConflictPayload, permit authz.Permit) error {
	if err := s.ensureIdentityStatus(p.IdentityID, p.ExpectedStatus); err != nil {
		return err
	}
	_, err := s.ResolveAgentIdentityConflictApproved(p.IdentityID, ResolveConflictParams{KeepBootID: p.KeepBootID, Reason: "approved", Operator: p.Operator, ClientIP: p.ClientIP}, permit)
	return err
}

func (s *V2ControlPlaneService) grantNamespaceTrustApproved(p namespaceTrustGrantPayload, permit authz.Permit) error {
	if s.namespaceTrustActive(p.FromNamespaceID, p.ToNamespaceID, p.Capability) != p.ExpectedActive {
		return apperr.ErrApprovalTargetChanged
	}
	_, err := s.GrantNamespaceTrustApproved(GrantNamespaceTrustParams{
		FromNamespaceID: p.FromNamespaceID, ToNamespaceID: p.ToNamespaceID, Capability: p.Capability, Note: p.Note,
		Operator: p.Operator, ClientIP: p.ClientIP,
	}, permit)
	return err
}

func (s *V2ControlPlaneService) assignServersApproved(p serverAssignPayload, permit authz.Permit) error {
	_, err := s.AssignServersApproved(AssignServersParams{
		ServerIDs: p.ServerIDs, TargetKind: p.TargetKind, TargetID: p.TargetID, IsDefaultEntry: p.IsDefaultEntry,
		Reason: "approved", Operator: p.Operator, ClientIP: p.ClientIP,
	}, permit)
	return err
}

func (s *V2ControlPlaneService) rezoneServersApproved(p serverRezonePayload, permit authz.Permit) error {
	_, err := s.RezoneServersApproved(RezoneServersParams{ServerIDs: p.ServerIDs, TargetKind: p.TargetKind, TargetID: p.TargetID, Reason: "approved", Operator: p.Operator, ClientIP: p.ClientIP}, permit)
	return err
}

func (s *V2ControlPlaneService) defaultEntryApproved(p defaultEntryPayload, permit authz.Permit) error {
	_, err := s.SetServerDefaultEntryApproved(SetServerDefaultEntryParams{ServerRowID: p.ServerRowID, Value: p.Value, Operator: p.Operator, ClientIP: p.ClientIP}, permit)
	return err
}

func (s *V2ControlPlaneService) placementTransferApproved(p placementTransferPayload, permit authz.Permit) error {
	_, err := s.TransferServerPlacementApproved(ServerPlacementTransferParams{ServerID: p.ServerID, TargetKind: p.TargetKind, TargetID: p.TargetID, Reason: "approved", Operator: p.Operator, ClientIP: p.ClientIP}, permit)
	return err
}

func (s *V2ControlPlaneService) drainingDisableApproved(p drainingPayload, permit authz.Permit) error {
	if p.Draining {
		return apperr.ErrInvalidParam
	}
	_, err := s.SetServerDrainingApproved(SetServerDrainingParams{ServerID: p.ServerID, Draining: false, Reason: "approved", Operator: p.Operator, ClientIP: p.ClientIP}, permit)
	return err
}

func (s *V2ControlPlaneService) ensureIdentityStatus(identityID, expected string) error {
	if expected == "" {
		return nil
	}
	status, err := s.identityStatus(identityID)
	if err != nil {
		return err
	}
	if status != expected {
		return apperr.ErrApprovalTargetChanged
	}
	return nil
}

func ensurePermit(permit authz.Permit, operation string) error {
	if permit.Operation() != operation || permit.RequestID() == "" || permit.PayloadHash() == "" || permit.SchemaVersion() != approvalSchemaVersion || permit.LeaseToken() == "" {
		return apperr.ErrForbidden
	}
	return nil
}

// ensureRequestPermit 确保审批请求与不可伪造的执行许可属于同一次执行。
func ensureRequestPermit(req authz.ApprovalRequest, permit authz.Permit) error {
	if err := ensurePermit(permit, req.Operation.Kind); err != nil ||
		req.RequestID == "" || req.RequestID != permit.RequestID() ||
		req.PayloadHash == "" || req.PayloadHash != permit.PayloadHash() ||
		req.SchemaVersion != permit.SchemaVersion() || req.Version != permit.Version() ||
		req.LeaseOwner == "" || req.LeaseOwner != permit.LeaseToken() {
		return apperr.ErrForbidden
	}
	return nil
}

func (s *V2ControlPlaneService) ApproveAgentIdentityApproved(identityID string, p ApproveAgentIdentityParams, permit authz.Permit) (*model.AgentIdentity, error) {
	if err := ensurePermit(permit, authz.OperationIdentityApprove); err != nil {
		return nil, err
	}
	return s.applyApproveAgentIdentity(identityID, p)
}

func (s *V2ControlPlaneService) UnbindAgentIdentityApproved(identityID string, p IdentityTransitionParams, permit authz.Permit) (*model.AgentIdentity, error) {
	if err := ensurePermit(permit, authz.OperationIdentityUnbind); err != nil {
		return nil, err
	}
	return s.applyTransitionIdentity(identityID, []string{model.AgentIdentityStatusActive, model.AgentIdentityStatusDisabled, model.AgentIdentityStatusConflict}, model.AgentIdentityStatusUnbound, model.ActionIdentityUnbound, p)
}

func (s *V2ControlPlaneService) EnableAgentIdentityApproved(identityID string, p IdentityTransitionParams, permit authz.Permit) (*model.AgentIdentity, error) {
	if err := ensurePermit(permit, authz.OperationIdentityEnable); err != nil {
		return nil, err
	}
	return s.applyTransitionIdentity(identityID, []string{model.AgentIdentityStatusDisabled}, model.AgentIdentityStatusActive, model.ActionIdentityEnabled, p)
}

// AllowAgentIdentityReapplyApproved 消费许可恢复已拒绝身份的重新申请资格。
func (s *V2ControlPlaneService) AllowAgentIdentityReapplyApproved(identityID string, p IdentityTransitionParams, permit authz.Permit) (*model.AgentIdentity, error) {
	if err := ensurePermit(permit, authz.OperationIdentityAllowReapply); err != nil {
		return nil, err
	}
	return s.applyTransitionIdentity(identityID, []string{model.AgentIdentityStatusRejected}, model.AgentIdentityStatusExpired, model.ActionIdentityReapplyAllowed, p)
}

func (s *V2ControlPlaneService) ResolveAgentIdentityConflictApproved(identityID string, p ResolveConflictParams, permit authz.Permit) (*model.AgentIdentity, error) {
	if err := ensurePermit(permit, authz.OperationIdentityResolveConflict); err != nil {
		return nil, err
	}
	return s.applyResolveAgentIdentityConflict(identityID, p)
}

func (s *V2ControlPlaneService) GrantNamespaceTrustApproved(p GrantNamespaceTrustParams, permit authz.Permit) (*NamespaceTrustView, error) {
	if err := ensurePermit(permit, authz.OperationNamespaceTrustGrant); err != nil {
		return nil, err
	}
	return s.applyGrantNamespaceTrust(p)
}

func (s *V2ControlPlaneService) AssignServersApproved(p AssignServersParams, permit authz.Permit) ([]model.Server, error) {
	if err := ensurePermit(permit, authz.OperationTopologyServerAssign); err != nil {
		return nil, err
	}
	return s.applyAssignServers(p)
}

func (s *V2ControlPlaneService) RezoneServersApproved(p RezoneServersParams, permit authz.Permit) ([]AssignmentResult, error) {
	if err := ensurePermit(permit, authz.OperationTopologyServerRezone); err != nil {
		return nil, err
	}
	return s.applyRezoneServers(p)
}

func (s *V2ControlPlaneService) SetServerDefaultEntryApproved(p SetServerDefaultEntryParams, permit authz.Permit) (*ServerView, error) {
	if err := ensurePermit(permit, authz.OperationTopologyDefaultEntryChange); err != nil {
		return nil, err
	}
	return s.applySetServerDefaultEntry(p)
}

func (s *V2ControlPlaneService) TransferServerPlacementApproved(p ServerPlacementTransferParams, permit authz.Permit) (*ServerPlacementTransferView, error) {
	if err := ensurePermit(permit, authz.OperationTopologyLobbyMemberMove); err != nil {
		return nil, err
	}
	return s.applyTransferServerPlacement(p)
}

func (s *V2ControlPlaneService) SetServerDrainingApproved(p SetServerDrainingParams, permit authz.Permit) (*ServerView, error) {
	if err := ensurePermit(permit, authz.OperationTopologyDrainingDisable); err != nil {
		return nil, err
	}
	return s.applySetServerDraining(p)
}
