package service

import (
	"context"
	"time"

	"beacon/internal/filetree"
	"beacon/internal/model"
	"beacon/internal/repository"
	"beacon/internal/runtime/longpoll"
)

// EffectiveOverride 是某 agent 身份适用的覆盖集集合（按覆盖链解析后 + 独立 overrideMd5）。
// 成员内容不在此返回（agent 经 files/content 取，复用通道B 内容通道）；这里只投递
// "目标根 + 受限重载命令 + 成员 path 清单" 这一事实（见 ADR-0011）。
type EffectiveOverride struct {
	Namespace string
	ServerID  string
	Group     string
	Zone      string
	// 适用覆盖集整体指纹（与配置 md5、fileTreeMd5 相互独立）
	OverrideMD5 string
	Sets        []filetree.EffectiveOverrideSet
}

// OverrideEffectiveService 按 agent 身份解析适用覆盖集（FR-15 投递）+ override 长轮询挂起。
// 复用文件长轮询的 Hub（override 投递与文件树同属通道B，唤醒集合共用 fileHub）。
type OverrideEffectiveService struct {
	setRepo    *repository.FileOverrideSetRepository
	fileRepo   *repository.FileObjectRepository
	assignRepo *repository.ZoneAssignmentRepository
	hub        *longpoll.Hub
}

// NewOverrideEffectiveService 构造服务。hub 仅长轮询用，纯解析场景可传 nil。
func NewOverrideEffectiveService(
	setRepo *repository.FileOverrideSetRepository,
	fileRepo *repository.FileObjectRepository,
	assignRepo *repository.ZoneAssignmentRepository,
	hub *longpoll.Hub,
) *OverrideEffectiveService {
	return &OverrideEffectiveService{setRepo: setRepo, fileRepo: fileRepo, assignRepo: assignRepo, hub: hub}
}

// Resolve 解析某 (namespace, serverId) 适用的覆盖集：
// 先按 zone_assignment 得 (group, zone)，未分配则 group=groupHint、zone 为空；再拉四层候选整集覆盖。
func (s *OverrideEffectiveService) Resolve(ns, serverID, groupHint string) (EffectiveOverride, error) {
	group, zone := groupHint, ""
	assign, err := s.assignRepo.FindByServer(ns, serverID)
	if err != nil {
		return EffectiveOverride{}, err
	}
	if assign != nil {
		group, zone = assign.GroupCode, assign.ZoneCode
	}
	candidates, err := s.setRepo.FindEffectiveSets(ns, group, zone, serverID)
	if err != nil {
		return EffectiveOverride{}, err
	}
	// 成员 path 清单由 repo 注入：仅取该集 enabled 成员的 path（已早校验在发布期把关）。
	sets := filetree.ResolveOverrideSets(candidates, func(setID uint) []string {
		members, e := s.fileRepo.ListByOverrideSet(setID)
		if e != nil {
			return nil // 取成员失败时返回空清单（该集本轮无成员，agent 不落盘；下轮重试）
		}
		paths := make([]string, 0, len(members))
		for _, m := range members {
			if m.Enabled {
				paths = append(paths, m.Path)
			}
		}
		return paths
	})
	return EffectiveOverride{
		Namespace: ns, ServerID: serverID, Group: group, Zone: zone,
		OverrideMD5: filetree.OverrideMD5(sets),
		Sets:        sets,
	}, nil
}

// MemberContent 取某 server 适用覆盖集（按 setName 定位）下某成员文件的整文件内容（agent 落 targetRoot 用）。
// 先按覆盖链解析出该 setName 在本 server 的归属集（取层级最高那份，权威由控制面定），再取成员内容。
// agent 只用 setName + 相对 path，不接触内部 setID；不存在返回 (nil, nil)。
func (s *OverrideEffectiveService) MemberContent(ns, serverID, groupHint, setName, path string) (*filetree.EffectiveFile, error) {
	group, zone := groupHint, ""
	assign, err := s.assignRepo.FindByServer(ns, serverID)
	if err != nil {
		return nil, err
	}
	if assign != nil {
		group, zone = assign.GroupCode, assign.ZoneCode
	}
	candidates, err := s.setRepo.FindEffectiveSets(ns, group, zone, serverID)
	if err != nil {
		return nil, err
	}
	// 按覆盖链取该 name 的归属集（层级最高那份），杜绝越权读到不适用本 server 的集成员。
	winnerID, ok := highestSetID(candidates, setName)
	if !ok {
		return nil, nil
	}
	obj, err := s.fileRepo.FindOverrideMember(winnerID, path)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, nil
	}
	return &filetree.EffectiveFile{Path: obj.Path, MD5: obj.ContentMD5, Content: obj.Content}, nil
}

// highestSetID 在候选集里取某 name 覆盖链上层级最高那份的 ID（与 ResolveOverrideSets 同口径）。
func highestSetID(candidates []model.FileOverrideSet, name string) (uint, bool) {
	var winner *model.FileOverrideSet
	for i := range candidates {
		c := &candidates[i]
		if c.Name != name || !c.Enabled {
			continue
		}
		if winner == nil || scopeRank(c.ScopeLevel) >= scopeRank(winner.ScopeLevel) {
			winner = c
		}
	}
	if winner == nil {
		return 0, false
	}
	return winner.ID, true
}

// scopeRank 覆盖层优先级（低→高），未知层 -1（与 filetree 内部 scopePriority 同序，此处复用避免跨包暴露）。
func scopeRank(level string) int {
	switch level {
	case model.ScopeGlobal:
		return 0
	case model.ScopeGroup:
		return 1
	case model.ScopeZone:
		return 2
	case model.ScopeServer:
		return 3
	default:
		return -1
	}
}

// WaitOverride override 长轮询：先注册 waiter 再算 overrideMd5（消除注册前发布丢唤醒窗口）。
// md5 与 agentMD5 不同 → 立即返回 (eff, true)；相同 → 挂起，被唤醒后重算比对；超时/断连返回 (_, false)。
func (s *OverrideEffectiveService) WaitOverride(ctx context.Context, ns, serverID, groupHint, agentMD5 string, timeout time.Duration) (EffectiveOverride, bool, error) {
	w := s.hub.Register(ns, serverID)
	defer s.hub.Deregister(w)
	deadline := time.Now().Add(timeout)
	for {
		eff, err := s.Resolve(ns, serverID, groupHint)
		if err != nil {
			return EffectiveOverride{}, false, err
		}
		if eff.OverrideMD5 != agentMD5 {
			return eff, true, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return EffectiveOverride{}, false, nil
		}
		if !w.Wait(ctx, remaining) {
			return EffectiveOverride{}, false, nil // 超时或客户端断连
		}
		// 被唤醒 → 循环重跑 Resolve 比对（唤醒即重算）
	}
}
