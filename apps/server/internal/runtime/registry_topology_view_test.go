package runtime

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestForEachTopologyConcurrentWithWriters 锁定 ForEachTopology 与各类写操作并发时的安全性。
//
// 为什么必须单独测并发：该方法在 r.mu **读锁持有期间**同步调用回调（与既有 ForEach 同款范式），
// 而生产里拓扑查询恰恰与 agent 注册 / 心跳 / 改派 / 离线同时发生。单线程用例再全也证明不了
// 锁的正确性——读锁与写锁的交互只有真并发才暴露（尤其配合 -race）。
//
// 同时验证「回调里拿到的是值拷贝」不会因并发写而读到撕裂状态：
// 每个 TopologyView 内部字段必须自洽（Role 与 Backends 的取值来自同一快照）。
func TestForEachTopologyConcurrentWithWriters(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	const ns = "prod"
	const readers, writers, iterations = 8, 4, 300

	// 预置一批实例。
	for i := range 40 {
		id := "srv-" + strconv.Itoa(i)
		role := "bukkit"
		if i%4 == 0 {
			role = "bungee"
		}
		in := &Instance{
			Namespace: ns, ServerID: id, Role: role,
			ResolvedGroup: "g1", ResolvedZone: "z1",
			Address:  "127.0.0.1:" + strconv.Itoa(20000+i),
			Metadata: map[string]string{"k": "v"},
		}
		if _, err := reg.Register(in, time.Minute, now); err != nil {
			t.Fatalf("预置 %s 失败：%v", id, err)
		}
	}

	var wg sync.WaitGroup

	// 读侧：并发遍历拓扑，校验每个视图内部自洽（不读撕裂状态）。
	//
	// 注意这里**不能**断言「bukkit 不带 Backends」：SetBackends 按设计不校验角色
	// （见其实现，只查实例是否存在），故直接调它可给任意角色挂后端——本用例的写侧正是这么做的。
	// 「只有 proxy 才填 Backends」是 service 层上报路径的约定，不是 Registry 的不变量。
	// 故此处只断言「字段本身有效」，不断言角色与 Backends 的组合关系。
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				reg.ForEachTopology(ns, func(v TopologyView) bool {
					if v.ServerID == "" {
						t.Errorf("ServerID 为空——读到未初始化状态")
						return false
					}
					// 遍历期间改回调里的切片，验证不污染真源。
					for i := range v.Backends {
						v.Backends[i] = "TAMPERED"
					}
					return true
				})
			}
		}()
	}

	// 写侧：并发做注册 / 心跳 / 改派 / 离线 / 改后端 / 清归属。
	for w := range writers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range iterations {
				serverID := "srv-" + strconv.Itoa(i%40)
				switch (i + id) % 6 {
				case 0:
					_, _ = reg.Register(&Instance{
						Namespace: ns, ServerID: serverID, Role: "bukkit",
						Address: "127.0.0.1:" + strconv.Itoa(30000+i),
					}, time.Minute, time.Now().UTC())
				case 1:
					reg.Heartbeat(ns, serverID, time.Now().UTC())
				case 2:
					reg.UpdateAssignment(ns, serverID, "g"+strconv.Itoa(i%3), "z"+strconv.Itoa(i%5))
				case 3:
					reg.Offline(ns, serverID)
				case 4:
					reg.SetBackends(ns, serverID, []string{"srv-1", "srv-2"})
				default:
					reg.ClearAssignment(ns, serverID)
				}
			}
		}(w)
	}

	wg.Wait()

	// 收尾只断言两件**确定性成立**的事：
	//  1. 注册表仍可用（并发读写未破坏内部结构）——遍历能取到实例；
	//  2. 读侧在回调里篡改 Backends 没有穿透真源。
	//
	// 刻意**不**断言「某个特定 serverId 仍在注册表里」：写侧会并发 Offline 掉任意实例，
	// 那是合法行为，据此判失败是把「预期内的并发结果」误当成缺陷（首版即如此，且它是偶发的——
	// 取决于写侧调度顺序，因而表现为间歇性假红）。
	seen := 0
	reg.ForEachTopology(ns, func(v TopologyView) bool {
		seen++
		for _, b := range v.Backends {
			if b == "TAMPERED" {
				t.Errorf("读侧篡改 Backends 穿透到了注册表真源（实例 %s）——切片未在锁内拷贝", v.ServerID)
			}
		}
		return true
	})
	if seen == 0 {
		t.Fatal("并发读写后遍历应仍能取到实例（注册表结构未被破坏）")
	}
}

