package service

import (
	"sort"

	"beacon/internal/runtime"
)

// TopologyService 组装集群拓扑事实（读内存注册表快照，不落 DB、不引重型件）。
// 拓扑来源 = 注册表「可用集合」（online+degraded，与发现 / 拓扑摘要同口径）
// + FR-36 的 bc 后端归属事实（Instance.Backends）。仅展示事实、不据它做任何调度 / 连接决策。
type TopologyService struct {
	registry *runtime.Registry
}

// NewTopologyService 构造服务。
func NewTopologyService(registry *runtime.Registry) *TopologyService {
	return &TopologyService{registry: registry}
}

// TopologyNode 是拓扑图中的一个节点（一个在线实例）。
type TopologyNode struct {
	ServerID string
	Role     string // bukkit / bungee
	Group    string
	Zone     string // 未分配为空
	Status   string // online / degraded
	Address  string
}

// TopologyEdge 是一条 bc→bukkit 连线（source/target 均为 serverId）。
type TopologyEdge struct {
	Source string // bc（bungee）serverId
	Target string // 后端 bukkit serverId
}

// TopologyGroup 是按大区 / zone 的分组信息（供前端分簇展示）。
// Group 为大区码；Zone 为小区码（未分配实例归到空 zone）；Members 是该 (group,zone) 下的 serverId 列表。
type TopologyGroup struct {
	Group   string
	Zone    string
	Members []string
}

// Topology 是某 namespace 的拓扑快照领域结果。
type Topology struct {
	Namespace string
	Nodes     []TopologyNode
	Edges     []TopologyEdge
	Groups    []TopologyGroup
}

// Build 读注册表快照组装某 namespace 的拓扑（纯组装、无副作用、无 DB IO）。
// 只纳入可用集合（online+degraded）；bc→bukkit 边只连 backends 中当前在册可用的 bukkit，
// 已离线的后端 serverId 不画悬挂边。返回结果各列表按 serverId 字典序稳定排序。
func (s *TopologyService) Build(namespace string) Topology {
	all := s.registry.List(runtime.Filter{Namespace: namespace})

	// 仅保留可用实例，并建 serverId→实例索引供边校验「目标是否在册可用」。
	avail := make([]*runtime.Instance, 0, len(all))
	index := make(map[string]*runtime.Instance, len(all))
	for _, inst := range all {
		if inst.Status == runtime.StatusOnline || inst.Status == runtime.StatusDegraded {
			avail = append(avail, inst)
			index[inst.ServerID] = inst
		}
	}

	nodes := buildNodes(avail)
	edges := buildEdges(avail, index)
	groups := buildGroups(avail)
	return Topology{Namespace: namespace, Nodes: nodes, Edges: edges, Groups: groups}
}

// buildNodes 把可用实例映射为节点，按 serverId 字典序排序。
func buildNodes(avail []*runtime.Instance) []TopologyNode {
	nodes := make([]TopologyNode, 0, len(avail))
	for _, inst := range avail {
		nodes = append(nodes, TopologyNode{
			ServerID: inst.ServerID, Role: inst.Role, Group: inst.ResolvedGroup,
			Zone: inst.ResolvedZone, Status: inst.Status, Address: inst.Address,
		})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ServerID < nodes[j].ServerID })
	return nodes
}

// buildEdges 由 bc（bungee）的 backends 事实生成 bc→bukkit 边，
// 仅连「目标实例在册可用」的后端（剔除已离线后端，避免悬挂边）。按 (source,target) 稳定排序。
func buildEdges(avail []*runtime.Instance, index map[string]*runtime.Instance) []TopologyEdge {
	edges := make([]TopologyEdge, 0)
	for _, inst := range avail {
		if inst.Role != roleBungee { // 仅 bc（bungee）有后端归属事实，bukkit 恒无边
			continue
		}
		for _, backend := range inst.Backends {
			if index[backend] == nil {
				continue // 后端不在可用集合（已离线 / 未注册）→ 不画悬挂边
			}
			edges = append(edges, TopologyEdge{Source: inst.ServerID, Target: backend})
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Source != edges[j].Source {
			return edges[i].Source < edges[j].Source
		}
		return edges[i].Target < edges[j].Target
	})
	return edges
}

// buildGroups 按 (group,zone) 聚合实例的 serverId，组与成员均按字典序稳定排序。
func buildGroups(avail []*runtime.Instance) []TopologyGroup {
	type key struct{ group, zone string }
	bucket := make(map[key][]string)
	for _, inst := range avail {
		k := key{group: inst.ResolvedGroup, zone: inst.ResolvedZone}
		bucket[k] = append(bucket[k], inst.ServerID)
	}
	groups := make([]TopologyGroup, 0, len(bucket))
	for k, members := range bucket {
		sort.Strings(members)
		groups = append(groups, TopologyGroup{Group: k.group, Zone: k.zone, Members: members})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Group != groups[j].Group {
			return groups[i].Group < groups[j].Group
		}
		return groups[i].Zone < groups[j].Zone
	})
	return groups
}
