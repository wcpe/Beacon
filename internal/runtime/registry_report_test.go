package runtime

import (
	"testing"
	"time"
)

// TestReportWritesLoadMetrics 验证 Report 把人数 / TPS / 内存 / CPU 四类负载事实写入实例（仅展示，不参与决策）。
func TestReportWritesLoadMetrics(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	if _, err := reg.Register(&Instance{
		Namespace: "prod", ServerID: "lobby-1", Role: "bukkit", Address: "10.0.0.1:25565",
	}, 30*time.Second, now); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	// 上报真实负载：人数 42、TPS 19.9、已用堆 128MiB、最大堆 512MiB、CPU 负载 0.35（bukkit 不带 proxy）。
	if ok := reg.Report("prod", "lobby-1", "md5x", 42, 19.9, 128<<20, 512<<20, 0.35, nil); !ok {
		t.Fatal("已注册实例 Report 应返回 true")
	}

	got := reg.Get("prod", "lobby-1")
	if got == nil {
		t.Fatal("应能取到实例快照")
	}
	if got.AppliedMD5 != "md5x" || got.PlayerCount != 42 || got.TPS != 19.9 {
		t.Fatalf("人数 / TPS / appliedMd5 写入错误：%+v", got)
	}
	if got.MemUsed != 128<<20 || got.MemMax != 512<<20 {
		t.Fatalf("内存字段写入错误：memUsed=%d memMax=%d", got.MemUsed, got.MemMax)
	}
	if got.CPULoad != 0.35 {
		t.Fatalf("CPU 负载写入错误：%v", got.CPULoad)
	}
}

// TestReportCPUUnavailableSentinel 验证 CPU 不可用哨兵值 -1.0 原样写入（由展示层判定不可用）。
func TestReportCPUUnavailableSentinel(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	if _, err := reg.Register(&Instance{
		Namespace: "prod", ServerID: "lobby-1", Address: "10.0.0.1:25565",
	}, 30*time.Second, now); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	reg.Report("prod", "lobby-1", "", 0, 20.0, 0, 0, -1.0, nil)
	if got := reg.Get("prod", "lobby-1"); got == nil || got.CPULoad != -1.0 {
		t.Fatalf("CPU 不可用哨兵 -1.0 应原样写入，实际 %v", got)
	}
}

// TestReportUnregisteredReturnsFalse 未注册实例 Report 返回 false（不创建条目）。
func TestReportUnregisteredReturnsFalse(t *testing.T) {
	reg := NewRegistry()
	if ok := reg.Report("prod", "ghost", "", 1, 20.0, 1, 2, 0.1, nil); ok {
		t.Fatal("未注册实例 Report 应返回 false")
	}
}

// TestReportProxyMetricsBC 验证 bc 上报的 proxy 指标写入实例；nil proxy 不刷新已有 Proxy（向后兼容）。
func TestReportProxyMetricsBC(t *testing.T) {
	reg := NewRegistry()
	now := time.Now().UTC()
	if _, err := reg.Register(&Instance{
		Namespace: "prod", ServerID: "bc-1", Role: "bungee", Address: "10.0.0.9:25577",
	}, 30*time.Second, now); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	// bc 上报含 proxy：连接 128 / 线程 64 / 运行时长 / 后端 3-of-4 / 平均延迟 12.5。
	proxy := &ProxyMetrics{
		OnlineConnections: 128, ThreadCount: 64, UptimeMs: 3600000,
		BackendUp: 3, BackendTotal: 4, BackendAvgLatencyMs: 12.5,
	}
	reg.Report("prod", "bc-1", "m", 50, 0.0, 1, 2, -1.0, proxy)
	got := reg.Get("prod", "bc-1")
	if got == nil || got.Proxy.OnlineConnections != 128 || got.Proxy.ThreadCount != 64 ||
		got.Proxy.BackendUp != 3 || got.Proxy.BackendTotal != 4 || got.Proxy.BackendAvgLatencyMs != 12.5 {
		t.Fatalf("bc proxy 指标写入错误：%+v", got)
	}

	// 再以 nil proxy 上报（模拟旧 agent / bukkit 缺键）：Proxy 字段不应被清空。
	reg.Report("prod", "bc-1", "m", 51, 0.0, 1, 2, -1.0, nil)
	got2 := reg.Get("prod", "bc-1")
	if got2 == nil || got2.Proxy.OnlineConnections != 128 {
		t.Fatalf("nil proxy 上报不应清空已有 Proxy，实际 %+v", got2.Proxy)
	}
}
