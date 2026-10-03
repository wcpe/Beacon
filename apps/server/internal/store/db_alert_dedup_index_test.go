package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// legacyAlertEventDedup 是「恶化链合并」之前的 alert_event 形态（仅供本用例构造既有库）：
// 收敛索引 idx_alert_event_dedup 把方向维度计入列序 (server_id, namespace, to_status, type)。
// 字段与列名同当时的 model.AlertEvent，保证迁移路径与真实升级一致。
type legacyAlertEventDedup struct {
	ID               uint       `gorm:"primaryKey;autoIncrement"`
	Type             string     `gorm:"column:type;size:32;not null;index:idx_alert_event_type,priority:1;index:idx_alert_event_dedup,priority:4"`
	Level            string     `gorm:"column:level;size:16;not null;index:idx_alert_event_level"`
	ServerID         string     `gorm:"column:server_id;size:128;index:idx_alert_event_dedup,priority:1"`
	Namespace        string     `gorm:"column:namespace;size:64;index:idx_alert_event_namespace;index:idx_alert_event_dedup,priority:2"`
	Message          string     `gorm:"column:message;size:512;not null"`
	Detail           string     `gorm:"column:detail;type:text"`
	CreatedAt        time.Time  `gorm:"index:idx_alert_event_time;index:idx_alert_event_type,priority:2"`
	Status           string     `gorm:"column:status;size:16;not null;default:'';index:idx_alert_event_status"`
	HandledBy        string     `gorm:"column:handled_by;size:128;not null;default:''"`
	HandledAt        *time.Time `gorm:"column:handled_at"`
	HandleNote       string     `gorm:"column:handle_note;size:512;not null;default:''"`
	ToStatus         string     `gorm:"column:to_status;size:32;not null;default:'';index:idx_alert_event_dedup,priority:3"`
	OccurrenceCount  int        `gorm:"column:occurrence_count;not null;default:1"`
	LastAt           *time.Time `gorm:"column:last_at"`
	SeverityOverride string     `gorm:"column:severity_override;size:16;not null;default:''"`
	OverriddenBy     string     `gorm:"column:overridden_by;size:128;not null;default:''"`
	OverriddenAt     *time.Time `gorm:"column:overridden_at"`
}

// TableName 复用真实表名，模拟既有库。
func (legacyAlertEventDedup) TableName() string { return "alert_event" }

// alertEventIndexSQL 读回 sqlite 上某表的全部索引 DDL（供断言索引列组成）。
func alertEventIndexSQL(t *testing.T, db *gorm.DB, table string) map[string]string {
	t.Helper()
	var rows []struct {
		Name string
		SQL  string
	}
	if err := db.Raw("SELECT name, COALESCE(sql, '') AS sql FROM sqlite_master WHERE type = 'index' AND tbl_name = ?", table).Scan(&rows).Error; err != nil {
		t.Fatalf("读索引定义失败: %v", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Name] = r.SQL
	}
	return out
}

