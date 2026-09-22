package service

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// fakeRecoverySink 记录自动消解调用（FR-232 接线测试用）。
type fakeRecoverySink struct {
	calls []string
}

func (f *fakeRecoverySink) AutoResolveAlerts(ns, serverID string) (int64, error) {
	f.calls = append(f.calls, ns+"/"+serverID)
	return 0, nil
}

// seedActiveServer 建一个 active 的 namespace + server 行，供注册 / 心跳的运行资格校验放行。
func seedActiveServer(t *testing.T, svc *InstanceService, nsCode, serverID string) {
	t.Helper()
	ns := model.Namespace{Code: nsCode, Name: nsCode, Lifecycle: model.NamespaceLifecycleActive}
	if err := svc.db.Create(&ns).Error; err != nil {
		t.Fatalf("建 namespace 失败: %v", err)
	}
	if err := svc.db.Create(&model.Server{NamespaceID: ns.ID, ServerID: serverID, Kind: model.ServerKindBackend}).Error; err != nil {
		t.Fatalf("建 server 失败: %v", err)
	}
}

// TestFR232RecoveryTriggersAutoResolveOnRealPaths 是 FR-232 的**接线**测试：走真实的
// Register / Heartbeat 与真实 SweepExpired 推进状态，验证「非 online → online」确实触发自动消解。
//
// 该测试专门堵住此前只测 dispatchAlerts 分支、却漏掉「该分支在生产永不可达」的假绿：
// 恢复 online 由注册 / 心跳直接置位、不经健康扫描的 Sweep 输出，故触发必须挂在 InstanceService。
func TestFR232RecoveryTriggersAutoResolveOnRealPaths(t *testing.T) {
	svc, reg, _ := newOfflineTestStack(t)
	seedActiveServer(t, svc, "prod", "rec-1")
	sink := &fakeRecoverySink{}
	svc.SetRecoverySink(sink)

	// 1) 首次注册（全新实例，注册前不存在）→ 不算恢复。
	if _, err := svc.Register(regParams("rec-1")); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if len(sink.calls) != 0 {
		t.Fatalf("首次注册不应触发自动消解，实际 %v", sink.calls)
	}

	// 2) 已 online 时心跳 → 不算恢复。
	if _, err := svc.Heartbeat("prod", "rec-1"); err != nil {
		t.Fatalf("心跳失败: %v", err)
	}
	if len(sink.calls) != 0 {
		t.Fatalf("已 online 续约不应触发自动消解，实际 %v", sink.calls)
	}

	// 3) 真实 SweepExpired 把它推进到 degraded（非 online）——模拟漏心跳。
	reg.SweepExpired(time.Now().UTC().Add(20*time.Second), 15*time.Second, 30*time.Second, 120*time.Second)
	got := reg.Get("prod", "rec-1")
	if got == nil || got.Status == runtime.StatusOnline {
		t.Fatalf("SweepExpired 后应处于非 online，实际 %+v", got)
	}

	// 4) 心跳翻回 online → 触发自动消解（核心断言）。
	if _, err := svc.Heartbeat("prod", "rec-1"); err != nil {
		t.Fatalf("恢复心跳失败: %v", err)
	}
	if len(sink.calls) != 1 || sink.calls[0] != "prod/rec-1" {
		t.Fatalf("恢复心跳应触发一次自动消解，实际 %v", sink.calls)
	}

	// 5) 再次心跳（已 online）→ 不再触发。
	if _, err := svc.Heartbeat("prod", "rec-1"); err != nil {
		t.Fatalf("二次心跳失败: %v", err)
	}
	if len(sink.calls) != 1 {
		t.Fatalf("已恢复后重复心跳不应再触发，实际 %v", sink.calls)
	}

	// 6) 再推进非 online，经**重新注册**（同址重连）翻回 online → 触发恢复（Register 路径）。
	reg.SweepExpired(time.Now().UTC().Add(20*time.Second), 15*time.Second, 30*time.Second, 120*time.Second)
	if _, err := svc.Register(regParams("rec-1")); err != nil {
		t.Fatalf("重注册失败: %v", err)
	}
	if len(sink.calls) != 2 {
		t.Fatalf("重注册恢复应再触发一次，实际 %v", sink.calls)
	}
}
