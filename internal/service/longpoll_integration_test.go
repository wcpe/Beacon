//go:build integration

package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"beacon/internal/merge"
	"beacon/internal/model"
	"beacon/internal/repository"
	"beacon/internal/runtime"
	"beacon/internal/runtime/longpoll"
	"beacon/internal/service"
)

// longpollStack 装配带唤醒的配置/zone 服务 + 有效配置长轮询服务（共享 hub 与 registry）。
func longpollStack(t *testing.T) (*service.ConfigService, *service.ZoneService, *service.EffectiveService, *runtime.Registry) {
	db := testDB(t)
	cr := repository.NewConfigItemRepository(db, noEncryptCipher())
	ar := repository.NewAuditLogRepository(db)
	asg := repository.NewZoneAssignmentRepository(db)
	reg := runtime.NewRegistry()
	hub := longpoll.NewHub()
	fileHub := longpoll.NewHub()
	eff := service.NewEffectiveService(cr, asg, hub)
	notifier := service.NewChangeNotifier(hub, fileHub, reg, asg)
	cfg := service.NewConfigService(db, cr, repository.NewConfigRevisionRepository(db, noEncryptCipher()), ar)
	cfg.SetNotifier(notifier)
	zone := service.NewZoneService(db, asg, ar, reg)
	zone.SetNotifier(notifier)
	return cfg, zone, eff, reg
}

func createGlobal(t *testing.T, cfg *service.ConfigService, content string) {
	t.Helper()
	if _, err := cfg.Create(service.CreateConfigParams{
		Namespace: "prod", Group: model.GlobalGroupCode, DataID: "app.yml",
		ScopeLevel: model.ScopeGlobal, Format: merge.FormatYAML, Content: content, Operator: "admin",
	}); err != nil {
		t.Fatalf("建全局配置失败: %v", err)
	}
}

type waitResult struct {
	eff     service.Effective
	changed bool
	err     error
}

// waitAsync 在 goroutine 中跑 WaitEffective，返回结果通道。
func waitAsync(eff *service.EffectiveService, serverID, groupHint, md5 string, timeout time.Duration) <-chan waitResult {
	ch := make(chan waitResult, 1)
	go func() {
		e, c, err := eff.WaitEffective(context.Background(), "prod", serverID, groupHint, md5, timeout)
		ch <- waitResult{e, c, err}
	}()
	return ch
}