// TestAlertEventDedupIndexUpgradeKeepsData 守护「恶化链合并」的收敛索引换代在既有库上的升级安全性：
// 旧库的 idx_alert_event_dedup 把方向维度 to_status 计入列序，新收敛键 (server_id, namespace, type)
// 不再含它。本用例在真实文件库上从旧形态升到新形态，断言：
//  1. 新索引 idx_alert_event_dedup_v2 列序为 (server_id, namespace, type)；
//  2. 旧索引被显式清理（GORM AutoMigrate 只增不删、且同名索引不做列比对，实测见下）；
//  3. 一列不删（to_status 保留，历史行仍可读）、一行不丢、字段值原样。
func TestAlertEventDedupIndexUpgradeKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beacon.db")
	open := func() *gorm.DB {
		t.Helper()
		db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
			Logger:  logger.Default.LogMode(logger.Silent),
			NowFunc: func() time.Time { return time.Now().UTC() },
		})
		if err != nil {
			t.Fatalf("打开文件库失败: %v", err)
		}
		return db
	}

	// 步骤 1：建既有库（旧索引 + 两条历史行，含已处理痕迹与方向值）
	db := open()
	if err := db.AutoMigrate(&legacyAlertEventDedup{}); err != nil {
		t.Fatalf("建既有库失败: %v", err)
	}
	legacyIdx := alertEventIndexSQL(t, db, "alert_event")["idx_alert_event_dedup"]
	if legacyIdx == "" {
		t.Fatalf("既有库应有旧收敛索引，实际索引集合 %+v", alertEventIndexSQL(t, db, "alert_event"))
	}
	seed := []struct {
		serverID, toStatus, status, message string
	}{
		{"s1", "lost", model.AlertEventStatusOpen, "s1 online → lost"},
		{"s2", "offline", model.AlertEventStatusAcknowledged, "s2 lost → offline"},
	}
	for _, s := range seed {
		if err := db.Exec(`INSERT INTO alert_event (type, level, server_id, namespace, message, detail, status, to_status, occurrence_count, handled_by, handle_note)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			model.AlertEventTypeHealthTransition, model.AlertLevelWarning, s.serverID, "prod",
			s.message, `{"address":"10.0.0.1:1"}`, s.status, s.toStatus, 3, "alice", "已确认").Error; err != nil {
			t.Fatalf("写历史行失败: %v", err)
		}
	}
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}

	// 步骤 2：重开连接走新模型迁移（与 store.Open 同序）
	db = open()
	if err := db.AutoMigrate(&model.AlertEvent{}); err != nil {
		t.Fatalf("新模型迁移失败: %v", err)
	}
	// GORM AutoMigrate 对索引只增不删，且**同名索引不看列定义直接跳过**——旧索引不会因模型改了
	// 索引名 / 列组成而被重建或移除，这正是必须在 store.Open 显式清理它的实测依据。
	if _, keep := alertEventIndexSQL(t, db, "alert_event")["idx_alert_event_dedup"]; !keep {
		t.Fatal("前提失效：AutoMigrate 竟自行移除了旧收敛索引，可去掉显式清理")
	}
	if !db.Migrator().HasIndex(&model.AlertEvent{}, "idx_alert_event_dedup_v2") {
		t.Fatal("AutoMigrate 应补建新收敛索引 idx_alert_event_dedup_v2")
	}
	newIdx := alertEventIndexSQL(t, db, "alert_event")["idx_alert_event_dedup_v2"]
	if want := "(`server_id`,`namespace`,`type`)"; !strings.Contains(newIdx, want) {
		t.Fatalf("新收敛索引列序应为 %s，实际 %s", want, newIdx)
	}
	if err := dropLegacyAlertDedupIndex(db); err != nil {
		t.Fatalf("清理旧收敛索引失败: %v", err)
	}

	// 步骤 3：断言升级后的库形态
	indexes := alertEventIndexSQL(t, db, "alert_event")
	if _, still := indexes["idx_alert_event_dedup"]; still {
		t.Fatalf("旧收敛索引应被移除，实际索引集合 %+v", indexes)
	}
	if !db.Migrator().HasIndex(&model.AlertEvent{}, "idx_alert_event_dedup_v2") {
		t.Fatal("新收敛索引应保留")
	}
	if !db.Migrator().HasColumn(&model.AlertEvent{}, "to_status") {
		t.Fatal("to_status 列必须保留（不删列，历史方向仍可读）")
	}
	var rows []model.AlertEvent
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatalf("回读历史行失败: %v", err)
	}
	if len(rows) != len(seed) {
		t.Fatalf("升级不得丢数据：应 %d 行，实际 %d", len(seed), len(rows))
	}
	for i, want := range seed {
		got := rows[i]
		if got.ServerID != want.serverID || got.ToStatus != want.toStatus || got.Status != want.status ||
			got.Message != want.message || got.OccurrenceCount != 3 || got.HandledBy != "alice" || got.HandleNote != "已确认" {
			t.Fatalf("第 %d 行升级后被改写：%+v", i+1, got)
		}
	}

	// 步骤 4：幂等——已升级库（或全新库）再调一次不得报错
	if err := dropLegacyAlertDedupIndex(db); err != nil {
		t.Fatalf("二次清理应幂等无错，实际 %v", err)
	}
}

// TestAlertEventDedupIndexFreshInstall 全新库走同一段迁移时不得被旧索引清理误伤：
// 新库只有 idx_alert_event_dedup_v2，清理函数应静默跳过。
func TestAlertEventDedupIndexFreshInstall(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:alertevent_fresh_"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.AlertEvent{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if db.Migrator().HasIndex(&model.AlertEvent{}, "idx_alert_event_dedup") {
		t.Fatal("全新库不应存在旧收敛索引")
	}
	if err := dropLegacyAlertDedupIndex(db); err != nil {
		t.Fatalf("全新库上清理应为无操作，实际 %v", err)
	}
	if !db.Migrator().HasIndex(&model.AlertEvent{}, "idx_alert_event_dedup_v2") {
		t.Fatal("全新库应建出新收敛索引")
	}
}
