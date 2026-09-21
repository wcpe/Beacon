package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/redact"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/bootwatch"
	"github.com/wcpe/Beacon/apps/server/internal/runtime/healthview"
)

const (
	defaultPendingTTL = 72 * time.Hour
)

func auditJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type trustKey struct {
	from       uint
	to         uint
	capability string
}

// V2ControlPlaneService 承载第二版身份、namespace 隔离与区服权威写模型。
type V2ControlPlaneService struct {
	db                      *gorm.DB
	trustSnapshotOwner      *V2ControlPlaneService
	runtime                 *runtime.Registry
	healthViews             *healthview.Store
	notifier                *ChangeNotifier
	registerMu              *sync.Mutex
	directoryResyncMu       *sync.Mutex
	directoryResyncRepo     *repository.AgentCommandRepository
	directoryResyncNotifier CommandNotifier
	trustMu                 *sync.RWMutex
	trustSet                map[trustKey]struct{}
	// 并发身份冲突检测（FR-177，spec §4.5）：bootId 活跃注册表（进程内真源）+ 冲突窗口取值 + 告警留痕出口。
	// 未装配（nil）时检测禁用——保持旧构造 NewV2ControlPlaneService(db) 与既有测试行为不变。
	bootRegistry     *bootwatch.Registry
	conflictWindow   func() time.Duration
	alertSink        AlertSink
	approval         *ApprovalService
	afterCommit      func(func())
	legacyZone       *ZoneService
	legacyScheduling *SchedulingService
}

// SetLegacyZoneService 注入 V1 区服兼容写入服务；仅审批适配器可在事务内调用其私有写入。
func (s *V2ControlPlaneService) SetLegacyZoneService(zone *ZoneService) {
	s.legacyZone = zone
}

// SetLegacySchedulingService 注入 V1 排空兼容写入服务；仅审批适配器可在事务内调用其私有写入。
func (s *V2ControlPlaneService) SetLegacySchedulingService(scheduling *SchedulingService) {
	s.legacyScheduling = scheduling
}

// NewV2ControlPlaneService 构造第二版控制面服务。
func NewV2ControlPlaneService(db *gorm.DB) *V2ControlPlaneService {
	s := &V2ControlPlaneService{
		db:                db,
		registerMu:        &sync.Mutex{},
		directoryResyncMu: &sync.Mutex{},
		trustMu:           &sync.RWMutex{},
		trustSet:          map[trustKey]struct{}{},
	}
	s.trustSnapshotOwner = s
	_ = s.reloadTrustSnapshot()
	return s
}

func (s *V2ControlPlaneService) scheduleAfterCommit(callback func()) {
	if s.afterCommit != nil {
		s.afterCommit(callback)
		return
	}
	callback()
}

type CreateV2NamespaceParams struct {
	Name        string
	Code        string
	DisplayName string
	Description string
	Operator    string
	ClientIP    string
}

