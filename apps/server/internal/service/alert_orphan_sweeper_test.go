package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
)

// 本文件覆盖 FR-232 的第三类自动消解触发点：实例被**外部删除**（压测实例用完即删 / 实例直接销毁，控制面
// 收不到任何删除信号）时，其未处理告警自动关闭（prod 实测：压测结束滞留 30+ 条永久 open）。
//
// 四项判据同时成立才关闭，其中判据 3（不在 server 表活动目录）是安全红线：在册的真实实例即使离线很久，
// 其告警也绝不能被自动关闭（运维必须看到）。若哪天有人把判据 3 写漏 / 写宽，本文件会红。

// fakeOrphanSettings 是 alertOrphanSettings 的测试替身（只测阈值读取本身时用）。
type fakeOrphanSettings struct{ hours int }

func (f fakeOrphanSettings) GetInt(string) int { return f.hours }

// newAlertOrphanSuite 装配「真实仓储 + 真实注册表 + 真实设置服务」的清理器（走真 SQL，只替身无必要之物）。
func newAlertOrphanSuite(t *testing.T, timeoutHours string) (*AlertOrphanSweeper, *gorm.DB, *runtime.Registry) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:alert_orphan_"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if sqlDB, e := db.DB(); e == nil {
		// sqlite shared-cache 下避免并发写 "table is locked"。
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.AutoMigrate(&model.Namespace{}, &model.Server{}, &model.AlertEvent{}); err != nil {
		t.Fatalf("迁移告警 / 实例目录测试表失败: %v", err)
	}
	for _, tbl := range []string{"namespace", "server", "alert_event"} {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	settings := settingsWith(t, map[string]string{SettingAlertOrphanTimeoutHours: timeoutHours})
	registry := runtime.NewRegistry()
	sweeper := NewAlertOrphanSweeper(repository.NewAlertOrphanRepository(db), repository.NewAlertEventRepository(db), registry, settings)
	return sweeper, db, registry
}

// seedOrphanAlert 直插一条告警（绕过 Record 的收敛逻辑，便于精确构造 last_at / created_at）。
func seedOrphanAlert(t *testing.T, db *gorm.DB, ns, serverID, status string, lastAt *time.Time) uint {
	t.Helper()
	createdAt := time.Now().UTC().Add(-72 * time.Hour)
	if lastAt != nil {
		createdAt = *lastAt
	}
	e := &model.AlertEvent{
		Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning,
		Namespace: ns, ServerID: serverID, Message: ns + "/" + serverID + " 状态异常",
		Status: status, OccurrenceCount: 1, CreatedAt: createdAt, LastAt: lastAt,
	}
	if err := db.Create(e).Error; err != nil {
		t.Fatalf("落库告警失败: %v", err)
	}
	return e.ID
}

// seedOrphanServerRow 建一条指定生命周期的实例目录行（空生命周期由 model 钩子归一为 active）。
func seedOrphanServerRow(t *testing.T, db *gorm.DB, nsCode, serverID, lifecycle string) {
	t.Helper()
	var ns model.Namespace
	err := db.Where("code = ?", nsCode).First(&ns).Error
	if err == gorm.ErrRecordNotFound {
		ns = model.Namespace{Code: nsCode, Name: nsCode}
		if err := db.Create(&ns).Error; err != nil {
			t.Fatalf("建 namespace 失败: %v", err)
		}
	} else if err != nil {
		t.Fatalf("查 namespace 失败: %v", err)
	}
	row := model.Server{NamespaceID: ns.ID, ServerID: serverID, Kind: model.ServerKindBackend, Lifecycle: lifecycle}
	if lifecycle == "" {
		row.Lifecycle = model.ServerLifecycleActive
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("建 server 目录行失败: %v", err)
	}
}

