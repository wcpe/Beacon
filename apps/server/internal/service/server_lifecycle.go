package service

import (
	"encoding/json"
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
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

type serverLifecyclePayload struct {
	SchemaVersion     int                     `json:"schemaVersion"`
	OperationKey      string                  `json:"operationKey"`
	Operator          string                  `json:"operator"`
	ClientIP          string                  `json:"clientIP"`
	ArchiveReason     string                  `json:"archiveReason"`
	ExpectedLifecycle string                  `json:"expectedLifecycle"`
	Server            serverLifecycleSnapshot `json:"server"`
}

type serverLifecycleSnapshot struct {
	ID                 uint                     `json:"id"`
	NamespaceID        uint                     `json:"namespaceId"`
	ServerID           string                   `json:"serverId"`
	DisplayName        string                   `json:"displayName"`
	Kind               string                   `json:"kind"`
	BCClusterID        *uint                    `json:"bcClusterId"`
	ZoneID             *uint                    `json:"zoneId"`
	LobbyClusterID     *uint                    `json:"lobbyClusterId"`
	PendingZoneID      *uint                    `json:"pendingZoneId"`
	PendingBCClusterID *uint                    `json:"pendingBcClusterId"`
	IsDefaultEntry     bool                     `json:"isDefaultEntry"`
	Draining           bool                     `json:"draining"`
	Lifecycle          string                   `json:"lifecycle"`
	Identities         []serverIdentitySnapshot `json:"identities"`
}

type serverIdentitySnapshot struct {
	ID          uint   `json:"id"`
	IdentityID  string `json:"identityId"`
	NamespaceID uint   `json:"namespaceId"`
	ServerID    string `json:"serverId"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
}

// PrepareApprovalRequest 冻结 server 生命周期操作的服务端权威目标。
func (s *V2ControlPlaneService) PrepareApprovalRequest(op authz.Operation, payload map[string]any, principal auth.Principal, clientIP string) (authz.Operation, map[string]any, error) {
	if isNamespaceLifecycleOperation(op.Kind) {
		return s.prepareNamespaceLifecycleApproval(op, payload, principal, clientIP)
	}
	if !isServerLifecycleOperation(op.Kind) {
		return op, payload, nil
	}
	serverID, err := lifecycleServerRowID(payload)
	if err != nil {
		return op, nil, err
	}
	snapshot, err := s.serverLifecycleSnapshot(serverID)
	if err != nil {
		return op, nil, err
	}
	if err := validateServerLifecycleRequest(op.Kind, snapshot.Lifecycle); err != nil {
		return op, nil, err
	}
	principal = auth.NormalizePrincipal(principal)
	op.Resource = model.TargetTypeServer
	op.ResourceID = strconv.FormatUint(uint64(serverID), 10)
	op.RiskLevel = "high"
	return op, serverLifecyclePayloadMap(op, snapshot, principal.AuditRef(), clientIP), nil
}

func isServerLifecycleOperation(operation string) bool {
	return operation == authz.OperationServerArchive || operation == authz.OperationServerRestore || operation == authz.OperationServerPermanentDelete
}

func lifecycleServerRowID(payload map[string]any) (uint, error) {
	if parameters, ok := payload["parameters"].(map[string]any); ok {
		return lifecycleUint(parameters["serverRowId"])
	}
	return lifecycleUint(payload["serverRowId"])
}

func lifecycleUint(value any) (uint, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return 0, apperr.ErrInvalidParam
	}
	var id uint64
	if err := json.Unmarshal(body, &id); err != nil || id == 0 || uint64(uint(id)) != id {
		return 0, apperr.ErrInvalidParam
	}
	return uint(id), nil
}

func validateServerLifecycleRequest(operation, lifecycle string) error {
	if operation == authz.OperationServerArchive && lifecycle != model.ServerLifecycleActive {
		return apperr.ErrServerNotActive
	}
	if operation == authz.OperationServerRestore && lifecycle != model.ServerLifecycleArchived {
		return apperr.ErrServerNotArchived
	}
	if operation == authz.OperationServerPermanentDelete && lifecycle != model.ServerLifecycleArchived {
		return apperr.ErrServerNotArchived
	}
	return nil
}

func serverLifecyclePayloadMap(op authz.Operation, snapshot serverLifecycleSnapshot, operator, clientIP string) map[string]any {
	return map[string]any{
		"schemaVersion": approvalSchemaVersion, "operationKey": op.Kind,
		"operator": operator, "clientIP": clientIP, "archiveReason": strings.TrimSpace(op.Reason),
		"expectedLifecycle": snapshot.Lifecycle, "server": snapshot,
	}
}

func (s *V2ControlPlaneService) serverLifecycleSnapshot(id uint) (serverLifecycleSnapshot, error) {
	return loadServerLifecycleSnapshot(s.db, id)
}

func loadServerLifecycleSnapshot(db *gorm.DB, id uint) (serverLifecycleSnapshot, error) {
	var server model.Server
	if err := db.First(&server, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return serverLifecycleSnapshot{}, apperr.ErrInstanceNotFound
		}
		return serverLifecycleSnapshot{}, err
	}
	var identities []model.AgentIdentity
	if err := db.Where("namespace_id = ? AND server_id = ?", server.NamespaceID, server.ServerID).Order("identity_id, id").Find(&identities).Error; err != nil {
		return serverLifecycleSnapshot{}, err
	}
	return newServerLifecycleSnapshot(&server, identities), nil
}

func newServerLifecycleSnapshot(server *model.Server, identities []model.AgentIdentity) serverLifecycleSnapshot {
	snapshot := serverLifecycleSnapshot{
		ID: server.ID, NamespaceID: server.NamespaceID, ServerID: server.ServerID, DisplayName: server.DisplayName, Kind: server.Kind,
		BCClusterID: server.BCClusterID, ZoneID: server.ZoneID, LobbyClusterID: server.LobbyClusterID,
		PendingZoneID: server.PendingZoneID, PendingBCClusterID: server.PendingBCClusterID,
		IsDefaultEntry: server.IsDefaultEntry, Draining: server.Draining, Lifecycle: serverLifecycleValue(server),
		Identities: make([]serverIdentitySnapshot, 0, len(identities)),
	}
	for _, identity := range identities {
		snapshot.Identities = append(snapshot.Identities, serverIdentitySnapshot{
			ID: identity.ID, IdentityID: identity.IdentityID, NamespaceID: identity.NamespaceID,
			ServerID: string(identity.ServerID), Kind: identity.Kind, Status: identity.Status,
		})
	}
	return snapshot
}

func serverLifecycleValue(server *model.Server) string {
	if server.Lifecycle == "" {
		return model.ServerLifecycleActive
	}
	return server.Lifecycle
}

func isServerActive(server *model.Server) bool {
	return server != nil && serverLifecycleValue(server) == model.ServerLifecycleActive
}

func ensureServerActive(db *gorm.DB, namespaceID uint, serverID string) error {
	if err := ensureNamespaceRuntimeActiveByID(db, namespaceID); err != nil {
		return err
	}
	if serverID == "" || !db.Migrator().HasTable(&model.Server{}) {
		return nil
	}
	var server model.Server
	if err := db.Where("namespace_id = ? AND server_id = ?", namespaceID, serverID).First(&server).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if !isServerActive(&server) {
		return apperr.ErrServerArchived
	}
	return nil
}

func ensureServerActiveForNamespace(db *gorm.DB, namespaceCode, serverID string) error {
	if namespaceCode == "" || serverID == "" || !db.Migrator().HasTable(&model.Namespace{}) {
		return nil
	}
	var namespace model.Namespace
	if err := db.Where("code = ?", namespaceCode).First(&namespace).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if err := ensureNamespaceRuntimeActive(&namespace); err != nil {
		return err
	}
	return ensureServerActive(db, namespace.ID, serverID)
}

// ensureNamespaceRuntimeActiveByID 是全部运行写入、投递与候选选择共用的环境资格闸。
// 历史元数据查询不得调用本函数，避免把只读观察误收紧为审批或运行资格检查。
func ensureNamespaceRuntimeActiveByID(db *gorm.DB, namespaceID uint) error {
	if namespaceID == 0 || !db.Migrator().HasTable(&model.Namespace{}) {
		return nil
	}
	var namespace model.Namespace
	if err := db.First(&namespace, namespaceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.ErrNamespaceNotFound
		}
		return err
	}
	return ensureNamespaceRuntimeActive(&namespace)
}

// ensureNamespaceRuntimeActiveByCode 按稳定 code 校验运行资格。
func ensureNamespaceRuntimeActiveByCode(db *gorm.DB, namespaceCode string) error {
	if namespaceCode == "" || !db.Migrator().HasTable(&model.Namespace{}) {
		return nil
	}
	var namespace model.Namespace
	if err := db.Where("code = ?", namespaceCode).First(&namespace).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.ErrNamespaceNotFound
		}
		return err
	}
	return ensureNamespaceRuntimeActive(&namespace)
}

func ensureNamespaceRuntimeActive(namespace *model.Namespace) error {
	if !isNamespaceActive(namespace) {
		return apperr.ErrNamespaceArchived
	}
	return nil
}

// ensureIdentityRuntimeBindingOpen 阻断已由永久墓碑关闭的身份资产绑定。
// 墓碑保留 serverId 供历史审计，不能将该历史绑定误作可重新注册或确认的运行资格。
func ensureIdentityRuntimeBindingOpen(identity *model.AgentIdentity) error {
	if identity != nil && identity.AssetBindingClosedAt != nil {
		return apperr.ErrServerArchived
	}
	return nil
}

func (s *V2ControlPlaneService) archiveServerApproved(payload serverLifecyclePayload, permit authz.Permit) error {
	return s.applyServerLifecycle(payload, permit, authz.OperationServerArchive)
}

func (s *V2ControlPlaneService) restoreServerApproved(payload serverLifecyclePayload, permit authz.Permit) error {
	return s.applyServerLifecycle(payload, permit, authz.OperationServerRestore)
}

func (s *V2ControlPlaneService) applyServerLifecycle(payload serverLifecyclePayload, permit authz.Permit, operation string) error {
	if err := ensurePermit(permit, operation); err != nil || payload.OperationKey != operation {
		return apperr.ErrForbidden
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		current, err := loadServerLifecycleSnapshot(tx, payload.Server.ID)
		if err != nil || !reflect.DeepEqual(current, payload.Server) || current.Lifecycle != payload.ExpectedLifecycle {
			return lifecycleTargetChanged(err)
		}
		return updateServerLifecycle(tx, payload, permit, operation)
	})
	if err != nil {
		return err
	}
	if operation == authz.OperationServerArchive || operation == authz.OperationServerPermanentDelete {
		s.scheduleRuntimeServerEviction(payload.Server.NamespaceID, payload.Server.ServerID)
	}
	return nil
}

// scheduleRuntimeServerEviction 在生命周期写入提交后摘除运行实例，恢复不自动复活旧注册。
func (s *V2ControlPlaneService) scheduleRuntimeServerEviction(namespaceID uint, serverID string) {
	if s.runtime == nil || namespaceID == 0 || serverID == "" {
		return
	}
	var namespace model.Namespace
	if err := s.db.First(&namespace, namespaceID).Error; err != nil {
		return
	}
	s.scheduleRuntimeEviction(namespace.Code, []string{serverID})
}

// scheduleRuntimeNamespaceEviction 在环境归档或墓碑提交后摘除其全部运行实例。
func (s *V2ControlPlaneService) scheduleRuntimeNamespaceEviction(namespace string, impact namespaceLifecycleImpact) {
	serverIDs := make([]string, 0, len(impact.Servers))
	for _, server := range impact.Servers {
		serverIDs = append(serverIDs, server.ServerID)
	}
	s.scheduleRuntimeEviction(namespace, serverIDs)
}

func (s *V2ControlPlaneService) scheduleRuntimeEviction(namespace string, serverIDs []string) {
	if s.runtime == nil || namespace == "" || len(serverIDs) == 0 {
		return
	}
	ids := append([]string(nil), serverIDs...)
	s.scheduleAfterCommit(func() {
		for _, serverID := range ids {
			s.runtime.Offline(namespace, serverID)
		}
		if s.notifier != nil {
			s.notifier.NotifyTopologyChange(namespace)
		}
	})
}

func lifecycleTargetChanged(err error) error {
	if err != nil && !errors.Is(err, apperr.ErrInstanceNotFound) {
		return err
	}
	return apperr.ErrApprovalTargetChanged
}

func updateServerLifecycle(tx *gorm.DB, payload serverLifecyclePayload, permit authz.Permit, operation string) error {
	now := time.Now().UTC()
	updates := lifecycleUpdates(payload, operation, now)
	query := tx.Model(&model.Server{}).Where("id = ?", payload.Server.ID)
	if payload.ExpectedLifecycle == model.ServerLifecycleActive {
		query = query.Where("(lifecycle = ? OR lifecycle = '')", payload.ExpectedLifecycle)
	} else {
		query = query.Where("lifecycle = ?", payload.ExpectedLifecycle)
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return apperr.ErrApprovalTargetChanged
	}
	if operation == authz.OperationServerArchive || operation == authz.OperationServerPermanentDelete {
		if err := expireServerCommands(tx, payload.Server.NamespaceID, payload.Server.ServerID); err != nil {
			return err
		}
	}
	return createAudit(tx, serverLifecycleAudit(payload, permit, operation))
}

func expireServerCommands(tx *gorm.DB, namespaceID uint, serverID string) error {
	var namespace model.Namespace
	if err := tx.First(&namespace, namespaceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	_, err := repository.NewAgentCommandRepository(tx).ExpireForTarget(namespace.Code, serverID)
	return err
}

func lifecycleUpdates(payload serverLifecyclePayload, operation string, now time.Time) map[string]any {
	if operation == authz.OperationServerArchive {
		return map[string]any{
			"lifecycle": model.ServerLifecycleArchived, "archived_at": &now,
			"archived_by": payload.Operator, "archive_reason": payload.ArchiveReason,
		}
	}
	if operation == authz.OperationServerPermanentDelete {
		return map[string]any{
			"lifecycle": model.ServerLifecycleTombstoned, "tombstoned_at": &now,
			"is_default_entry": false, "draining": false, "zone_id": nil, "bc_cluster_id": nil,
			"lobby_cluster_id": nil, "pending_zone_id": nil, "pending_bc_cluster_id": nil,
		}
	}
	return map[string]any{
		"lifecycle": model.ServerLifecycleActive, "archived_at": nil,
		"archived_by": "", "archive_reason": "",
	}
}

func serverLifecycleAudit(payload serverLifecyclePayload, permit authz.Permit, operation string) model.AuditLog {
	detail := map[string]any{"requestId": permit.RequestID(), "serverRowId": payload.Server.ID, "lifecycle": payload.ExpectedLifecycle}
	if operation == authz.OperationServerArchive {
		detail["reason"] = payload.ArchiveReason
	}
	return model.AuditLog{
		Operator: payload.Operator, Action: operation, TargetType: model.TargetTypeServer,
		TargetRef: fmt.Sprintf("%d", payload.Server.ID), Detail: auditJSON(detail),
		Result: model.ResultOK, ClientIP: payload.ClientIP,
	}
}
