package repository

import (
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// HealthFact 是一台 server 的健康 / 调度判定事实（FR-147，§4.5 事实来源的 DB 读侧投影）。
// 由健康计算轮每轮锁外读取；lost / level 等运行态判定不在此结构（属内存计算）。
type HealthFact struct {
	NamespaceID uint
	Namespace   string // namespace code（展示 / 视图用）
	ServerID    string
	Kind        string // proxy / backend
	ZoneName    string // v2 zone 名，未分配为空
	Unassigned  bool   // backend 且 zone_id 为空（§4.5 unassigned 输入）
	Draining    bool   // server.draining（v2-zone-authority §3.6 权威）
	// IdentityStatus 当前 agent 身份状态（agent_identity.status 权威）；无身份行为空串。
	IdentityStatus string
}

// HealthFactsRepository 批量读取健康计算所需的 DB 事实（server / zone / namespace / agent_identity），
// 一次全量四表批量取、内存拼装，禁循环内查库（N+1）。
type HealthFactsRepository struct {
	db *gorm.DB
}

// NewHealthFactsRepository 构造仓库。
func NewHealthFactsRepository(db *gorm.DB) *HealthFactsRepository {
	return &HealthFactsRepository{db: db}
}

// ListAll 返回全部在册 server 的健康判定事实（§3.2「全量在册实例」口径 = v2 server 表全部行）。
func (r *HealthFactsRepository) ListAll() ([]HealthFact, error) {
	var servers []model.Server
	if err := r.db.Find(&servers).Error; err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return []HealthFact{}, nil
	}
	zoneNameByID, err := r.loadZoneNames(servers)
	if err != nil {
		return nil, err
	}
	nsCodeByID, err := r.loadNamespaceCodes()
	if err != nil {
		return nil, err
	}
	statusByKey, err := r.loadIdentityStatuses()
	if err != nil {
		return nil, err
	}
	facts := make([]HealthFact, 0, len(servers))
	for i := range servers {
		s := &servers[i]
		fact := HealthFact{
			NamespaceID: s.NamespaceID, Namespace: nsCodeByID[s.NamespaceID],
			ServerID: s.ServerID, Kind: s.Kind,
			Unassigned: s.Kind == model.ServerKindBackend && s.ZoneID == nil,
			Draining:   s.Draining,
			IdentityStatus: statusByKey[healthFactKey{
				namespaceID: s.NamespaceID, serverID: s.ServerID,
			}],
		}
		if s.ZoneID != nil {
			fact.ZoneName = zoneNameByID[*s.ZoneID]
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

// healthFactKey 按 (namespace, serverId) 定位身份状态（serverId 仅 namespace 内唯一）。
type healthFactKey struct {
	namespaceID uint
	serverID    string
}

// loadZoneNames 批量取被引用 zone 的名称映射。
func (r *HealthFactsRepository) loadZoneNames(servers []model.Server) (map[uint]string, error) {
	idSet := map[uint]struct{}{}
	for i := range servers {
		if servers[i].ZoneID != nil {
			idSet[*servers[i].ZoneID] = struct{}{}
		}
	}
	byID := make(map[uint]string, len(idSet))
	if len(idSet) == 0 {
		return byID, nil
	}
	ids := make([]uint, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	var zones []model.Zone
	if err := r.db.Select("id", "name").Where("id IN ?", ids).Find(&zones).Error; err != nil {
		return nil, err
	}
	for i := range zones {
		byID[zones[i].ID] = zones[i].Name
	}
	return byID, nil
}

// loadNamespaceCodes 批量取 namespace code 映射。
func (r *HealthFactsRepository) loadNamespaceCodes() (map[uint]string, error) {
	var namespaces []model.Namespace
	if err := r.db.Select("id", "code").Find(&namespaces).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint]string, len(namespaces))
	for i := range namespaces {
		byID[namespaces[i].ID] = namespaces[i].Code
	}
	return byID, nil
}

// loadIdentityStatuses 批量取每台 server 的当前身份状态：同键多行（历史换绑）取 status_changed_at 最新一行。
func (r *HealthFactsRepository) loadIdentityStatuses() (map[healthFactKey]string, error) {
	var idents []model.AgentIdentity
	if err := r.db.Select("namespace_id", "server_id", "status", "status_changed_at").
		Order("status_changed_at ASC").Find(&idents).Error; err != nil {
		return nil, err
	}
	byKey := make(map[healthFactKey]string, len(idents))
	for i := range idents {
		// 升序遍历后写覆盖先写 → 留下 status_changed_at 最新的状态。
		byKey[healthFactKey{namespaceID: idents[i].NamespaceID, serverID: idents[i].ServerID}] = idents[i].Status
	}
	return byKey, nil
}
