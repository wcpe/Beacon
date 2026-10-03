package service

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// 本文件覆盖 FR-232 的又一自动消解触发点：**v1 主动下线**（`InstanceService.Offline`）。
// 背景：实例被运维按死（落 `server_offline` 拒绝态、重注册 403）后不可能再回 `online`，
// 其未处理告警若留着就永远没有出路。反向操作 `Online`（取消下线）**刻意不做任何告警动作**——
// 取消下线不等于实例已恢复在线，此时消解会把尚未恢复的真实故障藏起来。
//
// 所有用例都同时断言**其它实例 / 其它环境**的告警原样未动：一条 UPDATE 打宽了会把无关告警误标为
// 已处理，运维再也看不到真正的待办，比不消解更危险。

// newOfflineAlertTestStack 取主动下线测试栈，并再断言一次告警表就位且为空
// （消解用例需要真实的 UPDATE 与回读断言，故不隐含依赖共享栈的表清单）。
func newOfflineAlertTestStack(t *testing.T) (*InstanceService, *runtime.Registry, *gorm.DB) {
	t.Helper()
	svc, reg, db := newOfflineTestStack(t)
	if err := db.AutoMigrate(&model.AlertEvent{}); err != nil {
		t.Fatalf("迁移告警表失败: %v", err)
	}
	if err := db.Exec("DELETE FROM alert_event").Error; err != nil {
		t.Fatalf("清空告警表失败: %v", err)
	}
	return svc, reg, db
}

// hideAlertTable 临时把告警表改名，使自动消解的 UPDATE 必然失败（用于验证「消解失败不得让下线失败」）。
// 用改名而非 DROP：行数据必须保住，恢复表名后才能继续断言「告警本身没被改动」。
// 返回幂等的恢复函数（用例内可提前恢复；测试收尾也会再兜一次）。
func hideAlertTable(t *testing.T, db *gorm.DB) func() {
	t.Helper()
	if err := db.Exec("ALTER TABLE alert_event RENAME TO alert_event_hidden").Error; err != nil {
		t.Fatalf("临时改名告警表失败: %v", err)
	}
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		if err := db.Exec("ALTER TABLE alert_event_hidden RENAME TO alert_event").Error; err != nil {
			t.Errorf("恢复告警表名失败: %v", err)
		}
	}
	t.Cleanup(restore)
	return restore
}

// TestOfflineAutoResolvesAlertsWithoutCollateral 主动下线：该实例 open / acknowledged 告警全部消解，
// 同环境兄弟实例与另一环境的同名实例告警不受影响。
func TestOfflineAutoResolvesAlertsWithoutCollateral(t *testing.T) {
	svc, reg, db := newOfflineAlertTestStack(t)
	if _, err := svc.Register(regParams("lobby-1")); err != nil {
		t.Fatalf("注册 lobby-1 失败: %v", err)
	}
	openID := seedLifecycleAlert(t, db, "prod", "lobby-1", model.AlertEventStatusOpen)
	ackID := seedLifecycleAlert(t, db, "prod", "lobby-1", model.AlertEventStatusAcknowledged)
	siblingID := seedLifecycleAlert(t, db, "prod", "lobby-2", model.AlertEventStatusOpen)
	otherEnvID := seedLifecycleAlert(t, db, "dev", "lobby-1", model.AlertEventStatusOpen)

	if err := svc.Offline("prod", "lobby-1", "故障下架", "admin", "127.0.0.1"); err != nil {
		t.Fatalf("下线应成功: %v", err)
	}
	if reg.Get("prod", "lobby-1") != nil {
		t.Fatalf("下线后应已移出内存可用集")
	}

	assertAlertAutoResolved(t, db, openID, offlineAutoResolveNote)
	assertAlertAutoResolved(t, db, ackID, offlineAutoResolveNote)
	assertAlertUntouched(t, db, siblingID, model.AlertEventStatusOpen)
	assertAlertUntouched(t, db, otherEnvID, model.AlertEventStatusOpen)
}

// TestOfflineAutoResolveIsIdempotent 重复下线不报错、不改写既有处理痕迹；已 resolved 行不参与 UPDATE。
func TestOfflineAutoResolveIsIdempotent(t *testing.T) {
	svc, _, db := newOfflineAlertTestStack(t)
	alertID := seedLifecycleAlert(t, db, "prod", "lobby-1", model.AlertEventStatusOpen)
	resolvedID := seedLifecycleAlert(t, db, "prod", "lobby-1", model.AlertEventStatusResolved)
	siblingID := seedLifecycleAlert(t, db, "prod", "lobby-2", model.AlertEventStatusOpen)

	if err := svc.Offline("prod", "lobby-1", "故障下架", "admin", ""); err != nil {
		t.Fatalf("首次下线应成功: %v", err)
	}
	first := assertAlertAutoResolved(t, db, alertID, offlineAutoResolveNote)

	// 再次下线（同实例重复点击）：Upsert 幂等，告警侧无未处理行可关。
	if err := svc.Offline("prod", "lobby-1", "故障下架（再次确认）", "admin", ""); err != nil {
		t.Fatalf("重复下线不应报错: %v", err)
	}
	second := assertAlertAutoResolved(t, db, alertID, offlineAutoResolveNote)
	if !second.HandledAt.Equal(*first.HandledAt) {
		t.Fatalf("重复下线不得改写已消解告警的处理时刻，first=%v second=%v", first.HandledAt, second.HandledAt)
	}
	// 其它实例的告警在整条链路里始终原样；已 resolved 行不参与 UPDATE（处理痕迹原样保留）。
	assertAlertUntouched(t, db, siblingID, model.AlertEventStatusOpen)
	assertAlertUntouched(t, db, resolvedID, model.AlertEventStatusResolved)
	offs, err := svc.ListOffline("prod")
	if err != nil || len(offs) != 1 || offs[0].ServerID != "lobby-1" {
		t.Fatalf("重复下线后应仍只有一条 lobby-1 下线标记，actual=%+v err=%v", offs, err)
	}
}