// TestLongPollImmediateWhenChanged 当前 md5 与请求 md5 不同 → 立即返回（注册前已变不丢）。
func TestLongPollImmediateWhenChanged(t *testing.T) {
	cfg, _, eff, _ := longpollStack(t)
	createGlobal(t, cfg, "k: 1\n")

	start := time.Now()
	res, changed, err := eff.WaitEffective(context.Background(), "prod", "s1", "", "stale-md5", 2*time.Second)
	if err != nil || !changed {
		t.Fatalf("已变更应立即返回 changed=true，实际 changed=%v err=%v", changed, err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("已变更不应挂起等待")
	}
	if len(res.Items) != 1 {
		t.Fatalf("应返回 1 个 dataId，实际 %d", len(res.Items))
	}
}

// TestLongPollTimeout304 无变更挂起到超时 → 返回 changed=false。
func TestLongPollTimeout304(t *testing.T) {
	cfg, _, eff, _ := longpollStack(t)
	createGlobal(t, cfg, "k: 1\n")
	cur, _ := eff.Resolve("prod", "s1", "")

	start := time.Now()
	_, changed, err := eff.WaitEffective(context.Background(), "prod", "s1", "", cur.MD5, 200*time.Millisecond)
	if err != nil || changed {
		t.Fatalf("无变更应超时 changed=false，实际 changed=%v err=%v", changed, err)
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Fatal("应挂起到接近超时才返回")
	}
}

// TestLongPollWakeOnPublish 挂起期间发布 → 被唤醒、重算、200。
func TestLongPollWakeOnPublish(t *testing.T) {
	cfg, _, eff, _ := longpollStack(t)
	createGlobal(t, cfg, "k: 1\n")
	cur, _ := eff.Resolve("prod", "s1", "")

	ch := waitAsync(eff, "s1", "", cur.MD5, 3*time.Second)
	time.Sleep(80 * time.Millisecond) // 让 waiter 注册并挂起

	item, _ := cfg.Get(idOfGlobal(t, cfg))
	if _, err := cfg.Publish(item.ID, "k: 2\n", "admin", "改", ""); err != nil {
		t.Fatalf("发布失败: %v", err)
	}

	select {
	case r := <-ch:
		if r.err != nil || !r.changed {
			t.Fatalf("发布后应被唤醒 changed=true，实际 changed=%v err=%v", r.changed, r.err)
		}
		if !strings.Contains(r.eff.Items[0].Content, "k: 2") {
			t.Fatalf("应下发新内容，实际 %q", r.eff.Items[0].Content)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("发布后未在期限内被唤醒")
	}
}

// TestLongPollOnlyAffected 发布只唤醒受影响：他组发布不唤醒、本组发布唤醒。
func TestLongPollOnlyAffected(t *testing.T) {
	cfg, _, eff, reg := longpollStack(t)
	// s1 属 area1（注册进内存供 group 反查）
	if _, err := reg.Register(&runtime.Instance{
		Namespace: "prod", ServerID: "s1", GroupHint: "area1", ResolvedGroup: "area1", Address: "10.0.0.1:1",
	}, 30*time.Second, time.Now().UTC()); err != nil {
		t.Fatalf("注册实例失败: %v", err)
	}
	mkGroup := func(group, content string) uint {
		it, err := cfg.Create(service.CreateConfigParams{
			Namespace: "prod", Group: group, DataID: "app.yml",
			ScopeLevel: model.ScopeGroup, Format: merge.FormatYAML, Content: content, Operator: "admin",
		})
		if err != nil {
			t.Fatalf("建 %s 组配置失败: %v", group, err)
		}
		return it.ID
	}
	mkGroup("area1", "v: 1\n")
	area2ID := mkGroup("area2", "v: 1\n")
	cur, _ := eff.Resolve("prod", "s1", "area1")

	// 他组（area2）发布 → s1 不被唤醒 → 超时
	ch := waitAsync(eff, "s1", "area1", cur.MD5, 250*time.Millisecond)
	time.Sleep(80 * time.Millisecond)
	if _, err := cfg.Publish(area2ID, "v: 99\n", "admin", "他组改", ""); err != nil {
		t.Fatalf("发布 area2 失败: %v", err)
	}
	select {
	case r := <-ch:
		if r.changed {
			t.Fatal("他组发布不应唤醒 s1")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitEffective 未返回")
	}
}

// TestLongPollReassignHotPush 改派触发热推：被唤醒后按新 zone 重算下发。
func TestLongPollReassignHotPush(t *testing.T) {
	cfg, zone, eff, _ := longpollStack(t)
	mkZone := func(target, content string) {
		if _, err := cfg.Create(service.CreateConfigParams{
			Namespace: "prod", Group: "area1", DataID: "app.yml",
			ScopeLevel: model.ScopeZone, ScopeTarget: target, Format: merge.FormatYAML, Content: content, Operator: "admin",
		}); err != nil {
			t.Fatalf("建 zone %s 配置失败: %v", target, err)
		}
	}
	mkZone("zoneA", "z: \"A\"\n")
	mkZone("zoneB", "z: \"B\"\n")
	if _, err := zone.Assign("prod", "s1", "area1", "zoneA", "admin", "", ""); err != nil {
		t.Fatalf("初始指派失败: %v", err)
	}
	cur, _ := eff.Resolve("prod", "s1", "area1")

	ch := waitAsync(eff, "s1", "area1", cur.MD5, 3*time.Second)
	time.Sleep(80 * time.Millisecond)
	if _, err := zone.Assign("prod", "s1", "area1", "zoneB", "admin", "", ""); err != nil {
		t.Fatalf("改派失败: %v", err)
	}
	select {
	case r := <-ch:
		if r.err != nil || !r.changed {
			t.Fatalf("改派应热推 changed=true，实际 changed=%v err=%v", r.changed, r.err)
		}
		if !strings.Contains(r.eff.Items[0].Content, "B") {
			t.Fatalf("改派后应下发 zoneB 内容，实际 %q", r.eff.Items[0].Content)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("改派后未在期限内被唤醒")
	}
}

// idOfGlobal 取全局 app.yml 配置项的 id。
func idOfGlobal(t *testing.T, cfg *service.ConfigService) uint {
	t.Helper()
	items, err := cfg.List(repository.ConfigFilter{Namespace: "prod", DataID: "app.yml", ScopeLevel: model.ScopeGlobal})
	if err != nil || len(items) != 1 {
		t.Fatalf("取全局配置失败: err=%v 数量=%d", err, len(items))
	}
	return items[0].ID
}
