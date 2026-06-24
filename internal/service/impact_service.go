package service

import (
	"sort"

	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime"
)

// Impact 是某条配置 scope 的发布影响面（此刻会收到本次变更的在线子服集合，FR-79）。
type Impact struct {
	Affected []string // 受影响 serverId，按字典序去重
	Total    int      // = len(Affected)
}

// ImpactService 计算某 scope 覆盖到的在线子服集合（发布前只读预览，FR-79）。
// 归属真源 = zone_assignment（DB，ADR-0004）；在线真源 = 内存注册表可用集合（online+degraded）。
// 二者求交得「此刻真正会收到这次变更的在线子服」，纯读、不落 DB、不参与发布决策。
type ImpactService struct {
	registry   *runtime.Registry
	assignRepo *repository.ZoneAssignmentRepository
}

// NewImpactService 构造服务。
func NewImpactService(registry *runtime.Registry, assignRepo *repository.ZoneAssignmentRepository) *ImpactService {
	return &ImpactService{registry: registry, assignRepo: assignRepo}
}

// assignment 是某子服的权威归属（大区 / 小区）。
type assignment struct {
	group string
	zone  string
}

// Resolve 解析某 (namespace, scopeLevel, group, scopeTarget) 覆盖到的在线子服集合。
// 一次性拉该环境全部 zone_assignment（避免逐实例查库的 N+1），再与注册表可用集合按 scope 求交。
func (s *ImpactService) Resolve(ns, scopeLevel, group, scopeTarget string) (Impact, error) {
	assigns, err := s.assignRepo.List(ns, "", "")
	if err != nil {
		return Impact{}, err
	}
	byServer := make(map[string]assignment, len(assigns))
	for i := range assigns {
		byServer[assigns[i].ServerID] = assignment{group: assigns[i].GroupCode, zone: assigns[i].ZoneCode}
	}

	affected := make([]string, 0)
	for _, inst := range s.registry.List(runtime.Filter{Namespace: ns}) {
		// 仅可用集合（online+degraded）计入：与发现 / 拓扑 / 长轮询同口径，degraded 仍会收到变更。
		if inst.Status != runtime.StatusOnline && inst.Status != runtime.StatusDegraded {
			continue
		}
		// 归属以 DB 为权威；未指派回退 GroupHint、zone 为空（与 EffectiveService.Resolve 同口径）。
		instGroup, instZone := inst.GroupHint, ""
		if a, ok := byServer[inst.ServerID]; ok {
			instGroup, instZone = a.group, a.zone
		}
		if scopeCovers(scopeLevel, group, scopeTarget, instGroup, instZone, inst.ServerID) {
			affected = append(affected, inst.ServerID)
		}
	}
	sort.Strings(affected)
	return Impact{Affected: affected, Total: len(affected)}, nil
}

// scopeCovers 判定某 scope 是否覆盖某实例（纯函数，集中四层覆盖判定，与 FindEffectiveCandidates 覆盖链对称）。
// global：覆盖全部；group：解析大区相等；zone：大区+小区都相等；server：serverId 相等。
func scopeCovers(level, group, scopeTarget, instGroup, instZone, instServerID string) bool {
	switch level {
	case model.ScopeGlobal:
		return true
	case model.ScopeGroup:
		return instGroup == group
	case model.ScopeZone:
		return instGroup == group && instZone == scopeTarget
	case model.ScopeServer:
		return instServerID == scopeTarget
	default:
		return false
	}
}
