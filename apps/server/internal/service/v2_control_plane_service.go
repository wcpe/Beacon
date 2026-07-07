package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

const (
	defaultPendingTTL = 72 * time.Hour
)

var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type trustKey struct {
	from       uint
	to         uint
	capability string
}

// V2ControlPlaneService 承载第二版身份、namespace 隔离与区服权威写模型。
type V2ControlPlaneService struct {
	db         *gorm.DB
	registerMu sync.Mutex
	trustMu    sync.RWMutex
	trustSet   map[trustKey]struct{}
}

// NewV2ControlPlaneService 构造第二版控制面服务。
func NewV2ControlPlaneService(db *gorm.DB) *V2ControlPlaneService {
	s := &V2ControlPlaneService{db: db, trustSet: map[trustKey]struct{}{}}
	_ = s.reloadTrustSnapshot()
	return s
}

type CreateV2NamespaceParams struct {
	Name        string
	Description string
	Operator    string
	ClientIP    string
}

// CreateV2Namespace 创建 namespace，并返回一次性明文 token。
func (s *V2ControlPlaneService) CreateV2Namespace(p CreateV2NamespaceParams) (*model.Namespace, string, error) {
	if p.Name == "" {
		return nil, "", apperr.ErrInvalidParam
	}
	token, err := newAccessToken()
	if err != nil {
		return nil, "", err
	}
	ns := &model.Namespace{
		Code:            p.Name,
		Name:            p.Name,
		Description:     p.Description,
		AccessTokenHash: tokenHash(token),
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(ns).Error; err != nil {
			return err
		}
		return createAudit(tx, model.AuditLog{
			NamespaceCode: ns.Code,
			Operator:      operatorOrSystem(p.Operator),
			Action:        model.ActionNamespaceCreate,
			TargetType:    model.TargetTypeNamespace,
			TargetRef:     ns.Code,
			Result:        model.ResultOK,
			ClientIP:      p.ClientIP,
		})
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, "", apperr.ErrNamespaceConflict
		}
		return nil, "", err
	}
	return ns, token, nil
}

type AgentRegisterV2Params struct {
	Token        string
	IdentityID   string
	ServerID     string
	Kind         string
	BootID       string
	AgentVersion string
	Addr         string
	ClientIP     string
}

type AgentRegisterV2Result struct {
	Status    string
	ExpiresAt *time.Time
	Namespace string
	ServerID  string
}

type AgentRegistrationV2Status struct {
	Status    string
	Namespace string
	ServerID  string
	Reason    string
}

