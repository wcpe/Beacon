package service

import (
	"errors"
	"log/slog"
	"time"

	"beacon/internal/apperr"
	"beacon/internal/model"
	"beacon/internal/repository"
	"beacon/internal/runtime"
)

// RegisterParams 是实例注册入参（capacity/weight 顶层、metadata 自定义、无 canary）。
type RegisterParams struct {
	Namespace string
	ServerID  string
	Role      string
	GroupHint string
	Address   string
	Version   string
	Capacity  int
	Weight    int
	Metadata  map[string]string
	ClientIP  string
}

// RegisterResult 是注册结果（含解析回填的归属与下发的心跳参数）。
type RegisterResult struct {
	InstanceKey          string
	ResolvedGroup        string
	ResolvedZone         string
	Assigned             bool
	HeartbeatIntervalSec int
	TTLSec               int
}

// InstanceService 编排实例注册/心跳/上报/下线/发现（操作内存注册表 + 解析归属 + 审计）。
type InstanceService struct {
	registry          *runtime.Registry
	assignRepo        *repository.ZoneAssignmentRepository
	auditRepo         *repository.AuditLogRepository
	heartbeatInterval time.Duration
	ttl               time.Duration
}

// NewInstanceService 构造服务。
func NewInstanceService(registry *runtime.Registry, assignRepo *repository.ZoneAssignmentRepository, auditRepo *repository.AuditLogRepository, heartbeatInterval, ttl time.Duration) *InstanceService {
	return &InstanceService{
		registry: registry, assignRepo: assignRepo, auditRepo: auditRepo,
		heartbeatInterval: heartbeatInterval, ttl: ttl,
	}
}

// Register 注册实例：按 zone_assignment 解析回填 (group, zone)，写内存注册表，记审计。
func (s *InstanceService) Register(p RegisterParams) (*RegisterResult, error) {
	if p.Namespace == "" || p.ServerID == "" {
		return nil, apperr.ErrIdentityRequired
	}
	group, zone, assigned := p.GroupHint, "", false
	assign, err := s.assignRepo.FindByServer(p.Namespace, p.ServerID)
	if err != nil {
		return nil, err
	}
	if assign != nil {
		group, zone, assigned = assign.GroupCode, assign.ZoneCode, true
	}

	inst := &runtime.Instance{
		Namespace: p.Namespace, ServerID: p.ServerID, Role: p.Role, GroupHint: p.GroupHint,
		ResolvedGroup: group, ResolvedZone: zone, Assigned: assigned,
		Address: p.Address, Version: p.Version, Capacity: p.Capacity, Weight: p.Weight, Metadata: p.Metadata,
	}
	saved, err := s.registry.Register(inst, s.ttl, time.Now().UTC())
	if err != nil {
		if errors.Is(err, runtime.ErrDuplicateServerID) {
			slog.Warn("重复 serverId 注册被拒", "namespace", p.Namespace, "serverId", p.ServerID, "address", p.Address)
			s.audit(p.Namespace, model.ActionInstanceRegister, p.ServerID, "agent", model.ResultFail, p.ClientIP)
			return nil, apperr.ErrDuplicateServerID
		}
		return nil, err
	}
	s.audit(p.Namespace, model.ActionInstanceRegister, p.ServerID, "agent", model.ResultOK, p.ClientIP)
	slog.Info("实例注册", "namespace", p.Namespace, "serverId", p.ServerID,
		"group", saved.ResolvedGroup, "zone", saved.ResolvedZone, "assigned", assigned)
	return &RegisterResult{
		InstanceKey:   p.Namespace + "/" + p.ServerID,
		ResolvedGroup: saved.ResolvedGroup, ResolvedZone: saved.ResolvedZone, Assigned: assigned,
		HeartbeatIntervalSec: int(s.heartbeatInterval.Seconds()), TTLSec: int(s.ttl.Seconds()),
	}, nil
}

// Heartbeat 刷新心跳；未注册返回 NOT_REGISTERED。返回 ttlSec。
func (s *InstanceService) Heartbeat(ns, serverID string) (int, error) {
	if !s.registry.Heartbeat(ns, serverID, time.Now().UTC()) {
		return 0, apperr.ErrNotRegistered
	}
	return int(s.ttl.Seconds()), nil
}

// Report 写入 agent 上报指标；未注册返回 NOT_REGISTERED。
func (s *InstanceService) Report(ns, serverID, appliedMD5 string, playerCount int, tps float64) error {
	if !s.registry.Report(ns, serverID, appliedMD5, playerCount, tps) {
		return apperr.ErrNotRegistered
	}
	return nil
}

// List 按标签过滤列出实例。
func (s *InstanceService) List(f runtime.Filter) []*runtime.Instance {
	return s.registry.List(f)
}

// Get 取单实例；不存在返回 INSTANCE_NOT_FOUND。
func (s *InstanceService) Get(ns, serverID string) (*runtime.Instance, error) {
	inst := s.registry.Get(ns, serverID)
	if inst == nil {
		return nil, apperr.ErrInstanceNotFound
	}
	return inst, nil
}

// RequireRegistered 校验实例已注册并返回其 groupHint；未注册返回 NOT_REGISTERED。
// 供有效配置长轮询入口使用（agent 须先注册）。
func (s *InstanceService) RequireRegistered(ns, serverID string) (string, error) {
	inst := s.registry.Get(ns, serverID)
	if inst == nil {
		return "", apperr.ErrNotRegistered
	}
	return inst.GroupHint, nil
}

// Offline 手动下线（移除内存条目）；不存在返回 INSTANCE_NOT_FOUND。
func (s *InstanceService) Offline(ns, serverID, operator, clientIP string) error {
	if !s.registry.Offline(ns, serverID) {
		return apperr.ErrInstanceNotFound
	}
	s.audit(ns, model.ActionInstanceOffline, serverID, operator, model.ResultOK, clientIP)
	slog.Info("手动下线实例", "namespace", ns, "serverId", serverID, "operator", operator)
	return nil
}

// Discover 服务发现：返回可用实例（online + degraded）。degraded 为心跳陈旧但尚未失联、大概率仍在服务，
// 保留在发现结果（及由其派生的 BungeeCord 代理目录）中，直到 lost/offline 才摘除，避免亚健康实例被过早剔除。
func (s *InstanceService) Discover(f runtime.Filter) []*runtime.Instance {
	f.Status = "" // 不走单值 Status 过滤，下方按“可用”集合（online+degraded）筛
	all := s.registry.List(f)
	out := make([]*runtime.Instance, 0, len(all))
	for _, i := range all {
		if i.Status == runtime.StatusOnline || i.Status == runtime.StatusDegraded {
			out = append(out, i)
		}
	}
	return out
}

// audit 记一条实例审计（best-effort：注册的真源是内存，审计写库失败仅告警，不阻断 agent）。
func (s *InstanceService) audit(ns, action, serverID, operator, result, clientIP string) {
	entry := &model.AuditLog{
		NamespaceCode: ns, Operator: operator, Action: action,
		TargetType: model.TargetTypeInstance, TargetRef: ns + "/" + serverID, Result: result, ClientIP: clientIP,
	}
	if err := s.auditRepo.Create(entry); err != nil {
		slog.Warn("写实例审计失败", "namespace", ns, "serverId", serverID, "错误", err)
	}
}
