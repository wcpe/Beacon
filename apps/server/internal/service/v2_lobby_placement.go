package service

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
)

const (
	// LobbyPlacementKind 是大厅归属在迁移契约中的目标类型。
	LobbyPlacementKind = "lobby_cluster"

	actionLobbyMemberAssign   = "lobby_cluster.member.assign"
	actionLobbyMemberMoveIn   = "lobby_cluster.member.move_in"
	actionLobbyMemberMoveOut  = "lobby_cluster.member.move_out"
	actionLobbyMemberUnassign = "lobby_cluster.member.unassign"
)

var (
	errLobbyClusterNotFound          = apperr.New(404, "LOBBY_CLUSTER_NOT_FOUND", "大厅集群不存在")
	errLobbyClusterNamespaceConflict = apperr.New(409, "LOBBY_CLUSTER_NAMESPACE_CONFLICT", "服务器与大厅集群不属于同一 namespace")
	errLobbyMemberRoleInvalid        = apperr.New(409, "LOBBY_MEMBER_ROLE_INVALID", "仅已确认的 backend 服务器可以加入大厅集群")
	errServerPlacementConflict       = apperr.New(409, "SERVER_PLACEMENT_CONFLICT", "当前服务器归属不支持此迁移，请使用对应的区服操作")
)

// SetRuntimeRegistry 注入注册/玩家运行态；未注入时迁移按离线处理，便于独立服务测试。
func (s *V2ControlPlaneService) SetRuntimeRegistry(registry *runtime.Registry) {
	s.runtime = registry
}

// SetHealthViews 注入健康视图单一真源；未注入或无快照时大厅成员保守地不可调度。
func (s *V2ControlPlaneService) SetHealthViews(views *healthview.Store) {
	s.healthViews = views
}

// SetChangeNotifier 注入提交后拓扑唤醒器；未注入时只完成权威写入。
func (s *V2ControlPlaneService) SetChangeNotifier(notifier *ChangeNotifier) {
	s.notifier = notifier
}

// ServerPlacementTransferParams 是大厅成员与小区之间的单服原子迁移请求。
type ServerPlacementTransferParams struct {
	ServerID   string
	TargetKind string
	TargetID   uint
	Reason     string
	Operator   string
	ClientIP   string
}

// ServerPlacementTransferView 是迁移完成后的最小归属响应。
type ServerPlacementTransferView struct {
	ServerID       string `json:"serverId"`
	NamespaceID    uint   `json:"namespaceId"`
	PlacementKind  string `json:"placementKind"`
	LobbyClusterID *uint  `json:"lobbyClusterId"`
	ZoneID         *uint  `json:"zoneId"`
	IsDefaultEntry bool   `json:"isDefaultEntry"`
	Draining       bool   `json:"draining"`
}

