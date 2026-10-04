package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// 本文件锁定「失联孤儿告警清理」两条查询的契约：候选聚合取每个实例的最近触发时刻且排除已解决 / 非实例行；
// 在册目录只认 lifecycle=active（安全红线：在册实例的告警不得被自动关闭，见 service/alert_orphan_sweeper.go）。

// newAlertOrphanTestDB 打开私有内存 sqlite 并迁移相关表（不依赖 MySQL/DSN）。
func newAlertOrphanTestDB(t *testing.T) (*gorm.DB, *AlertOrphanRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:alertorphan_"+t.Name()+"?mode=memory"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Namespace{}, &model.Server{}, &model.AlertEvent{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	return db, NewAlertOrphanRepository(db)
}

// TestAlertOrphanRepositoryListUnresolvedByServer 候选聚合：按 (namespace, serverId) 各一行，
// 最近触发时刻取各行 MAX(COALESCE(last_at, created_at))；已 resolved / serverId 为空的非实例行都不返回。
func TestAlertOrphanRepositoryListUnresolvedByServer(t *testing.T) {
	db, repo := newAlertOrphanTestDB(t)
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	older := base
	newer := base.Add(2 * time.Hour)

	seed := func(ns, serverID, status string, createdAt time.Time, lastAt *time.Time) {
		t.Helper()
		if err := db.Create(&model.AlertEvent{
			Type: model.AlertEventTypeHealthTransition, Level: model.AlertLevelWarning,
			Namespace: ns, ServerID: serverID, Message: ns + "/" + serverID, Status: status,
			OccurrenceCount: 1, CreatedAt: createdAt, LastAt: lastAt,
		}).Error; err != nil {
			t.Fatalf("写告警失败: %v", err)
		}
	}
	seed("prod", "s1", model.AlertEventStatusOpen, older, &older)
	seed("prod", "s1", model.AlertEventStatusAcknowledged, older, &newer) // 合并行更晚 → 取它
	seed("prod", "s2", model.AlertEventStatusOpen, older, nil)            // last_at 空 → 用 created_at
	seed("prod", "s3", model.AlertEventStatusResolved, older, &older)     // 已解决 → 不返回
	seed("prod", "", model.AlertEventStatusOpen, older, &older)           // 非实例维度 → 不返回
	seed("dev", "s1", model.AlertEventStatusOpen, older, &older)          // 同名的另一环境 → 独立一行

	got, err := repo.ListUnresolvedByServer()
	if err != nil {
		t.Fatalf("查询未处理告警候选失败: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("应返回 3 个实例候选（prod/s1、prod/s2、dev/s1），实际 %d：%+v", len(got), got)
	}
	byKey := make(map[AlertServerKey]time.Time, len(got))
	for _, c := range got {
		byKey[AlertServerKey{Namespace: c.Namespace, ServerID: c.ServerID}] = c.LastAt
	}
	if at, ok := byKey[AlertServerKey{Namespace: "prod", ServerID: "s1"}]; !ok || !at.Equal(newer) {
		t.Fatalf("prod/s1 应取最晚一条的 last_at=%v，实际 %v（ok=%v）", newer, at, ok)
	}
	if at, ok := byKey[AlertServerKey{Namespace: "prod", ServerID: "s2"}]; !ok || !at.Equal(older) {
		t.Fatalf("prod/s2 的 last_at 为空应回退 created_at=%v，实际 %v（ok=%v）", older, at, ok)
	}
	if at, ok := byKey[AlertServerKey{Namespace: "dev", ServerID: "s1"}]; !ok || !at.Equal(older) {
		t.Fatalf("dev/s1 应与 prod/s1 各自一行，实际 %v（ok=%v）", at, ok)
	}
}

// TestAlertOrphanRepositoryActiveServerKeysOnlyActive 在册目录只认 lifecycle=active：
// archived / tombstoned 行不算在册（其告警由既有生命周期自动消解覆盖），且键用 namespace code 而非主键 id。
func TestAlertOrphanRepositoryActiveServerKeysOnlyActive(t *testing.T) {
	db, repo := newAlertOrphanTestDB(t)
	ns := model.Namespace{Code: "prod", Name: "生产"}
	if err := db.Create(&ns).Error; err != nil {
		t.Fatalf("建 namespace 失败: %v", err)
	}
	dev := model.Namespace{Code: "dev", Name: "开发"}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("建 namespace 失败: %v", err)
	}
	for _, row := range []model.Server{
		{NamespaceID: ns.ID, ServerID: "game-1", Kind: model.ServerKindBackend, Lifecycle: model.ServerLifecycleActive},
		{NamespaceID: ns.ID, ServerID: "game-2", Kind: model.ServerKindBackend, Lifecycle: model.ServerLifecycleArchived},
		{NamespaceID: ns.ID, ServerID: "game-3", Kind: model.ServerKindBackend, Lifecycle: model.ServerLifecycleTombstoned},
		{NamespaceID: dev.ID, ServerID: "game-1", Kind: model.ServerKindBackend, Lifecycle: model.ServerLifecycleActive},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("建 server 目录行失败: %v", err)
		}
	}

	keys, err := repo.ActiveServerKeys()
	if err != nil {
		t.Fatalf("查询在册实例键失败: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("应只有 2 个 active 在册键，实际 %d：%+v", len(keys), keys)
	}
	if _, ok := keys[AlertServerKey{Namespace: "prod", ServerID: "game-1"}]; !ok {
		t.Fatalf("prod/game-1（active）应算在册：%+v", keys)
	}
	if _, ok := keys[AlertServerKey{Namespace: "dev", ServerID: "game-1"}]; !ok {
		t.Fatalf("dev/game-1（active）应算在册（键用 namespace code）：%+v", keys)
	}
	for _, gone := range []AlertServerKey{
		{Namespace: "prod", ServerID: "game-2"}, // archived
		{Namespace: "prod", ServerID: "game-3"}, // tombstoned
	} {
		if _, ok := keys[gone]; ok {
			t.Fatalf("%+v 非 active，不应算在册（否则其告警会被永久保护）：%+v", gone, keys)
		}
	}
}