// RegisterAgentV2 处理 v2 agent 注册与待确认状态机入口。
func (s *V2ControlPlaneService) RegisterAgentV2(p AgentRegisterV2Params) (*AgentRegisterV2Result, error) {
	if !validUUID(p.IdentityID) || p.ServerID == "" || !model.IsValidServerKind(p.Kind) || p.BootID == "" {
		return nil, apperr.ErrInvalidParam
	}
	ns, err := s.namespaceByToken(p.Token)
	if err != nil {
		return nil, err
	}
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	now := time.Now().UTC()
	expiresAt := now.Add(defaultPendingTTL)
	var out AgentRegisterV2Result
	err = s.db.Transaction(func(tx *gorm.DB) error {
		current, err := findIdentityByID(tx, p.IdentityID)
		if err != nil {
			return err
		}
		if current == nil {
			if err := ensureServerIDAvailableForRegister(tx, ns.ID, p.ServerID); err != nil {
				return err
			}
			conflictReason, err := occupiedServerConflictReason(tx, ns.ID, p.ServerID, p.IdentityID)
			if err != nil {
				return err
			}
			ident := &model.AgentIdentity{
				IdentityID: p.IdentityID, NamespaceID: ns.ID, ServerID: p.ServerID,
				Kind: p.Kind, Status: model.AgentIdentityStatusPending,
				BootID: p.BootID, LastAddr: p.Addr, AgentVersion: p.AgentVersion,
				PendingExpiresAt: &expiresAt, StatusChangedAt: now, ConflictReason: conflictReason,
			}
			if err := tx.Create(ident).Error; err != nil {
				return err
			}
			out = AgentRegisterV2Result{Status: ident.Status, ExpiresAt: ident.PendingExpiresAt, Namespace: ns.Code, ServerID: ident.ServerID}
			return auditIdentity(tx, ns, ident, model.ActionIdentityRegistered, "agent", model.ResultOK, p.ClientIP)
		}
		return s.registerExistingIdentity(tx, ns, current, p, now, expiresAt, &out)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// AuthenticateAgentV2 允许已确认 v2 身份继续访问 legacy v1 数据面。
func (s *V2ControlPlaneService) AuthenticateAgentV2(token, identityID, bootID string) error {
	if !validUUID(identityID) || bootID == "" {
		return apperr.ErrUnauthorized
	}
	ns, err := s.namespaceByToken(token)
	if err != nil {
		return err
	}
	ident, err := findIdentityByID(s.db, identityID)
	if err != nil {
		return err
	}
	if ident == nil || ident.NamespaceID != ns.ID || ident.Status != model.AgentIdentityStatusActive || ident.BootID != bootID {
		return apperr.ErrUnauthorized
	}
	return nil
}

// GetAgentRegistrationV2 返回当前身份状态；token 仍决定 namespace 可见边界。
func (s *V2ControlPlaneService) GetAgentRegistrationV2(token, identityID string) (*AgentRegistrationV2Status, error) {
	if !validUUID(identityID) {
		return nil, apperr.ErrInvalidParam
	}
	ns, err := s.namespaceByToken(token)
	if err != nil {
		return nil, err
	}
	ident, err := findIdentityByID(s.db, identityID)
	if err != nil {
		return nil, err
	}
	if ident == nil {
		return nil, apperr.ErrInstanceNotFound
	}
	if ident.NamespaceID != ns.ID {
		return nil, apperr.ErrUnauthorized
	}
	return &AgentRegistrationV2Status{
		Status: ident.Status, Namespace: ns.Code, ServerID: ident.ServerID, Reason: ident.ConflictReason,
	}, nil
}

func (s *V2ControlPlaneService) registerExistingIdentity(tx *gorm.DB, ns *model.Namespace, current *model.AgentIdentity, p AgentRegisterV2Params, now, expiresAt time.Time, out *AgentRegisterV2Result) error {
	sameBinding := current.NamespaceID == ns.ID && current.ServerID == p.ServerID && current.Kind == p.Kind
	if !sameBinding && current.Status != model.AgentIdentityStatusUnbound && current.Status != model.AgentIdentityStatusExpired {
		return apperr.ErrIdentityBindingMismatch
	}
	switch current.Status {
	case model.AgentIdentityStatusPending:
		if !sameBinding {
			return apperr.ErrIdentityBindingMismatch
		}
		current.BootID = p.BootID
		current.LastAddr = p.Addr
		current.AgentVersion = p.AgentVersion
		current.PendingExpiresAt = &expiresAt
	case model.AgentIdentityStatusExpired, model.AgentIdentityStatusUnbound:
		if err := ensureServerIDAvailableForRegister(tx, ns.ID, p.ServerID); err != nil {
			return err
		}
		current.NamespaceID = ns.ID
		current.ServerID = p.ServerID
		current.Kind = p.Kind
		current.Status = model.AgentIdentityStatusPending
		current.BootID = p.BootID
		current.LastAddr = p.Addr
		current.AgentVersion = p.AgentVersion
		current.PendingExpiresAt = &expiresAt
		current.StatusChangedAt = now
	case model.AgentIdentityStatusActive, model.AgentIdentityStatusDisabled:
		if !sameBinding {
			return apperr.ErrIdentityBindingMismatch
		}
		current.BootID = p.BootID
		current.LastAddr = p.Addr
		current.AgentVersion = p.AgentVersion
	case model.AgentIdentityStatusRejected:
		return apperr.ErrIdentityRejected
	case model.AgentIdentityStatusConflict:
		return apperr.ErrIdentityConflict
	default:
		return apperr.ErrIllegalState
	}
	if err := tx.Save(current).Error; err != nil {
		return err
	}
	*out = AgentRegisterV2Result{Status: current.Status, ExpiresAt: current.PendingExpiresAt, Namespace: ns.Code, ServerID: current.ServerID}
	return nil
}

type ApproveAgentIdentityParams struct {
	Operator            string
	ClientIP            string
	ForceUnbindOccupier bool
	TargetKind          string
	TargetID            *uint
}

// ApproveAgentIdentity 确认待确认身份，并创建未分配 server 行。
func (s *V2ControlPlaneService) ApproveAgentIdentity(identityID string, p ApproveAgentIdentityParams) (*model.AgentIdentity, error) {
	now := time.Now().UTC()
	var out model.AgentIdentity
	err := s.db.Transaction(func(tx *gorm.DB) error {
		ident, err := findIdentityByID(tx, identityID)
		if err != nil {
			return err
		}
		if ident == nil {
			return apperr.ErrInstanceNotFound
		}
		if ident.Status != model.AgentIdentityStatusPending {
			return apperr.ErrIllegalState
		}
		ns, err := findNamespaceByID(tx, ident.NamespaceID)
		if err != nil {
			return err
		}
		if err := s.resolveOccupierForApprove(tx, ident, p); err != nil {
			return err
		}
		if _, err := ensureServerRow(tx, ident.NamespaceID, ident.ServerID, ident.Kind); err != nil {
			return err
		}
		if p.TargetID != nil {
			return apperr.ErrInvalidParam
		}
		ident.Status = model.AgentIdentityStatusActive
		ident.PendingExpiresAt = nil
		ident.BoundAt = &now
		ident.StatusChangedAt = now
		if err := tx.Save(ident).Error; err != nil {
			return err
		}
		out = *ident
		return auditIdentity(tx, ns, ident, model.ActionIdentityApproved, operatorOrSystem(p.Operator), model.ResultOK, p.ClientIP)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

type IdentityTransitionParams struct {
	Reason   string
	Operator string
	ClientIP string
}

// RejectAgentIdentity 拒绝待确认身份。
func (s *V2ControlPlaneService) RejectAgentIdentity(identityID string, p IdentityTransitionParams) (*model.AgentIdentity, error) {
	if p.Reason == "" {
		return nil, apperr.ErrInvalidParam
	}
	return s.transitionIdentity(identityID, []string{model.AgentIdentityStatusPending}, model.AgentIdentityStatusRejected, model.ActionIdentityRejected, p)
}

// AllowAgentIdentityReapply 允许已拒绝身份重新申请。
func (s *V2ControlPlaneService) AllowAgentIdentityReapply(identityID string, p IdentityTransitionParams) (*model.AgentIdentity, error) {
	if p.Reason == "" {
		return nil, apperr.ErrInvalidParam
	}
	return s.transitionIdentity(identityID, []string{model.AgentIdentityStatusRejected}, model.AgentIdentityStatusExpired, model.ActionIdentityReapplyAllowed, p)
}

// DisableAgentIdentity 临时禁用已确认身份。
func (s *V2ControlPlaneService) DisableAgentIdentity(identityID string, p IdentityTransitionParams) (*model.AgentIdentity, error) {
	if p.Reason == "" {
		return nil, apperr.ErrInvalidParam
	}
	return s.transitionIdentity(identityID, []string{model.AgentIdentityStatusActive}, model.AgentIdentityStatusDisabled, model.ActionIdentityDisabled, p)
}

// EnableAgentIdentity 重新启用禁用身份。
func (s *V2ControlPlaneService) EnableAgentIdentity(identityID string, p IdentityTransitionParams) (*model.AgentIdentity, error) {
	return s.transitionIdentity(identityID, []string{model.AgentIdentityStatusDisabled}, model.AgentIdentityStatusActive, model.ActionIdentityEnabled, p)
}

// UnbindAgentIdentity 解除当前身份绑定。
func (s *V2ControlPlaneService) UnbindAgentIdentity(identityID string, p IdentityTransitionParams) (*model.AgentIdentity, error) {
	if p.Reason == "" {
		return nil, apperr.ErrInvalidParam
	}
	return s.transitionIdentity(identityID, []string{
		model.AgentIdentityStatusActive, model.AgentIdentityStatusDisabled, model.AgentIdentityStatusConflict,
	}, model.AgentIdentityStatusUnbound, model.ActionIdentityUnbound, p)
}

func (s *V2ControlPlaneService) transitionIdentity(identityID string, allowed []string, nextStatus, action string, p IdentityTransitionParams) (*model.AgentIdentity, error) {
	now := time.Now().UTC()
	var out model.AgentIdentity
	err := s.db.Transaction(func(tx *gorm.DB) error {
		ident, err := findIdentityByID(tx, identityID)
		if err != nil {
			return err
		}
		if ident == nil {
			return apperr.ErrInstanceNotFound
		}
		if !stringIn(ident.Status, allowed) {
			return apperr.ErrIllegalState
		}
		ns, err := findNamespaceByID(tx, ident.NamespaceID)
		if err != nil {
			return err
		}
		ident.Status = nextStatus
		ident.StatusChangedAt = now
		if nextStatus != model.AgentIdentityStatusPending {
			ident.PendingExpiresAt = nil
		}
		if nextStatus == model.AgentIdentityStatusUnbound {
			ident.ConflictReason = ""
		}
		if err := tx.Save(ident).Error; err != nil {
			return err
		}
		out = *ident
		return createAudit(tx, model.AuditLog{
			NamespaceCode: ns.Code,
			Operator:      operatorOrSystem(p.Operator),
			Action:        action,
			TargetType:    model.TargetTypeIdentity,
			TargetRef:     ident.IdentityID,
			Detail:        p.Reason,
			Result:        model.ResultOK,
			ClientIP:      p.ClientIP,
		})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func stringIn(value string, allowed []string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

type ListAgentIdentitiesParams struct {
	NamespaceID uint
	Status      string
	Keyword     string
	Page        int
	PageSize    int
}

// ListAgentIdentities 分页查询身份列表。
func (s *V2ControlPlaneService) ListAgentIdentities(p ListAgentIdentitiesParams) ([]model.AgentIdentity, int64, error) {
	q := s.db.Model(&model.AgentIdentity{})
	if p.NamespaceID != 0 {
		q = q.Where("namespace_id = ?", p.NamespaceID)
	}
	if p.Status != "" {
		q = q.Where("status = ?", p.Status)
	}
	if p.Keyword != "" {
		like := "%" + p.Keyword + "%"
		q = q.Where("identity_id LIKE ? OR server_id LIKE ?", like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []model.AgentIdentity
	err := q.Order("updated_at DESC").Offset(pageOffset(p.Page, p.PageSize)).Limit(pageSize(p.PageSize)).Find(&items).Error
	return items, total, err
}

type ListServersParams struct {
	NamespaceID uint
	Kind        string
	Assigned    *bool
	Keyword     string
	Page        int
	PageSize    int
}

// ListServers 分页查询 v2 server 资产列表。
func (s *V2ControlPlaneService) ListServers(p ListServersParams) ([]model.Server, int64, error) {
	q := s.db.Model(&model.Server{})
	if p.NamespaceID != 0 {
		q = q.Where("namespace_id = ?", p.NamespaceID)
	}
	if p.Kind != "" {
		q = q.Where("kind = ?", p.Kind)
	}
	if p.Assigned != nil {
		if *p.Assigned {
			q = q.Where("zone_id IS NOT NULL OR bc_cluster_id IS NOT NULL")
		} else {
			q = q.Where("zone_id IS NULL AND bc_cluster_id IS NULL")
		}
	}
	if p.Keyword != "" {
		q = q.Where("server_id LIKE ?", "%"+p.Keyword+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []model.Server
	err := q.Order("updated_at DESC").Offset(pageOffset(p.Page, p.PageSize)).Limit(pageSize(p.PageSize)).Find(&items).Error
	return items, total, err
}

func (s *V2ControlPlaneService) ListNamespaces() ([]model.Namespace, error) {
	var items []model.Namespace
	err := s.db.Order("id ASC").Find(&items).Error
	return items, err
}

func (s *V2ControlPlaneService) ListNamespaceTrusts() ([]model.NamespaceTrust, error) {
	var items []model.NamespaceTrust
	err := s.db.Order("updated_at DESC").Find(&items).Error
	return items, err
}

func pageSize(size int) int {
	if size <= 0 {
		return 20
	}
	if size > 200 {
		return 200
	}
	return size
}

func pageOffset(page, size int) int {
	if page <= 1 {
		return 0
	}
	return (page - 1) * pageSize(size)
}

func (s *V2ControlPlaneService) resolveOccupierForApprove(tx *gorm.DB, ident *model.AgentIdentity, p ApproveAgentIdentityParams) error {
	occupier, err := findActiveIdentityByServer(tx, ident.NamespaceID, ident.ServerID, ident.IdentityID)
	if err != nil || occupier == nil {
		return err
	}
	if !p.ForceUnbindOccupier {
		return apperr.ErrServerIDOccupied
	}
	occupier.Status = model.AgentIdentityStatusUnbound
	occupier.StatusChangedAt = time.Now().UTC()
	if err := tx.Save(occupier).Error; err != nil {
		return err
	}
	ns, err := findNamespaceByID(tx, occupier.NamespaceID)
	if err != nil {
		return err
	}
	return auditIdentity(tx, ns, occupier, model.ActionIdentityForceRebind, operatorOrSystem(p.Operator), model.ResultOK, p.ClientIP)
}

type GrantNamespaceTrustParams struct {
	FromNamespaceID uint
	ToNamespaceID   uint
	Capability      string
	Note            string
	Operator        string
	ClientIP        string
}

// GrantNamespaceTrust 授予或复活一条 namespace 信任。
func (s *V2ControlPlaneService) GrantNamespaceTrust(p GrantNamespaceTrustParams) (*model.NamespaceTrust, error) {
	if p.FromNamespaceID == 0 || p.ToNamespaceID == 0 || p.FromNamespaceID == p.ToNamespaceID ||
		!model.IsValidNamespaceTrustCapability(p.Capability) || p.Note == "" {
		return nil, apperr.ErrInvalidParam
	}
	now := time.Now().UTC()
	var out model.NamespaceTrust
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := ensureNamespacesExist(tx, p.FromNamespaceID, p.ToNamespaceID); err != nil {
			return err
		}
		existing, err := findTrust(tx, p.FromNamespaceID, p.ToNamespaceID, p.Capability)
		if err != nil {
			return err
		}
		if existing != nil && existing.Status == model.NamespaceTrustStatusActive {
			return apperr.ErrServerIDOccupied
		}
		if existing == nil {
			existing = &model.NamespaceTrust{
				FromNamespaceID: p.FromNamespaceID,
				ToNamespaceID:   p.ToNamespaceID,
				Capability:      p.Capability,
			}
		}
		existing.Status = model.NamespaceTrustStatusActive
		existing.Note = p.Note
		existing.GrantedBy = operatorOrSystem(p.Operator)
		existing.GrantedAt = now
		existing.RevokedBy = ""
		existing.RevokedAt = nil
		existing.RevokeReason = ""
		if err := tx.Save(existing).Error; err != nil {
			return err
		}
		out = *existing
		return createAudit(tx, model.AuditLog{
			Operator: operatorOrSystem(p.Operator), Action: model.ActionNamespaceTrustGrant,
			TargetType: model.TargetTypeNamespaceTrust, TargetRef: fmt.Sprintf("%d/%d/%s", p.FromNamespaceID, p.ToNamespaceID, p.Capability),
			Detail: p.Note, Result: model.ResultOK, ClientIP: p.ClientIP,
		})
	})
	if err != nil {
		return nil, err
	}
	if err := s.reloadTrustSnapshot(); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeNamespaceTrust 收回信任并刷新进程内快照。
func (s *V2ControlPlaneService) RevokeNamespaceTrust(id uint, reason, operator string) error {
	if id == 0 || reason == "" {
		return apperr.ErrInvalidParam
	}
	now := time.Now().UTC()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var trust model.NamespaceTrust
		if err := tx.First(&trust, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.ErrInstanceNotFound
			}
			return err
		}
		if trust.Status != model.NamespaceTrustStatusActive {
			return apperr.ErrIllegalState
		}
		trust.Status = model.NamespaceTrustStatusRevoked
		trust.RevokedBy = operatorOrSystem(operator)
		trust.RevokedAt = &now
		trust.RevokeReason = reason
		if err := tx.Save(&trust).Error; err != nil {
			return err
		}
		return createAudit(tx, model.AuditLog{
			Operator: operatorOrSystem(operator), Action: model.ActionNamespaceTrustRevoke,
			TargetType: model.TargetTypeNamespaceTrust, TargetRef: fmt.Sprintf("%d", id),
			Detail: reason, Result: model.ResultOK,
		})
	})
	if err != nil {
		return err
	}
	return s.reloadTrustSnapshot()
}

// NamespaceTrustAllowed 查询进程内信任快照。
func (s *V2ControlPlaneService) NamespaceTrustAllowed(from, to uint, capability string) bool {
	s.trustMu.RLock()
	defer s.trustMu.RUnlock()
	_, ok := s.trustSet[trustKey{from: from, to: to, capability: capability}]
	return ok
}

type CreateBCClusterParams struct {
	NamespaceID uint
	Name        string
	Description string
	Operator    string
	ClientIP    string
}

func (s *V2ControlPlaneService) CreateBCCluster(p CreateBCClusterParams) (*model.BCCluster, error) {
	if p.NamespaceID == 0 || p.Name == "" {
		return nil, apperr.ErrInvalidParam
	}
	cluster := &model.BCCluster{NamespaceID: p.NamespaceID, Name: p.Name, Description: p.Description}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := ensureNamespacesExist(tx, p.NamespaceID); err != nil {
			return err
		}
		if err := tx.Create(cluster).Error; err != nil {
			return err
		}
		return createAudit(tx, model.AuditLog{
			Operator: operatorOrSystem(p.Operator), Action: model.ActionBCClusterCreate,
			TargetType: model.TargetTypeBCCluster, TargetRef: fmt.Sprintf("%d", cluster.ID),
			Result: model.ResultOK, ClientIP: p.ClientIP,
		})
	})
	return cluster, err
}

type CreateRegionParams struct {
	BCClusterID uint
	Name        string
	Description string
	Operator    string
	ClientIP    string
}

func (s *V2ControlPlaneService) CreateRegion(p CreateRegionParams) (*model.Region, error) {
	if p.BCClusterID == 0 || p.Name == "" {
		return nil, apperr.ErrInvalidParam
	}
	region := &model.Region{BCClusterID: p.BCClusterID, Name: p.Name, Description: p.Description}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := ensureBCClusterExists(tx, p.BCClusterID); err != nil {
			return err
		}
		if err := tx.Create(region).Error; err != nil {
			return err
		}
		return createAudit(tx, model.AuditLog{
			Operator: operatorOrSystem(p.Operator), Action: model.ActionRegionCreate,
			TargetType: model.TargetTypeRegion, TargetRef: fmt.Sprintf("%d", region.ID),
			Result: model.ResultOK, ClientIP: p.ClientIP,
		})
	})
	return region, err
}

type CreateZoneParams struct {
	RegionID    uint
	Name        string
	Description string
	Operator    string
	ClientIP    string
}

func (s *V2ControlPlaneService) CreateZone(p CreateZoneParams) (*model.Zone, error) {
	if p.RegionID == 0 || p.Name == "" {
		return nil, apperr.ErrInvalidParam
	}
	zone := &model.Zone{RegionID: p.RegionID, Name: p.Name, Description: p.Description}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := ensureRegionExists(tx, p.RegionID); err != nil {
			return err
		}
		if err := tx.Create(zone).Error; err != nil {
			return err
		}
		return createAudit(tx, model.AuditLog{
			Operator: operatorOrSystem(p.Operator), Action: model.ActionZoneCreate,
			TargetType: model.TargetTypeZone, TargetRef: fmt.Sprintf("%d", zone.ID),
			Result: model.ResultOK, ClientIP: p.ClientIP,
		})
	})
	return zone, err
}

type AssignServersParams struct {
	ServerIDs      []uint
	TargetKind     string
	TargetID       uint
	IsDefaultEntry bool
	Reason         string
	Operator       string
	ClientIP       string
}

// AssignServers 批量首次分配未分配 server。
func (s *V2ControlPlaneService) AssignServers(p AssignServersParams) ([]model.Server, error) {
	if len(p.ServerIDs) == 0 || p.TargetID == 0 {
		return nil, apperr.ErrInvalidParam
	}
	var out []model.Server
	err := s.db.Transaction(func(tx *gorm.DB) error {
		targetNS, targetKind, err := resolveAssignmentTarget(tx, p.TargetKind, p.TargetID)
		if err != nil {
			return err
		}
		var servers []model.Server
		if err := tx.Where("id IN ?", p.ServerIDs).Find(&servers).Error; err != nil {
			return err
		}
		if len(servers) != len(p.ServerIDs) {
			return apperr.ErrInstanceNotFound
		}
		for i := range servers {
			if err := validateAssignableServer(&servers[i], targetNS, targetKind); err != nil {
				return err
			}
			if targetKind == model.AssignmentTargetZone {
				servers[i].ZoneID = &p.TargetID
				servers[i].IsDefaultEntry = p.IsDefaultEntry
			} else {
				servers[i].BCClusterID = &p.TargetID
				servers[i].IsDefaultEntry = false
			}
			if err := tx.Save(&servers[i]).Error; err != nil {
				return err
			}
			if err := createAudit(tx, model.AuditLog{
				Operator: operatorOrSystem(p.Operator), Action: model.ActionServerAssign,
				TargetType: model.TargetTypeServer, TargetRef: fmt.Sprintf("%d", servers[i].ID),
				Detail: p.Reason, Result: model.ResultOK, ClientIP: p.ClientIP,
			}); err != nil {
				return err
			}
		}
		out = servers
		return nil
	})
	return out, err
}

func validateAssignableServer(server *model.Server, targetNS uint, targetKind string) error {
	if server.NamespaceID != targetNS {
		return apperr.ErrForbidden
	}
	if server.ZoneID != nil || server.BCClusterID != nil {
		return apperr.ErrRezoneRequired
	}
	if targetKind == model.AssignmentTargetZone && server.Kind != model.ServerKindBackend {
		return apperr.ErrInvalidParam
	}
	if targetKind == model.AssignmentTargetBCCluster && server.Kind != model.ServerKindProxy {
		return apperr.ErrInvalidParam
	}
	return nil
}

func resolveAssignmentTarget(tx *gorm.DB, targetKind string, targetID uint) (uint, string, error) {
	switch targetKind {
	case model.AssignmentTargetZone:
		var zone model.Zone
		if err := tx.First(&zone, targetID).Error; err != nil {
			return 0, "", err
		}
		ns, err := namespaceIDForZone(tx, zone.ID)
		return ns, targetKind, err
	case model.AssignmentTargetBCCluster:
		var cluster model.BCCluster
		if err := tx.First(&cluster, targetID).Error; err != nil {
			return 0, "", err
		}
		return cluster.NamespaceID, targetKind, nil
	default:
		return 0, "", apperr.ErrInvalidParam
	}
}

func namespaceIDForZone(tx *gorm.DB, zoneID uint) (uint, error) {
	var row struct{ NamespaceID uint }
	err := tx.Table("zone").
		Select("bc_cluster.namespace_id").
		Joins("JOIN region ON region.id = zone.region_id").
		Joins("JOIN bc_cluster ON bc_cluster.id = region.bc_cluster_id").
		Where("zone.id = ?", zoneID).
		Scan(&row).Error
	if err != nil {
		return 0, err
	}
	if row.NamespaceID == 0 {
		return 0, apperr.ErrInstanceNotFound
	}
	return row.NamespaceID, nil
}

func (s *V2ControlPlaneService) namespaceByToken(token string) (*model.Namespace, error) {
	if token == "" {
		return nil, apperr.ErrUnauthorized
	}
	var ns model.Namespace
	err := s.db.Where("access_token_hash = ?", tokenHash(token)).First(&ns).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperr.ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	return &ns, nil
}

func ensureServerIDAvailableForRegister(tx *gorm.DB, namespaceID uint, serverID string) error {
	var pending model.AgentIdentity
	err := tx.Where("namespace_id = ? AND server_id = ? AND status = ?", namespaceID, serverID, model.AgentIdentityStatusPending).First(&pending).Error
	if err == nil {
		return apperr.ErrServerIDPendingElsewhere
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}

func occupiedServerConflictReason(tx *gorm.DB, namespaceID uint, serverID, identityID string) (string, error) {
	occupier, err := findActiveIdentityByServer(tx, namespaceID, serverID, identityID)
	if err != nil || occupier == nil {
		return "", err
	}
	return "server-id-occupied", nil
}

func findIdentityByID(tx *gorm.DB, identityID string) (*model.AgentIdentity, error) {
	var ident model.AgentIdentity
	err := tx.Where("identity_id = ?", identityID).First(&ident).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ident, nil
}

func findNamespaceByID(tx *gorm.DB, id uint) (*model.Namespace, error) {
	var ns model.Namespace
	if err := tx.First(&ns, id).Error; err != nil {
		return nil, err
	}
	return &ns, nil
}

func findActiveIdentityByServer(tx *gorm.DB, namespaceID uint, serverID, exceptIdentityID string) (*model.AgentIdentity, error) {
	var ident model.AgentIdentity
	err := tx.Where("namespace_id = ? AND server_id = ? AND identity_id <> ? AND status IN ?", namespaceID, serverID, exceptIdentityID,
		[]string{model.AgentIdentityStatusActive, model.AgentIdentityStatusDisabled, model.AgentIdentityStatusConflict}).
		First(&ident).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ident, nil
}

func ensureServerRow(tx *gorm.DB, namespaceID uint, serverID, kind string) (*model.Server, error) {
	var server model.Server
	err := tx.Where("namespace_id = ? AND server_id = ?", namespaceID, serverID).First(&server).Error
	if err == nil {
		return &server, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	server = model.Server{NamespaceID: namespaceID, ServerID: serverID, Kind: kind}
	if err := tx.Create(&server).Error; err != nil {
		return nil, err
	}
	return &server, nil
}

func ensureNamespacesExist(tx *gorm.DB, ids ...uint) error {
	for _, id := range ids {
		var count int64
		if err := tx.Model(&model.Namespace{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return apperr.ErrNamespaceNotFound
		}
	}
	return nil
}

func ensureBCClusterExists(tx *gorm.DB, id uint) error {
	var count int64
	if err := tx.Model(&model.BCCluster{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return apperr.ErrInstanceNotFound
	}
	return nil
}

func ensureRegionExists(tx *gorm.DB, id uint) error {
	var count int64
	if err := tx.Model(&model.Region{}).Where("id = ?", id).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return apperr.ErrInstanceNotFound
	}
	return nil
}

func findTrust(tx *gorm.DB, from, to uint, capability string) (*model.NamespaceTrust, error) {
	var trust model.NamespaceTrust
	err := tx.Where("from_namespace_id = ? AND to_namespace_id = ? AND capability = ?", from, to, capability).First(&trust).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &trust, nil
}

func (s *V2ControlPlaneService) reloadTrustSnapshot() error {
	var trusts []model.NamespaceTrust
	if err := s.db.Where("status = ?", model.NamespaceTrustStatusActive).Find(&trusts).Error; err != nil {
		return err
	}
	next := make(map[trustKey]struct{}, len(trusts))
	for _, trust := range trusts {
		next[trustKey{from: trust.FromNamespaceID, to: trust.ToNamespaceID, capability: trust.Capability}] = struct{}{}
	}
	s.trustMu.Lock()
	s.trustSet = next
	s.trustMu.Unlock()
	return nil
}

func auditIdentity(tx *gorm.DB, ns *model.Namespace, ident *model.AgentIdentity, action, operator, result, clientIP string) error {
	return createAudit(tx, model.AuditLog{
		NamespaceCode: ns.Code,
		Operator:      operator,
		Action:        action,
		TargetType:    model.TargetTypeIdentity,
		TargetRef:     ident.IdentityID,
		Result:        result,
		ClientIP:      clientIP,
	})
}

func createAudit(db *gorm.DB, entry model.AuditLog) error {
	if entry.Operator == "" {
		entry.Operator = "system"
	}
	if entry.Result == "" {
		entry.Result = model.ResultOK
	}
	return db.Create(&entry).Error
}

func newAccessToken() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("生成 namespace token 失败: %w", err)
	}
	return "bn_" + hex.EncodeToString(b[:]), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func validUUID(s string) bool {
	return uuidV4Pattern.MatchString(s)
}

func operatorOrSystem(operator string) string {
	if operator == "" {
		return "system"
	}
	return operator
}
