package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/merge"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
	"github.com/wcpe/Beacon/apps/server/internal/runtime"
	"github.com/wcpe/Beacon/apps/server/internal/secret"
)

// newPlacementTestDB 建一个含区服归属真源（namespace / bc_cluster / region / zone / server）
// 与配置链（config_item）表的内存 sqlite，供归属迁移用例使用。每个测试用独立库名避免互相污染。
func newPlacementTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Namespace{}, &model.BCCluster{}, &model.Region{}, &model.Zone{}, &model.Server{},
		&model.ConfigItem{}, &model.ServerOffline{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// TestRegisterResolvesPlacementFromServerZone 实例注册读新真源：
// server.zone_id 指向 zoneA（大区 area1）时，注册回填 ResolvedGroup=大区 code、ResolvedZone=小区 code、Assigned=true。
func TestRegisterResolvesPlacementFromServerZone(t *testing.T) {
	db := newPlacementTestDB(t)
	seedServerPlacement(t, db, "prod", "lobby-1", "area1", "zoneA")
	reg := runtime.NewRegistry()
	svc := NewInstanceService(db, reg, repository.NewServerPlacementRepository(db),
		repository.NewServerOfflineRepository(db), repository.NewAuditLogRepository(db), 10*time.Second, 30*time.Second)

	res, err := svc.Register(RegisterParams{
		Namespace: "prod", ServerID: "lobby-1", Role: "bukkit", GroupHint: "hint-should-be-ignored",
		Address: "10.0.0.1:25565", ClientIP: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	if !res.Assigned || res.ResolvedGroup != "area1" || res.ResolvedZone != "zoneA" {
		t.Fatalf("应按 server.zone_id 回填 area1/zoneA 且 Assigned=true，实际 %+v", res)
	}
	got := reg.Get("prod", "lobby-1")
	if got == nil || !got.Assigned || got.ResolvedGroup != "area1" || got.ResolvedZone != "zoneA" {
		t.Fatalf("内存注册表应记录权威归属，实际 %+v", got)
	}
}

// TestRegisterFallsBackToGroupHintWithoutPlacement 无区服归属时保持迁移前语义：
// group 退回 agent 提示的 groupHint、zone 为空、Assigned=false。
// 覆盖三种「无归属」：server 行无任何归属 / 仅分到 BC 集群（代理）/ server 行不存在。
func TestRegisterFallsBackToGroupHintWithoutPlacement(t *testing.T) {
	cases := []struct {
		name    string
		seed    func(db *gorm.DB)
		server  string
		groupIn string
	}{
		{
			name:    "server 行无任何归属",
			seed:    func(db *gorm.DB) { seedServerRow(t, db, "prod", "plain-1") },
			server:  "plain-1",
			groupIn: "hintA",
		},
		{
			name:    "仅分到 BC 集群的代理",
			seed:    func(db *gorm.DB) { seedBCClusterOnlyPlacement(t, db, "prod", "bc-1") },
			server:  "bc-1",
			groupIn: "hintB",
		},
		{
			name:    "server 行不存在",
			seed:    func(*gorm.DB) {},
			server:  "ghost-1",
			groupIn: "hintC",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newPlacementTestDB(t)
			tc.seed(db)
			reg := runtime.NewRegistry()
			svc := NewInstanceService(db, reg, repository.NewServerPlacementRepository(db),
				repository.NewServerOfflineRepository(db), repository.NewAuditLogRepository(db), 10*time.Second, 30*time.Second)

			res, err := svc.Register(RegisterParams{
				Namespace: "prod", ServerID: tc.server, Role: "bukkit", GroupHint: tc.groupIn,
				Address: "10.0.0.9:25565", ClientIP: "127.0.0.1",
			})
			if err != nil {
				t.Fatalf("注册失败: %v", err)
			}
			if res.Assigned || res.ResolvedGroup != tc.groupIn || res.ResolvedZone != "" {
				t.Fatalf("无归属应退回 groupHint 且 zone 为空、Assigned=false，实际 %+v", res)
			}
		})
	}
}

// TestEffectiveResolveUsesServerZonePlacement 配置生效解析读新真源：
// server.zone_id 指向 zoneA 时按 zone 生效的配置参与合并，而 groupHint 传入的值不再被采信。
func TestEffectiveResolveUsesServerZonePlacement(t *testing.T) {
	db := newPlacementTestDB(t)
	seedServerPlacement(t, db, "prod", "lobby-1", "area1", "zoneA")
	cipher, _ := secret.NewCipher("")
	cfgRepo := repository.NewConfigItemRepository(db, cipher)
	eff := NewEffectiveService(cfgRepo, repository.NewServerPlacementRepository(db), nil, nil, nil)

	create := func(group, scope, target, content string) {
		t.Helper()
		if err := cfgRepo.Create(&model.ConfigItem{
			NamespaceCode: "prod", GroupCode: group, DataID: "app.yml",
			ScopeLevel: scope, ScopeTarget: target, Format: merge.FormatYAML,
			Content: content, ContentMD5: merge.MD5Hex(content), Version: 1, Enabled: true,
		}); err != nil {
			t.Fatalf("建 %s 层失败: %v", scope, err)
		}
	}
	create(model.GlobalGroupCode, model.ScopeGlobal, "", "base: 1\n")
	create("area1", model.ScopeGroup, "", "pool: 2\n")
	create("area1", model.ScopeZone, "zoneA", "zoneval: \"A\"\n")

	// 归隶属 server 表真源：即使 groupHint 传入别的值，也应按 area1/zoneA 解析
	res, err := eff.Resolve("prod", "lobby-1", "hint-should-be-ignored")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if res.Group != "area1" || res.Zone != "zoneA" {
		t.Fatalf("应按新真源解析出 area1/zoneA，实际 group=%s zone=%s", res.Group, res.Zone)
	}
	merged := map[string]any{}
	for _, item := range res.Items {
		parsed, err := merge.Parse(merge.FormatYAML, item.Content)
		if err != nil {
			t.Fatalf("解析合并内容失败: %v", err)
		}
		for k, v := range parsed.(map[string]any) {
			merged[k] = v
		}
	}
	if merged["base"] != float64(1) && merged["base"] != 1 {
		t.Fatalf("global 层应参与合并，实际 %+v", merged)
	}
	if merged["pool"] != float64(2) && merged["pool"] != 2 {
		t.Fatalf("集团层应参与合并，实际 %+v", merged)
	}
	if merged["zoneval"] != "A" {
		t.Fatalf("按 zone 生效的配置应由新真源归属命中，实际 %+v", merged)
	}
}

// TestEffectiveResolveZoneScopedConfigMissesWithoutPlacement 无区服归属时 zone 层不参与合并
// （保持迁移前「未指派」语义：group=groupHint、zone 为空）。
func TestEffectiveResolveZoneScopedConfigMissesWithoutPlacement(t *testing.T) {
	db := newPlacementTestDB(t)
	seedServerRow(t, db, "prod", "plain-1")
	cipher, _ := secret.NewCipher("")
	cfgRepo := repository.NewConfigItemRepository(db, cipher)
	eff := NewEffectiveService(cfgRepo, repository.NewServerPlacementRepository(db), nil, nil, nil)

	for _, item := range []*model.ConfigItem{
		{NamespaceCode: "prod", GroupCode: model.GlobalGroupCode, DataID: "app.yml", ScopeLevel: model.ScopeGlobal, Format: merge.FormatYAML, Content: "base: 1\n", Version: 1, Enabled: true},
		{NamespaceCode: "prod", GroupCode: "area1", DataID: "app.yml", ScopeLevel: model.ScopeZone, ScopeTarget: "zoneA", Format: merge.FormatYAML, Content: "zoneval: \"A\"\n", Version: 1, Enabled: true},
	} {
		if err := cfgRepo.Create(item); err != nil {
			t.Fatalf("建配置项失败: %v", err)
		}
	}

	res, err := eff.Resolve("prod", "plain-1", "hintA")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if res.Group != "hintA" || res.Zone != "" {
		t.Fatalf("无归属应退回 groupHint 且 zone 为空，实际 group=%s zone=%s", res.Group, res.Zone)
	}
	if len(res.Items) != 1 || res.Items[0].Content != "base: 1\n" {
		t.Fatalf("zone 层不应参与合并，实际 %+v", res.Items)
	}
}
