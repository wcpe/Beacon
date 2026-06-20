package runtime

import (
	"testing"
	"time"
)

// TestRegisterStoresBackends 验证注册时 bc 上报的后端 serverId 集合被存入实例（事实，仅内存）。
func TestRegisterStoresBackends(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	if _, err := reg.Register(&Instance{
		Namespace: "prod", ServerID: "bc-1", Role: "bungee", Address: "10.0.0.1:25577",
		Backends: []string{"lobby-1", "lobby-2"},
	}, 30*time.Second, now); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	got := reg.Get("prod", "bc-1")
	if got == nil {
		t.Fatal("应能取到实例快照")
	}
	if len(got.Backends) != 2 || got.Backends[0] != "lobby-1" || got.Backends[1] != "lobby-2" {
		t.Fatalf("后端集合写入错误：%v", got.Backends)
	}
}

// TestRegisterBackendsSnapshotIsolated 验证返回快照与外部入参切片隔离（深拷贝，互不影响）。
func TestRegisterBackendsSnapshotIsolated(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	src := []string{"lobby-1"}
	if _, err := reg.Register(&Instance{
		Namespace: "prod", ServerID: "bc-1", Role: "bungee", Address: "10.0.0.1:25577", Backends: src,
	}, 30*time.Second, now); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	// 改外部入参不应污染内存快照。
	src[0] = "tampered"
	if got := reg.Get("prod", "bc-1"); got == nil || got.Backends[0] != "lobby-1" {
		t.Fatalf("快照应与入参切片隔离，实际 %v", got.Backends)
	}
}

// TestSetBackendsUpdatesExisting 验证 SetBackends 随心跳/上报刷新已注册 bc 的后端集合（仅内存、不涉 DB）。
func TestSetBackendsUpdatesExisting(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	if _, err := reg.Register(&Instance{
		Namespace: "prod", ServerID: "bc-1", Role: "bungee", Address: "10.0.0.1:25577",
		Backends: []string{"lobby-1"},
	}, 30*time.Second, now); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if ok := reg.SetBackends("prod", "bc-1", []string{"lobby-1", "lobby-2", "lobby-3"}); !ok {
		t.Fatal("已注册实例 SetBackends 应返回 true")
	}
	got := reg.Get("prod", "bc-1")
	if got == nil || len(got.Backends) != 3 {
		t.Fatalf("后端集合刷新错误：%v", got)
	}
}

// TestSetBackendsUnregisteredReturnsFalse 验证未注册实例 SetBackends 返回 false（不创建条目）。
func TestSetBackendsUnregisteredReturnsFalse(t *testing.T) {
	reg := NewRegistry()
	if ok := reg.SetBackends("prod", "ghost", []string{"lobby-1"}); ok {
		t.Fatal("未注册实例 SetBackends 应返回 false")
	}
}

// TestSetBackendsNilClears 验证传 nil 清空后端集合（bc 当前无后端）。
func TestSetBackendsNilClears(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	if _, err := reg.Register(&Instance{
		Namespace: "prod", ServerID: "bc-1", Role: "bungee", Address: "10.0.0.1:25577",
		Backends: []string{"lobby-1"},
	}, 30*time.Second, now); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	reg.SetBackends("prod", "bc-1", nil)
	if got := reg.Get("prod", "bc-1"); got == nil || len(got.Backends) != 0 {
		t.Fatalf("传 nil 应清空后端集合，实际 %v", got.Backends)
	}
}