// TransferServerPlacement 在大厅、业务小区和未分配之间原子迁移单台 backend。
// Zone 到 Zone 仍由换区工单处理，绝不在这里驱动身份重新确认。
func (s *V2ControlPlaneService) TransferServerPlacement(p ServerPlacementTransferParams) (*ServerPlacementTransferView, error) {
	if strings.TrimSpace(p.ServerID) == "" || strings.TrimSpace(p.Reason) == "" || len(p.Reason) > 255 {
		return nil, apperr.ErrInvalidParam
	}
	if !validPlacementTarget(p.TargetKind, p.TargetID) {
		return nil, apperr.ErrInvalidParam
	}
	var result *ServerPlacementTransferView
	var changedNamespace string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		server, err := loadPlacementServerForUpdate(tx, p.ServerID)
		if err != nil {
			return err
		}
		namespace, err := loadPlacementNamespace(tx, server.NamespaceID)
		if err != nil {
			return err
		}
		current, err := placementKindOf(server)
		if err != nil {
			return err
		}
		target, err := resolvePlacementTarget(tx, p, server.NamespaceID)
		if err != nil {
			return err
		}
		if placementIsSame(current, target) {
			result = placementTransferViewOf(server)
			return nil
		}
		if !placementTransitionAllowed(current, target) {
			return errServerPlacementConflict
		}
		if target.kind == LobbyPlacementKind && !isConfirmedBackend(tx, server) {
			return errLobbyMemberRoleInvalid
		}
		if s.isPlacementOnlineNonempty(namespace.Code, server.ServerID) {
			return apperr.ErrZoneServerOnlineNonempty
		}

		applyPlacementTarget(server, target)
		if err := tx.Save(server).Error; err != nil {
			return err
		}
		if err := createAudit(tx, model.AuditLog{
			NamespaceCode: namespace.Code,
			Operator:      operatorOrSystem(p.Operator),
			Action:        lobbyPlacementAuditAction(current, target),
			TargetType:    model.TargetTypeServer,
			TargetRef:     server.ServerID,
			Detail:        lobbyPlacementAuditDetail(server.ServerID, current, target, strings.TrimSpace(p.Reason)),
			Result:        model.ResultOK,
			ClientIP:      p.ClientIP,
		}); err != nil {
			return err
		}
		changedNamespace = namespace.Code
		result = placementTransferViewOf(server)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if changedNamespace != "" && s.notifier != nil {
		s.notifier.NotifyTopologyChange(changedNamespace)
	}
	return result, nil
}

type placementRef struct {
	kind string
	id   uint
}

func validPlacementTarget(kind string, id uint) bool {
	if kind == "" {
		return id == 0
	}
	return id != 0 && (kind == LobbyPlacementKind || kind == model.AssignmentTargetZone)
}

func loadPlacementServerForUpdate(tx *gorm.DB, serverID string) (*model.Server, error) {
	var server model.Server
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("server_id = ?", serverID).First(&server).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperr.ErrInstanceNotFound
	}
	if err != nil {
		return nil, err
	}
	return &server, nil
}

func loadPlacementNamespace(tx *gorm.DB, namespaceID uint) (*model.Namespace, error) {
	var namespace model.Namespace
	if err := tx.First(&namespace, namespaceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.ErrInstanceNotFound
		}
		return nil, err
	}
	return &namespace, nil
}

func placementKindOf(server *model.Server) (placementRef, error) {
	count := 0
	if server.ZoneID != nil {
		count++
	}
	if server.BCClusterID != nil {
		count++
	}
	if server.LobbyClusterID != nil {
		count++
	}
	if count > 1 || server.BCClusterID != nil {
		return placementRef{}, errServerPlacementConflict
	}
	if server.LobbyClusterID != nil {
		return placementRef{kind: LobbyPlacementKind, id: *server.LobbyClusterID}, nil
	}
	if server.ZoneID != nil {
		return placementRef{kind: model.AssignmentTargetZone, id: *server.ZoneID}, nil
	}
	return placementRef{}, nil
}

func resolvePlacementTarget(tx *gorm.DB, p ServerPlacementTransferParams, namespaceID uint) (placementRef, error) {
	switch p.TargetKind {
	case "":
		return placementRef{}, nil
	case LobbyPlacementKind:
		var lobby model.LobbyCluster
		if err := tx.First(&lobby, p.TargetID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return placementRef{}, errLobbyClusterNotFound
			}
			return placementRef{}, err
		}
		if lobby.NamespaceID != namespaceID {
			return placementRef{}, errLobbyClusterNamespaceConflict
		}
		return placementRef{kind: LobbyPlacementKind, id: lobby.ID}, nil
	case model.AssignmentTargetZone:
		zoneNamespaceID, err := namespaceIDForZone(tx, p.TargetID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return placementRef{}, apperr.ErrInstanceNotFound
			}
			return placementRef{}, err
		}
		if zoneNamespaceID != namespaceID {
			return placementRef{}, errLobbyClusterNamespaceConflict
		}
		return placementRef{kind: model.AssignmentTargetZone, id: p.TargetID}, nil
	default:
		return placementRef{}, apperr.ErrInvalidParam
	}
}

