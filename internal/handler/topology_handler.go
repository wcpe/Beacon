package handler

import (
	"net/http"

	"beacon/internal/apperr"
	"beacon/internal/render"
	"beacon/internal/service"
)

// TopologyHandler 处理集群拓扑 admin 请求（FR-37）：读内存注册表快照，
// 返回 bc→bukkit 真实拓扑（节点 + 边 + 大区/zone 分组）。handler 不碰 runtime 内存结构细节，经 service 暴露领域结构。
type TopologyHandler struct {
	svc *service.TopologyService
}

// NewTopologyHandler 构造处理器。
func NewTopologyHandler(svc *service.TopologyService) *TopologyHandler {
	return &TopologyHandler{svc: svc}
}

// topologyNodeView 是拓扑节点对外视图（zone 未分配时为 null）。
type topologyNodeView struct {
	ServerID string  `json:"serverId"`
	Role     string  `json:"role"`
	Group    string  `json:"group"`
	Zone     *string `json:"zone"`
	Status   string  `json:"status"`
	Address  string  `json:"address"`
}

// topologyEdgeView 是 bc→bukkit 连线对外视图。
type topologyEdgeView struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// topologyGroupView 是大区/zone 分组对外视图（zone 未分配时为 null）。
type topologyGroupView struct {
	Group   string   `json:"group"`
	Zone    *string  `json:"zone"`
	Members []string `json:"members"`
}

// topologyView 是拓扑对外视图。
type topologyView struct {
	Namespace string              `json:"namespace"`
	Nodes     []topologyNodeView  `json:"nodes"`
	Edges     []topologyEdgeView  `json:"edges"`
	Groups    []topologyGroupView `json:"groups"`
}

// Topology 处理 GET /admin/v1/topology?namespace=：返回该 namespace 的拓扑快照。
func (h *TopologyHandler) Topology(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		render.WriteError(w, r, apperr.ErrInvalidParam)
		return
	}
	render.WriteJSON(w, http.StatusOK, toTopologyView(h.svc.Build(ns)))
}

// toTopologyView 把领域拓扑映射为对外视图。
func toTopologyView(t service.Topology) topologyView {
	nodes := make([]topologyNodeView, 0, len(t.Nodes))
	for _, n := range t.Nodes {
		nodes = append(nodes, topologyNodeView{
			ServerID: n.ServerID, Role: n.Role, Group: n.Group, Zone: nilIfEmpty(n.Zone),
			Status: n.Status, Address: n.Address,
		})
	}
	edges := make([]topologyEdgeView, 0, len(t.Edges))
	for _, e := range t.Edges {
		edges = append(edges, topologyEdgeView{Source: e.Source, Target: e.Target})
	}
	groups := make([]topologyGroupView, 0, len(t.Groups))
	for _, g := range t.Groups {
		groups = append(groups, topologyGroupView{Group: g.Group, Zone: nilIfEmpty(g.Zone), Members: g.Members})
	}
	return topologyView{Namespace: t.Namespace, Nodes: nodes, Edges: edges, Groups: groups}
}