// TestAlertOrphanSweeperResolvesAlertsOfExternallyDeletedInstance 判据 1+2+3+4 全真：
// 有未处理告警、不在运行时注册表、不在 server 表活动目录、最近触发已超阈值 → 自动消解。
// **只消解 open 行**：同一实例上人工已确认（acknowledged）的行保持原状、处置痕迹原样保留
// （语义变更说明见该用例内注释与 repository.AutoResolveOpenByServer）。
func TestAlertOrphanSweeperResolvesAlertsOfExternallyDeletedInstance(t *testing.T) {
	sweeper, db, _ := newAlertOrphanSuite(t, "24")
	now := time.Now().UTC()
	stale := now.Add(-30 * time.Hour)
	openID := seedOrphanAlert(t, db, "prod", "stress-1", model.AlertEventStatusOpen, &stale)
	ackID := seedOrphanAlert(t, db, "prod", "stress-1", model.AlertEventStatusAcknowledged, &stale)
	// 人工已确认的行带处置痕迹：自动消解若把它一并关掉，会把 handled_by / handle_note 覆盖成 system + 固定文案，
	// 事后无法再从行上区分「人工已处理」与「系统自动消解」（本表无历史表、自动消解不逐条写审计）。
	ackedAt := stale.Add(time.Hour)
	if err := db.Model(&model.AlertEvent{}).Where("id = ?", ackID).
		Updates(map[string]any{"handled_by": "ops-a", "handled_at": ackedAt, "handle_note": "正在排查，磁盘告警"}).Error; err != nil {
		t.Fatalf("预置人工已确认行失败: %v", err)
	}
	siblingID := seedOrphanAlert(t, db, "prod", "game-1", model.AlertEventStatusOpen, &stale)
	// 兄弟实例在册（active）→ 必须保持 open，见下一条用例的红线断言（此处同时防 UPDATE 打宽）。

	seedOrphanServerRow(t, db, "prod", "game-1", model.ServerLifecycleActive)

	n := sweeper.sweepOnce(now)
	if n != 1 {
		t.Fatalf("应只消解 1 条（open），实际 %d", n)
	}
	assertAlertAutoResolved(t, db, openID, alertOrphanResolveNote)
	assertAlertUntouched(t, db, siblingID, model.AlertEventStatusOpen)
	// acknowledged 行：状态与人工痕迹一字未改（这正是本次语义变更要守住的东西）
	var acked model.AlertEvent
	if err := db.First(&acked, ackID).Error; err != nil {
		t.Fatalf("回读人工已确认行失败: %v", err)
	}
	if acked.Status != model.AlertEventStatusAcknowledged || acked.HandledBy != "ops-a" ||
		acked.HandleNote != "正在排查，磁盘告警" || acked.HandledAt == nil || !acked.HandledAt.Equal(ackedAt) {
		t.Fatalf("人工已确认行不得被自动消解覆盖: %+v", acked)
	}
}

// TestAlertOrphanSweeperMatchesActiveServerDespiteNamespaceCase 判据 3 的键比对必须容忍大小写 / 首尾空白差异：
// 候选侧 namespace 来自 agent 上报值（可能是 `PROD` / ` prod `），在册侧的键来自 `namespace.code`（`prod`）。
// 若两侧逐字节比对，在册实例会**漏配** → 判据 3 被误判为「不在册」→ 安全红线被绕过、在册实例的告警被误关。
func TestAlertOrphanSweeperMatchesActiveServerDespiteNamespaceCase(t *testing.T) {
	sweeper, db, _ := newAlertOrphanSuite(t, "24")
	now := time.Now().UTC()
	stale := now.Add(-30 * time.Hour)
	seedOrphanServerRow(t, db, "prod", "lobby-1", model.ServerLifecycleActive)
	// 在册实例是 prod/lobby-1，而告警行的 namespace / serverId 写成大小写与空白有差异的形态。
	upperID := seedOrphanAlert(t, db, "PROD", "lobby-1", model.AlertEventStatusOpen, &stale)
	spaceID := seedOrphanAlert(t, db, " prod ", "LOBBY-1", model.AlertEventStatusOpen, &stale)

	if n := sweeper.sweepOnce(now); n != 0 {
		t.Fatalf("在册实例（键仅大小写 / 空白不同）的告警绝不能被自动关闭（安全红线），实际消解 %d 条", n)
	}
	assertAlertUntouched(t, db, upperID, model.AlertEventStatusOpen)
	assertAlertUntouched(t, db, spaceID, model.AlertEventStatusOpen)
}

