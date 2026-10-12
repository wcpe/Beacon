package service

import (
	"strconv"
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// seedTopologyRegistry 造一个 n 实例的注册表（含 Metadata 与 Backends，形态贴近生产）。
// bc 占 1/8（与真实拓扑结构同量级），其余为 bukkit；每个 bc 挂若干后端以便生成边。
func seedTopologyRegistry(b *testing.B, n int) *runtime.Registry {
	b.Helper()
	reg := runtime.NewRegistry()
	const ns = "prod"
	now := time.Now().UTC()
	for i := range n {
		id := "srv-" + strconv.Itoa(i)
		isBC := i%8 == 0
		role := "bukkit"
		if isBC {
			role = roleBungee
		}
		in := &runtime.Instance{
			Namespace:     ns,
			ServerID:      id,
			Role:          role,
			ResolvedGroup: "g" + strconv.Itoa(i%4),
			ResolvedZone:  "z" + strconv.Itoa(i%16),
			Address:       "127.0.0.1:" + strconv.Itoa(20000+i),
			// Metadata 是深拷贝里最贵的一项（每实例一次 map 分配），给 6 个键贴近生产。
			Metadata: map[string]string{
				"region": "r1", "zone": "z1", "role": "backend",
				"env": "prod", "owner": "ops", "version": "1.2.3",
			},
		}
		if _, err := reg.Register(in, time.Minute, now); err != nil {
			b.Fatalf("注册 %s 失败：%v", id, err)
		}
		if isBC {
			reg.SetBackends(ns, id, []string{"srv-1", "srv-2", "srv-3"})
		}
	}
	return reg
}

// BenchmarkTopologyBuild 量化 Build 的成本随实例数的增长（每实例边际成本）。
func BenchmarkTopologyBuild(b *testing.B) {
	for _, n := range []int{64, 1000} {
		reg := seedTopologyRegistry(b, n)
		svc := NewTopologyService(reg)
		b.Run(strconv.Itoa(n)+"台", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = svc.Build("prod")
			}
		})
	}
}

// BenchmarkSystemStatusOnlineCount 量化 system/status 取在线数的成本。
func BenchmarkSystemStatusOnlineCount(b *testing.B) {
	for _, n := range []int{64, 1000} {
		reg := seedTopologyRegistry(b, n)
		svc := NewSystemService("test", time.Now().UTC(), nil, reg, nil, nil)
		b.Run(strconv.Itoa(n)+"台", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = svc.onlineInstanceCount()
			}
		})
	}
}