// CreateV2Namespace 创建 namespace，并返回一次性明文 token。
func (s *V2ControlPlaneService) CreateV2Namespace(p CreateV2NamespaceParams) (*model.Namespace, string, error) {
	code, displayName, err := normalizeStableName(p.Name, p.Code, p.DisplayName)
	if err != nil {
		return nil, "", err
	}
	token, err := newAccessToken()
	if err != nil {
		return nil, "", err
	}
	ns := &model.Namespace{
		Code:            code,
		Name:            displayName,
		Description:     p.Description,
		AccessTokenHash: tokenHash(token),
	}
	detail := auditJSON(map[string]string{"code": code, "displayName": displayName})
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(ns).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.LobbyCluster{NamespaceID: ns.ID}).Error; err != nil {
			return err
		}
		return createAudit(tx, model.AuditLog{
			NamespaceCode: ns.Code,
			Operator:      operatorOrSystem(p.Operator),
			Action:        model.ActionNamespaceCreate,
			TargetType:    model.TargetTypeNamespace,
			TargetRef:     ns.Code,
			Detail:        detail,
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
	// ServerWorkDir agent 上报的服务器工作目录绝对路径（FR-226，可选；空表示旧 agent 未上报）。
	ServerWorkDir     string
	Addr              string
	DetectedHost      string
	ListenPort        *int
	Listeners         []AgentEndpointReport
	ListenersProvided bool
	ClientIP          string
}

type AgentRegisterV2Result struct {
	Status             string
	ExpiresAt          *time.Time
	Namespace          string
	ServerID           *string
	BoundAt            *time.Time
	BindingFingerprint *string
	BindingSource      string
	MigrationState     string
	Address            string
	Endpoints          []AgentEndpointView
}

type AgentRegistrationV2Status struct {
	Status             string
	Namespace          string
	ServerID           *string
	BoundAt            *time.Time
	BindingFingerprint *string
	Reason             string
	Address            string
	Endpoints          []AgentEndpointView
}

// RegisterAgentV2 处理 v2 agent 注册与待确认状态机入口。
func (s *V2ControlPlaneService) RegisterAgentV2(p AgentRegisterV2Params) (*AgentRegisterV2Result, error) {
	if !validUUID(p.IdentityID) || !model.IsValidServerKind(p.Kind) || p.BootID == "" {
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
	// 并发身份冲突检测（FR-177，spec §4.5）：先做内存往复观测（本调用释放注册表锁后再落库，守锁内不做 DB IO）。
	// 被冲突短路（落败方 / 已转冲突）则直接返回对应 409；否则落常规注册路径。
	if handled, err := s.detectRegisterConflict(p, now); handled {
		return nil, err
	}
	var out AgentRegisterV2Result
	err = s.db.Transaction(func(tx *gorm.DB) error {
		current, err := findIdentityByID(tx, p.IdentityID)
		if err != nil {
			return err
		}
		if err := ensureIdentityRuntimeBindingOpen(current); err != nil {
			return err
		}
		if err := ensureRegistrationServerActive(tx, ns.ID, current, p.ServerID); err != nil {
			return err
		}
		if current != nil {
			if err := s.registerExistingIdentity(tx, ns, current, p, now, expiresAt, &out); err != nil {
				return err
			}
			endpoints, err := syncAgentEndpoints(tx, current, p, now)
			if err != nil {
				return err
			}
			out = newAgentRegisterV2Result(current, ns.Code)
			out.Address, out.Endpoints = current.LastAddr, endpoints
			return nil
		}
		ident := &model.AgentIdentity{
			IdentityID: p.IdentityID, NamespaceID: ns.ID, ServerID: model.NullableServerID(p.ServerID),
			Kind: p.Kind, Status: model.AgentIdentityStatusPending,
			BootID: p.BootID, LastAddr: p.Addr, AgentVersion: p.AgentVersion,
			ServerWorkDir:    p.ServerWorkDir,
			PendingExpiresAt: &expiresAt, StatusChangedAt: now,
			BindingSource: identityBindingSourceForRegistration(p.ServerID),
		}
		if ident.ServerID.Assigned() {
			if err := ensureServerIDAvailableForRegister(tx, ns.ID, string(ident.ServerID)); err != nil {
				return err
			}
			conflictReason, err := occupiedServerConflictReason(tx, ns.ID, string(ident.ServerID), p.IdentityID)
			if err != nil {
				return err
			}
			ident.ConflictReason = conflictReason
		}
		if err := tx.Create(ident).Error; err != nil {
			return err
		}
		endpoints, err := syncAgentEndpoints(tx, ident, p, now)
		if err != nil {
			return err
		}
		out = newAgentRegisterV2Result(ident, ns.Code)
		out.Address, out.Endpoints = ident.LastAddr, endpoints
		return auditIdentity(tx, ns, ident, model.ActionIdentityRegistered, "agent", model.ResultOK, p.ClientIP)
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
	if ident == nil || ident.NamespaceID != ns.ID {
		return apperr.ErrUnauthorized
	}
	if err := ensureIdentityRuntimeBindingOpen(ident); err != nil {
		return err
	}
	// 已判冲突：双方都 409（权威取 DB 状态，跨重启可靠，spec §4.5）。
	if ident.Status == model.AgentIdentityStatusConflict {
		return apperr.ErrIdentityConflict
	}
	// bootId 活跃观测：resolve 落败方持续 409 + 指引；同时喂养往复检测。
	if s.bootRegistry != nil {
		if rep := s.bootRegistry.OnReport(identityID, bootID, "", time.Now().UTC(), s.conflictWindowDur()); rep.Evicted {
			return apperr.ErrIdentityConflictLoser
		}
	}
	if ident.Status != model.AgentIdentityStatusActive || !ident.ServerID.Assigned() {
		return apperr.ErrUnauthorized
	}
	if err := ensureServerActive(s.db, ident.NamespaceID, string(ident.ServerID)); err != nil {
		return err
	}
	// 陈旧 boot（与 DB 权威 boot_id 不一致）→ 404 促其重注册，复用 agent「404→重注册」路径喂养往复检测（spec §4.5）。
	// 选 404 而非 401：agent 只把 404 识别为「未注册需重注册」，401 仅退避重试同 boot（真机双实例不触发的根因）。
	if ident.BootID != bootID {
		return apperr.ErrAgentStaleReregister
	}
	return nil
}

// AuthenticateAgentReport 校验 v2 agent 数据面上报端点（指标 / 调度）的鉴权并返回权威绑定身份。
//
// 契约（spec §4.2/§5.1）：token↔namespace + identity 绑定校验。区别于 AuthenticateAgentV2（legacy v1 兼容、
// 一律 401 且需 bootId）——本端点按 spec 只认 token + identity，且把「已识别但未确认（status≠active）」细化为 403，
// 其余非法（token / 身份缺失或跨 namespace）返回 401。成功返回权威 namespace / serverId / kind 供注入 context。
func (s *V2ControlPlaneService) AuthenticateAgentReport(token, identityID, bootID, addr string) (agentauth.Identity, error) {
	if !validUUID(identityID) {
		return agentauth.Identity{}, apperr.ErrUnauthorized
	}
	ns, err := s.namespaceByToken(token)
	if err != nil {
		return agentauth.Identity{}, err
	}
	ident, err := findIdentityByID(s.db, identityID)
	if err != nil {
		return agentauth.Identity{}, err
	}
	if ident == nil || ident.NamespaceID != ns.ID {
		return agentauth.Identity{}, apperr.ErrUnauthorized
	}
	if err := ensureIdentityRuntimeBindingOpen(ident); err != nil {
		return agentauth.Identity{}, err
	}
	// 已判冲突：双方都 409（权威取 DB 状态，跨重启可靠，spec §4.5）。
	if ident.Status == model.AgentIdentityStatusConflict {
		return agentauth.Identity{}, apperr.ErrIdentityConflict
	}
	// bootId 活跃观测（往复检测喂养 + resolve 落败识别，spec §4.5）。
	if s.bootRegistry != nil {
		if rep := s.bootRegistry.OnReport(identityID, bootID, addr, time.Now().UTC(), s.conflictWindowDur()); rep.Evicted {
			// resolve 落败方持续 409 + 指引。
			return agentauth.Identity{}, apperr.ErrIdentityConflictLoser
		}
	}
	if ident.Status != model.AgentIdentityStatusActive || !ident.ServerID.Assigned() {
		return agentauth.Identity{}, apperr.ErrAgentNotConfirmed
	}
	if err := ensureServerActive(s.db, ident.NamespaceID, string(ident.ServerID)); err != nil {
		return agentauth.Identity{}, err
	}
	// 陈旧 boot（与 DB 权威 boot_id 不一致）→ 404 促其重注册，复用 agent「404→重注册」路径喂养往复检测（spec §4.5）。
	// 选 404 而非 401：agent 只把 404 识别为「未注册需重注册」，401 仅退避重试同 boot（真机双实例不触发的根因）。
	// bootId 为空（未带 X-Beacon-Boot 头）时跳过，兼容旧行为不影响存量上报路径。
	if bootID != "" && bootID != ident.BootID {
		return agentauth.Identity{}, apperr.ErrAgentStaleReregister
	}
	return agentauth.Identity{
		NamespaceID: ns.ID, Namespace: ns.Code, ServerID: string(ident.ServerID),
		Kind: ident.Kind, IdentityID: ident.IdentityID,
	}, nil
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
	boundAt, fingerprint := agentBindingSnapshot(ident, ns.Code)
	endpoints, err := agentEndpointViews(s.db, ident.ID)
	if err != nil {
		return nil, err
	}
	return &AgentRegistrationV2Status{
		Status: ident.Status, Namespace: ns.Code, ServerID: optionalIdentityServerID(ident),
		BoundAt: boundAt, BindingFingerprint: fingerprint, Reason: ident.ConflictReason,
		Address: ident.LastAddr, Endpoints: endpoints,
	}, nil
}

func (s *V2ControlPlaneService) registerExistingIdentity(tx *gorm.DB, ns *model.Namespace, current *model.AgentIdentity, p AgentRegisterV2Params, now, expiresAt time.Time, out *AgentRegisterV2Result) error {
	if !model.IsValidAgentIdentityBindingSource(current.BindingSource) {
		return apperr.ErrIllegalState
	}
	sameBinding := current.NamespaceID == ns.ID && current.Kind == p.Kind && (!current.ServerID.Assigned() || p.ServerID == "" || string(current.ServerID) == p.ServerID)
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
		// FR-226：工作目录仅在新值非空时覆盖（心跳可不携带，不抹掉上次上报值）。
		if p.ServerWorkDir != "" {
			current.ServerWorkDir = p.ServerWorkDir
		}
		current.PendingExpiresAt = &expiresAt
	case model.AgentIdentityStatusExpired, model.AgentIdentityStatusUnbound:
		if p.ServerID != "" {
			if err := ensureServerIDAvailableForRegister(tx, ns.ID, p.ServerID); err != nil {
				return err
			}
		}
		current.NamespaceID = ns.ID
		current.ServerID = model.NullableServerID(p.ServerID)
		current.Kind = p.Kind
		current.Status = model.AgentIdentityStatusPending
		current.BindingSource = identityBindingSourceForRegistration(p.ServerID)
		current.BootID = p.BootID
		current.LastAddr = p.Addr
		current.AgentVersion = p.AgentVersion
		// FR-226：工作目录仅在新值非空时覆盖（心跳可不携带，不抹掉上次上报值）。
		if p.ServerWorkDir != "" {
			current.ServerWorkDir = p.ServerWorkDir
		}
		current.PendingExpiresAt = &expiresAt
		current.StatusChangedAt = now
	case model.AgentIdentityStatusActive, model.AgentIdentityStatusDisabled:
		if !current.ServerID.Assigned() {
			return apperr.ErrIdentityBindingMismatch
		}
		if !sameBinding {
			return apperr.ErrIdentityBindingMismatch
		}
		current.BootID = p.BootID
		current.LastAddr = p.Addr
		current.AgentVersion = p.AgentVersion
		// FR-226：工作目录仅在新值非空时覆盖（心跳可不携带，不抹掉上次上报值）。
		if p.ServerWorkDir != "" {
			current.ServerWorkDir = p.ServerWorkDir
		}
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
	if current.BindingSource == model.AgentIdentityBindingSourceLegacyLocal && current.LegacyMigratedAt == nil && p.ServerID == "" &&
		(current.Status == model.AgentIdentityStatusActive || current.Status == model.AgentIdentityStatusDisabled) {
		migratedAt := now
		current.LegacyMigratedAt = &migratedAt
		if err := tx.Save(current).Error; err != nil {
			return err
		}
		if err := auditIdentity(tx, ns, current, model.ActionIdentityLegacyMigrated, "agent", model.ResultOK, p.ClientIP); err != nil {
			return err
		}
	}
	*out = newAgentRegisterV2Result(current, ns.Code)
	return nil
}

func ensureRegistrationServerActive(tx *gorm.DB, namespaceID uint, current *model.AgentIdentity, suppliedServerID string) error {
	if current == nil {
		return ensureServerActive(tx, namespaceID, suppliedServerID)
	}
	switch current.Status {
	case model.AgentIdentityStatusPending, model.AgentIdentityStatusActive, model.AgentIdentityStatusDisabled:
		if current.NamespaceID == namespaceID && current.ServerID.Assigned() {
			return ensureServerActive(tx, namespaceID, string(current.ServerID))
		}
	case model.AgentIdentityStatusExpired, model.AgentIdentityStatusUnbound:
		return ensureServerActive(tx, namespaceID, suppliedServerID)
	}
	return nil
}

type ApproveAgentIdentityParams struct {
	ServerID            string
	Reason              string
	Operator            string
	ClientIP            string
	ForceUnbindOccupier bool
	// TargetExplicitNull 表示请求显式传 target:null——换区重确认时含义为「确认但暂不分配」。
	TargetExplicitNull bool
	// TargetKind / TargetID 表示请求带对象目标（换区重确认落区）；非换区中带目标一律拒。
	TargetKind string
	TargetID   *uint
}

// ApproveAgentIdentity 禁止绕过审批适配器直接确认身份。

func (s *V2ControlPlaneService) ApproveAgentIdentity(_ string, _ ApproveAgentIdentityParams) (*model.AgentIdentity, error) {
	return nil, apperr.ErrForbidden
}

func (s *V2ControlPlaneService) applyApproveAgentIdentity(identityID string, p ApproveAgentIdentityParams) (*model.AgentIdentity, error) {
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
		if err := ensureIdentityRuntimeBindingOpen(ident); err != nil {
			return err
		}
		if ident.Status != model.AgentIdentityStatusPending {
			return apperr.ErrIllegalState
		}
		serverID, err := resolveApprovedServerID(p.ServerID)
		if err != nil {
			return err
		}
		if err := ensureServerActive(tx, ident.NamespaceID, serverID); err != nil {
			return err
		}
		ident.ServerID = model.NullableServerID(serverID)
		if err := ensureServerIDAvailableForApprove(tx, ident.NamespaceID, serverID, ident.IdentityID); err != nil {
			return err
		}
		ns, err := findNamespaceByID(tx, ident.NamespaceID)
		if err != nil {
			return err
		}
		if err := s.resolveOccupierForApprove(tx, ident, p); err != nil {
			return err
		}
		if err := s.applyApproveBinding(tx, ns, ident, p); err != nil {
			return err
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

// applyApproveBinding 处理确认时的 server 落绑定：换区中走重确认落区、否则首次确认只建未分配 server 行。
func (s *V2ControlPlaneService) applyApproveBinding(tx *gorm.DB, ns *model.Namespace, ident *model.AgentIdentity, p ApproveAgentIdentityParams) error {
	server, err := findServerRow(tx, ident.NamespaceID, string(ident.ServerID))
	if err != nil {
		return err
	}
	if server != nil && (server.PendingZoneID != nil || server.PendingBCClusterID != nil) {
		return s.completeRezoneApprove(tx, ns, server, p)
	}
	if p.TargetID != nil {
		return apperr.ErrInvalidParam // 非换区中不允许指定落区目标
	}
	_, err = ensureServerRow(tx, ident.NamespaceID, string(ident.ServerID), ident.Kind)
	return err
}

// completeRezoneApprove 换区工单重确认：按显式 / 预填目标落区（或暂不分配），清 pending，记 zone.rezone.completed 审计。
func (s *V2ControlPlaneService) completeRezoneApprove(tx *gorm.DB, ns *model.Namespace, server *model.Server, p ApproveAgentIdentityParams) error {
	targetKind, targetID, assign := resolveRezoneApproveTarget(server, p)
	if assign {
		targetNS, kind, err := resolveAssignmentTarget(tx, targetKind, targetID)
		if err != nil {
			return err
		}
		if err := validateAssignableServer(server, targetNS, kind); err != nil {
			return err
		}
		applyAssignment(server, kind, targetID, false)
	}
	server.PendingZoneID = nil
	server.PendingBCClusterID = nil
	if err := tx.Save(server).Error; err != nil {
		return err
	}
	return createAudit(tx, model.AuditLog{
		NamespaceCode: ns.Code, Operator: operatorOrSystem(p.Operator),
		Action: model.ActionServerRezoneDone, TargetType: model.TargetTypeServer,
		TargetRef: fmt.Sprintf("%d", server.ID), Result: model.ResultOK, ClientIP: p.ClientIP,
	})
}

// resolveRezoneApproveTarget 定换区重确认落区目标：显式 null=暂不分配；显式对象=该目标；缺省=预填目标。
func resolveRezoneApproveTarget(server *model.Server, p ApproveAgentIdentityParams) (kind string, id uint, assign bool) {
	if p.TargetExplicitNull {
		return "", 0, false
	}
	if p.TargetID != nil {
		return p.TargetKind, *p.TargetID, true
	}
	if server.PendingZoneID != nil {
		return model.AssignmentTargetZone, *server.PendingZoneID, true
	}
	return model.AssignmentTargetBCCluster, *server.PendingBCClusterID, true
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
	return s.applyTransitionIdentity(identityID, []string{model.AgentIdentityStatusPending}, model.AgentIdentityStatusRejected, model.ActionIdentityRejected, p)
}

// AllowAgentIdentityReapply 禁止绕过审批适配器直接恢复重新申请资格。
func (s *V2ControlPlaneService) AllowAgentIdentityReapply(_ string, _ IdentityTransitionParams) (*model.AgentIdentity, error) {
	return nil, apperr.ErrForbidden
}

// DisableAgentIdentity 临时禁用已确认身份。
func (s *V2ControlPlaneService) DisableAgentIdentity(identityID string, p IdentityTransitionParams) (*model.AgentIdentity, error) {
	if p.Reason == "" {
		return nil, apperr.ErrInvalidParam
	}
	return s.applyTransitionIdentity(identityID, []string{model.AgentIdentityStatusActive}, model.AgentIdentityStatusDisabled, model.ActionIdentityDisabled, p)
}

// EnableAgentIdentity 禁止绕过审批适配器直接启用身份。

func (s *V2ControlPlaneService) EnableAgentIdentity(_ string, _ IdentityTransitionParams) (*model.AgentIdentity, error) {
	return nil, apperr.ErrForbidden
}

// UnbindAgentIdentity 禁止绕过审批适配器直接解绑身份。

func (s *V2ControlPlaneService) UnbindAgentIdentity(_ string, _ IdentityTransitionParams) (*model.AgentIdentity, error) {
	return nil, apperr.ErrForbidden
}

func (s *V2ControlPlaneService) applyTransitionIdentity(identityID string, allowed []string, nextStatus, action string, p IdentityTransitionParams) (*model.AgentIdentity, error) {
	now := time.Now().UTC()
	var out model.AgentIdentity
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = s.applyTransitionIdentityInTx(tx, identityID, allowed, nextStatus, action, p, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	if nextStatus == model.AgentIdentityStatusUnbound && s.bootRegistry != nil {
		s.scheduleAfterCommit(func() { s.bootRegistry.Forget(identityID) })
	}
	return &out, nil
}

func (s *V2ControlPlaneService) applyTransitionIdentityInTx(tx *gorm.DB, identityID string, allowed []string, nextStatus, action string, p IdentityTransitionParams, now time.Time) (model.AgentIdentity, error) {
	ident, err := findIdentityByID(tx, identityID)
	if err != nil {
		return model.AgentIdentity{}, err
	}
	if ident == nil {
		return model.AgentIdentity{}, apperr.ErrInstanceNotFound
	}
	if err := ensureIdentityRuntimeBindingOpen(ident); err != nil {
		return model.AgentIdentity{}, err
	}
	if !stringIn(ident.Status, allowed) {
		return model.AgentIdentity{}, apperr.ErrIllegalState
	}
	ns, err := findNamespaceByID(tx, ident.NamespaceID)
	if err != nil {
		return model.AgentIdentity{}, err
	}
	ident.Status = nextStatus
	ident.StatusChangedAt = now
	if nextStatus != model.AgentIdentityStatusPending {
		ident.PendingExpiresAt = nil
	}
	if nextStatus == model.AgentIdentityStatusUnbound {
		ident.ConflictReason = ""
		// 解绑同时清 server 归属，使树与资产列表即时反映无可信 agent。
		if err := clearServerAssignmentByIdentity(tx, ident); err != nil {
			return model.AgentIdentity{}, err
		}
	}
	if err := tx.Save(ident).Error; err != nil {
		return model.AgentIdentity{}, err
	}
	if err := createAudit(tx, model.AuditLog{
		NamespaceCode: ns.Code,
		Operator:      operatorOrSystem(p.Operator),
		Action:        action,
		TargetType:    model.TargetTypeIdentity,
		TargetRef:     ident.IdentityID,
		Detail:        p.Reason,
		Result:        model.ResultOK,
		ClientIP:      p.ClientIP,
	}); err != nil {
		return model.AgentIdentity{}, err
	}
	return *ident, nil
}

// clearServerAssignmentByIdentity 按身份定位 server 行并清空全部归属（解绑联动）。
func clearServerAssignmentByIdentity(tx *gorm.DB, ident *model.AgentIdentity) error {
	if !ident.ServerID.Assigned() {
		return apperr.ErrIllegalState
	}
	server, err := findServerRow(tx, ident.NamespaceID, string(ident.ServerID))
	if err != nil {
		return err
	}
	if server == nil {
		return nil
	}
	server.ZoneID = nil
	server.BCClusterID = nil
	server.LobbyClusterID = nil
	server.IsDefaultEntry = false
	return tx.Save(server).Error
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

// ListAgentIdentityReadViews 分页查询身份列表并补齐只读绑定事实，避免 handler 另造指纹。
func (s *V2ControlPlaneService) ListAgentIdentityReadViews(p ListAgentIdentitiesParams) ([]AgentIdentityReadView, int64, error) {
	items, total, err := s.ListAgentIdentities(p)
	if err != nil {
		return nil, 0, err
	}
	views, err := enrichAgentIdentityReadViews(s.db, items)
	if err != nil {
		return nil, 0, err
	}
	return views, total, nil
}

type ListServersParams struct {
	NamespaceID     uint
	Kind            string
	Assigned        *bool
	Keyword         string
	LifecycleStatus string
	Lifecycle       string
	// Tags 按 server 标签交集筛选（FR-227，键值全等命中）；空表示不过滤。
	Tags     map[string]string
	Page     int
	PageSize int
}

type UpdateServerDisplayNameParams struct {
	ID          uint
	ServerID    *string
	DisplayName *string
	Operator    string
	ClientIP    string
}

func (s *V2ControlPlaneService) UpdateServerDisplayName(p UpdateServerDisplayNameParams) (*ServerView, error) {
	if p.ID == 0 {
		return nil, apperr.ErrInvalidParam
	}
	var server model.Server
	if err := s.db.First(&server, p.ID).Error; err != nil {
		return nil, apperr.ErrInstanceNotFound
	}
	if p.ServerID != nil && strings.TrimSpace(*p.ServerID) != server.ServerID {
		return nil, apperr.ErrImmutableIdentifier
	}
	oldDisplayName := server.DisplayName
	if oldDisplayName == "" {
		oldDisplayName = server.ServerID
		server.DisplayName = server.ServerID
	}
	if p.DisplayName != nil {
		next := strings.TrimSpace(*p.DisplayName)
		if !isValidDisplayName(next) {
			return nil, apperr.ErrInvalidParam
		}
		server.DisplayName = next
	}
	detail := auditJSON(map[string]string{"serverId": server.ServerID, "oldDisplayName": oldDisplayName, "newDisplayName": server.DisplayName})
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&server).Error; err != nil {
			return err
		}
		return createAudit(tx, model.AuditLog{Operator: operatorOrSystem(p.Operator), Action: model.ActionServerUpdate, TargetType: model.TargetTypeServer, TargetRef: server.ServerID, Detail: detail, Result: model.ResultOK, ClientIP: p.ClientIP})
	}); err != nil {
		return nil, err
	}
	views, err := enrichServers(s.db, []model.Server{server})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// ListServers 分页查询 v2 server 资产列表，返回富化视图（含归属名 / 默认入口 / 在线摘要）。
func (s *V2ControlPlaneService) ListServers(p ListServersParams) ([]ServerView, int64, error) {
	q := s.db.Model(&model.Server{})
	lifecycle, err := normalizeLifecycleFilter(p.LifecycleStatus, p.Lifecycle)
	if err != nil {
		return nil, 0, err
	}
	q = applyLifecycleFilter(q, lifecycle)
	if p.NamespaceID != 0 {
		q = q.Where("namespace_id = ?", p.NamespaceID)
	}
	if p.Kind != "" {
		q = q.Where("kind = ?", p.Kind)
	}
	if p.Assigned != nil {
		if *p.Assigned {
			q = q.Where("zone_id IS NOT NULL OR bc_cluster_id IS NOT NULL OR lobby_cluster_id IS NOT NULL")
		} else {
			q = q.Where("zone_id IS NULL AND bc_cluster_id IS NULL AND lobby_cluster_id IS NULL")
		}
	}
	if p.Keyword != "" {
		like := "%" + p.Keyword + "%"
		q = q.Where("server_id LIKE ? OR display_name LIKE ?", like, like)
	}
	// 标签交集筛选（FR-227）：先求命中 server_pk 集合再限定 id IN，避免 join 重复行。
	if len(p.Tags) > 0 {
		matched, err := serverRefsMatchingTags(s.db, p.NamespaceID, p.Tags)
		if err != nil {
			return nil, 0, err
		}
		if len(matched) == 0 {
			return []ServerView{}, 0, nil
		}
		ids := make([]uint, 0, len(matched))
		pkQ := s.db.Model(&model.Server{}).Select("id")
		nsConds := make([]string, 0, len(matched))
		args := make([]any, 0, len(matched)*2)
		for k := range matched {
			nsConds = append(nsConds, "(namespace_id = ? AND server_id = ?)")
			args = append(args, k.namespaceID, k.serverID)
		}
		if err := pkQ.Where(strings.Join(nsConds, " OR "), args...).Pluck("id", &ids).Error; err != nil {
			return nil, 0, err
		}
		if len(ids) == 0 {
			return []ServerView{}, 0, nil
		}
		q = q.Where("id IN ?", ids)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []model.Server
	if err := q.Order("updated_at DESC").Offset(pageOffset(p.Page, p.PageSize)).Limit(pageSize(p.PageSize)).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	views, err := enrichServers(s.db, items)
	if err != nil {
		return nil, 0, err
	}
	return views, total, nil
}

func normalizeLifecycleFilter(status, legacy string) (string, error) {
	status = strings.TrimSpace(status)
	legacy = strings.TrimSpace(legacy)
	if status != "" && legacy != "" && status != legacy {
		return "", apperr.ErrInvalidParam
	}
	if status == "" {
		status = legacy
	}
	if status == "" {
		return model.ServerLifecycleActive, nil
	}
	if status != model.ServerLifecycleActive && status != model.ServerLifecycleArchived && status != model.ServerLifecycleTombstoned && status != "all" {
		return "", apperr.ErrInvalidParam
	}
	return status, nil
}

func applyLifecycleFilter(q *gorm.DB, lifecycle string) *gorm.DB {
	switch lifecycle {
	case model.ServerLifecycleActive:
		return q.Where("(lifecycle = ? OR lifecycle = '' OR lifecycle IS NULL)", model.ServerLifecycleActive)
	case model.ServerLifecycleArchived:
		return q.Where("lifecycle = ?", model.ServerLifecycleArchived)
	case model.ServerLifecycleTombstoned:
		return q.Where("lifecycle = ?", model.ServerLifecycleTombstoned)
	default:
		return q
	}
}

// ServerLifecycleImpactView 是 server 生命周期操作的只读、有界影响摘要。
type ServerLifecycleImpactView struct {
	ServerRowID         uint   `json:"serverRowId"`
	NamespaceID         uint   `json:"namespaceId"`
	ServerID            string `json:"serverId"`
	Action              string `json:"action"`
	CurrentLifecycle    string `json:"currentLifecycle"`
	TargetLifecycle     string `json:"targetLifecycle"`
	EffectiveActive     bool   `json:"effectiveActive"`
	Online              bool   `json:"online"`
	Assigned            bool   `json:"assigned"`
	DefaultEntry        bool   `json:"defaultEntry"`
	Draining            bool   `json:"draining"`
	IdentityCount       int64  `json:"identityCount"`
	ActiveIdentityCount int64  `json:"activeIdentityCount"`
	ActiveCommandCount  int64  `json:"activeCommandCount"`
}

// GetServerLifecycleImpact 返回当前 server 的脱敏影响预览，不执行状态变更。
func (s *V2ControlPlaneService) GetServerLifecycleImpact(id uint, action string) (ServerLifecycleImpactView, error) {
	operation, ok := lifecycleImpactOperation(action)
	if !ok {
		return ServerLifecycleImpactView{}, apperr.ErrInvalidParam
	}
	var server model.Server
	if err := s.db.First(&server, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ServerLifecycleImpactView{}, apperr.ErrInstanceNotFound
		}
		return ServerLifecycleImpactView{}, err
	}
	lifecycle := serverLifecycleValue(&server)
	if err := validateServerLifecycleRequest(operation, lifecycle); err != nil {
		return ServerLifecycleImpactView{}, err
	}
	var namespace model.Namespace
	if err := s.db.First(&namespace, server.NamespaceID).Error; err != nil {
		return ServerLifecycleImpactView{}, err
	}
	var identityCount, activeIdentityCount, activeCommandCount int64
	identityQuery := s.db.Model(&model.AgentIdentity{}).Where("namespace_id = ? AND server_id = ?", server.NamespaceID, server.ServerID)
	if err := identityQuery.Count(&identityCount).Error; err != nil {
		return ServerLifecycleImpactView{}, err
	}
	if err := identityQuery.Where("status = ?", model.AgentIdentityStatusActive).Count(&activeIdentityCount).Error; err != nil {
		return ServerLifecycleImpactView{}, err
	}
	if s.db.Migrator().HasTable(&model.AgentCommand{}) {
		if err := s.db.Model(&model.AgentCommand{}).Where("namespace = ? AND server_id = ? AND status IN ?", namespace.Code, server.ServerID, []string{model.CommandStatusPending, model.CommandStatusFetched, model.CommandStatusReady}).Count(&activeCommandCount).Error; err != nil {
			return ServerLifecycleImpactView{}, err
		}
	}
	return ServerLifecycleImpactView{
		ServerRowID: id, NamespaceID: server.NamespaceID, ServerID: server.ServerID, Action: action,
		CurrentLifecycle: lifecycle, TargetLifecycle: targetLifecycle(operation), EffectiveActive: lifecycle == model.ServerLifecycleActive,
		Online: s.serverOnline(namespace.Code, server.ServerID), Assigned: isServerAssigned(&server),
		DefaultEntry: server.IsDefaultEntry, Draining: server.Draining,
		IdentityCount: identityCount, ActiveIdentityCount: activeIdentityCount, ActiveCommandCount: activeCommandCount,
	}, nil
}

func lifecycleImpactOperation(action string) (string, bool) {
	switch action {
	case "archive", authz.OperationServerArchive:
		return authz.OperationServerArchive, true
	case "restore", authz.OperationServerRestore:
		return authz.OperationServerRestore, true
	case "permanent-delete", authz.OperationServerPermanentDelete:
		return authz.OperationServerPermanentDelete, true
	default:
		return "", false
	}
}

func targetLifecycle(operation string) string {
	if operation == authz.OperationServerArchive {
		return model.ServerLifecycleArchived
	}
	if operation == authz.OperationServerPermanentDelete {
		return model.ServerLifecycleTombstoned
	}
	return model.ServerLifecycleActive
}

func (s *V2ControlPlaneService) serverOnline(namespace, serverID string) bool {
	if s.runtime == nil {
		return false
	}
	for _, inst := range s.runtime.List(runtime.Filter{Namespace: namespace}) {
		if inst.ServerID == serverID && (inst.Status == runtime.StatusOnline || inst.Status == runtime.StatusDegraded) {
			return true
		}
	}
	return false
}

func (s *V2ControlPlaneService) ListNamespaceTrusts() ([]NamespaceTrustView, error) {
	var items []model.NamespaceTrust
	if err := s.db.Order("updated_at DESC").Find(&items).Error; err != nil {
		return nil, err
	}
	return enrichTrusts(s.db, items)
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
	occupier, err := findActiveIdentityByServer(tx, ident.NamespaceID, string(ident.ServerID), ident.IdentityID)
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
	Reason          string
	Operator        string
	ClientIP        string
}

// GrantNamespaceTrust 授予或复活一条 namespace 信任。
func (s *V2ControlPlaneService) GrantNamespaceTrust(_ GrantNamespaceTrustParams) (*NamespaceTrustView, error) {
	return nil, apperr.ErrForbidden
}

func (s *V2ControlPlaneService) applyGrantNamespaceTrust(p GrantNamespaceTrustParams) (*NamespaceTrustView, error) {
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
	if err := s.reloadTrustSnapshotAfterCommit(); err != nil {
		return nil, err
	}
	return enrichTrust(s.db, &out)
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
	Code        string
	DisplayName string
	Description string
	Operator    string
	ClientIP    string
}

func (s *V2ControlPlaneService) CreateBCCluster(p CreateBCClusterParams) (*model.BCCluster, error) {
	code, displayName, err := normalizeStableName(p.Name, p.Code, p.DisplayName)
	if err != nil {
		return nil, err
	}
	if p.NamespaceID == 0 {
		return nil, apperr.ErrInvalidParam
	}
	cluster := &model.BCCluster{NamespaceID: p.NamespaceID, Code: code, Name: displayName, Description: p.Description}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := ensureNamespacesExist(tx, p.NamespaceID); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.BCCluster{}).Where("namespace_id = ? AND code = ?", p.NamespaceID, code).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return apperr.ErrBCClusterConflict
		}
		if err := tx.Create(cluster).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return apperr.ErrBCClusterConflict
			}
			return err
		}
		return createAudit(tx, model.AuditLog{
			Operator: operatorOrSystem(p.Operator), Action: model.ActionBCClusterCreate,
			TargetType: model.TargetTypeBCCluster, TargetRef: fmt.Sprintf("%d", cluster.ID),
			Detail: auditJSON(map[string]string{"code": code, "displayName": displayName}),
			Result: model.ResultOK, ClientIP: p.ClientIP,
		})
	})
	return cluster, err
}

