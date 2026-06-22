package service

import (
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/gitexport"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime"
)

// ZoneStat 是 zone 维度汇总（每 zone 的服数与在线数）。
type ZoneStat struct {
	Group       string
	Zone        string
	ServerCount int
	OnlineCount int
}

// ZoneService 编排 zone 指派 CRUD 与汇总。
// 改派后刷新内存实例归属；有效配置解析读 DB 指派，已即时反映（推送唤醒属 M3）。
type ZoneService struct {
	db               *gorm.DB
	assignRepo       *repository.ZoneAssignmentRepository
	defaultEntryRepo *repository.ZoneDefaultEntryRepository // 小区默认入口（FR-48）；nil 时默认入口能力不可用
	auditRepo        *repository.AuditLogRepository
	registry         *runtime.Registry
	notifier         *ChangeNotifier // 可选，改派/取消后唤醒该 serverId 的长轮询
	exporter         GitExporter     // 可选，改派/取消后触发 git 单向导出（FR-47，best-effort 非阻塞）
}

// NewZoneService 构造服务。
func NewZoneService(db *gorm.DB, assignRepo *repository.ZoneAssignmentRepository, defaultEntryRepo *repository.ZoneDefaultEntryRepository, auditRepo *repository.AuditLogRepository, registry *runtime.Registry) *ZoneService {
	return &ZoneService{db: db, assignRepo: assignRepo, defaultEntryRepo: defaultEntryRepo, auditRepo: auditRepo, registry: registry}
}

// SetNotifier 注入长轮询唤醒器（启动时装配；未注入则不唤醒）。
func (s *ZoneService) SetNotifier(n *ChangeNotifier) {
	s.notifier = n
}

// notifyServer 唤醒单个 serverId 的长轮询，并唤醒该 namespace 的拓扑 watch（改派改变拓扑 zone 归属，FR-29）。
func (s *ZoneService) notifyServer(ns, serverID string) {
	if s.notifier != nil {
		s.notifier.NotifyServer(ns, serverID)
		s.notifier.NotifyTopologyChange(ns)
	}
}

// notifyTopology 仅唤醒该 namespace 的拓扑 watch（默认入口变更不改某具体 serverId 的有效配置，故不唤醒长轮询，FR-48）。
func (s *ZoneService) notifyTopology(ns string) {
	if s.notifier != nil {
		s.notifier.NotifyTopologyChange(ns)
	}
}

// SetGitExporter 注入 git 导出触发器（启动时装配；未注入则不导出，FR-47）。
func (s *ZoneService) SetGitExporter(e GitExporter) {
	s.exporter = e
}

// exportGit 在改派/取消事务提交后触发 git 单向导出（best-effort 非阻塞，FR-47）。
// 改派只改 zone_assignment（不导出该表），源层内容不变、commit 通常为空 diff；
// 仍触发以与「提交后导出」时机一致，空 diff 由 git 实现自然跳过、无副作用。
func (s *ZoneService) exportGit(ns, serverID, action, operator string) {
	if s.exporter == nil {
		return
	}
	s.exporter.ExportAsync(gitexport.ExportMeta{
		Operator: operator,
		Action:   action,
		Target:   ns + "/" + serverID,
	})
}

// Assign 新增或改派 serverId→(group, zone)，事务内 upsert + 审计原子完成。
func (s *ZoneService) Assign(ns, serverID, group, zone, operator, note, clientIP string) (*model.ZoneAssignment, error) {
	if ns == "" || serverID == "" || group == "" || zone == "" || operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	// 纵深防御：zone 仅供 bukkit 子服归派，BC 代理（role=bungee）不该有 zone（FR-8/FR-35）。
	// 仅当实例在注册表且确为 bungee 时拒绝；离线实例无角色事实可凭，沿用既有指派逻辑。
	if inst := s.registry.Get(ns, serverID); inst != nil && inst.Role == roleBungee {
		return nil, apperr.ErrZoneNotAssignableToBC
	}
	prev, err := s.assignRepo.FindByServer(ns, serverID)
	if err != nil {
		return nil, err
	}
	action := model.ActionZoneAssign
	if prev != nil {
		action = model.ActionZoneMove
	}

	var a *model.ZoneAssignment
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var e error
		a, e = s.assignRepo.WithTx(tx).Upsert(ns, serverID, group, zone, note)
		if e != nil {
			return e
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: ns, Operator: operator, Action: action,
			TargetType: model.TargetTypeZone, TargetRef: ns + "/" + serverID,
			Detail: fmt.Sprintf(`{"group":"%s","zone":"%s"}`, group, zone), Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return nil, err
	}
	s.registry.UpdateAssignment(ns, serverID, group, zone)
	s.notifyServer(ns, serverID)
	s.exportGit(ns, serverID, action, operator)
	slog.Info("zone 指派", "namespace", ns, "serverId", serverID, "group", group, "zone", zone, "action", action)
	return a, nil
}

