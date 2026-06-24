//go:build integration

package service_test

import (
	"errors"
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/merge"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime"
	"github.com/wcpe/Beacon/internal/service"
)

// TestZoneReassignEffectiveRecompute 集成验证：改派后有效配置重算正确。
func TestZoneReassignEffectiveRecompute(t *testing.T) {
	db := testDB(t)
	cr := repository.NewConfigItemRepository(db, noEncryptCipher())
	ar := repository.NewAuditLogRepository(db)
	asg := repository.NewZoneAssignmentRepository(db)
	cfg := service.NewConfigService(db, cr, repository.NewConfigRevisionRepository(db, noEncryptCipher()), ar)
	eff := service.NewEffectiveService(cr, asg, nil, nil, nil)
	zone := service.NewZoneService(db, asg, repository.NewZoneDefaultEntryRepository(db), ar, runtime.NewRegistry())

	create := func(group, scope, target, content string) {
		if _, err := cfg.Create(service.CreateConfigParams{
			Namespace: "prod", Group: group, DataID: "app.yml",
			ScopeLevel: scope, ScopeTarget: target, Format: merge.FormatYAML,
			Content: content, Operator: "admin",
		}); err != nil {
			t.Fatalf("建 %s 层失败: %v", scope, err)
		}
	}
	create(model.GlobalGroupCode, model.ScopeGlobal, "", "base: 1\n")
	create("area1", model.ScopeZone, "zoneA", "zoneval: \"A\"\n")
	create("area1", model.ScopeZone, "zoneB", "zoneval: \"B\"\n")

	zoneval := func(serverID string) (string, string) {
		res, err := eff.Resolve("prod", serverID, "")
		if err != nil || len(res.Items) != 1 {
			t.Fatalf("解析失败或 items 数错误: err=%v items=%d", err, len(res.Items))
		}
		parsed, _ := merge.Parse(merge.FormatYAML, res.Items[0].Content)
		return parsed.(map[string]any)["zoneval"].(string), res.MD5
	}

	// 指派 zoneA → 含 A
	if _, err := zone.Assign("prod", "lobby-1", "area1", "zoneA", "admin", "", ""); err != nil {
		t.Fatalf("指派失败: %v", err)
	}
	valA, md5A := zoneval("lobby-1")
	if valA != "A" {
		t.Fatalf("指派 zoneA 后应解析出 A，实际 %s", valA)
	}

	// 改派 zoneB → 重算为 B，且整体 md5 变化
	if _, err := zone.Assign("prod", "lobby-1", "area1", "zoneB", "admin", "", ""); err != nil {
		t.Fatalf("改派失败: %v", err)
	}
	valB, md5B := zoneval("lobby-1")
	if valB != "B" {
		t.Fatalf("改派 zoneB 后应重算为 B，实际 %s", valB)
	}
	if md5A == md5B {
		t.Fatal("改派后整体 md5 应变化")
	}
}

// TestAssignRejectsBungeeAllowsBukkit 全路径验证 zone 指派的角色守卫（FR-8/FR-35）：
// 注册表中的 bukkit 子服放行（成功落库），bungee 代理拒绝（返回 ErrZoneNotAssignableToBC）。
func TestAssignRejectsBungeeAllowsBukkit(t *testing.T) {
	db := testDB(t)
	ar := repository.NewAuditLogRepository(db)
	asg := repository.NewZoneAssignmentRepository(db)
	reg := runtime.NewRegistry()
	zone := service.NewZoneService(db, asg, repository.NewZoneDefaultEntryRepository(db), ar, reg)

	now := time.Now().UTC()
	register := func(serverID, role, addr string) {
		if _, err := reg.Register(&runtime.Instance{
			Namespace: "prod", ServerID: serverID, Role: role, Address: addr,
		}, 30*time.Second, now); err != nil {
			t.Fatalf("注册 %s 失败: %v", serverID, err)
		}
	}
	register("lobby-1", "bukkit", "10.0.0.1:25565")
	register("bc-1", "bungee", "10.0.0.9:25577")

	// bukkit 放行：指派成功
	if _, err := zone.Assign("prod", "lobby-1", "area1", "zoneA", "admin", "", ""); err != nil {
		t.Fatalf("bukkit 子服指派应成功，实际 %v", err)
	}

	// bungee 拒绝：返回 ErrZoneNotAssignableToBC
	if _, err := zone.Assign("prod", "bc-1", "area1", "zoneA", "admin", "", ""); !errors.Is(err, apperr.ErrZoneNotAssignableToBC) {
		t.Fatalf("bungee 代理指派应返回 ErrZoneNotAssignableToBC，实际 %v", err)
	}
}