type CreateRegionParams struct {
	BCClusterID uint
	Name        string
	Code        string
	DisplayName string
	Description string
	Operator    string
	ClientIP    string
}

func (s *V2ControlPlaneService) CreateRegion(p CreateRegionParams) (*model.Region, error) {
	code, displayName, err := normalizeStableName(p.Name, p.Code, p.DisplayName)
	if err != nil {
		return nil, err
	}
	if p.BCClusterID == 0 {
		return nil, apperr.ErrInvalidParam
	}
	region := &model.Region{BCClusterID: p.BCClusterID, Code: code, Name: displayName, Description: p.Description}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := ensureBCClusterExists(tx, p.BCClusterID); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.Region{}).Where("bc_cluster_id = ? AND code = ?", p.BCClusterID, code).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return apperr.ErrRegionConflict
		}
		if err := tx.Create(region).Error; err != nil {

			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return apperr.ErrRegionConflict
			}
			return err
		}
		return createAudit(tx, model.AuditLog{
			Operator: operatorOrSystem(p.Operator), Action: model.ActionRegionCreate,
			TargetType: model.TargetTypeRegion, TargetRef: fmt.Sprintf("%d", region.ID),
			Detail: auditJSON(map[string]string{"code": code, "displayName": displayName}),
			Result: model.ResultOK, ClientIP: p.ClientIP,
		})
	})
	return region, err
}

