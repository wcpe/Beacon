package service

import (
	"encoding/json"
	"log/slog"
	"sort"
	"time"

	"gorm.io/gorm"

	"beacon/internal/apperr"
	"beacon/internal/model"
	"beacon/internal/repository"
	"beacon/internal/runtime"
)

// PlacementCandidate 是落位候选（控制面只给基于权威事实的建议，玩家连接由数据面执行，见 ADR-0017）。
// 仅携带 serverId/address/weight/capacity/drain 等事实，供数据面据此落位。
type PlacementCandidate struct {
	ServerID string
	Address  string
	Weight   int
	Capacity int
	Drained  bool // 恒为 false（已 drain 的不进候选），保留字段供视图明示
}

// RankPlacement 是落位排序纯函数（无副作用、可穷举单测）：
// 仅纳入 status=online 且未 drain 的实例，按 weight 降序 → capacity 降序 → serverId 升序确定性排序。
// 不读 playerCount/tps（二者仅展示、不参与决策，见 ADR-0017）。
func RankPlacement(insts []*runtime.Instance, drained map[string]bool) []PlacementCandidate {
	cands := make([]PlacementCandidate, 0, len(insts))
	for _, in := range insts {
		if in.Status != runtime.StatusOnline {
			continue
		}
		if drained[in.ServerID] {
			continue
		}
		cands = append(cands, PlacementCandidate{
			ServerID: in.ServerID, Address: in.Address,
			Weight: in.Weight, Capacity: in.Capacity, Drained: false,
		})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Weight != cands[j].Weight {
			return cands[i].Weight > cands[j].Weight
		}
		if cands[i].Capacity != cands[j].Capacity {
			return cands[i].Capacity > cands[j].Capacity
		}
		return cands[i].ServerID < cands[j].ServerID
	})
	return cands
}

// SchedulingService 编排流量调度（FR-10）：drain 标记落 DB + 落位建议（query-only）。
// 控制面只给决策，不执行玩家连接（架构红线，见 ADR-0017）。
type SchedulingService struct {
	db        *gorm.DB
	drainRepo *repository.ServerDrainRepository
	auditRepo *repository.AuditLogRepository
	registry  *runtime.Registry
}

// NewSchedulingService 构造服务。
func NewSchedulingService(db *gorm.DB, drainRepo *repository.ServerDrainRepository, auditRepo *repository.AuditLogRepository, registry *runtime.Registry) *SchedulingService {
	return &SchedulingService{db: db, drainRepo: drainRepo, auditRepo: auditRepo, registry: registry}
}

// Placement 给出某 zone 内落位候选（按推荐优先级排序）。
// 读内存注册表（在线实例）+ DB drain 集合（按 ns 一次性取，无 N+1），用纯函数排序剔除。
// zone 内无可用候选（空集 / 全 drain / 全离线）→ 返回空切片（不报错），由数据面兜底。
func (s *SchedulingService) Placement(ns, group, zone string) ([]PlacementCandidate, error) {
	if ns == "" || zone == "" {
		return nil, apperr.ErrInvalidParam
	}
	insts := s.registry.List(runtime.Filter{
		Namespace: ns, Group: group, Zone: zone, Status: runtime.StatusOnline,
	})
	drains, err := s.drainRepo.ListActive(ns)
	if err != nil {
		return nil, err
	}
	drained := make(map[string]bool, len(drains))
	for _, d := range drains {
		drained[d.ServerID] = true
	}
	return RankPlacement(insts, drained), nil
}

// Drain 标记某 serverId 为 drain（排空 / 维护）：事务内写 server_drain + 审计原子完成。
func (s *SchedulingService) Drain(ns, serverID, reason, operator, clientIP string) (*model.ServerDrain, error) {
	if ns == "" || serverID == "" || operator == "" {
		return nil, apperr.ErrInvalidParam
	}
	// 审计详情按 json 文本约定写（与 zone 改派一致），reason 经 marshal 转义
	detail, _ := json.Marshal(map[string]string{"reason": reason})
	var d *model.ServerDrain
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var e error
		d, e = s.drainRepo.WithTx(tx).Upsert(ns, serverID, reason)
		if e != nil {
			return e
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: ns, Operator: operator, Action: model.ActionSchedulingDrain,
			TargetType: model.TargetTypeInstance, TargetRef: ns + "/" + serverID,
			Detail: string(detail), Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return nil, err
	}
	slog.Info("标记 drain", "namespace", ns, "serverId", serverID, "operator", operator)
	return d, nil
}

// Undrain 取消某 serverId 的 drain（软删）；不存在返回 DRAIN_NOT_FOUND。
func (s *SchedulingService) Undrain(ns, serverID, operator, clientIP string) error {
	if ns == "" || serverID == "" || operator == "" {
		return apperr.ErrInvalidParam
	}
	now := time.Now().UTC()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		deleted, e := s.drainRepo.WithTx(tx).SoftDelete(ns, serverID, now)
		if e != nil {
			return e
		}
		if !deleted {
			return apperr.ErrDrainNotFound
		}
		return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
			NamespaceCode: ns, Operator: operator, Action: model.ActionSchedulingUndrain,
			TargetType: model.TargetTypeInstance, TargetRef: ns + "/" + serverID,
			Result: model.ResultOK, ClientIP: clientIP,
		})
	})
	if err != nil {
		return err
	}
	slog.Info("取消 drain", "namespace", ns, "serverId", serverID, "operator", operator)
	return nil
}

// ListDrains 列出某环境内当前 drain 标记。
func (s *SchedulingService) ListDrains(ns string) ([]model.ServerDrain, error) {
	return s.drainRepo.ListActive(ns)
}