// TestAlertOrphanSweeperKeepsAlertsWithinTimeout 判据 4 不成立：最近触发在阈值内 → 保持 open
// （给「外部删除后又被重新纳管」留观察窗，不在当天就抹掉运维可见的告警）。
func TestAlertOrphanSweeperKeepsAlertsWithinTimeout(t *testing.T) {
	sweeper, db, _ := newAlertOrphanSuite(t, "24")
	now := time.Now().UTC()
	fresh := now.Add(-1 * time.Hour)
	id := seedOrphanAlert(t, db, "prod", "stress-2", model.AlertEventStatusOpen, &fresh)

	if n := sweeper.sweepOnce(now); n != 0 {
		t.Fatalf("未超阈值不应消解任何告警，实际 %d", n)
	}
	assertAlertUntouched(t, db, id, model.AlertEventStatusOpen)
}

// TestAlertOrphanSweeperKeepsAlertsOfServerStillInDirectory 安全红线（判据 3）：实例仍在 server 表活动目录
// —— 即在册的真实实例 —— 即使它不在运行时注册表、告警最近触发远超阈值，也**绝不能**被自动关闭（运维必须看到）。
func TestAlertOrphanSweeperKeepsAlertsOfServerStillInDirectory(t *testing.T) {
	sweeper, db, _ := newAlertOrphanSuite(t, "24")
	now := time.Now().UTC()
	stale := now.Add(-90 * 24 * time.Hour) // 远超阈值：三个月前的老告警
	id := seedOrphanAlert(t, db, "prod", "game-1", model.AlertEventStatusOpen, &stale)
	seedOrphanServerRow(t, db, "prod", "game-1", model.ServerLifecycleActive)

	if n := sweeper.sweepOnce(now); n != 0 {
		t.Fatalf("在册实例的告警绝不能被自动关闭（安全红线），实际消解 %d 条", n)
	}
	assertAlertUntouched(t, db, id, model.AlertEventStatusOpen)
}

// TestAlertOrphanSweeperResolvesAlertsOfArchivedServerRow 判据 3 的口径：只有 lifecycle=active 才算「在册」。
// 已归档 / 墓碑行不算在册（这类实例的告警本应由既有生命周期自动消解覆盖，若仍有残留未处理行，
// 本清理器负责兜底关闭），否则墓碑行会让告警永久滞留。
func TestAlertOrphanSweeperResolvesAlertsOfArchivedServerRow(t *testing.T) {
	sweeper, db, _ := newAlertOrphanSuite(t, "24")
	now := time.Now().UTC()
	stale := now.Add(-30 * time.Hour)
	id := seedOrphanAlert(t, db, "prod", "game-gone", model.AlertEventStatusOpen, &stale)
	seedOrphanServerRow(t, db, "prod", "game-gone", model.ServerLifecycleArchived)

	if n := sweeper.sweepOnce(now); n != 1 {
		t.Fatalf("已归档实例不属于在册目录，其残留告警应被自动消解，实际 %d", n)
	}
	assertAlertAutoResolved(t, db, id, alertOrphanResolveNote)
}

// TestAlertOrphanSweeperKeepsAlertsOfRuntimeRegisteredInstance 判据 2 不成立：实例仍在运行时注册表
// （含刚断线的 lost / offline 登记）→ 不能视作外部消失，保持 open。
func TestAlertOrphanSweeperKeepsAlertsOfRuntimeRegisteredInstance(t *testing.T) {
	sweeper, db, registry := newAlertOrphanSuite(t, "24")
	now := time.Now().UTC()
	stale := now.Add(-30 * time.Hour)
	id := seedOrphanAlert(t, db, "prod", "live-1", model.AlertEventStatusOpen, &stale)
	if _, err := registry.Register(&runtime.Instance{
		Namespace: "prod", ServerID: "live-1", Role: "bukkit", Address: "10.0.0.9:25565",
	}, 30*time.Second, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("注册实例失败: %v", err)
	}

	if n := sweeper.sweepOnce(now); n != 0 {
		t.Fatalf("仍在运行时注册表的实例其告警不应被消解，实际 %d", n)
	}
	assertAlertUntouched(t, db, id, model.AlertEventStatusOpen)
}

