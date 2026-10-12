package service

import (
	"reflect"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// buildViaList 是优化前的实现（用 List 深拷贝后组装），保留为对照基线。
// 仅测试使用，不进生产路径。
func buildViaList(registry *runtime.Registry, namespace string) Topology {
	all := registry.List(runtime.Filter{Namespace: namespace})
	avail := make([]TopologyNode, 0, len(all))
	index := make(map[string]bool, len(all))
	for _, inst := range all {
		if inst.Status == runtime.StatusOnline || inst.Status == runtime.StatusDegraded {
			avail = append(avail, TopologyNode{
				ServerID: inst.ServerID, Role: inst.Role, Group: inst.ResolvedGroup,
				Zone: inst.ResolvedZone, Status: inst.Status, Address: inst.Address,
			})
			index[inst.ServerID] = true
		}
	}
	nodes := append([]TopologyNode(nil), avail...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ServerID < nodes[j].ServerID })

	edges := make([]TopologyEdge, 0)
	for _, inst := range all {
		if inst.Role != roleBungee {
			continue
		}
		if _, ok := index[inst.ServerID]; !ok {
			continue // 自身不可用时其边也不画（新实现同此口径）
		}
		for _, backend := range inst.Backends {
			if !index[backend] {
				continue
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
	return Topology{Namespace: namespace, Nodes: nodes, Edges: edges}
}

// seedEquivalenceRegistry 造一个覆盖各种边界的注册表：可用 / 不可用、
// 有 / 无 Backends、Backends 指向离线 / 未注册 / 跨 namespace 的目标、多 namespace。
func seedEquivalenceRegistry(t *testing.T, reg *runtime.Registry) {
	t.Helper()
	now := time.Now().UTC()
	up := func(ns, id, role string, backends []string) {
		inst := &runtime.Instance{
			Namespace: ns, ServerID: id, Role: role,
			ResolvedGroup: "g1", ResolvedZone: "z1",
			Address: "127.0.0.1:20000",
			Metadata: map[string]string{"k": "v"},
		}
		if _, err := reg.Register(inst, time.Minute, now); err != nil {
			t.Fatalf("注册 %s/%s 失败：%v", ns, id, err)
		}
		if len(backends) > 0 {
			reg.SetBackends(ns, id, backends)
		}
	}
	up("prod", "bc-1", roleBungee, []string{"bk-1", "bk-2", "ghost", "bc-1", "other-ns-bk"})
	up("prod", "bk-1", "bukkit", nil)
	up("prod", "bk-2", "bukkit", nil)
	// 离线实例：应先 online 以便拿到 Backends，再 Offline。
	up("prod", "bc-offline", roleBungee, []string{"bk-1"})
	up("prod", "bk-offline", "bukkit", nil)
	reg.Offline("prod", "bc-offline")
	reg.Offline("prod", "bk-offline")
	up("prod", "bk-3", "bukkit", nil)
	// 另一 namespace：不应出现在 prod 结果里，也不应被当成本 namespace 的边目标。
	up("test", "other-ns-bk", "bukkit", nil)
	up("test", "bc-2", roleBungee, []string{"other-ns-bk"})
}

// TestBuildMatchesListImplementation 锁定「窄投影优化不改变输出」。
//
// 这是本次优化的核心风险：把 List（深拷贝）换成 ForEachTopology（窄投影）后，
// 字段来源、可用集合口径、边目标校验都必须逐字不变。用「旧实现 vs 新实现」直接比对
// 输出，比逐条断言更能覆盖组合边界。
func TestBuildMatchesListImplementation(t *testing.T) {
	reg := runtime.NewRegistry()
	seedEquivalenceRegistry(t, reg)
	svc := NewTopologyService(reg)

	for _, ns := range []string{"prod", "test", "nonexistent"} {
		t.Run(ns, func(t *testing.T) {
			got := svc.Build(ns)
			want := buildViaList(reg, ns)

			// 用长度 + 逐元素比对，而不是 reflect.DeepEqual：
			// 空结果时 nil slice 与空 slice 在 DeepEqual 下不等，但对外行为完全一致
			// （handler 的 toTopologyView 用 make(...,0,len) 兜底，JSON 一律输出 []）。
			// 这里要锁的是「内容与顺序不变」，不是「底层切片是否 nil」。
			if len(got.Nodes) != len(want.Nodes) {
				t.Fatalf("Nodes 数量不一致：新=%d 旧=%d", len(got.Nodes), len(want.Nodes))
			}
			for i := range want.Nodes {
				if got.Nodes[i] != want.Nodes[i] {
					t.Fatalf("Nodes[%d] 不一致：\n新=%+v\n旧=%+v", i, got.Nodes[i], want.Nodes[i])
				}
			}
			if len(got.Edges) != len(want.Edges) {
				t.Fatalf("Edges 数量不一致：新=%d 旧=%d", len(got.Edges), len(want.Edges))
			}
			for i := range want.Edges {
				if got.Edges[i] != want.Edges[i] {
					t.Fatalf("Edges[%d] 不一致：\n新=%+v\n旧=%+v", i, got.Edges[i], want.Edges[i])
				}
			}
			if got.Namespace != want.Namespace {
				t.Fatalf("Namespace 应为 %s，实际 %s", want.Namespace, got.Namespace)
			}
		})
	}
}

// TestBuildEdgeTargetsStayInNamespace 锁定跨 namespace 隔离：边只连本 namespace 在册可用目标。
func TestBuildEdgeTargetsStayInNamespace(t *testing.T) {
	reg := runtime.NewRegistry()
	seedEquivalenceRegistry(t, reg)
	topo := NewTopologyService(reg).Build("prod")

	for _, e := range topo.Edges {
		if e.Target == "other-ns-bk" {
			t.Fatal("边不应连到其它 namespace 的实例（跨环境串线）")
		}
		if e.Target == "ghost" {
			t.Fatal("边不应连到未注册的目标（悬挂边）")
		}
		if e.Target == "bk-offline" {
			t.Fatal("边不应连到已离线目标（悬挂边）")
		}
	}
	// bc-1 应只连到在册可用的 bk-1 / bk-2；自环（bc-1 → bc-1）按旧口径是允许的（同为可用实例）。
	targets := make([]string, 0, len(topo.Edges))
	for _, e := range topo.Edges {
		if e.Source == "bc-1" {
			targets = append(targets, e.Target)
		}
	}
	sort.Strings(targets)
	if !reflect.DeepEqual(targets, []string{"bc-1", "bk-1", "bk-2"}) {
		t.Fatalf("bc-1 的边目标应为 [bc-1 bk-1 bk-2]（自环按旧口径保留），实际 %v", targets)
	}
}

// TestBuildDoesNotExposeRegistryInternals 锁定窄投影没有把注册表内部引用带出去。
//
// 优化后返回的是值拷贝（TopologyView），调用方改结果不影响注册表；
// 若误传内部指针，调用方的写会污染真源——这是深拷贝换成窄投影最危险的退化。
func TestBuildDoesNotExposeRegistryInternals(t *testing.T) {
	reg := runtime.NewRegistry()
	seedEquivalenceRegistry(t, reg)
	svc := NewTopologyService(reg)

	first := svc.Build("prod")
	// 篡改返回值。
	for i := range first.Nodes {
		first.Nodes[i].ServerID = "TAMPERED"
		first.Nodes[i].Status = "offline"
	}
	for i := range first.Groups {
		for j := range first.Groups[i].Members {
			first.Groups[i].Members[j] = "TAMPERED"
		}
	}

	second := svc.Build("prod")
	for _, n := range second.Nodes {
		if n.ServerID == "TAMPERED" {
			t.Fatal("篡改上一次的返回值污染了注册表真源——窄投影把内部引用透出去了")
		}
		if n.Status == "offline" {
			t.Fatal("篡改返回值影响了后续查询的状态")
		}
	}
	if len(second.Nodes) != len(first.Nodes) {
		t.Fatalf("两次 Build 的节点数应一致（%d），实际 %d", len(first.Nodes), len(second.Nodes))
	}
}

// TestForEachTopologyCopiesBackends 锁定 Backends 是在锁内拷贝的。
//
// 为什么这条必须有牙齿：TopologyView 是值类型，但 Backends 是切片——若直接透传注册表内部切片，
// 调用方（或任何后续写）改动该切片就会**污染注册表真源**，且污染是静默的：后续所有拓扑查询
// 都会用到被改过的后端列表，而没有任何地方报错。
//
// 直接对 ForEachTopology 的回调结果做「就地篡改」，再回到注册表核对：
// 不拷贝时篡改会穿透（这是 SetBackends 路径测不出来的——它整体替换切片，不暴露别名）。
func TestForEachTopologyCopiesBackends(t *testing.T) {
	reg := runtime.NewRegistry()
	seedEquivalenceRegistry(t, reg)

	var tampered bool
	reg.ForEachTopology("prod", func(v runtime.TopologyView) bool {
		if v.ServerID == "bc-1" && len(v.Backends) > 0 {
			for i := range v.Backends {
				v.Backends[i] = "TAMPERED" // 就地篡改：若未拷贝会直接改到注册表内部切片
			}
			tampered = true
		}
		return true
	})
	if !tampered {
		t.Fatal("前置条件：应遍历到 bc-1 且其 Backends 非空")
	}

	// 回注册表核对：内部 Backends 必须未被篡改。
	inst := reg.Get("prod", "bc-1")
	if inst == nil {
		t.Fatal("bc-1 应仍在注册表中")
	}
	for _, b := range inst.Backends {
		if b == "TAMPERED" {
			t.Fatal("篡改 ForEachTopology 回调里的 Backends 污染了注册表真源——切片未在锁内拷贝")
		}
	}
}

// TestBuildEdgesReflectLatestBackends 锁定新实现仍随注册表更新反映最新后端（不是快照写死）。
func TestBuildEdgesReflectLatestBackends(t *testing.T) {
	reg := runtime.NewRegistry()
	seedEquivalenceRegistry(t, reg)
	svc := NewTopologyService(reg)

	// 改注册表里 bc-1 的后端列表。
	reg.SetBackends("prod", "bc-1", []string{"bk-3"})
	after := svc.Build("prod")

	found := false
	for _, e := range after.Edges {
		if e.Source == "bc-1" && e.Target == "bk-3" {
			found = true
		}
		if e.Source == "bc-1" && (e.Target == "bk-1" || e.Target == "bk-2") {
			t.Fatalf("bc-1 的旧后端 %s 不应再出现（Build 应读最新注册表状态）", e.Target)
		}
	}
	if !found {
		t.Fatal("后续 Build 应反映新的 backends（bc-1 → bk-3）")
	}
}

// TestOnlineInstanceCountMatchesListFilter 锁定 system/status 的在线计数与旧口径等价。
func TestOnlineInstanceCountMatchesListFilter(t *testing.T) {
	reg := runtime.NewRegistry()
	now := time.Now().UTC()
	for i := range 20 {
		inst := &runtime.Instance{
			Namespace: "prod", ServerID: "s" + strconv.Itoa(i), Role: "bukkit",
			Address: "127.0.0.1:" + strconv.Itoa(20000+i),
		}
		if _, err := reg.Register(inst, time.Minute, now); err != nil {
			t.Fatalf("注册失败：%v", err)
		}
	}
	// 让其中 5 台离线。
	for i := range 5 {
		reg.Offline("prod", "s"+strconv.Itoa(i))
	}
	svc := NewSystemService("test", now, nil, reg, nil, nil)

	// 旧口径：len(List(Filter{Status: online}))。
	want := len(reg.List(runtime.Filter{Status: runtime.StatusOnline}))
	if got := svc.onlineInstanceCount(); got != want {
		t.Fatalf("在线数应为 %d（旧口径），实际 %d", want, got)
	}
	if want != 15 {
		t.Fatalf("前置条件：应剩 15 台在线，实际 %d", want)
	}
}

// TestOnlineInstanceCountNilRegistry 锁定注册表未装配时返回 0 而非 panic。
func TestOnlineInstanceCountNilRegistry(t *testing.T) {
	svc := NewSystemService("test", time.Now().UTC(), nil, nil, nil, nil)
	if got := svc.onlineInstanceCount(); got != 0 {
		t.Fatalf("注册表未装配时应返回 0，实际 %d", got)
	}
}