func placementIsSame(current, target placementRef) bool {
	return current.kind == target.kind && current.id == target.id
}

func placementTransitionAllowed(current, target placementRef) bool {
	switch current.kind {
	case "":
		return target.kind == LobbyPlacementKind
	case model.AssignmentTargetZone:
		return target.kind == LobbyPlacementKind
	case LobbyPlacementKind:
		return target.kind == "" || target.kind == model.AssignmentTargetZone
	default:
		return false
	}
}

func isConfirmedBackend(tx *gorm.DB, server *model.Server) bool {
	if server.Kind != model.ServerKindBackend {
		return false
	}
	var count int64
	err := tx.Model(&model.AgentIdentity{}).
		Where("namespace_id = ? AND server_id = ? AND kind = ? AND status IN ?", server.NamespaceID, server.ServerID,
			model.ServerKindBackend, []string{model.AgentIdentityStatusActive, model.AgentIdentityStatusDisabled}).
		Count(&count).Error
	return err == nil && count > 0
}

func (s *V2ControlPlaneService) isPlacementOnlineNonempty(namespace, serverID string) bool {
	if s.runtime == nil {
		return false
	}
	instance := s.runtime.Get(namespace, serverID)
	return instance != nil && instance.Status == runtime.StatusOnline && instance.PlayerCount > 0
}

func applyPlacementTarget(server *model.Server, target placementRef) {
	server.ZoneID = nil
	server.BCClusterID = nil
	server.LobbyClusterID = nil
	server.PendingZoneID = nil
	server.PendingBCClusterID = nil
	server.IsDefaultEntry = false
	switch target.kind {
	case LobbyPlacementKind:
		id := target.id
		server.LobbyClusterID = &id
	case model.AssignmentTargetZone:
		id := target.id
		server.ZoneID = &id
	}
}

func lobbyPlacementAuditAction(current, target placementRef) string {
	if current.kind == "" && target.kind == LobbyPlacementKind {
		return actionLobbyMemberAssign
	}
	if current.kind == model.AssignmentTargetZone && target.kind == LobbyPlacementKind {
		return actionLobbyMemberMoveIn
	}
	if current.kind == LobbyPlacementKind && target.kind == model.AssignmentTargetZone {
		return actionLobbyMemberMoveOut
	}
	return actionLobbyMemberUnassign
}

func lobbyPlacementAuditDetail(serverID string, current, target placementRef, reason string) string {
	payload, err := json.Marshal(map[string]any{
		"serverId": serverID,
		"from":     placementAuditRef(current),
		"to":       placementAuditRef(target),
		"reason":   reason,
	})
	if err != nil {
		return "{}"
	}
	return string(payload)
}

func placementAuditRef(ref placementRef) any {
	if ref.kind == "" {
		return nil
	}
	return map[string]any{"kind": ref.kind, "id": ref.id}
}

func placementTransferViewOf(server *model.Server) *ServerPlacementTransferView {
	placement, _ := placementKindOf(server)
	return &ServerPlacementTransferView{
		ServerID: server.ServerID, NamespaceID: server.NamespaceID, PlacementKind: placement.kind,
		LobbyClusterID: server.LobbyClusterID, ZoneID: server.ZoneID,
		IsDefaultEntry: server.IsDefaultEntry, Draining: server.Draining,
	}
}

// ListLobbyClustersParams 是大厅集群摘要的服务端筛选 / 分页参数。
type ListLobbyClustersParams struct {
	NamespaceID uint
	Ready       *bool
	Page        int
	PageSize    int
}