type CreateZoneParams struct {
	RegionID    uint
	Name        string
	Code        string
	DisplayName string
	Description string
	Operator    string
	ClientIP    string
}

func (s *V2ControlPlaneService) CreateZone(p CreateZoneParams) (*model.Zone, error) {
	code, displayName, err := normalizeStableName(p.Name, p.Code, p.DisplayName)
	if err != nil {
		return nil, err
	}
	if p.RegionID == 0 {
		return nil, apperr.ErrInvalidParam
	}
	zone := &model.Zone{RegionID: p.RegionID, Code: code, Name: displayName, Description: p.Description}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := ensureRegionExists(tx, p.RegionID); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.Zone{}).Where("region_id = ? AND code = ?", p.RegionID, code).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return apperr.ErrZoneConflict
		}
		if err := tx.Create(zone).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return apperr.ErrZoneConflict
			}
			return err
		}
		return createAudit(tx, model.AuditLog{
			Operator: operatorOrSystem(p.Operator), Action: model.ActionZoneCreate,
			TargetType: model.TargetTypeZone, TargetRef: fmt.Sprintf("%d", zone.ID),
			Detail: auditJSON(map[string]string{"code": code, "displayName": displayName}),
			Result: model.ResultOK, ClientIP: p.ClientIP,
		})
	})
	return zone, err
}

