package service

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/internal/merge"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/secret"
)

// newTimelineTestStack 装配变更时间线测试栈（内存 sqlite 存 config_item / config_revision / zone_assignment），
// 不依赖 MySQL；复用 ConfigService 走真实发布路径以产生 config_revision 历史。
func newTimelineTestStack(t *testing.T) (*ConfigService, *EffectiveService, *repository.ZoneAssignmentRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.ConfigItem{}, &model.ConfigRevision{}, &model.ZoneAssignment{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	for _, tbl := range []string{"config_item", "config_revision", "zone_assignment", "audit_log"} {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	cipher, _ := secret.NewCipher("")
	cr := repository.NewConfigItemRepository(db, cipher)
	rr := repository.NewConfigRevisionRepository(db, cipher)
	ar := repository.NewAuditLogRepository(db)
	asg := repository.NewZoneAssignmentRepository(db)
	cfg := NewConfigService(db, cr, rr, ar)
	eff := NewEffectiveService(cr, asg, nil, rr, nil)
	return cfg, eff, asg
}

// mkConfig 在指定层建一条配置项（首发 version=1）。
func mkConfig(t *testing.T, cfg *ConfigService, group, scope, target, dataID, content, operator string) *model.ConfigItem {
	t.Helper()
	it, err := cfg.Create(CreateConfigParams{
		Namespace: "prod", Group: group, DataID: dataID,
		ScopeLevel: scope, ScopeTarget: target, Format: merge.FormatYAML,
		Content: content, Operator: operator,
	})
	if err != nil {
		t.Fatalf("建 %s 层 %s 失败: %v", scope, dataID, err)
	}
	return it
}

// TestConfigTimelineMultiLayerSortedDesc 四层均有发布时，时间线汇总全部版本且按时间倒序，且各条标注其 scope。
func TestConfigTimelineMultiLayerSortedDesc(t *testing.T) {
	cfg, eff, asg := newTimelineTestStack(t)
	// 指派 lobby-1 → area1/zoneA，使其覆盖链含全部四层
	if _, err := asg.Upsert("prod", "lobby-1", "area1", "zoneA", ""); err != nil {
		t.Fatalf("指派失败: %v", err)
	}

	g := mkConfig(t, cfg, model.GlobalGroupCode, model.ScopeGlobal, "", "mysql.yml", "pool: 1\n", "alice")
	mkConfig(t, cfg, "area1", model.ScopeGroup, "", "mysql.yml", "pool: 2\n", "bob")
	mkConfig(t, cfg, "area1", model.ScopeZone, "zoneA", "mysql.yml", "nest:\n  a: 1\n", "carol")
	mkConfig(t, cfg, "area1", model.ScopeServer, "lobby-1", "mysql.yml", "extra: y\n", "dave")
	// global 层再发一版，制造该项两条历史
	if _, err := cfg.Publish(g.ID, "pool: 9\n", "eve", "调大池", ""); err != nil {
		t.Fatalf("发布失败: %v", err)
	}

	tl, err := eff.ConfigTimeline("prod", "lobby-1", "")
	if err != nil {
		t.Fatalf("解析时间线失败: %v", err)
	}
	if tl.Group != "area1" || tl.Zone != "zoneA" {
		t.Fatalf("时间线归属元数据错误：group=%s zone=%s", tl.Group, tl.Zone)
	}
	// 四层各 1 首发 + global 多 1 次发布 = 5 条
	if len(tl.Entries) != 5 {
		t.Fatalf("时间线应含 5 条版本，实际 %d", len(tl.Entries))
	}
	// 倒序：CreatedAt 非递增
	for i := 1; i < len(tl.Entries); i++ {
		if tl.Entries[i-1].CreatedAt.Before(tl.Entries[i].CreatedAt) {
			t.Fatalf("时间线未按时间倒序：第 %d 条早于第 %d 条", i-1, i)
		}
	}
	// 每条都带 scope 标注且非空 dataId
	for _, e := range tl.Entries {
		if e.ScopeLevel == "" || e.DataID != "mysql.yml" {
			t.Fatalf("时间线条目缺 scope/dataId：%+v", e)
		}
	}
	// global 项应有两个版本号（1、2）
	var globalVersions []int64
	for _, e := range tl.Entries {
		if e.ConfigItemID == g.ID {
			globalVersions = append(globalVersions, e.Version)
		}
	}
	if len(globalVersions) != 2 {
		t.Fatalf("global 项应有 2 个版本，实际 %v", globalVersions)
	}
}

// TestConfigTimelineUnassignedOnlyGlobal 未指派的 server 只解析到 global 层，时间线仅含 global 项历史。
func TestConfigTimelineUnassignedOnlyGlobal(t *testing.T) {
	cfg, eff, _ := newTimelineTestStack(t)
	g := mkConfig(t, cfg, model.GlobalGroupCode, model.ScopeGlobal, "", "mysql.yml", "pool: 1\n", "alice")
	// area1 层存在，但未指派的 ghost 不应解析到 area1
	mkConfig(t, cfg, "area1", model.ScopeServer, "lobby-1", "mysql.yml", "extra: y\n", "dave")

	tl, err := eff.ConfigTimeline("prod", "ghost-9", "")
	if err != nil {
		t.Fatalf("解析时间线失败: %v", err)
	}
	if len(tl.Entries) != 1 || tl.Entries[0].ConfigItemID != g.ID {
		t.Fatalf("未指派 server 时间线应只含 global 项，实际 %+v", tl.Entries)
	}
}

// TestConfigTimelineEmptyWhenNoConfig 无任何覆盖候选时返回空时间线（不报错）。
func TestConfigTimelineEmptyWhenNoConfig(t *testing.T) {
	_, eff, _ := newTimelineTestStack(t)
	tl, err := eff.ConfigTimeline("prod", "ghost-9", "")
	if err != nil {
		t.Fatalf("解析时间线失败: %v", err)
	}
	if len(tl.Entries) != 0 {
		t.Fatalf("无配置应返回空时间线，实际 %d 条", len(tl.Entries))
	}
}

// TestConfigTimelineIncludesRollback 回滚也是一次发布，应出现在时间线内。
func TestConfigTimelineIncludesRollback(t *testing.T) {
	cfg, eff, _ := newTimelineTestStack(t)
	g := mkConfig(t, cfg, model.GlobalGroupCode, model.ScopeGlobal, "", "mysql.yml", "pool: 1\n", "alice")
	if _, err := cfg.Publish(g.ID, "pool: 2\n", "bob", "v2", ""); err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	if _, err := cfg.Rollback(g.ID, 1, "carol", "回退到 v1", ""); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}

	tl, err := eff.ConfigTimeline("prod", "ghost-9", "")
	if err != nil {
		t.Fatalf("解析时间线失败: %v", err)
	}
	// 首发 + 发布 + 回滚 = 3 条版本
	if len(tl.Entries) != 3 {
		t.Fatalf("含回滚应有 3 条版本，实际 %d", len(tl.Entries))
	}
	// 最新一条（倒序第一）应是回滚产生的 v3
	if tl.Entries[0].Version != 3 {
		t.Fatalf("最新版本应为回滚产生的 v3，实际 v%d", tl.Entries[0].Version)
	}
}
