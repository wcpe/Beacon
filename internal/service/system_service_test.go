package service

import (
	"errors"
	"testing"
	"time"

	rt "beacon/internal/runtime"
)

// fakePinger 是 dbPinger 测试替身：按 err 决定 Ping 成败。
type fakePinger struct {
	err error
}

func (f fakePinger) Ping() error { return f.err }

// addOnline 向注册表注入一个在线实例（供在线计数断言）。
func addOnline(t *testing.T, reg *rt.Registry, ns, serverID, addr string) {
	t.Helper()
	if _, err := reg.Register(&rt.Instance{Namespace: ns, ServerID: serverID, Address: addr},
		30*time.Second, time.Now()); err != nil {
		t.Fatalf("注册在线实例失败: %v", err)
	}
}

// TestSystemStatusDBConnected 验证 Ping 成功时 DB 标记为连通、无错误说明。
func TestSystemStatusDBConnected(t *testing.T) {
	svc := NewSystemService("v1.2.3", time.Now().Add(-time.Minute), fakePinger{err: nil}, rt.NewRegistry(), true)
	st := svc.Status()
	if !st.DB.Connected {
		t.Fatalf("Ping 成功时 DB 应连通，实际 %+v", st.DB)
	}
	if st.DB.Error != "" {
		t.Fatalf("连通时不应带错误说明，实际 %q", st.DB.Error)
	}
}

// TestSystemStatusDBDisconnected 验证 Ping 失败时 DB 标记为断开并带错误说明（页眉能反映 DB 断开）。
func TestSystemStatusDBDisconnected(t *testing.T) {
	svc := NewSystemService("v1", time.Now(), fakePinger{err: errors.New("连接已断开")}, rt.NewRegistry(), true)
	st := svc.Status()
	if st.DB.Connected {
		t.Fatal("Ping 失败时 DB 应为断开，实际为连通")
	}
	if st.DB.Error == "" {
		t.Fatal("断开时应带错误说明，实际为空")
	}
}

// TestSystemStatusOnlineCount 验证在线实例数取自内存注册表的在线条目。
func TestSystemStatusOnlineCount(t *testing.T) {
	reg := rt.NewRegistry()
	addOnline(t, reg, "prod", "lobby-1", "10.0.0.1:25565")
	addOnline(t, reg, "prod", "lobby-2", "10.0.0.2:25565")
	svc := NewSystemService("v1", time.Now(), fakePinger{}, reg, false)
	st := svc.Status()
	if st.OnlineInstances != 2 {
		t.Fatalf("在线实例数应为 2，实际 %d", st.OnlineInstances)
	}
}

// TestSystemStatusUptimeAndFields 验证运行时长按 now-startedAt 计算，且采样器状态、CPU 占位、Go 运行时字段就位。
func TestSystemStatusUptimeAndFields(t *testing.T) {
	start := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	svc := NewSystemService("v9", start, fakePinger{}, rt.NewRegistry(), true)
	svc.now = func() time.Time { return start.Add(90 * time.Second) }
	st := svc.Status()

	if st.Version != "v9" {
		t.Fatalf("版本应为 v9，实际 %q", st.Version)
	}
	if !st.StartedAt.Equal(start) {
		t.Fatalf("启动时间应为 %v，实际 %v", start, st.StartedAt)
	}
	if st.UptimeSeconds != 90 {
		t.Fatalf("运行时长应为 90s，实际 %d", st.UptimeSeconds)
	}
	if !st.SamplerEnabled {
		t.Fatal("采样器应标记为启用")
	}
	if st.CPUAvailable {
		t.Fatal("当前无 dep-free CPU 采集办法，CPUAvailable 应为 false（占位）")
	}
	if st.Runtime.Goroutines <= 0 {
		t.Fatalf("goroutine 数应为正，实际 %d", st.Runtime.Goroutines)
	}
	if st.Runtime.HeapSys == 0 {
		t.Fatal("HeapSys 应为正（运行时已向系统申请堆）")
	}
}
