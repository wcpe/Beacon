package service

import (
	"errors"
	"testing"
	"time"

	rt "github.com/wcpe/Beacon/internal/runtime"
)

// fakePinger 是 dbPinger 测试替身：按 err 决定 Ping 成败。
type fakePinger struct {
	err error
}

func (f fakePinger) Ping() error { return f.err }

// samplerEnabledFn 把布尔常量包装为采样器启用回调（FR-61：NewSystemService 改取 func() bool）。
func samplerEnabledFn(enabled bool) func() bool {
	return func() bool { return enabled }
}

// fakeCPUSampler 是 cpuSampler 测试替身：按预置值返回 CPU 占比与可用性。
type fakeCPUSampler struct {
	percent   float64
	available bool
}

func (f fakeCPUSampler) Percent() (float64, bool) { return f.percent, f.available }

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
	svc := NewSystemService("v1.2.3", time.Now().Add(-time.Minute), fakePinger{err: nil}, rt.NewRegistry(), samplerEnabledFn(true), fakeCPUSampler{available: false})
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
	svc := NewSystemService("v1", time.Now(), fakePinger{err: errors.New("连接已断开")}, rt.NewRegistry(), samplerEnabledFn(true), fakeCPUSampler{available: false})
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
	svc := NewSystemService("v1", time.Now(), fakePinger{}, reg, samplerEnabledFn(false), fakeCPUSampler{available: false})
	st := svc.Status()
	if st.OnlineInstances != 2 {
		t.Fatalf("在线实例数应为 2，实际 %d", st.OnlineInstances)
	}
}

// TestSystemStatusUptimeAndFields 验证运行时长按 now-startedAt 计算，且采样器状态、Go 运行时字段就位。
func TestSystemStatusUptimeAndFields(t *testing.T) {
	start := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	svc := NewSystemService("v9", start, fakePinger{}, rt.NewRegistry(), samplerEnabledFn(true), fakeCPUSampler{available: false})
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
	if st.Runtime.Goroutines <= 0 {
		t.Fatalf("goroutine 数应为正，实际 %d", st.Runtime.Goroutines)
	}
	if st.Runtime.HeapSys == 0 {
		t.Fatal("HeapSys 应为正（运行时已向系统申请堆）")
	}
}

// TestSystemStatusCPUAvailable 验证 CPU 采集成功时 CPUAvailable=true、CPUPercent 取采样值并落在 [0,100]。
func TestSystemStatusCPUAvailable(t *testing.T) {
	svc := NewSystemService("v1", time.Now(), fakePinger{}, rt.NewRegistry(), samplerEnabledFn(true),
		fakeCPUSampler{percent: 37.5, available: true})
	st := svc.Status()
	if !st.CPUAvailable {
		t.Fatal("采样成功时 CPUAvailable 应为 true")
	}
	if st.CPUPercent < 0 || st.CPUPercent > 100 {
		t.Fatalf("CPUPercent 应落在 [0,100]，实际 %v", st.CPUPercent)
	}
	if st.CPUPercent != 37.5 {
		t.Fatalf("CPUPercent 应取采样值 37.5，实际 %v", st.CPUPercent)
	}
}

// TestSystemStatusCPUClamp 验证多核下采样值超 100% 时被钳到 100（gopsutil 进程占比不按核心数归一）。
func TestSystemStatusCPUClamp(t *testing.T) {
	svc := NewSystemService("v1", time.Now(), fakePinger{}, rt.NewRegistry(), samplerEnabledFn(true),
		fakeCPUSampler{percent: 350.0, available: true})
	st := svc.Status()
	if !st.CPUAvailable {
		t.Fatal("采样成功时 CPUAvailable 应为 true")
	}
	if st.CPUPercent != 100 {
		t.Fatalf("超 100%% 的采样值应被钳到 100，实际 %v", st.CPUPercent)
	}
}

// TestGopsutilCPUSamplerRealProcess 用真实 gopsutil 采样器跑通本进程：预热后采集应可用且占比落在 [0,100]。
// 真实数值不可断言，仅校验「可用」与区间，覆盖生产装配路径（预热 + 采集）。
func TestGopsutilCPUSamplerRealProcess(t *testing.T) {
	sampler := NewGopsutilCPUSampler()
	svc := NewSystemService("v1", time.Now(), fakePinger{}, rt.NewRegistry(), samplerEnabledFn(true), sampler)
	st := svc.Status()
	if !st.CPUAvailable {
		t.Fatal("真实采样器在本进程应可用（CPUAvailable=true）")
	}
	if st.CPUPercent < 0 || st.CPUPercent > 100 {
		t.Fatalf("真实采样 CPUPercent 应落在 [0,100]，实际 %v", st.CPUPercent)
	}
}

// TestSystemStatusCPUUnavailable 验证采集失败时优雅降级：CPUAvailable=false、CPUPercent 恒 0。
func TestSystemStatusCPUUnavailable(t *testing.T) {
	svc := NewSystemService("v1", time.Now(), fakePinger{}, rt.NewRegistry(), samplerEnabledFn(true),
		fakeCPUSampler{percent: 0, available: false})
	st := svc.Status()
	if st.CPUAvailable {
		t.Fatal("采集失败时 CPUAvailable 应为 false（降级）")
	}
	if st.CPUPercent != 0 {
		t.Fatalf("不可用时 CPUPercent 应恒 0，实际 %v", st.CPUPercent)
	}
}
