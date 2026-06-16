package service

import (
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"beacon/internal/apperr"
	"beacon/internal/model"
	"beacon/internal/repository"
	"beacon/internal/runtime"
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
	db         *gorm.DB
	assignRepo *repository.ZoneAssignmentRepository
	auditRepo  *repository.AuditLogRepository
	registry   *runtime.Registry
}

// NewZoneService 构造服务。
func NewZoneService(db *gorm.DB, assignRepo *repository.ZoneAssignmentRepository, auditRepo *repository.AuditLogRepository, registry *runtime.Registry) *ZoneService {
	return &ZoneService{db: db, assignRepo: assignRepo, auditRepo: auditRepo, registry: registry}
}

// Assign 新增或改派 serverId→(group, zone)，事务内 upsert + 审计原子完成。
func (s *ZoneService) Assign(ns, serverID, group, zone, operator, note string) (*model.ZoneAssignment, error) {
	if ns == "" || serverID == "" || group == "" || zone == "" || operator == "" {
		return nil, apperr.ErrInvalidParam
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
			Detail: fmt.Sprintf(`{"group":"%s","zone":"%s"}`, group, zone), Result: model.ResultOK,
		})
	})
	if err != nil {
		return nil, err
	}
	s.registry.UpdateAssignment(ns, serverID, group, zone)
	slog.Info("zone 指派", "namespace", ns, "serverId", serverID, "group", group, "zone", zone, "action", action)
	return a, nil
}

// Unassign 取消指派（软删）；不存在返回 ASSIGNMENT_NOT_FOUND。
func (s *ZoneService) Unassign(ns, serverID, operator string) error {
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
			TargetType: model.TargetTypeZone, TargetRef: ns + "/" + serverID, Result: model.ResultOK,
		})
	})
	if err != nil {
		return err
	}
	s.registry.ClearAssignment(ns, serverID)
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