type UpdateDisplayResourceParams struct {
	ID          uint
	Code        *string
	Name        *string
	DisplayName *string
	Description *string
	Operator    string
	ClientIP    string
}

func (s *V2ControlPlaneService) UpdateNamespace(p UpdateDisplayResourceParams) (*model.Namespace, error) {
	if p.ID == 0 {
		return nil, apperr.ErrInvalidParam
	}
	var ns model.Namespace
	if err := s.db.First(&ns, p.ID).Error; err != nil {
		return nil, apperr.ErrNamespaceNotFound
	}
	if err := rejectCodeChange(p.Code, ns.Code); err != nil {
		return nil, err
	}
	oldName := ns.Name
	if next, changed, err := normalizeDisplayNamePatch(ns.Code, ns.Name, p.Name, p.DisplayName); err != nil {
		return nil, err
	} else if changed {
		ns.Name = next
	}
	if p.Description != nil {
		ns.Description = *p.Description
	}
	detail := auditJSON(map[string]string{"code": ns.Code, "oldDisplayName": oldName, "newDisplayName": ns.Name})
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&ns).Error; err != nil {
			return err
		}
		return createAudit(tx, model.AuditLog{NamespaceCode: ns.Code, Operator: operatorOrSystem(p.Operator), Action: model.ActionNamespaceUpdate, TargetType: model.TargetTypeNamespace, TargetRef: ns.Code, Detail: detail, Result: model.ResultOK, ClientIP: p.ClientIP})
	}); err != nil {
		return nil, err
	}
	return &ns, nil
}

