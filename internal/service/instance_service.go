package service

import (
	"errors"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime"
)

// RegisterParams 是实例注册入参（capacity/weight 顶层、metadata 自定义、无 canary）。
type RegisterParams struct {
	Namespace string
	ServerID  string
	Role      string
	GroupHint string
	Address   string
	Version   string
	// AgentVersion 是 agent 自身构建版本（FR-86，见 ADR-0039）：agent 注册自报，透传写入内存注册表；旧 agent 缺键则空。
	AgentVersion string
	Capacity     int
	Weight       int
	Metadata     map[string]string
	// Backends 是 bc 上报的当前后端子服 serverId 集合（仅 bc 填，FR-36 事实）；透传写入内存注册表。
	Backends []string
	ClientIP string
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

// DefaultEntryResolver 解析某环境下被指定为「小区默认入口」的 serverId 集合（FR-48）。
// 由 ZoneService 注入（避免 InstanceService / handler 直接碰默认入口仓库，守分层单向）。
type DefaultEntryResolver func(ns string) (map[string]bool, error)

// InstanceService 编排实例注册/心跳/上报/下线/发现（操作内存注册表 + 解析归属 + 审计）。
// 主动下线拒绝态（FR-49）落 DB（offlineRepo），与内存注册/健康真源解耦：注册前查拒绝表、下线/取消下线在事务内写库。
type InstanceService struct {
	db                   *gorm.DB
	registry             *runtime.Registry
	assignRepo           *repository.ZoneAssignmentRepository
	offlineRepo          *repository.ServerOfflineRepository
	auditRepo            *repository.AuditLogRepository
	heartbeatInterval    time.Duration
	ttl                  time.Duration
	notifier             *ChangeNotifier      // 可选，注册/下线后唤醒拓扑 watch（FR-29）
	defaultEntryResolver DefaultEntryResolver // 可选，发现/实例视图标 zoneDefaultEntry（FR-48）；nil 时恒空集
}

// NewInstanceService 构造服务。
func NewInstanceService(db *gorm.DB, registry *runtime.Registry, assignRepo *repository.ZoneAssignmentRepository, offlineRepo *repository.ServerOfflineRepository, auditRepo *repository.AuditLogRepository, heartbeatInterval, ttl time.Duration) *InstanceService {
	return &InstanceService{
		db: db, registry: registry, assignRepo: assignRepo, offlineRepo: offlineRepo, auditRepo: auditRepo,
		heartbeatInterval: heartbeatInterval, ttl: ttl,
	}
}

// SetNotifier 注入拓扑唤醒器（启动时装配；未注入则不唤醒拓扑 watch）。
func (s *InstanceService) SetNotifier(n *ChangeNotifier) {
	s.notifier = n
}

// SetDefaultEntryResolver 注入小区默认入口解析器（启动时装配；未注入则默认入口标志恒为 false，FR-48）。
func (s *InstanceService) SetDefaultEntryResolver(r DefaultEntryResolver) {
	s.defaultEntryResolver = r
}

// DefaultEntrySet 返回某环境下被指定为小区默认入口的 serverId 集合（FR-48）。
// 供 handler 渲染实例/发现视图标 zoneDefaultEntry；未注入解析器或解析出错时返回空集（不阻断发现）。
func (s *InstanceService) DefaultEntrySet(ns string) map[string]bool {
	if s.defaultEntryResolver == nil {
		return map[string]bool{}
	}
	set, err := s.defaultEntryResolver(ns)
	if err != nil {
		slog.Warn("解析小区默认入口集合失败，本次发现不标默认入口", "namespace", ns, "错误", err)
		return map[string]bool{}
	}
	return set
}

// notifyTopology 唤醒该 namespace 的拓扑 watch（注入了才唤醒）。
func (s *InstanceService) notifyTopology(ns string) {
	if s.notifier != nil {
		s.notifier.NotifyTopologyChange(ns)
	}
}

// Register 注册实例：按 zone_assignment 解析回填 (group, zone)，写内存注册表，记审计。
func (s *InstanceService) Register(p RegisterParams) (*RegisterResult, error) {
	if p.Namespace == "" || p.ServerID == "" {
		return nil, apperr.ErrIdentityRequired
	}
	// 主动下线拒绝态（FR-49）：注册前查拒绝表，命中则拒绝接入（专门错误码，区别于自然 lost/offline 与重复 serverId）。
	// 仅在低频的注册路径查库（心跳热路径不查），下线收敛靠"移出内存→心跳 404→重注册被拒"。
	off, err := s.offlineRepo.FindByServer(p.Namespace, p.ServerID)
	if err != nil {
		return nil, err
	}
	if off != nil {
		slog.Warn("实例已被主动下线，拒绝注册接入", "namespace", p.Namespace, "serverId", p.ServerID, "address", p.Address)
		s.audit(p.Namespace, model.ActionInstanceRegister, p.ServerID, "agent", model.ResultFail, p.ClientIP)
		return nil, apperr.ErrInstanceOfflineRejected
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
		Address: p.Address, Version: p.Version, AgentVersion: p.AgentVersion,
		Capacity: p.Capacity, Weight: p.Weight, Metadata: p.Metadata,
		Backends: p.Backends,
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
	// 实例进入/刷新可用集合 → 唤醒拓扑 watch（同址重连摘要不变，由 StreamService 去重不推）。
	s.notifyTopology(p.Namespace)
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

// ReportParams 是状态上报入参（人数 / TPS / 内存 / CPU 仅展示；Backends 为 bc 后端归属事实，FR-36）。
type ReportParams struct {
	Namespace   string
	ServerID    string
	AppliedMD5  string
	PlayerCount int
	TPS         float64
	MemUsed     int64
	MemMax      int64
	CPULoad     float64
	// Backends 用指针区分「缺键」与「显式空集」：nil=旧 agent/bukkit 未报（保留原集合不动）；
	// 非空指针=bc 显式上报（含空集即清空）。仅 bc 填，向后兼容。
	Backends *[]string
	// Proxy 是 bc 专属负载指标（FR-34）：nil=bukkit/旧 agent 缺键（不刷新）；非 nil=bc 上报（刷新）。
	Proxy *runtime.ProxyMetrics
}

// Report 写入 agent 上报指标（人数 / TPS / 内存 / CPU，仅展示）；bc 附报的后端集合随上报刷新（FR-36）。
// 未注册返回 NOT_REGISTERED。
func (s *InstanceService) Report(p ReportParams) error {
	if !s.registry.Report(p.Namespace, p.ServerID, p.AppliedMD5, p.PlayerCount, p.TPS, p.MemUsed, p.MemMax, p.CPULoad, p.Proxy) {
		return apperr.ErrNotRegistered
	}
	// 仅当 bc 显式上报 backends（指针非空）时刷新；旧 agent/bukkit 缺键不动原集合。
	if p.Backends != nil {
		s.registry.SetBackends(p.Namespace, p.ServerID, *p.Backends)
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

// Offline 主动下线（FR-49）：事务内落 DB 拒绝态 + 审计 instance.offline，提交后移出内存可用集并唤醒拓扑 watch。
// 持久态使控制面重启仍生效、agent 重注册被拒。允许对不在内存的实例预先下线（移除内存仅是"是否在册"的副作用，不作前置条件）。
func (s *InstanceService) Offline(ns, serverID, reason, operator, clientIP string) error {
	if ns == "" || serverID == "" {
		return apperr.ErrInvalidParam
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if _, e := s.offlineRepo.WithTx(tx).Upsert(ns, serverID, reason); e != nil {
			return e
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: ns, Operator: operator, Action: model.ActionInstanceOffline,
			TargetType: model.TargetTypeInstance, TargetRef: ns + "/" + serverID, Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return err
	}
	// 事务提交成功后：移出内存可用集（其下一跳心跳将 404 → 重注册被拒）→ 唤醒拓扑 watch。
	s.registry.Offline(ns, serverID)
	s.notifyTopology(ns)
	slog.Info("主动下线实例", "namespace", ns, "serverId", serverID, "operator", operator)
	return nil
}

// Online 取消主动下线（FR-49）：事务内软删拒绝态 + 审计 instance.online；不存在拒绝态返回 OFFLINE_NOT_FOUND。
// 清除后不主动复活实例（等 agent 降频探测重连或运维 reconnect）。
func (s *InstanceService) Online(ns, serverID, operator, clientIP string) error {
	if ns == "" || serverID == "" {
		return apperr.ErrInvalidParam
	}
	now := time.Now().UTC()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		deleted, e := s.offlineRepo.WithTx(tx).SoftDelete(ns, serverID, now)
		if e != nil {
			return e
		}
		if !deleted {
			return apperr.ErrOfflineNotFound
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: ns, Operator: operator, Action: model.ActionInstanceOnline,
			TargetType: model.TargetTypeInstance, TargetRef: ns + "/" + serverID, Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return err
	}
	slog.Info("取消主动下线", "namespace", ns, "serverId", serverID, "operator", operator)
	return nil
}

// ListOffline 列出某环境内当前主动下线标记（FR-49）。
func (s *InstanceService) ListOffline(ns string) ([]model.ServerOffline, error) {
	return s.offlineRepo.ListActive(ns)
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