// TestOnlineDoesNotAutoResolveAlerts 取消下线（Online）不触发任何消解：它只解除拒绝态，
// 实例此刻仍未上线，其告警必须原样留待实例**真正恢复 online** 时由既有路径处理。
// 用例末尾用真实恢复路径（心跳翻回 online，注入真实 AlertEventService）做对照，
// 证明「Online 不动作」是刻意的语义分离，而非消解链路整体失效。
func TestOnlineDoesNotAutoResolveAlerts(t *testing.T) {
	svc, reg, db := newOfflineAlertTestStack(t)
	seedActiveServer(t, svc, "prod", "lobby-1")
	// 注入真实消解器（`AutoResolveAlerts` → `AutoResolveByServer`，note「实例恢复 online，自动消解」），
	// 使对照步骤断言的是真实落库行为而非假实现。
	svc.SetRecoverySink(NewAlertEventService(db, repository.NewAlertEventRepository(db), repository.NewAuditLogRepository(db)))

	if _, err := svc.Register(regParams("lobby-1")); err != nil {
		t.Fatalf("注册 lobby-1 失败: %v", err)
	}
	if err := svc.Offline("prod", "lobby-1", "", "admin", ""); err != nil {
		t.Fatalf("下线应成功: %v", err)
	}
	// 下线态期间该实例仍可能被触发告警（运维 / 外部系统补录，或下线前的告警在此时才落行）。
	openID := seedLifecycleAlert(t, db, "prod", "lobby-1", model.AlertEventStatusOpen)
	ackID := seedLifecycleAlert(t, db, "prod", "lobby-1", model.AlertEventStatusAcknowledged)

	if err := svc.Online("prod", "lobby-1", "admin", ""); err != nil {
		t.Fatalf("取消下线应成功: %v", err)
	}

	// 核心断言：取消下线后告警状态原样（既没被 resolved，也没有任何处理痕迹）。
	assertAlertUntouched(t, db, openID, model.AlertEventStatusOpen)
	assertAlertUntouched(t, db, ackID, model.AlertEventStatusAcknowledged)

	// 对照：取消下线后实例重新接入（全新注册，非恢复）仍不消解。
	if _, err := svc.Register(regParams("lobby-1")); err != nil {
		t.Fatalf("取消下线后重注册应成功: %v", err)
	}
	if reg.Get("prod", "lobby-1") == nil {
		t.Fatalf("取消下线并重注册后应在册")
	}
	assertAlertUntouched(t, db, openID, model.AlertEventStatusOpen)
	assertAlertUntouched(t, db, ackID, model.AlertEventStatusAcknowledged)

	// 对照：只有实例真正由非 online 翻回 online（心跳续上）才走既有恢复消解。
	reg.SweepExpired(time.Now().UTC().Add(20*time.Second), 15*time.Second, 30*time.Second, 120*time.Second)
	if got := reg.Get("prod", "lobby-1"); got == nil || got.Status == runtime.StatusOnline {
		t.Fatalf("SweepExpired 后应处于非 online，实际 %+v", got)
	}
	if _, err := svc.Heartbeat("prod", "lobby-1"); err != nil {
		t.Fatalf("恢复心跳失败: %v", err)
	}
	assertAlertAutoResolved(t, db, openID, "实例恢复 online，自动消解")
	assertAlertAutoResolved(t, db, ackID, "实例恢复 online，自动消解")
}

// TestOfflineSucceedsWhenAlertResolveFails 告警消解失败不得让下线失败：下线是运维的明确意图，
// 不能被告警表侧的问题阻断（与 maybeAutoResolve 的取舍一致），且失败后下线态本身照常生效。
func TestOfflineSucceedsWhenAlertResolveFails(t *testing.T) {
	svc, reg, db := newOfflineAlertTestStack(t)
	if _, err := svc.Register(regParams("lobby-1")); err != nil {
		t.Fatalf("注册 lobby-1 失败: %v", err)
	}
	alertID := seedLifecycleAlert(t, db, "prod", "lobby-1", model.AlertEventStatusOpen)
	restoreAlertTable := hideAlertTable(t, db) // 消解的 UPDATE 必然报错（表不存在）

	if err := svc.Offline("prod", "lobby-1", "故障下架", "admin", ""); err != nil {
		t.Fatalf("告警消解失败时下线仍应成功: %v", err)
	}
	if reg.Get("prod", "lobby-1") != nil {
		t.Fatalf("下线后应已移出内存可用集")
	}
	offs, err := svc.ListOffline("prod")
	if err != nil || len(offs) != 1 || offs[0].ServerID != "lobby-1" {
		t.Fatalf("下线标记应照常落库，actual=%+v err=%v", offs, err)
	}

	// 表恢复后确认告警行原样（失败是「放弃消解」，不是「乱写一半」）。
	restoreAlertTable()
	assertAlertUntouched(t, db, alertID, model.AlertEventStatusOpen)
}