// TestAlertOrphanSweeperKeepsNonInstanceAlerts 非实例维度告警（server_id 为空，如集群级事件）
// 不代表任何实例失联，不属于本清理器职责（判据 1 的 (namespace, serverId) 恒指实例）。
func TestAlertOrphanSweeperKeepsNonInstanceAlerts(t *testing.T) {
	sweeper, db, _ := newAlertOrphanSuite(t, "24")
	now := time.Now().UTC()
	stale := now.Add(-30 * time.Hour)
	id := seedOrphanAlert(t, db, "prod", "", model.AlertEventStatusOpen, &stale)

	if n := sweeper.sweepOnce(now); n != 0 {
		t.Fatalf("非实例维度告警不应被本清理器消解，实际 %d", n)
	}
	assertAlertUntouched(t, db, id, model.AlertEventStatusOpen)
}

// TestAlertOrphanSweeperIsIdempotent 幂等：第二轮无可关行（affected=0），且不改写已消解行的处理时刻。
func TestAlertOrphanSweeperIsIdempotent(t *testing.T) {
	sweeper, db, _ := newAlertOrphanSuite(t, "24")
	now := time.Now().UTC()
	stale := now.Add(-30 * time.Hour)
	id := seedOrphanAlert(t, db, "prod", "stress-3", model.AlertEventStatusOpen, &stale)

	if n := sweeper.sweepOnce(now); n != 1 {
		t.Fatalf("首轮应消解 1 条，实际 %d", n)
	}
	first := assertAlertAutoResolved(t, db, id, alertOrphanResolveNote)

	if n := sweeper.sweepOnce(now.Add(time.Minute)); n != 0 {
		t.Fatalf("重复扫描应无待关行（affected=0），实际 %d", n)
	}
	var second model.AlertEvent
	if err := db.First(&second, id).Error; err != nil {
		t.Fatalf("回读告警 %d 失败: %v", id, err)
	}
	if second.HandledAt == nil || !second.HandledAt.Equal(*first.HandledAt) {
		t.Fatalf("重复扫描不得改写已消解行的处理时刻，first=%v second=%+v", first.HandledAt, second)
	}
}

// TestAlertOrphanTimeoutSettingRegistered 阈值设置键四项同步（缺一即 fail-open）：
// 键常量字面量、白名单元数据（int / 下限 1 / 上限 8760）、默认 24、且登记为高影响设置（改动走审批 + 审计）。
func TestAlertOrphanTimeoutSettingRegistered(t *testing.T) {
	const key = SettingAlertOrphanTimeoutHours
	if key != "alert.orphan-timeout-hours" {
		t.Fatalf("阈值键字面量不符: %q", key)
	}
	meta, ok := settingMetaFor(key)
	if !ok {
		t.Fatalf("阈值键未登记进 settingsWhitelist（写该键会被拒）")
	}
	if meta.valueType != model.SettingValueTypeInt || meta.min != 1 || meta.max != 8760 {
		t.Fatalf("阈值键应为 int 且上下界 [1,8760]，实际 type=%q min=%d max=%d", meta.valueType, meta.min, meta.max)
	}
	if got := meta.defaultFromConfig(config.Default()); got != "24" {
		t.Fatalf("阈值默认值应为 24 小时，实际 %q", got)
	}
	if !SettingDangerous(key) {
		t.Fatalf("%s 未登记进 dangerousSettingKeys —— 阈值改动会绕过危险设置审批流程", key)
	}
}

// TestAlertOrphanSweeperFallsBackToDefaultTimeout 阈值缺省 / 非法（≤0）时回退默认 24 小时：
// 否则「已超阈值」判据恒真，会把刚产生的告警当轮就自动关掉。
func TestAlertOrphanSweeperFallsBackToDefaultTimeout(t *testing.T) {
	for _, hours := range []int{0, -1} {
		sweeper := NewAlertOrphanSweeper(nil, nil, nil, fakeOrphanSettings{hours: hours})
		if got := sweeper.orphanTimeoutHours(); got != alertOrphanTimeoutDefaultHours {
			t.Fatalf("阈值为 %d 时应回退默认 %d 小时，实际 %d", hours, alertOrphanTimeoutDefaultHours, got)
		}
	}
	sweeper := NewAlertOrphanSweeper(nil, nil, nil, fakeOrphanSettings{hours: 6})
	if got := sweeper.orphanTimeoutHours(); got != 6 {
		t.Fatalf("阈值 6 应原样生效（热生效按每轮读取），实际 %d", got)
	}
}
