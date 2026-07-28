package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
}

// RegisterV2ControlPlaneApprovalAdapters 注册 V2 控制面危险操作执行适配器。
func RegisterV2ControlPlaneApprovalAdapters(registry *authz.ApprovalRegistry, svc *V2ControlPlaneService) {
	if registry == nil || svc == nil {
		return
	}
	for _, kind := range []string{
		authz.OperationIdentityApprove,
		authz.OperationIdentityUnbind,
		authz.OperationIdentityResolveConflict,
		authz.OperationIdentityEnable,
		authz.OperationNamespaceTrustGrant,
		authz.OperationTopologyServerAssign,
		authz.OperationTopologyServerRezone,
		authz.OperationTopologyDefaultEntryChange,
		authz.OperationTopologyLobbyMemberMove,
		authz.OperationTopologyDrainingDisable,
	} {
		registry.Register(kind, authz.AdapterFunc(svc.executeApprovedV2Operation))
	}
}

func (s *V2ControlPlaneService) executeApprovedV2Operation(req authz.ApprovalRequest, permit authz.Permit) error {
	switch permit.Operation() {
	case authz.OperationIdentityApprove:
		var p identityApprovePayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		return s.approveAgentIdentityApproved(p, permit)
	case authz.OperationIdentityUnbind:
		var p identityTransitionPayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		return s.transitionIdentityApproved(p, authz.OperationIdentityUnbind, permit)
	case authz.OperationIdentityEnable:
		var p identityTransitionPayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		return s.transitionIdentityApproved(p, authz.OperationIdentityEnable, permit)
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
	case authz.OperationTopologyServerAssign:
		var p serverAssignPayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		return s.assignServersApproved(p, permit)
	case authz.OperationTopologyServerRezone:
		var p serverRezonePayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		return s.rezoneServersApproved(p, permit)
	case authz.OperationTopologyDefaultEntryChange:
		var p defaultEntryPayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		return s.defaultEntryApproved(p, permit)
	case authz.OperationTopologyLobbyMemberMove:
		var p placementTransferPayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		return s.placementTransferApproved(p, permit)
	case authz.OperationTopologyDrainingDisable:
		var p drainingPayload
		if err := decodeApprovalPayload(req.Payload, &p); err != nil {
			return err
		}
		return s.drainingDisableApproved(p, permit)
	default:
		return apperr.ErrInvalidParam
	}
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
	created, err := s.approval.Request(authz.Operation{
		Kind: kind, Resource: resource, ResourceID: resourceID,
		IdempotencyKey: key, RiskLevel: "high", Reason: reason,
	}, payload, principal, clientIP)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	return ApprovalTicketView{ApprovalRequestID: created.RequestID, Status: created.Status, OperationKey: created.OperationKey}, nil
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
	if _, err := resolveApprovedServerID(p.ServerID); err != nil {
		return ApprovalTicketView{}, err
	}
	status, err := s.identityStatus(identityID)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	payload := baseApprovalPayload(authz.OperationIdentityApprove, p.Operator, p.ClientIP)
	payload["identityId"] = identityID
	payload["expectedStatus"] = status
	payload["serverId"] = p.ServerID
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

func (s *V2ControlPlaneService) requestIdentityTransitionApproval(kind, identityID string, p IdentityTransitionParams, principal auth.Principal, idempotencyKey string) (ApprovalTicketView, error) {
	status, err := s.identityStatus(identityID)
	if err != nil {
		return ApprovalTicketView{}, err
	}
	payload := baseApprovalPayload(kind, p.Operator, p.ClientIP)
	payload["identityId"] = identityID
	payload["expectedStatus"] = status
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

func (s *V2ControlPlaneService) serverSnapshot(ids []uint) []map[string]any {
	if len(ids) == 0 {
		return nil
	}
	var servers []model.Server
	if err := s.db.Where("id IN ?", ids).Find(&servers).Error; err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(servers))
	for i := range servers {
		out = append(out, serverSnapshotValue(&servers[i]))
	}
	return out
}

func (s *V2ControlPlaneService) serverSnapshotByServerID(serverID string) map[string]any {
	server, err := findServerByServerID(s.db, serverID)
	if err != nil {
		return nil
	}
	return serverSnapshotValue(server)
}

func serverSnapshotValue(server *model.Server) map[string]any {
	return map[string]any{
		"id": server.ID, "serverId": server.ServerID, "namespaceId": server.NamespaceID,
		"zoneId": ptrUintValue(server.ZoneID), "bcClusterId": ptrUintValue(server.BCClusterID), "lobbyClusterId": ptrUintValue(server.LobbyClusterID),
		"pendingZoneId": ptrUintValue(server.PendingZoneID), "pendingBCClusterId": ptrUintValue(server.PendingBCClusterID),
		"isDefaultEntry": server.IsDefaultEntry, "draining": server.Draining,
	}
}

func ptrUintValue(v *uint) any {
	if v == nil {
		return nil
	}
	return *v
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
	ServerIDs      []uint `json:"serverIds"`
	TargetKind     string `json:"targetKind"`
	TargetID       uint   `json:"targetId"`
	IsDefaultEntry bool   `json:"isDefaultEntry"`
	Operator       string `json:"operator"`
	ClientIP       string `json:"clientIP"`
}

type serverRezonePayload struct {
	ServerIDs  []uint `json:"serverIds"`
	TargetKind string `json:"targetKind"`
	TargetID   uint   `json:"targetId"`
	Operator   string `json:"operator"`
	ClientIP   string `json:"clientIP"`
}

type defaultEntryPayload struct {
	ServerRowID uint   `json:"serverRowId"`
	Value       bool   `json:"value"`
	Operator    string `json:"operator"`
	ClientIP    string `json:"clientIP"`
}

type placementTransferPayload struct {
	ServerID   string `json:"serverId"`
	TargetKind string `json:"targetKind"`
	TargetID   uint   `json:"targetId"`
	Operator   string `json:"operator"`
	ClientIP   string `json:"clientIP"`
}

type drainingPayload struct {
	ServerID string `json:"serverId"`
	Draining bool   `json:"draining"`
	Operator string `json:"operator"`
	ClientIP string `json:"clientIP"`
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
	params := IdentityTransitionParams{Operator: p.Operator, ClientIP: p.ClientIP}
	var err error
	if kind == authz.OperationIdentityUnbind {
		_, err = s.UnbindAgentIdentityApproved(p.IdentityID, params, permit)
	} else {
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
	if permit.Operation() != operation || permit.RequestID() == "" || permit.PayloadHash() == "" {
		return apperr.ErrForbidden
	}
	return nil
}

func (s *V2ControlPlaneService) ApproveAgentIdentityApproved(identityID string, p ApproveAgentIdentityParams, permit authz.Permit) (*model.AgentIdentity, error) {
	if err := ensurePermit(permit, authz.OperationIdentityApprove); err != nil {
		return nil, err
	}
	return s.ApproveAgentIdentity(identityID, p)
}

func (s *V2ControlPlaneService) UnbindAgentIdentityApproved(identityID string, p IdentityTransitionParams, permit authz.Permit) (*model.AgentIdentity, error) {
	if err := ensurePermit(permit, authz.OperationIdentityUnbind); err != nil {
		return nil, err
	}
	return s.UnbindAgentIdentity(identityID, p)
}

func (s *V2ControlPlaneService) EnableAgentIdentityApproved(identityID string, p IdentityTransitionParams, permit authz.Permit) (*model.AgentIdentity, error) {
	if err := ensurePermit(permit, authz.OperationIdentityEnable); err != nil {
		return nil, err
	}
	return s.EnableAgentIdentity(identityID, p)
}

func (s *V2ControlPlaneService) ResolveAgentIdentityConflictApproved(identityID string, p ResolveConflictParams, permit authz.Permit) (*model.AgentIdentity, error) {
	if err := ensurePermit(permit, authz.OperationIdentityResolveConflict); err != nil {
		return nil, err
	}
	return s.ResolveAgentIdentityConflict(identityID, p)
}

func (s *V2ControlPlaneService) GrantNamespaceTrustApproved(p GrantNamespaceTrustParams, permit authz.Permit) (*NamespaceTrustView, error) {
	if err := ensurePermit(permit, authz.OperationNamespaceTrustGrant); err != nil {
		return nil, err
	}
	return s.GrantNamespaceTrust(p)
}

func (s *V2ControlPlaneService) AssignServersApproved(p AssignServersParams, permit authz.Permit) ([]model.Server, error) {
	if err := ensurePermit(permit, authz.OperationTopologyServerAssign); err != nil {
		return nil, err
	}
	return s.AssignServers(p)
}

func (s *V2ControlPlaneService) RezoneServersApproved(p RezoneServersParams, permit authz.Permit) ([]AssignmentResult, error) {
	if err := ensurePermit(permit, authz.OperationTopologyServerRezone); err != nil {
		return nil, err
	}
	return s.RezoneServers(p)
}

func (s *V2ControlPlaneService) SetServerDefaultEntryApproved(p SetServerDefaultEntryParams, permit authz.Permit) (*ServerView, error) {
	if err := ensurePermit(permit, authz.OperationTopologyDefaultEntryChange); err != nil {
		return nil, err
	}
	return s.SetServerDefaultEntry(p)
}

func (s *V2ControlPlaneService) TransferServerPlacementApproved(p ServerPlacementTransferParams, permit authz.Permit) (*ServerPlacementTransferView, error) {
	if err := ensurePermit(permit, authz.OperationTopologyLobbyMemberMove); err != nil {
		return nil, err
	}
	return s.TransferServerPlacement(p)
}

func (s *V2ControlPlaneService) SetServerDrainingApproved(p SetServerDrainingParams, permit authz.Permit) (*ServerView, error) {
	if err := ensurePermit(permit, authz.OperationTopologyDrainingDisable); err != nil {
		return nil, err
	}
	return s.SetServerDraining(p)
}