// Unassign 取消指派（软删）；不存在返回 ASSIGNMENT_NOT_FOUND。
func (s *ZoneService) Unassign(ns, serverID, operator, clientIP string) error {
	if ns == "" || serverID == "" || operator == "" {
		return apperr.ErrInvalidParam
	}
	now := time.Now().UTC()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		deleted, e := s.assignRepo.WithTx(tx).SoftDelete(ns, serverID, now)
		if e != nil {
			return e
		}
		if !deleted {
			return apperr.ErrAssignmentNotFound
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: ns, Operator: operator, Action: model.ActionZoneUnassign,
			TargetType: model.TargetTypeZone, TargetRef: ns + "/" + serverID, Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return err
	}
	s.registry.ClearAssignment(ns, serverID)
	s.notifyServer(ns, serverID)
	s.exportGit(ns, serverID, model.ActionZoneUnassign, operator)
	slog.Info("取消 zone 指派", "namespace", ns, "serverId", serverID, "operator", operator)
	return nil
}

// ListAssignments 列出指派。
func (s *ZoneService) ListAssignments(ns, group, zone string) ([]model.ZoneAssignment, error) {
	return s.assignRepo.List(ns, group, zone)
}

// Summary 汇总每个 (group, zone) 的服数（DB 指派）与在线数（内存注册表）。
func (s *ZoneService) Summary(ns, group string) ([]ZoneStat, error) {
	assigns, err := s.assignRepo.List(ns, group, "")
	if err != nil {
		return nil, err
	}
	type key struct{ g, z string }
	counts := map[key]int{}
	var order []key
	for _, a := range assigns {
		k := key{a.GroupCode, a.ZoneCode}
		if _, ok := counts[k]; !ok {
			order = append(order, k)
		}
		counts[k]++
	}
	online := map[key]int{}
	for _, inst := range s.registry.List(runtime.Filter{Namespace: ns, Group: group, Status: runtime.StatusOnline}) {
		online[key{inst.ResolvedGroup, inst.ResolvedZone}]++
	}
	stats := make([]ZoneStat, 0, len(order))
	for _, k := range order {
		stats = append(stats, ZoneStat{Group: k.g, Zone: k.z, ServerCount: counts[k], OnlineCount: online[k]})
	}
	return stats, nil
}

// SetDefaultEntry 设置某小区 (group, zone) 的默认入口 serverId（FR-48）。
// 校验：serverId 必须是当前已指派到该 (group, zone) 的子服（应用层校验，查 zone_assignment）；
// 事务内 upsert 默认入口 + 审计原子完成，提交后唤醒拓扑 watch（默认入口变化影响 BC 设默认服）。
func (s *ZoneService) SetDefaultEntry(ns, group, zone, serverID, operator, clientIP string) (*model.ZoneDefaultEntry, error) {
	if ns == "" || group == "" || zone == "" || serverID == "" || operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	// 校验 serverId 已指派到该 (group, zone)：默认入口必须指向真正属于该小区的子服。
	assign, err := s.assignRepo.FindByServer(ns, serverID)
	if err != nil {
		return nil, err
	}
	if assign == nil || assign.GroupCode != group || assign.ZoneCode != zone {
		return nil, apperr.ErrDefaultEntryServerNotInZone
	}

	var e *model.ZoneDefaultEntry
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var te error
		e, te = s.defaultEntryRepo.WithTx(tx).Upsert(ns, group, zone, serverID)
		if te != nil {
			return te
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: ns, Operator: operator, Action: model.ActionZoneSetDefaultEntry,
			TargetType: model.TargetTypeZone, TargetRef: ns + "/" + group + "/" + zone,
			Detail: fmt.Sprintf(`{"defaultServerId":"%s"}`, serverID), Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return nil, err
	}
	s.notifyTopology(ns)
	slog.Info("设置小区默认入口", "namespace", ns, "group", group, "zone", zone, "defaultServerId", serverID, "operator", operator)
	return e, nil
}

// ClearDefaultEntry 清除某小区 (group, zone) 的默认入口（FR-48）；不存在返回 DEFAULT_ENTRY_NOT_FOUND。
func (s *ZoneService) ClearDefaultEntry(ns, group, zone, operator, clientIP string) error {
	if ns == "" || group == "" || zone == "" || operator == "" {
		return apperr.ErrInvalidParam
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		deleted, te := s.defaultEntryRepo.WithTx(tx).Delete(ns, group, zone)
		if te != nil {
			return te
		}
		if !deleted {
			return apperr.ErrDefaultEntryNotFound
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: ns, Operator: operator, Action: model.ActionZoneClearDefaultEntry,
			TargetType: model.TargetTypeZone, TargetRef: ns + "/" + group + "/" + zone, Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return err
	}
	s.notifyTopology(ns)
	slog.Info("清除小区默认入口", "namespace", ns, "group", group, "zone", zone, "operator", operator)
	return nil
}

// ListDefaultEntries 列出某环境（可选按 group 过滤）的小区默认入口（FR-48）。
func (s *ZoneService) ListDefaultEntries(ns, group string) ([]model.ZoneDefaultEntry, error) {
	return s.defaultEntryRepo.List(ns, group)
}

// DefaultEntryServerIDs 解析某环境下被指定为「该小区默认入口」的 serverId 集合（FR-48）。
// 供发现/实例视图渲染时给命中的 bukkit 实例标 zoneDefaultEntry；返回的 map 仅含被指定为默认入口的 serverId。
// defaultEntryRepo 未装配（nil）时返回空集（默认入口能力关闭，向后兼容）。
func (s *ZoneService) DefaultEntryServerIDs(ns string) (map[string]bool, error) {
	if s.defaultEntryRepo == nil {
		return map[string]bool{}, nil
	}
	list, err := s.defaultEntryRepo.List(ns, "")
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(list))
	for _, e := range list {
		out[e.DefaultServerID] = true
	}
	return out, nil
}