func (s *V2ControlPlaneService) UpdateBCCluster(p UpdateDisplayResourceParams) (*model.BCCluster, error) {
	var item model.BCCluster
	if err := loadForDisplayUpdate(s.db, p.ID, &item, apperr.ErrBCClusterNotFound); err != nil {
		return nil, err
	}
	if err := rejectCodeChange(p.Code, item.Code); err != nil {
		return nil, err
	}
	oldName := item.Name
	if err := applyDisplayPatch(item.Code, &item.Name, p.Name, p.DisplayName); err != nil {
		return nil, err
	}
	if p.Description != nil {
		item.Description = *p.Description
	}
	if err := s.saveDisplayAudit(&item, model.ActionBCClusterUpdate, model.TargetTypeBCCluster, p.Operator, p.ClientIP, item.Code, oldName, item.Name); err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *V2ControlPlaneService) UpdateRegion(p UpdateDisplayResourceParams) (*model.Region, error) {
	var item model.Region
	if err := loadForDisplayUpdate(s.db, p.ID, &item, apperr.ErrRegionNotFound); err != nil {
		return nil, err
	}
	if err := rejectCodeChange(p.Code, item.Code); err != nil {
		return nil, err
	}
	oldName := item.Name
	if err := applyDisplayPatch(item.Code, &item.Name, p.Name, p.DisplayName); err != nil {
		return nil, err
	}
	if p.Description != nil {
		item.Description = *p.Description
	}
	if err := s.saveDisplayAudit(&item, model.ActionRegionUpdate, model.TargetTypeRegion, p.Operator, p.ClientIP, item.Code, oldName, item.Name); err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *V2ControlPlaneService) UpdateZone(p UpdateDisplayResourceParams) (*model.Zone, error) {
	var item model.Zone
	if err := loadForDisplayUpdate(s.db, p.ID, &item, apperr.ErrZoneNotFound); err != nil {
		return nil, err
	}
	if err := rejectCodeChange(p.Code, item.Code); err != nil {
		return nil, err
	}
	oldName := item.Name
	if err := applyDisplayPatch(item.Code, &item.Name, p.Name, p.DisplayName); err != nil {
		return nil, err
	}
	if p.Description != nil {
		item.Description = *p.Description
	}
	if err := s.saveDisplayAudit(&item, model.ActionZoneUpdate, model.TargetTypeZone, p.Operator, p.ClientIP, item.Code, oldName, item.Name); err != nil {
		return nil, err
	}
	return &item, nil
}

func loadForDisplayUpdate(db *gorm.DB, id uint, out any, notFound error) error {
	if id == 0 {
		return apperr.ErrInvalidParam
	}
	if err := db.First(out, id).Error; err != nil {
		return notFound
	}
	return nil
}

func rejectCodeChange(code *string, current string) error {
	if code != nil && strings.TrimSpace(*code) != current {
		return apperr.ErrImmutableIdentifier
	}
	return nil
}

func applyDisplayPatch(code string, current *string, name, displayName *string) error {
	next, changed, err := normalizeDisplayNamePatch(code, *current, name, displayName)
	if err != nil {
		return err
	}
	if changed {
		*current = next
	}
	return nil
}

func (s *V2ControlPlaneService) saveDisplayAudit(value any, action, targetType, operator, clientIP, code, oldDisplayName, newDisplayName string) error {
	detail := auditJSON(map[string]string{"code": code, "oldDisplayName": oldDisplayName, "newDisplayName": newDisplayName})
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(value).Error; err != nil {
			return err
		}
		return createAudit(tx, model.AuditLog{Operator: operatorOrSystem(operator), Action: action, TargetType: targetType, TargetRef: code, Detail: detail, Result: model.ResultOK, ClientIP: clientIP})
	})
}

// DeleteNodeParams 删除结构节点的公共参数。
type DeleteNodeParams struct {
	ID       uint
	Operator string
	ClientIP string
}

// DeleteBCCluster 删除空的 BC 集群：无大区、无已分配代理。
func (s *V2ControlPlaneService) DeleteBCCluster(p DeleteNodeParams) error {
	if p.ID == 0 {
		return apperr.ErrInvalidParam
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var cluster model.BCCluster
		if err := tx.First(&cluster, p.ID).Error; err != nil {
			return apperr.ErrBCClusterNotFound
		}
		var regionCount int64
		if err := tx.Model(&model.Region{}).Where("bc_cluster_id = ?", p.ID).Count(&regionCount).Error; err != nil {
			return err
		}
		if regionCount > 0 {
			return apperr.ErrBCClusterHasRegions
		}
		var proxyCount int64
		if err := tx.Model(&model.Server{}).Where("bc_cluster_id = ?", p.ID).Count(&proxyCount).Error; err != nil {
			return err
		}
		if proxyCount > 0 {
			return apperr.ErrBCClusterHasProxies
		}
		if err := tx.Delete(&cluster).Error; err != nil {
			return err
		}
		return createAudit(tx, model.AuditLog{
			Operator: operatorOrSystem(p.Operator), Action: model.ActionBCClusterDelete,
			TargetType: model.TargetTypeBCCluster, TargetRef: fmt.Sprintf("%d", p.ID),
			Detail: cluster.Name, Result: model.ResultOK, ClientIP: p.ClientIP,
		})
	})
}

// DeleteRegion 删除空的大区：无小区。
func (s *V2ControlPlaneService) DeleteRegion(p DeleteNodeParams) error {
	if p.ID == 0 {
		return apperr.ErrInvalidParam
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var region model.Region
		if err := tx.First(&region, p.ID).Error; err != nil {
			return apperr.ErrRegionNotFound
		}
		var zoneCount int64
		if err := tx.Model(&model.Zone{}).Where("region_id = ?", p.ID).Count(&zoneCount).Error; err != nil {
			return err
		}
		if zoneCount > 0 {
			return apperr.ErrRegionHasZones
		}
		if err := tx.Delete(&region).Error; err != nil {
			return err
		}
		return createAudit(tx, model.AuditLog{
			Operator: operatorOrSystem(p.Operator), Action: model.ActionRegionDelete,
			TargetType: model.TargetTypeRegion, TargetRef: fmt.Sprintf("%d", p.ID),
			Detail: region.Name, Result: model.ResultOK, ClientIP: p.ClientIP,
		})
	})
}

// DeleteZone 删除空的小区：无已分配子服。
func (s *V2ControlPlaneService) DeleteZone(p DeleteNodeParams) error {
	if p.ID == 0 {
		return apperr.ErrInvalidParam
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var zone model.Zone
		if err := tx.First(&zone, p.ID).Error; err != nil {
			return apperr.ErrZoneNotFound
		}
		var serverCount int64
		if err := tx.Model(&model.Server{}).Where("zone_id = ?", p.ID).Count(&serverCount).Error; err != nil {
			return err
		}
		if serverCount > 0 {
			return apperr.ErrZoneHasServers
		}
		if err := tx.Delete(&zone).Error; err != nil {
			return err
		}
		return createAudit(tx, model.AuditLog{
			Operator: operatorOrSystem(p.Operator), Action: model.ActionZoneDelete,
			TargetType: model.TargetTypeZone, TargetRef: fmt.Sprintf("%d", p.ID),
			Detail: zone.Name, Result: model.ResultOK, ClientIP: p.ClientIP,
		})
	})
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

// AssignServers 批量首次分配未分配 server；TargetKind 为空且 TargetID=0 时表示解除分配（target:null）。
func (s *V2ControlPlaneService) AssignServers(_ AssignServersParams) ([]model.Server, error) {
	return nil, apperr.ErrForbidden
}