// LobbyClusterSummaryView 是大厅集群列表项。
type LobbyClusterSummaryView struct {
	ID               uint      `json:"id"`
	NamespaceID      uint      `json:"namespaceId"`
	NamespaceName    string    `json:"namespaceName"`
	MemberCount      int       `json:"memberCount"`
	SchedulableCount int       `json:"schedulableCount"`
	Ready            bool      `json:"ready"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// LobbyClusterListView 是大厅摘要分页响应。
type LobbyClusterListView struct {
	Items []LobbyClusterSummaryView `json:"items"`
	Total int                       `json:"total"`
}

// ListLobbyClusters 返回 namespace 大厅摘要。健康快照缺失不伪造可调度候选。
func (s *V2ControlPlaneService) ListLobbyClusters(p ListLobbyClustersParams) (*LobbyClusterListView, error) {
	var lobbies []model.LobbyCluster
	query := s.db.Order("id ASC")
	if p.NamespaceID != 0 {
		query = query.Where("namespace_id = ?", p.NamespaceID)
	}
	if err := query.Find(&lobbies).Error; err != nil {
		return nil, err
	}
	summaries, err := s.lobbySummaries(lobbies)
	if err != nil {
		return nil, err
	}
	filtered := make([]LobbyClusterSummaryView, 0, len(summaries))
	for i := range summaries {
		if p.Ready != nil && summaries[i].Ready != *p.Ready {
			continue
		}
		filtered = append(filtered, summaries[i])
	}
	total := len(filtered)
	start := pageOffset(p.Page, p.PageSize)
	if start >= total {
		return &LobbyClusterListView{Items: []LobbyClusterSummaryView{}, Total: total}, nil
	}
	end := start + pageSize(p.PageSize)
	if end > total {
		end = total
	}
	return &LobbyClusterListView{Items: filtered[start:end], Total: total}, nil
}

func (s *V2ControlPlaneService) lobbySummaries(lobbies []model.LobbyCluster) ([]LobbyClusterSummaryView, error) {
	if len(lobbies) == 0 {
		return []LobbyClusterSummaryView{}, nil
	}
	ids := make([]uint, 0, len(lobbies))
	namespaceIDs := make([]uint, 0, len(lobbies))
	for i := range lobbies {
		ids = append(ids, lobbies[i].ID)
		namespaceIDs = append(namespaceIDs, lobbies[i].NamespaceID)
	}
	var namespaces []model.Namespace
	if err := s.db.Where("id IN ?", namespaceIDs).Find(&namespaces).Error; err != nil {
		return nil, err
	}
	namespaceNames := make(map[uint]string, len(namespaces))
	for i := range namespaces {
		namespaceNames[namespaces[i].ID] = namespaces[i].Code
	}
	var members []model.Server
	if err := s.db.Where("lobby_cluster_id IN ? AND lifecycle = ?", ids, model.ServerLifecycleActive).Find(&members).Error; err != nil {
		return nil, err
	}
	memberCount := map[uint]int{}
	schedulableCount := map[uint]int{}
	for i := range members {
		memberCount[*members[i].LobbyClusterID]++
		if s.memberHealth(members[i]).Schedulable {
			schedulableCount[*members[i].LobbyClusterID]++
		}
	}
	out := make([]LobbyClusterSummaryView, 0, len(lobbies))
	for i := range lobbies {
		count := schedulableCount[lobbies[i].ID]
		out = append(out, LobbyClusterSummaryView{
			ID: lobbies[i].ID, NamespaceID: lobbies[i].NamespaceID, NamespaceName: namespaceNames[lobbies[i].NamespaceID],
			MemberCount: memberCount[lobbies[i].ID], SchedulableCount: count, Ready: count > 0, UpdatedAt: lobbies[i].UpdatedAt,
		})
	}
	return out, nil
}

// LobbyClusterDetailParams 是大厅成员的服务端分页 / 筛选参数。
type LobbyClusterDetailParams struct {
	MemberPage     int
	MemberPageSize int
	Keyword        string
	Status         string
}

// LobbyMemberView 是大厅成员的运行态展示；容量与可调度均复用健康视图。
type LobbyMemberView struct {
	ServerID    string   `json:"serverId"`
	Online      bool     `json:"online"`
	PlayerCount int      `json:"playerCount"`
	Score       int      `json:"score"`
	Level       string   `json:"level"`
	Schedulable bool     `json:"schedulable"`
	Reasons     []string `json:"reasons"`
	OnlineCount int      `json:"onlineCount"`
	MaxOnline   int      `json:"maxOnline"`
	Draining    bool     `json:"draining"`
}

// LobbyClusterDetailView 是大厅详情与成员分页。
type LobbyClusterDetailView struct {
	LobbyClusterSummaryView
	Members     []LobbyMemberView `json:"members"`
	MemberTotal int               `json:"memberTotal"`
}

// GetLobbyCluster 读取单个大厅集群与成员分页。
func (s *V2ControlPlaneService) GetLobbyCluster(id uint, p LobbyClusterDetailParams) (*LobbyClusterDetailView, error) {
	if id == 0 || p.MemberPage < 0 || p.MemberPageSize < 0 {
		return nil, apperr.ErrInvalidParam
	}
	var lobby model.LobbyCluster
	if err := s.db.First(&lobby, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errLobbyClusterNotFound
		}
		return nil, err
	}
	summaries, err := s.lobbySummaries([]model.LobbyCluster{lobby})
	if err != nil {
		return nil, err
	}
	members, total, err := s.listLobbyMembers(lobby.ID, summaries[0].NamespaceName, p)
	if err != nil {
		return nil, err
	}
	return &LobbyClusterDetailView{LobbyClusterSummaryView: summaries[0], Members: members, MemberTotal: total}, nil
}

func (s *V2ControlPlaneService) listLobbyMembers(lobbyID uint, namespace string, p LobbyClusterDetailParams) ([]LobbyMemberView, int, error) {
	query := s.db.Where("lobby_cluster_id = ? AND lifecycle = ?", lobbyID, model.ServerLifecycleActive)
	if p.Keyword != "" {
		query = query.Where("server_id LIKE ?", "%"+p.Keyword+"%")
	}
	var servers []model.Server
	if err := query.Order("server_id ASC").Find(&servers).Error; err != nil {
		return nil, 0, err
	}
	filtered := make([]LobbyMemberView, 0, len(servers))
	for i := range servers {
		member := s.lobbyMemberView(servers[i], namespace)
		if p.Status != "" && !lobbyMemberStatusMatches(member, p.Status) {
			continue
		}
		filtered = append(filtered, member)
	}
	total := len(filtered)
	start := pageOffset(p.MemberPage, p.MemberPageSize)
	if start >= total {
		return []LobbyMemberView{}, total, nil
	}
	end := start + pageSize(p.MemberPageSize)
	if end > total {
		end = total
	}
	return filtered[start:end], total, nil
}

func (s *V2ControlPlaneService) lobbyMemberView(server model.Server, namespace string) LobbyMemberView {
	health := s.memberHealth(server)
	member := LobbyMemberView{
		ServerID: server.ServerID, Score: health.Score, Level: health.Level, Schedulable: health.Schedulable,
		Reasons: health.Reasons, PlayerCount: health.OnlineCount, OnlineCount: health.OnlineCount,
		MaxOnline: health.MaxOnline, Draining: server.Draining,
	}
	if member.Reasons == nil {
		member.Reasons = []string{}
	}
	if s.runtime != nil {
		if instance := s.runtime.Get(namespace, server.ServerID); instance != nil {
			member.Online = instance.Status == runtime.StatusOnline
		}
	}
	return member
}

func (s *V2ControlPlaneService) memberHealth(server model.Server) healthview.View {
	if s.healthViews == nil {
		return healthview.View{Reasons: []string{healthview.ReasonLost}}
	}
	health, ok := s.healthViews.Get(server.NamespaceID, server.ServerID)
	if !ok {
		return healthview.View{Reasons: []string{healthview.ReasonLost}}
	}
	return health
}

func lobbyMemberStatusMatches(member LobbyMemberView, status string) bool {
	switch status {
	case "online":
		return member.Online
	case "offline":
		return !member.Online
	case "schedulable":
		return member.Schedulable
	case "unschedulable":
		return !member.Schedulable
	default:
		return false
	}
}