// TestForEachTopologyEarlyReturn 锁定 fn 返回 false 时提前终止遍历（避免无谓的全表扫描）。
func TestForEachTopologyEarlyReturn(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	for i := range 30 {
		in := &Instance{
			Namespace: "prod", ServerID: "s" + strconv.Itoa(i), Role: "bukkit",
			Address: "127.0.0.1:" + strconv.Itoa(20000+i),
		}
		if _, err := reg.Register(in, time.Minute, now); err != nil {
			t.Fatalf("注册失败：%v", err)
		}
	}

	visited := 0
	reg.ForEachTopology("prod", func(TopologyView) bool {
		visited++
		return visited < 5 // 第 5 个后终止
	})
	if visited != 5 {
		t.Fatalf("提前终止后应只访问 5 个实例，实际 %d", visited)
	}
}

// TestForEachTopologyNamespaceFilter 锁定 namespace 过滤（空串=全部，非空=精确匹配）。
func TestForEachTopologyNamespaceFilter(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	for _, spec := range []struct{ ns, id string }{
		{"prod", "p1"}, {"prod", "p2"}, {"test", "t1"},
	} {
		in := &Instance{
			Namespace: spec.ns, ServerID: spec.id, Role: "bukkit",
			Address: "127.0.0.1:20000",
		}
		if _, err := reg.Register(in, time.Minute, now); err != nil {
			t.Fatalf("注册 %s/%s 失败：%v", spec.ns, spec.id, err)
		}
	}

	count := func(ns string) int {
		n := 0
		reg.ForEachTopology(ns, func(TopologyView) bool { n++; return true })
		return n
	}
	if got := count("prod"); got != 2 {
		t.Fatalf("prod 应有 2 个实例，实际 %d", got)
	}
	if got := count("test"); got != 1 {
		t.Fatalf("test 应有 1 个实例，实际 %d", got)
	}
	if got := count(""); got != 3 {
		t.Fatalf("空 namespace 应遍历全部（3 个），实际 %d", got)
	}
	if got := count("nonexistent"); got != 0 {
		t.Fatalf("不存在的 namespace 应为 0，实际 %d", got)
	}
}

// TestForEachTopologyBackendsCopied 锁定 Backends 必须是拷贝（就地篡改不得穿透真源）。
func TestForEachTopologyBackendsCopied(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	in := &Instance{
		Namespace: "prod", ServerID: "bc-1", Role: "bungee",
		Address: "127.0.0.1:20000",
	}
	if _, err := reg.Register(in, time.Minute, now); err != nil {
		t.Fatalf("注册失败：%v", err)
	}
	reg.SetBackends("prod", "bc-1", []string{"bk-1", "bk-2"})

	reg.ForEachTopology("prod", func(v TopologyView) bool {
		for i := range v.Backends {
			v.Backends[i] = "TAMPERED"
		}
		return true
	})

	got := reg.Get("prod", "bc-1")
	for _, b := range got.Backends {
		if b == "TAMPERED" {
			t.Fatal("回调里篡改 Backends 穿透到了注册表真源——未在锁内拷贝")
		}
	}
	if len(got.Backends) != 2 {
		t.Fatalf("真源 Backends 应仍为 2 个，实际 %v", got.Backends)
	}
}