func (s *V2ControlPlaneService) applyAssignServers(p AssignServersParams) ([]model.Server, error) {
	if len(p.ServerIDs) == 0 {
		return nil, apperr.ErrInvalidParam
	}
	// 解除分配：target 显式 null 时 TargetKind/TargetID 均为零值
	if p.TargetKind == "" && p.TargetID == 0 {
		return s.unassignServers(p)
	}
	if p.TargetID == 0 {
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
		// 批量分配勾选默认入口时，同区只允许一台：先清目标小区其它默认入口
		if p.IsDefaultEntry && targetKind == model.AssignmentTargetZone {
			if err := clearOtherDefaultEntriesInZone(tx, p.TargetID, 0); err != nil {
				return err
			}
		}
		// 本批多台同时勾默认入口：仅首台 backend 保留，避免一区多入口
		defaultGranted := false
		for i := range servers {
			if err := validateAssignableServer(&servers[i], targetNS, targetKind); err != nil {
				return err
			}
			wantDefault := p.IsDefaultEntry
			if wantDefault && targetKind == model.AssignmentTargetZone {
				if defaultGranted {
					wantDefault = false
				} else {
					defaultGranted = true
				}
			}
			applyAssignment(&servers[i], targetKind, p.TargetID, wantDefault)
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

// unassignServers 批量解除分配：清空 zone_id / bc_cluster_id / lobby_cluster_id / 默认入口，原因必填。
func (s *V2ControlPlaneService) unassignServers(p AssignServersParams) ([]model.Server, error) {
	if p.Reason == "" {
		return nil, apperr.ErrInvalidParam
	}
	var out []model.Server
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var servers []model.Server
		if err := tx.Where("id IN ?", p.ServerIDs).Find(&servers).Error; err != nil {
			return err
		}
		if len(servers) != len(p.ServerIDs) {
			return apperr.ErrInstanceNotFound
		}
		for i := range servers {
			servers[i].ZoneID = nil
			servers[i].BCClusterID = nil
			servers[i].LobbyClusterID = nil
			servers[i].IsDefaultEntry = false
			if err := tx.Save(&servers[i]).Error; err != nil {
				return err
			}
			if err := createAudit(tx, model.AuditLog{
				Operator: operatorOrSystem(p.Operator), Action: model.ActionServerUnassign,
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

// applyAssignment 把 server 归属落到目标（zone→backend 落 zone_id，bc_cluster→proxy 落 bc_cluster_id）。
func applyAssignment(server *model.Server, targetKind string, targetID uint, isDefaultEntry bool) {
	id := targetID
	if targetKind == model.AssignmentTargetZone {
		server.BCClusterID = nil
		server.LobbyClusterID = nil
		server.ZoneID = &id
		server.IsDefaultEntry = isDefaultEntry
	} else {
		server.ZoneID = nil
		server.LobbyClusterID = nil
		server.BCClusterID = &id
		server.IsDefaultEntry = false
	}
}

func validateAssignableServer(server *model.Server, targetNS uint, targetKind string) error {
	if server.NamespaceID != targetNS {
		return apperr.ErrForbidden
	}
	if isServerAssigned(server) {
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

// AssignmentResult 是批量分配 / 换区的逐台结果（对齐 mock zone-authority.ts 的 AssignmentResult）。
type AssignmentResult struct {
	ID       uint   `json:"id"`
	ServerID string `json:"serverId"`
	Ok       bool   `json:"ok"`
	Code     string `json:"code,omitempty"`
}

type RezoneServersParams struct {
	ServerIDs  []uint
	TargetKind string
	TargetID   uint
	Reason     string
	Operator   string
	ClientIP   string
}

// RezoneServers 批量发起换区工单（§4.7）：逐台校验已分配 + 同 namespace + 同 kind，
// 单事务内解绑清归属 + 写预填目标 + 驱动身份重入 pending + 记 zone.rezone.initiated 审计；任一失败整批回滚。
func (s *V2ControlPlaneService) RezoneServers(_ RezoneServersParams) ([]AssignmentResult, error) {
	return nil, apperr.ErrForbidden
}

func (s *V2ControlPlaneService) applyRezoneServers(p RezoneServersParams) ([]AssignmentResult, error) {
	if len(p.ServerIDs) == 0 || p.TargetID == 0 || p.Reason == "" || !model.IsValidAssignmentTarget(p.TargetKind) {
		return nil, apperr.ErrInvalidParam
	}
	now := time.Now().UTC()
	expiresAt := now.Add(defaultPendingTTL)
	var results []AssignmentResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		targetNS, targetKind, err := resolveAssignmentTarget(tx, p.TargetKind, p.TargetID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.ErrInstanceNotFound
			}
			return err
		}
		servers, err := loadServersByIDs(tx, p.ServerIDs)
		if err != nil {
			return err
		}
		for i := range servers {
			if err := validateRezonableServer(&servers[i], targetNS, targetKind); err != nil {
				return err
			}
		}
		results = make([]AssignmentResult, 0, len(servers))
		for i := range servers {
			if err := s.initRezone(tx, &servers[i], p, targetKind, now, expiresAt); err != nil {
				return err
			}
			results = append(results, AssignmentResult{ID: servers[i].ID, ServerID: servers[i].ServerID, Ok: true})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// initRezone 对单台已分配 server 发起换区：解绑清全部归属（含默认入口）+ 写预填目标 + 驱动身份重入 pending + 审计。
func (s *V2ControlPlaneService) initRezone(tx *gorm.DB, server *model.Server, p RezoneServersParams, targetKind string, now, expiresAt time.Time) error {
	id := p.TargetID
	server.ZoneID = nil
	server.BCClusterID = nil
	server.LobbyClusterID = nil
	server.IsDefaultEntry = false
	if targetKind == model.AssignmentTargetZone {
		server.PendingZoneID = &id
		server.PendingBCClusterID = nil
	} else {
		server.PendingBCClusterID = &id
		server.PendingZoneID = nil
	}
	if err := tx.Save(server).Error; err != nil {
		return err
	}
	if err := driveIdentityPending(tx, server.NamespaceID, server.ServerID, now, expiresAt); err != nil {
		return err
	}
	return createAudit(tx, model.AuditLog{
		Operator: operatorOrSystem(p.Operator), Action: model.ActionServerRezoneInit,
		TargetType: model.TargetTypeServer, TargetRef: fmt.Sprintf("%d", server.ID),
		Detail: p.Reason, Result: model.ResultOK, ClientIP: p.ClientIP,
	})
}

// driveIdentityPending 换区工单编排：把绑定该 server 的身份直接重入 pending（对齐 mock：工单编排不经 unbound 中转）。
func driveIdentityPending(tx *gorm.DB, namespaceID uint, serverID string, now, expiresAt time.Time) error {
	ident, err := findBoundIdentityByServer(tx, namespaceID, serverID)
	if err != nil || ident == nil {
		return err
	}
	exp := expiresAt
	ident.Status = model.AgentIdentityStatusPending
	ident.PendingExpiresAt = &exp
	ident.StatusChangedAt = now
	return tx.Save(ident).Error
}

func validateRezonableServer(server *model.Server, targetNS uint, targetKind string) error {
	if !isServerAssigned(server) {
		return apperr.ErrRezoneNotAssigned
	}
	if server.LobbyClusterID != nil && server.ZoneID == nil && server.BCClusterID == nil {
		return apperr.ErrIllegalState
	}
	if server.NamespaceID != targetNS {
		return apperr.ErrForbidden
	}
	if targetKind == model.AssignmentTargetZone && server.Kind != model.ServerKindBackend {
		return apperr.ErrInvalidParam
	}
	if targetKind == model.AssignmentTargetBCCluster && server.Kind != model.ServerKindProxy {
		return apperr.ErrInvalidParam
	}
	return nil
}

func loadServersByIDs(tx *gorm.DB, ids []uint) ([]model.Server, error) {
	var servers []model.Server
	if err := tx.Where("id IN ?", ids).Find(&servers).Error; err != nil {
		return nil, err
	}
	if len(servers) != len(ids) {
		return nil, apperr.ErrInstanceNotFound
	}
	return servers, nil
}

func findBoundIdentityByServer(tx *gorm.DB, namespaceID uint, serverID string) (*model.AgentIdentity, error) {
	var ident model.AgentIdentity
	err := tx.Where("namespace_id = ? AND server_id = ? AND status IN ?", namespaceID, serverID,
		[]string{model.AgentIdentityStatusActive, model.AgentIdentityStatusDisabled}).First(&ident).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ident, nil
}

type SetServerDrainingParams struct {
	ServerID string
	Draining bool
	Reason   string
	Operator string
	ClientIP string
}

// SetServerDraining 切换 server 排空标记（消费方为调度 schedulable 判定），单事务 + 审计，返回富化视图。
// 路径按业务 serverId 定位（前端契约不带 namespace，同名 serverId 取首条）。
func (s *V2ControlPlaneService) SetServerDraining(p SetServerDrainingParams) (*ServerView, error) {
	if !p.Draining {
		return nil, apperr.ErrForbidden
	}
	return s.applySetServerDraining(p)
}

func (s *V2ControlPlaneService) applySetServerDraining(p SetServerDrainingParams) (*ServerView, error) {
	if p.ServerID == "" {
		return nil, apperr.ErrInvalidParam
	}
	var view *ServerView
	err := s.db.Transaction(func(tx *gorm.DB) error {
		server, err := findServerByServerID(tx, p.ServerID)
		if err != nil {
			return err
		}
		server.Draining = p.Draining
		if err := tx.Save(server).Error; err != nil {
			return err
		}
		if err := createAudit(tx, model.AuditLog{
			Operator: operatorOrSystem(p.Operator), Action: model.ActionServerSetDraining,
			TargetType: model.TargetTypeServer, TargetRef: fmt.Sprintf("%d", server.ID),
			Detail: p.Reason, Result: model.ResultOK, ClientIP: p.ClientIP,
		}); err != nil {
			return err
		}
		view, err = enrichSingleServer(tx, *server)
		return err
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

type SetServerDefaultEntryParams struct {
	ServerRowID uint
	Value       bool
	Reason      string
	Operator    string
	ClientIP    string
}

// SetServerDefaultEntry 更新 server 默认入口标记；未分配小区（zone_id 为空）置默认入口一律 409。
// 同一小区至多一台默认入口：置 true 时先清掉同 zone 其他服的标记，再写本机。
// 单事务 + 审计，返回富化视图。
func (s *V2ControlPlaneService) SetServerDefaultEntry(_ SetServerDefaultEntryParams) (*ServerView, error) {
	return nil, apperr.ErrForbidden
}

func (s *V2ControlPlaneService) applySetServerDefaultEntry(p SetServerDefaultEntryParams) (*ServerView, error) {
	if p.ServerRowID == 0 {
		return nil, apperr.ErrInvalidParam
	}
	var view *ServerView
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var server model.Server
		if err := tx.First(&server, p.ServerRowID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.ErrInstanceNotFound
			}
			return err
		}
		if server.ZoneID == nil {
			return apperr.ErrDefaultEntryNotAssigned
		}
		// 置为默认入口：同小区其它服一律清掉，保证一区一台
		if p.Value {
			if err := clearOtherDefaultEntriesInZone(tx, *server.ZoneID, server.ID); err != nil {
				return err
			}
		}
		server.IsDefaultEntry = p.Value
		if err := tx.Save(&server).Error; err != nil {
			return err
		}
		action := model.ActionZoneSetDefaultEntry
		if !p.Value {
			action = model.ActionZoneClearDefaultEntry
		}
		if err := createAudit(tx, model.AuditLog{
			Operator: operatorOrSystem(p.Operator), Action: action,
			TargetType: model.TargetTypeServer, TargetRef: fmt.Sprintf("%d", server.ID),
			Result: model.ResultOK, ClientIP: p.ClientIP,
		}); err != nil {
			return err
		}
		enriched, err := enrichSingleServer(tx, server)
		if err != nil {
			return err
		}
		view = enriched
		return nil
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// clearOtherDefaultEntriesInZone 将某小区内除 excludeID 外的默认入口标记全部清零。
func clearOtherDefaultEntriesInZone(tx *gorm.DB, zoneID, excludeID uint) error {
	return tx.Model(&model.Server{}).
		Where("zone_id = ? AND id <> ? AND is_default_entry = ?", zoneID, excludeID, true).
		Update("is_default_entry", false).Error
}

// enrichSingleServer 富化单台 server 为视图。
func enrichSingleServer(db *gorm.DB, server model.Server) (*ServerView, error) {
	views, err := enrichServers(db, []model.Server{server})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

func findServerByServerID(tx *gorm.DB, serverID string) (*model.Server, error) {
	var server model.Server
	err := tx.Where("server_id = ?", serverID).First(&server).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperr.ErrInstanceNotFound
	}
	if err != nil {
		return nil, err
	}
	return &server, nil
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

func ensureServerIDAvailableForApprove(tx *gorm.DB, namespaceID uint, serverID, identityID string) error {
	var pending model.AgentIdentity
	err := tx.Where("namespace_id = ? AND server_id = ? AND identity_id <> ? AND status = ?", namespaceID, serverID, identityID, model.AgentIdentityStatusPending).First(&pending).Error
	if err == nil {
		return apperr.ErrServerIDPendingElsewhere
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

// resolveApprovedServerID 只接受审批请求显式指定的 serverId。
// pending 行遗留的 serverId 仅供界面提示，不能作为服务端回退来源。
func resolveApprovedServerID(supplied string) (string, error) {
	serverID := strings.TrimSpace(supplied)
	if serverID == "" || len(serverID) > 64 || strings.ContainsAny(serverID, " \t\r\n") {
		return "", apperr.ErrInvalidParam
	}
	return serverID, nil
}

func optionalIdentityServerID(ident *model.AgentIdentity) *string {
	if !ident.ServerID.Assigned() {
		return nil
	}
	serverID := string(ident.ServerID)
	return &serverID
}

func identityBindingSourceForRegistration(serverID string) string {
	if serverID == "" {
		return model.AgentIdentityBindingSourceAdminAssigned
	}
	return model.AgentIdentityBindingSourceLegacyLocal
}

func newAgentRegisterV2Result(ident *model.AgentIdentity, namespace string) AgentRegisterV2Result {
	boundAt, fingerprint := agentBindingSnapshot(ident, namespace)
	return AgentRegisterV2Result{
		Status: ident.Status, ExpiresAt: ident.PendingExpiresAt, Namespace: namespace,
		ServerID: optionalIdentityServerID(ident), BoundAt: boundAt, BindingFingerprint: fingerprint,
		BindingSource: ident.BindingSource, MigrationState: agentMigrationState(ident),
	}
}

// agentMigrationState 按 FR-203 的 legacy 迁移事实归纳管理面状态。
func agentMigrationState(ident *model.AgentIdentity) string {
	if ident.BindingSource != model.AgentIdentityBindingSourceLegacyLocal {
		return "not_required"
	}
	if ident.LegacyMigratedAt == nil {
		return "pending"
	}
	return "completed"
}

// agentBindingSnapshot 只从已持久化的激活绑定生成快照；事实不完整时返回空值，禁止使用本地时钟补造。
func agentBindingSnapshot(ident *model.AgentIdentity, namespace string) (*time.Time, *string) {
	if (ident.Status != model.AgentIdentityStatusActive && ident.Status != model.AgentIdentityStatusDisabled) ||
		!ident.ServerID.Assigned() || ident.BoundAt == nil {
		return nil, nil
	}
	fingerprint := agentBindingFingerprint(ident.IdentityID, namespace, string(ident.ServerID), ident.Kind, *ident.BoundAt)
	return ident.BoundAt, &fingerprint
}

// agentBindingFingerprint 使用版本前缀和换行分隔的稳定字段顺序，摘要中绝不包含 token 或密钥。
func agentBindingFingerprint(identityID, namespace, serverID, kind string, boundAt time.Time) string {
	payload := strings.Join([]string{
		"beacon-binding-v1", identityID, namespace, serverID, kind, boundAt.UTC().Format(time.RFC3339Nano),
	}, "\n")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
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
	owner := s.trustSnapshotOwner
	if owner == nil {
		owner = s
	}
	var trusts []model.NamespaceTrust
	if err := owner.db.Where("status = ?", model.NamespaceTrustStatusActive).Find(&trusts).Error; err != nil {
		return err
	}
	next := make(map[trustKey]struct{}, len(trusts))
	for _, trust := range trusts {
		next[trustKey{from: trust.FromNamespaceID, to: trust.ToNamespaceID, capability: trust.Capability}] = struct{}{}
	}
	owner.trustMu.Lock()
	owner.trustSet = next
	owner.trustMu.Unlock()
	return nil
}

func (s *V2ControlPlaneService) reloadTrustSnapshotAfterCommit() error {
	var reloadErr error
	s.scheduleAfterCommit(func() {
		reloadErr = s.reloadTrustSnapshot()
		if reloadErr != nil {
			slog.Warn("审批提交后刷新命名空间信任快照失败，进程内信任将保持原状态", "错误", redact.DesensitizeErr(reloadErr))
		}
	})
	return reloadErr
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
